package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
)

const catalogNotesBundle = "/data/installer/setup.json"

// notesPlan adds the catalog KyNotes, routed at notes.example.com, to the catalog KyIdentity plan.
func notesPlan(t *testing.T, f *fixture, clients string) {
	catalogIdentityPlan(t, f, "kyidentity", "  - product: kynotes\n    target: k1\n    bundle: "+catalogNotesBundle+"\n    identity_client: kynotes\n")
	p := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(p)
	y := strings.Replace(string(b), "    target: k1\nedges:", "    target: k1\n  - name: kynotes\n    target: k1\nedges:", 1)
	y = strings.Replace(y, "integrations:", "      - app: kynotes\n        hostname: notes.example.com\n        forward_host: app.kyq-kynotes.svc.cluster.local\n        forward_port: 8080\n        certificate_id: 1\nintegrations:", 1)
	y = strings.Replace(y, "      clients: []\n", clients, 1)
	os.WriteFile(p, []byte(y), 0o600)
}

const wantNotesClient = `{"id":"kynotes","name":"KyNotes","redirectUris":["https://notes.example.com/api/v1/auth/oidc/callback"],"postLogoutUris":null,"administratorRole":"kynotes.admin","everydayRole":"kynotes.user","suiteWebhook":{"url":"https://notes.example.com/api/v1/sync/events"}}`

func notesExport(setupID string) string {
	return `{"schema":"kyidentity.client-credentials/1","setupId":"` + setupID + `","issuerUrl":"https://identity.example.com","clientId":"kynotes",` +
		`"redirectUris":["https://notes.example.com/api/v1/auth/oidc/callback"],"directoryUrl":"https://notes.example.com/api/v1/sync/events",` +
		`"clientSecret":"` + clientSecret + `","directorySecret":"` + directorySecret + `",` +
		`"administrator":{"subject":"5e0c8a52-6d1b-4f7e-9a3c-2b1d0e9f8a7c","username":"owner-admin","role":"kynotes.admin"}}`
}

// AC5: one apply installing KyIdentity and KyNotes puts KyNotes' client in the first setup.
func TestKyNotesClientFromCatalog(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	notesPlan(t, f, "      clients: []\n")
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(s.integrations[0].Identity.Clients); string(b) != "["+wantNotesClient+"]" || len(s.amendments) != 0 || !slices.Equal(s.derived, []string{"kynotes"}) {
		t.Fatalf("derived clients = %s, amendments %d", b, len(s.amendments))
	}
	for _, want := range []string{"KyNotes at https://notes.example.com/", "as owner-admin (administers only", "or owner, whose account", "role\n  kynotes.user", "No catalog product can pair with KyRecovery in this release", "no local password"} {
		if !strings.Contains(s.handover, want) {
			t.Errorf("handover lacks %q: %s", want, s.handover)
		}
	}
	written := "      clients:\n        - id: kynotes\n          name: KyNotes\n          redirect_uris: [https://notes.example.com/api/v1/auth/oidc/callback]\n          administrator_role: kynotes.admin\n          everyday_role: kynotes.user\n          suite_webhook: {url: https://notes.example.com/api/v1/sync/events}\n"
	notesPlan(t, f, written)
	if s, err := load(f.state, nil, f.o); err != nil || len(s.integrations[0].Identity.Clients) != 1 {
		t.Fatalf("matching hand-written client: %v", err)
	}
	notesPlan(t, f, strings.Replace(written, "kynotes.user", "user", 1))
	if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), "differs from the catalog's KyNotes client") {
		t.Fatalf("different hand-written client: %v", err)
	}
}

// fakeNotes is KyNotes' distroless pod: apply-setup only, with its staging, import and setup.
type fakeNotes struct {
	files   map[string]string
	applied bool
	calls   []string // apply-setup arguments
	stdin   []string // bodies received on stdin
	caps    []string
	report  string // overrides apply-setup's report when set
}

var notesFeatures = []string{"sso", "issuer-bound-admin", "dedicated-admin", "protected-setup-files", "bundle-preparation", "client-credential-import", "backup", "check"}

func (n *fakeNotes) stream(args []string, o remotecommand.StreamOptions) (string, error) {
	if args[0] != "/kynotes-server" || args[1] != "apply-setup" {
		return "", errors.New("KyNotes has no shell")
	}
	n.calls = append(n.calls, strings.Join(args[1:], " "))
	var in []byte
	if o.Stdin != nil {
		in, _ = io.ReadAll(o.Stdin)
		n.stdin = append(n.stdin, string(in))
	}
	check := args[len(args)-1] == "--check"
	file := ""
	if i := slices.Index(args, "--file"); i > 0 {
		file = args[i+1]
	}
	dir := path.Dir(file)
	// put publishes name unless present; a different retained value is refused, never replaced.
	put := func(name, v string) (string, error) {
		old, ok := n.files[name]
		switch {
		case ok && old != v:
			return "", errors.New("conflict")
		case ok:
			return "present", nil
		case check:
			return "", errors.New("missing")
		}
		n.files[name] = v
		return "created", nil
	}
	switch {
	case slices.Contains(args, "--capabilities"):
		b, _ := json.Marshal(map[string]any{"schema": "ky.setup-capabilities/1", "service": "kynotes", "bundleSchema": "kynotes.setup/1", "features": n.caps})
		return string(b), nil
	case slices.Contains(args, "--stage"):
		if file != catalogNotesBundle {
			return "", errors.New("stage accepts only <data_dir>/installer/setup.json")
		}
		status, err := put(file, string(in))
		return `{"schema":"kynotes.setup-preparation/1","status":"` + status + `"}`, err
	case slices.Contains(args, "--import-client"):
		var frame clientCredentials
		if n.files[file] == "" || json.Unmarshal(in, &frame) != nil {
			return "", errors.New("not staged")
		}
		status, err := put(dir+"/client.secret", frame.ClientSecret)
		if err == nil {
			status, err = put(dir+"/directory.secret", frame.DirectorySecret)
		}
		return `{"schema":"kynotes.client-import/1","status":"` + status + `"}`, err
	}
	if n.files[file] == "" || n.files[dir+"/client.secret"] == "" || check && !n.applied {
		return "", errors.New("conflict")
	}
	status := "present"
	if !check && !n.applied {
		n.applied, status = true, "created"
	}
	if n.report != "" {
		return n.report, nil
	}
	return `{"version":1,"results":[{"section":"sso","status":"` + status + `"},{"section":"admin:owner-admin","status":"` + status + `"},{"section":"backup.dir","status":"present"},{"section":"backup.keep","status":"present"}],` +
		`"handover":{"url":"https://notes.example.com","adminUsernames":["owner-admin"],"recoveryKeyFingerprint":"","backupDir":"/backups","version":"f597e99"}}`, nil
}

// notesSession loads the plan over ready KyIdentity and KyNotes pods; running adds
// KyIdentity's Deployment, as an apply after its first setup sees it.
func notesSession(t *testing.T, f *fixture, identity *fakeIdentity, notes *fakeNotes, running bool) *session {
	t.Helper()
	ctx := context.Background()
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	if running {
		f.cs.AppsV1().Deployments("kyq-kyidentity").Create(ctx, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Labels: map[string]string{"app.kubernetes.io/name": "kyidentity", managed.Label: managed.Value}},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: "app", Env: []corev1.EnvVar{{Name: "KYIDENTITY_ISSUER_URL", Value: "https://identity.example.com"}},
			}}}}},
		}, metav1.CreateOptions{})
	}
	for _, app := range []string{"kyidentity", "kynotes"} {
		f.cs.CoreV1().Pods("kyq-"+app).Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "app-1", Labels: map[string]string{"app.kubernetes.io/name": app, managed.Label: managed.Value}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
		}, metav1.CreateOptions{})
	}
	stream := func(ctx context.Context, namespace, pod, container string, args []string, o remotecommand.StreamOptions) error {
		if namespace == "kyq-kyidentity" {
			return identity.stream(ctx, namespace, pod, container, args, o)
		}
		out, err := notes.stream(args, o)
		if err == nil {
			_, err = io.WriteString(o.Stdout, out)
		}
		return err
	}
	s.drivers["k1"] = kubeDriver{c: kube.Fake(f.cs, "https://k1.test", stream)}
	s.edges = nil
	return s
}

func stepsByID(t *testing.T, s *session) map[string]engine.Step {
	t.Helper()
	list, err := s.deploySteps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]engine.Step{}
	for _, st := range list {
		out[st.ID()] = st
	}
	return out
}

// AC7: kynotes.setup stages, imports on stdin, re-exports and applies; Verify only checks.
func TestKyNotesSetup(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	notesPlan(t, f, "      clients: []\n")
	identity := &fakeIdentity{exports: map[string]string{"kynotes": notesExport(parentSetup)}}
	notes := &fakeNotes{files: map[string]string{}, caps: notesFeatures}
	steps := stepsByID(t, notesSession(t, f, identity, notes, false))
	if err := steps["kyidentity.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	setup := steps["kynotes.setup"]
	var errs []string
	if done, _ := setup.Inspect(ctx); done || len(notes.files) != 0 {
		t.Fatal("fresh KyNotes inspected as done, or inspection wrote")
	}
	identity.calls, notes.calls, notes.stdin = nil, nil, nil
	if err := setup.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	p := "apply-setup "
	if want := []string{
		p + "--capabilities", p + "--capabilities", // transfer, then staging
		p + "--stage --file " + catalogNotesBundle, p + "--stage --file " + catalogNotesBundle + " --check",
		p + "--import-client --file " + catalogNotesBundle, p + "--import-client --file " + catalogNotesBundle + " --check",
		p + "--capabilities", p + "--file " + catalogNotesBundle,
	}; !slices.Equal(notes.calls, want) {
		t.Fatalf("KyNotes calls\n got %q\nwant %q", notes.calls, want)
	}
	exports := slices.DeleteFunc(slices.Clone(identity.calls), func(c string) bool { return strings.Contains(c, "--capabilities") })
	if len(exports) != 2 || exports[0] != exports[1] || !strings.HasSuffix(exports[0], "--file /data/installer/"+parentSetup+"/setup.json --export-client kynotes") {
		t.Fatalf("export and re-export = %q", identity.calls)
	}
	var b stack.NotesPreparation
	if err := json.Unmarshal([]byte(notes.files[catalogNotesBundle]), &b); err != nil {
		t.Fatal(err)
	}
	if got, _ := json.Marshal(b); string(got) != `{"version":1,"sso":{"issuerUrl":"https://identity.example.com","clientId":"kynotes","clientSecretFile":"/data/installer/client.secret","redirectUri":"https://notes.example.com/api/v1/auth/oidc/callback","directoryHmacSecretFile":"/data/installer/directory.secret"},`+
		`"admins":[{"issuer":"https://identity.example.com","subject":"5e0c8a52-6d1b-4f7e-9a3c-2b1d0e9f8a7c","username":"owner-admin"}],"backup":{"dir":"/backups","keep":7}}` {
		t.Fatalf("staged bundle = %s", got)
	}
	// The frame travels only on stdin: never argv, never the staged bundle.
	if notes.files["/data/installer/client.secret"] != clientSecret || notes.files["/data/installer/directory.secret"] != directorySecret ||
		!slices.Contains(notes.stdin, identity.exports["kynotes"]) || strings.Contains(strings.Join(notes.calls, " "), clientSecret) || strings.Contains(notes.files[catalogNotesBundle], clientSecret) {
		t.Fatal("secrets misplaced")
	}

	// Verify: every KyNotes call is a check or the capabilities; nothing is written.
	notes.calls, identity.calls = nil, nil
	before := len(notes.files)
	if err := setup.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	for _, c := range notes.calls {
		if !strings.HasSuffix(c, "--check") && c != p+"--capabilities" {
			t.Errorf("Verify ran %q", c)
		}
	}
	readOnly(t, slices.DeleteFunc(identity.calls, func(c string) bool { return strings.Contains(c, "--export-client") }))
	if len(notes.files) != before {
		t.Fatal("Verify wrote")
	}

	// A mutated inspection report, an export not matching the plan, a changed secret.
	notes.report = `{"version":1,"results":[{"section":"sso","status":"created"}],"handover":{"url":"","adminUsernames":[],"recoveryKeyFingerprint":"","backupDir":"","version":""}}`
	if err := setup.Verify(ctx); err == nil || err.Error() != "KyNotes setup is incomplete or inspection reported a mutation" {
		t.Fatalf("mutated report: %v", err)
	}
	notes.report = ""
	for _, bad := range []string{
		strings.Replace(notesExport(parentSetup), "notes.example.com/api/v1/auth", "other.example.com/api/v1/auth", 1),
		strings.Replace(notesExport(parentSetup), `"role":"kynotes.admin"`, `"role":"kynotes.user"`, 1),
		strings.Replace(notesExport(parentSetup), "https://identity.example.com", "https://other.example.com", 1),
	} {
		identity.exports["kynotes"] = bad
		err := setup.Apply(ctx)
		if !errors.Is(err, errBadExport) {
			t.Fatalf("bad export: %v", err)
		}
		errs = append(errs, err.Error())
	}
	identity.exports["kynotes"] = strings.Replace(notesExport(parentSetup), clientSecret, "Q2hhbmdlZENoYW5nZWRDaGFuZ2VkQ2hhbmdlZENoYW4", 1)
	err := setup.Apply(ctx)
	if err == nil || !strings.HasPrefix(err.Error(), "private destination credential import was not verified; no KyNotes route was added. Its container must resolve https://identity.example.com") || notes.files["/data/installer/client.secret"] != clientSecret {
		t.Fatalf("regenerated secret: %v", err)
	}
	errs = append(errs, err.Error())

	// Capabilities: KyIdentity's export and KyNotes' preparation and import are required
	// before KyNotes is touched.
	identity.exports["kynotes"] = notesExport(parentSetup)
	for _, missing := range []string{"bundle-preparation", "client-credential-import"} {
		notes.caps = slices.DeleteFunc(slices.Clone(notesFeatures), func(f string) bool { return f == missing })
		notes.calls = nil
		err := setup.Apply(ctx)
		if err == nil || !strings.Contains(err.Error(), missing) || slices.ContainsFunc(notes.calls, func(c string) bool { return !strings.Contains(c, "--capabilities") }) {
			t.Fatalf("without %s: %v, calls %q", missing, err, notes.calls)
		}
		errs = append(errs, err.Error())
	}
	notes.caps = notesFeatures
	noExport := setup.(notesSetupStep)
	noExport.export = func(ctx context.Context, args []string) ([]byte, error) {
		return bytes.ReplaceAll(transferCaps("kyidentity"), []byte("client-credential-export"), []byte("missing")), nil
	}
	notes.calls = nil
	if err := noExport.Apply(ctx); err == nil || !strings.Contains(err.Error(), "client-credential-export") || len(notes.calls) != 0 {
		t.Fatalf("without client-credential-export: %v, calls %q", err, notes.calls)
	}
	for _, e := range errs {
		if strings.Contains(e, clientSecret) || strings.Contains(e, directorySecret) {
			t.Fatalf("secret in error %q", e)
		}
	}
	if strings.Contains(f.out.String(), clientSecret) || strings.Contains(f.out.String(), directorySecret) {
		t.Fatal("secret in output")
	}
}

// AC6: KyNotes joining after KyIdentity's first setup comes in a setup amendment, through
// amendLate and newAmendment unchanged, applied before kynotes.setup.
func TestKyNotesAddedByAmendment(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	identity := setUpIdentity(t, f, amendmentFeatures)
	notesPlan(t, f, "      clients: []\n")
	id := amendmentID(parentSetup, "kynotes")
	bundle := "/data/installer/" + id + "/setup.json"
	amendment := &fakeIdentity{parent: identity, export: notesExport(id)}
	identity.setups[bundle] = amendment
	notes := &fakeNotes{files: map[string]string{}, caps: notesFeatures}

	s := notesSession(t, f, identity, notes, true)
	prior, err := s.ensureInstallation(f.state)
	if err != nil {
		t.Fatal(err)
	}
	publish, err := s.amendLate(ctx, f.state, prior)
	if err != nil || publish == nil || len(s.amendments) != 1 || len(s.derived) != 0 {
		t.Fatalf("no amendment recorded: %v", err)
	}
	readOnly(t, identity.calls)
	if !strings.Contains(f.out.String(), "ran without kynotes's client: adding it with setup amendment "+id) {
		t.Fatalf("output = %q", f.out.String())
	}
	want := `{"schema":"kyidentity.setup/1","setupId":"` + id + `","extends":"` + parentSetup + `","clients":[` + wantNotesClient + `]}`
	if got := string(s.amendments[0].bundle); got != want {
		t.Fatalf("amendment bundle\n got %s\nwant %s", got, want)
	}
	steps := setupStepsOf(t, s)
	if got := strings.Join(ids(steps), " "); got != "kyidentity.setup kyidentity.amend.kynotes kyidentity.retire-bootstrap kynotes.setup" {
		t.Fatalf("steps = %s", got)
	}
	var out bytes.Buffer
	e := &engine.Engine{Dir: t.TempDir(), Out: &out, Redact: &engine.Redactor{}}
	if err := e.Run(ctx, steps); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if amendment.intent != want || !notes.applied || slices.ContainsFunc(identity.calls, func(c string) bool { return strings.Contains(c, "--export-client") && !strings.Contains(c, bundle) }) {
		t.Fatalf("staged %s, KyNotes applied %v; export must read the amendment", amendment.intent, notes.applied)
	}
	out.Reset()
	if err := e.Run(ctx, steps); err != nil || strings.Count(out.String(), "skipped") != 4 {
		t.Fatalf("rerun: %v\n%s", err, out.String())
	}
	if err := publish(); err != nil || f.stack(t).Integrations[1].IdentityAmendment != id {
		t.Fatalf("amendment not recorded: %v", err)
	}
}

// AC8: KyNotes' setup runs after the edges route to KyIdentity and before its own route.
func TestKyNotesSetupBetweenRoutes(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	notesPlan(t, f, "      clients: []\n")
	s := notesSession(t, f, &fakeIdentity{}, &fakeNotes{files: map[string]string{}, caps: notesFeatures}, false)
	s.edges = s.plan.Edges
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
	route := func(app string) int {
		return slices.IndexFunc(list, func(st engine.Step) bool { return edge.RouteApp(st) == app })
	}
	firstEdge := slices.IndexFunc(order, func(id string) bool { return strings.HasPrefix(id, "k1.edge.") })
	setup := slices.Index(order, "kynotes.setup")
	if firstEdge < 0 || route("kyidentity") < 0 || !(route("kyidentity") < setup && setup < route("kynotes")) ||
		slices.Index(order, "kyidentity.setup") > firstEdge || slices.Index(order, "kynotes.health") > firstEdge {
		t.Fatalf("steps = %v", order)
	}
}

// AC15: KyNotes on a Docker host is refused before any connection, naming Kubernetes.
func TestKyNotesOnDockerRequiresRoute(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	catalogIdentityPlan(t, f, "kyidentity", "")
	p := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "    target: k1\nedges:", "    target: k1\n  - name: kynotes\n    target: nas\nedges:", 1)), 0o600)
	dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
		t.Fatal("connected")
		return nil, nil
	}
	for _, cmd := range [][]string{{"review"}, {"plan", "--noninteractive"}, {"apply"}} {
		if err := f.run(cmd...); err == nil || !strings.Contains(err.Error(), "kynotes") || !strings.Contains(err.Error(), "route") {
			t.Errorf("%s: %v", cmd[0], err)
		}
	}
}

// A routed KyNotes without its integration would serve its open first-run web setup: refused
// before any connection.
func TestRoutedKyNotesWithoutSetupRefused(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	notesPlan(t, f, "      clients: []\n")
	p := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "  - product: kynotes\n    target: k1\n    bundle: "+catalogNotesBundle+"\n    identity_client: kynotes\n", "", 1)), 0o600)
	dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
		t.Fatal("connected")
		return nil, nil
	}
	for _, cmd := range [][]string{{"review"}, {"plan", "--noninteractive"}, {"apply"}} {
		if err := f.run(cmd...); err == nil || !strings.Contains(err.Error(), "kynotes is routed but has no integration on k1") {
			t.Errorf("%s: %v", cmd[0], err)
		}
	}
	// Unrouted, it is unreachable through the edge and allowed.
	b, _ = os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "      - app: kynotes\n", "      - app: npm\n", 1)), 0o600)
	if err := f.run("review"); err != nil && strings.Contains(err.Error(), "is routed") {
		t.Fatalf("unrouted KyNotes refused: %v", err)
	}
}
