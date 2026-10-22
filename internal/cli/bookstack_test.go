package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/stockbookstack"
)

const (
	bookstackSetupID = "3c4d5e6f-7a8b-4c9d-8e0f-1a2b3c4d5e6f"
	bookstackBundle  = "/data/installer/" + bookstackSetupID + "/setup.json"
)

var bookstackClientCanary = base64.RawURLEncoding.EncodeToString([]byte("CanaryBookStackClientSecretCanar"))

// bookstackCatalog is the embedded catalog with BookStack's receiver image pinned, as it is
// once a kyoffboard tag publishes it, and edit applied to the files.
func bookstackCatalog(edit func(path, body string) string) fs.FS {
	sub := fstest.MapFS{}
	fs.WalkDir(catalog.Embedded(), ".", func(p string, d fs.DirEntry, err error) error {
		if !d.IsDir() {
			b, _ := fs.ReadFile(catalog.Embedded(), p)
			body := string(b)
			if p == "bookstack/manifest.yaml" {
				body = strings.Replace(body, "pending_images: [kyoffboard]\n", "", 1)
				body = strings.Replace(body, "images:\n", "images:\n  kyoffboard: ghcr.io/busnes-app/kyoffboard:0.0.1@sha256:"+strings.Repeat("ab", 32)+"\n", 1)
			}
			if edit != nil {
				body = edit(p, body)
			}
			sub[p] = &fstest.MapFile{Data: []byte(body)}
		}
		return err
	})
	return sub
}

// bookstackPlan installs NPM and KyIdentity on k1 and BookStack on Docker host nas, routed
// through k1's edge; edit changes the YAML before it is written.
func bookstackStackPlan(t *testing.T, f *fixture, edit func(string) string) {
	t.Helper()
	catalogIdentityPlan(t, f, "kyidentity", "  - product: bookstack\n    target: nas\n    bundle: "+bookstackBundle+"\n    identity_client: bookstack\n")
	p := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(p)
	routes := "      - app: bookstack\n        hostname: wiki.example.com\n        forward_host: 192.0.2.1\n        forward_port: 18080\n        certificate_id: 1\n" +
		"      - app: bookstack\n        role: directory\n        hostname: wiki-directory.example.com\n        forward_host: 192.0.2.1\n        forward_port: 18081\n        certificate_id: 1\n"
	y := strings.Replace(string(b), "    target: k1\nedges:", "    target: k1\n  - name: bookstack\n    target: nas\nedges:", 1)
	y = strings.Replace(y, "    connector_cidr: 192.0.2.9/32\n", "    connector_cidr: 192.0.2.9/32\n    egress_address: 192.0.2.50\n", 1)
	y = strings.Replace(y, "integrations:", routes+"integrations:", 1)
	if edit != nil {
		y = edit(y)
	}
	os.WriteFile(p, []byte(y), 0o600)
}

func newBookStackFixture(t *testing.T, edit func(path, body string) string) *fixture {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = bookstackCatalog(edit)
	return f
}

// AC4, AC14: plans refused before any connection, naming the field; a valid plan is accepted.
func TestBookStackPlanRefusals(t *testing.T) {
	route := func(from, to string) func(string) string {
		return func(y string) string { return strings.Replace(y, from, to, 1) }
	}
	identityAt := func(v string) func(p, body string) string {
		return func(p, body string) string {
			if p == "kyidentity/manifest.yaml" {
				return strings.Replace(body, "version: 0.4.6\n", "version: "+v+"\n", 1)
			}
			return body
		}
	}
	for name, c := range map[string]struct {
		catalog func(p, body string) string
		edit    func(string) string
		want    string
	}{
		"on kubernetes": {nil, func(y string) string {
			y = strings.Replace(y, "  - name: bookstack\n    target: nas\n", "  - name: bookstack\n    target: k1\n", 1)
			return strings.Replace(y, "  - product: bookstack\n    target: nas\n", "  - product: bookstack\n    target: k1\n", 1)
		}, "place it on a Docker host"},
		"no directory route":   {nil, route("      - app: bookstack\n        role: directory\n", "      - app: bookstack\n        role: gone\n"), `role "gone" must be one of its roles`},
		"directory route lost": {nil, route("      - app: bookstack\n        role: directory\n        hostname: wiki-directory.example.com\n        forward_host: 192.0.2.1\n        forward_port: 18081\n        certificate_id: 1\n", ""), "needs a route with role directory"},
		"two directory routes": {nil, route("integrations:", "      - app: bookstack\n        role: directory\n        hostname: wiki-hooks.example.com\n        forward_host: 192.0.2.1\n        forward_port: 18081\n        certificate_id: 1\nintegrations:"), `role "directory" must be one of its roles, once`},
		"no app route":         {nil, route("      - app: bookstack\n        hostname: wiki.example.com\n        forward_host: 192.0.2.1\n        forward_port: 18080\n        certificate_id: 1\n", ""), "needs a route with no role: its app"},
		"two app routes":       {nil, route("integrations:", "      - app: bookstack\n        hostname: wiki2.example.com\n        forward_host: 192.0.2.1\n        forward_port: 18080\n        certificate_id: 1\nintegrations:"), `role "" must be one of its roles, once`},
		"no integration":       {nil, route("  - product: bookstack\n    target: nas\n    bundle: "+bookstackBundle+"\n    identity_client: bookstack\n", ""), "add {product: bookstack"},
		"existing kyidentity": {nil, func(y string) string {
			y = strings.Replace(y, "  - name: kyidentity\n    target: k1\n", "", 1)
			y = strings.Replace(y, "      - app: kyidentity\n        hostname: identity.example.com\n        forward_host: app.kyq-kyidentity.svc.cluster.local\n        forward_port: 5867\n        certificate_id: 1\n", "", 1)
			y = strings.Replace(y, "integrations:\n  - product: kyidentity\n    target: k1\n", "existing:\n  - product: kyidentity\n    target: k1\n    url: https://identity.example.com\nintegrations:\n  - product: kyidentity\n    target: k1\n    container: app\n    namespace: kyq-kyidentity\n    pod: app-1\n", 1)
			return y
		}, "bookstack needs KyIdentity v0.4.6 or later"},
		"kyidentity 0.4.5": {identityAt("0.4.5"), nil, "found: 0.4.5, the version this plan installs"},
		"receiver pending": {func(p, body string) string {
			return strings.Replace(body, "images:\n  kyoffboard:", "pending_images: [kyoffboard]\nimages:\n  unused:", 1)
		}, nil, "its kyoffboard image has no published release. Nothing was changed"},
		"no edge egress": {nil, route("    egress_address: 192.0.2.50\n", ""), "egress_address"},
		"wildcard forward": {nil, func(y string) string {
			return strings.ReplaceAll(y, "forward_host: 192.0.2.1", "forward_host: 0.0.0.0")
		}, "forward_host 0.0.0.0 must be one LAN IPv4 address"},
		"shared forward": {nil, route("forward_port: 18081", "forward_port: 18080"), "forward_port 18080 is given to two services"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newBookStackFixture(t, c.catalog)
			bookstackStackPlan(t, f, c.edit)
			dialed := false
			saved := dialTarget
			defer func() { dialTarget = saved }()
			dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
				dialed = true
				return nil, errors.New("no connection expected")
			}
			for _, args := range [][]string{{"review"}, {"plan", "--noninteractive"}, {"apply"}} {
				if err := f.run(args...); err == nil || !strings.Contains(err.Error(), c.want) || dialed {
					t.Fatalf("%v: err = %v (dialed %v), want %q", args, err, dialed, c.want)
				}
			}
		})
	}
	f := newBookStackFixture(t, nil)
	bookstackStackPlan(t, f, nil)
	if err := f.run("review"); err != nil {
		t.Fatal(err)
	}
	// The embedded catalog has no receiver image yet: no plan installs BookStack.
	f.o.Catalog = catalog.Embedded()
	if err := f.run("review"); err == nil || !strings.Contains(err.Error(), "cannot be installed yet") {
		t.Fatalf("embedded catalog: %v", err)
	}
}

// AC4: an installed KyIdentity older than 0.4.6 that this run leaves in place refuses BookStack
// before any step.
func TestBookStackRecordedIdentity(t *testing.T) {
	for _, c := range []struct {
		order   []string
		version string
		refused bool
	}{
		{[]string{"bookstack"}, "0.4.5", true}, {[]string{"bookstack"}, "", true}, {[]string{"bookstack"}, "0.4.6", false},
		{[]string{"kyidentity", "bookstack"}, "0.4.5", false}, {[]string{"kydrive"}, "0.4.5", false},
	} {
		s := &session{order: c.order, placed: map[string]stack.Target{"kyidentity": {Name: "k1"}},
			drivers: map[string]driver{"k1": recordedDriver{d: managed.Deployed{Found: c.version != "", Version: c.version}}}}
		err := s.refuseOldIdentity(context.Background())
		if (err != nil) != c.refused || err != nil && !strings.Contains(err.Error(), "v0.4.6") {
			t.Errorf("%v at %q: %v", c.order, c.version, err)
		}
	}
}

type recordedDriver struct {
	d managed.Deployed
	driver
}

func (r recordedDriver) deployed(context.Context, string) (managed.Deployed, error) { return r.d, nil }

// The client: its redirect at the app's host, its directory webhook at the directory route's.
func TestBookStackClient(t *testing.T) {
	f := newBookStackFixture(t, nil)
	bookstackStackPlan(t, f, nil)
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s.integrations[0].Identity.Clients)
	want := `[{"id":"bookstack","name":"BookStack","redirectUris":["https://wiki.example.com/oidc/callback"],"postLogoutUris":null,"administratorRole":"bookstack.admin","everydayRole":"bookstack.user","suiteWebhook":{"url":"https://wiki-directory.example.com/kyq/directory"}}]`
	if string(b) != want {
		t.Fatalf("clients = %s", b)
	}
	for _, w := range []string{"BookStack at https://wiki.example.com/", bookstackResidual} {
		if !strings.Contains(s.handover, w) {
			t.Errorf("handover lacks %q:\n%s", w, s.handover)
		}
	}
}

// AC5: the setup before its deploy, bootstrap and roles after its health, the directory last.
func TestBookStackStepOrder(t *testing.T) {
	f := newBookStackFixture(t, nil)
	bookstackStackPlan(t, f, nil)
	s, _ := messageSession(t, f, &fakeIdentity{})
	s.edges = nil
	steps, err := s.deploySteps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, st := range steps {
		if strings.HasPrefix(st.ID(), "bookstack.") {
			ids = append(ids, st.ID())
		}
	}
	want := []string{"bookstack.secrets", "bookstack.setup", "bookstack.deploy", "bookstack.health", "bookstack.bootstrap", "bookstack.roles", "bookstack.directory"}
	if !slices.Equal(ids, want) || steps[len(steps)-1].ID() != "bookstack.directory" {
		t.Fatalf("steps %v", ids)
	}
}

// bookstack.setup writes the four files once, private, from the export; a changed one refuses.
func TestBookStackSetupFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := stack.Integration{Product: "kyidentity", Bundle: "/data/installer/271333e2-c221-4601-b249-563b3d828530/setup.json",
		Identity: &stack.IdentityPreparation{SetupID: "271333e2-c221-4601-b249-563b3d828530", Admin: stack.IdentityAccount{Username: "owner-admin"},
			Clients: []stack.IdentityClient{{ID: "bookstack", RedirectURIs: []string{"https://wiki.example.com/oidc/callback"}, AdministratorRole: "bookstack.admin",
				SuiteWebhook: &stack.IdentityWebhook{URL: "https://wiki-directory.example.com/kyq/directory"}}}}}
	export := `{"schema":"kyidentity.client-credentials/1","setupId":"271333e2-c221-4601-b249-563b3d828530","issuerUrl":"https://identity.example.com","clientId":"bookstack",` +
		`"redirectUris":["https://wiki.example.com/oidc/callback"],"directoryUrl":"https://wiki-directory.example.com/kyq/directory","clientSecret":"` + bookstackClientCanary +
		`","directorySecret":"` + directoryCanary + `","administrator":{"subject":"` + adminSubject + `","username":"owner-admin","role":"bookstack.admin"}}`
	exec := func(_ context.Context, args []string) ([]byte, error) {
		if slices.Contains(args, "--capabilities") {
			return []byte(strings.Replace(identityCaps, `"check"]`, `"check","protected-setup-files","bundle-preparation","target-credentials","client-credential-export"]`, 1)), nil
		}
		return []byte(export), nil
	}
	redact := &engine.Redactor{}
	s := bookstackSetupStep{app: "bookstack", target: "nas", client: "bookstack", issuer: "https://identity.example.com", source: source,
		export: exec, file: hostFile(remote.Local{}, dir), redact: redact}
	if err := s.Verify(ctx); err == nil {
		t.Fatal("Verify passed before Apply")
	}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{"oidc_issuer": "https://identity.example.com", "oidc_client_id": "bookstack", "oidc_client_secret": bookstackClientCanary, "directory_secret": directoryCanary} {
		p := filepath.Join(dir, "data", f)
		b, _ := os.ReadFile(p)
		fi, _ := os.Stat(p)
		if string(b) != want || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s = %q mode %v", f, b, fi.Mode().Perm())
		}
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if redact.Redact(bookstackClientCanary+directoryCanary) == bookstackClientCanary+directoryCanary {
		t.Error("secrets not redacted")
	}
	os.WriteFile(filepath.Join(dir, "data", "oidc_client_secret"), []byte("other"), 0o600)
	if err := s.Apply(ctx); err == nil || !strings.Contains(err.Error(), "never replaced") {
		t.Fatalf("changed secret: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "data", "oidc_client_secret")); string(b) != "other" {
		t.Fatal("a retained file was replaced")
	}
}

// AC6: bootstrap killed at each stage converges on a rerun: it removes a leftover one-off,
// stops BookStack before the standard-auth one-off starts, publishes no port, re-keys, and
// removes the one-off even when it fails.
func TestBookStackBootstrapKilled(t *testing.T) {
	ctx := context.Background()
	compose := func(args string) string { return "COMPOSE " + args }
	saved := bootstrapPoll
	bootstrapPoll = 0
	defer func() { bootstrapPoll = saved }()
	// The calls: stop, remove, start the one-off, its readiness (polled, so not a kill), re-key,
	// remove, start BookStack.
	for _, kill := range []int{0, 1, 2, 4, 5, 6} {
		var log []string
		token := false // the receiver's token works
		step := bootstrapStep{app: "bookstack", target: "nas", compose: compose,
			check: func(context.Context) error {
				if !token {
					return errors.New("refused")
				}
				return nil
			}}
		calls := 0
		step.run = func(_ context.Context, cmd string) error {
			log = append(log, cmd)
			calls++
			if calls == kill+1 {
				return errors.New("killed")
			}
			if strings.Contains(cmd, "create-admin") {
				token = true
			}
			return nil
		}
		if err := step.Apply(ctx); err == nil {
			t.Fatalf("kill at %d: Apply passed", kill)
		}
		token = false // the old token is refused: a rerun bootstraps again
		calls = -100
		if done, _ := step.Inspect(ctx); done {
			t.Fatal("Inspect passed with a refused token")
		}
		if err := step.Apply(ctx); err != nil {
			t.Fatalf("rerun after kill at %d: %v", kill, err)
		}
		if err := step.Verify(ctx); err != nil {
			t.Fatal(err)
		}
		all := strings.Join(log, "\n")
		if strings.Contains(all, "--service-ports") || strings.Contains(all, " -p ") || strings.Contains(all, "--publish") {
			t.Fatalf("a published port:\n%s", all)
		}
		// Every start of the one-off follows a stop of BookStack and a removal, in its run.
		for i, c := range log {
			if strings.Contains(c, "run -d --name kyq-bookstack-standard") && (i < 2 || !strings.HasPrefix(log[i-2], "COMPOSE stop bookstack") || !strings.HasPrefix(log[i-1], "docker rm -f")) {
				t.Fatalf("kill at %d: one-off started without stopping BookStack first:\n%s", kill, all)
			}
		}
		if !strings.HasPrefix(log[len(log)-1], "docker rm -f "+bootstrapContainer) || strings.Count(all, "create-admin") < 1 {
			t.Fatalf("kill at %d: no final removal or re-key:\n%s", kill, all)
		}
		if !strings.Contains(all, "--initial --email=admin@admin.com") || !strings.Contains(all, "--generate-password 2>/dev/null | (COMPOSE run --rm -T bootstrap)") {
			t.Fatalf("re-key does not pipe the password into the receiver image:\n%s", all)
		}
		// The failed run removed its one-off too.
		first := log[:kill+2]
		if !strings.HasPrefix(first[len(first)-1], "docker rm -f "+bootstrapContainer) {
			t.Fatalf("kill at %d: the failed run left its one-off:\n%s", kill, strings.Join(first, "\n"))
		}
	}
}

// AC4: the guided plan asks BookStack's two hostnames under one domain, one port each.
func TestGuidedBookStackRoutes(t *testing.T) {
	cat, err := catalog.Load(bookstackCatalog(nil))
	if err != nil {
		t.Fatal(err)
	}
	st := &stack.Stack{Version: 1, Targets: []stack.Target{{Name: "nas", Host: "192.0.2.1", User: "kyq", Port: 22, Root: "/opt/kyq"}},
		Apps: []stack.App{{Name: "bookstack", Target: "nas"}}}
	answers := []string{"lan", "https://npm.example.com", "192.168.1.0/24", "", "yes", "example.com", "192.0.2.1", "3", "", "", "", ""}
	var out strings.Builder
	p := &prompts{ctx: context.Background(), in: bufioReader(strings.Join(answers, "\n") + "\n"), out: &out, answers: map[string]string{}, draft: filepath.Join(t.TempDir(), "answers.json")}
	if err := guidedEdges(p, st, cat); err != nil {
		t.Fatal(err, out.String())
	}
	var got []string
	for _, r := range st.Edges[0].Routes {
		got = append(got, r.Role+"="+r.Hostname+":"+strconvItoa(r.ForwardPort)+"@"+r.ForwardHost)
	}
	if want := []string{"=bookstack.example.com:80@192.0.2.1", "directory=bookstack-directory.example.com:8080@192.0.2.1"}; !slices.Equal(got, want) {
		t.Fatalf("routes = %v\n%s", got, out.String())
	}
	for _, line := range []string{"bookstack's domain. Its hostnames go under it", "bookstack.example.com (app), bookstack-directory.example.com (directory)",
		"bookstack app hostname [bookstack.example.com]", "bookstack directory hostname [bookstack-directory.example.com]", "covering *.example.com []"} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("prompts lack %q:\n%s", line, out.String())
		}
	}
	if strings.Contains(out.String(), "Matrix") || strings.Contains(out.String(), "permanent") {
		t.Errorf("Matrix wording for BookStack:\n%s", out.String())
	}
	st.Edges[0].EgressAddress = "192.0.2.50"
	if _, err := withRoute(st, "bookstack", st.Targets[0], cat["bookstack"]); err != nil {
		t.Fatalf("guided routes refused: %v", err)
	}
}

func TestBookStackServiceAccount(t *testing.T) {
	if bookstackService != stockbookstack.ServiceEmail {
		t.Fatal("bootstrap re-keys another account than the receiver protects")
	}
}

// AC15: the handover's offboarding residual and self-delete note are the guide's, word for word.
func TestBookStackResidualMatchesDocs(t *testing.T) {
	doc, err := os.ReadFile("../../docs/guided-setup.md")
	if err != nil {
		t.Fatal(err)
	}
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	section, _, _ := strings.Cut(string(doc[strings.Index(string(doc), "## Catalog BookStack on Docker"):]), "\n## ")
	if !strings.Contains(flat(section), flat(bookstackResidual)) {
		t.Fatal("docs/guided-setup.md's BookStack section lacks the handover's residual verbatim")
	}
	if h := bookstackHandover("wiki.example.com", "a", "e"); !strings.Contains(h, bookstackResidual) {
		t.Fatal("the handover lacks the residual")
	}
}

// The edge gets BookStack's directory route as a webhook route, and no other route.
func TestBookStackWebhookRoute(t *testing.T) {
	f := newBookStackFixture(t, nil)
	bookstackStackPlan(t, f, nil)
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	var hooks []string
	for _, e := range s.edges {
		for _, r := range e.Routes {
			if r.Webhook {
				hooks = append(hooks, r.Hostname)
			}
		}
	}
	if !slices.Equal(hooks, []string{"wiki-directory.example.com"}) {
		t.Fatalf("webhook routes %v", hooks)
	}
}
