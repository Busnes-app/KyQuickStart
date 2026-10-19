package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/suite"
)

const whoami = "traefik/whoami:v1.11.0@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab"

// writeApp writes a Docker-only catalog app with one "whoami" service; extra holds at least
// its version.
func writeApp(t *testing.T, dir, name, extra string) {
	t.Helper()
	d := filepath.Join(dir, name)
	os.MkdirAll(d, 0o700)
	manifest := "name: " + name + "\ncategory: ky\nimages:\n  whoami: " + whoami + "\nhealth:\n  timeout_seconds: 1\n" + extra
	if err := os.WriteFile(filepath.Join(d, "manifest.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d, "compose.yaml.tmpl"), []byte("services:\n  whoami:\n    image: {{ .Images.whoami }}\n"), 0o600)
}

// suiteFixture is a plan with no apps over a catalog of db 1.0.0 and web 1.0.0, which
// depends on db.
func suiteFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	cat := t.TempDir()
	writeApp(t, cat, "db", "version: 1.0.0\n")
	writeApp(t, cat, "web", "version: 1.0.0\ndepends_on: [db]\n")
	f := newFixture(t, cat)
	f.plan(t, "apps: []\n")
	return f, cat
}

func (f *fixture) marker(t *testing.T) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(f.h.root, ".plan"))
	return string(b)
}

// step returns the engine's status word for id in the last command's output.
func (f *fixture) step(id string) string {
	for _, line := range strings.Split(f.out.String(), "\n") {
		if w := strings.Fields(line); len(w) >= 2 && w[0] == id {
			return strings.TrimSuffix(w[1], ":")
		}
	}
	return ""
}

func (f *fixture) ran(sub string) bool {
	for _, c := range f.h.docker {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func TestInstallAddsDependenciesThenPublishes(t *testing.T) {
	f, _ := suiteFixture(t)
	if err := f.run("install", "web", "--target", "nas"); err != nil {
		t.Fatal(err, f.out.String())
	}
	st := f.stack(t)
	if st.Revision != 1 || len(st.Apps) != 2 || st.Apps[0].Name != "db" || st.Apps[1].Name != "web" {
		t.Fatalf("plan = %+v", st)
	}
	if f.marker(t) != `{"revision":1,"owner":"cli","runner_id":"","installation_id":"`+st.InstallationID+`"}` {
		t.Errorf("marker = %s", f.marker(t))
	}
	if !strings.Contains(f.h.up["kyq-web"], managed.VersionLabel+": 1.0.0") || f.h.up["kyq-db"] == "" {
		t.Errorf("deployed = %v", f.h.up)
	}
	if !strings.Contains(f.out.String(), "db.health") || strings.Index(f.out.String(), "db.deploy") > strings.Index(f.out.String(), "web.deploy") {
		t.Errorf("dependency not deployed first:\n%s", f.out.String())
	}

	for _, c := range []struct {
		args []string
		want suite.Code
	}{
		{[]string{"install", "web", "--target", "nas"}, suite.AlreadyInstalled},
		{[]string{"install", "nope", "--target", "nas"}, suite.UnknownApp},
		{[]string{"install", "db", "--target", "elsewhere"}, suite.AlreadyInstalled},
		{[]string{"install", "web", "db", "--target", "nas"}, suite.InvalidRequest},
	} {
		if err := f.run(c.args...); code(err) != c.want {
			t.Errorf("%v: err = %v, want %s", c.args, err, c.want)
		}
	}
	if f.stack(t).Revision != 1 {
		t.Error("a refusal wrote the plan")
	}
}

func TestInstallUnknownTarget(t *testing.T) {
	f, _ := suiteFixture(t)
	if err := f.run("install", "db", "--target", "elsewhere"); code(err) != suite.UnknownTarget {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallRerunAfterFailure(t *testing.T) {
	f, _ := suiteFixture(t)
	f.h.fail = "-p 'kyq-web' -f compose.yaml -f compose.kyq.yaml up"
	if err := f.run("install", "web", "--target", "nas"); code(err) != suite.StepFailed {
		t.Fatalf("err = %v", err)
	}
	if st := f.stack(t); st.Revision != 0 || len(st.Apps) != 0 {
		t.Fatalf("failed install published the plan: %+v", st)
	}
	if f.marker(t) != `{"revision":0,"owner":"cli","runner_id":""}` {
		t.Errorf("marker after failure = %s", f.marker(t))
	}
	f.h.fail = ""
	if err := f.run("install", "web", "--target", "nas"); err != nil {
		t.Fatal(err, f.out.String())
	}
	if f.step("db.deploy") != "skipped" || len(f.stack(t).Apps) != 2 {
		t.Errorf("rerun:\n%s", f.out.String())
	}
}

func TestUninstallKeepsDataAndRefusesDependency(t *testing.T) {
	f, _ := suiteFixture(t)
	if err := f.run("install", "web", "--target", "nas"); err != nil {
		t.Fatal(err)
	}
	if err := f.run("uninstall", "db"); code(err) != suite.Dependency || !strings.Contains(err.Error(), "web depends on db") {
		t.Fatalf("err = %v", err)
	}
	f.h.fail = " down"
	if err := f.run("uninstall", "web"); code(err) != suite.StepFailed {
		t.Fatalf("err = %v", err)
	}
	if len(f.stack(t).Apps) != 2 {
		t.Fatal("failed uninstall dropped the app")
	}
	f.h.fail, f.h.docker = "", nil
	if err := f.run("uninstall", "web"); err != nil {
		t.Fatal(err, f.out.String())
	}
	st := f.stack(t)
	if st.Revision != 2 || len(st.Apps) != 1 || st.Apps[0].Name != "db" {
		t.Fatalf("plan = %+v", st)
	}
	if _, running := f.h.up["kyq-web"]; running || f.ran(" -v") || f.ran("--volumes") {
		t.Errorf("web running %v or volumes removed: %q", running, f.h.docker)
	}
	if _, err := os.Stat(filepath.Join(f.h.root, "web", "compose.yaml")); err != nil {
		t.Errorf("app directory removed: %v", err)
	}
	if !strings.Contains(f.marker(t), `"revision":2`) {
		t.Errorf("marker = %s", f.marker(t))
	}
	if err := f.run("uninstall", "web"); code(err) != suite.NotInstalled {
		t.Errorf("second uninstall: %v", err)
	}
	if err := f.run("uninstall", "nope"); code(err) != suite.UnknownApp {
		t.Errorf("unknown app: %v", err)
	}
	// A reinstall finds the kept data at the same version and redeploys.
	if err := f.run("install", "web", "--target", "nas"); err != nil {
		t.Fatal(err, f.out.String())
	}
}

func TestUnmanageHandsOverOnce(t *testing.T) {
	f, _ := suiteFixture(t)
	if err := f.run("install", "web", "--target", "nas"); err != nil {
		t.Fatal(err)
	}
	if err := f.run("unmanage", "db"); code(err) != suite.Dependency {
		t.Fatalf("err = %v", err)
	}
	f.h.fail = "-p 'kyq-web' -f compose.yaml -f compose.kyq.yaml up --no-start"
	if err := f.run("unmanage", "web"); code(err) != suite.StepFailed {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.state, "handover.md")); !os.IsNotExist(err) || len(f.stack(t).Apps) != 2 {
		t.Fatal("failed unmanage recorded a handover or dropped the app")
	}
	f.h.fail = ""
	if err := f.run("unmanage", "web"); err != nil {
		t.Fatal(err, f.out.String())
	}
	if strings.Contains(f.h.up["kyq-web"], managed.Label) || !strings.Contains(f.h.up["kyq-web"], managed.VersionLabel) {
		t.Errorf("recreated with %q", f.h.up["kyq-web"])
	}
	b, _ := os.ReadFile(filepath.Join(f.state, "handover.md"))
	if strings.Count(string(b), "## web 1.0.0 on nas is no longer managed") != 1 || !strings.Contains(string(b), "docker compose -p kyq-web") {
		t.Errorf("handover:\n%s", b)
	}
	if st := f.stack(t); len(st.Apps) != 1 || st.Revision != 2 {
		t.Errorf("plan = %+v", st)
	}
	// Rerunning the section append, as a rerun after a crash before publishing would, adds nothing.
	if err := appendHandover(f.state, handoverSection("web", f.stack(t).Targets[0], "1.0.0", nil)); err != nil {
		t.Fatal(err)
	}
	if b2, _ := os.ReadFile(filepath.Join(f.state, "handover.md")); string(b2) != string(b) {
		t.Error("handover appended twice")
	}
	if err := f.run("unmanage", "web"); code(err) != suite.NotInstalled {
		t.Errorf("second unmanage: %v", err)
	}
	if err := f.run("install", "web", "--target", "nas"); code(err) != suite.AlreadyInstalled || !strings.Contains(err.Error(), "not managed") {
		t.Errorf("reinstall over an unmanaged app: %v", err)
	}
}

func TestUpgradeGate(t *testing.T) {
	f, cat := suiteFixture(t)
	if err := f.run("install", "web", "--target", "nas"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		manifest string
		ack      bool
		want     suite.Code
	}{
		{"version: 1.1.0\n", false, suite.AckRequired},
		{"version: 1.1.0\nupgrade:\n  migrates_data: true\n", true, suite.BackupRequired},
		{"version: 2.0.0\nupgrade:\n  min_from: 1.5.0\n", true, suite.UpgradeRefused},
		{"version: 1.3.0\nupgrade:\n  no_skip: [1.2.0]\n", true, suite.UpgradeRefused},
		{"version: 0.9.0\n", true, suite.UpgradeRefused},
	} {
		writeApp(t, cat, "web", c.manifest+"depends_on: [db]\n")
		f.h.docker = nil
		args := []string{"upgrade", "web"}
		if c.ack {
			args = append(args, "--acknowledge-no-backup")
		}
		if err := f.run(args...); code(err) != c.want {
			t.Errorf("%q: err = %v, want %s", c.manifest, err, c.want)
		}
		if f.ran(" pull") || f.ran(" up ") {
			t.Errorf("%q: deployed after a refusal", c.manifest)
		}
	}
	// apply is gated the same way.
	writeApp(t, cat, "web", "version: 1.1.0\ndepends_on: [db]\n")
	if err := f.run("apply"); code(err) != suite.AckRequired {
		t.Errorf("apply: %v", err)
	}
	if err := f.run("upgrade", "web", "--acknowledge-no-backup"); err != nil {
		t.Fatal(err, f.out.String())
	}
	r, _ := engine.Read(filepath.Join(f.state, "results"), "web.upgrade")
	if r.Status != "ok" || !strings.Contains(r.Diagnostics, "1.0.0 to 1.1.0") || !strings.Contains(r.Diagnostics, "acknowledged") {
		t.Errorf("acknowledgement record = %+v", r)
	}
	if !strings.Contains(f.h.up["kyq-web"], managed.VersionLabel+": 1.1.0") {
		t.Errorf("deployed = %q", f.h.up["kyq-web"])
	}
	if f.stack(t).Revision != 1 || !strings.Contains(f.marker(t), `"revision":1`) {
		t.Error("upgrade changed the plan revision")
	}
	if err := f.run("upgrade"); err != nil || f.step("web.deploy") != "skipped" {
		t.Errorf("upgrade at the catalog version: %v\n%s", err, f.out.String())
	}
	for args, want := range map[string]suite.Code{"nope": suite.UnknownApp} {
		if err := f.run("upgrade", args); code(err) != want {
			t.Errorf("upgrade %s: %v", args, err)
		}
	}
}

func TestUpgradeStopsAtFirstFailureInOrder(t *testing.T) {
	f, cat := suiteFixture(t)
	if err := f.run("install", "web", "--target", "nas"); err != nil {
		t.Fatal(err)
	}
	writeApp(t, cat, "db", "version: 1.1.0\n")
	writeApp(t, cat, "web", "version: 1.1.0\ndepends_on: [db]\n")
	f.h.fail, f.h.docker = "-p 'kyq-db' -f compose.yaml -f compose.kyq.yaml up", nil
	if err := f.run("upgrade", "--acknowledge-no-backup"); code(err) != suite.StepFailed || !strings.Contains(err.Error(), "db.deploy") {
		t.Fatalf("err = %v", err)
	}
	if f.ran("kyq-web' -f compose.yaml -f compose.kyq.yaml pull") {
		t.Error("web upgraded after db failed")
	}
	// The rerun finishes: db's files already carry 1.1.0, so only web is gated again.
	f.h.fail = ""
	if err := f.run("upgrade", "--acknowledge-no-backup"); err != nil {
		t.Fatal(err, f.out.String())
	}
	if !strings.Contains(f.h.up["kyq-db"], "1.1.0") || !strings.Contains(f.h.up["kyq-web"], "1.1.0") {
		t.Errorf("deployed = %v", f.h.up)
	}
}

func TestUpgradeRefusesUnknownVersion(t *testing.T) {
	f, _ := suiteFixture(t)
	if err := f.run("install", "db", "--target", "nas"); err != nil {
		t.Fatal(err)
	}
	// What an install from before versions were recorded left behind.
	old := "services:\n  whoami:\n    labels:\n      ky.managed-by: kyquickstart\n      ky.release-set: \"0.3.0\"\n"
	os.WriteFile(filepath.Join(f.h.root, "db", "compose.kyq.yaml"), []byte(old), 0o600)
	err := f.run("upgrade", "db", "--acknowledge-no-backup")
	if code(err) != suite.UpgradeRefused || !strings.Contains(err.Error(), "version unknown") || !strings.Contains(err.Error(), "0.3.0") {
		t.Fatalf("err = %v", err)
	}
	// The same release set deployed this catalog, so its version is known.
	os.WriteFile(filepath.Join(f.h.root, "db", "compose.kyq.yaml"), []byte(strings.Replace(old, "0.3.0", "test", 1)), 0o600)
	if err := f.run("upgrade", "db"); err != nil {
		t.Fatal(err)
	}
}

func TestStatusJSON(t *testing.T) {
	f, _ := suiteFixture(t)
	if err := f.run("install", "web", "--target", "nas"); err != nil {
		t.Fatal(err)
	}
	f.h.fail = "-p 'kyq-db' -f compose.yaml -f compose.kyq.yaml ps"
	f.run("apply")
	f.h.fail = ""
	if err := f.run("status", "--json"); err != nil {
		t.Fatal(err)
	}
	var st suite.Status
	dec := json.NewDecoder(strings.NewReader(f.out.String()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		t.Fatal(err, f.out.String())
	}
	if st.Runner != (suite.Runner{ReleaseSet: "test", PlanRevision: 1, PlanOwner: "cli"}) || len(st.Targets) != 1 || st.Targets[0] != (suite.Target{Name: "nas", Kind: "docker"}) {
		t.Errorf("status = %+v", st)
	}
	if len(st.Apps) != 2 {
		t.Fatalf("apps = %+v", st.Apps)
	}
	db, web := st.Apps[0], st.Apps[1]
	if db.Name != "db" || db.State != "failed" || db.LastResult == nil || db.LastResult.Status != "failed" {
		t.Errorf("db = %+v", db)
	}
	if web.Name != "web" || web.Version != "1.0.0" || web.Category != "ky" || web.Target != "nas" || web.State != "installed" || web.Health == nil || !web.Health.OK || web.LastResult == nil {
		t.Errorf("web = %+v", web)
	}
	if !strings.Contains(f.out.String(), `"operation": null`) {
		t.Errorf("operation not null:\n%s", f.out.String())
	}
	if err := f.run("status", "web"); err != ErrUsage {
		t.Errorf("status with an app: %v", err)
	}
}
