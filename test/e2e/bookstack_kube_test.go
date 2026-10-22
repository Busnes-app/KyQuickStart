//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// bookstackPage is a page as the service token reads it: what offboarding must leave intact.
type bookstackPage struct {
	ID        int              `json:"id"`
	BookID    int              `json:"book_id"`
	HTML      string           `json:"html"`
	Name      string           `json:"name"`
	CreatedBy struct{ ID int } `json:"created_by"`
	OwnedBy   struct{ ID int } `json:"owned_by"`
}

// TestKubernetesBookStack installs the catalog BookStack on a Docker target behind the kind
// cluster's lan edge, after KyIdentity's first setup (a setup amendment), and proves its
// install, bootstrap, sign-in by role, offboarding with the accepted residual, removal before
// first sign-in, reactivation, and backup and restore with the replay of removals.
func TestKubernetesBookStack(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	work := t.TempDir()
	state := filepath.Join(work, "state")
	os.MkdirAll(state, 0o700)
	name, kc := kindCluster(t, work)
	node := name + "-control-plane"
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	cs := kubernetes.NewForConfigOrDie(cfg)
	catalogDir := filepath.Join(work, "catalog")
	for _, app := range []string{"npm", "kyidentity"} {
		if err := os.CopyFS(filepath.Join(catalogDir, app), os.DirFS("../../internal/catalog/apps/"+app)); err != nil {
			t.Fatal(err)
		}
	}
	loadCatalogApp(t, name, cs, "kyidentity")
	logAppOnFailure(t, cs, "kyidentity")
	plan := filepath.Join(state, "stack.yaml")
	os.WriteFile(plan, []byte("version: 1\ntargets:\n  - name: k8s\n    kubeconfig: "+kc+"\n    storage_class: standard\napps:\n  - name: npm\n    target: k8s\n"), 0o600)
	var outputs, trust []string
	run := func(stdin string, args ...string) (string, error) {
		t.Helper()
		out, err := kyq(t, ctx, state, catalogDir, trust, stdin, args...)
		outputs = append(outputs, out)
		return out, err
	}
	apply := func(label string) string {
		t.Helper()
		out, err := run("", "apply")
		if err != nil {
			t.Fatalf("apply (%s): %v\n%s", label, err, out)
		}
		return out
	}
	apply("npm")
	connected, err := kube.Connect(kc, "")
	if err != nil {
		t.Fatal(err)
	}
	sec, err := cs.CoreV1().Secrets("kyq-edge").Get(ctx, "kyq-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	certID, certPEM := uploadCertificate(t, ctx, connected.ServiceDialer("kyq-npm", "app", 81), string(sec.Data["npm_token"]))
	nodeIP := docker(t, "inspect", node, "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
	bs := startBookStackTarget(t, work, catalogDir, nodeIP, certPEM)
	t.Logf("AC1: receiver image %s (local test build: %v)", bs.receiver, bs.local)
	st, err := stack.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	st.Edges = []stack.Edge{{Target: "k8s", Provider: "lan", NPMURL: "kube://kyq-npm/app:81", ClientPort: 30443, EgressAddress: nodeIP,
		AllowCIDRs: []string{mustNode(t, ctx, cs, node).Spec.PodCIDR, bs.forward + "/32", docker(t, "inspect", bs.relay, "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}") + "/32"}}}
	addCatalogIdentity(st, certID)
	savePlan(t, plan, st)
	// KyIdentity delivers BookStack's directory events through the edge, by name.
	testClusterDNS(t, ctx, cs, nodeIP, nodeIP+" "+bookstackHosts[""]+" "+bookstackHosts["directory"])
	apply("identity")
	trustTestCA(t, ctx, cs, connected, "kyidentity", certPEM)

	// AC5: BookStack joins later, by amendment.
	st, _ = stack.Load(plan)
	addCatalogBookStack(st, certID, bs)
	savePlan(t, plan, st)
	trust = []string{"--trust-host-key", "nas=" + bs.fp}
	listening := wildcardListeners(t, bs.name)
	argv := watchArgv()
	installStart := time.Now()
	out := apply("bookstack")
	seen := argv.end()
	t.Logf("BookStack apply took %s", time.Since(installStart).Round(time.Second))
	ids := []string{"kyidentity.amend.bookstack", "bookstack.secrets", "bookstack.setup", "bookstack.deploy", "bookstack.health", "bookstack.bootstrap", "bookstack.roles", "bookstack.directory"}
	for _, id := range ids {
		if status(out, id) != "ok" {
			t.Fatalf("%s = %q\n%s", id, status(out, id), out)
		}
	}
	transport := bookstackTransport(t, certPEM, nodeIP)
	plain := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	edgeGet := func(host, path string) (int, string) {
		t.Helper()
		r, err := plain.Get("https://" + host + ":30443" + path)
		if err != nil {
			t.Fatalf("GET %s%s: %v", host, path, err)
		}
		r.Body.Close()
		return r.StatusCode, r.Header.Get("Location")
	}
	if code, _ := edgeGet(bookstackHosts[""], "/status"); code != 200 {
		t.Fatalf("AC5: /status through the edge: %d", code)
	}
	// The directory route serves its path alone; GET there is the receiver's 405.
	for path, want := range map[string]int{"/kyq/directory": 405, "/": 404, "/status": 404} {
		if code, _ := edgeGet(bookstackHosts["directory"], path); code != want {
			t.Errorf("AC5: directory route %s = %d, want %d", path, code, want)
		}
	}

	root := bs.root + "/bookstack"
	secrets := map[string]string{}
	for _, f := range []string{"secrets/db_password", "secrets/db_root_password", "secrets/app_key", "data/oidc_client_secret", "data/directory_secret", "token/bookstack_token"} {
		secrets[f] = strings.TrimSpace(docker(t, "exec", bs.name, "cat", root+"/"+f))
	}
	service := secrets["token/bookstack_token"]
	if bad := docker(t, "exec", bs.name, "find", root, "!", "-user", "kyq"); bad != "" {
		t.Errorf("AC5: files not the SSH user's: %s", bad)
	}
	modes := docker(t, "exec", bs.name, "sh", "-c", "stat -c '%a %n' "+root+"/secrets/* "+root+"/data/* "+root+"/token/*")
	for _, l := range strings.Split(modes, "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[0] != "600" {
			t.Errorf("AC5: mode %s", l)
		}
	}
	// AC1 and AC5: the pinned images, no DOCKER_MODS, publications on the forward address only,
	// no secret in any container's environment or command.
	manifest, _ := os.ReadFile(filepath.Join(catalogDir, "bookstack/manifest.yaml"))
	for _, svc := range []string{"bookstack", "mariadb", "directory"} {
		var in []struct {
			Config struct {
				Image                string
				Env, Cmd, Entrypoint []string
			}
			HostConfig struct {
				Memory, MemorySwap int64
				Privileged         bool
				CapAdd             []string
				PortBindings       map[string][]struct{ HostIp, HostPort string }
			}
		}
		if err := json.Unmarshal([]byte(docker(t, "inspect", "kyq-bookstack-"+svc+"-1")), &in); err != nil || len(in) != 1 {
			t.Fatalf("inspect %s: %v", svc, err)
		}
		i := in[0]
		if !strings.Contains(i.Config.Image, "@sha256:") || !bytes.Contains(manifest, []byte(i.Config.Image)) {
			t.Errorf("AC1: %s image %s is not the manifest's pin", svc, i.Config.Image)
		}
		t.Logf("AC1: %s runs %s", svc, i.Config.Image)
		if slices.ContainsFunc(i.Config.Env, func(e string) bool { return strings.HasPrefix(e, "DOCKER_MODS=") }) {
			t.Errorf("AC1: %s sets DOCKER_MODS", svc)
		}
		if i.HostConfig.Memory <= 0 || i.HostConfig.Memory != i.HostConfig.MemorySwap || i.HostConfig.Privileged || len(i.HostConfig.CapAdd) > 0 {
			t.Errorf("%s memory %d swap %d privileged %v cap_add %v", svc, i.HostConfig.Memory, i.HostConfig.MemorySwap, i.HostConfig.Privileged, i.HostConfig.CapAdd)
		}
		for p, bindings := range i.HostConfig.PortBindings {
			for _, b := range bindings {
				if b.HostIp != bs.forward || svc == "mariadb" {
					t.Errorf("AC5: %s publishes %s on %q", svc, p, b.HostIp)
				}
			}
		}
		for _, v := range secrets {
			if strings.Contains(strings.Join(slices.Concat(i.Config.Env, i.Config.Cmd, i.Config.Entrypoint), "\n"), v) {
				t.Errorf("AC5: %s's environment or command holds a secret", svc)
			}
		}
	}
	bsPorts := []string{"80", "3306", "8080", strconv.Itoa(bs.ports[""]), strconv.Itoa(bs.ports["directory"])}
	for l := range wildcardListeners(t, bs.name) {
		if !listening[l] {
			if _, port, _ := net.SplitHostPort(strings.Fields(l)[1]); slices.Contains(bsPorts, port) {
				t.Errorf("AC5: new wildcard listener on the target: %s", l)
			}
		}
	}
	for _, l := range seen {
		for k, v := range secrets {
			if strings.Contains(l, v) {
				t.Errorf("AC5: %s in a command line during apply: %.120s", k, l)
			}
		}
		if regexp.MustCompile(`--password(=|\s)`).MatchString(l) {
			t.Errorf("AC5: a password option in a command line: %.200s", l)
		}
	}
	t.Logf("AC5: %d distinct command lines sampled during the apply", len(seen))

	// AC5: KyIdentity's client, as the setup amendment that added it holds it.
	admin := newIdentityAdmin(t, ctx, transport, "prepared-admin", ownerPassword(t, ctx, connected, "admin.password"))
	amendment := ""
	for _, i := range mustStack(t, plan).Integrations {
		if i.Product == "bookstack" {
			amendment = i.IdentityAmendment
		}
	}
	ns, pod, container, err := connected.AppPod(ctx, "kyidentity")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := connected.Exec(ctx, ns, pod, container, []string{"cat", "/data/installer/" + amendment + "/setup.json"})
	if err != nil || amendment == "" {
		t.Fatalf("AC5: KyIdentity's amendment %q: %v", amendment, err)
	}
	for _, want := range []string{bookstackOrigin + "/oidc/callback", "https://" + bookstackHosts["directory"] + ":30443/kyq/directory"} {
		if !bytes.Contains(reg, []byte(want)) {
			t.Errorf("AC5: KyIdentity's bookstack client lacks %s: %.600s", want, reg)
		}
	}

	// AC6: the seeded password and the service account sign in nowhere; guests see nothing.
	forms := []struct{ label, base string }{{"edge", bookstackOrigin}, {"published port", "http://" + net.JoinHostPort(bs.forward, strconv.Itoa(bs.ports[""]))}}
	for _, f := range forms {
		for _, pw := range []string{"password", "any-password-at-all"} {
			if postLogin(t, ctx, transport, f.base, pw) {
				t.Errorf("AC6: POST /login as %s with %q signed in through the %s", "admin@admin.com", pw, f.label)
			}
		}
	}
	if code, loc := edgeGet(bookstackHosts[""], "/books"); code != 302 || !strings.HasSuffix(loc, "/login") {
		t.Errorf("AC6: a guest's /books: %d %s", code, loc)
	}
	serviceID := 0
	var svc struct{ Data []struct{ ID int } }
	bookstackAPI(t, ctx, transport, service, "GET", "users?filter%5Bemail%5D=admin%40admin.com", &svc)
	if len(svc.Data) == 1 {
		serviceID = svc.Data[0].ID
	}
	if n := bookstackSQL(t, fmt.Sprint("select count(*) from api_tokens where user_id=", serviceID)); serviceID == 0 || n != "1" {
		t.Errorf("AC6: the service account (%d) holds %s tokens, want 1", serviceID, n)
	}

	// AC5: a rerun skips every BookStack step.
	out = apply("rerun")
	for _, id := range ids {
		if s := status(out, id); s != "skipped" {
			t.Errorf("AC5: rerun %s = %q", id, s)
		}
	}
	for _, want := range []string{"BookStack at " + bookstackOrigin + "/", "removes every\n  role from their BookStack account", "until it has been idle for\n  60 minutes", "The edge refuses self-delete"} {
		if !strings.Contains(out, want) {
			t.Errorf("AC15: handover lacks %q", want)
		}
	}

	// AC7: the owner's accounts.
	adminPW, everydayPW := ownerPassword(t, ctx, connected, "admin.password"), ownerPassword(t, ctx, connected, "everyday.password")
	owner, ok := bookstackSignIn(t, ctx, transport, "prepared-admin", slices.Clone(adminPW))
	if !ok {
		t.Fatal("AC7: the owner's admin was refused")
	}
	if _, roles := bookstackAccount(t, ctx, transport, service, admin.findUser("prepared-admin")); !slices.Equal(roles, []string{"Admin", "Editor"}) {
		t.Errorf("AC7: the owner admin's roles %v", roles)
	}
	if code, _, _ := owner.get("/settings/users"); code != 200 {
		t.Errorf("AC7: the owner admin's /settings/users = %d", code)
	}
	everyday, ok := bookstackSignIn(t, ctx, transport, "prepared-user", slices.Clone(everydayPW))
	if !ok {
		t.Fatal("AC7: the owner's everyday account was refused")
	}
	if _, roles := bookstackAccount(t, ctx, transport, service, admin.findUser("prepared-user")); !slices.Equal(roles, []string{"Editor"}) {
		t.Errorf("AC7: the everyday owner's roles %v", roles)
	}
	code, book := everyday.createBook("Everyday book")
	if code != 200 {
		t.Fatalf("AC7: everyday book: %d", code)
	}
	if code, _ := everyday.createPage(book, "Everyday page", "<p>hello</p>"); code != 200 {
		t.Errorf("AC7: everyday page: %d", code)
	}
	for _, p := range []string{"/settings", "/settings/users"} {
		if code, _, _ := everyday.get(p); code == 200 {
			t.Errorf("AC7: the everyday owner reached %s", p)
		}
	}

	// AC8: L, assigned with no role, gets Editor; U, never assigned, is refused.
	app := admin.app("bookstack")
	people := map[string]string{}
	for _, n := range []string{"leaver", "unassigned", "admin-a", "early", "restored", "outage"} {
		people[n] = admin.createPerson(n, n+"@example.test", n+"-Password-1")
	}
	admin.assign(app, people["leaver"])
	l, ok := bookstackSignIn(t, ctx, transport, "leaver", []byte("leaver-Password-1"))
	if !ok {
		t.Fatal("AC8: L was refused")
	}
	if _, roles := bookstackAccount(t, ctx, transport, service, people["leaver"]); !slices.Equal(roles, []string{"Editor"}) {
		t.Errorf("AC8: L's roles %v", roles)
	}
	code, lBook := l.createBook("L's book")
	if code != 200 {
		t.Fatalf("AC8: L's book: %d", code)
	}
	if code, _ := l.createPage(lBook, "L's page", "<p>L</p>"); code != 200 {
		t.Errorf("AC8: L's page: %d", code)
	}
	if _, ok := bookstackSignIn(t, ctx, transport, "unassigned", []byte("unassigned-Password-1")); ok {
		t.Error("AC8: KyIdentity let U, never assigned, sign in")
	}
	if id, _ := bookstackAccount(t, ctx, transport, service, people["unassigned"]); id != 0 {
		t.Errorf("AC8: U has BookStack account %d", id)
	}

	// AC9: A, mapped to bookstack.admin, makes content and a token, then is unassigned.
	admin.assign(app, people["admin-a"])
	var appRoles struct{ Roles []struct{ ID, Name string } }
	admin.get("/api/admin/app-registry/"+app+"/roles", &appRoles)
	k := slices.IndexFunc(appRoles.Roles, func(r struct{ ID, Name string }) bool { return r.Name == "bookstack.admin" })
	if k < 0 {
		t.Fatalf("KyIdentity has no bookstack.admin role: %v", appRoles)
	}
	admin.change("PUT", "/api/admin/app-registry/"+app+"/roles/"+appRoles.Roles[k].ID+"/assignments/users/"+people["admin-a"], map[string]string{})
	a, ok := bookstackSignIn(t, ctx, transport, "admin-a", []byte("admin-a-Password-1"))
	if !ok {
		t.Fatal("AC9: A was refused")
	}
	aID, aRoles := bookstackAccount(t, ctx, transport, service, people["admin-a"])
	if !slices.Equal(aRoles, []string{"Admin", "Editor"}) {
		t.Errorf("AC9: A's roles %v", aRoles)
	}
	aToken := a.mintToken()
	secrets["A's token"] = aToken
	code, bookB := a.createBook("Book B")
	if code != 200 {
		t.Fatalf("AC9: book B: %d", code)
	}
	code, pageP := a.createPage(bookB, "Page P", "<p>P's content</p>")
	if code != 200 {
		t.Fatalf("AC9: page P: %d", code)
	}
	p := readPage(t, ctx, transport, service, bookB, "Page P")
	imagePath := a.uploadImage(p.ID)
	if code, _, _ := a.get(imagePath); code != 200 {
		t.Fatalf("AC9: A's own image: %d", code)
	}
	if code, _ := edgeGet(bookstackHosts[""], imagePath); code == 200 {
		t.Error("AC6: a guest fetched an uploaded image")
	}
	if code := bookstackAPI(t, ctx, transport, aToken, "GET", "books", nil); code != 200 {
		t.Fatalf("AC9: A's token before: %d", code)
	}
	removed := time.Now()
	admin.change("DELETE", "/api/admin/app-registry/"+app+"/assignments/users/"+people["admin-a"], nil)
	took := waitRoles(t, ctx, transport, service, people["admin-a"], removed)
	t.Logf("AC9: A's roles were [] %s after the unassignment", took.Round(time.Second))
	offboarded(t, ctx, transport, service, a, aToken, bookB, pageP, imagePath, p, aID)
	if code, _ := a.form("/my-account", url.Values{"_method": {"DELETE"}}); code != 404 {
		t.Errorf("AC9: DELETE /my-account through the edge: %d", code)
	}
	if code, _, _ := a.get("/my-account/delete"); code != 404 {
		t.Errorf("AC9: /my-account/delete through the edge: %d", code)
	}
	if id, _ := bookstackAccount(t, ctx, transport, service, people["admin-a"]); id != aID {
		t.Error("AC9: A's account is gone")
	}
	if a.authorized() {
		t.Error("AC9: KyIdentity authorized A after the removal")
	}
	// KyIdentity's setup check refuses role holders it did not create: an installer rerun
	// would fail while A holds bookstack.admin (the documented second-administrator limit).
	admin.change("DELETE", "/api/admin/app-registry/"+app+"/roles/"+appRoles.Roles[k].ID+"/assignments/users/"+people["admin-a"], nil)
	resync(t, ctx, connected)

	// AC9 for L, disabled in KyIdentity; AC11, L enabled again.
	_, lPage := l.createPage(lBook, "L's second page", "<p>L2</p>")
	lp := readPage(t, ctx, transport, service, lBook, "L's second page")
	lID, _ := bookstackAccount(t, ctx, transport, service, people["leaver"])
	removed = time.Now()
	admin.change("PUT", "/api/admin/users/"+people["leaver"], map[string]string{"status": "disabled"})
	took = waitRoles(t, ctx, transport, service, people["leaver"], removed)
	t.Logf("AC9: L's roles were [] %s after the disable", took.Round(time.Second))
	offboarded(t, ctx, transport, service, l, "", lBook, lPage, "", lp, lID)
	if l.authorized() {
		t.Error("AC9: KyIdentity authorized the disabled L")
	}
	resync(t, ctx, connected)
	admin.change("PUT", "/api/admin/users/"+people["leaver"], map[string]string{"status": "active"})
	if _, ok := bookstackSignIn(t, ctx, transport, "leaver", []byte("leaver-Password-1")); !ok {
		t.Fatal("AC11: L, enabled again, was refused")
	}
	if _, roles := bookstackAccount(t, ctx, transport, service, people["leaver"]); !slices.Equal(roles, []string{"Editor"}) {
		t.Errorf("AC11: L's roles after reactivation %v", roles)
	}

	// AC10: E, unassigned before ever signing in.
	admin.assign(app, people["early"])
	admin.change("DELETE", "/api/admin/app-registry/"+app+"/assignments/users/"+people["early"], nil)
	resync(t, ctx, connected)
	if id, _ := bookstackAccount(t, ctx, transport, service, people["early"]); id != 0 {
		t.Errorf("AC10: E has BookStack account %d", id)
	}
	if _, ok := bookstackSignIn(t, ctx, transport, "early", []byte("early-Password-1")); ok {
		t.Error("AC10: KyIdentity let E sign in after the unassignment")
	}

	// A removal while the receiver is down: the edge answers 503, KyIdentity retries, and the
	// roles go once it is back, with no administrator step.
	admin.assign(app, people["outage"])
	if _, ok := bookstackSignIn(t, ctx, transport, "outage", []byte("outage-Password-1")); !ok {
		t.Fatal("outage person was refused")
	}
	docker(t, "stop", "kyq-bookstack-directory-1")
	r, err := plain.Post("https://"+bookstackHosts["directory"]+":30443/kyq/directory", "application/scim+json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 503 {
		t.Errorf("the edge answered %d for a stopped receiver, want 503", r.StatusCode)
	}
	removed = time.Now()
	admin.change("DELETE", "/api/admin/app-registry/"+app+"/assignments/users/"+people["outage"], nil)
	time.Sleep(20 * time.Second) // KyIdentity's first delivery attempt meets the outage
	if _, roles := bookstackAccount(t, ctx, transport, service, people["outage"]); len(roles) == 0 {
		t.Fatal("the removal applied while the receiver was down")
	}
	docker(t, "start", "kyq-bookstack-directory-1")
	took = waitRoles(t, ctx, transport, service, people["outage"], removed)
	t.Logf("receiver outage: roles [] %s after the unassignment, the receiver down for its first 20 s", took.Round(time.Second))
	resync(t, ctx, connected)

	bookstackBackupRestore(t, ctx, bookstackOps{plan: plan, work: work, state: state, run: run, apply: apply, bs: bs, admin: admin, app: app,
		transport: transport, service: service, person: people["restored"], ids: ids, people: slices.Collect(maps.Values(people)), owners: [2][]byte{adminPW, everydayPW}, secrets: secrets})

	// AC5: no secret in any apply, backup or restore output, nor under the state directory.
	noSecrets(t, state, outputs, sweepSecrets(secrets))
	t.Logf("TestKubernetesBookStack took %s", time.Since(start).Round(time.Second))
}

// postLogin posts the standard sign-in form at base as the service account with password and
// reports whether BookStack then serves that browser an account page.
func postLogin(t *testing.T, ctx context.Context, transport http.RoundTripper, base, password string) bool {
	t.Helper()
	b := &bookstackBrowser{t: t, ctx: ctx, c: newJarClient(transport)}
	b.c.Timeout = 30 * time.Second
	if strings.HasPrefix(base, "http://") {
		b.c.Transport = &http.Transport{Proxy: nil}
	}
	b.do("GET", base+"/login", nil, "")
	b.do("POST", base+"/login", strings.NewReader(url.Values{"_token": {b.csrf}, "email": {"admin@admin.com"}, "password": {password}}.Encode()), "application/x-www-form-urlencoded")
	code, _, _ := b.do("GET", base+"/my-account/profile", nil, "")
	return code == 200
}

// bookstackSQL runs one read-only query in BookStack's MariaDB as its user. Test-only: the
// password reaches the client through its environment, never its command line.
func bookstackSQL(t *testing.T, query string) string {
	t.Helper()
	return docker(t, "exec", "kyq-bookstack-mariadb-1", "sh", "-c", `MYSQL_PWD=$(cat /run/secrets/db_password) exec mariadb -ubookstack -N -B bookstack -e "$1"`, "sh", query)
}

// readPage finds the page named name in book through the service token.
func readPage(t *testing.T, ctx context.Context, transport http.RoundTripper, token, book, name string) bookstackPage {
	t.Helper()
	var list struct{ Data []struct{ ID int } }
	bookstackAPI(t, ctx, transport, token, "GET", "pages?"+url.Values{"filter[name]": {name}}.Encode(), &list)
	if len(list.Data) != 1 {
		t.Fatalf("pages named %q: %v", name, list.Data)
	}
	var p bookstackPage
	if code := bookstackAPI(t, ctx, transport, token, "GET", fmt.Sprint("pages/", list.Data[0].ID), &p); code != 200 {
		t.Fatalf("page %q: %d", name, code)
	}
	return p
}

// waitRoles waits until the service token reads no role on the account of KyIdentity ID
// external, at most five minutes after since, and returns how long that took.
func waitRoles(t *testing.T, ctx context.Context, transport http.RoundTripper, token, external string, since time.Time) time.Duration {
	t.Helper()
	for {
		if _, roles := bookstackAccount(t, ctx, transport, token, external); len(roles) == 0 {
			return time.Since(since)
		}
		if time.Since(since) > 5*time.Minute {
			t.Fatalf("AC9: %s still holds roles five minutes after the removal", external)
		}
		time.Sleep(time.Second)
	}
}

// offboarded proves what a removed person keeps and loses: their token refused, their open
// session signed in but seeing and editing nothing, their own profile still writable, and
// their book and page unchanged with their authorship.
func offboarded(t *testing.T, ctx context.Context, transport http.RoundTripper, service string, b *bookstackBrowser, token, book, page, img string, before bookstackPage, id int) {
	t.Helper()
	if token != "" {
		if code := bookstackAPI(t, ctx, transport, token, "GET", "books", nil); code != 403 {
			t.Errorf("AC9: the removed person's token: %d, want 403", code)
		}
	}
	if !b.signedIn() {
		t.Error("AC9: the documented residual: the open session should stay signed in")
	}
	_, _, list := b.get("/books")
	if strings.Contains(list, "/books/"+book+`"`) {
		t.Error("AC9: the open session lists the book")
	}
	for _, p := range []string{"/books/" + book, page, page + "/edit", "/books/" + book + "/create-page", "/search?term=content"} {
		if code, _, body := b.get(p); code == 200 && (strings.Contains(body, before.Name) || strings.HasSuffix(p, "/edit")) {
			t.Errorf("AC9: the open session read %s", p)
		}
	}
	if img != "" {
		if code, _, _ := b.get(img); code == 200 {
			t.Error("AC9: the open session fetched the uploaded image")
		}
	}
	if code, _ := b.form(page, url.Values{"_method": {"PUT"}, "name": {"defaced"}, "html": {"<p>defaced</p>"}}); code == 302 {
		if after := readPage(t, ctx, transport, service, book, before.Name); after.HTML != before.HTML {
			t.Error("AC9: the open session edited the page")
		}
	}
	// The residual: their own name is still theirs to change.
	b.get("/my-account/profile")
	if code, _ := b.form("/my-account/profile", url.Values{"_method": {"PUT"}, "name": {"Renamed after offboarding"}}); code != 302 {
		t.Errorf("AC9: the documented residual: the profile name change answered %d", code)
	}
	var u struct{ Name string }
	bookstackAPI(t, ctx, transport, service, "GET", fmt.Sprint("users/", id), &u)
	if u.Name != "Renamed after offboarding" {
		t.Errorf("AC9: the documented residual: the name is %q", u.Name)
	}
	after := readPage(t, ctx, transport, service, book, before.Name)
	if after.HTML != before.HTML || after.CreatedBy.ID != id || after.OwnedBy.ID != id || before.CreatedBy.ID != id {
		t.Errorf("AC9: the page changed: %+v, was %+v (author %d)", after, before, id)
	}
}

// resync wants KyIdentity's resync of BookStack's connector delivered.
func resync(t *testing.T, ctx context.Context, connected *kube.Client) {
	t.Helper()
	ns, pod, container, err := connected.AppPod(ctx, "kyidentity")
	if err != nil {
		t.Fatal(err)
	}
	out, err := connected.Exec(ctx, ns, pod, container, []string{"/usr/local/bin/kyidentity", "resync-system", "--client", "bookstack", "--wait", "2m"})
	var r struct{ Status string }
	if err != nil || json.Unmarshal(out, &r) != nil || r.Status != "delivered" {
		t.Fatalf("resync-system --client bookstack: %v %s", err, out)
	}
}

type bookstackOps struct {
	plan, work, state, app, person, service string
	run                                     func(stdin string, args ...string) (string, error)
	apply                                   func(label string) string
	bs                                      bookstackTarget
	admin                                   *identityAdmin
	transport                               http.RoundTripper
	ids, people                             []string
	owners                                  [2][]byte
	secrets                                 map[string]string
}

var bookstackSnapshot = regexp.MustCompile(`(?m)^bookstack: snapshot ([0-9a-f]{64}) in `)

// bookstackBackupRestore is AC13: R's page and image are backed up, R is disabled, the
// target loses BookStack, and a restore brings it back with R's removal replayed.
func bookstackBackupRestore(t *testing.T, ctx context.Context, o bookstackOps) {
	t.Helper()
	o.admin.assign(o.app, o.person)
	r, ok := bookstackSignIn(t, ctx, o.transport, "restored", []byte("restored-Password-1"))
	if !ok {
		t.Fatal("AC13: R was refused")
	}
	code, book := r.createBook("R's book")
	if code != 200 {
		t.Fatalf("AC13: R's book: %d", code)
	}
	r.createPage(book, "Page Q", "<p>Q</p>")
	q := readPage(t, ctx, o.transport, o.service, book, "Page Q")
	r.uploadImage(q.ID)
	images := func() string {
		return docker(t, "exec", o.bs.name, "sh", "-c", `cd "$1" && find config/www/images/gallery -type f -exec sha256sum {} + | sort`, "sh", o.bs.root+"/bookstack")
	}
	beforeImages := images()
	if !strings.Contains(beforeImages, "pixel") && !strings.Contains(beforeImages, ".png") {
		t.Fatalf("AC13: no uploaded image on the target:\n%s", beforeImages)
	}

	srv := startRestServer(t, o.work, "nas")
	files := strings.TrimSuffix(o.bs.root, "/root") + "/backup-files"
	dest := srv.destination(t, o.bs.name, files, "nas")
	docker(t, "exec", o.bs.name, "chown", "-R", "kyq", files)
	st := mustStack(t, o.plan)
	for i := range st.Targets {
		if st.Targets[i].Name == "nas" {
			st.Targets[i].Backup = dest
		}
	}
	savePlan(t, o.plan, st)
	handover := o.apply("backup destination")
	nas := nasTarget(t, o.plan)
	checkBackupHandover(t, handover, "bookstack", nas)
	o.secrets["the rest-server password"] = srv.passwords["nas"]

	out, err := o.run("", "backup", "bookstack")
	m := bookstackSnapshot.FindStringSubmatch(out)
	if err != nil || m == nil {
		t.Fatalf("AC13: backup: %v\n%s", err, out)
	}
	id := m[1]
	password := handoverPassword(t, ctx, o.state, handover, nas)
	o.secrets["restic_password"] = password
	pw := o.bs.root + "/bookstack/secrets/restic_password"
	ls, err := restic(dest, "bookstack", pw, "ls", id)
	if err != nil {
		t.Fatalf("AC13: restic ls: %v\n%s", err, ls)
	}
	listed := strings.Split(ls, "\n")
	for _, want := range []string{"/kyq/dump", "/kyq/secrets/app_key", "/kyq/secrets/db_password", "/kyq/secrets/db_root_password", "/kyq/volumes/config/www/.env",
		"/kyq/volumes/data/directory_secret", "/kyq/volumes/token/bookstack_token"} {
		if !slices.Contains(listed, want) {
			t.Errorf("AC13: snapshot lacks %s", want)
		}
	}
	for _, l := range listed {
		if strings.HasPrefix(l, "/kyq/volumes/config/www/framework/sessions/") || strings.HasPrefix(l, "/kyq/volumes/mariadb") {
			t.Errorf("AC13: snapshot holds %s", l)
		}
	}

	// R is disabled after the snapshot, then the target loses BookStack.
	removed := time.Now()
	o.admin.change("PUT", "/api/admin/users/"+o.person, map[string]string{"status": "disabled"})
	waitRoles(t, ctx, o.transport, o.service, o.person, removed)
	docker(t, "compose", "-p", "kyq-bookstack", "down")
	docker(t, "exec", o.bs.name, "rm", "-rf", o.bs.root+"/bookstack")
	start := time.Now()
	out, err = o.run(password+"\n", "restore", "bookstack", "--snapshot", id, "--yes")
	if err != nil {
		logs, _ := exec.Command("docker", "logs", "kyq-bookstack-directory-1").CombinedOutput()
		ps, _ := exec.Command("docker", "ps", "-a", "--filter", "label=com.docker.compose.project=kyq-bookstack", "--format", "{{.Names}} {{.Status}} {{.Ports}}").CombinedOutput()
		for _, id := range o.people {
			var view json.RawMessage
			o.admin.get("/api/admin/users/"+id+"/offboarding", &view)
			t.Logf("KyIdentity offboarding view of %s: %s", id, view)
		}
		t.Fatalf("AC13: restore: %v\n%s\nreceiver:\n%s\ncontainers:\n%s", err, out, logs, ps)
	}
	for _, want := range []string{"bookstack: dump restored into mariadb.", "bookstack: KyIdentity replayed its bookstack connector's state, removals included, and delivered it (", "bookstack: restored from snapshot " + id} {
		if !strings.Contains(out, want) {
			t.Fatalf("AC13: restore output lacks %q", want)
		}
	}
	t.Logf("AC13: restore took %s", time.Since(start).Round(time.Second))
	if _, roles := bookstackAccount(t, ctx, o.transport, o.service, o.person); len(roles) != 0 {
		t.Errorf("AC13: R's roles after the restore: %v", roles)
	}
	if r.authorized() {
		t.Error("AC13: KyIdentity authorized the disabled R")
	}
	if after := readPage(t, ctx, o.transport, o.service, book, "Page Q"); after.HTML != q.HTML {
		t.Error("AC13: page Q changed")
	}
	// Viewing may add thumbnails; every file the snapshot held must come back unchanged.
	if after := images(); slices.ContainsFunc(strings.Split(beforeImages, "\n"), func(l string) bool { return !strings.Contains(after, l) }) {
		t.Errorf("AC13: images changed:\n%s\nwere:\n%s", after, beforeImages)
	}
	for i, n := range []string{"prepared-admin", "prepared-user"} {
		if _, ok := bookstackSignIn(t, ctx, o.transport, n, slices.Clone(o.owners[i])); !ok {
			t.Errorf("AC13: %s was refused after the restore", n)
		}
	}
	if _, roles := bookstackAccount(t, ctx, o.transport, o.service, o.admin.findUser("prepared-admin")); !slices.Equal(roles, []string{"Admin", "Editor"}) {
		t.Errorf("AC13: the owner admin's roles after the restore %v", roles)
	}
	out = o.apply("after restore")
	for _, id := range o.ids {
		if s := status(out, id); s != "skipped" {
			t.Errorf("AC13: rerun after restore %s = %q", id, s)
		}
	}
}
