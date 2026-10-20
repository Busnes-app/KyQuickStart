package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/catalog/catalogtest"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	immichSetupID    = "5b2e8c41-9a7d-4e3f-b1c6-2d8e0f4a6b9c"
	immichBundlePath = "/kyq-setup/installer/" + immichSetupID + "/setup.json"
	immichKey        = "U2VydmljZUtleVNlcnZpY2VLZXlTZXJ2aWNlS2V5U2U"
	immichSCIMToken  = "U0NJTVRva2VuU0NJTVRva2VuU0NJTVRva2VuU0NJTVQ"
)

const wantImmichClient = `{"id":"immich","name":"Immich","redirectUris":["https://photos.example.com/auth/login","https://photos.example.com/api/oauth/mobile-redirect"],"postLogoutUris":null,"administratorRole":"admin","everydayRole":"user"}`

// immichPlan installs NPM and KyIdentity on cluster k1 and Immich on Docker host nas, both
// routed through k1's lan edge; prep is KyIdentity's identity settings.
func immichCatalogPlan(t *testing.T, f *fixture, prep string) {
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	f.plan(t, "  - name: k1\n    kubeconfig: "+kc+"\napps:\n  - name: npm\n    target: k1\n  - name: kyidentity\n    target: k1\n  - name: immich\n    target: nas\n"+
		"edges:\n  - target: k1\n    provider: lan\n    npm_url: kube://kyq-npm/app:81\n    allow_cidrs: [192.168.1.0/24]\n    routes:\n"+
		"      - app: kyidentity\n        hostname: identity.example.com\n        forward_host: app.kyq-kyidentity.svc.cluster.local\n        forward_port: 5867\n        certificate_id: 1\n"+
		"      - app: immich\n        hostname: photos.example.com\n        forward_host: 192.0.2.1\n        forward_port: 18443\n        certificate_id: 1\n"+
		"integrations:\n  - product: kyidentity\n    target: k1\n    bundle: /data/installer/"+parentSetup+"/setup.json\n"+prep+
		"  - product: immich\n    target: nas\n    bundle: "+immichBundlePath+"\n    identity_client: immich\n")
}

func immichFixture(t *testing.T) *fixture {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalogtest.FS()
	immichCatalogPlan(t, f, identityPrep)
	return f
}

func immichExport(secret string) string {
	return `{"schema":"kyidentity.client-credentials/1","setupId":"` + parentSetup + `","issuerUrl":"https://identity.example.com","clientId":"immich",` +
		`"redirectUris":["https://photos.example.com/auth/login","https://photos.example.com/api/oauth/mobile-redirect"],"clientSecret":"` + secret + `",` +
		`"administrator":{"subject":"` + adminSubject + `","username":"owner-admin","role":"admin"}}`
}

// fakeImmich is the server container behind docker exec: its shell's files and immich-admin's
// apply-setup, which answers like the module.
type fakeImmich struct {
	files   map[string]string
	calls   []string // immich-admin arguments
	placed  []string // placeScript mode and path
	applied bool
	stock   bool              // no module: apply-setup does not exist
	caps    string            // overrides the capabilities report
	created string            // overrides the first apply's nativeUsers extra entries
	later   string            // extra nativeUsers once applied
	owner   string            // overrides the owner's native UUID in every report
	probe   map[string]string // container path -> a host path probeScript inspects in its place
}

func (m *fakeImmich) admin(args []string) ([]byte, error) {
	m.calls = append(m.calls, strings.Join(args, " "))
	switch {
	case m.stock:
		return nil, errors.New("unknown command apply-setup")
	case slices.Contains(args, "--capabilities"):
		if m.caps != "" {
			return []byte(m.caps), nil
		}
		return []byte(immichCaps), nil
	}
	check := args[len(args)-1] == "--check"
	var b immichBundle
	if json.Unmarshal([]byte(m.files[args[slices.Index(args, "--file")+1]]), &b) != nil || check && !m.applied {
		return nil, errors.New("refused")
	}
	created, extra := `[]`, m.later
	if !check && !m.applied {
		m.applied, created, extra = true, `["provider","administrator","everyday","service"]`, m.created
	}
	owner := immichOwnerID
	if m.owner != "" {
		owner = m.owner
	}
	r := `{"schema":"immich.setup-report/1","setupId":"` + b.SetupID + `","check":` + map[bool]string{true: "true", false: "false"}[check] + `,"created":` + created + `,"planned":[],` +
		`"accounts":[{"id":"` + owner + `","subject":"` + b.Administrator.Subject + `","administrator":true},{"id":"` + immichEverydayID + `","subject":"` + b.Everyday.Subject + `","administrator":false}],` +
		`"service":{"id":"` + immichServiceID + `"},"nativeUsers":` + strings.Replace(immichUsers(extra), immichOwnerID, owner, 1) + `}`
	return []byte(r), nil
}

// exec answers docker exec into Immich's server, found by its Compose service.
func (m *fakeImmich) exec(t *testing.T) func(string, []byte) ([]byte, error) {
	return func(cmd string, stdin []byte) ([]byte, error) {
		rest, ok := strings.CutPrefix(cmd, dockerAppContainer("immich"))
		if !ok || !strings.Contains(dockerAppContainer("immich"), "'label=com.docker.compose.service=immich-server'") {
			t.Fatalf("exec without finding Immich's server: %s", cmd)
		}
		rest, ok = strings.CutPrefix(strings.TrimPrefix(rest, "docker exec -i "), `"$c" `)
		if !ok {
			rest, ok = strings.CutPrefix(strings.TrimPrefix(rest, "docker exec "), `"$c" `)
		}
		if !ok {
			t.Fatalf("exec into another container: %s", cmd)
		}
		out, err := remote.Local{}.Run(context.Background(), `printf '%s\0' `+rest, nil)
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
		switch args[0] {
		case "sh":
			if args[1] == "-c" && args[2] == probeScript {
				return remote.Local{}.Run(context.Background(), "sh -c "+remote.Quote(probeScript)+" sh "+remote.Quote(m.probe[args[4]]), nil)
			}
			if args[1] != "-c" || args[2] != placeScript {
				return nil, errors.New("unexpected script")
			}
			m.placed = append(m.placed, args[4]+" "+args[5])
			return nil, fakePlace(m.files, args[4], args[5], stdin)
		case "/usr/src/app/server/bin/immich-admin":
			return m.admin(args[1:])
		}
		return nil, errors.New("unexpected binary")
	}
}

// immichSession loads the plan over KyIdentity's ready pod and Immich's fake server.
func immichSession(t *testing.T, f *fixture, identity *fakeIdentity, m *fakeImmich) *session {
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
	s.drivers["nas"] = sshDriver{t: s.placed["immich"], c: f.h}
	f.h.exec = m.exec(t)
	return s
}

// AC6, AC9: Immich's client, its Compose route with the bridge beside it, the edge's
// bridge path and egress, and apply's steps: setup and bridge after health, both before the
// edges, the connector last, targeting the bridge with the bridge's token.
func TestImmichClientStepsAndConnector(t *testing.T) {
	ctx := context.Background()
	f := immichFixture(t)
	s := immichSession(t, f, &fakeIdentity{}, &fakeImmich{files: map[string]string{}})
	if b, _ := json.Marshal(s.integrations[0].Identity.Clients); string(b) != "["+wantImmichClient+"]" {
		t.Fatalf("derived clients = %s", b)
	}
	c := string(s.cat["immich"].Compose)
	if !strings.Contains(c, `ports: ["192.0.2.1:18443:2283"]`) || !strings.Contains(c, `ports: ["192.0.2.1:18444:8443"]`) || s.cat["immich"].UsesRoute() {
		t.Fatalf("compose:\n%s", c)
	}
	if u := s.upstreams("k1"); len(u) != 2 || u[0].String() != "192.0.2.1:18443" || u[1].String() != "192.0.2.1:18444" {
		t.Fatalf("upstreams = %v", u)
	}
	if len(s.bridges) != 1 || s.bridges["immich"] != "/scim/immich" {
		t.Fatalf("bridges = %v", s.bridges)
	}
	var err error
	if s.npms["k1"], err = edge.NewNPM("kube://kyq-npm/app:81", "token", "test-installation", func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("no NPM in this test")
	}); err != nil {
		t.Fatal(err)
	}
	list, err := s.deploySteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	order := ids(list)
	at := func(id string) int { return slices.Index(order, id) }
	firstEdge := slices.IndexFunc(order, func(id string) bool { return strings.HasPrefix(id, "k1.edge.") })
	if at("immich.secrets") < 0 || at("immich.deploy") != at("immich.secrets")+1 || at("immich.health") != at("immich.deploy")+1 ||
		at("immich.setup") != at("immich.health")+1 || at("immich.directory") != at("immich.setup")+1 || firstEdge < at("immich.directory") ||
		order[len(order)-1] != "kyidentity.scim.immich" || at("kyidentity.setup") > at("immich.setup") {
		t.Fatalf("steps = %v", order)
	}
	scim := list[len(list)-1].(scimStep)
	var pass struct {
		Clients []struct {
			ID   string
			SCIM stack.IdentitySCIM
		}
	}
	if json.Unmarshal(scim.bundle, &pass) != nil || len(pass.Clients) != 1 || pass.Clients[0].ID != "immich" ||
		pass.Clients[0].SCIM != (stack.IdentitySCIM{URL: "https://photos.example.com/scim/immich", TokenFile: "/data/installer/scim-immich.token"}) {
		t.Fatalf("connector pass = %s", scim.bundle)
	}
	// The connector's token is the bridge's, read on Immich's target.
	secrets := filepath.Join(f.h.root, "immich/secrets")
	os.MkdirAll(secrets, 0o700)
	os.WriteFile(filepath.Join(secrets, "scim_token"), []byte(immichSCIMToken), 0o600)
	if tok, err := scim.token(ctx); err != nil || string(tok) != immichSCIMToken {
		t.Fatalf("connector token %q, %v", tok, err)
	}
	if !strings.Contains(s.handover, immichHandover("photos.example.com", "owner-admin", "owner", "192.0.2.1", 18443, "", s.cat["immich"].Images["server"])) {
		t.Fatalf("handover = %s", s.handover)
	}
}

// AC6: an Immich added after KyIdentity's first setup ran comes in a setup amendment.
func TestImmichAddedByAmendment(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalogtest.FS()
	identity := setUpIdentity(t, f, amendmentFeatures)
	immichCatalogPlan(t, f, identityPrep)
	s := runningSession(t, f, identity, &fakeRecovery{files: map[string]string{}})
	s.drivers["nas"] = sshDriver{t: s.placed["immich"], c: f.h}
	f.h.exec = (&fakeImmich{files: map[string]string{}}).exec(t)
	if _, err := s.amendLate(ctx, f.state, nil); err != nil || len(s.amendments) != 1 || s.amendments[0].app != "immich" {
		t.Fatalf("no amendment recorded: %v", err)
	}
	got := strings.Join(ids(setupStepsOf(t, s)), " ")
	if got != "kyidentity.setup kyidentity.amend.immich kyidentity.retire-bootstrap immich.setup" {
		t.Fatalf("steps = %s", got)
	}
	if b, _ := json.Marshal(s.amendments[0].source.Identity.Clients); string(b) != "["+wantImmichClient+"]" {
		t.Fatalf("amendment clients = %s", b)
	}
}

// wantImmichBundle is the module's bundle for the fixture: rolesClaim exactly roles.
const wantImmichBundle = `{"schema":"immich.setup/1","setupId":"` + immichSetupID + `","provider":{"issuer":"https://identity.example.com","clientId":"immich","clientSecret":"` + clientSecret + `",` +
	`"rolesClaim":"roles","mobileRedirectUri":"https://photos.example.com/api/oauth/mobile-redirect"},"serviceKeyFile":"/kyq-setup/service.key",` +
	`"administrator":{"subject":"` + adminSubject + `","email":"admin@example.test","name":"owner-admin"},"everyday":{"subject":"` + ownerSubject + `","email":"owner@example.test","name":"owner"}}`

func immichSetupSteps(t *testing.T, f *fixture, identity *fakeIdentity, m *fakeImmich) map[string]engine.Step {
	t.Helper()
	s := immichSession(t, f, identity, m)
	s.edges = nil
	steps := stepsByID(t, s)
	if err := steps["kyidentity.setup"].Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	identity.checkReport = firstSetupCheck(ownerReused)
	return steps
}

// AC7: immich.setup checks both contracts, places the exact bundle, applies, checks, and
// proves the three setup accounts; Verify only reads; nothing secret is printed.
func TestImmichSetup(t *testing.T) {
	ctx := context.Background()
	f := immichFixture(t)
	identity := &fakeIdentity{export: immichExport(clientSecret)}
	m := &fakeImmich{files: map[string]string{}}
	setup := immichSetupSteps(t, f, identity, m)["immich.setup"]
	if done, _ := setup.Inspect(ctx); done || len(m.files) != 0 {
		t.Fatal("fresh Immich inspected as done, or inspection wrote")
	}
	m.calls, m.placed, identity.calls = nil, nil, nil
	if err := setup.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.calls, []string{"apply-setup --capabilities", "apply-setup --file " + immichBundlePath, "apply-setup --file " + immichBundlePath + " --check"}) {
		t.Fatalf("immich-admin calls %q", m.calls)
	}
	if !slices.Equal(m.placed, []string{"write " + immichBundlePath}) || len(m.files) != 1 {
		t.Fatalf("placed %q", m.placed)
	}
	if m.files[immichBundlePath] != wantImmichBundle {
		t.Fatalf("bundle\n got %s\nwant %s", m.files[immichBundlePath], wantImmichBundle)
	}
	export := "/usr/local/bin/kyidentity apply-setup --file /data/installer/" + parentSetup + "/setup.json --export-client immich"
	calls := slices.DeleteFunc(slices.Clone(identity.calls), func(c string) bool { return strings.Contains(c, "--capabilities") })
	if !slices.Equal(calls, []string{export, "/usr/local/bin/kyidentity apply-setup --file /data/installer/" + parentSetup + "/setup.json --check", export}) {
		t.Fatalf("KyIdentity calls %q", identity.calls)
	}

	// Verify: placement only compares, every apply-setup is a check.
	m.calls, m.placed = nil, nil
	before := snapshot(m.files)
	if err := setup.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.placed, []string{"check " + immichBundlePath}) || snapshot(m.files) != before {
		t.Fatalf("Verify placed %q", m.placed)
	}
	for _, c := range m.calls {
		if !strings.HasSuffix(c, "--check") && !strings.HasSuffix(c, "--capabilities") {
			t.Fatalf("Verify ran %s", c)
		}
	}
	// Later KyIdentity assignees, administrators too, do not fail the check.
	m.later = `,{"id":"` + immichLaterID + `","administrator":true}`
	if err := setup.Verify(ctx); err != nil {
		t.Fatalf("later account: %v", err)
	}
	if strings.Contains(f.out.String(), clientSecret) {
		t.Fatal("client secret printed")
	}
}

// AC7: each refusal leaves Immich's files as they were, and names no secret.
func TestImmichSetupRefusals(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		prepare func(m *fakeImmich, identity *fakeIdentity)
		applied bool // the first setup ran before the refused one
	}{
		"missing capability": {prepare: func(m *fakeImmich, _ *fakeIdentity) {
			m.caps = strings.Replace(immichCaps, `"service-administrator",`, "", 1)
		}},
		"stock image": {prepare: func(m *fakeImmich, _ *fakeIdentity) { m.stock = true }},
		"fourth native user": {prepare: func(m *fakeImmich, _ *fakeIdentity) {
			m.created = `,{"id":"` + immichLaterID + `","administrator":false}`
		}},
		"extra administrator": {prepare: func(m *fakeImmich, _ *fakeIdentity) {
			m.created = `,{"id":"` + immichLaterID + `","administrator":true}`
		}},
		"changed export": {applied: true, prepare: func(_ *fakeImmich, identity *fakeIdentity) { identity.export = immichExport(postSecret) }},
	} {
		t.Run(name, func(t *testing.T) {
			f := immichFixture(t)
			identity := &fakeIdentity{export: immichExport(clientSecret)}
			m := &fakeImmich{files: map[string]string{}}
			setup := immichSetupSteps(t, f, identity, m)["immich.setup"]
			if c.applied {
				if err := setup.Apply(ctx); err != nil {
					t.Fatal(err)
				}
			}
			held := filepath.Join(t.TempDir(), "setup.json")
			os.WriteFile(held, []byte(m.files[immichBundlePath]), 0o600)
			m.probe = map[string]string{immichBundlePath: held}
			before := snapshot(m.files)
			c.prepare(m, identity)
			m.calls = nil
			err := setup.Apply(ctx)
			if err == nil {
				t.Fatal("refusal accepted")
			}
			for _, secret := range []string{clientSecret, postSecret} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error names a secret: %v", err)
				}
			}
			// Before its first setup only the bundle may be new: the module refused to apply it.
			if c.applied && snapshot(m.files) != before || !c.applied && len(m.files) > 1 {
				t.Fatalf("files changed: %v", m.files)
			}
			// The bundle as the target shows it: where it is, that it differs, what to do.
			want := "Immich's setup bundle " + f.h.root + "/immich/setup/installer/" + immichSetupID + "/setup.json on nas differs from what this apply writes"
			if name == "changed export" && (!strings.Contains(err.Error(), want) || !strings.HasSuffix(err.Error(), "move it aside only if you are sure it is stale, then rerun apply")) {
				t.Fatalf("changed export: %v", err)
			}
			if (name == "missing capability" || name == "stock image") && slices.ContainsFunc(m.calls, func(c string) bool { return strings.Contains(c, "--file") }) {
				t.Fatalf("an unqualified server reached setup: %q", m.calls)
			}
		})
	}
}

// sh is a target whose docker is fakeDockerScript, commands run for real in a POSIX sh.
type sh struct{ env string }

func (r sh) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	return remote.Local{}.Run(ctx, r.env+cmd, stdin)
}

// fakeDockerScript answers the docker commands immich.directory runs: the server found by
// its Compose service, its service key, the bridge's one-off runs, start and health.
const fakeDockerScript = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE/log"
case " $* " in
*" ps -q "*) case " $* " in *" label=com.docker.compose.service=immich-server "*) echo c0ffee ;; esac ;;
*" exec c0ffee cat /kyq-setup/service.key "*) cat "$FAKE/root/immich/setup/service.key" ;;
*" run --rm --no-deps -T directory --config /directory/config/runtime.json --init-tls "*)
  c=$FAKE/root/immich/directory/config; umask 077; [ -e "$c/tls.key" ] || { echo key > "$c/tls.key"; echo crt > "$c/tls.crt"; } ;;
*" run --rm --no-deps -T directory --config /directory/config/runtime.json --check "*)
  [ -z "$CHECK_EXIT" ] || { echo "starting" >&2; echo "kyoffboard: refused $SECRET" >&2; exit "$CHECK_EXIT"; } ;;
*" up -d --no-deps --force-recreate directory "*) : > "$FAKE/up" ;;
*" ps --all --format json "*) [ ! -e "$FAKE/up" ] || echo '{"Service":"directory","State":"running","Health":"healthy"}' ;;
*) echo "unexpected docker $*" >&2; exit 1 ;;
esac
`

// immichDirectory is a set-up Immich and its immich.directory step on a real-shell target
// under fake/root, with the server's service key and the installer's SCIM token in place.
func immichDirectory(t *testing.T, m *fakeImmich) (immichDirectoryStep, string) {
	t.Helper()
	ctx := context.Background()
	fake, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(fake, "bin"), 0o700)
	os.WriteFile(filepath.Join(fake, "bin/docker"), []byte(fakeDockerScript), 0o700)
	app := filepath.Join(fake, "root/immich")
	for _, d := range []string{"setup", "secrets", "directory"} {
		os.MkdirAll(filepath.Join(app, d), 0o700)
	}
	os.WriteFile(filepath.Join(app, "setup/service.key"), []byte(immichKey), 0o600)
	os.WriteFile(filepath.Join(app, "secrets/scim_token"), []byte(immichSCIMToken), 0o600)

	identity := &fakeIdentity{export: immichExport(clientSecret), staged: true, applied: true, checkReport: firstSetupCheck(ownerReused)}
	exec := func(ctx context.Context, args []string) ([]byte, error) {
		var out bytes.Buffer
		err := identity.stream(ctx, "kyq-kyidentity", "app-1", "app", append([]string{identityBinary}, args...), remotecommand.StreamOptions{Stdout: &out})
		return out.Bytes(), err
	}
	f := immichFixture(t)
	st := f.stack(t)
	first := st.Integrations[0]
	var client stack.IdentityClient
	if err := json.Unmarshal([]byte(wantImmichClient), &client); err != nil {
		t.Fatal(err)
	}
	first.Identity.Clients = []stack.IdentityClient{client}
	setup := immichSetupStep{i: st.Integrations[1], source: first, first: first.Bundle, issuer: "https://identity.example.com",
		mobile: "https://photos.example.com/api/oauth/mobile-redirect", admin: first.Identity.Admin, everyday: first.Identity.Everyday,
		export: exec, exec: func(_ context.Context, args []string) ([]byte, error) { return m.admin(args) },
		place: func(_ context.Context, args []string, in []byte) error {
			return fakePlace(m.files, args[0], args[1], in)
		}, target: "nas", setup: filepath.Join(app, "setup")}
	if err := setup.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load(catalogtest.FS())
	if err != nil {
		t.Fatal(err)
	}
	runner := sh{env: "PATH=" + remote.Quote(filepath.Join(fake, "bin")) + ":$PATH FAKE=" + remote.Quote(fake) + "; export PATH FAKE; "}
	redact := &engine.Redactor{}
	redact.Add(immichSCIMToken)
	return immichDirectoryStep{setup: setup, app: dockerhost.App{Name: "immich", Root: filepath.Join(fake, "root"), Runner: runner, Catalog: cat["immich"], Redact: redact}, origin: "https://photos.example.com"}, fake
}

const wantImmichRuntime = `{"schema":"ky.offboarding-runtime/1","setup_id":"` + immichSetupID + `","listen":"0.0.0.0:8443","state_dir":"/directory/state","cert_file":"tls.crt","key_file":"tls.key",` +
	`"apps":[{"app":"immich","kind":"immich","origin":"https://photos.example.com","api_origin":"http://immich-server:2283","issuer":"https://identity.example.com","client_id":"immich",` +
	`"product_token_file":"immich-token","scim_token_file":"scim-token","directory":{"administrator_role":"admin","everyday_role":"user","create_accounts":true},` +
	`"accounts":[{"native_uuid":"` + immichOwnerID + `","subject":"` + adminSubject + `"},{"native_uuid":"` + immichEverydayID + `","subject":"` + ownerSubject + `"}]}]}` + "\n"

// AC7: immich.directory copies the tokens on the target (in no argv), writes the bridge's
// configuration from Immich's verified setup, creates its TLS, starts it and waits for health;
// Verify only reads; a different binding or token is refused and nothing is replaced.
func TestImmichDirectory(t *testing.T) {
	ctx := context.Background()
	step, fake := immichDirectory(t, &fakeImmich{files: map[string]string{}})
	config := filepath.Join(fake, "root/immich/directory/config")
	if done, _ := step.Inspect(ctx); done {
		t.Fatal("fresh bridge inspected as done")
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatalf("inspection created %s", config)
	}
	os.Remove(filepath.Join(fake, "log"))
	if err := step.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"immich-token": immichKey, "scim-token": immichSCIMToken, "runtime.json": wantImmichRuntime, "tls.key": "key\n"} {
		b, err := os.ReadFile(filepath.Join(config, name))
		info, _ := os.Stat(filepath.Join(config, name))
		if err != nil || string(b) != want || info.Mode().Perm() != 0o600 {
			t.Errorf("%s = %q, %v", name, b, err)
		}
	}
	for _, d := range []string{config, filepath.Join(fake, "root/immich/directory/state")} {
		if info, err := os.Stat(d); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v", d, err)
		}
	}
	log, _ := os.ReadFile(filepath.Join(fake, "log"))
	var verbs []string
	for _, l := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		for _, v := range []string{"ps -q", "exec c0ffee", "--init-tls", "--force-recreate directory", "ps --all"} {
			if strings.Contains(l, v) {
				verbs = append(verbs, v)
			}
		}
		if strings.Contains(l, immichKey) || strings.Contains(l, immichSCIMToken) {
			t.Errorf("a token in docker's argv: %s", l)
		}
		if strings.HasPrefix(l, "ps -q") && !strings.Contains(l, "label=com.docker.compose.service=immich-server") {
			t.Errorf("container lookup not scoped to immich-server: %s", l)
		}
	}
	if !slices.Equal(verbs, []string{"ps -q", "exec c0ffee", "--init-tls", "--force-recreate directory", "ps --all"}) {
		t.Fatalf("docker calls: %q", verbs)
	}

	// Verify reads: nothing changes and the bridge is neither re-run with --init-tls nor restarted.
	os.Remove(filepath.Join(fake, "log"))
	before := tree(t, filepath.Join(fake, "root"))
	if err := step.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	log, _ = os.ReadFile(filepath.Join(fake, "log"))
	if after := tree(t, filepath.Join(fake, "root")); !maps.Equal(before, after) || strings.Contains(string(log), "--init-tls") || strings.Contains(string(log), " up ") || !strings.Contains(string(log), "--check") {
		t.Fatalf("Verify changed files or ran %s", log)
	}

	// A different binding, a changed key, a refused configuration or a symlinked token is refused.
	refusals := map[string]func(s *immichDirectoryStep) (undo func()){
		"other owner account": func(s *immichDirectoryStep) func() {
			other := &fakeImmich{files: map[string]string{immichBundlePath: wantImmichBundle}, applied: true, owner: immichLaterID}
			s.setup.exec = func(_ context.Context, args []string) ([]byte, error) { return other.admin(args) }
			return func() {}
		},
		"changed service key": func(*immichDirectoryStep) func() {
			p := filepath.Join(fake, "root/immich/setup/service.key")
			os.WriteFile(p, []byte("Q2hhbmdlZENoYW5nZWRDaGFuZ2VkQ2hhbmdlZENoYW4"), 0o600)
			return func() { os.WriteFile(p, []byte(immichKey), 0o600) }
		},
		"symlinked token": func(*immichDirectoryStep) func() {
			p := filepath.Join(fake, "root/immich/secrets/scim_token")
			os.Rename(p, p+".real")
			os.Symlink(p+".real", p)
			return func() { os.Remove(p); os.Rename(p+".real", p) }
		},
		"bridge refuses": func(s *immichDirectoryStep) func() {
			s.app.Runner = sh{env: s.app.Runner.(sh).env + "CHECK_EXIT=1 SECRET=" + immichSCIMToken + "; export CHECK_EXIT SECRET; "}
			return func() {}
		},
	}
	// What each refusal says: the path, which fault, and the action; the bridge's own words.
	config = filepath.Join(fake, "root/immich/directory/config")
	says := map[string]string{
		"other owner account": "the bridge's configuration " + config + "/runtime.json on nas differs from what this apply writes",
		"changed service key": "the bridge's copy of Immich's service key " + config + "/immich-token on nas differs",
		"symlinked token":     "the SCIM token " + filepath.Join(fake, "root/immich/secrets/scim_token") + " on nas is missing, a symlink or not a private file",
		"bridge refuses":      "the bridge refuses its configuration (kyoffboard --check) (kyoffboard: kyoffboard: refused [redacted]). On nas, docker compose -p kyq-immich logs directory (run in " + filepath.Join(fake, "root/immich") + ")",
	}
	for name, prepare := range refusals {
		s := step
		undo := prepare(&s)
		if err := s.Verify(ctx); err == nil || !strings.Contains(err.Error(), says[name]) || strings.Contains(err.Error(), immichSCIMToken) {
			t.Errorf("%s: Verify: %v", name, err)
		} else if name != "bridge refuses" && name != "symlinked token" && !strings.Contains(err.Error(), "; move it aside only if you are sure it is stale, then rerun apply") {
			t.Errorf("%s: no action in %v", name, err)
		}
		if name != "bridge refuses" {
			if err := s.Apply(ctx); err == nil || strings.Contains(err.Error(), immichSCIMToken) || strings.Contains(err.Error(), immichKey) {
				t.Errorf("%s: Apply: %v", name, err)
			}
		}
		if after := tree(t, filepath.Join(fake, "root/immich/directory")); !maps.Equal(subtree(before, "directory"), after) {
			t.Errorf("%s: the bridge's files changed", name)
		}
		undo()
	}
}

// tree maps every regular file under root to its content.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(root, p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

// subtree is the part of a tree under the immich/<prefix>, relative to it.
func subtree(m map[string]string, prefix string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if rest, ok := strings.CutPrefix(k, path.Join("immich", prefix)+"/"); ok {
			out[rest] = v
		}
	}
	return out
}

// AC5: review, plan --noninteractive and apply refuse each fault before any connection; the
// same plan without it passes and lists Immich's five components.
func TestImmichPlanRefusals(t *testing.T) {
	saved := dialTarget
	t.Cleanup(func() { dialTarget = saved })
	for name, c := range map[string]struct {
		edit func(string) string
		want string
	}{
		"on kubernetes": {func(y string) string {
			y = strings.Replace(y, "  - name: immich\n    target: nas\n", "  - name: immich\n    target: k1\n", 1)
			return strings.Replace(y, "    target: nas\n    bundle: "+immichBundlePath, "    target: k1\n    bundle: "+immichBundlePath, 1)
		}, "no Kubernetes deployment"},
		"without kyidentity": {withoutIdentity, "kyidentity"},
		"kyidentity on docker": {func(y string) string {
			y = strings.Replace(y, "  - name: kyidentity\n    target: k1\n", "  - name: kyidentity\n    target: nas\n", 1)
			y = strings.Replace(y, "forward_host: app.kyq-kyidentity.svc.cluster.local", "forward_host: 192.0.2.1", 1)
			return strings.Replace(y, "  - product: kyidentity\n    target: k1\n", "  - product: kyidentity\n    target: nas\n", 1)
		}, "Immich is proven only with KyIdentity on Kubernetes, and this plan places kyidentity on Docker target nas. Place kyidentity on a Kubernetes target"},
		"uppercase owner": {func(y string) string {
			return strings.Replace(y, "email: owner@example.test", "email: Owner@example.test", 1)
		}, `Immich takes only lowercase email addresses. Change identity.everyday.email "Owner@example.test" to "owner@example.test"`},
		"uppercase admin": {func(y string) string {
			return strings.Replace(y, "email: admin@example.test", "email: Admin@Example.test", 1)
		}, "lowercase"},
		"bundle in library": {func(y string) string {
			return strings.Replace(y, immichBundlePath, "/data/installer/"+immichSetupID+"/setup.json", 1)
		}, "is outside /kyq-setup, the bind directory only its setup uses. Change it to: bundle: " + immichBundlePath},
		"no setup": {func(y string) string { return y[:strings.Index(y, "  - product: immich\n")] },
			"Immich needs its setup in the plan, or its first visitor could make themselves its administrator. Add this to integrations (the UUID is fresh; any UUID will do): {product: immich, target: nas, bundle: /kyq-setup/installer/"},
	} {
		t.Run(name, func(t *testing.T) {
			f := immichFixture(t)
			dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
				t.Fatal("connected")
				return nil, nil
			}
			p := filepath.Join(f.state, "stack.yaml")
			b, _ := os.ReadFile(p)
			os.WriteFile(p, []byte(c.edit(string(b))), 0o600)
			for _, cmd := range [][]string{{"review"}, {"plan", "--noninteractive"}, {"apply"}} {
				if err := f.run(cmd...); err == nil || !strings.Contains(err.Error(), c.want) {
					t.Errorf("%s: %v, want %q", cmd[0], err, c.want)
				}
			}
		})
	}
	f := immichFixture(t)
	for _, cmd := range [][]string{{"review"}, {"plan", "--noninteractive"}} {
		if err := f.run(cmd...); err != nil {
			t.Fatalf("%s: %v", cmd[0], err)
		}
		if !strings.Contains(f.out.String(), "Install immich on nas; required: kyidentity\n  Components: server, machine learning, database (Postgres), cache (Valkey), bridge (kyoffboard)\n") {
			t.Fatalf("%s listing:\n%s", cmd[0], f.out.String())
		}
	}
}

// withoutIdentity drops KyIdentity's app, route and integration from the plan.
func withoutIdentity(y string) string {
	y = strings.Replace(y, "  - name: kyidentity\n    target: k1\n", "", 1)
	y = strings.Replace(y, "      - app: kyidentity\n        hostname: identity.example.com\n        forward_host: app.kyq-kyidentity.svc.cluster.local\n        forward_port: 5867\n        certificate_id: 1\n", "", 1)
	start, end := strings.Index(y, "  - product: kyidentity\n"), strings.Index(y, "  - product: immich\n")
	return y[:start] + y[end:]
}

// AC20: the handover, line by line, then 008's backup lines.
func TestImmichHandover(t *testing.T) {
	want := []string{
		"",
		"Immich at https://photos.example.com/: sign in through KyIdentity as owner-admin (administers Immich) or owner",
		"  (your photos). Use owner for photos: Immich lets its administrator keep photos too, and",
		"  owner-admin should only administer.",
		`  Phone app: enter the server URL https://photos.example.com, then choose "Login with OAuth".`,
		"  Anyone you assign to Immich in KyIdentity later signs in the same way; their account is",
		"  created for them.",
		"  Removing someone from Immich in KyIdentity ends their Immich sessions and API keys and keeps",
		"  their account and photos.",
		"  Assigning them again restores their sign-in to the same account, with none of their old",
		"  sessions or keys.",
		"  Its accounts have no local password: every sign-in goes through KyIdentity.",
		"",
		"  Notes:",
		"  - Ports 192.0.2.1:18443 (Immich) and 192.0.2.1:18444 (its KyIdentity bridge) are open to your whole",
		"    LAN: at your firewall, restrict both to the edge's address as the Docker host sees it,",
		"    with a rule tested on the Docker host (docs/guided-setup.md, Catalog Immich on Docker).",
		"    The edge reaches the bridge over TLS whose self-signed certificate it does not verify.",
		"  - Immich connects out to GitHub (version check), Hugging Face (machine learning models) and",
		"    tiles.immich.cloud (map tiles).",
		"  - Source of its KyQuickStart module (AGPL):",
		"    https://github.com/Busnes-app/KyQuickStart/tree/immich-v3.2.0-ky.1/internal/adapters/immich/module",
		"",
	}
	server := "ghcr.io/busnes-app/kyq-immich-server:3.2.0-ky.1@" + catalogtest.Digest
	if got := strings.Split(immichHandover("photos.example.com", "owner-admin", "owner", "192.0.2.1", 18443, "", server), "\n"); !slices.Equal(got, want) {
		t.Fatalf("handover:\n%s", strings.Join(got, "\n"))
	}
	// With the edge's egress_address, the firewall sentence names it.
	egress := "    LAN: at your firewall, restrict both to 192.168.1.20 (the edge's egress_address),\n"
	if got := immichHandover("photos.example.com", "owner-admin", "owner", "192.0.2.1", 18443, "192.168.1.20", server); !strings.Contains(got, egress) || strings.Count(got, "\n") != len(want)-1 {
		t.Fatalf("handover with egress_address:\n%s", got)
	}
	f := immichFixture(t)
	p := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "    allow_cidrs: [192.168.1.0/24]\n", "    allow_cidrs: [192.168.1.0/24]\n    egress_address: 192.168.1.20\n", 1)), 0o600)
	if s, err := load(f.state, nil, f.o); err != nil || !strings.Contains(s.handover, egress) {
		t.Fatalf("apply's handover lacks the edge's egress_address: %v", err)
	}
	// The source link names the tag the running image was built from.
	if got := immichSourceTag("ghcr.io/busnes-app/kyq-immich-server:3.2.0-ky.2@" + catalogtest.Digest); got != "immich-v3.2.0-ky.2" {
		t.Fatalf("source tag = %s", got)
	}
	// Apply prints it with 008's backup lines when the target has a destination.
	f = immichFixture(t)
	p = filepath.Join(f.state, "stack.yaml")
	b, _ = os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "    root: "+f.h.root+"\n", "    root: "+f.h.root+"\n    backup:\n      repository: rest:https://backup.example.com:8000/kyq\n      credential_file: /home/kyq/.rest-password\n", 1)), 0o600)
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	cat, _ := catalog.Load(catalogtest.FS())
	lines := backupLines("immich", s.placed["immich"], cat["immich"])
	if lines == "" || !strings.HasSuffix(s.handover, strings.Join(want, "\n")+lines) {
		t.Fatalf("handover = %s", s.handover)
	}
}

// A plan naming an app built but not released says so, before connecting.
func TestImmichUnreleased(t *testing.T) {
	f := immichFixture(t)
	f.o.Catalog = catalog.Embedded()
	saved := dialTarget
	t.Cleanup(func() { dialTarget = saved })
	dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
		t.Fatal("connected")
		return nil, nil
	}
	for _, cmd := range [][]string{{"review"}, {"plan", "--noninteractive"}, {"apply"}, {"upgrade", "immich"}} {
		if err := f.run(cmd...); err == nil || !strings.Contains(err.Error(), "Immich is built but not released yet: its images are unpublished") {
			t.Errorf("%s: %v", cmd[0], err)
		}
	}
}

// The bridge's last line is cut at 300 bytes on a rune boundary and stays valid UTF-8.
func TestCutUTF8(t *testing.T) {
	s := strings.Repeat("a", 299) + "é" + "tail" // é is 2 bytes, bytes 299-300
	if got := cutUTF8(s, 300); got != strings.Repeat("a", 299) {
		t.Fatalf("cut = %q", got)
	}
	if got := cutUTF8("ok\xff", 300); got != "ok\uFFFD" || cutUTF8("short", 300) != "short" {
		t.Fatalf("invalid bytes: %q", got)
	}
}
