package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	postSetupID    = "8d4f2a6c-3b1e-4c5d-9a7f-6e0b1c2d3e4f"
	postBundlePath = "/kypost/private/installer/" + postSetupID + "/setup.json"
	postSecret     = "UG9zdFNlY3JldFBvc3RTZWNyZXRQb3N0U2VjcmV0UG8"
)

// postPlan installs KyIdentity on cluster k1 and KyPost on Docker host nas, both routed
// through k1's lan edge; clients replaces KyIdentity's empty client list.
func postPlan(t *testing.T, f *fixture, clients string) {
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	f.plan(t, "  - name: k1\n    kubeconfig: "+kc+"\napps:\n  - name: npm\n    target: k1\n  - name: kyidentity\n    target: k1\n  - name: kypost\n    target: nas\n"+
		"edges:\n  - target: k1\n    provider: lan\n    npm_url: kube://kyq-npm/app:81\n    allow_cidrs: [192.168.1.0/24]\n    routes:\n"+
		"      - app: kyidentity\n        hostname: identity.example.com\n        forward_host: app.kyq-kyidentity.svc.cluster.local\n        forward_port: 5867\n        certificate_id: 1\n"+
		"      - app: kypost\n        hostname: post.example.com\n        forward_host: 192.0.2.1\n        forward_port: 18866\n        certificate_id: 1\n"+
		"integrations:\n  - product: kyidentity\n    target: k1\n    bundle: /data/installer/"+parentSetup+"/setup.json\n"+strings.Replace(identityPrep, "      clients: []\n", clients, 1)+
		"  - product: kypost\n    target: nas\n    bundle: "+postBundlePath+"\n    identity_client: kypost\n")
}

const wantPostClient = `{"id":"kypost","name":"KyPost","redirectUris":["https://post.example.com/api/auth/oidc/callback"],"postLogoutUris":null,"administratorRole":"kypost.admin","everydayRole":"kypost.user"}`

func postExport(setupID, secret string) string {
	return `{"schema":"kyidentity.client-credentials/1","setupId":"` + setupID + `","issuerUrl":"https://identity.example.com","clientId":"kypost",` +
		`"redirectUris":["https://post.example.com/api/auth/oidc/callback"],"clientSecret":"` + secret + `",` +
		`"administrator":{"subject":"` + adminSubject + `","username":"owner-admin","role":"kypost.admin"}}`
}

// AC5: one apply installing KyIdentity and KyPost puts KyPost's client, without a directory,
// in the first setup; a differing hand-written client is refused.
func TestKyPostClientFromCatalog(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	postPlan(t, f, "      clients: []\n")
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(s.integrations[0].Identity.Clients); string(b) != "["+wantPostClient+"]" || len(s.amendments) != 0 || !slices.Equal(s.derived, []string{"kypost"}) {
		t.Fatalf("derived clients = %s, amendments %d", b, len(s.amendments))
	}
	if len(f.stack(t).Integrations[0].Identity.Clients) != 0 {
		t.Fatal("derived client saved in the plan")
	}
	for _, want := range []string{
		"KyPost at https://post.example.com/", "as owner-admin, for administration only", "it can also add mail\n  accounts", "or as owner for mail",
		"KyIdentity role kypost.user and gets a KyPost account at first sign-in", "opens no mail port", "Native\n  mail, receiving, a relay and domains are not set up",
		"SANDBOX_PRIVATE_HOSTS", "on nas:\n       docker exec --user kypost kyq-kypost-app-1 cat /kypost/config/first-run-password.txt",
		"docker exec --user kypost kyq-kypost-app-1 rm /kypost/config/first-run-password.txt", "stops new sign-ins",
		"To end every live session, on nas:\n       docker restart kyq-kypost-app-1", "Pair it with KyRecovery",
	} {
		if !strings.Contains(s.handover, want) {
			t.Errorf("handover lacks %q:\n%s", want, s.handover)
		}
	}
	if change, rm := strings.Index(s.handover, "change it. Only after that"), strings.Index(s.handover, "rm /kypost/config/first-run-password.txt"); change < 0 || rm < change {
		t.Errorf("break-glass steps out of order:\n%s", s.handover)
	}
	written := "      clients:\n        - id: kypost\n          name: KyPost\n          redirect_uris: [https://post.example.com/api/auth/oidc/callback]\n          administrator_role: kypost.admin\n          everyday_role: kypost.user\n"
	postPlan(t, f, written)
	if s, err := load(f.state, nil, f.o); err != nil || len(s.integrations[0].Identity.Clients) != 1 {
		t.Fatalf("matching hand-written client: %v", err)
	}
	for name, bad := range map[string]string{
		"role":      strings.Replace(written, "kypost.user", "user", 1),
		"directory": written + "          suite_webhook: {url: https://post.example.com/api/sync/webhook}\n",
	} {
		postPlan(t, f, bad)
		if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), "differs from the catalog's KyPost client") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// AC3: KyPost on a Kubernetes target, with or without its setup, is refused before any
// connection as a placement error.
func TestKyPostOnKubernetesRefused(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
		t.Fatal("connected")
		return nil, nil
	}
	for _, setup := range []bool{true, false} {
		postPlan(t, f, "      clients: []\n")
		p := filepath.Join(f.state, "stack.yaml")
		b, _ := os.ReadFile(p)
		y := strings.Replace(string(b), "  - name: kypost\n    target: nas\n", "  - name: kypost\n    target: k1\n", 1)
		y = strings.Replace(y, "forward_host: 192.0.2.1", "forward_host: app.kyq-kypost.svc.cluster.local", 1)
		integration := "  - product: kypost\n    target: nas\n    bundle: " + postBundlePath + "\n    identity_client: kypost\n"
		if setup {
			y = strings.Replace(y, integration, strings.Replace(integration, "target: nas", "target: k1", 1), 1)
		} else {
			y = strings.Replace(y, integration, "", 1)
		}
		os.WriteFile(p, []byte(y), 0o600)
		for _, cmd := range [][]string{{"review"}, {"plan", "--noninteractive"}, {"apply"}} {
			if err := f.run(cmd...); err == nil || !strings.Contains(err.Error(), "kypost") || !strings.Contains(err.Error(), "has no Kubernetes deployment; place it on a Docker host") {
				t.Errorf("setup %v, %s: %v", setup, cmd[0], err)
			}
		}
	}
}

// fakePost is KyPost's container behind docker exec: its shell's files and its native
// apply-setup. Every exec must run as kypost.
type fakePost struct {
	files        map[string]string
	placed       []string // placeScript's mode and path, in order
	calls        []string // kypost-server arguments
	applied      bool
	mutableCheck bool // --check reports an applied change
}

// postArgs runs the shell words of cmd after KyPost's container lookup, as the target's shell
// would, with the found container c0ffee: docker exec [-i] --user kypost c0ffee <args>.
func postArgs(t *testing.T, cmd string) []string {
	t.Helper()
	rest, ok := strings.CutPrefix(cmd, dockerAppContainer("kypost"))
	if !ok {
		t.Fatalf("exec without finding KyPost's container: %s", cmd)
	}
	out, err := remote.Local{}.Run(context.Background(), `c=c0ffee; printf '%s\0' `+rest, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if w[2] == "-i" {
		w = slices.Delete(w, 2, 3)
	}
	if !slices.Equal(w[:5], []string{"docker", "exec", "--user", "kypost", "c0ffee"}) {
		t.Fatalf("exec not as kypost into KyPost's container: %q", w)
	}
	return w[5:]
}

const postCaps = `{"schema":"ky.setup-capabilities/1","service":"kypost","bundleSchema":"kypost.setup/1","features":["sso","retained-pairing-key","domains","recovery-pairing","secret-files","check"]}`

func (p *fakePost) exec(t *testing.T) func(string, []byte) ([]byte, error) {
	return func(cmd string, stdin []byte) ([]byte, error) {
		args := postArgs(t, cmd)
		switch {
		case args[0] == "sh":
			if args[1] != "-c" || args[2] != placeScript {
				return nil, errors.New("unexpected script")
			}
			p.placed = append(p.placed, args[4]+" "+args[5])
			return nil, fakePlace(p.files, args[4], args[5], stdin)
		case args[0] != "/usr/local/bin/kypost-server":
			return nil, errors.New("unexpected binary " + args[0])
		}
		p.calls = append(p.calls, strings.Join(args[1:], " "))
		if slices.Contains(args, "--capabilities") {
			return []byte(postCaps), nil
		}
		check := slices.Contains(args, "--check")
		if p.files[args[slices.Index(args, "--file")+1]] == "" || check && !p.applied {
			return nil, errors.New("setup sso is missing; run apply-setup before checking")
		}
		applied, unchanged := `[]`, `["sso"]`
		if !check && !p.applied || check && p.mutableCheck {
			p.applied, applied, unchanged = true, `["sso"]`, `[]`
		}
		return []byte(`{"version":1,"applied":` + applied + `,"unchanged":` + unchanged + `,"dns":[],"pending":["relay","receiving","everyday-assignment","backup-restore","https-proxy"],"configurationApplied":true}`), nil
	}
}

// postSession loads the plan over KyIdentity's ready pod on k1 and KyPost's container on nas.
func postSession(t *testing.T, f *fixture, identity *fakeIdentity, post *fakePost) *session {
	t.Helper()
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	f.cs.CoreV1().Pods("kyq-kyidentity").Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Labels: map[string]string{"app.kubernetes.io/name": "kyidentity", managed.Label: managed.Value}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}, metav1.CreateOptions{})
	s.drivers["k1"] = kubeDriver{c: kube.Fake(f.cs, "https://k1.test", identity.stream)}
	s.drivers["nas"] = sshDriver{t: s.placed["kypost"], c: f.h}
	f.h.exec = post.exec(t)
	s.edges = nil
	return s
}

// AC7: kypost.setup exports the client, places its secret and then KyPost's native bundle as
// kypost, applies with postSetup and re-exports. Verify only checks.
func TestKyPostSetup(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	postPlan(t, f, "      clients: []\n")
	identity := &fakeIdentity{export: postExport(parentSetup, postSecret)}
	post := &fakePost{files: map[string]string{}}
	steps := stepsByID(t, postSession(t, f, identity, post))
	if err := steps["kyidentity.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(identity.intent, wantPostClient) {
		t.Fatalf("KyIdentity intent lacks the client: %s", identity.intent)
	}
	setup := steps["kypost.setup"]
	if done, _ := setup.Inspect(ctx); done || len(post.files) != 0 {
		t.Fatal("fresh KyPost inspected as done, or inspection wrote")
	}
	identity.calls, post.calls, post.placed = nil, nil, nil
	if err := setup.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	export := "/usr/local/bin/kyidentity apply-setup --file /data/installer/" + parentSetup + "/setup.json --export-client kypost"
	if calls := slices.DeleteFunc(slices.Clone(identity.calls), func(c string) bool { return strings.Contains(c, "--capabilities") }); !slices.Equal(calls, []string{export, export}) {
		t.Fatalf("KyIdentity calls %q", identity.calls)
	}
	dir := path.Dir(postBundlePath)
	if !slices.Equal(post.placed, []string{"write " + dir + "/client.secret", "write " + postBundlePath}) {
		t.Fatalf("placed %q", post.placed)
	}
	if !slices.Equal(post.calls, []string{"apply-setup --capabilities", "apply-setup --file " + postBundlePath}) {
		t.Fatalf("KyPost calls %q", post.calls)
	}
	want := `{"version":1,"sso":{"enabled":true,"issuerUrl":"https://identity.example.com","clientId":"kypost","autoProvision":true},"clientSecretFile":"` + dir + `/client.secret"}`
	if post.files[postBundlePath] != want || post.files[dir+"/client.secret"] != postSecret || len(post.files) != 2 {
		t.Fatalf("bundle %s, files %d", post.files[postBundlePath], len(post.files))
	}

	// Verify: placements only compare, every apply-setup is a check; nothing is written.
	identity.calls, post.calls, post.placed = nil, nil, nil
	before := snapshot(post.files)
	if err := setup.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(post.placed, []string{"check " + dir + "/client.secret", "check " + postBundlePath}) {
		t.Fatalf("Verify placed %q", post.placed)
	}
	if !slices.Equal(post.calls, []string{"apply-setup --capabilities", "apply-setup --file " + postBundlePath + " --check"}) {
		t.Fatalf("Verify calls %q", post.calls)
	}
	readOnly(t, slices.DeleteFunc(identity.calls, func(c string) bool { return strings.HasSuffix(c, "--export-client kypost") }))
	if snapshot(post.files) != before {
		t.Fatal("Verify wrote")
	}
	if done, _ := setup.Inspect(ctx); !done {
		t.Fatal("applied setup not inspected as done")
	}

	// A check that reports a change is refused.
	post.mutableCheck = true
	err := setup.Verify(ctx)
	if err == nil || !strings.Contains(err.Error(), "invalid or incomplete KyPost configuration report") {
		t.Fatalf("mutating check: %v", err)
	}
	post.mutableCheck = false
	errs := []string{err.Error()}

	// A changed source credential is refused; the retained secret is compared, never replaced.
	identity.export = postExport(parentSetup, "Q2hhbmdlZENoYW5nZWRDaGFuZ2VkQ2hhbmdlZENoYW4")
	post.calls = nil
	err = setup.Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "KyPost's client.secret is missing, unsafe or differs") || post.files[dir+"/client.secret"] != postSecret || len(post.calls) != 0 {
		t.Fatalf("changed client secret: %v, calls %q", err, post.calls)
	}
	errs = append(errs, err.Error())
	for _, e := range errs {
		if strings.Contains(e, postSecret) {
			t.Fatalf("secret in error %q", e)
		}
	}
	// The secret crossed in memory: not in apply's output, the state directory or the host root.
	if strings.Contains(f.out.String(), postSecret) {
		t.Fatal("secret in output")
	}
	for _, d := range []string{f.state, f.h.root} {
		filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
			if b, _ := os.ReadFile(p); err == nil && !e.IsDir() && bytes.Contains(b, []byte(postSecret)) {
				t.Errorf("client secret written to %s", p)
			}
			return nil
		})
	}
}

// AC6: KyPost joining after KyIdentity's first setup comes in a setup amendment, staged before
// kypost.setup, whose export reads it. KyPost has no SCIM connector and no secrets step.
func TestKyPostAddedByAmendment(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	identity := setUpIdentity(t, f, amendmentFeatures)
	postPlan(t, f, "      clients: []\n")
	id := amendmentID(parentSetup, "kypost")
	bundle := "/data/installer/" + id + "/setup.json"
	amendment := &fakeIdentity{parent: identity, export: postExport(id, postSecret)}
	identity.setups[bundle] = amendment
	post := &fakePost{files: map[string]string{}}

	s := runningSession(t, f, identity, &fakeRecovery{files: map[string]string{}})
	s.drivers["nas"] = sshDriver{t: s.placed["kypost"], c: f.h}
	f.h.exec = post.exec(t)
	prior, err := s.ensureInstallation(f.state)
	if err != nil {
		t.Fatal(err)
	}
	publish, err := s.amendLate(ctx, f.state, prior)
	if err != nil || publish == nil || len(s.amendments) != 1 || s.amendments[0].app != "kypost" || len(s.derived) != 0 {
		t.Fatalf("no amendment recorded: %v", err)
	}
	readOnly(t, identity.calls)
	if !strings.Contains(f.out.String(), "ran without kypost's client: adding it with setup amendment "+id) {
		t.Fatalf("output = %q", f.out.String())
	}
	want := `{"schema":"kyidentity.setup/1","setupId":"` + id + `","extends":"` + parentSetup + `","clients":[` + wantPostClient + `]}`
	if got := string(s.amendments[0].bundle); got != want {
		t.Fatalf("amendment bundle\n got %s\nwant %s", got, want)
	}
	all, err := s.deploySteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	order := ids(all)
	if slices.Contains(order, "kyidentity.scim.kypost") || slices.Contains(order, "kypost.secrets") {
		t.Fatalf("steps = %v", order)
	}
	steps := setupStepsOf(t, s)
	if got := strings.Join(ids(steps), " "); got != "kyidentity.setup kyidentity.amend.kypost kyidentity.retire-bootstrap kypost.setup" {
		t.Fatalf("steps = %s", got)
	}
	var out bytes.Buffer
	e := &engine.Engine{Dir: t.TempDir(), Out: &out, Redact: &engine.Redactor{}}
	if err := e.Run(ctx, steps); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if amendment.intent != want || !post.applied || post.files[path.Dir(postBundlePath)+"/client.secret"] != postSecret ||
		!slices.Contains(amendment.calls, "/usr/local/bin/kyidentity apply-setup --file "+bundle+" --export-client kypost") {
		t.Fatalf("staged %s, KyPost applied %v, export calls %q", amendment.intent, post.applied, amendment.calls)
	}
	out.Reset()
	if err := e.Run(ctx, steps); err != nil || strings.Count(out.String(), "skipped") != 4 {
		t.Fatalf("rerun: %v\n%s", err, out.String())
	}
	if err := publish(); err != nil || f.stack(t).Integrations[1].IdentityAmendment != id {
		t.Fatalf("amendment not recorded: %v", err)
	}
}

// AC8: KyPost's setup follows its health and precedes every edge step, as kyPulse's.
func TestKyPostSetupBeforeEdges(t *testing.T) {
	if slices.Contains(routedFirst, "kypost") {
		t.Fatal("kypost in routedFirst")
	}
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	postPlan(t, f, "      clients: []\n")
	s := postSession(t, f, &fakeIdentity{}, &fakePost{files: map[string]string{}})
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
	setup := slices.Index(order, "kypost.setup")
	firstEdge := slices.IndexFunc(order, func(id string) bool { return strings.HasPrefix(id, "k1.edge.") })
	route := slices.IndexFunc(list, func(st engine.Step) bool { return edge.RouteApp(st) == "kypost" })
	if setup < 1 || order[setup-1] != "kypost.health" || firstEdge < 0 || setup > firstEdge || route < firstEdge {
		t.Fatalf("steps = %v", order)
	}
}

// KyCalendar's and KyDrive's bundles and step inputs are byte for byte those of the code
// before KyPost (golden values from origin/master eca04d6).
func TestBundlesUnchangedByKyPost(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	drivePlan(t, f, "192.0.2.1")
	drive := &fakeDrive{files: map[string]string{}}
	_, steps := driveSteps(t, f, (&fakeIdentity{export: driveExport(driveSecret)}).stream, drive)
	identity := steps["kyidentity.setup"]
	if err := identity.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := steps["kydrive.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	const wantDrive = `{"admins":[{"issuer":"https://identity.example.com","subject":"5e0c8a52-6d1b-4f7e-9a3c-2b1d0e9f8a7c","username":"owner-admin"}],"schema":"ky.product-setup/1","service":"kydrive","setupId":"3c5a7e91-2b4d-4f6a-8c1e-9d0b2a4c6e8f","sso":{"clientId":"kydrive","clientSecretFile":"/data/installer/3c5a7e91-2b4d-4f6a-8c1e-9d0b2a4c6e8f/client.secret","issuerUrl":"https://identity.example.com","redirectUri":"https://drive.example.com/api/sso/kysignon/callback","scimTokenFile":"/run/secrets/scim_token"}}`
	if drive.bundle != wantDrive {
		t.Errorf("KyDrive bundle = %s", drive.bundle)
	}
	if got := steps["kydrive.setup"].InputHash(); got != "7e630bfa3ad979351574324a51fb0fe8be44972150a24e595e3ed12e5a3689de" {
		t.Errorf("kydrive.setup input hash = %s", got)
	}

	f = newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	calendarPlan(t, f, "      clients: []\n")
	cid := &fakeIdentity{exports: map[string]string{"kycalendar": calendarExport(parentSetup)}}
	cal := &fakeCalendar{files: map[string]string{}}
	csteps := stepsByID(t, calendarSession(t, f, cid, cal, false))
	if err := csteps["kyidentity.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	cid.checkReport = firstSetupCheck(ownerReused)
	if err := csteps["kycalendar.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	const wantCalendar = `{"admins":[{"issuer":"https://identity.example.com","subject":"5e0c8a52-6d1b-4f7e-9a3c-2b1d0e9f8a7c","username":"owner-admin"}],"everyday":[{"issuer":"https://identity.example.com","subject":"9b1d4e7a-2c3f-4a5b-8d6e-7f8091a2b3c4","username":"owner"}],"schema":"ky.product-setup/1","service":"kycalendar","setupId":"3c8f1b2a-7d4e-4f5a-9b6c-0d1e2f3a4b5c","sso":{"clientId":"kycalendar","clientSecretFile":"/app/data/installer/3c8f1b2a-7d4e-4f5a-9b6c-0d1e2f3a4b5c/client.secret","directorySecretFile":"/app/data/installer/3c8f1b2a-7d4e-4f5a-9b6c-0d1e2f3a4b5c/directory.secret","issuerUrl":"https://identity.example.com","redirectUri":"https://calendar.example.com/api/sso/kysignon/callback"}}`
	if cal.files[calendarBundle] != wantCalendar {
		t.Errorf("KyCalendar bundle = %s", cal.files[calendarBundle])
	}
	if got := csteps["kycalendar.setup"].InputHash(); got != "226358e905fa7132cad9ce5f50744b56ef19869844bc826f6e661ee5d73ba89f" {
		t.Errorf("kycalendar.setup input hash = %s", got)
	}
}
