package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/Busnes-app/kyquickstart/internal/suite"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
)

const parentSetup = "271333e2-c221-4601-b249-563b3d828530"

var amendmentFeatures = []string{"setup-amendments", "disable-user"}

// setUpIdentity is a KyIdentity whose first setup ran from the plan before KyRecovery joined it.
func setUpIdentity(t *testing.T, f *fixture, features []string) *fakeIdentity {
	t.Helper()
	catalogIdentityPlan(t, f, "kyidentity", "")
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s.integrations[0].Identity)
	return &fakeIdentity{staged: true, applied: true, intent: string(raw), features: features, setups: map[string]*fakeIdentity{}}
}

// runningSession loads the plan over KyIdentity's running Deployment and both apps' ready pods.
func runningSession(t *testing.T, f *fixture, identity *fakeIdentity, recovery *fakeRecovery) *session {
	t.Helper()
	ctx := context.Background()
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	f.cs.AppsV1().Deployments("kyq-kyidentity").Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Labels: map[string]string{"app.kubernetes.io/name": "kyidentity", managed.Label: managed.Value}},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app", Env: []corev1.EnvVar{{Name: "KYIDENTITY_ISSUER_URL", Value: "https://identity.example.com"}},
		}}}}},
	}, metav1.CreateOptions{})
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
	return s
}

// setupSteps keeps the steps of deploySteps that set up products, in order.
func setupStepsOf(t *testing.T, s *session) []engine.Step {
	t.Helper()
	all, err := s.deploySteps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(all, func(st engine.Step) bool {
		return !strings.HasPrefix(st.ID(), "kyidentity.amend.") && !strings.HasSuffix(st.ID(), ".setup") && st.ID() != "kyidentity.retire-bootstrap"
	})
}

func ids(steps []engine.Step) []string {
	var out []string
	for _, st := range steps {
		out = append(out, st.ID())
	}
	return out
}

func readOnly(t *testing.T, calls []string) {
	t.Helper()
	for _, c := range calls {
		if !strings.Contains(c, "--capabilities") && !strings.HasSuffix(c, "--check") {
			t.Errorf("read-only phase ran %q", c)
		}
	}
}

// KyRecovery installed after KyIdentity's first setup joins it through a setup amendment.
func TestRecoveryAddedByAmendment(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	identity := setUpIdentity(t, f, amendmentFeatures)
	recoveryPlan(t, f, "      clients: []\n")
	id := amendmentID(parentSetup, "kyrecovery")
	bundle := "/data/installer/" + id + "/setup.json"
	amendment := &fakeIdentity{parent: identity, export: strings.Replace(recoveryExport("https://identity.example.com"), parentSetup, id, 1)}
	identity.setups[bundle] = amendment
	recovery := &fakeRecovery{files: map[string]string{}}

	s := runningSession(t, f, identity, recovery)
	prior, err := s.ensureInstallation(f.state) // as apply does first
	if err != nil || prior == nil {
		t.Fatal(err)
	}
	s.changesPlan = false
	publish, err := s.amendLate(ctx, f.state, prior)
	if err != nil || publish == nil || !s.changesPlan {
		t.Fatalf("no amendment recorded: %v", err)
	}
	readOnly(t, identity.calls)
	if !strings.Contains(f.out.String(), "setup amendment "+id) {
		t.Fatalf("output = %q", f.out.String())
	}
	// The first setup keeps its retained intent; the amendment carries the client alone.
	if len(s.integrations[0].Identity.Clients) != 0 || len(s.derived) != 0 || len(s.amendments) != 1 {
		t.Fatalf("parent clients %v, derived %v, amendments %d", s.integrations[0].Identity.Clients, s.derived, len(s.amendments))
	}
	want := `{"schema":"kyidentity.setup/1","setupId":"` + id + `","extends":"` + parentSetup + `","clients":[` + wantRecoveryClient + `]}`
	if got := string(s.amendments[0].bundle); got != want {
		t.Fatalf("amendment bundle\n got %s\nwant %s", got, want)
	}

	steps := setupStepsOf(t, s)
	amendment.calls = nil // deploySteps' read-only export: not registered yet
	if got := strings.Join(ids(steps), " "); got != "kyidentity.setup kyidentity.amend.kyrecovery kyidentity.retire-bootstrap kyrecovery.setup" {
		t.Fatalf("steps = %s", got)
	}
	var out bytes.Buffer
	e := &engine.Engine{Dir: t.TempDir(), Out: &out, Redact: &engine.Redactor{}}
	if err := e.Run(ctx, steps); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	p := "/usr/local/bin/kyidentity apply-setup "
	if got, want := amendment.calls, []string{
		p + "--stage --file " + bundle + " --check", // inspect: not staged yet
		p + "--stage --file " + bundle, p + "--stage --file " + bundle + " --check", p + "--file " + bundle,
		p + "--stage --file " + bundle + " --check", p + "--file " + bundle + " --check", // verify
		// kyrecovery.setup: inspect, apply and its re-export, verify
		p + "--file " + bundle + " --export-client kyrecovery", p + "--file " + bundle + " --export-client kyrecovery",
		p + "--file " + bundle + " --export-client kyrecovery", p + "--file " + bundle + " --export-client kyrecovery",
	}; !slices.Equal(got, want) {
		t.Fatalf("amendment calls\n got %q\nwant %q", got, want)
	}
	if amendment.intent != want || !identity.disabled {
		t.Fatalf("staged %s, bootstrap disabled %v", amendment.intent, identity.disabled)
	}
	if slices.ContainsFunc(identity.calls, func(c string) bool { return strings.Contains(c, "--export-client") && !strings.Contains(c, bundle) }) {
		t.Fatal("export read the first setup")
	}
	var b struct{ SetupID string }
	if json.Unmarshal([]byte(recovery.bundle), &b) != nil || b.SetupID != "0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c" || !recovery.applied {
		t.Fatalf("KyRecovery bundle = %s", recovery.bundle)
	}

	// Rerun: every step skips.
	out.Reset()
	if err := e.Run(ctx, steps); err != nil || strings.Count(out.String(), "skipped") != 4 {
		t.Fatalf("rerun: %v\n%s", err, out.String())
	}

	// Published, the plan records the amendment once; the next apply changes nothing.
	if err := publish(); err != nil {
		t.Fatal(err)
	}
	st := f.stack(t)
	if st.Integrations[1].IdentityAmendment != id || st.Revision != s.revision+1 || st.InstallationID != s.installation {
		t.Fatalf("recorded %q at revision %d", st.Integrations[1].IdentityAmendment, st.Revision)
	}
	identity.calls = nil
	s = runningSession(t, f, identity, recovery)
	if again, err := s.amendLate(ctx, f.state, nil); again != nil || err != nil || s.changesPlan || len(s.amendments) != 1 || s.amendments[0].source.Bundle != bundle {
		t.Fatalf("second apply changed the plan: %v", err)
	}
	out.Reset()
	if err := e.Run(ctx, setupStepsOf(t, s)); err != nil || strings.Count(out.String(), "skipped") != 4 {
		t.Fatalf("after publish: %v\n%s", err, out.String())
	}

	// A recorded amendment on a KyIdentity without amendments fails its own step.
	identity.features = []string{"disable-user"}
	if err := setupStepsOf(t, s)[1].Verify(ctx); err == nil || !strings.Contains(err.Error(), "setup-amendments") {
		t.Fatalf("amendment without the capability: %v", err)
	}

	// A hand-written client cannot also come in the amendment.
	b2, _ := os.ReadFile(filepath.Join(f.state, "stack.yaml"))
	written := strings.Replace(string(b2), "clients: []", "clients:\n            - {id: kyrecovery, name: KyRecovery, redirect_uris: ['https://recovery.example.com/api/auth/callback']}", 1)
	os.WriteFile(filepath.Join(f.state, "stack.yaml"), []byte(written), 0o600)
	if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), "comes in setup amendment "+id) {
		t.Fatalf("hand-written amended client: %v", err)
	}
}

// KyIdentity 0.4.2 takes no amendment: apply keeps today's refusal and names the version.
func TestAmendmentNeedsCapability(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	identity := setUpIdentity(t, f, nil)
	recoveryPlan(t, f, "      clients: []\n")
	s := runningSession(t, f, identity, &fakeRecovery{files: map[string]string{}})
	if publish, err := s.amendLate(ctx, f.state, nil); publish != nil || err != nil || s.changesPlan || s.amendments != nil {
		t.Fatalf("0.4.2 amended: %v", err)
	}
	steps := setupStepsOf(t, s)
	err := steps[0].Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "prepared without client kyrecovery") || !strings.Contains(err.Error(), "#110") || !strings.Contains(err.Error(), "0.4.3") {
		t.Fatalf("late client on 0.4.2: %v", err)
	}
	// With amendments but not yet recorded (an upgrade run, say): rerun apply.
	identity.features = []string{"setup-amendments"}
	if err := steps[0].Apply(ctx); err == nil || !strings.Contains(err.Error(), "rerun kyquickstart apply, which adds it with a setup amendment") {
		t.Fatalf("late client unrecorded: %v", err)
	}
}

func TestFreshInstallPutsClientsInTheFirstSetup(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	recoveryPlan(t, f, "      clients: []\n")
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	identity := &fakeIdentity{features: amendmentFeatures}
	s.drivers["k1"] = kubeDriver{c: kube.Fake(f.cs, "https://k1.test", identity.stream)}
	if publish, err := s.amendLate(context.Background(), f.state, nil); publish != nil || err != nil || s.amendments != nil || len(s.integrations[0].Identity.Clients) != 1 || len(identity.calls) != 0 {
		t.Fatalf("fresh install amended: %v, calls %v", err, identity.calls)
	}
}

// An amended client's URLs are bound like a first setup's: moving them is refused.
func TestAmendedClientMoveRefused(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	identity := setUpIdentity(t, f, amendmentFeatures)
	id := amendmentID(parentSetup, "kyrecovery")
	recoveryPlan(t, f, "      clients: []\n")
	b, _ := os.ReadFile(filepath.Join(f.state, "stack.yaml"))
	os.WriteFile(filepath.Join(f.state, "stack.yaml"), []byte(strings.Replace(string(b), "identity_client: kyrecovery\n", "identity_client: kyrecovery\n    identity_amendment: "+id+"\n", 1)), 0o600)
	moved := strings.ReplaceAll(strings.Replace(recoveryExport("https://identity.example.com"), parentSetup, id, 1), "recovery.example.com", "recovery.example.com:30443")
	identity.setups["/data/installer/"+id+"/setup.json"] = &fakeIdentity{parent: identity, staged: true, applied: true, export: moved}
	_, err := movedSetupSteps(t, f, "https://identity.example.com", identity, true)
	if code(err) != suite.IssuerChanged || !strings.Contains(err.Error(), "to https://recovery.example.com/api/auth/callback") || !strings.Contains(err.Error(), "KyIdentity's own administration") {
		t.Fatalf("moved amended client: %v", err)
	}
}

func TestRetireBootstrap(t *testing.T) {
	ctx := context.Background()
	identity := &fakeIdentity{applied: true}
	var note string
	step := retireBootstrapStep{exec: func(ctx context.Context, args []string) ([]byte, error) {
		var out bytes.Buffer
		err := identity.stream(ctx, "kyq-kyidentity", "app-1", "app", append([]string{identityBinary}, args...), remotecommand.StreamOptions{Stdout: &out})
		return out.Bytes(), err
	}, bootstrap: "kyq-bootstrap", admin: "owner-admin", note: &note}

	// 0.4.2: nothing runs, the handover says so.
	if err := step.Apply(ctx); err != nil || step.Verify(ctx) != nil || !strings.Contains(note, "Not yet") || slices.ContainsFunc(identity.calls, func(c string) bool { return strings.Contains(c, "disable-user") }) {
		t.Fatalf("without disable-user: %v %q", err, note)
	}
	identity.features = []string{"disable-user"}
	if err := step.Verify(ctx); err == nil {
		t.Fatal("active bootstrap verified")
	}
	if err := step.Apply(ctx); err != nil || !identity.disabled || note != "" || step.Verify(ctx) != nil {
		t.Fatalf("disable: %v %q", err, note)
	}
	if err := step.Apply(ctx); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	// The guard is the owner's administrator; KyIdentity refuses any other.
	step.admin = "someone"
	if err := step.Apply(ctx); err == nil || !strings.Contains(err.Error(), "another active administrator") {
		t.Fatalf("guard: %v", err)
	}
}
