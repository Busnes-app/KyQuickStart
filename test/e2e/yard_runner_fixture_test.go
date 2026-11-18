//go:build e2e

package e2e

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// yardRunnerSource copies only Docker build inputs. Relay patches must be embedded
// before compiling; the runner never reads the workstation's copied catalog.
func yardRunnerSource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"cmd", "internal"} {
		if err := os.CopyFS(filepath.Join(root, dir), os.DirFS("../../"+dir)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"go.mod", "go.sum", "Dockerfile", ".dockerignore"} {
		b, err := os.ReadFile("../../" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for app := range relayPatches {
		relaysOff(t, filepath.Join(root, "internal/catalog/apps", app))
	}
	return root
}

// yardCheckRunnerRelayCatalog inspects a stopped, networkless container before
// kind imports or starts the image. An unpatched cached binary fails closed.
func yardCheckRunnerRelayCatalog(t *testing.T, image string) {
	t.Helper()
	id := docker(t, "create", "--network", "none", image)
	defer func() { _ = exec.Command("docker", "rm", id).Run() }()
	path := filepath.Join(t.TempDir(), "kyquickstart")
	docker(t, "cp", id+":/usr/local/bin/kyquickstart", path)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const limit = 128 << 20
	binary, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(binary) > limit {
		t.Fatal("fixture runner binary unreadable or oversized")
	}
	catalogDir := relaysOffCatalog(t)
	for app, patches := range relayPatches {
		for _, p := range patches {
			expected, err := os.ReadFile(filepath.Join(catalogDir, app, p.file))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(binary, expected) {
				t.Fatalf("fixture runner lacks exact relay-isolated embedded catalog: %s/%s; rebuild from yardRunnerSource", app, p.file)
			}
		}
	}
	t.Log("fixture runner embedded relay-isolated catalog verified before workload startup")
}

// TestYardRunnerRelayCatalog permits a bounded preflight of an actual cached image
// without starting any container or contacting a relay.
func TestYardRunnerRelayCatalog(t *testing.T) {
	image := os.Getenv("KYQ_TEST_YARD_FIXTURE_RUNNER_ID")
	if image == "" {
		t.Skip("explicit local fixture runner image required")
	}
	yardCheckRunnerRelayCatalog(t, image)
}

func TestYardRunnerSource(t *testing.T) {
	root := yardRunnerSource(t)
	want := relaysOffCatalog(t)
	for app, patches := range relayPatches {
		for _, p := range patches {
			actual, err := os.ReadFile(filepath.Join(root, "internal/catalog/apps", app, p.file))
			if err != nil {
				t.Fatal(err)
			}
			expected, err := os.ReadFile(filepath.Join(want, app, p.file))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actual, expected) {
				t.Fatalf("runner build input not relay-isolated: %s/%s", app, p.file)
			}
		}
	}
}
