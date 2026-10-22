package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

var addr = &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 22}

func hostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s.PublicKey()
}

func check(t *testing.T, c SSHConfig, key ssh.PublicKey) error {
	t.Helper()
	cb, err := hostKeyCallback(c)
	if err != nil {
		t.Fatal(err)
	}
	return cb("nas.example.lan:22", addr, key)
}

func TestUnknownKeyNeedsTrustOrConfirm(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	key := hostKey(t)
	fp := ssh.FingerprintSHA256(key)
	base := SSHConfig{Name: "nas", KnownHosts: kh}

	if err := check(t, base, key); err == nil || !strings.Contains(err.Error(), "unknown host key "+fp) {
		t.Fatalf("no trust: %v", err)
	}
	wrong := base
	wrong.Trust = ssh.FingerprintSHA256(hostKey(t))
	if err := check(t, wrong, key); err == nil {
		t.Fatal("accepted with a different trusted fingerprint")
	}
	no := base
	no.Confirm = func(string, string) bool { return false }
	if err := check(t, no, key); err == nil {
		t.Fatal("accepted after the operator said no")
	}
	if b, _ := os.ReadFile(kh); len(b) != 0 {
		t.Fatalf("refused key was pinned: %s", b)
	}

	trusted := base
	trusted.Trust = fp
	if err := check(t, trusted, key); err != nil {
		t.Fatal(err)
	}
	if err := check(t, base, key); err != nil {
		t.Fatalf("pinned key refused on the next run: %v", err)
	}
}

func TestConfirmPinsKey(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	key := hostKey(t)
	var asked string
	c := SSHConfig{Name: "nas", KnownHosts: kh, Confirm: func(name, fp string) bool { asked = name + " " + fp; return true }}
	if err := check(t, c, key); err != nil {
		t.Fatal(err)
	}
	if asked != "nas "+ssh.FingerprintSHA256(key) {
		t.Errorf("asked %q", asked)
	}
	if err := check(t, SSHConfig{Name: "nas", KnownHosts: kh}, key); err != nil {
		t.Fatal(err)
	}
}

func TestChangedHostKeyIsRefused(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	old, replaced := hostKey(t), hostKey(t)
	if err := check(t, SSHConfig{Name: "nas", KnownHosts: kh, Trust: ssh.FingerprintSHA256(old)}, old); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(kh)
	c := SSHConfig{
		Name:       "nas",
		KnownHosts: kh,
		Trust:      ssh.FingerprintSHA256(replaced),
		Confirm:    func(string, string) bool { return true },
	}
	err := check(t, c, replaced)
	if err == nil || !strings.Contains(err.Error(), "host key changed") {
		t.Fatalf("err = %v", err)
	}
	if after, _ := os.ReadFile(kh); string(after) != string(before) {
		t.Error("known_hosts changed")
	}
}

func TestWrapSurvivesLoginShells(t *testing.T) {
	const cmd = `printf '%s|' 'a\b' "it's" '$HOME' 'x\'; cat`
	const want = `a\b|it's|$HOME|x\|in`
	for _, shell := range []string{"sh", "bash", "fish"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			t.Logf("%s not installed, skipping", shell)
			continue
		}
		c := exec.Command(path, "-c", wrap(cmd))
		c.Stdin = strings.NewReader("in")
		out, err := c.Output()
		if err != nil {
			t.Fatalf("%s: %v", shell, err)
		}
		if string(out) != want {
			t.Errorf("%s: got %q, want %q", shell, out, want)
		}
	}
}

// serveSSH accepts one client key on a loopback port and answers every exec with "ran".
func serveSSH(t *testing.T, client ssh.PublicKey, extra ...ssh.Signer) (port int, hostFP string) {
	t.Helper()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	host, _ := ssh.NewSignerFromKey(hostPriv)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if string(k.Marshal()) != string(client.Marshal()) {
			return nil, errors.New("unknown key")
		}
		return nil, nil
	}}
	cfg.AddHostKey(host)
	for _, k := range extra {
		cfg.AddHostKey(k)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, in, _ := nc.Accept()
					go func() {
						for r := range in {
							r.Reply(r.Type == "exec", nil)
							if r.Type == "exec" {
								ch.Write([]byte("ran"))
								ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
								ch.Close()
							}
						}
					}()
				}
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, ssh.FingerprintSHA256(host.PublicKey())
}

func TestDialWithKeyFile(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	os.WriteFile(key, pem.EncodeToMemory(block), 0o600)
	port, fp := serveSSH(t, signer.PublicKey())
	c := SSHConfig{Name: "nas", Host: "127.0.0.1", Port: port, User: "kyq", KnownHosts: filepath.Join(dir, "known_hosts"), Trust: fp, KeyFile: key}

	s, err := Dial(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Run(context.Background(), "true", nil)
	s.Close()
	if err != nil || string(out) != "ran" {
		t.Fatalf("run: %q %v", out, err)
	}
	// A key file others can read is refused before connecting.
	os.Chmod(key, 0o644)
	if _, err := Dial(context.Background(), c); err == nil || !strings.Contains(err.Error(), "ssh key") {
		t.Fatalf("readable key: %v", err)
	}
}

// A stock server also holds an ECDSA key, which Go would ask for first. An unpinned host is
// asked for ed25519, the key an operator's ssh-keyscan -t ed25519 fingerprint names; a pinned
// host only for the pinned type, so earlier ECDSA pins keep working.
func TestHostKeyAlgorithmMatchesThePin(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	os.WriteFile(key, pem.EncodeToMemory(block), 0o600)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ecSigner, _ := ssh.NewSignerFromKey(ec)
	port, fp := serveSSH(t, signer.PublicKey(), ecSigner)
	dial := func(kh, trust string) error {
		s, err := Dial(context.Background(), SSHConfig{Name: "nas", Host: "127.0.0.1", Port: port, User: "kyq", KnownHosts: kh, Trust: trust, KeyFile: key})
		if err == nil {
			s.Close()
		}
		return err
	}
	kh := filepath.Join(dir, "known_hosts")
	if err := dial(kh, fp); err != nil {
		t.Fatalf("first contact with the ed25519 fingerprint: %v", err)
	}
	if err := dial(kh, ""); err != nil {
		t.Fatalf("pinned ed25519: %v", err)
	}
	old := filepath.Join(dir, "known_hosts.ecdsa")
	os.WriteFile(old, []byte(knownhosts.Line([]string{knownhosts.Normalize(net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))}, ecSigner.PublicKey())+"\n"), 0o600)
	if err := dial(old, ""); err != nil {
		t.Fatalf("pinned ECDSA: %v", err)
	}
}
