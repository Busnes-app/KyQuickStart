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
	"github.com/Busnes-app/kyquickstart/internal/suite"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	yardSetupID = "8d4f2a6c-3b1e-4c7d-9a5f-6e0b1c2d3e4f"
	yardBundle  = "/data/installer/" + yardSetupID + "/setup.json"
	yardRunner  = "ghcr.io/busnes-app/kyquickstart-runner:0.5.0@sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// yardPlan adds the catalog KyYard, routed at yard.example.com, to the catalog KyIdentity plan.
func yardPlanFixture(t *testing.T, f *fixture, clients string) {
	catalogIdentityPlan(t, f, "kyidentity", "  - product: kyyard\n    target: k1\n    bundle: "+yardBundle+"\n    identity_client: kyyard\n")
	p := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(p)
	y := strings.Replace(string(b), "    target: k1\nedges:", "    target: k1\n  - name: kyyard\n    target: k1\nedges:", 1)
	y = strings.Replace(y, "integrations:", "      - app: kyyard\n        hostname: yard.example.com\n        forward_host: app.kyq-kyyard.svc.cluster.local\n        forward_port: 9273\n        certificate_id: 1\nintegrations:", 1)
	y = strings.Replace(y, "      clients: []\n", clients, 1)
	os.WriteFile(p, []byte(y), 0o600)
}

func yardFixture(t *testing.T) *fixture {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog, f.o.RunnerImage = catalog.Embedded(), yardRunner
	return f
}

const wantYardClient = `{"id":"kyyard","name":"KyYard","redirectUris":["https://yard.example.com/api/sso/kyidentity/callback"],"postLogoutUris":null,"administratorRole":"kyyard.admin","suiteWebhook":{"url":"https://yard.example.com/api/installer/directory"}}`

func yardExport(setupID string) string {
	return `{"schema":"kyidentity.client-credentials/1","setupId":"` + setupID + `","issuerUrl":"https://identity.example.com","clientId":"kyyard",` +
		`"redirectUris":["https://yard.example.com/api/sso/kyidentity/callback"],"directoryUrl":"https://yard.example.com/api/installer/directory",` +
		`"clientSecret":"` + clientSecret + `","directorySecret":"` + directorySecret + `",` +
		`"administrator":{"subject":"` + adminSubject + `","username":"owner-admin","role":"kyyard.admin"}}`
}

// AC5, AC13: one apply installing KyIdentity and KyYard puts KyYard's admin-only client in the
// first setup, and the handover names the break-glass account and the handoff line.
func TestKyYardClientFromCatalog(t *testing.T) {
	f := yardFixture(t)
	yardPlanFixture(t, f, "      clients: []\n")
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(s.integrations[0].Identity.Clients); string(b) != "["+wantYardClient+"]" || len(s.amendments) != 0 || !slices.Equal(s.derived, []string{"kyyard"}) {
		t.Fatalf("derived clients = %s, amendments %d", b, len(s.amendments))
	}
	kc := s.placed["kyyard"].Kubeconfig
	state, _ := filepath.Abs(f.state)
	for _, want := range []string{
		"KyYard at https://yard.example.com/: sign in through KyIdentity as owner-admin (administration only)",
		"Everyday\n  accounts are not given KyYard access",
		"kubectl --kubeconfig '" + kc + "' -n kyq-kyyard exec deployment/app -c app -- cat /data/initial-admin-password",
		"change it. Only after that, delete the file:",
		"kyquickstart handoff --state '" + state + "' --kyyard-target k1 --runner-pod kyq-kyyard/app.kubernetes.io/name=kyyard\n",
		"workstation apply is refused until kyquickstart handoff --revoke",
		"handoff --revoke, apply, then handoff again",
		"KyYard 0.4.2 has no Suite page yet",
		"pair it with KyRecovery",
	} {
		if !strings.Contains(s.handover, want) {
			t.Errorf("handover lacks %q:\n%s", want, s.handover)
		}
	}
	if strings.Contains(s.handover, "owner (") || strings.Contains(s.handover, " owner,") {
		t.Errorf("handover names the everyday account: %s", s.handover)
	}
	written := "      clients:\n        - id: kyyard\n          name: KyYard\n          redirect_uris: [https://yard.example.com/api/sso/kyidentity/callback]\n          administrator_role: kyyard.admin\n          suite_webhook: {url: https://yard.example.com/api/installer/directory}\n"
	yardPlanFixture(t, f, written)
	if s, err := load(f.state, nil, f.o); err != nil || len(s.integrations[0].Identity.Clients) != 1 {
		t.Fatalf("matching hand-written client: %v", err)
	}
	yardPlanFixture(t, f, strings.Replace(written, "          administrator_role: kyyard.admin\n", "          administrator_role: kyyard.admin\n          everyday_role: kyyard.user\n", 1))
	if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), "differs from the catalog's KyYard client") {
		t.Fatalf("hand-written client with an everyday role: %v", err)
	}
}

// fakeYard is KyYard's app container: its shell's private files and its apply-setup, which
// activates without a restart.
type fakeYard struct {
	files   map[string]string
	placed  []string // placeScript's mode and path, in order
	calls   []string // apply-setup arguments
	applied bool
}

func (c *fakeYard) stream(args []string, o remotecommand.StreamOptions) (string, error) {
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
	if args[0] != "/app/kyyard-server" {
		return "", errors.New("unexpected binary " + args[0])
	}
	c.calls = append(c.calls, strings.Join(args[1:], " "))
	if slices.Contains(args, "--capabilities") {
		return `{"schema":"ky.setup-capabilities/1","service":"kyyard","bundleSchema":"ky.product-setup/1","features":["kyidentity-oidc","directory-webhook","dedicated-admin","check"]}`, nil
	}
	check := slices.Contains(args, "--check")
	if c.files[args[slices.Index(args, "--file")+1]] == "" || check && !c.applied {
		return "", errors.New("not applied")
	}
	created, present := `[]`, `[{"kind":"identity","name":"owner-admin"}]`
	if !check && !c.applied {
		c.applied, created, present = true, present, `[]`
	}
	return `{"schema":"ky.product-setup-report/1","service":"kyyard","setupId":"` + yardSetupID + `","created":` + created + `,"present":` + present + `,"pending":[]}`, nil
}

// yardSession loads the plan over ready KyIdentity and KyYard pods; running adds KyIdentity's
// Deployment, as an apply after its first setup sees it.
func yardSession(t *testing.T, f *fixture, identity *fakeIdentity, yard *fakeYard, running bool) *session {
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
	for _, app := range []string{"kyidentity", "kyyard"} {
		f.cs.CoreV1().Pods("kyq-"+app).Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "app-1", Labels: map[string]string{"app.kubernetes.io/name": app, managed.Label: managed.Value}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
		}, metav1.CreateOptions{})
	}
	stream := func(ctx context.Context, namespace, pod, container string, args []string, o remotecommand.StreamOptions) error {
		if namespace == "kyq-kyidentity" {
			return identity.stream(ctx, namespace, pod, container, args, o)
		}
		if container != "app" {
			return errors.New("exec into " + container)
		}
		out, err := yard.stream(args, o)
		if err == nil {
			_, err = io.WriteString(o.Stdout, out)
		}
		return err
	}
	s.drivers["k1"] = kubeDriver{c: kube.Fake(f.cs, "https://k1.test", stream), runnerImage: s.o.RunnerImage}
	s.edges = nil
	return s
}

// AC7: kyyard.setup exports the client, places both secrets and then the bundle, applies and
// re-exports. Verify only checks; a changed export is refused and no secret is ever shown.
func TestKyYardSetup(t *testing.T) {
	ctx := context.Background()
	f := yardFixture(t)
	yardPlanFixture(t, f, "      clients: []\n")
	identity := &fakeIdentity{exports: map[string]string{"kyyard": yardExport(parentSetup)}}
	yard := &fakeYard{files: map[string]string{}}
	steps := stepsByID(t, yardSession(t, f, identity, yard, false))
	if err := steps["kyidentity.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	setup := steps["kyyard.setup"]
	if done, _ := setup.Inspect(ctx); done || len(yard.files) != 0 {
		t.Fatal("fresh KyYard inspected as done, or inspection wrote")
	}
	identity.calls, yard.calls, yard.placed = nil, nil, nil
	if err := setup.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	export := "/usr/local/bin/kyidentity apply-setup --file /data/installer/" + parentSetup + "/setup.json --export-client kyyard"
	calls := slices.DeleteFunc(slices.Clone(identity.calls), func(c string) bool { return strings.Contains(c, "--capabilities") })
	if !slices.Equal(calls, []string{export, export}) {
		t.Fatalf("KyIdentity calls %q", identity.calls)
	}
	dir := path.Dir(yardBundle)
	if !slices.Equal(yard.placed, []string{"write " + dir + "/client.secret", "write " + dir + "/directory.secret", "write " + yardBundle}) {
		t.Fatalf("placed %q", yard.placed)
	}
	if !slices.Equal(yard.calls, []string{"apply-setup --capabilities", "apply-setup --file " + yardBundle}) {
		t.Fatalf("KyYard calls %q", yard.calls)
	}
	want := `{"admins":[{"issuer":"https://identity.example.com","subject":"` + adminSubject + `","username":"owner-admin"}],` +
		`"schema":"ky.product-setup/1","service":"kyyard","setupId":"` + yardSetupID + `",` +
		`"sso":{"clientId":"kyyard","clientSecretFile":"` + dir + `/client.secret","directorySecretFile":"` + dir + `/directory.secret","issuerUrl":"https://identity.example.com","redirectUri":"https://yard.example.com/api/sso/kyidentity/callback"}}`
	if yard.files[yardBundle] != want {
		t.Fatalf("bundle\n got %s\nwant %s", yard.files[yardBundle], want)
	}
	if yard.files[dir+"/client.secret"] != clientSecret || yard.files[dir+"/directory.secret"] != directorySecret {
		t.Fatal("secrets misplaced")
	}

	// Verify: placements only compare, every apply-setup is a check; nothing is written.
	identity.calls, yard.calls, yard.placed = nil, nil, nil
	before := snapshot(yard.files)
	if err := setup.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range yard.placed {
		if !strings.HasPrefix(p, "check ") {
			t.Errorf("Verify placed %q", p)
		}
	}
	readOnly(t, yard.calls)
	if len(yard.calls) != 2 || slices.ContainsFunc(identity.calls, func(c string) bool { return c != export && !strings.Contains(c, "--capabilities") }) {
		t.Fatalf("Verify calls %q %q", yard.calls, identity.calls)
	}
	if snapshot(yard.files) != before {
		t.Fatal("Verify wrote")
	}

	// A changed export on rerun is refused: retained secrets are compared, never replaced.
	identity.exports["kyyard"] = strings.Replace(yardExport(parentSetup), clientSecret, "Q2hhbmdlZENoYW5nZWRDaGFuZ2VkQ2hhbmdlZENoYW4", 1)
	err := setup.Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "KyYard's client.secret is missing, unsafe or differs") || snapshot(yard.files) != before {
		t.Fatalf("changed export: %v", err)
	}
	for _, s := range []string{clientSecret, directorySecret, "Q2hhbmdlZENoYW5nZWRDaGFuZ2VkQ2hhbmdlZENoYW4"} {
		if strings.Contains(err.Error(), s) || strings.Contains(f.out.String(), s) {
			t.Fatal("secret in error or output")
		}
	}
}

// AC6: KyYard joining after KyIdentity's first setup comes in a setup amendment, applied before
// kyyard.setup; KyYard has no SCIM step.
func TestKyYardAddedByAmendment(t *testing.T) {
	ctx := context.Background()
	f := yardFixture(t)
	identity := setUpIdentity(t, f, amendmentFeatures)
	yardPlanFixture(t, f, "      clients: []\n")
	id := amendmentID(parentSetup, "kyyard")
	amendment := &fakeIdentity{parent: identity, export: yardExport(id)}
	identity.setups["/data/installer/"+id+"/setup.json"] = amendment
	yard := &fakeYard{files: map[string]string{}}

	s := yardSession(t, f, identity, yard, true)
	prior, err := s.ensureInstallation(f.state)
	if err != nil {
		t.Fatal(err)
	}
	publish, err := s.amendLate(ctx, f.state, prior)
	if err != nil || publish == nil || len(s.amendments) != 1 || len(s.derived) != 0 {
		t.Fatalf("no amendment recorded: %v", err)
	}
	readOnly(t, identity.calls)
	if !strings.Contains(f.out.String(), "ran without kyyard's client: adding it with setup amendment "+id) {
		t.Fatalf("output = %q", f.out.String())
	}
	want := `{"schema":"kyidentity.setup/1","setupId":"` + id + `","extends":"` + parentSetup + `","clients":[` + wantYardClient + `]}`
	if got := string(s.amendments[0].bundle); got != want {
		t.Fatalf("amendment bundle\n got %s\nwant %s", got, want)
	}
	all, err := s.deploySteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(all, func(st engine.Step) bool { return strings.HasPrefix(st.ID(), "kyidentity.scim.") }) {
		t.Fatalf("SCIM step for KyYard: %v", ids(all))
	}
	steps := setupStepsOf(t, s)
	if got := strings.Join(ids(steps), " "); got != "kyidentity.setup kyidentity.amend.kyyard kyidentity.retire-bootstrap kyyard.setup" {
		t.Fatalf("steps = %s", got)
	}
	var out bytes.Buffer
	e := &engine.Engine{Dir: t.TempDir(), Out: &out, Redact: &engine.Redactor{}}
	if err := e.Run(ctx, steps); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if amendment.intent != want || !yard.applied {
		t.Fatalf("staged %s, KyYard applied %v", amendment.intent, yard.applied)
	}
	out.Reset()
	if err := e.Run(ctx, steps); err != nil || strings.Count(out.String(), "skipped") != 4 {
		t.Fatalf("rerun: %v\n%s", err, out.String())
	}
	if err := publish(); err != nil || f.stack(t).Integrations[1].IdentityAmendment != id {
		t.Fatalf("amendment not recorded: %v", err)
	}
}

// yardSteps are deploySteps with an NPM edge that is never dialled.
func yardSteps(t *testing.T, s *session) []engine.Step {
	t.Helper()
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
	return list
}

// AC8: under the runner, KyYard's workload is one note step and nothing of kyq-kyyard's
// Deployment is read or written; requests naming KyYard are refused. Under the CLI the steps
// are the usual ones, setup between health and the first edge step.
func TestKyYardRunnerSelfExclusion(t *testing.T) {
	ctx := context.Background()
	f := yardFixture(t)
	yardPlanFixture(t, f, "      clients: []\n")

	order := ids(yardSteps(t, yardSession(t, f, &fakeIdentity{}, &fakeYard{files: map[string]string{}}, false)))
	setup := slices.Index(order, "kyyard.setup")
	firstEdge := slices.IndexFunc(order, func(id string) bool { return strings.HasPrefix(id, "k1.edge.") })
	if slices.Contains(routedFirst, "kyyard") || setup < 1 || order[setup-1] != "kyyard.health" || order[setup-2] != "kyyard.deploy" || firstEdge < 0 || setup > firstEdge || slices.Contains(order, "kyyard.runner") {
		t.Fatalf("CLI steps = %v", order)
	}

	f.o.RunnerID, f.o.RunnerImage = "9b6c1f3e-2a1d-4f6e-9c3b-5d7e8f9a0b1c", ""
	s := yardSession(t, f, &fakeIdentity{}, &fakeYard{files: map[string]string{}}, false)
	f.cs.ClearActions()
	list := yardSteps(t, s)
	order = ids(list)
	if slices.ContainsFunc(order, func(id string) bool { return id == "kyyard.deploy" || id == "kyyard.health" || id == "kyyard.setup" }) || !slices.Contains(order, "kyidentity.deploy") {
		t.Fatalf("runner steps = %v", order)
	}
	note := list[slices.Index(order, "kyyard.runner")].(noteStep)
	if note.note != runnerSelfNote {
		t.Fatalf("note = %q", note.note)
	}
	// An upgrade of every app runs these steps: the note is recorded, KyYard untouched.
	var out bytes.Buffer
	e := &engine.Engine{Dir: t.TempDir(), Out: &out, Redact: &engine.Redactor{}}
	if err := e.Run(ctx, []engine.Step{note}); err != nil || !strings.Contains(out.String(), "kyyard.runner") {
		t.Fatalf("note step: %v %s", err, out.String())
	}
	for _, a := range f.cs.Actions() {
		if a.GetNamespace() == "kyq-kyyard" {
			t.Errorf("runner touched kyq-kyyard: %s %s", a.GetVerb(), a.GetResource().Resource)
		}
	}

	// Requests naming KyYard are refused before any connection.
	dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
		t.Fatal("connected")
		return nil, nil
	}
	for kind, call := range map[string]func() error{
		"upgrade":   func() error { return Upgrade(ctx, f.state, Request{Apps: []string{"kyyard"}}, f.o) },
		"uninstall": func() error { return Uninstall(ctx, f.state, Request{Apps: []string{"kyyard"}}, f.o) },
		"unmanage":  func() error { return Unmanage(ctx, f.state, Request{Apps: []string{"kyyard"}}, f.o) },
	} {
		if err := call(); code(err) != suite.RunnerHost || !strings.Contains(err.Error(), "handoff --revoke, apply, then handoff") {
			t.Errorf("%s kyyard: %v", kind, err)
		}
	}
	catalogIdentityPlan(t, f, "kyidentity", "")
	if err := Install(ctx, f.state, Request{Apps: []string{"kyyard"}, Target: "k1"}, f.o); code(err) != suite.RunnerHost {
		t.Errorf("install kyyard: %v", err)
	}
}

// AC14: a workstation apply or upgrade that would redeploy KyYard is refused while the runner
// owns the plan; status, handoff and commands without KyYard are not.
func TestKyYardWorkstationDeployRefusedUnderRunner(t *testing.T) {
	f := yardFixture(t)
	yardPlanFixture(t, f, "      clients: []\n")
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	runner := stack.Marker{Owner: "runner", RunnerID: "9b6c1f3e-2a1d-4f6e-9c3b-5d7e8f9a0b1c", InstallationID: s.installation}
	if err := s.guard("k1", runner); err != nil {
		t.Fatalf("status/handoff under the runner: %v", err)
	}
	s.deploys = true
	if err := s.guard("k1", runner); code(err) != suite.OwnedByRunner || !strings.Contains(err.Error(), "handoff --revoke, then this command, then handoff again") {
		t.Fatalf("apply under the runner: %v", err)
	}
	if err := s.guard("k1", stack.Marker{Owner: "cli", InstallationID: s.installation}); err != nil {
		t.Fatalf("apply under the CLI: %v", err)
	}
	s.order = slices.DeleteFunc(s.order, func(a string) bool { return a == "kyyard" })
	if err := s.guard("k1", runner); err != nil {
		t.Fatalf("upgrade without KyYard under the runner: %v", err)
	}
}

// AC4: plans KyYard's runner or setup could not take are refused before any connection.
func TestKyYardPlanRefusals(t *testing.T) {
	f := yardFixture(t)
	dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
		t.Fatal("connected")
		return nil, nil
	}
	write := func(edit func(string) string) {
		yardPlanFixture(t, f, "      clients: []\n")
		p := filepath.Join(f.state, "stack.yaml")
		b, _ := os.ReadFile(p)
		os.WriteFile(p, []byte(edit(string(b))), 0o600)
	}
	without := func(y string) string {
		y = strings.Replace(y, "  - name: kyyard\n    target: k1\n", "", 1)
		y = strings.Replace(y, "      - app: kyyard\n", "      - app: npm\n", 1)
		return strings.Replace(y, "  - product: kyyard\n    target: k1\n    bundle: "+yardBundle+"\n    identity_client: kyyard\n", "", 1)
	}
	admin := func(name string) func(string) string {
		return func(y string) string {
			return strings.Replace(y, "admin: {username: owner-admin,", "admin: {username: "+name+",", 1)
		}
	}
	cmds := [][]string{{"review"}, {"plan", "--noninteractive"}, {"apply"}}
	refuse := func(name, image string, edit func(string) string, wants ...string) {
		t.Helper()
		f.o.RunnerImage = image
		write(edit)
		for _, cmd := range cmds {
			err := f.run(cmd...)
			for _, want := range wants {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("%s, %s: %v, want %q", name, cmd[0], err, want)
				}
			}
		}
		write(func(y string) string { return edit(without(y)) })
		if err := f.run("review"); err != nil {
			t.Errorf("%s without kyyard: %v", name, err)
		}
	}
	same := func(y string) string { return y }
	refuse("no runner image", "", same, "KyYard's suite runner has no attested release image in this build")
	refuse("unpinned runner image", "ghcr.io/busnes-app/kyquickstart-runner:stable", same, `suite runner image "ghcr.io/busnes-app/kyquickstart-runner:stable" is not pinned by digest`)
	refuse("on Docker", yardRunner, func(y string) string {
		y = strings.Replace(y, "  - name: kyyard\n    target: k1\n", "  - name: kyyard\n    target: nas\n", 1)
		return strings.Replace(y, "  - product: kyyard\n    target: k1\n", "  - product: kyyard\n    target: nas\n", 1)
	}, "KyYard runs on Kubernetes only", "not nas")
	for _, name := range []string{"admin", "Admin", "Owner-Admin", "ab"} {
		refuse("admin "+name, yardRunner, admin(name), `KyYard cannot take the KyIdentity admin username "`+name+`"`, "3 to 64 lowercase letters", "identity.admin.username")
	}
	// The everyday account is not KyYard's.
	write(func(y string) string {
		return strings.Replace(y, "everyday: {username: owner,", "everyday: {username: Owner,", 1)
	})
	if err := f.run("review"); err != nil {
		t.Errorf("everyday username refused: %v", err)
	}
}

// AC7: KyCalendar's and KyDrive's bundles and step inputs are byte for byte those of
// origin/master (eca04d6, and 72a0cd4); kyPulse's and KyRecovery's are TestBundlesWithoutEveryday's.
func TestBundlesUnchangedByKyYard(t *testing.T) {
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
	if err := steps["kycalendar.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	const wantCalendar = `{"admins":[{"issuer":"https://identity.example.com","subject":"5e0c8a52-6d1b-4f7e-9a3c-2b1d0e9f8a7c","username":"owner-admin"}],"everyday":[{"issuer":"https://identity.example.com","subject":"9b1d4e7a-2c3f-4a5b-8d6e-7f8091a2b3c4","username":"owner"}],"schema":"ky.product-setup/1","service":"kycalendar","setupId":"3c8f1b2a-7d4e-4f5a-9b6c-0d1e2f3a4b5c","sso":{"clientId":"kycalendar","clientSecretFile":"/app/data/installer/3c8f1b2a-7d4e-4f5a-9b6c-0d1e2f3a4b5c/client.secret","directorySecretFile":"/app/data/installer/3c8f1b2a-7d4e-4f5a-9b6c-0d1e2f3a4b5c/directory.secret","issuerUrl":"https://identity.example.com","redirectUri":"https://calendar.example.com/api/sso/kysignon/callback"}}`
	if cal.files[calendarBundle] != wantCalendar || steps["kycalendar.setup"].InputHash() != "226358e905fa7132cad9ce5f50744b56ef19869844bc826f6e661ee5d73ba89f" {
		t.Errorf("KyCalendar bundle %s, input %s", cal.files[calendarBundle], steps["kycalendar.setup"].InputHash())
	}

	g := newFixture(t, "../catalog/testdata/apps")
	g.o.Catalog = catalog.Embedded()
	drivePlan(t, g, "192.0.2.1")
	di := &fakeIdentity{export: driveExport(driveSecret)}
	drive := &fakeDrive{files: map[string]string{}}
	_, dsteps := driveSteps(t, g, di.stream, drive)
	if err := dsteps["kyidentity.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dsteps["kydrive.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	const wantDrive = `{"admins":[{"issuer":"https://identity.example.com","subject":"5e0c8a52-6d1b-4f7e-9a3c-2b1d0e9f8a7c","username":"owner-admin"}],"schema":"ky.product-setup/1","service":"kydrive","setupId":"3c5a7e91-2b4d-4f6a-8c1e-9d0b2a4c6e8f","sso":{"clientId":"kydrive","clientSecretFile":"/data/installer/3c5a7e91-2b4d-4f6a-8c1e-9d0b2a4c6e8f/client.secret","issuerUrl":"https://identity.example.com","redirectUri":"https://drive.example.com/api/sso/kysignon/callback","scimTokenFile":"/run/secrets/scim_token"}}`
	if drive.bundle != wantDrive || dsteps["kydrive.setup"].InputHash() != "7e630bfa3ad979351574324a51fb0fe8be44972150a24e595e3ed12e5a3689de" {
		t.Errorf("KyDrive bundle %s, input %s", drive.bundle, dsteps["kydrive.setup"].InputHash())
	}
}
