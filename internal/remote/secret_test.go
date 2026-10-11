package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadSecretPermissionAndSymlinkBoundary(t *testing.T) {
	// macOS temp dirs sit under the /var symlink, which ReadSecret refuses.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "npm_token")
	if err := os.WriteFile(f, []byte("secret-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := ReadSecret(context.Background(), Local{}, f)
	if err != nil || string(b) != "secret-value" {
		t.Fatalf("read: %q %v", b, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(f, link); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadSecret(context.Background(), Local{}, link); err == nil || len(b) != 0 {
		t.Fatal("followed secret symlink")
	}
	if err := os.Chmod(f, 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadSecret(context.Background(), Local{}, f); err == nil || len(b) != 0 {
		t.Fatal("read world-readable secret")
	}
}
