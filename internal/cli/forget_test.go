package cli

import (
	"bytes"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
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

// A released route keeps exactly its upstream in the cluster edge's egress (session.upstreams,
// which the edge's NetworkPolicy admits) after the app leaves the plan, and the edge stays in
// the plan with no route of its own. An in-cluster route adds no address; a Docker edge
// records nothing.
func TestReleasedUpstreamKeepsEgress(t *testing.T) {
	plan := `version: 1
targets:
  - name: k1
    kubeconfig: /dev/null
  - name: nas
    host: 192.0.2.10
    user: root
    root: /srv/kyq
  - name: nas2
    host: 192.0.2.11
    user: root
    root: /srv/kyq
apps:
  - name: npm
    target: k1
  - name: hello
    target: k1
  - name: old
    target: nas
edges:
  - target: k1
    provider: lan
    npm_url: kube://kyq-npm/app:81
    allow_cidrs: [192.0.2.0/24]
    routes:
      - {app: old, hostname: old.example.com, forward_host: 192.0.2.10, forward_port: 18443, certificate_id: 1}
      - {app: old, hostname: chat.example.com, forward_host: 192.0.2.10, forward_port: 18080, certificate_id: 1}
      - {app: hello, hostname: hello.example.com, forward_host: app.kyq-hello.svc.cluster.local, forward_port: 8080, certificate_id: 1}
  - target: nas2
    provider: lan
    npm_url: https://npm.test
    allow_cidrs: [192.0.2.0/24]
    routes:
      - {app: old, hostname: old2.example.com, forward_host: 192.0.2.10, forward_port: 18443, certificate_id: 1}
`
	st, err := stack.Parse([]byte(plan))
	if err != nil {
		t.Fatal(err)
	}
	upstreams := func(st *stack.Stack) []netip.AddrPort {
		s := &session{edges: st.Edges, placed: map[string]stack.Target{}}
		for _, a := range st.Apps {
			s.placed[a.Name], _ = st.Target(a.Target)
		}
		return s.upstreams("k1")
	}
	want := []netip.AddrPort{netip.MustParseAddrPort("192.0.2.10:18080"), netip.MustParseAddrPort("192.0.2.10:18443")}
	if got := upstreams(st); !slices.Equal(got, want) {
		t.Fatalf("before: %v", got)
	}
	recordReleased(st, "old", "nas")
	withoutApp(st, "old", "nas")
	b, err := yaml.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if st, err = stack.Parse(b); err != nil {
		t.Fatalf("the forgotten plan does not load: %v\n%s", err, b)
	}
	if got := upstreams(st); !slices.Equal(got, want) {
		t.Errorf("after forget, the edge admits %v, want %v", got, want)
	}
	if len(st.Edges) != 1 || len(st.Edges[0].Released) != 2 || st.Edges[0].Released[0] != (stack.Released{Hostname: "old.example.com", Upstream: "192.0.2.10:18443"}) {
		t.Errorf("edges = %+v", st.Edges)
	}

	// hello was the edge's last route: the edge stays for its released routes; an in-cluster
	// route released there adds no address.
	recordReleased(st, "hello", "k1")
	withoutApp(st, "hello", "k1")
	if len(st.Edges) != 1 || st.Edges[0].Released[2] != (stack.Released{Hostname: "hello.example.com"}) {
		t.Fatalf("edges = %+v", st.Edges)
	}
	if got := upstreams(st); !slices.Equal(got, want) {
		t.Errorf("after the in-cluster release, the edge admits %v", got)
	}
	b, _ = yaml.Marshal(st)
	if _, err := stack.Parse(b); err != nil {
		t.Fatalf("an edge with only released routes does not load: %v", err)
	}
}
