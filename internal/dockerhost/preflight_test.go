package dockerhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

func versions(engine, compose string) *fakeRunner {
	return &fakeRunner{reply: func(cmd string) ([]byte, error) {
		switch {
		case strings.HasPrefix(cmd, "docker version"):
			return []byte(engine + "\n"), nil
		case strings.HasPrefix(cmd, "docker compose version"):
			return []byte(compose + "\n"), nil
		}
		return []byte("/opt\n"), nil
	}}
}

func TestPreflightVersions(t *testing.T) {
	cases := map[string]struct {
		engine, compose string
		ok              [2]bool
	}{
		"current":        {"29.8.2", "5.1.3", [2]bool{true, true}},
		"floors":         {"24.0.0", "v2.21.0", [2]bool{true, true}},
		"old engine":     {"23.0.6", "2.29.1", [2]bool{false, true}},
		"old compose":    {"27.1.1", "2.20.3", [2]bool{true, false}},
		"desktop suffix": {"27.1.1", "2.29.1-desktop.1", [2]bool{true, true}},
		"garbage":        {"", "nope", [2]bool{false, false}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := Preflight(context.Background(), versions(c.engine, c.compose), stack.Target{Root: "/opt/kyq"})
			if got[0].OK != c.ok[0] || got[1].OK != c.ok[1] {
				t.Errorf("findings = %+v", got)
			}
		})
	}
}

func TestPreflightDockerUnreachable(t *testing.T) {
	f := &fakeRunner{reply: func(cmd string) ([]byte, error) {
		return nil, &remote.ExitError{Code: 1, Stderr: []byte("permission denied while trying to connect to the Docker daemon socket")}
	}}
	got := Preflight(context.Background(), f, stack.Target{Root: "/opt/kyq"})
	if got[0].OK || !strings.Contains(got[0].Detail, "permission denied") {
		t.Errorf("finding = %+v", got[0])
	}
}

func rootFinding(t *testing.T, root string) Finding {
	t.Helper()
	for _, f := range Preflight(context.Background(), remote.Local{}, stack.Target{Root: root}) {
		if f.Check == "root" {
			return f
		}
	}
	t.Fatal("no root finding")
	return Finding{}
}

func TestPreflightRoot(t *testing.T) {
	dir := t.TempDir()
	if f := rootFinding(t, filepath.Join(dir, "a", "b")); !f.OK {
		t.Errorf("nested under writable dir: %+v", f)
	}
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o600)
	if f := rootFinding(t, filepath.Join(file, "b")); f.OK {
		t.Errorf("under a file: %+v", f)
	}
	if os.Geteuid() != 0 {
		ro := filepath.Join(dir, "ro")
		os.Mkdir(ro, 0o500)
		if f := rootFinding(t, filepath.Join(ro, "b")); f.OK {
			t.Errorf("under read-only dir: %+v", f)
		}
	}
}
