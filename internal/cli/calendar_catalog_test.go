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
	calendarBundle  = "/app/data/installer/" + calendarSetupID + "/setup.json"
	ownerSubject    = "9b1d4e7a-2c3f-4a5b-8d6e-7f8091a2b3c4"
	adminSubject    = "5e0c8a52-6d1b-4f7e-9a3c-2b1d0e9f8a7c"
)

// calendarPlan adds the catalog KyCalendar, routed at calendar.example.com, to the catalog
// KyIdentity plan.
func calendarPlan(t *testing.T, f *fixture, clients string) {
	catalogIdentityPlan(t, f, "kyidentity", "  - product: kycalendar\n    target: k1\n    bundle: "+calendarBundle+"\n    identity_client: kycalendar\n")
	p := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(p)
	y := strings.Replace(string(b), "    target: k1\nedges:", "    target: k1\n  - name: kycalendar\n    target: k1\nedges:", 1)
	y = strings.Replace(y, "integrations:", "      - app: kycalendar\n        hostname: calendar.example.com\n        forward_host: app.kyq-kycalendar.svc.cluster.local\n        forward_port: 8080\n        certificate_id: 1\nintegrations:", 1)
	y = strings.Replace(y, "      clients: []\n", clients, 1)
	os.WriteFile(p, []byte(y), 0o600)
}

const wantCalendarClient = `{"id":"kycalendar","name":"KyCalendar","redirectUris":["https://calendar.example.com/api/sso/kysignon/callback"],"postLogoutUris":null,"administratorRole":"kycalendar.admin","everydayRole":"kycalendar.user","suiteWebhook":{"url":"https://calendar.example.com/api/sso/kysignon/sync"}}`

func calendarExport(setupID string) string {
	return `{"schema":"kyidentity.client-credentials/1","setupId":"` + setupID + `","issuerUrl":"https://identity.example.com","clientId":"kycalendar",` +
		`"redirectUris":["https://calendar.example.com/api/sso/kysignon/callback"],"directoryUrl":"https://calendar.example.com/api/sso/kysignon/sync",` +
		`"clientSecret":"` + clientSecret + `","directorySecret":"` + directorySecret + `",` +
		`"administrator":{"subject":"` + adminSubject + `","username":"owner-admin","role":"kycalendar.admin"}}`
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
	calendarPlan(t, f, "      clients: []\n")
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(s.integrations[0].Identity.Clients); string(b) != "["+wantCalendarClient+"]" || len(s.amendments) != 0 || !slices.Equal(s.derived, []string{"kycalendar"}) {
		t.Fatalf("derived clients = %s, amendments %d", b, len(s.amendments))
	}
	kubectl := "kubectl --kubeconfig '" + s.placed["kycalendar"].Kubeconfig + "' -n kyq-kycalendar exec deployment/app -- "
	for _, want := range []string{"KyCalendar at https://calendar.example.com/", "as owner-admin (its console only", "or owner (calendars)",
		kubectl + "cat /app/data/initial-admin-password", `local "admin" break-glass`, "No catalog product can pair with KyRecovery in this release", "The KyPost-KyCalendar invitation pairing is not configured by this installer."} {
		if !strings.Contains(s.handover, want) {
			t.Errorf("handover lacks %q: %s", want, s.handover)
		}
	}
	// The file goes only once the password is changed.
	if change, rm := strings.Index(s.handover, "change it. Only after that"), strings.Index(s.handover, kubectl+"rm /app/data/initial-admin-password"); change < 0 || rm < change {
		t.Errorf("break-glass steps out of order: %s", s.handover)
	}
	written := "      clients:\n        - id: kycalendar\n          name: KyCalendar\n          redirect_uris: [https://calendar.example.com/api/sso/kysignon/callback]\n          administrator_role: kycalendar.admin\n          everyday_role: kycalendar.user\n          suite_webhook: {url: https://calendar.example.com/api/sso/kysignon/sync}\n"
	calendarPlan(t, f, written)
	if s, err := load(f.state, nil, f.o); err != nil || len(s.integrations[0].Identity.Clients) != 1 {
		t.Fatalf("matching hand-written client: %v", err)
	}
	calendarPlan(t, f, strings.Replace(written, "kycalendar.user", "user", 1))
	if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), "differs from the catalog's KyCalendar client") {
		t.Fatalf("different hand-written client: %v", err)
	}
}

// fakeCalendar is KyCalendar's pod: its shell's private files and its apply-setup, which
// activates by graceful restart.
type fakeCalendar struct {
	files            map[string]string
	placed           []string // placeScript's mode and path, in order
	calls            []string // apply-setup arguments
	applied, pending bool
}

func (c *fakeCalendar) stream(args []string, o remotecommand.StreamOptions) (string, error) {
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
	if args[0] != "/app/kycalendar" {
		return "", errors.New("unexpected binary " + args[0])
	}
	c.calls = append(c.calls, strings.Join(args[1:], " "))
	switch {
	case slices.Contains(args, "--capabilities"):
		return `{"schema":"ky.setup-capabilities/1","service":"kycalendar","bundleSchema":"ky.product-setup/1","features":["kyidentity-oidc","directory-webhook","dedicated-admin","local-backup-config","check","restart"]}`, nil
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
	return `{"schema":"ky.product-setup-report/1","service":"kycalendar","setupId":"` + calendarSetupID + `","created":` + created + `,"present":[],"pending":` + pending + `}`, nil
}

// calendarSession loads the plan over ready KyIdentity and KyCalendar pods; running adds
// KyIdentity's Deployment, as an apply after its first setup sees it.
func calendarSession(t *testing.T, f *fixture, identity *fakeIdentity, cal *fakeCalendar, running bool) *session {
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
	for _, app := range []string{"kyidentity", "kycalendar"} {
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
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	calendarPlan(t, f, "      clients: []\n")
	identity := &fakeIdentity{exports: map[string]string{"kycalendar": calendarExport(parentSetup)}}
	cal := &fakeCalendar{files: map[string]string{}}
	steps := stepsByID(t, calendarSession(t, f, identity, cal, false))
	if err := steps["kyidentity.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	identity.checkReport = firstSetupCheck(ownerReused)
	setup := steps["kycalendar.setup"]
	if done, _ := setup.Inspect(ctx); done || len(cal.files) != 0 {
		t.Fatal("fresh KyCalendar inspected as done, or inspection wrote")
	}
	identity.calls, cal.calls, cal.placed = nil, nil, nil
	if err := setup.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	first := "/data/installer/" + parentSetup + "/setup.json"
	export := "/usr/local/bin/kyidentity apply-setup --file " + first + " --export-client kycalendar"
	calls := slices.DeleteFunc(slices.Clone(identity.calls), func(c string) bool { return strings.Contains(c, "--capabilities") })
	if !slices.Equal(calls, []string{export, "/usr/local/bin/kyidentity apply-setup --file " + first + " --check", export}) {
		t.Fatalf("KyIdentity calls %q", identity.calls)
	}
	dir := path.Dir(calendarBundle)
	if !slices.Equal(cal.placed, []string{"write " + dir + "/client.secret", "write " + dir + "/directory.secret", "write " + calendarBundle}) {
		t.Fatalf("placed %q", cal.placed)
	}
	if !slices.Equal(cal.calls, []string{"apply-setup --capabilities", "apply-setup --file " + calendarBundle, "apply-setup --restart", "apply-setup --file " + calendarBundle + " --check"}) {
		t.Fatalf("KyCalendar calls %q", cal.calls)
	}
	want := `{"admins":[{"issuer":"https://identity.example.com","subject":"` + adminSubject + `","username":"owner-admin"}],` +
		`"everyday":[{"issuer":"https://identity.example.com","subject":"` + ownerSubject + `","username":"owner"}],` +
		`"schema":"ky.product-setup/1","service":"kycalendar","setupId":"` + calendarSetupID + `",` +
		`"sso":{"clientId":"kycalendar","clientSecretFile":"` + dir + `/client.secret","directorySecretFile":"` + dir + `/directory.secret","issuerUrl":"https://identity.example.com","redirectUri":"https://calendar.example.com/api/sso/kysignon/callback"}}`
	if cal.files[calendarBundle] != want {
		t.Fatalf("bundle\n got %s\nwant %s", cal.files[calendarBundle], want)
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
	readOnly(t, slices.DeleteFunc(identity.calls, func(c string) bool { return strings.HasSuffix(c, "--export-client kycalendar") }))
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
		*cal = fakeCalendar{files: map[string]string{}}
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
	if err == nil || !strings.Contains(err.Error(), "KyCalendar's setup.json is missing, unsafe or differs") || cal.files[calendarBundle] != want {
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
func TestKyCalendarAddedByAmendment(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	identity := setUpIdentity(t, f, amendmentFeatures)
	identity.checkReport = firstSetupCheck(ownerReused)
	calendarPlan(t, f, "      clients: []\n")
	id := amendmentID(parentSetup, "kycalendar")
	bundle := "/data/installer/" + id + "/setup.json"
	amendment := &fakeIdentity{parent: identity, export: calendarExport(id)}
	identity.setups[bundle] = amendment
	cal := &fakeCalendar{files: map[string]string{}}

	s := calendarSession(t, f, identity, cal, true)
	prior, err := s.ensureInstallation(f.state)
	if err != nil {
		t.Fatal(err)
	}
	publish, err := s.amendLate(ctx, f.state, prior)
	if err != nil || publish == nil || len(s.amendments) != 1 || len(s.derived) != 0 {
		t.Fatalf("no amendment recorded: %v", err)
	}
	readOnly(t, identity.calls)
	if !strings.Contains(f.out.String(), "ran without kycalendar's client: adding it with setup amendment "+id) {
		t.Fatalf("output = %q", f.out.String())
	}
	want := `{"schema":"kyidentity.setup/1","setupId":"` + id + `","extends":"` + parentSetup + `","clients":[` + wantCalendarClient + `]}`
	if got := string(s.amendments[0].bundle); got != want {
		t.Fatalf("amendment bundle\n got %s\nwant %s", got, want)
	}
	steps := setupStepsOf(t, s)
	if got := strings.Join(ids(steps), " "); got != "kyidentity.setup kyidentity.amend.kycalendar kyidentity.retire-bootstrap kycalendar.setup" {
		t.Fatalf("steps = %s", got)
	}
	identity.calls = nil
	var out bytes.Buffer
	e := &engine.Engine{Dir: t.TempDir(), Out: &out, Redact: &engine.Redactor{}}
	if err := e.Run(ctx, steps); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if amendment.intent != want || !cal.applied || !strings.Contains(cal.files[calendarBundle], `"subject":"`+ownerSubject+`"`) {
		t.Fatalf("staged %s, KyCalendar applied %v, bundle %s", amendment.intent, cal.applied, cal.files[calendarBundle])
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
func TestKyCalendarSetupBeforeEdges(t *testing.T) {
	if slices.Contains(routedFirst, "kycalendar") {
		t.Fatal("kycalendar in routedFirst")
	}
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	calendarPlan(t, f, "      clients: []\n")
	s := calendarSession(t, f, &fakeIdentity{}, &fakeCalendar{files: map[string]string{}}, false)
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
	setup := slices.Index(order, "kycalendar.setup")
	firstEdge := slices.IndexFunc(order, func(id string) bool { return strings.HasPrefix(id, "k1.edge.") })
	route := slices.IndexFunc(list, func(st engine.Step) bool { return edge.RouteApp(st) == "kycalendar" })
	if setup < 1 || order[setup-1] != "kycalendar.health" || firstEdge < 0 || setup > firstEdge || route < firstEdge {
		t.Fatalf("steps = %v", order)
	}
}

// AC8: owner usernames KyCalendar's setup would refuse are refused before any connection.
func TestKyCalendarUsernamesRefused(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
		t.Fatal("connected")
		return nil, nil
	}
	set := func(calendar bool, account, name string) {
		calendarPlan(t, f, "      clients: []\n")
		p := filepath.Join(f.state, "stack.yaml")
		b, _ := os.ReadFile(p)
		y := strings.Replace(string(b), account+": {username: "+map[string]string{"admin": "owner-admin", "everyday": "owner"}[account]+",", account+": {username: "+name+",", 1)
		if !calendar {
			y = strings.Replace(y, "  - name: kycalendar\n    target: k1\n", "", 1)
			y = strings.Replace(y, "      - app: kycalendar\n", "      - app: npm\n", 1)
			y = strings.Replace(y, "  - product: kycalendar\n    target: k1\n    bundle: "+calendarBundle+"\n    identity_client: kycalendar\n", "", 1)
		}
		os.WriteFile(p, []byte(y), 0o600)
	}
	for _, c := range [][2]string{{"admin", "admin"}, {"admin", "Admin"}, {"everyday", "ADMIN"}, {"everyday", "ow"}, {"admin", "a"}, {"everyday", "o1"}} {
		set(true, c[0], c[1])
		for _, cmd := range [][]string{{"review"}, {"plan", "--noninteractive"}, {"apply"}} {
			err := f.run(cmd...)
			if err == nil || !strings.Contains(err.Error(), `KyCalendar cannot take the KyIdentity `+c[0]+` username "`+c[1]+`"`) || !strings.Contains(err.Error(), "3 to 64 letters, digits") || !strings.Contains(err.Error(), "identity."+c[0]+".username") {
				t.Errorf("%s %s=%s: %v", cmd[0], c[0], c[1], err)
			}
		}
		set(false, c[0], c[1])
		if err := f.run("review"); err != nil && strings.Contains(err.Error(), "KyCalendar") {
			t.Errorf("without kycalendar, %s=%s refused: %v", c[0], c[1], err)
		}
	}
}

// AC16: KyCalendar on a Docker host is refused before any connection, naming Kubernetes.
func TestKyCalendarOnDockerRequiresRoute(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	catalogIdentityPlan(t, f, "kyidentity", "")
	p := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "    target: k1\nedges:", "    target: k1\n  - name: kycalendar\n    target: nas\nedges:", 1)), 0o600)
	dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
		t.Fatal("connected")
		return nil, nil
	}
	for _, cmd := range [][]string{{"review"}, {"plan", "--noninteractive"}, {"apply"}} {
		if err := f.run(cmd...); err == nil || !strings.Contains(err.Error(), "kycalendar") || !strings.Contains(err.Error(), "route") {
			t.Errorf("%s: %v", cmd[0], err)
		}
	}
}
