package dockerhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

func TestTrustedDir(t *testing.T) {
	ctx := context.Background()
	mk := func(t *testing.T, p string, mode os.FileMode) string {
		t.Helper()
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("fresh dir is created private", func(t *testing.T) {
		d := filepath.Join(t.TempDir(), "new")
		if err := trustedDir(ctx, remote.Local{}, d); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(d)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("stat = %v, %v", fi, err)
		}
	})
	t.Run("world-writable dir fails", func(t *testing.T) {
		d := mk(t, filepath.Join(t.TempDir(), "d"), 0o777)
		if err := trustedDir(ctx, remote.Local{}, d); err == nil {
			t.Fatal("accepted 0777")
		}
	})
	t.Run("symlink fails", func(t *testing.T) {
		base := t.TempDir()
		target := mk(t, filepath.Join(base, "real"), 0o700)
		link := filepath.Join(base, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := trustedDir(ctx, remote.Local{}, link); err == nil {
			t.Fatal("accepted symlink")
		}
	})
	t.Run("writable parent without sticky fails", func(t *testing.T) {
		parent := mk(t, filepath.Join(t.TempDir(), "p"), 0o777)
		if err := trustedDir(ctx, remote.Local{}, filepath.Join(parent, "d")); err == nil {
			t.Fatal("accepted 0777 parent")
		}
	})
	t.Run("sticky parent passes", func(t *testing.T) {
		parent := mk(t, filepath.Join(t.TempDir(), "p"), 0o777|os.ModeSticky)
		if err := trustedDir(ctx, remote.Local{}, filepath.Join(parent, "d")); err != nil {
			t.Fatal(err)
		}
	})
}
