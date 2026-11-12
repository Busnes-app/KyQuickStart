//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// kindEdge is the proxy a kind edge suite runs behind, with the throwaway *.example.test
// certificate its routes use: an NPM certificate ID, or the nginx edge's TLS Secret.
type kindEdge struct {
	proxy   string
	certID  int
	secret  string
	certPEM []byte
}

// certify rewrites the plan's edge for the nginx edge. The suites write every edge and route
// as NPM's (npm_url, certificate_id), so one body serves both proxies.
func (k kindEdge) certify(st *stack.Stack) {
	if k.proxy != stack.NginxApp {
		return
	}
	for i := range st.Edges {
		e := &st.Edges[i]
		e.Proxy, e.NPMURL = stack.NginxApp, ""
		for j := range e.Routes {
			if r := &e.Routes[j]; r.CertificateID != 0 {
				r.CertificateID, r.CertificateSecret = 0, k.secret
			}
		}
	}
}

// testCertificate is a self-signed *.example.test certificate and its key, as PEM.
func testCertificate(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "*.example.test"}, DNSNames: []string{"*.example.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// npmBootstrap is what the NPM suites check of the catalog NPM's administrator and token.
type npmBootstrap struct {
	admin    *edge.Admin
	password string
	token    string
	stored   func() string
}

// checkNPMInstall checks the catalog NPM's first apply: its namespace, Services, bootstrap and
// policy, and uploads the suite's certificate.
func checkNPMInstall(t *testing.T, ctx context.Context, cs kubernetes.Interface, connected *kube.Client, out string) (kindEdge, *npmBootstrap) {
	t.Helper()
	if status(out, "npm.bootstrap") != "ok" {
		t.Fatal("npm was not bootstrapped")
	}
	ns, err := cs.CoreV1().Namespaces().Get(ctx, "kyq-npm", metav1.GetOptions{})
	if err != nil || ns.Labels["pod-security.kubernetes.io/enforce"] != "baseline" || ns.Labels[managed.Label] != managed.Value {
		t.Fatalf("npm namespace = %v, %v", ns.Labels, err)
	}
	admin, _ := cs.CoreV1().Services("kyq-npm").Get(ctx, "app", metav1.GetOptions{})
	public, _ := cs.CoreV1().Services("kyq-npm").Get(ctx, "app-public", metav1.GetOptions{})
	if admin.Spec.Type != corev1.ServiceTypeClusterIP || len(admin.Spec.Ports) != 1 || admin.Spec.Ports[0].Port != 81 {
		t.Fatalf("admin service = %+v", admin.Spec)
	}
	if public.Spec.Type != corev1.ServiceTypeNodePort || len(public.Spec.Ports) != 2 || public.Spec.Ports[0].NodePort != 30080 || public.Spec.Ports[1].NodePort != 30443 {
		t.Fatalf("public service = %+v", public.Spec)
	}

	// Bootstrap: the stored token works, NPM is set up, the default login and none other works.
	tunnel := connected.ServiceDialer("kyq-npm", "app", 81)
	npm, err := edge.NewAdmin("kube://kyq-npm/app:81", tunnel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(npm.Close)
	secrets, _ := cs.CoreV1().Secrets("kyq-npm").Get(ctx, "kyq-secrets", metav1.GetOptions{})
	password := string(secrets.Data["admin_password"])
	stored := func() string {
		sec, err := cs.CoreV1().Secrets("kyq-edge").Get(ctx, "kyq-credentials", metav1.GetOptions{})
		if err != nil || sec.Labels[managed.Label] != managed.Value {
			t.Fatalf("edge credential: %v", err)
		}
		return string(sec.Data["npm_token"])
	}
	token := stored()
	if setup, err := npm.Setup(ctx); err != nil || !setup {
		t.Fatalf("NPM setup = %v, %v", setup, err)
	}
	if ok, err := npm.Verify(ctx, token); !ok || err != nil {
		t.Fatalf("stored token rejected: %v", err)
	}
	if def, err := npm.Login(ctx, edge.DefaultEmail, edge.DefaultPassword); def != "" || err != nil {
		t.Fatalf("default login accepted: %v", err)
	}
	if mine, err := npm.Login(ctx, "admin@kyquickstart.invalid", password); mine == "" || err != nil {
		t.Fatalf("installer login rejected: %v", err)
	}
	if err := npm.CreateAdmin(ctx, "intruder@example.test", "intruder-password"); err == nil {
		t.Fatal("NPM still accepts an unauthenticated administrator")
	}

	// The policy: ports 80/443 answer any pod, the admin API answers none.
	for url, want := range map[string]string{
		"http://app-public.kyq-npm.svc.cluster.local/": "200",
		"http://app.kyq-npm.svc.cluster.local:81/api/": "000",
	} {
		if got := probe(t, ctx, cs, "npm", url); got != want {
			t.Errorf("pod to %s: HTTP %s, want %s", url, got, want)
		}
	}

	// A throwaway certificate, uploaded through the tunnel.
	certID, certPEM := uploadCertificate(t, ctx, tunnel, token)
	return kindEdge{proxy: "npm", certID: certID, certPEM: certPEM}, &npmBootstrap{admin: npm, password: password, token: token, stored: stored}
}

// nginxTLSSecret is the operator's TLS Secret the nginx suites' routes name.
const nginxTLSSecret = "example-test-tls"

// checkNginxInstall checks the catalog nginx's first apply: a restricted namespace, a ready
// pod on every node, no admin surface, its policy, and creates the suite's certificate as the
// operator would, as a labelled TLS Secret.
func checkNginxInstall(t *testing.T, ctx context.Context, cs kubernetes.Interface, out string) kindEdge {
	t.Helper()
	if status(out, "nginx.health") != "ok" || status(out, "npm.bootstrap") != "" {
		t.Fatalf("nginx install:\n%s", out)
	}
	ns, err := cs.CoreV1().Namespaces().Get(ctx, "kyq-nginx", metav1.GetOptions{})
	if err != nil || ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" || ns.Labels[managed.Label] != managed.Value {
		t.Fatalf("nginx namespace = %v, %v", ns.Labels, err)
	}
	ds, err := cs.AppsV1().DaemonSets("kyq-nginx").Get(ctx, "app", metav1.GetOptions{})
	if err != nil || ds.Status.NumberReady != ds.Status.DesiredNumberScheduled || ds.Status.NumberReady == 0 {
		t.Fatalf("nginx daemonset: %d ready of %d, %v", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled, err)
	}
	if _, err := cs.AppsV1().Deployments("kyq-nginx").Get(ctx, "app", metav1.GetOptions{}); err == nil {
		t.Fatal("nginx has a Deployment")
	}
	// Nothing but the public Service, on 80 and 443: there is no admin API to reach.
	svcs, _ := cs.CoreV1().Services("kyq-nginx").List(ctx, metav1.ListOptions{})
	if len(svcs.Items) != 1 || svcs.Items[0].Name != "app-public" {
		t.Fatalf("nginx services = %+v", svcs.Items)
	}
	public := svcs.Items[0]
	if public.Spec.Type != corev1.ServiceTypeNodePort || public.Spec.ExternalTrafficPolicy != corev1.ServiceExternalTrafficPolicyLocal || len(public.Spec.Ports) != 2 || public.Spec.Ports[0].NodePort != 30080 || public.Spec.Ports[1].NodePort != 30443 {
		t.Fatalf("public service = %+v", public.Spec)
	}
	claims, _ := cs.CoreV1().PersistentVolumeClaims("kyq-nginx").List(ctx, metav1.ListOptions{})
	if len(claims.Items) != 0 {
		t.Fatalf("nginx has %d claims", len(claims.Items))
	}
	// The policy: port 80 answers any pod (a redirect, and the readiness path), and the pod's
	// loopback status port, 8081, answers none.
	for url, want := range map[string]string{
		"http://app-public.kyq-nginx.svc.cluster.local/":          "301",
		"http://app-public.kyq-nginx.svc.cluster.local/kyq-ready": "204",
	} {
		if got := probe(t, ctx, cs, "nginx", url); got != want {
			t.Errorf("pod to %s: HTTP %s, want %s", url, got, want)
		}
	}
	pods, _ := cs.CoreV1().Pods("kyq-nginx").List(ctx, metav1.ListOptions{})
	if got := probe(t, ctx, cs, "nginx", "http://"+pods.Items[0].Status.PodIP+":8081/generation"); got != "000" {
		t.Errorf("the pod's loopback status port answered a pod: HTTP %s", got)
	}
	certPEM, keyPEM := testCertificate(t)
	if _, err := cs.CoreV1().Secrets("kyq-nginx").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: nginxTLSSecret, Labels: map[string]string{managed.Label: managed.Value}},
		Type:       corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: certPEM, corev1.TLSPrivateKeyKey: keyPEM},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// certID is a placeholder the suite's NPM-shaped routes carry until certify rewrites them.
	return kindEdge{proxy: stack.NginxApp, certID: 1, secret: nginxTLSSecret, certPEM: certPEM}
}

// TestKubernetesNginxEdge is TestKubernetesNPMEdge behind the nginx edge: the same routes,
// forged-header and allow-list checks, joins and sign-ins, through a proxy with no admin API.
func TestKubernetesNginxEdge(t *testing.T) { testKubernetesEdge(t, stack.NginxApp) }

// TestKubernetesNginxEdgeJoins is TestKubernetesNPMEdgeJoins behind the nginx edge.
func TestKubernetesNginxEdgeJoins(t *testing.T) { testKubernetesEdgeJoins(t, stack.NginxApp) }

// nginxTestToken stands in for a Cloudflare API token: it must stay in its Secrets.
const nginxTestToken = "e2e-cloudflare-token-0123456789abcdefghij"

// The cert-manager release whose CRDs the test installs, alone: no controller runs, so the test
// plays cert-manager, and nothing contacts an ACME server.
const (
	certManagerCRDs   = "https://github.com/cert-manager/cert-manager/releases/download/v1.21.3/cert-manager.crds.yaml"
	certManagerSHA256 = "29810b2a84c8ee0731a79679ee1dfd7e93cd750eb0244542eec47fa8700f2749"
)

// testNginxEdge covers what only the nginx edge has, on the lan edge the suite left: routes
// load by reload in the same pods, a removed route is disabled and kept, a foreign file's
// hostname is refused, and a managed certificate goes through cert-manager's API.
func testNginxEdge(t *testing.T, ctx context.Context, cs kubernetes.Interface, kc, state, catalogDir, nodeIP, client string, st *stack.Stack, save func(*stack.Stack), apply func(string) string) {
	t.Helper()
	pods := func() string {
		list, err := cs.CoreV1().Pods("kyq-nginx").List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range list.Items {
			out = append(out, fmt.Sprintf("%s restarts=%d", p.UID, p.Status.ContainerStatuses[0].RestartCount))
		}
		slices.Sort(out)
		return strings.Join(out, " ")
	}
	code := func(host string) string {
		b, _ := exec.Command("docker", "exec", client, "curl", "-sk", "-o", "/dev/null", "-w", "%{http_code}", "-m", "10", "--resolve", host+":30443:"+nodeIP, "https://"+host+":30443/").Output()
		return strings.TrimSpace(string(b))
	}
	files := func() map[string]string {
		cm, err := cs.CoreV1().ConfigMaps("kyq-nginx").Get(ctx, "kyq-edge-config", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return cm.Data
	}
	try := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := cli.Run(ctx, append(args, "--state", state), cli.Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "e2e", Out: &out})
		t.Logf("%s: %v\n%s", args[0], err, out.String())
		return out.String(), err
	}
	before := pods()
	// The marker names this installation, and the Cloudflare and lan edges so far were loads.
	for name, file := range files() {
		if strings.HasSuffix(name, ".server") && !strings.HasPrefix(file, "# kyquickstart:"+st.InstallationID+":k8s:hello:hello.example.test\n") {
			t.Fatalf("route file %s:\n%s", name, file)
		}
	}

	// A second hostname is served, then leaves the plan: disabled and kept, never deleted.
	hello := st.Edges[0].Routes[0]
	second := hello
	second.Hostname = "second.example.test"
	st.Edges[0].Routes = append(st.Edges[0].Routes, second)
	save(st)
	apply("second route")
	if got := code("second.example.test"); got != "200" {
		t.Fatalf("second route answered %s", got)
	}
	st.Edges[0].Routes = st.Edges[0].Routes[:1]
	save(st)
	if out := apply("route removed"); status(out, "k8s.edge.removed") != "ok" {
		t.Fatal("removed route not disabled")
	}
	var disabled, enabled int
	for name, file := range files() {
		switch {
		case strings.HasSuffix(name, ".disabled") && strings.Contains(file, "server_name second.example.test;"):
			disabled++
		case strings.HasSuffix(name, ".server"):
			enabled++
		}
	}
	if disabled != 1 || enabled != 1 || code("second.example.test") != "000" || code("hello.example.test") != "200" {
		t.Fatalf("after removal: %d disabled, %d enabled, second %s, hello %s", disabled, enabled, code("second.example.test"), code("hello.example.test"))
	}

	// A file the installer did not write that answers for a route's name is refused, untouched.
	cm, _ := cs.CoreV1().ConfigMaps("kyq-nginx").Get(ctx, "kyq-edge-config", metav1.GetOptions{})
	cm.Data["operator.disabled"] = "server {\n  server_name by-hand.example.test;\n}\n"
	cm.Data["zz-foreign.server"] = "server {\n  listen 443 ssl;\n  server_name hello.example.test;\n  ssl_reject_handshake on;\n}\n"
	if _, err := cs.CoreV1().ConfigMaps("kyq-nginx").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if out, err := try("apply"); err == nil || !strings.Contains(out, "hello.example.test is claimed by zz-foreign.server") {
		t.Fatalf("foreign file: %v\n%s", err, out)
	}
	cm, _ = cs.CoreV1().ConfigMaps("kyq-nginx").Get(ctx, "kyq-edge-config", metav1.GetOptions{})
	if cm.Data["zz-foreign.server"] == "" || cm.Data["operator.disabled"] == "" {
		t.Fatal("the installer changed a file it does not own")
	}
	delete(cm.Data, "zz-foreign.server")
	if _, err := cs.CoreV1().ConfigMaps("kyq-nginx").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	apply("foreign file gone")
	if got := code("hello.example.test"); got != "200" {
		t.Fatalf("hello after the foreign file: %s", got)
	}
	// Every change so far reached the same pods by reload: none was replaced or restarted.
	if after := pods(); after != before {
		t.Fatalf("route changes replaced or restarted pods:\n%s\n%s", before, after)
	}

	// A managed certificate needs the cluster's cert-manager: preflight refuses without it.
	sec, _ := cs.CoreV1().Secrets("kyq-edge").Get(ctx, "kyq-credentials", metav1.GetOptions{})
	if sec == nil || sec.Labels[managed.Label] != managed.Value {
		t.Fatal("the nginx edge did not create the edge credential namespace and Secret")
	}
	if _, err := cs.CoreV1().Secrets("kyq-edge").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "dns", Labels: map[string]string{managed.Label: managed.Value}},
		Data: map[string][]byte{"token": []byte(nginxTestToken)}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	st.Edges[0].Certificates = []stack.Certificate{{Name: "wild", Domains: []string{"*.example.test"}, Email: "ops@example.test", DNSProvider: "cloudflare", Credentials: stack.SecretRef{Secret: "dns", Key: "token"}}}
	managedRoute := hello
	managedRoute.Hostname, managedRoute.CertificateID, managedRoute.Certificate = "managed.example.test", 0, "wild"
	st.Edges[0].Routes = append(st.Edges[0].Routes, managedRoute)
	save(st)
	if out, err := try("preflight"); err == nil || !strings.Contains(out, "does not serve cert-manager.io/v1 issuers") {
		t.Fatalf("preflight without cert-manager: %v\n%s", err, out)
	}
	// apply runs the same preflight first, so it changes nothing either.
	if out, err := try("apply"); err == nil || !strings.Contains(out, "does not serve cert-manager.io/v1 issuers") || status(out, "k8s.edge.removed") != "" || code("managed.example.test") != "000" {
		t.Fatalf("apply without cert-manager: %v\n%s", err, out)
	}

	// With its API served, apply writes the Issuer and Certificate and waits; the test issues.
	crds := fetchPinned(t, certManagerCRDs, certManagerSHA256)
	kubectl := func(stdin []byte, args ...string) string {
		t.Helper()
		cmd := exec.Command("docker", append([]string{"exec", "-i", strings.TrimSuffix(client, "-client") + "-control-plane", "kubectl", "--kubeconfig", "/etc/kubernetes/admin.conf"}, args...)...)
		cmd.Stdin = bytes.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("kubectl %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	kubectl(crds, "apply", "--server-side", "-f", "-")
	kubectl(nil, "wait", "--for=condition=Established", "--timeout=60s", "crd/issuers.cert-manager.io", "crd/certificates.cert-manager.io")
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	dyn := dynamic.NewForConfigOrDie(cfg)
	gvr := func(resource string) schema.GroupVersionResource {
		return schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: resource}
	}
	issued := make(chan error, 1)
	go func() {
		// cert-manager's part: once the installer's Certificate exists, write its Secret and Ready.
		deadline := time.Now().Add(3 * time.Minute)
		for {
			c, err := dyn.Resource(gvr("certificates")).Namespace("kyq-nginx").Get(ctx, "kyq-wild", metav1.GetOptions{})
			if err == nil {
				certPEM, keyPEM := testCertificate(t)
				if _, err := cs.CoreV1().Secrets("kyq-nginx").Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "kyq-tls-wild", Labels: map[string]string{managed.Label: managed.Value}},
					Type:       corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: certPEM, corev1.TLSPrivateKeyKey: keyPEM},
				}, metav1.CreateOptions{}); err != nil {
					issued <- err
					return
				}
				unstructured.SetNestedSlice(c.Object, []any{map[string]any{"type": "Ready", "status": "True", "reason": "Ready", "message": "Certificate is up to date and has not expired",
					"lastTransitionTime": time.Now().UTC().Format(time.RFC3339)}}, "status", "conditions")
				_, err = dyn.Resource(gvr("certificates")).Namespace("kyq-nginx").UpdateStatus(ctx, c, metav1.UpdateOptions{})
				issued <- err
				return
			}
			if time.Now().After(deadline) {
				issued <- errors.New("the installer created no Certificate")
				return
			}
			time.Sleep(time.Second)
		}
	}()
	out := apply("managed certificate")
	if err := <-issued; err != nil {
		t.Fatal(err)
	}
	if status(out, "k8s.edge.cert.wild") != "ok" || code("managed.example.test") != "200" {
		t.Fatalf("managed route answered %s", code("managed.example.test"))
	}
	// The objects cert-manager's own schema accepted, strictly: Let's Encrypt, DNS-01 through
	// Cloudflare, the token by reference to its copy in the Issuer's namespace.
	issuer, err := dyn.Resource(gvr("issuers")).Namespace("kyq-nginx").Get(ctx, "kyq-wild", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	server, _, _ := unstructured.NestedString(issuer.Object, "spec", "acme", "server")
	email, _, _ := unstructured.NestedString(issuer.Object, "spec", "acme", "email")
	solvers, _, _ := unstructured.NestedSlice(issuer.Object, "spec", "acme", "solvers")
	ref, _, _ := unstructured.NestedStringMap(solvers[0].(map[string]any), "dns01", "cloudflare", "apiTokenSecretRef")
	if server != "https://acme-v02.api.letsencrypt.org/directory" || email != "ops@example.test" || ref["name"] != "kyq-wild-dns" || ref["key"] != "api-token" {
		t.Fatalf("issuer = %v", issuer.Object["spec"])
	}
	cert, _ := dyn.Resource(gvr("certificates")).Namespace("kyq-nginx").Get(ctx, "kyq-wild", metav1.GetOptions{})
	names, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	secretName, _, _ := unstructured.NestedString(cert.Object, "spec", "secretName")
	if !slices.Equal(names, []string{"*.example.test"}) || secretName != "kyq-tls-wild" {
		t.Fatalf("certificate = %v", cert.Object["spec"])
	}
	copied, _ := cs.CoreV1().Secrets("kyq-nginx").Get(ctx, "kyq-wild-dns", metav1.GetOptions{})
	if string(copied.Data["api-token"]) != nginxTestToken {
		t.Fatal("the Issuer's token copy differs")
	}
	// The token is in Secrets only: no ConfigMap, Issuer or Certificate holds it.
	for name, file := range files() {
		if strings.Contains(file, nginxTestToken) {
			t.Fatalf("token in config file %s", name)
		}
	}
	if b, _ := json.Marshal([]any{issuer.Object, cert.Object}); strings.Contains(string(b), nginxTestToken) {
		t.Fatal("token in a cert-manager object")
	}
	// A new certificate is a new volume: the pods were replaced, one node at a time, and the
	// rerun changes nothing.
	if out, err := try("apply"); err != nil || status(out, "k8s.edge.cert.wild") != "skipped" || status(out, "nginx.deploy") != "skipped" {
		t.Fatalf("rerun after the certificate: %v\n%s", err, out)
	}
	// The suite goes on with its own routes; the managed one leaves the plan and is disabled.
	st.Edges[0].Routes = st.Edges[0].Routes[:1]
	st.Edges[0].Certificates = nil
	save(st)
	apply("managed route removed")
	if got := code("managed.example.test"); got != "000" {
		t.Fatalf("removed managed route answered %s", got)
	}
}

// fetchPinned downloads url and refuses content other than the pinned digest.
func fetchPinned(t *testing.T, url, sum string) []byte {
	t.Helper()
	res, err := (&http.Client{Timeout: 2 * time.Minute}).Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if got := sha256.Sum256(b); err != nil || res.StatusCode != 200 || hex.EncodeToString(got[:]) != sum {
		t.Fatalf("%s: HTTP %d, %v, sha256 %x", url, res.StatusCode, err, got)
	}
	return b
}

// TestKubernetesNginxEdgeFailover runs the nginx edge on a cluster of three nodes and shows
// what it is for: requests through the Service keep succeeding while a pod is replaced, and
// while a node is gone the other node's pod serves and apply still changes and verifies routes.
func TestKubernetesNginxEdgeFailover(t *testing.T) {
	ctx := context.Background()
	work := t.TempDir()
	state := filepath.Join(work, "state")
	os.MkdirAll(state, 0o700)
	name, kc := kindClusterNodes(t, work, 2)
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	cs := kubernetes.NewForConfigOrDie(cfg)
	catalogDir := filepath.Join(work, "catalog")
	for _, src := range []string{"../../internal/catalog/testdata/apps/hello", "../../internal/catalog/apps/nginx"} {
		if err := os.CopyFS(filepath.Join(catalogDir, filepath.Base(src)), os.DirFS(src)); err != nil {
			t.Fatal(err)
		}
	}
	logAppOnFailure(t, cs, "nginx")
	plan := filepath.Join(state, "stack.yaml")
	os.WriteFile(plan, []byte("version: 1\ntargets:\n  - name: k8s\n    kubeconfig: "+kc+"\n    storage_class: standard\napps:\n  - name: nginx\n    target: k8s\n  - name: hello\n    target: k8s\n"), 0o600)
	apply := func(want string) string {
		t.Helper()
		var out bytes.Buffer
		err := cli.Run(ctx, []string{"apply", "--state", state}, cli.Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "e2e", Out: &out})
		t.Logf("apply (%s):\n%s", want, out.String())
		if err != nil {
			t.Fatalf("apply (%s): %v", want, err)
		}
		return out.String()
	}
	k := checkNginxInstall(t, ctx, cs, apply("install"))
	edgePods := func() []corev1.Pod {
		list, err := cs.CoreV1().Pods("kyq-nginx").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=nginx"})
		if err != nil {
			t.Fatal(err)
		}
		return list.Items
	}
	// A pod on each worker; the control plane's taint keeps one off it.
	if pods := edgePods(); len(pods) != 2 || pods[0].Spec.NodeName == pods[1].Spec.NodeName {
		t.Fatalf("edge pods = %d", len(pods))
	}
	ip := func(container string) string {
		return docker(t, "inspect", container, "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
	}
	client := name + "-client"
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", client).Run() })
	docker(t, "run", "-d", "--name", client, "--network", "kind", "--entrypoint", "sleep", kindNode, "1800")
	st, err := stack.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	st.Edges = []stack.Edge{{Target: "k8s", Provider: "lan", NPMURL: "kube://kyq-npm/app:81", AllowCIDRs: []string{ip(client) + "/32", "10.244.0.0/16"}, ClientPort: 30443, Routes: []stack.Route{{
		App: "hello", Hostname: "hello.example.test", ForwardHost: "app.kyq-hello.svc.cluster.local", ForwardPort: 8080, CertificateID: k.certID,
	}}}}
	save := func() {
		k.certify(st)
		b, _ := yaml.Marshal(st)
		os.WriteFile(plan, b, 0o600)
	}
	save()
	apply("route")

	// The node hello runs on stays; the other worker is the one that fails.
	hello, err := cs.CoreV1().Pods("kyq-hello").List(ctx, metav1.ListOptions{})
	if err != nil || len(hello.Items) != 1 {
		t.Fatalf("hello pods: %v", err)
	}
	stays := hello.Items[0].Spec.NodeName
	fails := name + "-worker"
	if stays == fails {
		fails = name + "-worker2"
	}
	if stays == name+"-control-plane" {
		t.Fatal("hello runs on the control plane")
	}
	get := func(node string) string {
		b, _ := exec.Command("docker", "exec", client, "curl", "-sk", "-o", "/dev/null", "-w", "%{http_code}", "-m", "5", "--resolve", "hello.example.test:30443:"+ip(node), "https://hello.example.test:30443/").Output()
		return strings.TrimSpace(string(b))
	}
	// Local policy: each worker's NodePort is served by that worker's own pod.
	for _, node := range []string{stays, fails} {
		if got := get(node); got != "200" {
			t.Fatalf("through %s: HTTP %s", node, got)
		}
	}

	// 1. A pod is deleted while a client inside the cluster keeps asking the Service: every
	// request succeeds, since the pod keeps serving until the Service has stopped sending to it.
	clusterIP, err := cs.CoreV1().Services("kyq-nginx").Get(ctx, "app-public", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	loop := `i=0; fail=0; while [ $i -lt 150 ]; do c=$(curl -sk -o /dev/null -w '%{http_code}' -m 3 --resolve hello.example.test:443:` + clusterIP.Spec.ClusterIP + ` https://hello.example.test/); [ "$c" = 200 ] || fail=$((fail+1)); i=$((i+1)); sleep 0.1; done; echo "$i $fail"`
	// From the node that stays, whose own address the route allows as a pod-network client.
	st.Edges[0].AllowCIDRs = append(st.Edges[0].AllowCIDRs, ip(stays)+"/32")
	save()
	apply("allow the node")
	result := make(chan string, 1)
	go func() {
		b, _ := exec.Command("docker", "exec", stays, "sh", "-c", loop).Output()
		result <- strings.TrimSpace(string(b))
	}()
	time.Sleep(3 * time.Second)
	var victim corev1.Pod
	for _, p := range edgePods() {
		if p.Spec.NodeName == fails {
			victim = p
		}
	}
	if err := cs.CoreV1().Pods("kyq-nginx").Delete(ctx, victim.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := <-result; got != "150 0" {
		t.Fatalf("requests through the Service while a pod was replaced (asked failed): %s", got)
	}
	apply("pod replaced") // health waits for the replacement
	if got := get(fails); got != "200" {
		t.Fatalf("replaced pod on %s: HTTP %s", fails, got)
	}

	// 2. A node is lost outright. The other node's pod serves throughout.
	go func() {
		b, _ := exec.Command("docker", "exec", client, "sh", "-c",
			`i=0; fail=0; while [ $i -lt 120 ]; do c=$(curl -sk -o /dev/null -w '%{http_code}' -m 3 --resolve hello.example.test:30443:`+ip(stays)+` https://hello.example.test:30443/); [ "$c" = 200 ] || fail=$((fail+1)); i=$((i+1)); sleep 0.5; done; echo "$i $fail"`).Output()
		result <- strings.TrimSpace(string(b))
	}()
	time.Sleep(2 * time.Second)
	docker(t, "stop", "-t", "0", fails)
	started := time.Now()
	// The Service inside the cluster recovers once the dead node's endpoint is withdrawn.
	inside := func() string {
		b, _ := exec.Command("docker", "exec", stays, "curl", "-sk", "-o", "/dev/null", "-w", "%{http_code}", "-m", "3", "--resolve", "hello.example.test:443:"+clusterIP.Spec.ClusterIP, "https://hello.example.test/").Output()
		return strings.TrimSpace(string(b))
	}
	for streak := 0; streak < 20; time.Sleep(500 * time.Millisecond) {
		if inside() == "200" {
			streak++
		} else {
			streak = 0
		}
		if time.Since(started) > 4*time.Minute {
			t.Fatal("the Service inside the cluster did not recover from the node's loss")
		}
	}
	t.Logf("MEASURED: the in-cluster Service answered 20 requests in a row %s after the node stopped", time.Since(started).Round(time.Second))
	if got := <-result; got != "120 0" {
		t.Fatalf("requests through the surviving node while the other was lost (asked failed): %s", got)
	}
	// While the node is gone apply still passes: a new route is rendered, loaded and verified
	// on the pod that answers, and the lost node is not waited for.
	for deadline := time.Now().Add(3 * time.Minute); ; time.Sleep(2 * time.Second) {
		n, err := cs.CoreV1().Nodes().Get(ctx, fails, metav1.GetOptions{})
		if err == nil && !slices.ContainsFunc(n.Status.Conditions, func(c corev1.NodeCondition) bool {
			return c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue
		}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stopped node still reports Ready")
		}
	}
	second := st.Edges[0].Routes[0]
	second.Hostname = "second.example.test"
	st.Edges[0].Routes = append(st.Edges[0].Routes, second)
	save()
	out := apply("route with a node down")
	if status(out, "nginx.health") == "failed" {
		t.Fatal("health waited for the lost node")
	}
	b, _ := exec.Command("docker", "exec", client, "curl", "-sk", "-o", "/dev/null", "-w", "%{http_code}", "-m", "5", "--resolve", "second.example.test:30443:"+ip(stays), "https://second.example.test:30443/").Output()
	if string(b) != "200" {
		t.Fatalf("route added with a node down: HTTP %s", b)
	}

	// The node returns: its pod loads the configuration it missed, and serves the new route.
	docker(t, "start", fails)
	for deadline := time.Now().Add(4 * time.Minute); ; time.Sleep(2 * time.Second) {
		b, _ := exec.Command("docker", "exec", client, "curl", "-sk", "-o", "/dev/null", "-w", "%{http_code}", "-m", "5", "--resolve", "second.example.test:30443:"+ip(fails), "https://second.example.test:30443/").Output()
		if string(b) == "200" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the returned node does not serve the route it missed: HTTP %s", b)
		}
	}
	rerun := apply("rerun with every node back")
	for _, line := range strings.Split(strings.TrimSpace(rerun), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && strings.Contains(f[0], ".") && f[1] != "skipped" {
			t.Errorf("rerun step not skipped: %s", line)
		}
	}
}
