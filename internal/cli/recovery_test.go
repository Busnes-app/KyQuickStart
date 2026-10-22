package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
)

const recoveryBundle = "/app/data/installer/0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c/setup.json"

// recoveryPlan adds KyRecovery, routed at recovery.example.com, to the catalog KyIdentity plan.
func recoveryPlan(t *testing.T, f *fixture, clients string) {
	catalogIdentityPlan(t, f, "kyidentity", "  - product: kyrecovery\n    target: k1\n    bundle: "+recoveryBundle+"\n    identity_client: kyrecovery\n")
	path := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(path)
	y := strings.Replace(string(b), "    target: k1\nedges:", "    target: k1\n  - name: kyrecovery\n    target: k1\nedges:", 1)
	y = strings.Replace(y, "integrations:", "      - app: kyrecovery\n        hostname: recovery.example.com\n        forward_host: app.kyq-kyrecovery.svc.cluster.local\n        forward_port: 8095\n        certificate_id: 1\n        exposure: internal\nintegrations:", 1)
	y = strings.Replace(y, "      clients: []\n", clients, 1)
	os.WriteFile(path, []byte(internalEdge(y)), 0o600)
}

// internalEdge gives the plan's tunnel edge the LAN clients its internal routes serve:
// KyRecovery, kyPulse and KyYard are internal only.
func internalEdge(y string) string {
	if strings.Contains(y, "    allow_cidrs:") {
		return y
	}
	return strings.Replace(y, "    connector_cidr: 192.0.2.9/32\n", "    connector_cidr: 192.0.2.9/32\n    allow_cidrs: [192.168.1.0/24]\n", 1)
}

const wantRecoveryClient = `{"id":"kyrecovery","name":"KyRecovery","redirectUris":["https://recovery.example.com/api/auth/callback"],"postLogoutUris":null,"administratorRole":"kyrecovery.admin","suiteWebhook":{"url":"https://recovery.example.com/api/installer/directory"}}`

func TestKyRecoveryClientFromCatalog(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	recoveryPlan(t, f, "      clients: []\n")
	saved, _ := os.ReadFile(filepath.Join(f.state, "stack.yaml"))
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	clients := s.integrations[0].Identity.Clients
	if b, _ := json.Marshal(clients); len(clients) != 1 || "["+wantRecoveryClient+"]" != string(b) {
		t.Fatalf("derived clients = %s", b)
	}
	if !strings.Contains(s.handover, "https://recovery.example.com/ through KyIdentity as owner-admin") {
		t.Fatalf("handover = %q", s.handover)
	}
	// The saved plan keeps only what the operator wrote.
	if after, _ := os.ReadFile(filepath.Join(f.state, "stack.yaml")); string(after) != string(saved) {
		t.Fatal("derived client written to stack.yaml")
	}
	if st, _ := stack.Load(filepath.Join(f.state, "stack.yaml")); len(st.Integrations[0].Identity.Clients) != 0 {
		t.Fatal("derived client leaked into the loaded plan")
	}

	// A hand-written client must be the catalog's exactly.
	written := "      clients:\n        - id: kyrecovery\n          name: KyRecovery\n          redirect_uris: [https://recovery.example.com/api/auth/callback]\n          administrator_role: kyrecovery.admin\n          suite_webhook: {url: https://recovery.example.com/api/installer/directory}\n"
	recoveryPlan(t, f, written)
	if s, err := load(f.state, nil, f.o); err != nil || len(s.integrations[0].Identity.Clients) != 1 {
		t.Fatalf("matching hand-written client: %v", err)
	}
	recoveryPlan(t, f, strings.Replace(written, "kyrecovery.admin", "admin", 1))
	if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), "differs from the catalog's KyRecovery client") {
		t.Fatalf("different hand-written client: %v", err)
	}

	// No route, or no KyIdentity setup to add the client to.
	recoveryPlan(t, f, "      clients: []\n")
	b, _ := os.ReadFile(filepath.Join(f.state, "stack.yaml"))
	os.WriteFile(filepath.Join(f.state, "stack.yaml"), []byte(strings.Replace(string(b), "      - app: kyrecovery\n", "      - app: npm\n", 1)), 0o600)
	if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), `"kyrecovery" signs in through its route hostname`) {
		t.Fatalf("no route: %v", err)
	}
	recoveryPlan(t, f, "      clients: []\n")
	b, _ = os.ReadFile(filepath.Join(f.state, "stack.yaml"))
	os.WriteFile(filepath.Join(f.state, "stack.yaml"), []byte(strings.Replace(string(b), identityPrep, "", 1)), 0o600)
	if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), "integration kyidentity") {
		t.Fatalf("no identity settings: %v", err)
	}
}

// fakeRecovery is KyRecovery's pod: its shell's private files and its apply-setup.
type fakeRecovery struct {
	files   map[string]string
	applied bool
	bundle  string // last bundle apply-setup read
}

func (r *fakeRecovery) stream(args []string, o remotecommand.StreamOptions) (string, error) {
	switch {
	case args[0] == "sh":
		if args[2] != placeScript {
			return "", errors.New("unexpected script")
		}
		mode, path := args[4], args[5]
		b, _ := io.ReadAll(o.Stdin)
		if old, ok := r.files[path]; ok {
			if old != string(b) {
				return "", errors.New("differs")
			}
			return "", nil
		}
		if mode != "write" {
			return "", errors.New("missing")
		}
		r.files[path] = string(b)
		return "", nil
	case slices.Contains(args, "--capabilities"):
		return `{"schema":"ky.setup-capabilities/1","service":"kyrecovery","bundleSchema":"ky.product-setup/1","features":["kyidentity-oidc","exact-admin-subjects","admin-only","directory-webhook","check"]}`, nil
	}
	check := slices.Contains(args, "--check")
	r.bundle = r.files[args[slices.Index(args, "--file")+1]]
	if r.bundle == "" || check && !r.applied {
		return "", errors.New("not applied")
	}
	created := `[]`
	if !check && !r.applied {
		r.applied, created = true, `[{"kind":"configuration","name":"sso"}]`
	}
	return `{"schema":"ky.product-setup-report/1","service":"kyrecovery","setupId":"0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c","created":` + created + `,"present":[],"pending":[]}`, nil
}

const (
	clientSecret    = "Q2xpZW50U2VjcmV0Q2xpZW50U2VjcmV0Q2xpZW50U2U"
	directorySecret = "RGlyZWN0b3J5U2VjcmV0RGlyZWN0b3J5U2VjcmV0RGk"
)

func recoveryExport(issuer string) string {
	return `{"schema":"kyidentity.client-credentials/1","setupId":"271333e2-c221-4601-b249-563b3d828530","issuerUrl":"` + issuer + `","clientId":"kyrecovery",` +
		`"redirectUris":["https://recovery.example.com/api/auth/callback"],"directoryUrl":"https://recovery.example.com/api/installer/directory",` +
		`"clientSecret":"` + clientSecret + `","directorySecret":"` + directorySecret + `",` +
		`"administrator":{"subject":"5e0c8a52-6d1b-4f7e-9a3c-2b1d0e9f8a7c","username":"owner-admin","role":"kyrecovery.admin"}}`
}

// recoverySteps loads the plan with both apps' ready pods and returns apply's steps by ID.
func recoverySteps(t *testing.T, f *fixture, identity *fakeIdentity, recovery *fakeRecovery) map[string]engine.Step {
	t.Helper()
	ctx := context.Background()
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"kyidentity", "kyrecovery"} {
		f.cs.CoreV1().Pods("kyq-"+app).Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "app-1", Labels: map[string]string{"app.kubernetes.io/name": app, managed.Label: managed.Value}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
		}, metav1.CreateOptions{})
	}
	stream := func(ctx context.Context, namespace, pod, container string, args []string, o remotecommand.StreamOptions) error {
		if namespace == "kyq-kyidentity" {
			return identity.stream(ctx, namespace, pod, container, args, o)
		}
		out, err := recovery.stream(args, o)
		if err == nil {
			_, err = io.WriteString(o.Stdout, out)
		}
		return err
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
	if i := slices.Index(ids, "kyrecovery.setup"); i < 0 || ids[i-1] != "kyrecovery.health" || slices.Index(ids, "kyidentity.setup") > i {
		t.Fatalf("steps = %v", ids)
	}
	return steps
}

func TestKyRecoverySetup(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	recoveryPlan(t, f, "      clients: []\n")
	identity := &fakeIdentity{export: recoveryExport("https://identity.example.com")}
	recovery := &fakeRecovery{files: map[string]string{}}
	steps := recoverySteps(t, f, identity, recovery)
	if err := steps["kyidentity.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(identity.intent, wantRecoveryClient) {
		t.Fatalf("KyIdentity intent lacks the client: %s", identity.intent)
	}
	setup := steps["kyrecovery.setup"]
	if done, _ := setup.Inspect(ctx); done {
		t.Fatal("fresh KyRecovery inspected as done")
	}
	if err := setup.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if done, _ := setup.Inspect(ctx); !done {
		t.Fatal("applied setup not inspected as done")
	}
	dir := filepath.Dir(recoveryBundle)
	var b struct {
		Schema, Service, SetupID string
		SSO                      map[string]string
		Admins                   []map[string]string
	}
	if json.Unmarshal([]byte(recovery.bundle), &b) != nil || b.Service != "kyrecovery" || b.SetupID != "0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c" ||
		b.SSO["issuerUrl"] != "https://identity.example.com" || b.SSO["clientId"] != "kyrecovery" || b.SSO["redirectUri"] != "https://recovery.example.com/api/auth/callback" ||
		b.SSO["clientSecretFile"] != dir+"/client.secret" || b.SSO["directorySecretFile"] != dir+"/directory.secret" ||
		len(b.Admins) != 1 || b.Admins[0]["subject"] != "5e0c8a52-6d1b-4f7e-9a3c-2b1d0e9f8a7c" || b.Admins[0]["username"] != "owner-admin" || b.Admins[0]["issuer"] != b.SSO["issuerUrl"] {
		t.Fatalf("bundle = %s", recovery.bundle)
	}
	if recovery.files[dir+"/client.secret"] != clientSecret || recovery.files[dir+"/directory.secret"] != directorySecret || strings.Contains(recovery.bundle, clientSecret) {
		t.Fatal("secrets misplaced")
	}

	// A changed source credential is refused and the retained file kept.
	identity.export = strings.Replace(identity.export, clientSecret, "Q2hhbmdlZENoYW5nZWRDaGFuZ2VkQ2hhbmdlZENoYW4", 1)
	if err := setup.Apply(ctx); err == nil || !strings.Contains(err.Error(), "client.secret") || recovery.files[dir+"/client.secret"] != clientSecret {
		t.Fatalf("changed client secret: %v", err)
	}
	// An export for another issuer or owner role never reaches the target.
	for _, bad := range []string{recoveryExport("https://other.example.com"), strings.Replace(recoveryExport("https://identity.example.com"), `"role":"kyrecovery.admin"`, `"role":"admin"`, 1)} {
		identity.export = bad
		if err := setup.Verify(ctx); !errors.Is(err, errBadExport) {
			t.Fatalf("bad export: %v", err)
		}
	}
}

// KyIdentity set up before KyRecovery joined the plan: its retained intent has no client.
func TestKyRecoveryAfterIdentitySetupRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	catalogIdentityPlan(t, f, "kyidentity", "")
	s, _ := load(f.state, nil, f.o)
	identity := &fakeIdentity{}
	raw, _ := json.Marshal(s.integrations[0].Identity)
	identity.staged, identity.applied, identity.intent = true, true, string(raw)

	recoveryPlan(t, f, "      clients: []\n")
	steps := recoverySteps(t, f, identity, &fakeRecovery{files: map[string]string{}})
	err := steps["kyidentity.setup"].Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "prepared without client kyrecovery") || !strings.Contains(err.Error(), "Busnes-app/KyIdentity-server#110") {
		t.Fatalf("late client: %v", err)
	}
	// Any other intent change keeps the product's own refusal.
	identity.intent = "other"
	if err := steps["kyidentity.setup"].Apply(ctx); err == nil || strings.Contains(err.Error(), "#110") {
		t.Fatalf("other change: %v", err)
	}
}
