package dockerhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

const (
	keyA = "restrict ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAA kyquickstart-runner 0b6c1f3e-2a1d-4f6e-9c3b-5d7e8f9a0b1c"
	keyB = "restrict ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBBB kyquickstart-runner 9b6c1f3e-2a1d-4f6e-9c3b-5d7e8f9a0b1c"
)

func TestAuthorizeRunnerKeepsOtherLines(t *testing.T) {
	ctx := context.Background()
	home := realTemp(t)
	t.Setenv("HOME", home)
	keys := filepath.Join(home, ".ssh", "authorized_keys")
	read := func() string { b, _ := os.ReadFile(keys); return string(b) }

	// No ~/.ssh yet: created private.
	if err := AuthorizeRunner(ctx, remote.Local{}, keyA); err != nil {
		t.Fatal(err)
	}
	if read() != keyA+"\n" {
		t.Fatalf("keys = %q", read())
	}
	if fi, _ := os.Stat(filepath.Dir(keys)); fi.Mode().Perm() != 0o700 {
		t.Errorf(".ssh mode %v", fi.Mode())
	}

	operator := "ssh-ed25519 AAAAoperator me@laptop\n# a comment\ncommand=\"x\" ssh-rsa AAAAother kyquickstart-runner-notes\n"
	os.WriteFile(keys, []byte(operator+keyA+"\n"), 0o644)
	if err := AuthorizeRunner(ctx, remote.Local{}, keyB); err != nil {
		t.Fatal(err)
	}
	if read() != operator+keyB+"\n" {
		t.Fatalf("replace: %q", read())
	}
	if fi, _ := os.Stat(keys); fi.Mode().Perm() != 0o600 {
		t.Errorf("keys mode %v", fi.Mode())
	}
	// A rerun changes nothing; revoking keeps everything else.
	if err := AuthorizeRunner(ctx, remote.Local{}, keyB); err != nil || read() != operator+keyB+"\n" {
		t.Fatalf("rerun: %v %q", err, read())
	}
	if err := AuthorizeRunner(ctx, remote.Local{}, ""); err != nil || read() != operator {
		t.Fatalf("revoke: %v %q", err, read())
	}
	if err := AuthorizeRunner(ctx, remote.Local{}, ""); err != nil || read() != operator {
		t.Fatalf("second revoke: %v %q", err, read())
	}
	if left, _ := filepath.Glob(filepath.Join(home, ".ssh", ".authorized_keys.*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestAuthorizeRunnerRefusesUnsafeFiles(t *testing.T) {
	ctx := context.Background()
	home := realTemp(t)
	t.Setenv("HOME", home)
	ssh := filepath.Join(home, ".ssh")
	os.Mkdir(ssh, 0o700)
	elsewhere := filepath.Join(home, "elsewhere")
	os.WriteFile(elsewhere, []byte("keep\n"), 0o600)

	os.Symlink(elsewhere, filepath.Join(ssh, "authorized_keys"))
	if err := AuthorizeRunner(ctx, remote.Local{}, keyA); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked file: %v", err)
	}
	if b, _ := os.ReadFile(elsewhere); string(b) != "keep\n" {
		t.Fatal("followed the symlink")
	}
	os.Remove(filepath.Join(ssh, "authorized_keys"))
	os.WriteFile(filepath.Join(ssh, "authorized_keys"), nil, 0o666)
	os.Chmod(filepath.Join(ssh, "authorized_keys"), 0o666)
	if err := AuthorizeRunner(ctx, remote.Local{}, keyA); err == nil {
		t.Fatal("accepted a world-writable file")
	}

	os.RemoveAll(ssh)
	os.Symlink(home, ssh)
	if err := AuthorizeRunner(ctx, remote.Local{}, keyA); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked directory: %v", err)
	}
	for _, bad := range []string{"ssh-ed25519 AAAA kyquickstart-runner 0b6c1f3e-2a1d-4f6e-9c3b-5d7e8f9a0b1c", keyA + "\nssh-rsa AAAA evil", "restrict ssh-ed25519 AAAA other"} {
		if err := AuthorizeRunner(ctx, remote.Local{}, bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
