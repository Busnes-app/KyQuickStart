package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/Busnes-app/ky-primitives/keyfile"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

type SSHConfig struct {
	Name       string // target name, used in messages and --trust-host-key
	Host       string
	Port       int
	User       string
	KnownHosts string                              // pinned host keys
	Trust      string                              // "SHA256:..." accepted for an unknown host
	Confirm    func(name, fingerprint string) bool // nil when not interactive
	KeyFile    string                              // private key file; empty uses the SSH agent
}

// SSH runs commands on one target over one connection.
type SSH struct {
	client *ssh.Client
	agent  net.Conn // nil with a key file
}

// maxKeyFile bounds a private key file; an ed25519 OpenSSH key is about 400 bytes.
const maxKeyFile = 16 << 10

const dialTimeout = 15 * time.Second

func Dial(ctx context.Context, c SSHConfig) (*SSH, error) {
	var conn net.Conn
	if confirm := c.Confirm; confirm != nil {
		// The prompt waits outside the handshake deadline; sshd's LoginGraceTime still bounds it.
		c.Confirm = func(name, fp string) bool {
			conn.SetDeadline(time.Time{})
			defer conn.SetDeadline(time.Now().Add(dialTimeout))
			return confirm(name, fp)
		}
	}
	hk, err := hostKeyCallback(c)
	if err != nil {
		return nil, err
	}
	auth, ac, err := authMethod(ctx, c.KeyFile)
	if err != nil {
		return nil, err
	}
	closeAgent := func() {
		if ac != nil {
			ac.Close()
		}
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	conn, err = (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		closeAgent()
		return nil, fmt.Errorf("%s: %w", c.Name, err)
	}
	conn.SetDeadline(time.Now().Add(dialTimeout))
	algos, err := hostKeyAlgorithms(c.KnownHosts, addr)
	if err != nil {
		conn.Close()
		closeAgent()
		return nil, err
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:              c.User,
		Auth:              []ssh.AuthMethod{auth},
		HostKeyCallback:   hk,
		HostKeyAlgorithms: algos,
	})
	if err != nil {
		conn.Close()
		closeAgent()
		return nil, fmt.Errorf("%s: ssh: %w", c.Name, err)
	}
	conn.SetDeadline(time.Time{})
	return &SSH{client: ssh.NewClient(sc, chans, reqs), agent: ac}, nil
}

// authMethod signs with keyFile, a private file of this user, or else with the SSH agent,
// whose connection it returns.
func authMethod(ctx context.Context, keyFile string) (ssh.AuthMethod, net.Conn, error) {
	if keyFile != "" {
		b, err := keyfile.ReadPrivate(keyFile, maxKeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("ssh key %s: %w", keyFile, err)
		}
		signer, err := ssh.ParsePrivateKey(b)
		clear(b)
		if err != nil {
			return nil, nil, fmt.Errorf("ssh key %s: %w", keyFile, err)
		}
		return ssh.PublicKeys(signer), nil, nil
	}
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, nil, errors.New("SSH_AUTH_SOCK is not set: start an SSH agent and add your key")
	}
	ac, err := (&net.Dialer{}).DialContext(ctx, "unix", sock)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh agent: %w", err)
	}
	return ssh.PublicKeysCallback(agent.NewClient(ac).Signers), ac, nil
}

func (s *SSH) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	sess, err := s.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	var out, errb bytes.Buffer
	sess.Stdin, sess.Stdout, sess.Stderr = bytes.NewReader(stdin), &out, &errb
	done := make(chan error, 1)
	go func() { done <- sess.Run(wrap(cmd)) }()
	select {
	case <-ctx.Done():
		sess.Close()
		<-done
		return out.Bytes(), ctx.Err()
	case err = <-done:
	}
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		return out.Bytes(), &ExitError{Code: ee.ExitStatus(), Stderr: errb.Bytes()}
	}
	return out.Bytes(), err
}

// wrap hands cmd to sh through the login shell. The login shell may be fish or another
// non-POSIX shell, so the line holds only characters every shell reads the same.
func wrap(cmd string) string {
	return `sh -c 'eval "$(echo ` + base64.StdEncoding.EncodeToString([]byte(cmd)) + ` | base64 -d)"'`
}

func (s *SSH) Close() error {
	err := s.client.Close()
	if s.agent != nil {
		err = errors.Join(err, s.agent.Close())
	}
	return err
}

// firstContactAlgorithms prefers ed25519, the key `ssh-keyscan -t ed25519` and ssh-keygen pin;
// Go's default would ask a stock server for its ECDSA key and miss a fingerprint taken that way.
var firstContactAlgorithms = []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}

// hostKeyAlgorithms asks a pinned host only for the key types pinned for it, so the server
// presents a key the pin can match, and an unpinned host for firstContactAlgorithms.
func hostKeyAlgorithms(knownHosts, addr string) ([]string, error) {
	b, err := os.ReadFile(knownHosts)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	host := knownhosts.Normalize(addr)
	var algos []string
	for _, line := range bytes.Split(b, []byte("\n")) {
		f := bytes.Fields(line)
		if len(f) < 2 || string(f[0]) != host {
			continue
		}
		switch t := string(f[1]); t {
		case ssh.KeyAlgoRSA:
			algos = append(algos, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256)
		default:
			algos = append(algos, t)
		}
	}
	if len(algos) == 0 {
		return firstContactAlgorithms, nil
	}
	return algos, nil
}

// hostKeyCallback accepts a pinned key, pins an unknown key only when trusted by flag or
// confirmed by the operator, and always refuses a changed key.
func hostKeyCallback(c SSHConfig) (ssh.HostKeyCallback, error) {
	f, err := os.OpenFile(c.KnownHosts, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	known, err := knownhosts.New(c.KnownHosts)
	if err != nil {
		return nil, err
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := known(hostname, remote, key)
		var ke *knownhosts.KeyError
		if !errors.As(err, &ke) {
			return err
		}
		fp := ssh.FingerprintSHA256(key)
		if len(ke.Want) > 0 {
			return fmt.Errorf("%s: host key changed: got %s, pinned %s in %s; a reinstall or an interception, so check with the host's owner before editing that file",
				c.Name, fp, ssh.FingerprintSHA256(ke.Want[0].Key), c.KnownHosts)
		}
		if fp != c.Trust && (c.Confirm == nil || !c.Confirm(c.Name, fp)) {
			return fmt.Errorf("%s: unknown host key %s; compare it with `ssh-keygen -lf` on the host, then confirm interactively or pass --trust-host-key %s=<fingerprint>",
				c.Name, fp, c.Name)
		}
		f, err := os.OpenFile(c.KnownHosts, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key))
		return errors.Join(err, f.Close())
	}, nil
}
