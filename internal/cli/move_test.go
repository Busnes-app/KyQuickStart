package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/suite"
)

// An upgrade.move (kymessage's PostgreSQL 17 to 18) gates a deploy at the same version on
// what the target holds: with a destination it is backed up first, and a failed backup leaves
// the old files deployed; without one it needs --acknowledge-no-backup; once moved, nothing.
func TestUpgradeMoveBacksUpFirst(t *testing.T) {
	f, _, k := backupFixture(t)
	dir := filepath.Join(f.h.root, "dumpdemo")
	m := string(dumpCatalog["dumpdemo/manifest.yaml"].Data) + "upgrade:\n  move:\n    from: files/old/PG_VERSION\n    to: files/new/PG_VERSION\n    what: the test move\n"
	f.o.Catalog = fstest.MapFS{
		"dumpdemo/manifest.yaml":     {Data: []byte(m)},
		"dumpdemo/compose.yaml.tmpl": {Data: append(dumpCatalog["dumpdemo/compose.yaml.tmpl"].Data, "    environment: {MOVED: 'yes'}\n"...)},
	}
	os.MkdirAll(filepath.Join(dir, "files", "old"), 0o700)
	os.WriteFile(filepath.Join(dir, "files", "old", "PG_VERSION"), []byte("17\n"), 0o600)
	deployed := func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, "compose.yaml"))
		return strings.Contains(string(b), "MOVED")
	}

	// The backup fails: nothing deployed.
	k.match = "'backup' '--json'"
	err := f.run("upgrade", "dumpdemo")
	k.match = ""
	if err == nil || !strings.Contains(err.Error(), "no upgrade: the pre-upgrade backup failed") || deployed() {
		t.Fatalf("failed backup: %v, deployed %v\n%s", err, deployed(), f.out.String())
	}
	// It succeeds: one snapshot, then the deploy.
	if err := f.run("upgrade", "dumpdemo"); err != nil || !deployed() {
		t.Fatalf("upgrade: %v, deployed %v\n%s", err, deployed(), f.out.String())
	}
	backup, _ := engine.Read(filepath.Join(f.state, "results"), "dumpdemo.pre-upgrade-backup")
	note, _ := engine.Read(filepath.Join(f.state, "results"), "dumpdemo.upgrade")
	if backup.Status != "ok" || note.Diagnostics != "dumpdemo 1.0.0 moves the test move, after a verified pre-upgrade backup" {
		t.Fatalf("backup %+v, note %q", backup, note.Diagnostics)
	}
	// Moved: the gate says nothing more.
	os.MkdirAll(filepath.Join(dir, "files", "new"), 0o700)
	os.WriteFile(filepath.Join(dir, "files", "new", "PG_VERSION"), []byte("18\n"), 0o600)
	if err := f.run("upgrade", "dumpdemo"); err != nil || strings.Contains(f.out.String(), "pre-upgrade-backup") {
		t.Fatalf("moved app gated again: %v\n%s", err, f.out.String())
	}
}

func TestUpgradeMoveWithoutDestination(t *testing.T) {
	f, _, _ := backupFixture(t)
	dir := filepath.Join(f.h.root, "dumpdemo")
	f.plan(t, "installation_id: "+installation+"\napps:\n  - name: dumpdemo\n    target: nas\n")
	m := string(dumpCatalog["dumpdemo/manifest.yaml"].Data) + "upgrade:\n  move:\n    from: files/old/PG_VERSION\n    to: files/new/PG_VERSION\n    what: the test move\n"
	f.o.Catalog = fstest.MapFS{"dumpdemo/manifest.yaml": {Data: []byte(m)}, "dumpdemo/compose.yaml.tmpl": dumpCatalog["dumpdemo/compose.yaml.tmpl"]}
	os.MkdirAll(filepath.Join(dir, "files", "old"), 0o700)
	os.WriteFile(filepath.Join(dir, "files", "old", "PG_VERSION"), []byte("17\n"), 0o600)
	before, _ := os.ReadFile(filepath.Join(dir, "compose.kyq.yaml"))

	err := f.run("upgrade", "dumpdemo")
	after, _ := os.ReadFile(filepath.Join(dir, "compose.kyq.yaml"))
	if code(err) != suite.AckRequired || !strings.Contains(err.Error(), "moves the test move, and kyquickstart cannot back it up first here") || !strings.Contains(err.Error(), "--acknowledge-no-backup") || string(after) != string(before) {
		t.Fatalf("no destination: %v\n%s", err, f.out.String())
	}
	if err := f.run("upgrade", "dumpdemo", "--acknowledge-no-backup"); err != nil {
		t.Fatal(err, f.out.String())
	}
	note, _ := engine.Read(filepath.Join(f.state, "results"), "dumpdemo.upgrade")
	b, _ := os.ReadFile(filepath.Join(dir, "compose.kyq.yaml"))
	if note.Diagnostics != "dumpdemo 1.0.0 moves the test move; operator acknowledged no pre-upgrade backup" || !strings.Contains(string(b), managed.Label) {
		t.Fatalf("note %q\n%s", note.Diagnostics, f.out.String())
	}
}
