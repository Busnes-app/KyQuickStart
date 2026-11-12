package edge

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// fakeCluster is an nginx edge's cluster in memory. Its pods load a written configuration at
// once unless stale is set, and reject one containing "broken".
type fakeCluster struct {
	files   map[string]string
	version int
	pods    []string
	stale   bool
	loaded  string // the generation the pods run
	tls     map[string][]byte
	secrets map[string][]byte // kyq-edge Secrets, as "name/key"
	tokens  map[string][]byte
	objects map[string]map[string]any // "Kind/name" -> spec
	hashes  map[string]string
	ready   bool // what cert-manager reports for a Certificate
	writes  int
}

func newFakeCluster() *fakeCluster {
	f := &fakeCluster{files: map[string]string{NginxMainFile: NginxMain}, version: 1, pods: []string{"app-a", "app-b"},
		tls: map[string][]byte{}, secrets: map[string][]byte{}, tokens: map[string][]byte{}, objects: map[string]map[string]any{}, hashes: map[string]string{}}
	f.loaded = NginxGeneration(f.files)
	return f
}

func (f *fakeCluster) EdgeSecret(_ context.Context, name, key string) ([]byte, error) {
	v, ok := f.secrets[name+"/"+key]
	if !ok {
		return nil, errors.New("secret absent")
	}
	return v, nil
}
func (f *fakeCluster) LANDNSLedger(context.Context) ([]byte, error)          { return nil, nil }
func (f *fakeCluster) SetLANDNSLedger(context.Context, []byte) error         { return nil }
func (f *fakeCluster) ACMEToken(_ context.Context, n string) ([]byte, error) { return f.tokens[n], nil }
func (f *fakeCluster) SetACMEToken(_ context.Context, n string, token []byte) error {
	f.tokens[n] = token
	return nil
}
func (f *fakeCluster) EdgeFiles(context.Context) (map[string]string, string, error) {
	return maps.Clone(f.files), fmt.Sprint(f.version), nil
}
func (f *fakeCluster) SetEdgeFiles(_ context.Context, files map[string]string, version string) error {
	if version != fmt.Sprint(f.version) {
		return engine.ErrTransient
	}
	f.files, f.version, f.writes = maps.Clone(files), f.version+1, f.writes+1
	return nil
}
func (f *fakeCluster) EdgeLoaded(context.Context, string) (map[string]string, error) {
	reason := ""
	for name, file := range f.files {
		if strings.HasSuffix(name, enabledExt) && strings.Contains(file, "broken") {
			reason = "nginx: [emerg] unknown directive \"broken\""
		}
	}
	if !f.stale && reason == "" {
		f.loaded = NginxGeneration(f.files)
	}
	out := map[string]string{}
	for _, p := range f.pods {
		out[p] = f.loaded + "\n" + reason
	}
	return out, nil
}
func (f *fakeCluster) TLSCertificate(_ context.Context, secret string) ([]byte, error) {
	return f.tls[secret], nil
}
func (f *fakeCluster) CertManager(_ context.Context, kind, name string) (string, bool, string, error) {
	return f.hashes[kind+"/"+name], f.ready, "Issuing: waiting for the DNS record", nil
}
func (f *fakeCluster) ApplyCertManager(_ context.Context, kind, name, hash string, spec map[string]any) error {
	f.objects[kind+"/"+name], f.hashes[kind+"/"+name] = spec, hash
	return nil
}

// testCertificate is a self-signed certificate for names, valid until notAfter.
func testCertificate(t *testing.T, notAfter time.Time, names ...string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	k, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k})
}

func nginxFixture(t *testing.T) (*Nginx, *fakeCluster, stack.Edge) {
	t.Helper()
	old := [3]time.Duration{nginxWait, nginxIssueWait, nginxPoll}
	nginxWait, nginxIssueWait, nginxPoll = 30*time.Millisecond, 30*time.Millisecond, time.Millisecond
	t.Cleanup(func() { nginxWait, nginxIssueWait, nginxPoll = old[0], old[1], old[2] })
	f := newFakeCluster()
	f.tls["wild"], _ = testCertificate(t, time.Now().Add(time.Hour), "*.example.test")
	n, err := NewNginx(f, testID, &engine.Redactor{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.Close)
	e := stack.Edge{Target: "k1", Provider: "lan", Proxy: "nginx", AllowCIDRs: []string{"192.168.1.0/24"}, ClientPort: 443, Routes: []stack.Route{
		{App: "hello", Hostname: "hello.example.test", ForwardHost: "app.kyq-hello.svc.cluster.local", ForwardPort: 8080, ForwardScheme: "http", CertificateSecret: "wild"},
		{App: "other", Hostname: "other.example.test", ForwardHost: "192.168.1.9", ForwardPort: 8443, ForwardScheme: "https", CertificateSecret: "wild"},
	}}
	return n, f, e
}

func runSteps(t *testing.T, steps []engine.Step) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := (&engine.Engine{Dir: t.TempDir(), Out: &out, Redact: &engine.Redactor{}}).Run(context.Background(), steps)
	return out.String(), err
}

// The nginx edge's server block holds exactly the configuration NPM's advanced_config holds,
// from the one generator, with the upstream written out where NPM's template has variables.
func TestNginxSharesNPMConfiguration(t *testing.T) {
	n, _, e := nginxFixture(t)
	npm, err := NewNPM("https://npm.example.test", "token", testID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range []stack.Edge{e,
		{Target: "k1", Provider: "cloudflare", Proxy: "nginx", ConnectorCIDR: "10.0.0.7/32", Routes: e.Routes},
	} {
		for _, r := range edge.Routes {
			r.Paths, r.DenyPaths = nil, []string{"^/admin"}
			file := n.want(edge, r)
			advanced := npm.config(edge, r, 1).Advanced
			marker, body, _ := strings.Cut(advanced, "\n")
			upstream := fmt.Sprintf("%s://%s:%d", r.ForwardScheme, r.ForwardHost, r.ForwardPort)
			want := marker + "\nserver {\nlisten 443 ssl;\nhttp2 on;\nserver_name " + r.Hostname + ";\nssl_certificate /etc/nginx/tls/wild/tls.crt;\nssl_certificate_key /etc/nginx/tls/wild/tls.key;\n" +
				strings.ReplaceAll(body, "$forward_scheme://$server:$port", upstream) + "}\n"
			if file != want {
				t.Fatalf("%s on a %s edge:\n%s\nwant:\n%s", r.Hostname, edge.Provider, file, want)
			}
			if !strings.Contains(file, "proxy_pass "+upstream+"$request_uri;") || !strings.Contains(file, "location ~ ^/admin {\n  return 404;") {
				t.Fatalf("%s: upstream or denied path missing:\n%s", r.Hostname, file)
			}
		}
	}
	lan, tunnel := n.want(e, e.Routes[0]), n.want(stack.Edge{Target: "k1", Provider: "cloudflare", ConnectorCIDR: "10.0.0.7/32", Routes: e.Routes}, e.Routes[0])
	if !strings.Contains(lan, "allow 192.168.1.0/24;\ndeny all;") || strings.Contains(lan, "real_ip_header") {
		t.Fatalf("internal route trusts a header or lost its allow-list:\n%s", lan)
	}
	if !strings.Contains(tunnel, "set_real_ip_from 10.0.0.7/32;\nreal_ip_recursive off;\nif ($realip_remote_addr != 10.0.0.7) { return 403; }") || !strings.Contains(tunnel, "real_ip_header CF-Connecting-IP;") {
		t.Fatalf("external route does not pin its connector:\n%s", tunnel)
	}
}

func TestNginxRoutes(t *testing.T) {
	n, f, e := nginxFixture(t)
	ctx := context.Background()
	if err := n.Preflight(ctx, e); err != nil {
		t.Fatal(err)
	}
	out, err := runSteps(t, n.Steps(e))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	key := routeKey("hello.example.test")
	if got := f.files[key+enabledExt]; !strings.HasPrefix(got, "# kyquickstart:"+testID+":k1:hello:hello.example.test\n") || len(f.files) != 3 {
		t.Fatalf("files = %v", f.files)
	}
	if got := n.Steps(e)[1].ID(); got != "k1.edge."+key {
		t.Fatalf("route step = %s", got)
	}
	if app := RouteApp(n.Steps(e)[2]); app != "other" {
		t.Fatalf("RouteApp = %q", app)
	}
	// A rerun changes nothing.
	writes := f.writes
	for _, s := range n.Steps(e) {
		if done, err := s.Inspect(ctx); !done || err != nil {
			t.Fatalf("%s: done %v, %v", s.ID(), done, err)
		}
	}
	// A pod that has not loaded the configuration makes the route unverified.
	f.stale, f.loaded = true, "old"
	if done, _ := n.Steps(e)[1].Inspect(ctx); done {
		t.Fatal("route reported healthy while a pod runs another configuration")
	}
	if err := n.Steps(e)[1].Verify(ctx); err == nil || !strings.Contains(err.Error(), "app-a has not loaded") {
		t.Fatalf("verify = %v", err)
	}
	f.stale = false
	if f.writes != writes {
		t.Fatal("inspection wrote")
	}

	// A hostname that left the routes is disabled, never deleted, and its file stays.
	removed := e
	removed.Routes = e.Routes[:1]
	if out, err := runSteps(t, n.Steps(removed)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	gone := routeKey("other.example.test")
	if _, on := f.files[gone+enabledExt]; on || !strings.Contains(f.files[gone+disabledExt], "server_name other.example.test;") {
		t.Fatalf("removed route: %v", f.files)
	}
	if f.loaded != NginxGeneration(f.files) {
		t.Fatal("pods not waited for after disabling")
	}
	// Back in the routes, it is enabled again from the plan, and the disabled copy goes.
	if out, err := runSteps(t, n.Steps(e)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, off := f.files[gone+disabledExt]; off || f.files[gone+enabledExt] != n.want(e, e.Routes[1]) {
		t.Fatalf("re-enabled route: %v", f.files)
	}
}

func TestNginxOwnership(t *testing.T) {
	n, f, e := nginxFixture(t)
	ctx := context.Background()
	key := routeKey("hello.example.test")
	for name, file := range map[string]string{
		key + enabledExt:    "# kyquickstart:another-installation:k1:hello:hello.example.test\nserver { server_name hello.example.test; }\n",
		key + disabledExt:   "# kyquickstart:" + testID + ":k1:different:hello.example.test\nserver { server_name hello.example.test; }\n",
		"mine" + enabledExt: "server {\n  server_name  a.example.test hello.example.test;\n}\n",
		"wild" + enabledExt: "server {\nserver_name *.example.test;\n}\n",
	} {
		f.files = map[string]string{NginxMainFile: NginxMain, name: file}
		before := maps.Clone(f.files)
		if err := n.Preflight(ctx, e); err == nil || !strings.Contains(err.Error(), "hello.example.test is claimed") {
			t.Fatalf("%s: preflight = %v", name, err)
		}
		if _, err := runSteps(t, n.Steps(e)); err == nil || !maps.Equal(before, f.files) {
			t.Fatalf("%s: a foreign file was changed or served over: %v", name, err)
		}
	}
	// Files without this installation's mark are never disabled as removed routes.
	f.files = map[string]string{NginxMainFile: NginxMain, "hand" + enabledExt: "server {\nserver_name hand.example.org;\n}\n"}
	if out, err := runSteps(t, n.Steps(e)); err != nil || f.files["hand"+enabledExt] == "" {
		t.Fatalf("hand-written file: %v\n%s", err, out)
	}
	if _, err := NewNginx(f, "", &engine.Redactor{}); err == nil {
		t.Fatal("accepted no installation ID")
	}
}

func TestNginxCertificateRequired(t *testing.T) {
	n, f, e := nginxFixture(t)
	ctx := context.Background()
	expired, _ := testCertificate(t, time.Now().Add(-time.Minute), "*.example.test")
	other, _ := testCertificate(t, time.Now().Add(time.Hour), "*.example.org")
	for why, pem := range map[string][]byte{"absent": nil, "not a certificate": []byte("junk"), "expired": expired, "another name": other} {
		f.tls["wild"] = pem
		if err := n.Preflight(ctx, e); err == nil || !strings.Contains(err.Error(), "TLS Secret wild") {
			t.Fatalf("%s: preflight = %v", why, err)
		}
		if _, err := runSteps(t, n.Steps(e)); err == nil || len(f.files) != 1 {
			t.Fatalf("%s: a route was enabled without a valid certificate: %v %v", why, err, f.files)
		}
	}
	// Before the nginx app's first deploy there is no namespace to hold the Secret: preflight
	// leaves the check to the route's step.
	fresh, _ := NewNginx(versionless{f}, testID, &engine.Redactor{})
	if err := fresh.Preflight(ctx, e); err != nil {
		t.Fatalf("preflight before the first deploy: %v", err)
	}
}

// versionless is a cluster whose nginx app is not deployed yet.
type versionless struct{ *fakeCluster }

func (versionless) EdgeFiles(context.Context) (map[string]string, string, error) {
	return map[string]string{}, "", nil
}

// A configuration the pods reject is disabled again: the route is not left half-applied, and
// the reason reaches the operator.
func TestNginxRejectedConfigurationIsDisabled(t *testing.T) {
	n, f, e := nginxFixture(t)
	e.Routes = e.Routes[:1]
	e.Routes[0].ForwardHost = "broken.example.test"
	out, err := runSteps(t, n.Steps(e))
	if err == nil || !strings.Contains(err.Error(), "rejected the configuration") || !strings.Contains(err.Error(), "unknown directive") {
		t.Fatalf("%v\n%s", err, out)
	}
	key := routeKey("hello.example.test")
	if _, on := f.files[key+enabledExt]; on || f.files[key+disabledExt] == "" {
		t.Fatalf("rejected route left enabled: %v", f.files)
	}
}

func TestNginxCertificates(t *testing.T) {
	n, f, e := nginxFixture(t)
	ctx := context.Background()
	const token = "cloudflare-token-0123456789abcdef"
	f.secrets["dns/token"] = []byte(token + "\n")
	e.Certificates = []stack.Certificate{{Name: "wild", Domains: []string{"example.test", "*.example.test"}, Email: "ops+acme@example.test", DNSProvider: "cloudflare", Credentials: stack.SecretRef{Secret: "dns", Key: "token"}}}
	e.Routes = []stack.Route{{App: "hello", Hostname: "hello.example.test", ForwardHost: "app.kyq-hello.svc.cluster.local", ForwardPort: 8080, ForwardScheme: "http", Certificate: "wild"}}
	f.tls[ManagedTLSSecret("wild")], _ = testCertificate(t, time.Now().Add(time.Hour), "example.test", "*.example.test")
	if got := TLSSecrets(e); len(got) != 1 || got[0] != "kyq-tls-wild" {
		t.Fatalf("TLS Secrets = %v", got)
	}
	if err := n.Preflight(ctx, e); err != nil {
		t.Fatal(err)
	}

	// Not issued in time: the step fails with cert-manager's reason, and the route is not enabled.
	out, err := runSteps(t, n.Steps(e))
	if err == nil || !strings.Contains(err.Error(), "waiting for the DNS record") || len(f.files) != 1 {
		t.Fatalf("%v\n%s\n%v", err, out, f.files)
	}
	issuer, cert := f.objects["Issuer/kyq-wild"], f.objects["Certificate/kyq-wild"]
	acme := issuer["acme"].(map[string]any)
	solver := acme["solvers"].([]any)[0].(map[string]any)["dns01"].(map[string]any)["cloudflare"].(map[string]any)["apiTokenSecretRef"].(map[string]any)
	if acme["server"] != "https://acme-v02.api.letsencrypt.org/directory" || acme["email"] != "ops+acme@example.test" || solver["name"] != "kyq-wild-dns" || solver["key"] != "api-token" {
		t.Fatalf("issuer = %v", issuer)
	}
	if cert["secretName"] != "kyq-tls-wild" || fmt.Sprint(cert["dnsNames"]) != "[example.test *.example.test]" || cert["issuerRef"].(map[string]any)["kind"] != "Issuer" {
		t.Fatalf("certificate = %v", cert)
	}
	// The token is in its Secret only: not in an object, a file, an error or the output.
	if string(f.tokens["kyq-wild-dns"]) != token {
		t.Fatalf("issuer's token copy = %q", f.tokens["kyq-wild-dns"])
	}
	if leak := fmt.Sprint(f.objects, f.files, f.hashes, err, out); strings.Contains(leak, token) {
		t.Fatal("DNS token left its Secret")
	}

	f.ready = true
	if out, err := runSteps(t, n.Steps(e)); err != nil || len(f.files) != 2 {
		t.Fatalf("%v\n%s", err, out)
	}
	step := n.Steps(e)[1]
	if done, err := step.Inspect(ctx); !done || err != nil {
		t.Fatalf("issued certificate: %v %v", done, err)
	}
	// A rotated token reaches the Issuer's copy; a changed domain list reaches cert-manager.
	f.secrets["dns/token"] = []byte("cloudflare-token-rotated-0123456789")
	if done, _ := step.Inspect(ctx); done {
		t.Fatal("rotated token not noticed")
	}
	if err := step.Apply(ctx); err != nil || string(f.tokens["kyq-wild-dns"]) != "cloudflare-token-rotated-0123456789" {
		t.Fatalf("rotation: %v", err)
	}
	e.Certificates[0].Domains = []string{"example.test"}
	if done, _ := n.Steps(e)[1].Inspect(ctx); done {
		t.Fatal("changed domains not noticed")
	}
	f.secrets["dns/token"] = []byte("not a token")
	if err := n.Preflight(ctx, e); err == nil || !strings.Contains(err.Error(), "not a Cloudflare API token") || strings.Contains(err.Error(), "not a token") {
		t.Fatalf("malformed token: %v", err)
	}
}

func TestNginxGeneration(t *testing.T) {
	a := map[string]string{NginxMainFile: "main", "b" + enabledExt: "B", "a" + enabledExt: "A", "c" + disabledExt: "C"}
	b := maps.Clone(a)
	delete(b, "c"+disabledExt)
	if NginxGeneration(a) != NginxGeneration(b) {
		t.Fatal("a disabled file changed the generation")
	}
	b["a"+enabledExt] = "A2"
	if NginxGeneration(a) == NginxGeneration(b) || len(NginxGeneration(a)) != 64 {
		t.Fatal("an enabled file did not change the generation")
	}
	if got := serverNames("server {\n  server_name one.example.test  two.example.test;\n}\nserver {\nserver_name three.example.test;\n}\n"); len(got) != 3 || got[1] != "two.example.test" {
		t.Fatalf("server names = %q", got)
	}
}
