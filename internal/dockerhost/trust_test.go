package dockerhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
		d := filepath.Join(realTemp(t), "new")
		if err := trustedDir(ctx, remote.Local{}, d); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(d)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("stat = %v, %v", fi, err)
		}
	})
	t.Run("world-writable dir fails", func(t *testing.T) {
		d := mk(t, filepath.Join(realTemp(t), "d"), 0o777)
		if err := trustedDir(ctx, remote.Local{}, d); err == nil {
			t.Fatal("accepted 0777")
		}
	})
	t.Run("symlink fails", func(t *testing.T) {
		base := realTemp(t)
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
		parent := mk(t, filepath.Join(realTemp(t), "p"), 0o777)
		if err := trustedDir(ctx, remote.Local{}, filepath.Join(parent, "d")); err == nil {
			t.Fatal("accepted 0777 parent")
		}
	})
	// The symlink's target is safe, but anyone can re-point the link: its directory is open.
	t.Run("symlinked ancestor in a writable dir fails", func(t *testing.T) {
		base := realTemp(t)
		safe := mk(t, filepath.Join(base, "safe"), 0o700)
		open := mk(t, filepath.Join(base, "open"), 0o777)
		if err := os.Symlink(safe, filepath.Join(open, "link")); err != nil {
			t.Fatal(err)
		}
		if err := trustedDir(ctx, remote.Local{}, filepath.Join(open, "link", "d")); err == nil {
			t.Fatal("accepted a path through a link others can replace")
		}
	})
	// Two hops: the middle link's directory is on neither the given nor the resolved path, so
	// any symlink in the path is refused and the error names the path to use instead.
	t.Run("multi-hop symlinked ancestor fails and names the real path", func(t *testing.T) {
		base := realTemp(t)
		safe := mk(t, filepath.Join(base, "safe"), 0o700)
		mid := mk(t, filepath.Join(base, "mid"), 0o700)
		if err := os.Symlink(safe, filepath.Join(mid, "hop")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(mid, "hop"), filepath.Join(base, "link")); err != nil {
			t.Fatal(err)
		}
		err := trustedDir(ctx, remote.Local{}, filepath.Join(base, "link", "d"))
		if err == nil || !strings.Contains(err.Error(), filepath.Join(safe, "d")) {
			t.Fatalf("err = %v, want refusal naming %s", err, filepath.Join(safe, "d"))
		}
	})
	t.Run("sticky parent passes", func(t *testing.T) {
		parent := mk(t, filepath.Join(realTemp(t), "p"), 0o777|os.ModeSticky)
		if err := trustedDir(ctx, remote.Local{}, filepath.Join(parent, "d")); err != nil {
			t.Fatal(err)
		}
	})
}
