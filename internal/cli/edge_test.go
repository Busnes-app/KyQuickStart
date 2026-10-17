package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
)

const edgePlan = "apps:\n  - name: hello\n    target: nas\nedges:\n  - target: nas\n    provider: frp\n    npm_url: https://npm.test\n    connector_cidr: 172.30.0.3/32\n    routes:\n      - app: hello\n        hostname: hello.example.com\n        forward_host: app\n        forward_port: 8080\n        certificate_id: 1\n"

// The installation ID is a plan change: it bumps the revision, is published only by a
// passing apply, and is refused on a runner-owned target unless already present.
func TestInstallationIDIsAPlanChange(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.plan(t, "revision: 4\n"+edgePlan)
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := s.ensureInstallation(f.state)
	if err != nil || pub == nil || !s.changesPlan {
		t.Fatalf("ensureInstallation = %v, changesPlan %v", err, s.changesPlan)
	}
	if _, err := stack.Parse([]byte("version: 1\ntargets: [{name: a, kubeconfig: /k}]\ninstallation_id: " + s.installation + "\n")); err != nil {
		t.Fatalf("generated id %q: %v", s.installation, err)
	}
	if f.stack(t).InstallationID != "" {
		t.Fatal("id written before apply passed")
	}
	if err := s.guard("nas", stack.Marker{Owner: "runner", RunnerID: runnerID}); code(err) != suite.OwnedByRunner {
		t.Fatalf("runner-owned target: %v", err)
	}
	if err := pub(); err != nil {
		t.Fatal(err)
	}
	st := f.stack(t)
	if st.InstallationID != s.installation || st.Revision != 5 {
		t.Fatalf("published id %q revision %d", st.InstallationID, st.Revision)
	}
	// Present: no change, so a runner-owned target is not refused for it.
	s, _ = load(f.state, nil, f.o)
	if pub, err := s.ensureInstallation(f.state); pub != nil || err != nil || s.changesPlan || s.installation != st.InstallationID {
		t.Fatalf("second run changed the plan: %v", err)
	}
	if err := s.guard("nas", stack.Marker{Revision: 5, Owner: "runner", RunnerID: runnerID}); err != nil {
		t.Fatalf("runner-owned target refused: %v", err)
	}
	// No edges: no id.
	f.plan(t, "apps:\n  - name: hello\n    target: nas\n")
	s, _ = load(f.state, nil, f.o)
	if pub, _ := s.ensureInstallation(f.state); pub != nil || s.installation != "" {
		t.Fatal("id generated without edges")
	}
}

func TestFailedApplyPublishesNoID(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.plan(t, edgePlan) // no npm_token on the target: connect fails
	if err := f.run("apply"); err == nil {
		t.Fatal("apply passed without an NPM token")
	}
	if st := f.stack(t); st.InstallationID != "" || st.Revision != 0 {
		t.Fatalf("failed apply changed the plan: %+v", st)
	}
	if err := f.run("preflight", "--adopt-edge-hosts"); err == nil || !strings.Contains(err.Error(), "only for apply") {
		t.Fatalf("--adopt-edge-hosts outside apply: %v", err)
	}
}

func TestNodePortsAndEdgePlacement(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	k1 := "  - name: k1\n    kubeconfig: " + kc + "\n"
	f.plan(t, k1+"apps:\n  - name: npm\n    target: k1\n    node_ports: {443: 31443}\n")
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	if np := s.cat["npm"].Kubernetes.NodePorts; np[443] != 31443 || np[80] != 30080 || s.edgeApp("k1") != "npm" || s.edgeApp("nas") != "" {
		t.Fatalf("node ports %v, edge %q", np, s.edgeApp("k1"))
	}
	if fresh, _ := catalog.Load(catalog.Embedded()); fresh["npm"].Kubernetes.NodePorts[443] != 30443 {
		t.Fatal("override leaked into the catalog")
	}
	for plan, want := range map[string]string{
		k1 + "apps:\n  - name: npm\n    target: k1\n    node_ports: {81: 31081}\n": "not one of its published ports",
		"apps:\n  - name: npm\n    target: nas\n":                                  "place it on a Kubernetes target",
	} {
		f.plan(t, plan)
		if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", plan, err, want)
		}
	}
}
