package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

// nginxFixture is a fixture whose catalog holds the demo app beside both edge components.
func nginxFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	dir := t.TempDir()
	for name, src := range map[string]string{"hello": "../catalog/testdata/apps/hello", "nginx": "../catalog/apps/nginx", "npm": "../catalog/apps/npm"} {
		if err := os.CopyFS(filepath.Join(dir, name), os.DirFS(src)); err != nil {
			t.Fatal(err)
		}
	}
	f := newFixture(t, dir)
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	return f, "  - name: k1\n    kubeconfig: " + kc + "\n"
}

const nginxEdgePlan = `installation_id: 8c1e2173-3701-458c-8d27-8899dba16323
apps:
  - name: nginx
    target: k1
  - name: hello
    target: k1
edges:
  - target: k1
    provider: lan
    proxy: nginx
    allow_cidrs: [192.168.1.0/24]
    client_port: 443
    certificates:
      - name: wild
        domains: ["*.example.com"]
        email: ops@example.com
        dns_provider: cloudflare
        credentials: {secret: dns, key: token}
    routes:
      - app: hello
        hostname: hello.example.com
        forward_host: app.kyq-hello.svc.cluster.local
        forward_port: 8080
        certificate: wild
`

// A proxy: nginx edge connects no NPM: its steps come from the cluster driver, its pods mount
// the plan's TLS Secrets, and its cluster is asked for the nginx edge's permissions and
// cert-manager.
func TestNginxEdgeSession(t *testing.T) {
	ctx := context.Background()
	f, k1 := nginxFixture(t)
	f.plan(t, k1+nginxEdgePlan)
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if err := s.connect(ctx, nil); err != nil {
		t.Fatal(err)
	}
	d := s.drivers["k1"].(kubeDriver)
	if len(s.npms) != 0 || s.nginx["k1"] == nil || !slices.Equal(d.edges, []string{"nginx"}) || !slices.Equal(d.tlsSecrets, []string{"kyq-tls-wild"}) ||
		d.needs != (kube.Needs{EdgeProxy: true, Certificates: true}) {
		t.Fatalf("driver = %+v, npms %v", d, s.npms)
	}
	if a := d.app("nginx", s.cat["nginx"], "test", nil); !slices.Equal(a.TLSSecrets, d.tlsSecrets) {
		t.Fatalf("nginx app = %+v", a)
	}
	list, err := s.deploySteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	order := ids(list)
	want := []string{"hello.secrets", "hello.deploy", "hello.health", "nginx.secrets", "nginx.deploy", "nginx.health", "k1.edge.removed", "k1.edge.cert.wild"}
	if len(order) != len(want)+1 || !slices.Equal(order[:len(want)], want) || !strings.HasPrefix(order[len(want)], "k1.edge.") {
		t.Fatalf("steps = %v", order)
	}
	// Preflight on a cluster that grants nothing and has no cert-manager names both.
	f.cs.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		return true, a.(ktesting.CreateAction).GetObject(), nil // Allowed stays false
	})
	var perms, certManager string
	for _, f := range d.preflight(ctx) {
		switch f.Check {
		case "permissions":
			perms = f.Detail
		case "cert-manager":
			certManager = f.Detail
		}
	}
	for _, need := range []string{"create daemonsets", "update configmaps", "patch pods", "list nodes", "create issuers", "create certificates"} {
		if !strings.Contains(perms, need) {
			t.Errorf("preflight does not ask for %s: %s", need, perms)
		}
	}
	if !strings.Contains(certManager, "cert-manager") {
		t.Fatalf("cert-manager finding = %q", certManager)
	}
	// The same plan with operator TLS Secrets needs neither cert-manager nor its permissions.
	f.plan(t, k1+strings.Replace(strings.Replace(nginxEdgePlan, "        certificate: wild", "        certificate_secret: operator-tls", 1),
		"    certificates:\n      - name: wild\n        domains: [\"*.example.com\"]\n        email: ops@example.com\n        dns_provider: cloudflare\n        credentials: {secret: dns, key: token}\n", "", 1))
	s2, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.close()
	if err := s2.connect(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if d := s2.drivers["k1"].(kubeDriver); d.needs != (kube.Needs{EdgeProxy: true}) || !slices.Equal(d.tlsSecrets, []string{"operator-tls"}) {
		t.Fatalf("driver = %+v", d)
	}

	// The edge's nginx cannot be uninstalled from under the routes it serves.
	f.cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kyq-nginx",
		Labels: map[string]string{managed.Label: managed.Value, managed.ReleaseSetLabel: "test", managed.VersionLabel: s.cat["nginx"].Version}}}, metav1.CreateOptions{})
	if err := f.run("uninstall", "nginx"); code(err) != suite.Dependency || !strings.Contains(err.Error(), "routes its apps through nginx") {
		t.Fatalf("uninstall nginx: %v", err)
	}
}

// While the nginx edge replaces an NPM both are placed on the cluster, each with its own
// NodePorts and address; the other apps admit both edges' pods.
func TestNginxBesideNPM(t *testing.T) {
	f, k1 := nginxFixture(t)
	beside := func(nginx string) string {
		return k1 + "apps:\n  - name: npm\n    target: k1\n  - name: nginx\n    target: k1\n" + nginx + "  - name: hello\n    target: k1\n" +
			"edges:\n  - target: k1\n    provider: lan\n    npm_url: kube://kyq-npm/app:81\n    allow_cidrs: [192.168.1.0/24]\n    client_port: 443\n    routes:\n" +
			"      - app: hello\n        hostname: hello.example.com\n        forward_host: app.kyq-hello.svc.cluster.local\n        forward_port: 8080\n        certificate_id: 1\n"
	}
	f.plan(t, beside(""))
	if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), "apps nginx and npm on k1 both publish 30") {
		t.Fatalf("both edges on the default NodePorts: %v", err)
	}
	f.plan(t, beside("    node_ports: {80: 31080, 443: 31443}\n    load_balancer_address: 192.168.1.50\n")+"")
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.edgeApps("k1"), []string{"nginx", "npm"}) || s.needs("k1") != (kube.Needs{LoadBalancer: true, EdgeProxy: true}) || s.plan.Edges[0].ClusterApp() != "npm" {
		t.Fatalf("edge apps %v, needs %+v", s.edgeApps("k1"), s.needs("k1"))
	}
	if np := s.nodePorts("k1"); np[31443] != "nginx" || np[30443] != "npm" || len(np) != 4 {
		t.Fatalf("node ports = %v", np)
	}
	withNPM := strings.Replace(beside("    node_ports: {80: 31080, 443: 31443}\n    load_balancer_address: 192.168.1.50\n"), "  - name: npm\n    target: k1\n", "  - name: npm\n    target: k1\n    load_balancer_address: 192.168.1.50\n", 1)
	f.plan(t, withNPM)
	if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), "both publish 192.168.1.50") {
		t.Fatalf("one address on both edges: %v", err)
	}
}

func TestInstallRouteBehindNginx(t *testing.T) {
	f, k1 := nginxFixture(t)
	f.plan(t, k1+strings.Replace(nginxEdgePlan, "    routes:\n      - app: hello\n        hostname: hello.example.com\n        forward_host: app.kyq-hello.svc.cluster.local\n        forward_port: 8080\n        certificate: wild\n",
		"    routes:\n      - app: nginx\n        hostname: edge.example.com\n        forward_host: 192.168.1.9\n        forward_port: 8080\n        certificate: wild\n", 1))
	st := f.stack(t)
	cat, err := catalog.Load(f.o.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	r := Request{Target: "k1", Hostname: "hello.example.com", ForwardHost: "app.kyq-hello.svc.cluster.local", ForwardPort: 8080}
	for cert, want := range map[string]stack.Route{"wild": {Certificate: "wild"}, "operator-tls": {CertificateSecret: "operator-tls"}} {
		r.Certificate = cert
		_, got, _, err := installRoute(st, cat["hello"], "hello", r)
		if err != nil || got.Certificate != want.Certificate || got.CertificateSecret != want.CertificateSecret || got.CertificateID != 0 {
			t.Fatalf("--certificate %s: %+v %v", cert, got, err)
		}
	}
	r.Certificate, r.CertificateID = "", 12
	if _, _, _, err := installRoute(st, cat["hello"], "hello", r); code(err) != suite.InvalidRequest || !strings.Contains(err.Error(), "--certificate") {
		t.Fatalf("--certificate-id behind nginx: %v", err)
	}
	// And --certificate is the nginx edge's alone.
	st.Edges[0].Proxy, st.Edges[0].NPMURL = "", "kube://kyq-npm/app:81"
	r.Certificate, r.CertificateID = "wild", 0
	if _, _, _, err := installRoute(st, cat["hello"], "hello", r); code(err) != suite.InvalidRequest || !strings.Contains(err.Error(), "--certificate-id") {
		t.Fatalf("--certificate behind an NPM: %v", err)
	}
}
