package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	dnsSetupID = "8a1f3c5e-7b9d-4e2f-a6c8-0d2e4f6a8b1c"
	dnsBundle  = "/data/installer/" + dnsSetupID + "/setup.json"
)

// dnsPlan installs KyIdentity on cluster k1 and KyDNS on Docker host nas, routed through k1's
// lan edge, whose egress_address egress is ("" for none). forward is KyDNS's forward_host.
func dnsPlan(t *testing.T, f *fixture, target, forward, egress, bundle string) {
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	if egress != "" {
		egress = "    egress_address: " + egress + "\n"
	}
	f.plan(t, "  - name: k1\n    kubeconfig: "+kc+"\napps:\n  - name: npm\n    target: k1\n  - name: kyidentity\n    target: k1\n  - name: kydns\n    target: "+target+"\n"+
		"edges:\n  - target: k1\n    provider: lan\n    npm_url: kube://kyq-npm/app:81\n    allow_cidrs: [192.168.1.0/24]\n"+egress+"    routes:\n"+
		"      - app: kyidentity\n        hostname: identity.example.com\n        forward_host: app.kyq-kyidentity.svc.cluster.local\n        forward_port: 5867\n        certificate_id: 1\n"+
		"      - app: kydns\n        hostname: dns.example.com\n        forward_host: "+forward+"\n        forward_port: 18053\n        certificate_id: 1\n        status_path: /healthz\n"+
		"integrations:\n  - product: kyidentity\n    target: k1\n    bundle: /data/installer/271333e2-c221-4601-b249-563b3d828530/setup.json\n"+identityPrep+
		"  - product: kydns\n    target: "+target+"\n    bundle: "+bundle+"\n    identity_client: kydns\n")
}

func dnsExport(setupID, secret string) string {
	return `{"schema":"kyidentity.client-credentials/1","setupId":"` + setupID + `","issuerUrl":"https://identity.example.com","clientId":"kydns",` +
		`"redirectUris":["https://dns.example.com/auth/sso/callback"],"directoryUrl":"https://dns.example.com/api/v1/installer/directory",` +
		`"clientSecret":"` + secret + `","directorySecret":"` + directorySecret + `",` +
		`"administrator":{"subject":"` + adminSubject + `","username":"owner-admin","role":"kydns.admin"}}`
}

const wantDNSClient = `{"id":"kydns","name":"KyDNS","redirectUris":["https://dns.example.com/auth/sso/callback"],"postLogoutUris":null,"administratorRole":"kydns.admin","suiteWebhook":{"url":"https://dns.example.com/api/v1/installer/directory"}}`

// recorder runs commands on the fake Docker host and keeps each with its stdin.
type recorder struct {
	*dockerHost
	cmds, stdins []string
}

func (r *recorder) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	r.cmds, r.stdins = append(r.cmds, cmd), append(r.stdins, string(stdin))
	return r.dockerHost.Run(ctx, cmd, stdin)
}

// fakeDNS is KyDNS's container behind docker exec. It has no shell: only its apply-setup, which
// reads the bundle from the host's bind directory, as the real container does through /data.
type fakeDNS struct {
	root    string // the target's root
	calls   []string
	applied bool
	bundle  string
}

func (d *fakeDNS) exec(t *testing.T) func(string, []byte) ([]byte, error) {
	return func(cmd string, stdin []byte) ([]byte, error) {
		rest, ok := strings.CutPrefix(cmd, dockerAppContainer("kydns")+`docker exec "$c" `)
		if !ok {
			t.Fatalf("exec outside KyDNS's container: %s", cmd)
		}
		out, err := remote.Local{}.Run(context.Background(), `printf '%s\0' `+rest, nil)
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
		d.calls = append(d.calls, strings.Join(args, " "))
		switch {
		case args[0] != "/usr/local/bin/kydns" || args[1] != "apply-setup" || slices.Contains(args, "--restart"):
			return nil, errors.New("unexpected command")
		case slices.Contains(args, "--capabilities"):
			return []byte(`{"schema":"ky.setup-capabilities/1","service":"kydns","bundleSchema":"ky.product-setup/1","features":["kyidentity-oidc","exact-admin-subjects","admin-only","directory-webhook","check"]}`), nil
		}
		file := args[slices.Index(args, "--file")+1]
		b, err := os.ReadFile(filepath.Join(d.root, "kydns", file))
		check := slices.Contains(args, "--check")
		if err != nil || check && !d.applied {
			return nil, errors.New("not applied")
		}
		d.bundle = string(b)
		created := `[]`
		if !check && !d.applied {
			d.applied, created = true, `[{"kind":"identity","name":"owner-admin"}]`
		}
		return []byte(`{"schema":"ky.product-setup-report/1","service":"kydns","setupId":"` + dnsSetupID + `","created":` + created + `,"present":[],"pending":[]}`), nil
	}
}

// dnsSession is the plan's session over KyIdentity's ready pod and the recorded Docker host.
func dnsSession(t *testing.T, f *fixture, identity *fakeIdentity, dns *fakeDNS) (*session, *recorder) {
	t.Helper()
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	f.cs.CoreV1().Pods("kyq-kyidentity").Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Labels: map[string]string{"app.kubernetes.io/name": "kyidentity", managed.Label: managed.Value}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}, metav1.CreateOptions{})
	rec := &recorder{dockerHost: f.h}
	s.drivers["k1"] = kubeDriver{c: kube.Fake(f.cs, "https://k1.test", identity.stream)}
	s.drivers["nas"] = sshDriver{t: s.placed["kydns"], c: rec}
	f.h.exec = dns.exec(t)
	return s, rec
}

// listenSetupSocket opens KyDNS's setup socket in its host data directory, as the running
// container does. A relative bind keeps the path under the kernel's limit.
func listenSetupSocket(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "kydns/data/installer")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	t.Chdir(dir)
	l, err := net.Listen("unix", "setup.sock")
	t.Chdir(wd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
}

// AC5: one apply installing KyIdentity and KyDNS puts KyDNS's admin-only client in the first
// setup; a differing hand-written client is refused. AC13's handover, as composed.
func TestKyDNSClientFromCatalog(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	dnsPlan(t, f, "nas", "192.0.2.1", "192.168.1.20", dnsBundle)
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(s.integrations[0].Identity.Clients); string(b) != "["+wantDNSClient+"]" || len(s.amendments) != 0 || !slices.Equal(s.derived, []string{"kydns"}) {
		t.Fatalf("derived clients = %s, amendments %d", b, len(s.amendments))
	}
	if saved := f.stack(t).Integrations[0].Identity.Clients; len(saved) != 0 {
		t.Fatalf("saved plan changed: %v", saved)
	}
	for _, want := range []string{
		"KyDNS at https://dns.example.com/: sign in through KyIdentity as owner-admin, the one account it admits",
		"It answers DNS at 192.0.2.1:53 (UDP and TCP)",
		"ports 53 and 18053 are open on 192.0.2.1. 18053, its admin UI, serves only the edge",
		"DNS answers private address ranges only",
		"Point clients at 192.0.2.1 for DNS: your router's DHCP DNS server",
		"Set one on nas:\n       docker exec -it kyq-kydns-app-1 /usr/local/bin/kydns admin reset-password",
		"Pair it with KyRecovery",
	} {
		if !strings.Contains(s.handover, want) {
			t.Errorf("handover lacks %q:\n%s", want, s.handover)
		}
	}
	// The compose file the session deploys: the route's address and the edge's egress address.
	c := string(s.cat["kydns"].Compose)
	if !strings.Contains(c, `ports: ["192.0.2.1:18053:8053", "192.0.2.1:53:53/udp", "192.0.2.1:53:53/tcp"]`) || !strings.Contains(c, `KYDNS_TRUSTED_PROXIES: "192.168.1.20"`) || s.cat["kydns"].UsesRoute() {
		t.Fatalf("compose:\n%s", c)
	}
	// An IPv6 egress address; stack validation keeps forward_host to IPv4 and names today.
	dnsPlan(t, f, "nas", "192.0.2.1", "fd00::20", dnsBundle)
	if s, err := load(f.state, nil, f.o); err != nil || !strings.Contains(string(s.cat["kydns"].Compose), `KYDNS_TRUSTED_PROXIES: "fd00::20"`) {
		t.Fatalf("IPv6 egress address: %v", err)
	}

	written := strings.Replace(identityPrep, "      clients: []\n", "      clients:\n        - id: kydns\n          name: KyDNS\n          redirect_uris: [https://dns.example.com/auth/sso/callback]\n          administrator_role: kydns.admin\n          suite_webhook: {url: https://dns.example.com/api/v1/installer/directory}\n", 1)
	if written == identityPrep {
		t.Fatal("identityPrep changed")
	}
	for _, c := range []struct{ prep, want string }{
		{written, ""},
		{strings.Replace(written, "kydns.admin\n", "kydns.admin\n          everyday_role: kydns.user\n", 1), "differs from the catalog's KyDNS client"},
	} {
		dnsPlan(t, f, "nas", "192.0.2.1", "192.168.1.20", dnsBundle)
		b, _ := os.ReadFile(filepath.Join(f.state, "stack.yaml"))
		os.WriteFile(filepath.Join(f.state, "stack.yaml"), bytes.Replace(b, []byte(identityPrep), []byte(c.prep), 1), 0o600)
		_, err := load(f.state, nil, f.o)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("hand-written client: %v, want %q", err, c.want)
		}
	}
}

// AC4: plan and review refuse KyDNS on Kubernetes, without its edge's egress_address or with
// a hostname forward_host, before any connection; the same plans without KyDNS pass.
func TestKyDNSPlanRefusals(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
		t.Fatal("connected to a target")
		return nil, nil
	}
	without := func() {
		p := filepath.Join(f.state, "stack.yaml")
		st := f.stack(t)
		st.Apps = slices.DeleteFunc(st.Apps, func(a stack.App) bool { return a.Name == "kydns" })
		st.Integrations = slices.DeleteFunc(st.Integrations, func(i stack.Integration) bool { return i.Product == "kydns" })
		st.Edges[0].Routes = slices.DeleteFunc(st.Edges[0].Routes, func(r stack.Route) bool { return r.App == "kydns" })
		b, _ := yaml.Marshal(st)
		os.WriteFile(p, b, 0o600)
	}
	for _, c := range []struct {
		name, target, forward, egress string
		want                          []string
	}{
		{"kubernetes", "k1", "192.0.2.1", "192.168.1.20", []string{"kydns", "has no Kubernetes deployment; place it on a Docker host"}},
		{"no egress_address", "nas", "192.0.2.1", "", []string{`"kydns"`, "set egress_address on edge k1"}},
		{"hostname forward", "nas", "dns.lan.example.com", "192.168.1.20", []string{`"kydns"`, "must be an IP address of nas"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, cmd := range [][]string{{"review"}, {"plan", "--noninteractive"}, {"apply"}} {
				dnsPlan(t, f, c.target, c.forward, c.egress, dnsBundle)
				err := f.run(cmd...)
				for _, want := range c.want {
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Fatalf("%v: %v, want %q", cmd, err, want)
					}
				}
				without()
				if cmd[0] != "apply" {
					if err := f.run(cmd...); err != nil {
						t.Fatalf("%v without kydns: %v", cmd, err)
					}
				} else if _, err := load(f.state, nil, f.o); err != nil {
					t.Fatalf("load without kydns: %v", err)
				}
			}
		})
	}
}

// AC7: kydns.setup exports the client on the cluster and places its files on the Docker host
// over SSH, as the target user, with the payloads on stdin only; setup is live, verify is
// read-only, and nothing leaks a secret.
func TestKyDNSSetupOnHost(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	dnsPlan(t, f, "nas", "192.0.2.1", "192.168.1.20", dnsBundle)
	identity := &fakeIdentity{export: dnsExport(parentSetup, clientSecret)}
	dns := &fakeDNS{root: f.h.root}
	s, rec := dnsSession(t, f, identity, dns)
	s.edges = nil
	list, err := s.deploySteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	order := ids(list)
	if i := slices.Index(order, "kydns.setup"); i < 1 || order[i-1] != "kydns.health" || slices.Index(order, "kyidentity.setup") > i {
		t.Fatalf("steps = %v", order)
	}
	setup := list[slices.Index(order, "kydns.setup")]
	if err := list[slices.Index(order, "kyidentity.setup")].Apply(ctx); err != nil || !strings.Contains(identity.intent, wantDNSClient) {
		t.Fatalf("KyIdentity's setup: %v\n%s", err, identity.intent)
	}

	host := filepath.Join(f.h.root, "kydns/data/installer", dnsSetupID)
	if done, _ := setup.Inspect(ctx); done {
		t.Fatal("fresh KyDNS inspected as done")
	}
	if _, err := os.Stat(host); !os.IsNotExist(err) {
		t.Fatalf("inspection wrote %s: %v", host, err)
	}
	listenSetupSocket(t, f.h.root)
	rec.cmds, rec.stdins, dns.calls, identity.calls = nil, nil, nil, nil
	if err := setup.Apply(ctx); err != nil {
		t.Fatal(err)
	}

	// Setup waited for KyDNS's socket on the host.
	if !slices.Contains(rec.cmds, "test -S "+remote.Quote(filepath.Join(f.h.root, "kydns/data/installer/setup.sock"))) {
		t.Fatalf("no wait for the setup socket:\n%s", strings.Join(rec.cmds, "\n"))
	}
	// Placement: placeScript on the host, in write mode, files in order, payload on stdin only.
	var placed []string
	for i, cmd := range rec.cmds {
		if strings.Contains(cmd, clientSecret) || strings.Contains(cmd, directorySecret) {
			t.Errorf("secret in argv: %s", cmd)
		}
		if !strings.Contains(cmd, remote.Quote(placeScript)) {
			continue
		}
		if strings.Contains(cmd, "docker ") {
			t.Errorf("placement through docker: %s", cmd)
		}
		for _, name := range []string{"client.secret", "directory.secret", "setup.json"} {
			if strings.HasSuffix(cmd, " sh 'write' "+remote.Quote(host+"/"+name)) {
				placed = append(placed, name)
				if want := map[string]string{"client.secret": clientSecret, "directory.secret": directorySecret}[name]; want != "" && rec.stdins[i] != want {
					t.Errorf("%s stdin = %q", name, rec.stdins[i])
				}
			}
		}
	}
	if !slices.Equal(placed, []string{"client.secret", "directory.secret", "setup.json"}) {
		t.Fatalf("placed %v:\n%s", placed, strings.Join(rec.cmds, "\n"))
	}
	stat, err := remote.Local{}.Run(ctx, "stat -c '%n %a %u %F' "+remote.Quote(host)+" "+remote.Quote(host)+"/*", nil)
	uid := strings.TrimSpace(string(must(remote.Local{}.Run(ctx, "id -u", nil))))
	want := host + " 700 " + uid + " directory\n" + host + "/client.secret 600 " + uid + " regular file\n" + host + "/directory.secret 600 " + uid + " regular file\n" + host + "/setup.json 600 " + uid + " regular file\n"
	if err != nil || string(stat) != want {
		t.Fatalf("host files:\n%s\nwant\n%s %v", stat, want, err)
	}
	if b, _ := os.ReadFile(host + "/client.secret"); string(b) != clientSecret {
		t.Fatal("client secret differs")
	}

	// The bundle names the container paths, the directory secret, no SCIM and no everyday account.
	var b map[string]json.RawMessage
	if json.Unmarshal([]byte(dns.bundle), &b) != nil {
		t.Fatalf("bundle = %s", dns.bundle)
	}
	dir := "/data/installer/" + dnsSetupID
	wantBundle := `{"admins":[{"issuer":"https://identity.example.com","subject":"` + adminSubject + `","username":"owner-admin"}],"schema":"ky.product-setup/1","service":"kydns","setupId":"` + dnsSetupID + `",` +
		`"sso":{"clientId":"kydns","clientSecretFile":"` + dir + `/client.secret","directorySecretFile":"` + dir + `/directory.secret","issuerUrl":"https://identity.example.com","redirectUri":"https://dns.example.com/auth/sso/callback"}}`
	if dns.bundle != wantBundle {
		t.Fatalf("bundle =\n%s\nwant\n%s", dns.bundle, wantBundle)
	}
	// Live setup, no restart; the export is read again and compared.
	if !slices.Contains(dns.calls, "/usr/local/bin/kydns apply-setup --file "+dnsBundle) || slices.ContainsFunc(dns.calls, func(c string) bool { return strings.Contains(c, "--restart") }) {
		t.Fatalf("KyDNS calls = %q", dns.calls)
	}
	exports := 0
	for _, c := range identity.calls {
		if strings.HasSuffix(c, "--export-client kydns") {
			exports++
		}
	}
	if exports != 2 {
		t.Fatalf("exports = %d, want the export and its recheck", exports)
	}

	// Verify only compares: placeScript in check mode, --check, and every file unchanged.
	before := hostFiles(t, host)
	rec.cmds, dns.calls = nil, nil
	if done, _ := setup.Inspect(ctx); !done {
		t.Fatal("applied setup not inspected as done")
	}
	for _, cmd := range rec.cmds {
		if strings.Contains(cmd, remote.Quote(placeScript)) && !strings.Contains(cmd, " sh 'check' ") {
			t.Errorf("verify placed in another mode: %s", cmd)
		}
	}
	if !slices.Contains(dns.calls, "/usr/local/bin/kydns apply-setup --file "+dnsBundle+" --check") || slices.Contains(dns.calls, "/usr/local/bin/kydns apply-setup --file "+dnsBundle) {
		t.Fatalf("verify calls = %q", dns.calls)
	}
	if after := hostFiles(t, host); after != before {
		t.Fatalf("verify changed files:\n%s\n%s", before, after)
	}

	// A changed source secret is refused, the retained file kept, and the secret never shown.
	const changed = "Q2hhbmdlZENoYW5nZWRDaGFuZ2VkQ2hhbmdlZENoYW4"
	identity.export = dnsExport(parentSetup, changed)
	err = setup.Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "KyDNS's client.secret is missing, unsafe or differs") || strings.Contains(err.Error(), changed) || strings.Contains(err.Error(), clientSecret) {
		t.Fatalf("changed client secret: %v", err)
	}
	if b, _ := os.ReadFile(host + "/client.secret"); string(b) != clientSecret {
		t.Fatal("retained client secret replaced")
	}
	for _, dir := range []string{f.state} {
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if b, _ := os.ReadFile(p); err == nil && !d.IsDir() && (bytes.Contains(b, []byte(clientSecret)) || bytes.Contains(b, []byte(directorySecret))) {
				t.Errorf("secret written to %s", p)
			}
			return nil
		})
	}
	if strings.Contains(f.out.String(), clientSecret) || strings.Contains(f.out.String(), directorySecret) {
		t.Error("secret printed")
	}
}

// A symlinked setup directory on the host is refused before any write through it.
func TestKyDNSPlacementRefusesSymlinks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	elsewhere := t.TempDir()
	os.MkdirAll(filepath.Join(root, "kydns/data"), 0o700)
	os.Symlink(elsewhere, filepath.Join(root, "kydns/data/installer"))
	place, err := hostPlace(remote.Local{}, filepath.Join(root, "kydns"), dnsBundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := place(ctx, []string{"write", "/data/installer/" + dnsSetupID + "/client.secret"}, []byte(clientSecret)); err == nil {
		t.Fatal("placed through a symlink")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("wrote %v through the symlink", entries)
	}
	os.Remove(filepath.Join(root, "kydns/data/installer"))
	if err := place(ctx, []string{"write", "/data/installer/other/client.secret"}, []byte(clientSecret)); err == nil {
		t.Fatal("placed outside its setup directory")
	}
	if err := place(ctx, []string{"write", "/data/installer/" + dnsSetupID + "/client.secret"}, []byte(clientSecret)); err != nil {
		t.Fatalf("placement without a symlink: %v", err)
	}
}

// A bundle outside /data/installer/<uuid>/ is refused before any write.
func TestKyDNSBundleOutsideInstallerRefused(t *testing.T) {
	reached := 0
	for _, bundle := range []string{"/app/data/installer/" + dnsSetupID + "/setup.json", "/data/other/installer/" + dnsSetupID + "/setup.json", "/srv/installer/" + dnsSetupID + "/setup.json"} {
		f := newFixture(t, "../catalog/testdata/apps")
		f.o.Catalog = catalog.Embedded()
		dnsPlan(t, f, "nas", "192.0.2.1", "192.168.1.20", bundle)
		s, err := load(f.state, nil, f.o)
		if err != nil {
			continue // the plan refuses it already
		}
		reached++
		s, rec := dnsSession(t, f, &fakeIdentity{export: dnsExport(parentSetup, clientSecret)}, &fakeDNS{root: f.h.root})
		s.edges = nil
		if _, err := s.deploySteps(context.Background()); err == nil || !strings.Contains(err.Error(), "/data/installer/") {
			t.Errorf("%s: %v", bundle, err)
		}
		for _, cmd := range rec.cmds {
			if strings.Contains(cmd, remote.Quote(placeScript)) {
				t.Errorf("%s: placed %s", bundle, cmd)
			}
		}
		if _, err := os.Stat(filepath.Join(f.h.root, "kydns")); !os.IsNotExist(err) {
			t.Errorf("%s: wrote on the host", bundle)
		}
	}
	if reached != 3 {
		t.Fatalf("the plan refused %d bundles before setup did", 3-reached)
	}
}

// Setup waits for KyDNS's socket, which a just-started container may not have opened yet.
func TestKyDNSSetupWaitsForItsSocket(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "kydns/data/installer/setup.sock")
	os.MkdirAll(filepath.Dir(socket), 0o700)
	t.Chdir(filepath.Dir(socket)) // a relative bind keeps the path under the kernel's limit
	ran := false
	setup := waitForSocket(remote.Local{}, socket, func(context.Context, identityExec, string, bool) ([]byte, error) {
		ran = true
		return nil, nil
	})
	listened := make(chan net.Listener, 1)
	go func() {
		time.Sleep(1500 * time.Millisecond)
		l, _ := net.Listen("unix", "setup.sock")
		listened <- l
	}()
	start := time.Now()
	_, err := setup(context.Background(), nil, dnsBundle, false)
	if l := <-listened; l != nil {
		l.Close()
	}
	if err != nil || !ran || time.Since(start) < time.Second {
		t.Fatalf("setup = %v, ran %v after %s", err, ran, time.Since(start))
	}
}

// AC6: KyDNS joins a KyIdentity whose first setup ran without it, by setup amendment, applied
// before its setup, which exports from the amendment.
func TestKyDNSAddedByAmendment(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	identity := setUpIdentity(t, f, amendmentFeatures)
	dnsPlan(t, f, "nas", "192.0.2.1", "192.168.1.20", dnsBundle)
	id := amendmentID(parentSetup, "kydns")
	bundle := "/data/installer/" + id + "/setup.json"
	amendment := &fakeIdentity{parent: identity, export: dnsExport(id, clientSecret)}
	identity.setups[bundle] = amendment
	dns := &fakeDNS{root: f.h.root}
	listenSetupSocket(t, f.h.root)

	s := runningSession(t, f, identity, &fakeRecovery{files: map[string]string{}})
	s.drivers["nas"] = sshDriver{t: s.placed["kydns"], c: f.h}
	f.h.exec = dns.exec(t)
	publish, err := s.amendLate(ctx, f.state, nil)
	if err != nil || len(s.amendments) != 1 || s.amendments[0].app != "kydns" || !strings.Contains(f.out.String(), "ran without kydns's client: adding it with setup amendment "+id) {
		t.Fatalf("no amendment recorded: %v\n%s", err, f.out.String())
	}
	steps := setupStepsOf(t, s)
	if got := strings.Join(ids(steps), " "); got != "kyidentity.setup kyidentity.amend.kydns kyidentity.retire-bootstrap kydns.setup" {
		t.Fatalf("steps = %s", got)
	}
	var out bytes.Buffer
	e := &engine.Engine{Dir: t.TempDir(), Out: &out, Redact: &engine.Redactor{}}
	if err := e.Run(ctx, steps); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !slices.Contains(amendment.calls, "/usr/local/bin/kyidentity apply-setup --file "+bundle+" --export-client kydns") || !dns.applied {
		t.Fatalf("export calls %q, KyDNS applied %v", amendment.calls, dns.applied)
	}
	if !strings.Contains(amendment.intent, `"clients":[`+wantDNSClient+`]`) {
		t.Fatalf("amendment = %s", amendment.intent)
	}
	out.Reset()
	if err := e.Run(ctx, setupStepsOf(t, s)); err != nil || strings.Count(out.String(), "skipped") != 4 {
		t.Fatalf("rerun: %v\n%s", err, out.String())
	}
	if err := publish(); err != nil || f.stack(t).Integrations[1].IdentityAmendment != id {
		t.Fatalf("amendment not recorded: %v", err)
	}
}

// AC8: KyDNS's setup follows its health and precedes every edge step, so its route appears
// only after setup closed its first-run /setup.
func TestKyDNSSetupBeforeEdges(t *testing.T) {
	if slices.Contains(routedFirst, "kydns") {
		t.Fatal("kydns in routedFirst")
	}
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	dnsPlan(t, f, "nas", "192.0.2.1", "192.168.1.20", dnsBundle)
	s, _ := dnsSession(t, f, &fakeIdentity{}, &fakeDNS{root: f.h.root})
	var err error
	if s.npms["k1"], err = edge.NewNPM("kube://kyq-npm/app:81", "token", "test-installation", func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("no NPM in this test")
	}); err != nil {
		t.Fatal(err)
	}
	list, err := s.deploySteps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	order := ids(list)
	setup := slices.Index(order, "kydns.setup")
	firstEdge := slices.IndexFunc(order, func(id string) bool { return strings.HasPrefix(id, "k1.edge.") })
	route := slices.IndexFunc(list, func(st engine.Step) bool { return edge.RouteApp(st) == "kydns" })
	if setup < 1 || order[setup-1] != "kydns.health" || firstEdge < 0 || setup > firstEdge || route < firstEdge {
		t.Fatalf("steps = %v", order)
	}
}

// hostFiles is every file under dir with its mode and content.
func hostFiles(t *testing.T, dir string) string {
	t.Helper()
	var out strings.Builder
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		info, _ := d.Info()
		b, _ := os.ReadFile(p)
		out.WriteString(p + " " + info.Mode().String() + " " + info.ModTime().String() + " " + string(b) + "\n")
		return nil
	})
	return out.String()
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}
