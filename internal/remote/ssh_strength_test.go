package remote

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Exercise Dial's actual handshake, pinning and authentication with synthetic keys.
func TestSSHStrengthPolicy(t *testing.T) {
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rsaKeys := make(map[int]*rsa.PrivateKey)
	for _, bits := range []int{1024, 2047, 2048} {
		rsaKeys[bits], err = rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, bits := range []int{1024, 2047, 2048} {
		for _, pinned := range []bool{false, true} {
			t.Run(fmt.Sprintf("host-RSA-%d/pinned-%t", bits, pinned), func(t *testing.T) {
				cfg, closeServer := strengthServer(t, rsaKeys[bits], nil)
				defer closeServer()
				c := strengthClient(t, cfg, ed, false)
				if pinned {
					signer, _ := ssh.NewSignerFromKey(rsaKeys[bits])
					line := knownhosts.Line([]string{knownhosts.Normalize(net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))}, signer.PublicKey()) + "\n"
					if err := os.WriteFile(c.KnownHosts, []byte(line), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				before, _ := os.ReadFile(c.KnownHosts)
				strengthDial(t, c, bits >= 2048)
				if bits < 2048 {
					after, _ := os.ReadFile(c.KnownHosts)
					if string(before) != string(after) {
						t.Fatal("refused host key changed the pin file")
					}
				}
			})
		}
		for _, useAgent := range []bool{false, true} {
			t.Run(fmt.Sprintf("client-RSA-%d/agent-%t", bits, useAgent), func(t *testing.T) {
				cfg, closeServer := strengthServer(t, ed, nil)
				defer closeServer()
				strengthDial(t, strengthClient(t, cfg, rsaKeys[bits], useAgent), bits >= 2048)
			})
		}
	}
	for _, tc := range []struct {
		name string
		set  func(*ssh.ServerConfig)
		ok   bool
	}{
		{"SHA1-key-exchange", func(c *ssh.ServerConfig) { c.KeyExchanges = []string{ssh.InsecureKeyExchangeDH14SHA1} }, false},
		{"truncated-96-bit-MAC", func(c *ssh.ServerConfig) { c.MACs = []string{ssh.InsecureHMACSHA196} }, false},
		{"full-HMAC-SHA1", func(c *ssh.ServerConfig) { c.MACs = []string{ssh.HMACSHA1} }, true},
		{"SHA256-key-exchange-and-MAC", func(c *ssh.ServerConfig) {
			c.KeyExchanges = []string{ssh.KeyExchangeDH14SHA256}
			c.MACs = []string{ssh.HMACSHA256}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, closeServer := strengthServer(t, ed, func(c *ssh.ServerConfig) {
				// Force a non-AEAD cipher so MAC negotiation is exercised.
				c.Ciphers = []string{ssh.CipherAES128CTR}
				tc.set(c)
			})
			defer closeServer()
			strengthDial(t, strengthClient(t, cfg, ed, false), tc.ok)
		})
	}
	for _, useAgent := range []bool{false, true} {
		t.Run(fmt.Sprintf("SHA1-client-signature/agent-%t", useAgent), func(t *testing.T) {
			cfg, closeServer := strengthServer(t, ed, func(c *ssh.ServerConfig) {
				c.PublicKeyAuthAlgorithms = []string{ssh.KeyAlgoRSA}
			})
			defer closeServer()
			strengthDial(t, strengthClient(t, cfg, rsaKeys[2048], useAgent), false)
		})
	}
}

func TestSSHAgentCertificatesAndMixedKeys(t *testing.T) {
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	strong, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		leaf, ca   crypto.PrivateKey
		sha1CA     bool
		sha1Server bool
		ok         bool
	}{
		{"strong-RSA", strong, ed, false, false, true},
		{"strong-ECDSA", ec, strong, false, false, true},
		{"short-leaf", weak, ed, false, false, false},
		{"short-authority", ed, weak, false, false, false},
		{"SHA1-authority", ed, strong, true, false, false},
		{"RSA-certificate-SHA1-auth", strong, ed, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leaf, err := ssh.NewSignerFromKey(tc.leaf)
			if err != nil {
				t.Fatal(err)
			}
			ca, err := ssh.NewSignerFromKey(tc.ca)
			if err != nil {
				t.Fatal(err)
			}
			if tc.sha1CA {
				ca, err = ssh.NewSignerWithAlgorithms(ca.(ssh.AlgorithmSigner), []string{ssh.KeyAlgoRSA})
				if err != nil {
					t.Fatal(err)
				}
			}
			cert := &ssh.Certificate{Key: leaf.PublicKey(), CertType: ssh.UserCert, ValidBefore: ssh.CertTimeInfinity}
			if err := cert.SignCert(rand.Reader, ca); err != nil {
				t.Fatal(err)
			}
			c, closeServer := strengthServer(t, ed, func(c *ssh.ServerConfig) {
				if tc.sha1Server {
					c.PublicKeyAuthAlgorithms = []string{ssh.KeyAlgoRSA}
				}
			})
			defer closeServer()
			c.KnownHosts = filepath.Join(t.TempDir(), "known_hosts")
			strengthAgent(t, agent.AddedKey{PrivateKey: tc.leaf, Certificate: cert})
			strengthDial(t, c, tc.ok)
		})
	}
	t.Run("mixed-agent-never-offers-short-key", func(t *testing.T) {
		var weakOffered atomic.Bool
		c, closeServer := strengthServer(t, ed, func(c *ssh.ServerConfig) {
			c.PublicKeyCallback = func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
				if key.Type() == ssh.KeyAlgoRSA {
					weakOffered.Store(true)
				}
				return nil, nil
			}
		})
		defer closeServer()
		c.KnownHosts = filepath.Join(t.TempDir(), "known_hosts")
		strengthAgent(t, agent.AddedKey{PrivateKey: weak}, agent.AddedKey{PrivateKey: ed})
		strengthDial(t, c, true)
		if weakOffered.Load() {
			t.Fatal("short agent key was offered despite a usable strong key")
		}
	})
}

// One connection per fixture, bounded even when algorithm negotiation is refused.
func strengthServer(t *testing.T, private crypto.PrivateKey, configure func(*ssh.ServerConfig)) (SSHConfig, func()) {
	t.Helper()
	host, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
		return nil, nil
	}}
	cfg.AddHostKey(host)
	if configure != nil {
		configure(cfg)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var once sync.Once
	cleanup := func() { once.Do(func() { close(stop); l.Close() }) }
	t.Cleanup(cleanup)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		_, _, _, err = ssh.NewServerConn(c, cfg)
		if err == nil {
			<-stop
		}
	}()
	return SSHConfig{Name: "fixture", Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, User: "fixture", Trust: ssh.FingerprintSHA256(host.PublicKey())}, cleanup
}

func strengthClient(t *testing.T, c SSHConfig, private crypto.PrivateKey, useAgent bool) SSHConfig {
	t.Helper()
	dir := t.TempDir()
	c.KnownHosts = filepath.Join(dir, "known_hosts")
	if useAgent {
		strengthAgent(t, agent.AddedKey{PrivateKey: private})
		return c
	}
	block, err := ssh.MarshalPrivateKey(private, "synthetic fixture")
	if err != nil {
		t.Fatal(err)
	}
	c.KeyFile = filepath.Join(dir, "key")
	if err := os.WriteFile(c.KeyFile, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return c
}

func strengthAgent(t *testing.T, keys ...agent.AddedKey) {
	t.Helper()
	kr := agent.NewKeyring()
	for _, key := range keys {
		if err := kr.Add(key); err != nil {
			t.Fatal(err)
		}
	}
	socketDir, err := os.MkdirTemp("", "ssh-agent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "a.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		ac, err := l.Accept()
		if err == nil {
			defer ac.Close()
			agent.ServeAgent(kr, ac)
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", socket)
}

func strengthDial(t *testing.T, c SSHConfig, wantOK bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, c)
	if client != nil {
		client.Close()
	}
	if (err == nil) != wantOK {
		t.Fatalf("connection success=%t, want %t: %v", err == nil, wantOK, err)
	}
	if err != nil && strings.Contains(err.Error(), "deadline") {
		t.Fatalf("policy test timed out instead of completing negotiation: %v", err)
	}
}
