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

const (
	calendarSetupID = "3c8f1b2a-7d4e-4f5a-9b6c-0d1e2f3a4b5c"
	ownerSubject    = "9b1d4e7a-2c3f-4a5b-8d6e-7f8091a2b3c4"
	adminSubject    = "5e0c8a52-6d1b-4f7e-9a3c-2b1d0e9f8a7c"
)

// everydayApp is a catalog Ky app whose setup names the owner's admin and everyday accounts
// (KyCalendar, KyMessages), routed at host on k1 and set up from bundle setupID.
type everydayApp struct {
	app, name, host, setupID string
	sso                      string // its SSO path prefix, e.g. /api/sso/kysignon
}

var calendarApp = everydayApp{"kycalendar", "KyCalendar", "calendar.example.com", calendarSetupID, "/api/sso/kysignon"}

func (a everydayApp) bundle() string   { return "/app/data/installer/" + a.setupID + "/setup.json" }
func (a everydayApp) callback() string { return "https://" + a.host + a.sso + "/callback" }
func (a everydayApp) sync() string     { return "https://" + a.host + a.sso + "/sync" }

// plan adds the catalog app, routed at its host, to the catalog KyIdentity plan.
func (a everydayApp) plan(t *testing.T, f *fixture, clients string) {
	catalogIdentityPlan(t, f, "kyidentity", "  - product: "+a.app+"\n    target: k1\n    bundle: "+a.bundle()+"\n    identity_client: "+a.app+"\n")
	p := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(p)
	y := strings.Replace(string(b), "    target: k1\nedges:", "    target: k1\n  - name: "+a.app+"\n    target: k1\nedges:", 1)
	y = strings.Replace(y, "integrations:", "      - app: "+a.app+"\n        hostname: "+a.host+"\n        forward_host: app.kyq-"+a.app+".svc.cluster.local\n        forward_port: 8080\n        certificate_id: 1\nintegrations:", 1)
	y = strings.Replace(y, "      clients: []\n", clients, 1)
	os.WriteFile(p, []byte(y), 0o600)
}

func calendarPlan(t *testing.T, f *fixture, clients string) { calendarApp.plan(t, f, clients) }

// client is the KyIdentity client composeClients derives for the app.
func (a everydayApp) client() string {
	return `{"id":"` + a.app + `","name":"` + a.name + `","redirectUris":["` + a.callback() + `"],"postLogoutUris":null,"administratorRole":"` + a.app + `.admin","everydayRole":"` + a.app + `.user","suiteWebhook":{"url":"` + a.sync() + `"}}`
}

// writtenClient is that client as a plan writes it.
func (a everydayApp) writtenClient() string {
	return "      clients:\n        - id: " + a.app + "\n          name: " + a.name + "\n          redirect_uris: [" + a.callback() + "]\n          administrator_role: " + a.app + ".admin\n          everyday_role: " + a.app + ".user\n          suite_webhook: {url: " + a.sync() + "}\n"
}

func (a everydayApp) export(setupID string) string {
	return `{"schema":"kyidentity.client-credentials/1","setupId":"` + setupID + `","issuerUrl":"https://identity.example.com","clientId":"` + a.app + `",` +
		`"redirectUris":["` + a.callback() + `"],"directoryUrl":"` + a.sync() + `",` +
		`"clientSecret":"` + clientSecret + `","directorySecret":"` + directorySecret + `",` +
		`"administrator":{"subject":"` + adminSubject + `","username":"owner-admin","role":"` + a.app + `.admin"}}`
}

// firstSetupCheck is KyIdentity's --check report of its first setup: both owner accounts reused.
func firstSetupCheck(owner string) string {
	return `{"schema":"kyidentity.setup/1","created":[],"reused":[{"kind":"user","name":"owner-admin","id":"` + adminSubject + `"},` +
		owner + `{"kind":"client","name":"kycalendar","id":"c-1"}]}`
}

var ownerReused = `{"kind":"user","name":"owner","id":"` + ownerSubject + `"},`

// AC5: one apply installing KyIdentity and KyCalendar puts KyCalendar's client in the first setup.
func TestKyCalendarClientFromCatalog(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	s := derivedClient(t, f, calendarApp)
	kubectl := "kubectl --kubeconfig '" + s.placed["kycalendar"].Kubeconfig + "' -n kyq-kycalendar exec deployment/app -- "
	for _, want := range []string{"KyCalendar at https://calendar.example.com/", "as owner-admin (its console only", "or owner (calendars)",
		kubectl + "cat /app/data/initial-admin-password", `local "admin" break-glass`, "pair it with KyRecovery"} {
		if !strings.Contains(s.handover, want) {
			t.Errorf("handover lacks %q: %s", want, s.handover)
		}
	}
	// The file goes only once the password is changed.
	if change, rm := strings.Index(s.handover, "change it. Only after that"), strings.Index(s.handover, kubectl+"rm /app/data/initial-admin-password"); change < 0 || rm < change {
		t.Errorf("break-glass steps out of order: %s", s.handover)
	}
	handWrittenClient(t, f, calendarApp)
}

// derivedClient loads a plan installing KyIdentity and a in one apply: a's catalog client joins
// KyIdentity's first setup, with no amendment.
func derivedClient(t *testing.T, f *fixture, a everydayApp) *session {
	t.Helper()
	a.plan(t, f, "      clients: []\n")
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(s.integrations[0].Identity.Clients); string(b) != "["+a.client()+"]" || len(s.amendments) != 0 || !slices.Equal(s.derived, []string{a.app}) {
		t.Fatalf("derived clients = %s, amendments %d", b, len(s.amendments))
	}
	return s
}

// handWrittenClient: a hand-written client equal to the app's catalog one is kept; any other
// is refused.
func handWrittenClient(t *testing.T, f *fixture, a everydayApp) {
	t.Helper()
	written := a.writtenClient()
	a.plan(t, f, written)
	if s, err := load(f.state, nil, f.o); err != nil || len(s.integrations[0].Identity.Clients) != 1 {
		t.Fatalf("matching hand-written client: %v", err)
	}
	a.plan(t, f, strings.Replace(written, a.app+".user", "user", 1))
	if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), "differs from the catalog's "+a.name+" client") {
		t.Fatalf("different hand-written client: %v", err)
	}
}

// fakeApp is an everydayApp's pod: its shell's private files and its apply-setup, which
// activates by graceful restart.
type fakeApp struct {
	a                everydayApp
	files            map[string]string
	placed           []string // placeScript's mode and path, in order
	calls            []string // apply-setup arguments
	applied, pending bool
}

func (c *fakeApp) stream(args []string, o remotecommand.StreamOptions) (string, error) {
	if args[0] == "sh" {
		if args[2] != placeScript {
			return "", errors.New("unexpected script")
		}
		mode, p := args[4], args[5]
		c.placed = append(c.placed, mode+" "+p)
		b, _ := io.ReadAll(o.Stdin)
		if old, ok := c.files[p]; ok {
			if old != string(b) {
				return "", errors.New("differs")
			}
			return "", nil
		}
		if mode != "write" {
			return "", errors.New("missing")
		}
		c.files[p] = string(b)
		return "", nil
	}
	if args[0] != productAdapters[c.a.app].binary {
		return "", errors.New("unexpected binary " + args[0])
	}
	c.calls = append(c.calls, strings.Join(args[1:], " "))
	switch {
	case slices.Contains(args, "--capabilities"):
		return `{"schema":"ky.setup-capabilities/1","service":"` + c.a.app + `","bundleSchema":"ky.product-setup/1","features":["kyidentity-oidc","directory-webhook","dedicated-admin","local-backup-config","check","restart"]}`, nil
	case slices.Contains(args, "--restart"):
		c.pending = false
		return "", nil
	}
	check := slices.Contains(args, "--check")
	if c.files[args[slices.Index(args, "--file")+1]] == "" || check && !c.applied {
		return "", errors.New("not applied")
	}
	created, pending := `[]`, `[]`
	if !check && !c.applied {
		c.applied, c.pending, created = true, true, `[{"kind":"identity","name":"owner-admin"},{"kind":"identity","name":"owner"}]`
	}
	if c.pending {
		pending = `[{"kind":"activation","name":"restart"}]`
	}
	return `{"schema":"ky.product-setup-report/1","service":"` + c.a.app + `","setupId":"` + c.a.setupID + `","created":` + created + `,"present":[],"pending":` + pending + `}`, nil
}

// appSession loads the plan over ready KyIdentity and app pods; running adds KyIdentity's
// Deployment, as an apply after its first setup sees it.
func appSession(t *testing.T, f *fixture, identity *fakeIdentity, cal *fakeApp, running bool) *session {
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
	for _, app := range []string{"kyidentity", cal.a.app} {
		f.cs.CoreV1().Pods("kyq-"+app).Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "app-1", Labels: map[string]string{"app.kubernetes.io/name": app, managed.Label: managed.Value}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
		}, metav1.CreateOptions{})
	}
	stream := func(ctx context.Context, namespace, pod, container string, args []string, o remotecommand.StreamOptions) error {
		if namespace == "kyq-kyidentity" {
			return identity.stream(ctx, namespace, pod, container, args, o)
		}
		out, err := cal.stream(args, o)
		if err == nil {
			_, err = io.WriteString(o.Stdout, out)
		}
		return err
	}
	s.drivers["k1"] = kubeDriver{c: kube.Fake(f.cs, "https://k1.test", stream)}
	s.edges = nil
	return s
}

// AC7: kycalendar.setup exports the client, takes the everyday subject from KyIdentity's
// read-only inspection of its first setup, places the secrets and the bundle, applies and
// re-exports. Verify only checks.
func TestKyCalendarSetup(t *testing.T) {
	setup := everydaySetup(t, calendarApp)
	// Golden from origin/master eca04d6, before KyMessages shared the everyday path.
	if h := setup.InputHash(); h != "226358e905fa7132cad9ce5f50744b56ef19869844bc826f6e661ee5d73ba89f" {
		t.Errorf("kycalendar.setup input hash = %s", h)
	}
}

// everydaySetup proves a's setup step and returns it.
func everydaySetup(t *testing.T, a everydayApp) engine.Step {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	a.plan(t, f, "      clients: []\n")
	identity := &fakeIdentity{exports: map[string]string{a.app: a.export(parentSetup)}}
	cal := &fakeApp{a: a, files: map[string]string{}}
	steps := stepsByID(t, appSession(t, f, identity, cal, false))
	if err := steps["kyidentity.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	identity.checkReport = firstSetupCheck(ownerReused)
	setup := steps[a.app+".setup"]
	if done, _ := setup.Inspect(ctx); done || len(cal.files) != 0 {
		t.Fatal("fresh app inspected as done, or inspection wrote")
	}
	identity.calls, cal.calls, cal.placed = nil, nil, nil
	if err := setup.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	first := "/data/installer/" + parentSetup + "/setup.json"
	export := "/usr/local/bin/kyidentity apply-setup --file " + first + " --export-client " + a.app
	calls := slices.DeleteFunc(slices.Clone(identity.calls), func(c string) bool { return strings.Contains(c, "--capabilities") })
	if !slices.Equal(calls, []string{export, "/usr/local/bin/kyidentity apply-setup --file " + first + " --check", export}) {
		t.Fatalf("KyIdentity calls %q", identity.calls)
	}
	bundle := a.bundle()
	dir := path.Dir(bundle)
	if !slices.Equal(cal.placed, []string{"write " + dir + "/client.secret", "write " + dir + "/directory.secret", "write " + bundle}) {
		t.Fatalf("placed %q", cal.placed)
	}
	if !slices.Equal(cal.calls, []string{"apply-setup --capabilities", "apply-setup --file " + bundle, "apply-setup --restart", "apply-setup --file " + bundle + " --check"}) {
		t.Fatalf("%s calls %q", a.name, cal.calls)
	}
	want := `{"admins":[{"issuer":"https://identity.example.com","subject":"` + adminSubject + `","username":"owner-admin"}],` +
		`"everyday":[{"issuer":"https://identity.example.com","subject":"` + ownerSubject + `","username":"owner"}],` +
		`"schema":"ky.product-setup/1","service":"` + a.app + `","setupId":"` + a.setupID + `",` +
		`"sso":{"clientId":"` + a.app + `","clientSecretFile":"` + dir + `/client.secret","directorySecretFile":"` + dir + `/directory.secret","issuerUrl":"https://identity.example.com","redirectUri":"` + a.callback() + `"}}`
	if cal.files[bundle] != want {
		t.Fatalf("bundle\n got %s\nwant %s", cal.files[bundle], want)
	}
	if cal.files[dir+"/client.secret"] != clientSecret || cal.files[dir+"/directory.secret"] != directorySecret {
		t.Fatal("secrets misplaced")
	}

	// Verify: placements only compare, every apply-setup is a check; nothing is written.
	identity.calls, cal.calls, cal.placed = nil, nil, nil
	before := snapshot(cal.files)
	if err := setup.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range cal.placed {
		if !strings.HasPrefix(p, "check ") {
			t.Errorf("Verify placed %q", p)
		}
	}
	readOnly(t, cal.calls)
	readOnly(t, slices.DeleteFunc(identity.calls, func(c string) bool { return strings.HasSuffix(c, "--export-client "+a.app) }))
	if after := snapshot(cal.files); after != before {
		t.Fatal("Verify wrote")
	}

	// The everyday subject must be reported exactly once, by a clean inspection; otherwise
	// nothing is placed.
	var errs []string
	fixed := "KyIdentity's read-only inspection of its first setup " + first + " does not report everyday account owner exactly once; check that the plan's KyIdentity everyday username is the one that setup created"
	for name, report := range map[string]string{
		"missing":  firstSetupCheck(""),
		"twice":    firstSetupCheck(ownerReused + ownerReused),
		"created":  strings.Replace(firstSetupCheck(ownerReused), `"created":[]`, `"created":[{"kind":"user","name":"owner","id":"x"}]`, 1),
		"not json": `{"schema":"kyidentity.setup/1",`,
		"group":    firstSetupCheck(strings.Replace(ownerReused, `"kind":"user"`, `"kind":"group"`, 1)),
	} {
		identity.checkReport = report
		saved := *cal
		*cal = fakeApp{a: a, files: map[string]string{}}
		err := setup.Apply(ctx)
		if err == nil || err.Error() != fixed || len(cal.placed) != 0 {
			t.Errorf("%s: %v, placed %q", name, err, cal.placed)
		}
		if err != nil {
			errs = append(errs, err.Error())
		}
		*cal = saved
	}

	// A changed subject on rerun is refused: the retained bundle is compared, never replaced.
	identity.checkReport = firstSetupCheck(strings.Replace(ownerReused, ownerSubject, "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d", 1))
	err := setup.Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), a.name+"'s setup.json is missing, unsafe or differs") || cal.files[bundle] != want {
		t.Fatalf("changed subject: %v", err)
	}
	errs = append(errs, err.Error())
	for _, e := range errs {
		if strings.Contains(e, clientSecret) || strings.Contains(e, directorySecret) {
			t.Fatalf("secret in error %q", e)
		}
	}
	if strings.Contains(f.out.String(), clientSecret) || strings.Contains(f.out.String(), directorySecret) {
		t.Fatal("secret in output")
	}

	// The everyday username is part of the step's input.
	h := setup.InputHash()
	c := setup.(clientSetupStep)
	c.everyday = &everydayUser{Bundle: c.everyday.Bundle, Username: "other"}
	if c.InputHash() == h {
		t.Fatal("everyday username not in the input hash")
	}
	return setup
}

func snapshot(m map[string]string) string {
	b, _ := json.Marshal(m)
	return string(b)
}

// kyPulse's and KyRecovery's bundles and step inputs are byte for byte those of the code
// before KyCalendar (golden values from origin/master 8260bbf): no everyday.
func TestBundlesWithoutEveryday(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	pulsePlan(t, f)
	identity := &fakeIdentity{exports: map[string]string{"kyrecovery": recoveryExport("https://identity.example.com"), "kypulse": pulseExport("https://identity.example.com")}}
	pulse := &fakePulse{files: map[string]string{}, caps: pulseCaps}
	steps := pulseSteps(t, f, identity, pulse)
	if err := steps["kyidentity.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := steps["kypulse.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	const wantPulse = `{"admins":[{"issuer":"https://identity.example.com","subject":"5e0c8a52-6d1b-4f7e-9a3c-2b1d0e9f8a7c","username":"owner-admin"}],"monitoring":{"targets":[{"key":"kyidentity","name":"kyidentity","url":"https://identity.example.com/readyz"},{"key":"kyrecovery","name":"kyrecovery","url":"https://recovery.example.com/healthz"}]},"schema":"ky.product-setup/1","service":"kypulse","setupId":"7c2d9e4a-1b3f-4a5c-8d6e-9f0a1b2c3d4e","sso":{"clientId":"kypulse","clientSecretFile":"/app/data/installer/7c2d9e4a-1b3f-4a5c-8d6e-9f0a1b2c3d4e/client.secret","directorySecretFile":"/app/data/installer/7c2d9e4a-1b3f-4a5c-8d6e-9f0a1b2c3d4e/directory.secret","issuerUrl":"https://identity.example.com","redirectUri":"https://pulse.example.com/api/sso/kysignon/callback"}}`
	if pulse.files[pulseBundle] != wantPulse {
		t.Fatalf("kyPulse bundle = %s", pulse.files[pulseBundle])
	}
	for id, want := range map[string]string{
		"kypulse.setup":    "03080f0c4092e56e5776fac2521f2516b307d6890cb3608d6c0e3da88847d606",
		"kyrecovery.setup": "8e7038d0bbb27fad84937ff7711ce840bfa6a82ba2523c859fad9d9d14f4e5fe",
	} {
		if got := steps[id].InputHash(); got != want {
			t.Errorf("%s input hash = %s", id, got)
		}
	}

	f = newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	recoveryPlan(t, f, "      clients: []\n")
	identity = &fakeIdentity{export: recoveryExport("https://identity.example.com")}
	recovery := &fakeRecovery{files: map[string]string{}}
	rsteps := recoverySteps(t, f, identity, recovery)
	if err := rsteps["kyidentity.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rsteps["kyrecovery.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	const wantRecovery = `{"admins":[{"issuer":"https://identity.example.com","subject":"5e0c8a52-6d1b-4f7e-9a3c-2b1d0e9f8a7c","username":"owner-admin"}],"schema":"ky.product-setup/1","service":"kyrecovery","setupId":"0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c","sso":{"clientId":"kyrecovery","clientSecretFile":"/app/data/installer/0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c/client.secret","directorySecretFile":"/app/data/installer/0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c/directory.secret","issuerUrl":"https://identity.example.com","redirectUri":"https://recovery.example.com/api/auth/callback"}}`
	if recovery.files[recoveryBundle] != wantRecovery {
		t.Fatalf("KyRecovery bundle = %s", recovery.files[recoveryBundle])
	}
}

// AC6: KyCalendar joining after KyIdentity's first setup comes in a setup amendment, applied
// before kycalendar.setup; its export reads the amendment, its everyday subject the first setup.
func TestKyCalendarAddedByAmendment(t *testing.T) { everydayAmendment(t, calendarApp) }

func everydayAmendment(t *testing.T, a everydayApp) {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	identity := setUpIdentity(t, f, amendmentFeatures)
	identity.checkReport = firstSetupCheck(ownerReused)
	a.plan(t, f, "      clients: []\n")
	id := amendmentID(parentSetup, a.app)
	bundle := "/data/installer/" + id + "/setup.json"
	amendment := &fakeIdentity{parent: identity, export: a.export(id)}
	identity.setups[bundle] = amendment
	cal := &fakeApp{a: a, files: map[string]string{}}

	s := appSession(t, f, identity, cal, true)
	prior, err := s.ensureInstallation(f.state)
	if err != nil {
		t.Fatal(err)
	}
	publish, err := s.amendLate(ctx, f.state, prior)
	if err != nil || publish == nil || len(s.amendments) != 1 || len(s.derived) != 0 {
		t.Fatalf("no amendment recorded: %v", err)
	}
	readOnly(t, identity.calls)
	if !strings.Contains(f.out.String(), "ran without "+a.app+"'s client: adding it with setup amendment "+id) {
		t.Fatalf("output = %q", f.out.String())
	}
	want := `{"schema":"kyidentity.setup/1","setupId":"` + id + `","extends":"` + parentSetup + `","clients":[` + a.client() + `]}`
	if got := string(s.amendments[0].bundle); got != want {
		t.Fatalf("amendment bundle\n got %s\nwant %s", got, want)
	}
	steps := setupStepsOf(t, s)
	if got := strings.Join(ids(steps), " "); got != "kyidentity.setup kyidentity.amend."+a.app+" kyidentity.retire-bootstrap "+a.app+".setup" {
		t.Fatalf("steps = %s", got)
	}
	identity.calls = nil
	var out bytes.Buffer
	e := &engine.Engine{Dir: t.TempDir(), Out: &out, Redact: &engine.Redactor{}}
	if err := e.Run(ctx, steps); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if amendment.intent != want || !cal.applied || !strings.Contains(cal.files[a.bundle()], `"subject":"`+ownerSubject+`"`) {
		t.Fatalf("staged %s, %s applied %v, bundle %s", amendment.intent, a.name, cal.applied, cal.files[a.bundle()])
	}
	if !slices.Contains(identity.calls, "/usr/local/bin/kyidentity apply-setup --file /data/installer/"+parentSetup+"/setup.json --check") ||
		slices.ContainsFunc(identity.calls, func(c string) bool { return strings.Contains(c, "--export-client") && !strings.Contains(c, bundle) }) {
		t.Fatalf("export must read the amendment, the inspection the first setup: %q", identity.calls)
	}
	out.Reset()
	if err := e.Run(ctx, steps); err != nil || strings.Count(out.String(), "skipped") != 4 {
		t.Fatalf("rerun: %v\n%s", err, out.String())
	}
	if err := publish(); err != nil || f.stack(t).Integrations[1].IdentityAmendment != id {
		t.Fatalf("amendment not recorded: %v", err)
	}
}

// AC9: KyCalendar's setup follows its health and precedes every edge step, as kyPulse's.
func TestKyCalendarSetupBeforeEdges(t *testing.T) { setupBeforeEdges(t, calendarApp) }

func setupBeforeEdges(t *testing.T, a everydayApp) {
	t.Helper()
	if slices.Contains(routedFirst, a.app) {
		t.Fatal(a.app + " in routedFirst")
	}
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	a.plan(t, f, "      clients: []\n")
	s := appSession(t, f, &fakeIdentity{}, &fakeApp{a: a, files: map[string]string{}}, false)
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
	setup := slices.Index(order, a.app+".setup")
	firstEdge := slices.IndexFunc(order, func(id string) bool { return strings.HasPrefix(id, "k1.edge.") })
	route := slices.IndexFunc(list, func(st engine.Step) bool { return edge.RouteApp(st) == a.app })
	if slices.Contains(order, "kyidentity.scim."+a.app) {
		t.Fatalf("SCIM step for %s: %v", a.app, order)
	}
	if setup < 1 || order[setup-1] != a.app+".health" || firstEdge < 0 || setup > firstEdge || route < firstEdge {
		t.Fatalf("steps = %v", order)
	}
}

// AC8: owner usernames KyCalendar's setup would refuse are refused before any connection.
func TestKyCalendarUsernamesRefused(t *testing.T) { usernamesRefused(t, calendarApp) }

func usernamesRefused(t *testing.T, a everydayApp) {
	t.Helper()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
		t.Fatal("connected")
		return nil, nil
	}
	set := func(installed bool, account, name string) {
		a.plan(t, f, "      clients: []\n")
		p := filepath.Join(f.state, "stack.yaml")
		b, _ := os.ReadFile(p)
		y := strings.Replace(string(b), account+": {username: "+map[string]string{"admin": "owner-admin", "everyday": "owner"}[account]+",", account+": {username: "+name+",", 1)
		if !installed {
			y = strings.Replace(y, "  - name: "+a.app+"\n    target: k1\n", "", 1)
			y = strings.Replace(y, "      - app: "+a.app+"\n", "      - app: npm\n", 1)
			y = strings.Replace(y, "  - product: "+a.app+"\n    target: k1\n    bundle: "+a.bundle()+"\n    identity_client: "+a.app+"\n", "", 1)
		}
		os.WriteFile(p, []byte(y), 0o600)
	}
	for _, c := range [][2]string{{"admin", "admin"}, {"admin", "Admin"}, {"everyday", "ADMIN"}, {"everyday", "ow"}, {"admin", "a"}, {"everyday", "o1"}} {
		set(true, c[0], c[1])
		for _, cmd := range [][]string{{"review"}, {"plan", "--noninteractive"}, {"apply"}} {
			err := f.run(cmd...)
			// KyCalendar's text as job 002 shipped it, with the product's name.
			want := a.name + ` cannot take the KyIdentity ` + c[0] + ` username "` + c[1] + `": its usernames are 3 to 64 letters, digits, ".", "_" or "-", and "admin" is its own break-glass account. Change identity.` + c[0] + `.username in the kyidentity integration`
			if err == nil || err.Error() != want {
				t.Errorf("%s %s=%s: %v", cmd[0], c[0], c[1], err)
			}
		}
		set(false, c[0], c[1])
		if err := f.run("review"); err != nil && strings.Contains(err.Error(), a.name) {
			t.Errorf("without %s, %s=%s refused: %v", a.app, c[0], c[1], err)
		}
	}
}

// AC16: KyCalendar on a Docker host is refused before any connection, naming Kubernetes.
func TestKyCalendarOnDockerRefused(t *testing.T) { dockerRefused(t, "kycalendar") }

func dockerRefused(t *testing.T, app string) {
	t.Helper()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	catalogIdentityPlan(t, f, "kyidentity", "")
	p := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "    target: k1\nedges:", "    target: k1\n  - name: "+app+"\n    target: nas\nedges:", 1)), 0o600)
	dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
		t.Fatal("connected")
		return nil, nil
	}
	for _, cmd := range [][]string{{"review"}, {"plan", "--noninteractive"}, {"apply"}} {
		if err := f.run(cmd...); err == nil || !strings.Contains(err.Error(), app) || !strings.Contains(err.Error(), "place it on a Kubernetes target") {
			t.Errorf("%s: %v", cmd[0], err)
		}
	}
}
