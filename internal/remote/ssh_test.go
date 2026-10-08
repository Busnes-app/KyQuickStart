package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
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
