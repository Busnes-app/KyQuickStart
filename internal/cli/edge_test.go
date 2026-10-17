package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	ktesting "k8s.io/client-go/testing"
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
	// Every plan gets one, edges or not.
	f.plan(t, "apps:\n  - name: hello\n    target: nas\n")
	s, _ = load(f.state, nil, f.o)
	if pub, _ := s.ensureInstallation(f.state); pub == nil || s.installation == "" {
		t.Fatal("no id generated without edges")
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
	if np := s.nodePorts("k1"); len(np) != 2 || np[31443] != "npm" || np[30080] != "npm" || len(s.nodePorts("nas")) != 0 {
		t.Fatalf("preflight node ports %v", np)
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

// A NodePort another Service holds fails apply at preflight, before anything is created.
func TestApplyRefusesAHeldNodePort(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	f.plan(t, "  - name: k1\n    kubeconfig: "+kc+"\n    storage_class: local-path\napps:\n  - name: npm\n    target: k1\n")
	f.cs.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{Major: "1", Minor: "37"}
	f.cs.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		r := a.(ktesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		r.Status.Allowed = true
		return true, r, nil
	})
	f.cs.StorageV1().StorageClasses().Create(ctx, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "local-path"}}, metav1.CreateOptions{})
	f.cs.CoreV1().Services("media").Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "jellyfin"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeNodePort, Ports: []corev1.ServicePort{{Port: 8096, NodePort: 30080}}}}, metav1.CreateOptions{})
	err := f.run("apply")
	if err == nil || !strings.Contains(f.out.String(), "30080 (for npm) is held by Service media/jellyfin") {
		t.Fatalf("apply: %v\n%s", err, f.out.String())
	}
	if _, err := f.cs.CoreV1().Namespaces().Get(ctx, "kyq-npm", metav1.GetOptions{}); err == nil {
		t.Error("namespace created before preflight passed")
	}
	if st := f.stack(t); st.InstallationID != "" || st.Revision != 0 {
		t.Errorf("refused apply changed the plan: %+v", st)
	}
}
