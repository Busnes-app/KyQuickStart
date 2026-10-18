package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
)

const identityPrep = "    identity:\n      schema: kyidentity.setup/1\n      setup_id: 271333e2-c221-4601-b249-563b3d828530\n" +
	"      admin: {username: owner-admin, email: admin@example.test}\n      everyday: {username: owner, email: owner@example.test}\n      clients: []\n"

// catalogIdentityPlan places NPM and KyIdentity on k1, routes identity.example.com to
// KyIdentity and selects its setup; extra is appended to the integration.
func catalogIdentityPlan(t *testing.T, f *fixture, route, extra string) {
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	f.plan(t, "  - name: k1\n    kubeconfig: "+kc+"\napps:\n  - name: npm\n    target: k1\n  - name: kyidentity\n    target: k1\n"+
		"edges:\n  - target: k1\n    provider: cloudflare\n    npm_url: kube://kyq-npm/app:81\n    connector_cidr: 192.0.2.9/32\n    routes:\n"+
		"      - app: "+route+"\n        hostname: identity.example.com\n        forward_host: app.kyq-kyidentity.svc.cluster.local\n        forward_port: 5867\n        certificate_id: 1\n"+
		"integrations:\n  - product: kyidentity\n    target: k1\n    bundle: /data/installer/271333e2-c221-4601-b249-563b3d828530/setup.json\n"+identityPrep+extra)
}

func TestCatalogKyIdentityTakesItsRouteAndIntegration(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	catalogIdentityPlan(t, f, "kyidentity", "")
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.cat["kyidentity"].Kubernetes.Env["KYIDENTITY_ISSUER_URL"]; got != "https://identity.example.com" {
		t.Fatalf("issuer = %q", got)
	}
	if fresh, _ := catalog.Load(catalog.Embedded()); !fresh["kyidentity"].Kubernetes.UsesHostname() {
		t.Fatal("hostname leaked into the catalog")
	}

	// The setup step follows health and execs into the app's one ready pod.
	ctx := context.Background()
	f.cs.CoreV1().Pods("kyq-kyidentity").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Labels: map[string]string{"app.kubernetes.io/name": "kyidentity", managed.Label: managed.Value}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}, metav1.CreateOptions{})
	native := &fakeIdentity{}
	s.drivers["k1"] = kubeDriver{c: kube.Fake(f.cs, "https://k1.test", native.stream)}
	s.edges = nil // edge steps need a reachable NPM; not under test here
	steps, err := s.deploySteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, st := range steps {
		ids = append(ids, st.ID())
	}
	at := slices.Index(ids, "kyidentity.setup")
	if at < 1 || ids[at-1] != "kyidentity.health" || slices.Contains(ids, "npm.setup") {
		t.Fatalf("steps = %v", ids)
	}
	setup := steps[at]
	if done, err := setup.Inspect(ctx); done || err != nil {
		t.Fatalf("fresh install inspected as done: %v", err)
	}
	if err := setup.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := setup.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if done, _ := setup.Inspect(ctx); !done || !native.staged || !native.applied {
		t.Fatal("setup not inspected as done")
	}
	if err := f.run("integrate-stack"); err == nil || !strings.Contains(err.Error(), "which apply configures") {
		t.Fatalf("integrate-stack ran an installed app's setup: %v", err)
	}
	if native.pod != "kyq-kyidentity/app-1/app" || !strings.Contains(native.stdin, `"setupId":"271333e2-c221-4601-b249-563b3d828530"`) {
		t.Fatalf("exec reached %s with %q", native.pod, native.stdin)
	}
}

func TestCatalogKyIdentityRefusals(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	for name, c := range map[string]struct{ route, extra, want string }{
		"no route":        {"npm", "", `"kyidentity" takes its URL from its route hostname`},
		"named workload":  {"kyidentity", "    container: app\n", "names no container"},
		"no identity set": {"kyidentity", "", "from its identity settings"},
	} {
		catalogIdentityPlan(t, f, c.route, c.extra)
		if name == "no identity set" {
			b, _ := os.ReadFile(filepath.Join(f.state, "stack.yaml"))
			os.WriteFile(filepath.Join(f.state, "stack.yaml"), []byte(strings.Replace(string(b), identityPrep, "", 1)), 0o600)
		}
		if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

// fakeIdentity answers the kyidentity CLI's capabilities, staging and setup like the product:
// checks fail until the matching mutation ran.
type fakeIdentity struct {
	staged, applied bool
	pod, stdin      string
	intent          string // retained at the first staging; a different one is refused
	export          string // --export-client's frame
}

func (n *fakeIdentity) stream(_ context.Context, namespace, pod, container string, args []string, o remotecommand.StreamOptions) error {
	n.pod = namespace + "/" + pod + "/" + container
	check := args[len(args)-1] == "--check"
	var out string
	switch {
	case slices.Contains(args, "--capabilities"):
		out = `{"schema":"ky.setup-capabilities/1","service":"kyidentity","bundleSchema":"kyidentity.setup/1","features":["local-accounts","confidential-oidc-clients","assigned-only","everyday-assignment","app-role-assignments","scim-connectors","suite-webhook-connectors","directory-groups","app-group-assignments","scim-groups","check","protected-setup-files","bundle-preparation","target-credentials","client-credential-export"]}`
	case slices.Contains(args, "--stage"):
		b, _ := io.ReadAll(o.Stdin)
		n.stdin = string(b)
		if check && !n.staged || n.staged && n.intent != n.stdin {
			return errors.New("not staged, or intent changed")
		}
		n.intent = n.stdin
		status := "present"
		if !check && !n.staged {
			n.staged, status = true, "created"
		}
		out = `{"schema":"kyidentity.setup-preparation/1","status":"` + status + `"}`
	case slices.Contains(args, "--export-client"):
		if !n.applied {
			return errors.New("not applied")
		}
		out = n.export
	default:
		if check && !n.applied || !n.staged {
			return errors.New("not applied")
		}
		created := `[]`
		if !check && !n.applied {
			n.applied, created = true, `[{"kind":"user","name":"owner-admin","id":"u1"}]`
		}
		out = `{"schema":"kyidentity.setup/1","created":` + created + `,"reused":[]}`
	}
	_, err := io.WriteString(o.Stdout, out)
	return err
}
