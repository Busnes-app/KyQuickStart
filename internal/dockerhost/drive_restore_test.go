package dockerhost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/remote"
)

const driveCaps = `{"schema":"ky.setup-capabilities/1","service":"kydrive","bundleSchema":"ky.product-setup/1","features":["restore-reset"]}`

func TestDriveRestoreNativeCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		fail       bool
	}{
		{"supported", driveCaps, false},
		{"old image", strings.ReplaceAll(driveCaps, `["restore-reset"]`, `[]`), true},
		{"wrong service", strings.ReplaceAll(driveCaps, `"kydrive"`, `"another"`), true},
		{"wrong schema", strings.ReplaceAll(driveCaps, "ky.setup-capabilities/1", "ky.setup-capabilities/2"), true},
		{"wrong bundle", strings.ReplaceAll(driveCaps, "ky.product-setup/1", "ky.product-setup/2"), true},
		{"malformed", `{`, true}, {"oversize", driveCaps + strings.Repeat(" ", 128<<10), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{reply: func(string) ([]byte, error) { return []byte(tc.body), nil }}
			st := Store{App: App{Name: "kydrive", Root: "/owned", Runner: f, Catalog: catalog.App{Manifest: catalog.Manifest{Images: map[string]string{"kydrive": "ghcr.io/busnes-app/kydrive-server:0.4.2@sha256:" + strings.Repeat("a", 64)}}}}}
			if err := st.CheckDriveRestoreReset(context.Background()); (err != nil) != tc.fail {
				t.Fatalf("failure=%v: %v", tc.fail, err)
			}
			if len(f.calls) != 1 || strings.Contains(f.calls[0].cmd, " -v ") || !strings.Contains(f.calls[0].cmd, " --network none ") {
				t.Fatalf("capability probe crossed data/network boundary: %+v", f.calls)
			}
			st.App.Catalog.Images["kydrive"] = "ghcr.io/busnes-app/kydrive-server:latest"
			f.calls = nil
			if err := st.CheckDriveRestoreReset(context.Background()); err == nil || len(f.calls) != 0 {
				t.Fatal("unpinned image executed", err)
			}
		})
	}
}

func TestDriveRestoreResetRunsOfflineOnOnlyItsData(t *testing.T) {
	f := &fakeRunner{reply: func(cmd string) ([]byte, error) {
		if strings.Contains(cmd, "'apply-setup' '--capabilities'") {
			return []byte(driveCaps), nil
		}
		return nil, nil
	}}
	st := Store{App: App{Name: "kydrive", Root: "/owned/path with ' quote", Runner: f, Catalog: catalog.App{Manifest: catalog.Manifest{Images: map[string]string{"kydrive": "ghcr.io/busnes-app/kydrive-server:0.4.2@sha256:" + strings.Repeat("a", 64)}}}}}
	if err := st.ResetDriveRestore(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.Contains(c.cmd, "docker compose") {
			t.Fatal("fresh target without running containers needs no Compose files", c.cmd)
		}
	}
	cmd := f.calls[len(f.calls)-1].cmd
	for _, want := range []string{`--user "$(id -u):$(id -g)"`, " --read-only --cap-drop ALL --security-opt no-new-privileges --network none ", " --memory 1g --memory-swap 1g --cpus 1 --pids-limit 128 ", " --entrypoint /app/kydrive-server ", remote.Quote("/owned/path with ' quote/kydrive/data:/restore/data:rw"), "'restore-reset' '-to' '/restore'"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("native reset lacks %q: %s", want, cmd)
		}
	}
	if strings.Count(cmd, " -v ") != 1 || strings.Contains(cmd, " -e ") || strings.Contains(cmd, " -p ") {
		t.Fatal("native reset gained mounts,credentials or publication", cmd)
	}
	f.reply = func(string) ([]byte, error) { return nil, errors.New("stop failed") }
	f.calls = nil
	if err := st.ResetDriveRestore(context.Background()); err == nil || len(f.calls) != 1 {
		t.Fatal("failed stop continued resetting", err, f.calls)
	}
}
