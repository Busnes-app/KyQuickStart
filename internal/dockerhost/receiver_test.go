package dockerhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// ReceiverChecks run for real: only the pinned executable, never through a symlink or
// writable by others, and a TLS directory the container's uid 1000 can read.
func TestReceiverChecks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Docker targets are Linux hosts; the checks use GNU or BusyBox stat")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "maddy")
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	sum := sha256.Sum256([]byte("#!/bin/sh\n"))
	pin := hex.EncodeToString(sum[:])
	tls := filepath.Join(dir, "tls")
	os.Mkdir(tls, 0o755)
	os.WriteFile(filepath.Join(tls, "fullchain.pem"), []byte("cert"), 0o644)
	os.WriteFile(filepath.Join(tls, "privkey.pem"), []byte("key"), 0o600)
	rc := stack.MailReceiver{Hostname: "mx.example.com", SMTPAddress: "192.0.2.77", MaddyBinary: bin, TLSDir: tls, PublishPort25: true}
	check := func(rc stack.MailReceiver, pin string) map[string]bool {
		t.Helper()
		out := map[string]bool{}
		for _, f := range ReceiverChecks(context.Background(), remote.Local{}, "kypost", rc, pin) {
			out[f.Check] = f.OK
		}
		return out
	}
	got := check(rc, pin)
	if !got["maddy binary"] || got["smtp address"] {
		t.Fatalf("findings = %v (192.0.2.77 is no address of this host)", got)
	}
	// Only uid 1000 owns the key as KyPost's container user does.
	if want := os.Getuid() == 1000; got["receiver tls"] != want {
		t.Errorf("receiver tls = %v, want %v for uid %d", got["receiver tls"], want, os.Getuid())
	}
	if want := runtime.GOARCH == "amd64"; got["mail receiver"] != want {
		t.Errorf("mail receiver = %v on %s", got["mail receiver"], runtime.GOARCH)
	}
	if check(rc, "0"+pin[1:])["maddy binary"] {
		t.Error("another hash accepted")
	}
	link := filepath.Join(dir, "link")
	os.Symlink(bin, link)
	if check(stack.MailReceiver{MaddyBinary: link, TLSDir: tls, SMTPAddress: "192.0.2.77"}, pin)["maddy binary"] {
		t.Error("symlinked binary accepted")
	}
	os.Chmod(bin, 0o775)
	if check(rc, pin)["maddy binary"] {
		t.Error("group-writable binary accepted")
	}
	os.Chmod(bin, 0o755)
	os.Chmod(filepath.Join(tls, "privkey.pem"), 0o640)
	if check(rc, pin)["receiver tls"] {
		t.Error("group-readable key accepted")
	}
	os.Chmod(filepath.Join(tls, "privkey.pem"), 0o600)
	os.Remove(filepath.Join(tls, "fullchain.pem"))
	os.Symlink(filepath.Join(dir, "maddy"), filepath.Join(tls, "fullchain.pem"))
	if check(rc, pin)["receiver tls"] {
		t.Error("symlinked certificate accepted")
	}
}
