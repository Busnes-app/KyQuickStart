package catalog

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteKyMessageCompose is scripts/test-postgres-17-to-18.sh's renderer, skipped unless
// KYQ_RENDER_TO names a directory. It writes kymessage's compose.yaml as deploy renders it,
// the catalog files that file binds, and backup.json (the manifest's backup and restore
// commands), from the apps directory KYQ_RENDER_APPS (the embedded catalog when unset).
func TestWriteKyMessageCompose(t *testing.T) {
	to := os.Getenv("KYQ_RENDER_TO")
	if to == "" {
		t.Skip("KYQ_RENDER_TO is not set")
	}
	var fsys fs.FS = Embedded()
	if d := os.Getenv("KYQ_RENDER_APPS"); d != "" {
		fsys = os.DirFS(d)
	}
	a, err := loadApp(fsys, "kymessage")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte, mode os.FileMode) {
		p := filepath.Join(to, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, mode); err != nil {
			t.Fatal(err)
		}
	}
	write("compose.yaml", a.WithRoute(messageRoute).WithPrivateRecovery(false).Compose, 0o600)
	for name, b := range a.Files {
		write(name, b, 0o644) // as deploy writes them
	}
	b, err := json.Marshal(map[string][]string{"command": a.Backup.Command, "restore": a.Backup.RestoreCommand})
	if err != nil {
		t.Fatal(err)
	}
	write("backup.json", b, 0o600)
}
