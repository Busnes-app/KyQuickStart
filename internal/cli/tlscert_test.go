package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/remotecommand"
)

// tlsTestCert is a self-signed certificate for names, valid from notBefore for a day, and its key.
func tlsTestCert(t *testing.T, notBefore time.Time, names ...string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: notBefore, NotAfter: notBefore.Add(24 * time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := x509.MarshalPKCS8PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k})
}

// The chain is copied only where the catalog NPM or the nginx edge alone terminates the
// clients' TLS for the app's route.
func TestTLSCertRoute(t *testing.T) {
	route := func(exposure string) stack.Route {
		return stack.Route{App: "kydrive", Hostname: "drive.example.com", Exposure: exposure}
	}
	const npm = "kube://kyq-npm/app:81"
	for name, c := range map[string]struct {
		edge stack.Edge
		want bool
	}{
		"lan, catalog NPM":                 {stack.Edge{Provider: "lan", NPMURL: npm, Routes: []stack.Route{route("")}}, true},
		"lan, nginx edge":                  {stack.Edge{Provider: "lan", Proxy: "nginx", Routes: []stack.Route{route("")}}, true},
		"cloudflare internal, catalog NPM": {stack.Edge{Provider: "cloudflare", NPMURL: npm, Routes: []stack.Route{route("internal")}}, true},
		"cloudflare internal, nginx edge":  {stack.Edge{Provider: "cloudflare", Proxy: "nginx", Routes: []stack.Route{route("internal")}}, true},
		"cloudflare external (probe)":      {stack.Edge{Provider: "cloudflare", NPMURL: npm, Routes: []stack.Route{route("")}}, false},
		"cloudflare external with lan_dns": {stack.Edge{Provider: "cloudflare", NPMURL: npm, LANDNS: &stack.LANDNS{}, Routes: []stack.Route{route("")}}, false},
		"lan, NPM the operator runs":       {stack.Edge{Provider: "lan", NPMURL: "https://npm.lan.example", Routes: []stack.Route{route("")}}, false},
		"frp, NPM the operator runs":       {stack.Edge{Provider: "frp", NPMURL: "https://npm.lan.example", Routes: []stack.Route{route("")}}, false},
		"cloudflare-direct":                {stack.Edge{Provider: "cloudflare-direct", Routes: []stack.Route{route("")}}, false},
		"another app":                      {stack.Edge{Provider: "lan", NPMURL: npm, Routes: []stack.Route{{App: "kypost", Hostname: "post.example.com"}}}, false},
	} {
		if _, _, got := tlsCertRoute(&stack.Stack{Edges: []stack.Edge{c.edge}}, "kydrive"); got != c.want {
			t.Errorf("%s: copied = %v, want %v", name, got, c.want)
		}
		// Never both: the probe is set only where no chain is copied.
		if _, _, copied := tlsCertRoute(&stack.Stack{Edges: []stack.Edge{c.edge}}, "kydrive"); copied && tlsPinProbe(&stack.Stack{Edges: []stack.Edge{c.edge}}, "kydrive") {
			t.Errorf("%s: both the probe and a copy", name)
		}
	}
}

// Only CERTIFICATE blocks survive, re-encoded; a key anywhere in the source is dropped, and a
// chain whose leaf does not name the host or is not valid now is refused.
func TestPublicChain(t *testing.T) {
	now := time.Now()
	leaf, key := tlsTestCert(t, now.Add(-2*time.Hour), "drive.example.com")
	ca, caKey := tlsTestCert(t, now.Add(-2*time.Hour), "Test CA")
	raw := slices.Concat(key, leaf, []byte("junk\n"), caKey, ca, key)
	got, l, err := publicChain(raw, "drive.example.com", now)
	if err != nil || !bytes.Equal(got, slices.Concat(leaf, ca)) || l.Subject.CommonName != "drive.example.com" || bytes.Contains(got, []byte("PRIVATE")) {
		t.Fatalf("chain = %q, %v", got, err)
	}
	other, _ := tlsTestCert(t, now.Add(-2*time.Hour), "other.example.com")
	old, _ := tlsTestCert(t, now.Add(-48*time.Hour), "drive.example.com")
	future, _ := tlsTestCert(t, now.Add(time.Hour), "drive.example.com")
	for name, raw := range map[string][]byte{
		"another host":    slices.Concat(other, leaf),
		"expired":         old,
		"not yet valid":   future,
		"a key alone":     key,
		"empty":           nil,
		"over the bound":  bytes.Repeat(leaf, 64<<10/len(leaf)+1),
		"a broken block":  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("nope")}),
		"wildcard depth2": func() []byte { c, _ := tlsTestCert(t, now.Add(-time.Hour), "*.example.com"); return c }(),
	} {
		host := "drive.example.com"
		if name == "wildcard depth2" {
			host = "a.drive.example.com"
		}
		if _, _, err := publicChain(raw, host, now); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// localHost runs a Docker target's shell commands on this machine.
type localHost struct{ remote.Local }

func (localHost) Close() error { return nil }

// tlsHost is KyDrive's Docker target: a directory on this machine, written by the real shell.
func tlsHost(t *testing.T) (stack.Target, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return stack.Target{Name: "nas", Host: "192.0.2.1", User: "kyq", Root: root}, root
}

// tlsSession is a session with KyDrive on nas behind e on cluster k1 (client), its rendered
// Compose file naming the copied chain.
func tlsSession(e stack.Edge, nas stack.Target, client *kube.Client) *session {
	return &session{plan: &stack.Stack{Edges: []stack.Edge{e}}, installation: "test-installation", redact: &engine.Redactor{}, o: Options{Out: &bytes.Buffer{}},
		cat:    catalog.Catalog{"kydrive": {Compose: []byte(`KYDRIVE_TLS_CERT_FILE: "` + catalog.TLSCertFile + `"`)}},
		placed: map[string]stack.Target{"kydrive": nas}, npms: map[string]*edge.NPM{},
		drivers: map[string]driver{"nas": sshDriver{t: nas, c: localHost{}}, "k1": kubeDriver{c: client}}}
}

// tlsServingClient keeps the API fake but performs real TLS handshakes through
// its pod port-forward, as restore and refresh do even when no copy exists.
func tlsServingClient(t *testing.T, cs *fake.Clientset, cert, key []byte) *kube.Client {
	t.Helper()
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(nil)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	_, err = cs.CoreV1().Pods("kyq-nginx").Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-ready", Namespace: "kyq-nginx", Labels: map[string]string{"app.kubernetes.io/name": "nginx", managed.Label: managed.Value}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return kube.FakeWithForward(cs, func(ctx context.Context, ns, pod string, port int) (net.Conn, error) {
		if ns != "kyq-nginx" || pod != "app-ready" || port != 443 {
			t.Fatalf("unexpected probe %s/%s:%d", ns, pod, port)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
	})
}

// run runs the step s makes for KyDrive and returns its event status ("ok", "skipped",
// "failed") with the step, and the engine's error.
func runTLSStep(t *testing.T, s *session, results string, served ...func(context.Context, []byte) error) (string, *tlsCertStep, error) {
	t.Helper()
	st, err := s.tlsCertStep("kydrive")
	if err != nil || st == nil {
		t.Fatalf("step = %v, %v", st, err)
	}
	if len(served) > 0 {
		st.served = served[0]
	}
	st.bootstrap = true // this helper exercises apply's first-deploy path
	var status string
	e := &engine.Engine{Dir: results, Out: &bytes.Buffer{}, Redact: s.redact, Progress: func(ev engine.Event) { status = ev.Status }}
	err = e.Run(context.Background(), []engine.Step{st})
	return status, st, err
}

// noKeyWritten fails when any file below root holds key material.
func noKeyWritten(t *testing.T, root string) {
	t.Helper()
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("PRIVATE KEY")) {
				t.Errorf("%s holds a private key", p)
			}
		}
		return nil
	})
}

func inode(t *testing.T, p string) uint64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Ino
}

// Behind the nginx edge the chain comes from the route's TLS Secret, whose key is never
// written: the first run copies it 0644, a rerun changes nothing, a served renewal replaces
// it atomically, a backdated renewal not yet served waits, and another hostname is refused
// with the copy left as it was.
// A first copy is written however new: KyPost has none to start with otherwise.
func TestTLSCertFromNginxSecret(t *testing.T) {
	ctx := context.Background()
	nas, root := tlsHost(t)
	results := t.TempDir()
	cs := fake.NewClientset()
	now := time.Now()
	// Just issued, as on a first install: with no copy yet it is written at once.
	leaf, key := tlsTestCert(t, now.Add(-time.Minute), "drive.example.com")
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kyq-tls-wild", Namespace: "kyq-nginx", Labels: map[string]string{managed.Label: managed.Value}},
		Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: leaf, corev1.TLSPrivateKeyKey: key}}
	if _, err := cs.CoreV1().Secrets("kyq-nginx").Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	e := stack.Edge{Target: "k1", Provider: "lan", Proxy: "nginx", Routes: []stack.Route{{App: "kydrive", Hostname: "drive.example.com", Certificate: "wild"}}}
	s := tlsSession(e, nas, kube.New(cs))
	file := filepath.Join(root, "kydrive", "tls", "fullchain.pem")
	ready := true
	served := func(context.Context, []byte) error {
		if !ready {
			return errors.New("edge has not reloaded")
		}
		return nil
	}

	status, st, err := runTLSStep(t, s, results, served)
	if b, _ := os.ReadFile(file); err != nil || status != "ok" || !bytes.Equal(b, leaf) || st.issuer != "k1.edge.cert.wild" || !strings.Contains(st.note, "copied") {
		t.Fatalf("first run: %s %v, issuer %q, note %q, file %q", status, err, st.issuer, st.note, b)
	}
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Dir(file)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v", fi.Mode())
	}
	noKeyWritten(t, root)
	noKeyWritten(t, results)
	ino := inode(t, file)

	if status, _, err := runTLSStep(t, s, results, served); err != nil || status != "skipped" || inode(t, file) != ino {
		t.Fatalf("rerun: %s %v", status, err)
	}

	renewed, renewedKey := tlsTestCert(t, now.Add(-2*time.Hour), "drive.example.com")
	secret.Data = map[string][]byte{corev1.TLSCertKey: renewed, corev1.TLSPrivateKeyKey: renewedKey}
	cs.CoreV1().Secrets("kyq-nginx").Update(ctx, secret, metav1.UpdateOptions{})
	status, _, err = runTLSStep(t, s, results, served)
	if b, _ := os.ReadFile(file); err != nil || status != "ok" || !bytes.Equal(b, renewed) || inode(t, file) == ino {
		t.Fatalf("renewal: %s %v, file %q", status, err, b)
	}
	if left, _ := filepath.Glob(filepath.Join(root, "kydrive", "tls", ".kyq.*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}

	// The edge may not serve it yet: the copy stays, so the step passes as it did.
	fresh, _ := tlsTestCert(t, now.Add(-2*time.Hour), "drive.example.com")
	ready = false
	secret.Data = map[string][]byte{corev1.TLSCertKey: fresh}
	cs.CoreV1().Secrets("kyq-nginx").Update(ctx, secret, metav1.UpdateOptions{})
	status, st, err = runTLSStep(t, s, results, served)
	if b, _ := os.ReadFile(file); err != nil || status != "skipped" || !bytes.Equal(b, renewed) || !strings.Contains(st.note, "kept the current copy") {
		t.Fatalf("fresh renewal: %s %v, note %q", status, err, st.note)
	}
	if !strings.Contains(s.o.Out.(*bytes.Buffer).String(), "edge has not reloaded") {
		t.Fatal("deferred renewal was not logged on the skipped path")
	}
	ready = true
	status, _, err = runTLSStep(t, s, results, served)
	if b, _ := os.ReadFile(file); err != nil || status != "ok" || !bytes.Equal(b, fresh) {
		t.Fatalf("loaded renewal: %s %v", status, err)
	}

	other, _ := tlsTestCert(t, now.Add(-2*time.Hour), "other.example.com")
	secret.Data = map[string][]byte{corev1.TLSCertKey: other}
	cs.CoreV1().Secrets("kyq-nginx").Update(ctx, secret, metav1.UpdateOptions{})
	status, _, err = runTLSStep(t, s, results)
	if b, _ := os.ReadFile(file); err == nil || status != "failed" || !strings.Contains(err.Error(), "does not name drive.example.com") || !bytes.Equal(b, fresh) {
		t.Fatalf("other host: %s %v, file %q", status, err, b)
	}
	if r, ok := engine.Read(results, "kydrive.tls-cert"); !ok || r.Status != "failed" {
		t.Fatalf("result = %+v", r)
	}
	noKeyWritten(t, root)
	noKeyWritten(t, results)
}

// Behind the catalog NPM the chain is NPM's fullchain.pem, read with cat in NPM's pod; the
// API is asked only for the certificate's metadata, never its download, which bundles the key.
func TestTLSCertFromNPM(t *testing.T) {
	for provider, want := range map[string]string{"letsencrypt": "/etc/letsencrypt/live/npm-7/fullchain.pem", "other": "/data/custom_ssl/npm-7/fullchain.pem"} {
		nas, root := tlsHost(t)
		leaf, _ := tlsTestCert(t, time.Now().Add(-2*time.Hour), "drive.example.com")
		var paths []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			if r.URL.Path != "/api/nginx/certificates/7" || r.Method != http.MethodGet {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"id":7,"provider":%q,"domain_names":["drive.example.com"]}`, provider)
		}))
		defer srv.Close()
		cs := fake.NewClientset(&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "kyq-npm", Labels: map[string]string{"app.kubernetes.io/name": "npm", managed.Label: managed.Value}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}})
		var execs [][]string
		client := kube.Fake(cs, "https://k1.test", func(_ context.Context, ns, pod, container string, args []string, o remotecommand.StreamOptions) error {
			execs = append(execs, append([]string{ns, pod, container}, args...))
			_, err := o.Stdout.Write(leaf)
			return err
		})
		e := stack.Edge{Target: "k1", Provider: "lan", NPMURL: "kube://kyq-npm/app:81", Routes: []stack.Route{{App: "kydrive", Hostname: "drive.example.com", CertificateID: 7}}}
		s := tlsSession(e, nas, client)
		n, err := edge.NewNPM(e.NPMURL, "token", "test-installation", func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
		})
		if err != nil {
			t.Fatal(err)
		}
		s.npms["k1"] = n
		status, st, err := runTLSStep(t, s, t.TempDir())
		b, _ := os.ReadFile(filepath.Join(root, "kydrive", "tls", "fullchain.pem"))
		if err != nil || status != "ok" || !bytes.Equal(b, leaf) || st.issuer != "" {
			t.Fatalf("%s: %s %v, file %q", provider, status, err, b)
		}
		if !slices.EqualFunc(execs, [][]string{{"kyq-npm", "app-1", "app", "cat", want}}, slices.Equal) {
			t.Fatalf("%s: execs = %v", provider, execs)
		}
		if slices.ContainsFunc(paths, func(p string) bool { return strings.Contains(p, "download") }) {
			t.Fatalf("%s: asked NPM for %v", provider, paths)
		}
		noKeyWritten(t, root)
	}
}

// A freshly observed renewal may already have a backdated NotBefore. With no
// serving edge to confirm it, replacement must wait and leave the copy intact.
func TestTLSCertBackdatedRenewalNeedsServingEdge(t *testing.T) {
	ctx := context.Background()
	nas, root := tlsHost(t)
	old, _ := tlsTestCert(t, time.Now().Add(-3*time.Hour), "drive.example.com")
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kyq-tls-wild", Namespace: "kyq-nginx", Labels: map[string]string{managed.Label: managed.Value}},
		Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: old}}
	cs := fake.NewClientset(secret)
	e := stack.Edge{Target: "k1", Provider: "lan", Proxy: "nginx", Routes: []stack.Route{{App: "kydrive", Hostname: "drive.example.com", Certificate: "wild"}}}
	s := tlsSession(e, nas, kube.New(cs))
	results := t.TempDir()
	if _, _, err := runTLSStep(t, s, results); err != nil {
		t.Fatal(err)
	}
	renewed, _ := tlsTestCert(t, time.Now().Add(-2*time.Hour), "drive.example.com")
	secret.Data[corev1.TLSCertKey] = renewed
	if _, err := cs.CoreV1().Secrets("kyq-nginx").Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	status, _, err := runTLSStep(t, s, results)
	b, readErr := os.ReadFile(filepath.Join(root, "kydrive", "tls", "fullchain.pem"))
	if err != nil || status != "skipped" || readErr != nil || !bytes.Equal(b, old) {
		t.Fatalf("unconfirmed backdated renewal replaced copy: status %s, err %v, read %v", status, err, readErr)
	}
}

// A certificate an app's copy reads is issued just before that copy, ahead of the app's deploy.
func TestIssueFirst(t *testing.T) {
	copy := &tlsCertStep{app: "kypost", issuer: "k1.edge.cert.wild"}
	list := []engine.Step{noteStep{id: "npm.health"}, copy, noteStep{id: "kypost.deploy"}, noteStep{id: "k1.edge.removed"}, noteStep{id: "k1.edge.cert.wild"}, noteStep{id: "k1.edge.abc"}}
	want := []string{"npm.health", "k1.edge.cert.wild", "kypost.tls-cert", "kypost.deploy", "k1.edge.removed", "k1.edge.abc"}
	if got := ids(issueFirst(list)); !slices.Equal(got, want) {
		t.Fatalf("order = %v", got)
	}
	// Without the edge's steps (an upgrade) the copy reads what NPM already has.
	alone := []engine.Step{copy, noteStep{id: "kypost.deploy"}}
	if got := ids(issueFirst(alone)); !slices.Equal(got, []string{"kypost.tls-cert", "kypost.deploy"}) {
		t.Fatalf("order = %v", got)
	}
}

// Apply deploys the edge app before KyDrive, and copies KyDrive's chain just before its deploy.
func TestTLSCertBeforeDeploy(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	drivePlan(t, f, "192.0.2.1")
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	if npm, drive := slices.Index(s.order, "npm"), slices.Index(s.order, "kydrive"); npm < 0 || npm > drive {
		t.Fatalf("order = %v", s.order)
	}
	_, steps := driveSteps(t, f, (&fakeIdentity{export: driveExport(driveSecret)}).stream, &fakeDrive{files: map[string]string{}})
	if _, ok := steps["kydrive.tls-cert"].(*tlsCertStep); !ok {
		t.Fatalf("steps = %v", steps)
	}
	s, _ = driveSteps(t, f, (&fakeIdentity{export: driveExport(driveSecret)}).stream, &fakeDrive{files: map[string]string{}})
	list, err := s.deploySteps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	order := ids(list)
	if i := slices.Index(order, "kydrive.tls-cert"); i < 0 || order[i+1] != "kydrive.deploy" || slices.Index(order, "npm.health") > i {
		t.Fatalf("steps = %v", order)
	}
}

// RefreshTLS, the runner's daily job, takes the plan's locks, copies the chain of each app
// that reads one, and on a second run with the same certificate writes nothing.
func TestRefreshTLS(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/apps")
	f.o.Catalog = catalog.Embedded()
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	f.plan(t, "  - name: k1\n    kubeconfig: "+kc+"\ninstallation_id: 8c1e2173-3701-458c-8d27-8899dba16323\napps:\n  - name: nginx\n    target: k1\n  - name: kyidentity\n    target: k1\n  - name: kydrive\n    target: nas\n"+
		"edges:\n  - target: k1\n    provider: lan\n    proxy: nginx\n    allow_cidrs: [192.168.1.0/24]\n    client_port: 443\n    routes:\n"+
		"      - {app: kyidentity, hostname: identity.example.com, forward_host: app.kyq-kyidentity.svc.cluster.local, forward_port: 5867, certificate_secret: my-tls}\n"+
		"      - {app: kydrive, hostname: drive.example.com, forward_host: 192.0.2.1, forward_port: 18443, certificate_secret: my-tls}\n")
	leaf, key := tlsTestCert(t, time.Now().Add(-2*time.Hour), "drive.example.com", "identity.example.com")
	f.cs.CoreV1().Secrets("kyq-nginx").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "my-tls", Labels: map[string]string{managed.Label: managed.Value}},
		Type: corev1.SecretTypeTLS, Data: map[string][]byte{corev1.TLSCertKey: leaf, corev1.TLSPrivateKeyKey: key}}, metav1.CreateOptions{})
	if err := RefreshTLS(ctx, f.state, "test", f.o); err == nil {
		t.Fatal("refresh treated a missing copy as permission to bootstrap without a serving edge")
	}
	client := tlsServingClient(t, f.cs, leaf, key)
	previousDial := dialTarget
	dialTarget = func(ctx context.Context, target stack.Target, config remote.SSHConfig, needsStorage bool) (driver, error) {
		if target.Kubernetes() {
			return kubeDriver{c: client, needsStorage: needsStorage, storageClass: target.StorageClass}, nil
		}
		return previousDial(ctx, target, config, needsStorage)
	}
	if err := RefreshTLS(ctx, f.state, "test", f.o); err != nil {
		t.Fatalf("%v\n%s", err, f.out.String())
	}
	file := filepath.Join(f.h.root, "kydrive", "tls", "fullchain.pem")
	if b, _ := os.ReadFile(file); !bytes.Equal(b, leaf) || !strings.Contains(f.out.String(), "kydrive.tls-cert") {
		t.Fatalf("file %q\n%s", b, f.out.String())
	}
	ino := inode(t, file)
	f.out.Reset()
	if err := RefreshTLS(ctx, f.state, "test", f.o); err != nil || inode(t, file) != ino || !strings.Contains(f.out.String(), "kydrive.tls-cert") || !strings.Contains(f.out.String(), "skipped") {
		t.Fatalf("rerun: %v\n%s", err, f.out.String())
	}
	noKeyWritten(t, f.h.root)
	noKeyWritten(t, f.state)
}
