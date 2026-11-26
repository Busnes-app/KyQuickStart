package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/suite"
)

// An installed app that left the catalog: every command refuses the plan without touching it,
// unmanage refuses while a route of it is on a tunnel or frp edge, and otherwise forgets it
// with no Docker command at all, so nothing is stopped, recreated or removed.
func TestRetiredAppIsForgottenNotRemoved(t *testing.T) {
	f, cat := suiteFixture(t)
	if err := f.run("install", "web", "--target", "nas"); err != nil {
		t.Fatal(err, f.out.String())
	}
	created := f.h.up["kyq-web"]
	if err := os.RemoveAll(filepath.Join(cat, "web")); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(f.state, "stack.yaml")
	before, _ := os.ReadFile(plan)
	f.h.docker = nil
	for _, args := range [][]string{{"apply"}, {"preflight"}, {"status"}, {"review"}, {"upgrade", "web"}, {"uninstall", "web"}, {"stop", "web"}, {"restart", "web"}, {"install", "db", "--target", "nas"}} {
		err := f.run(args...)
		if code(err) != suite.UnknownApp || !strings.Contains(err.Error(), "stack.yaml names web, which this release cannot manage") || !strings.Contains(err.Error(), "`kyquickstart unmanage web` forgets it and keeps it running and reachable") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if len(f.h.docker) > 0 {
		t.Fatalf("a refused command ran docker: %q", f.h.docker)
	}

	// A route on an frp edge: its connector would drop it, so unmanage refuses.
	frp := "edges:\n  - target: nas\n    provider: frp\n    npm_url: https://npm.test\n    connector_cidr: 172.30.0.3/32\n    routes:\n      - app: web\n        hostname: web.example.com\n        forward_host: 192.0.2.1\n        forward_port: 8080\n        certificate_id: 1\n"
	withFRP := append(bytes.Clone(before), frp...)
	os.WriteFile(plan, withFRP, 0o600)
	if err := f.run("unmanage", "web"); code(err) != suite.Dependency || !strings.Contains(err.Error(), "edge nas (frp) carries web's route web.example.com") || !strings.Contains(err.Error(), "Nothing was changed") {
		t.Fatalf("frp route: %v", err)
	}
	if after, _ := os.ReadFile(plan); !bytes.Equal(after, withFRP) || len(f.h.docker) > 0 {
		t.Fatalf("refused unmanage changed the plan or ran docker: %q", f.h.docker)
	}

	os.WriteFile(plan, before, 0o600)
	if err := f.run("unmanage", "web"); err != nil {
		t.Fatal(err, f.out.String())
	}
	if len(f.h.docker) > 0 {
		t.Errorf("forget ran docker: %q", f.h.docker)
	}
	if f.h.up["kyq-web"] != created || !strings.Contains(created, managed.Label) {
		t.Error("web's containers were recreated")
	}
	if _, err := os.Stat(filepath.Join(f.h.root, "web", "compose.kyq.yaml")); err != nil {
		t.Errorf("web's files were touched: %v", err)
	}
	if st := f.stack(t); len(st.Apps) != 1 || st.Apps[0].Name != "db" {
		t.Errorf("plan = %+v", st.Apps)
	}
	b, _ := os.ReadFile(filepath.Join(f.state, "handover.md"))
	if !strings.Contains(string(b), "## web on nas is no longer managed: it left the catalog") || !strings.Contains(string(b), "Nothing of it was stopped, recreated or deleted") {
		t.Errorf("handover:\n%s", b)
	}
	if f.step("web.handover") != "ok" {
		t.Errorf("no handover note:\n%s", f.out.String())
	}
	// The rest of the installation is managed again, and web is never named.
	if err := f.run("apply"); err != nil {
		t.Fatal(err, f.out.String())
	}
	if f.ran("kyq-web") {
		t.Errorf("apply touched web: %q", f.h.docker)
	}
	if err := f.run("unmanage", "web"); code(err) != suite.UnknownApp {
		t.Errorf("second unmanage: %v", err)
	}
}
