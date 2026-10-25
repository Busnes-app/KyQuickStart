package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
)

const pulseBundle = "/app/data/installer/7c2d9e4a-1b3f-4a5c-8d6e-9f0a1b2c3d4e/setup.json"

// pulsePlan adds kyPulse, routed at pulse.example.com, to the KyRecovery plan.
func pulsePlan(t *testing.T, f *fixture) {
	recoveryPlan(t, f, "      clients: []\n")
	path := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(path)
	y := strings.Replace(string(b), "  - name: kyrecovery\n    target: k1\nedges:", "  - name: kyrecovery\n    target: k1\n  - name: kypulse\n    target: k1\nedges:", 1)
	y = strings.Replace(y, "integrations:", "      - app: kypulse\n        hostname: pulse.example.com\n        forward_host: app.kyq-kypulse.svc.cluster.local\n        forward_port: 8080\n        certificate_id: 1\n        exposure: internal\nintegrations:", 1)
	y += "  - product: kypulse\n    target: k1\n    bundle: " + pulseBundle + "\n    identity_client: kypulse\n"
	os.WriteFile(path, []byte(internalEdge(y)), 0o600)
}

const wantPulseClient = `{"id":"kypulse","name":"kyPulse","redirectUris":["https://pulse.example.com/api/sso/kysignon/callback"],"postLogoutUris":null,"administratorRole":"kypulse.admin","suiteWebhook":{"url":"https://pulse.example.com/api/sso/kysignon/sync"}}`

func TestKyPulseClientFromCatalog(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	pulsePlan(t, f)
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(s.integrations[0].Identity.Clients); "["+wantRecoveryClient+","+wantPulseClient+"]" != string(b) {
		t.Fatalf("derived clients = %s", b)
	}
	// Every other routed app, through its route; NPM has none, kyPulse never watches itself.
	if b, _ := json.Marshal(s.pulseTargets); string(b) != `[{"key":"kyidentity","name":"kyidentity","url":"https://identity.example.com/readyz"},{"key":"kyrecovery","name":"kyrecovery","url":"https://recovery.example.com/healthz"}]` {
		t.Fatalf("targets = %s", b)
	}
	if !strings.Contains(s.handover, "kyPulse at https://pulse.example.com/ (sign in through KyIdentity as owner-admin) watches: kyidentity, kyrecovery.") || !strings.Contains(s.handover, "Not watched (no readiness or status path, or not exactly one edge route): npm.") || !strings.Contains(s.handover, "custodian ceremony") {
		t.Fatalf("handover = %q", s.handover)
	}
}

// An app without a Kubernetes block (Docker only) is watched at its status_path.
func TestPulseTargetsDockerApps(t *testing.T) {
	st := &stack.Stack{
		Apps: []stack.App{{Name: "kydrive", Target: "nas"}, {Name: "kymessage", Target: "nas"}, {Name: "kypulse", Target: "k1"}},
		Edges: []stack.Edge{{Target: "k1", Provider: "lan", AllowCIDRs: []string{"192.168.1.0/24"}, ClientPort: 30443, Routes: []stack.Route{
			{App: "kydrive", Hostname: "drive.example.com"},
			{App: "kymessage", Hostname: "message.example.com"},
			{App: "kymessage", Role: "matrix", Hostname: "matrix.example.com"},
		}}},
	}
	cat := catalog.Catalog{
		"kydrive":   {Manifest: catalog.Manifest{Name: "kydrive", StatusPath: "/healthz"}},
		"kymessage": {Manifest: catalog.Manifest{Name: "kymessage", StatusPath: "/api/installer/network"}},
	}
	targets, unwatched := pulseTargets(st, cat)
	if b, _ := json.Marshal(targets); string(b) != `[{"key":"kydrive","name":"kydrive","url":"https://drive.example.com:30443/healthz"},{"key":"kymessage","name":"kymessage","url":"https://message.example.com:30443/api/installer/network"}]` || len(unwatched) != 0 {
		t.Fatalf("targets = %s, unwatched %v", b, unwatched)
	}
}

// fakePulse is kyPulse's pod: its shell's private files and its apply-setup.
type fakePulse struct {
	files   map[string]string
	applied bool
	bundle  string
	caps    string
}

func (p *fakePulse) stream(args []string, o remotecommand.StreamOptions) (string, error) {
	switch {
	case args[0] == "sh":
		if args[2] != placeScript {
			return "", errors.New("unexpected script")
		}
		mode, path := args[4], args[5]
		b, _ := io.ReadAll(o.Stdin)
		if old, ok := p.files[path]; ok && mode != "replace" {
			if old != string(b) {
				return "", errors.New("differs")
			}
			return "", nil
		}
		if mode == "check" {
			return "", errors.New("missing")
		}
		p.files[path] = string(b)
		return "", nil
	case slices.Contains(args, "--capabilities"):
		return p.caps, nil
	}
	check := slices.Contains(args, "--check")
	p.bundle = p.files[args[slices.Index(args, "--file")+1]]
	if p.bundle == "" || check && !p.applied {
		return "", errors.New("not applied")
	}
	created := `[]`
	if !check && !p.applied {
		p.applied, created = true, `[{"kind":"configuration","name":"monitoring.target.kyidentity","id":"t1"}]`
	}
	return `{"schema":"ky.product-setup-report/1","service":"kypulse","setupId":"7c2d9e4a-1b3f-4a5c-8d6e-9f0a1b2c3d4e","created":` + created + `,"present":[],"pending":[]}`, nil
}

const pulseCaps = `{"schema":"ky.setup-capabilities/1","service":"kypulse","bundleSchema":"ky.product-setup/1","features":["kyidentity-oidc","directory-webhook","dedicated-admin","local-backup-config","monitoring-targets","log-sources","offboarding-alerts","check","restart"]}`

func pulseExport(issuer string) string {
	return `{"schema":"kyidentity.client-credentials/1","setupId":"271333e2-c221-4601-b249-563b3d828530","issuerUrl":"` + issuer + `","clientId":"kypulse",` +
		`"redirectUris":["https://pulse.example.com/api/sso/kysignon/callback"],"directoryUrl":"https://pulse.example.com/api/sso/kysignon/sync",` +
		`"clientSecret":"` + clientSecret + `","directorySecret":"` + directorySecret + `",` +
		`"administrator":{"subject":"5e0c8a52-6d1b-4f7e-9a3c-2b1d0e9f8a7c","username":"owner-admin","role":"kypulse.admin"}}`
}

// pulseSteps loads the plan with every app's ready pod and returns apply's steps by ID.
func pulseSteps(t *testing.T, f *fixture, identity *fakeIdentity, pulse *fakePulse) map[string]engine.Step {
	t.Helper()
	ctx := context.Background()
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"kyidentity", "kyrecovery", "kypulse"} {
		f.cs.CoreV1().Pods("kyq-"+app).Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "app-1", Labels: map[string]string{"app.kubernetes.io/name": app, managed.Label: managed.Value}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
		}, metav1.CreateOptions{})
	}
	stream := func(ctx context.Context, namespace, pod, container string, args []string, o remotecommand.StreamOptions) error {
		switch namespace {
		case "kyq-kyidentity":
			return identity.stream(ctx, namespace, pod, container, args, o)
		case "kyq-kypulse":
			out, err := pulse.stream(args, o)
			if err == nil {
				_, err = io.WriteString(o.Stdout, out)
			}
			return err
		}
		return errors.New("unexpected pod " + namespace)
	}
	s.drivers["k1"] = kubeDriver{c: kube.Fake(f.cs, "https://k1.test", stream)}
	s.edges = nil
	list, err := s.deploySteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]engine.Step{}
	var ids []string
	for _, st := range list {
		steps[st.ID()], ids = st, append(ids, st.ID())
	}
	if i := slices.Index(ids, "kypulse.setup"); i < 0 || ids[i-1] != "kypulse.health" || slices.Index(ids, "kyidentity.setup") > i {
		t.Fatalf("steps = %v", ids)
	}
	return steps
}

func TestKyPulseSetup(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	pulsePlan(t, f)
	identity := &fakeIdentity{exports: map[string]string{"kypulse": pulseExport("https://identity.example.com")}}
	pulse := &fakePulse{files: map[string]string{}, caps: pulseCaps}
	steps := pulseSteps(t, f, identity, pulse)
	if err := steps["kyidentity.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(identity.intent, wantRecoveryClient+","+wantPulseClient) {
		t.Fatalf("KyIdentity intent lacks the clients: %s", identity.intent)
	}
	setup := steps["kypulse.setup"]
	if done, _ := setup.Inspect(ctx); done {
		t.Fatal("fresh kyPulse inspected as done")
	}
	if err := setup.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if done, _ := setup.Inspect(ctx); !done {
		t.Fatal("applied setup not inspected as done")
	}
	dir := filepath.Dir(pulseBundle)
	var b struct {
		Service, SetupID string
		SSO              map[string]string
		Admins           []map[string]string
		Everyday         []any
		Monitoring       struct{ Targets []map[string]any }
	}
	if json.Unmarshal([]byte(pulse.bundle), &b) != nil || b.Service != "kypulse" || b.SetupID != "7c2d9e4a-1b3f-4a5c-8d6e-9f0a1b2c3d4e" ||
		b.SSO["clientId"] != "kypulse" || b.SSO["redirectUri"] != "https://pulse.example.com/api/sso/kysignon/callback" || b.SSO["directorySecretFile"] != dir+"/directory.secret" ||
		len(b.Admins) != 1 || b.Admins[0]["subject"] != "5e0c8a52-6d1b-4f7e-9a3c-2b1d0e9f8a7c" || b.Everyday != nil ||
		len(b.Monitoring.Targets) != 2 || b.Monitoring.Targets[0]["url"] != "https://identity.example.com/readyz" || b.Monitoring.Targets[1]["key"] != "kyrecovery" {
		t.Fatalf("bundle = %s", pulse.bundle)
	}
	if pulse.files[dir+"/client.secret"] != clientSecret || pulse.files[dir+"/directory.secret"] != directorySecret || strings.Contains(pulse.bundle, clientSecret) {
		t.Fatal("secrets misplaced")
	}

	// A plan without KyRecovery's route watches less: the bundle is replaced, kyPulse keeps the
	// target it omits. A changed secret is still refused, the retained one kept.
	pulse.files[pulseBundle] = strings.Replace(pulse.files[pulseBundle], "kyrecovery", "other", -1)
	if done, _ := setup.Inspect(ctx); done {
		t.Fatal("changed bundle inspected as done")
	}
	if err := setup.Apply(ctx); err != nil || !strings.Contains(pulse.files[pulseBundle], `"key":"kyrecovery"`) {
		t.Fatalf("bundle not replaced: %v", err)
	}
	identity.exports["kypulse"] = strings.Replace(identity.exports["kypulse"], clientSecret, "Q2hhbmdlZENoYW5nZWRDaGFuZ2VkQ2hhbmdlZENoYW4", 1)
	if err := setup.Apply(ctx); err == nil || !strings.Contains(err.Error(), "kyPulse's client.secret") || pulse.files[dir+"/client.secret"] != clientSecret {
		t.Fatalf("changed client secret: %v", err)
	}

	// The existing kyPulse adapter's capabilities stay required.
	identity.exports["kypulse"] = pulseExport("https://identity.example.com")
	pulse.caps = strings.Replace(pulseCaps, `"monitoring-targets",`, "", 1)
	if err := setup.Verify(ctx); err == nil || !strings.Contains(err.Error(), "monitoring-targets") {
		t.Fatalf("missing capability: %v", err)
	}
}

// KyIdentity set up with KyRecovery's client but before kyPulse joined the plan.
func TestKyPulseAfterIdentitySetupRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	recoveryPlan(t, f, "      clients: []\n")
	s, _ := load(f.state, nil, f.o)
	identity := &fakeIdentity{}
	raw, _ := json.Marshal(s.integrations[0].Identity)
	identity.staged, identity.applied, identity.intent = true, true, string(raw)

	pulsePlan(t, f)
	steps := pulseSteps(t, f, identity, &fakePulse{files: map[string]string{}, caps: pulseCaps})
	err := steps["kyidentity.setup"].Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "prepared without client kypulse") || !strings.Contains(err.Error(), "Busnes-app/KyIdentity-server#110") {
		t.Fatalf("late client: %v", err)
	}
}

func TestPulseClientMoveAfterSetupRefused(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	pulsePlan(t, f)
	native := &fakeIdentity{staged: true, applied: true, exports: map[string]string{
		"kyrecovery": recoveryExport("https://identity.example.com"),
		"kypulse":    strings.ReplaceAll(pulseExport("https://identity.example.com"), "pulse.example.com", "pulse.example.com:30443"),
	}}
	_, err := movedSetupSteps(t, f, "https://identity.example.com", native, true)
	want := fmt.Sprintf("this plan moves kyPulse's KyIdentity client from https://%s/api/sso/kysignon/callback to https://pulse.example.com/api/sso/kysignon/callback", "pulse.example.com:30443")
	if code(err) != suite.IssuerChanged || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v", err)
	}
}

// placeScript under a real sh: write never replaces, replace swaps in a regular file only,
// check only compares, and a symlink is never followed.
func TestPlaceScript(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "new", "f")
	run := func(mode, path, in string) error {
		c := exec.Command("sh", "-c", placeScript, "sh", mode, path)
		c.Stdin = strings.NewReader(in)
		return c.Run()
	}
	read := func() string { b, _ := os.ReadFile(f); return string(b) }
	if run("check", f, "a") == nil || run("write", f, "a") != nil || read() != "a" || run("check", f, "a") != nil || run("write", f, "a") != nil {
		t.Fatal("write or check")
	}
	if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if run("write", f, "b") == nil || run("check", f, "b") == nil || read() != "a" {
		t.Fatal("write replaced a file")
	}
	if run("replace", f, "b") != nil || read() != "b" {
		t.Fatal("replace")
	}
	link := filepath.Join(dir, "link")
	os.Symlink(f, link)
	os.Mkdir(filepath.Join(dir, "d"), 0o700)
	for _, c := range [][2]string{{"write", link}, {"replace", link}, {"check", link}, {"replace", filepath.Join(dir, "d")}} {
		if run(c[0], c[1], "c") == nil {
			t.Errorf("%s %s accepted", c[0], c[1])
		}
	}
	if read() != "b" {
		t.Fatal("followed a symlink")
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "d")); len(entries) != 0 {
		t.Fatal("replace wrote into a directory")
	}
}
