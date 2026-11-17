package dockerhost

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// failRename runs commands locally, with the final rename of a write failing.
type failRename struct{}

func (failRename) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	return remote.Local{}.Run(ctx, strings.Replace(cmd, "mv -f", "false", 1), stdin)
}

// The copy is replaced by rename: a write that fails before it leaves the old file and no
// temporary one; a symlink or a directory in its place is refused, never followed.
func TestTLSChainFile(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "kypost", "tls", "fullchain.pem")
	if b, err := ReadTLSChain(ctx, remote.Local{}, root, "kypost"); err != nil || b != nil {
		t.Fatalf("absent: %q %v", b, err)
	}
	if err := WriteTLSChain(ctx, remote.Local{}, root, "kypost", []byte("old")); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadTLSChain(ctx, remote.Local{}, root, "kypost"); err != nil || string(b) != "old" {
		t.Fatalf("read: %q %v", b, err)
	}
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if err := WriteTLSChain(ctx, failRename{}, root, "kypost", []byte("new")); err == nil {
		t.Fatal("failed write reported success")
	}
	left, _ := filepath.Glob(filepath.Join(root, "kypost", "tls", ".kyq.*"))
	if b, _ := os.ReadFile(file); string(b) != "old" || len(left) != 0 {
		t.Fatalf("after a failed write: %q, left %v", b, left)
	}
	big := bytes.Repeat([]byte("x"), MaxTLSChain+10)
	if err := WriteTLSChain(ctx, remote.Local{}, root, "kypost", big); err != nil {
		t.Fatal(err)
	}
	if b, _ := ReadTLSChain(ctx, remote.Local{}, root, "kypost"); len(b) != MaxTLSChain+1 {
		t.Fatalf("read %d bytes", len(b))
	}

	target := filepath.Join(root, "elsewhere")
	os.WriteFile(target, []byte("not ours"), 0o600)
	os.Remove(file)
	if err := os.Symlink(target, file); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTLSChain(ctx, remote.Local{}, root, "kypost"); err == nil {
		t.Fatal("symlink read")
	}
	os.Remove(file)
	os.Mkdir(file, 0o700)
	if _, err := ReadTLSChain(ctx, remote.Local{}, root, "kypost"); err == nil {
		t.Fatal("directory read")
	}
	// A directory that is not the target user's own and closed is refused before writing.
	os.Remove(file)
	os.Chmod(filepath.Dir(file), 0o777)
	if err := WriteTLSChain(ctx, remote.Local{}, root, "kypost", []byte("new")); err == nil {
		t.Fatal("wrote into a world-writable directory")
	}
}
