package dockerhost

import (
	"context"
	"path/filepath"
	"testing"
)

type call struct{ cmd, stdin string }

// fakeRunner records commands and answers through reply.
type fakeRunner struct {
	calls []call
	reply func(cmd string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, cmd string, stdin []byte) ([]byte, error) {
	f.calls = append(f.calls, call{cmd, string(stdin)})
	if f.reply == nil {
		return nil, nil
	}
	return f.reply(cmd)
}

// realTemp is t.TempDir() with symlinks resolved: trustedDir refuses symlinked paths, and
// macOS keeps temp dirs under the /var -> /private/var link.
func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}
