//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"maps"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	catalogImmichHost    = "photos.example.test"
	catalogImmichURLHost = catalogImmichHost + ":30443"
	immichOrigin         = "https://" + catalogImmichURLHost
	immichSetupID        = "3f9d2c71-6a4e-4b8d-9e1f-5c7a0b2d4e68"
	immichBundle         = "/kyq-setup/installer/" + immichSetupID + "/setup.json"
	immichProject        = "kyq-immich"
	// registryImage is the local registry the Docker target pulls the two locally built images
	// from (test plumbing; see immichImages), docker.io/library/registry:2 by digest.
	registryImage = "registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"
)

var immichServices = []string{"immich-server", "immich-machine-learning", "database", "redis", "directory"}

var immichRouteStep = func() string {
	sum := sha256.Sum256([]byte(catalogImmichHost))
	return "k8s.edge." + hex.EncodeToString(sum[:8])
}()

// TestKubernetesImmich installs the catalog Immich on a Docker target behind its own kind
// cluster's lan edge, with NPM and KyIdentity 0.4.4 there and 008's rest-server as its backup
// destination, and proves the job's gates: deployment and hardening, owner and everyday
// rights, a later account with phone sign-in, role changes, offboarding and reactivation,
// backup, the restore drill without readmission, reruns, secrets and the handover.
//
// ON HOLD (H1): the catalog's server and bridge pins are sha256:pending until Yoshi publishes
// them. Until then KYQ_TEST_IMMICH_SERVER_IMAGE names a local build of the module image
// (internal/adapters/immich/module/README.md) and the bridge is built from this checkout.
func TestKubernetesImmich(t *testing.T) {
	serverImage := os.Getenv("KYQ_TEST_IMMICH_SERVER_IMAGE")
	if serverImage == "" {
		t.Skip("Immich's catalog images are unpublished (H1): set KYQ_TEST_IMMICH_SERVER_IMAGE to a local build of the module image")
	}
	began := time.Now()
	ctx := context.Background()
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
	server, bridge := immichImages(t, serverImage)

	plan := filepath.Join(state, "stack.yaml")
	os.WriteFile(plan, []byte("version: 1\ntargets:\n  - name: k8s\n    kubeconfig: "+kc+"\n    storage_class: standard\napps:\n  - name: npm\n    target: k8s\n"), 0o600)
	var outputs, trust []string
	apply := func(label string) string {
		t.Helper()
		out, err := kyq(t, ctx, state, catalogDir, trust, "", "apply")
		outputs = append(outputs, out)
		if err != nil {
			t.Fatalf("apply (%s): %v", label, err)
		}
		return out
	}
	if out := apply("npm"); status(out, "npm.bootstrap") != "ok" {
		t.Fatal("npm was not bootstrapped")
	}
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
	lan := name + "-client"
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", lan).Run() })
	docker(t, "run", "-d", "--name", lan, "--network", "kind", "--memory", "64m", "--entrypoint", "sleep", kindNode, "7200")
	curl := func(args ...string) string {
		t.Helper()
		return docker(t, append([]string{"exec", lan, "curl", "-sk", "-m", "10", "--resolve", catalogImmichURLHost + ":" + nodeIP}, args...)...)
	}

	target := startImmichTarget(t, work, catalogDir, nodeIP, certPEM, server, bridge)
	st, err := stack.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	allow := []string{docker(t, "inspect", lan, "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}") + "/32", mustNode(t, ctx, cs, node).Spec.PodCIDR, target.forward + "/32", target.relayIP(t) + "/32"}
	st.Edges = []stack.Edge{{Target: "k8s", Provider: "lan", NPMURL: "kube://kyq-npm/app:81", AllowCIDRs: allow, ClientPort: 30443}}
	addCatalogIdentity(st, certID)
	savePlan(t, plan, st)
	testClusterDNS(t, ctx, cs, nodeIP)
	out := apply("identity")
	for _, id := range []string{"kyidentity.deploy", "kyidentity.health", "kyidentity.setup"} {
		if status(out, id) != "ok" {
			t.Fatalf("%s = %q", id, status(out, id))
		}
	}
	if ns, err := cs.CoreV1().Namespaces().Get(ctx, "kyq-kyidentity", metav1.GetOptions{}); err != nil || ns.Labels[managed.VersionLabel] != "0.4.4" {
		t.Fatalf("AC9: KyIdentity's deployment record: %v %v", ns.Labels, err)
	}
	t.Log("AC9: the first apply installed npm and KyIdentity 0.4.4")
	trustTestCA(t, ctx, cs, connected, "kyidentity", certPEM)

	// AC9: the second apply adds Immich, its route, setup, bridge and connector.
	st, _ = stack.Load(plan)
	st.Targets = append(st.Targets, stack.Target{Name: "nas", Host: "127.0.0.1", Port: target.port, User: "kyq", Root: target.root})
	st.Apps = append(st.Apps, stack.App{Name: "immich", Target: "nas"})
	st.Edges[0].Routes = append(st.Edges[0].Routes, stack.Route{App: "immich", Hostname: catalogImmichHost, ForwardHost: target.forward, ForwardPort: target.forwardPort, CertificateID: certID, StatusPath: "/api/server/ping"})
	st.Integrations = append(st.Integrations, stack.Integration{Product: "immich", Target: "nas", Bundle: immichBundle, IdentityClient: "immich"})
	srv := startRestServer(t, work, "nas")
	dest := srv.destination(t, target.name, target.base+"/backup-files", "nas")
	docker(t, "exec", target.name, "chown", "-R", target.owner, target.base+"/backup-files")
	st.Targets[len(st.Targets)-1].Backup = dest
	savePlan(t, plan, st)
	trust = []string{"--trust-host-key", "nas=" + target.fp}
	cold := map[string]bool{}
	for _, ref := range target.pins {
		cold[ref] = exec.Command("docker", "image", "inspect", ref).Run() != nil
	}
	start := time.Now()
	out = apply("immich")
	t.Logf("TIMING immich apply (deploy, image pull, migrations, setup, bridge, connector): %s; images absent before: %v", time.Since(start).Round(time.Second), cold)
	for _, id := range []string{"kyidentity.amend.immich", "immich.secrets", "immich.deploy", "immich.health", "immich.setup", "immich.directory", immichRouteStep, "kyidentity.scim.immich"} {
		if status(out, id) != "ok" {
			t.Fatalf("AC9: %s = %q", id, status(out, id))
		}
	}
	t.Log("AC9: kyidentity.amend.immich, immich.secrets, immich.deploy, immich.health, immich.setup, immich.directory, " + immichRouteStep + " and kyidentity.scim.immich ok")
	nas := nasTarget(t, plan)
	app := target.root + "/immich"
	containers := checkImmichContainers(t, target)
	secrets := immichSecrets(t, target)
	checkImmichHardening(t, target, containers, secrets)

	// AC9 through the edge, from a LAN client on kind.
	if b := curl(immichOrigin + "/api/server/ping"); b != `{"res":"pong"}` {
		t.Fatalf("AC9: ping through the edge: %s", b)
	}
	var version struct{ Major, Minor, Patch int }
	if b := curl(immichOrigin + "/api/server/version"); json.Unmarshal([]byte(b), &version) != nil || version != (struct{ Major, Minor, Patch int }{3, 2, 0}) {
		t.Fatalf("AC9: version through the edge: %s", b)
	}
	key := make([]byte, 16)
	rand.Read(key)
	upgrade := docker(t, "exec", lan, "sh", "-c", `curl -sk --http1.1 -m 5 -o /dev/null -w '%{http_code}' --resolve "$1" -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H "Sec-WebSocket-Key: $2" "$3"; true`,
		"sh", catalogImmichURLHost+":"+nodeIP, base64.StdEncoding.EncodeToString(key), immichOrigin+"/api/socket.io/?EIO=4&transport=websocket")
	if upgrade != "101" {
		t.Fatalf("AC9: socket.io upgrade through the edge: %q", upgrade)
	}
	for _, auth := range []string{"", "Authorization: Bearer " + randomPassword()} {
		args := []string{"-o", "/dev/null", "-w", "%{http_code}"}
		if auth != "" {
			args = append(args, "-H", auth)
		}
		if code := curl(append(args, immichOrigin+"/scim/immich/Users")...); code != "401" {
			t.Fatalf("AC9: bridge request with %q: %s", auth, code)
		}
	}
	t.Log("AC9: through the edge ping pong, version 3.2.0, socket.io 101, bridge 401 without and with a wrong token")

	// AC20: the handover, line by line, and 008's backup lines.
	checkImmichHandover(t, out, target)
	checkBackupHandover(t, out, "immich", nas)

	tr := immichTransport(t, certPEM, nodeIP)
	im := &immichAPI{t: t, ctx: ctx, tr: tr}
	adminPW := string(ownerPassword(t, ctx, connected, "admin.password"))
	userPW := string(ownerPassword(t, ctx, connected, "everyday.password"))
	idAdmin := newIdentityAdmin(t, ctx, tr, "prepared-admin", []byte(adminPW))
	secrets["prepared-admin's password"], secrets["prepared-user's password"] = adminPW, userPW

	// AC10: the owner's administrator administers Immich, on two fresh sign-ins; the everyday
	// account does not.
	admin := im.login("prepared-admin", adminPW)
	if !admin.me.IsAdmin || admin.me.Email != "prepared-admin@example.test" {
		t.Fatalf("AC10: prepared-admin is %+v", admin.me)
	}
	setupUsers := im.adminUsers(admin)
	if len(setupUsers) != 3 {
		t.Fatalf("AC10: admin API lists %d accounts: %+v", len(setupUsers), setupUsers)
	}
	ids := map[string]string{}
	for _, u := range setupUsers {
		switch {
		case u.Email == "prepared-admin@example.test" && u.IsAdmin && u.ID == admin.me.ID:
			ids["owner"] = u.ID
		case u.Email == "prepared-user@example.test" && !u.IsAdmin:
			ids["everyday"] = u.ID
		case u.Name == "kyquickstart-service" && u.IsAdmin && u.OAuthID == "":
			ids["service"] = u.ID
		}
	}
	if len(ids) != 3 {
		t.Fatalf("AC10: the admin API lists %+v, not the owner, everyday and service accounts", setupUsers)
	}
	admin2 := im.login("prepared-admin", adminPW)
	if !admin2.me.IsAdmin || admin2.me.ID != ids["owner"] || im.status(admin2, "GET", "/api/admin/users") != 200 {
		t.Fatalf("AC10: second sign-in of prepared-admin: %+v", admin2.me)
	}
	var everyday *immichLogin
	for i := range 2 {
		everyday = im.login("prepared-user", userPW)
		if everyday.me.IsAdmin || everyday.me.ID != ids["everyday"] || im.status(everyday, "GET", "/api/admin/users") != 403 {
			t.Fatalf("AC10: sign-in %d of prepared-user: %+v", i+1, everyday.me)
		}
	}
	t.Logf("AC10: prepared-admin %s isAdmin true, admin API 200 listing exactly owner/everyday/service, on two fresh sign-ins; prepared-user %s isAdmin false, admin API 403 twice", ids["owner"], ids["everyday"])

	// AC15: no takeover through the edge.
	if code, _ := im.anon("POST", "/api/auth/admin-sign-up", `{"email":"taker@example.test","password":"`+randomPassword()+`","name":"Taker"}`); code/100 != 4 {
		t.Fatalf("AC15: admin-sign-up: %d", code)
	}
	serviceEmail := ""
	for _, u := range setupUsers {
		if u.ID == ids["service"] {
			serviceEmail = u.Email
		}
	}
	for _, email := range []string{"prepared-admin@example.test", "prepared-user@example.test", serviceEmail} {
		for _, pw := range []string{adminPW, userPW, randomPassword()} {
			if code, _ := im.anon("POST", "/api/auth/login", `{"email":"`+email+`","password":"`+pw+`"}`); code != 401 {
				t.Fatalf("AC15: password login of %s: %d", email, code)
			}
		}
	}
	t.Logf("AC15: admin-sign-up 4xx; password login 401 for the owner, everyday and service (%s, no OAuth subject) accounts", serviceEmail)

	// AC20: the source offer.
	var about struct {
		Repository, RepositoryURL, SourceRef, SourceURL, BuildImage string
	}
	if code, b := im.call(admin, "GET", "/api/server/about", nil, ""); code != 200 || json.Unmarshal(b, &about) != nil {
		t.Fatalf("AC20: about: %d %s", code, b)
	}
	imageTag := docker(t, "image", "inspect", server, "--format", `{{range .Config.Env}}{{println .}}{{end}}`)
	m := regexp.MustCompile(`(?m)^IMMICH_BUILD_IMAGE=(3\.2\.0-ky\.[a-z0-9]+)$`).FindStringSubmatch(imageTag)
	if m == nil || about.Repository != "Busnes-app/KyQuickStart" || about.RepositoryURL != "https://github.com/Busnes-app/KyQuickStart" || about.BuildImage != m[1] || !strings.HasPrefix(about.SourceURL, "https://github.com/Busnes-app/KyQuickStart/tree/immich-v3.2.0-ky.") {
		t.Fatalf("AC20: about %+v, image build %v", about, m)
	}
	t.Logf("AC20: /api/server/about names %s, image %s, source %s", about.Repository, about.BuildImage, about.SourceURL)

	// AC12: the everyday account's main job, and what an admin upload does (H2).
	p1 := testJPEG(1)
	p1ID := im.upload(everyday, "P1.jpg", p1, 201)
	im.waitThumbnail(everyday, p1ID, 3*time.Minute)
	if !im.inTimeline(everyday, p1ID) {
		t.Fatal("AC12: P1 is not in prepared-user's timeline")
	}
	code, _ := im.uploadStatus(admin, "admin.jpg", testJPEG(9))
	t.Logf("ADMIN-UPLOAD %d", code)
	ping := docker(t, "exec", containers["immich-server"], "node", "-e", `fetch("http://immich-machine-learning:3003/ping").then(r=>r.text()).then(t=>process.stdout.write(t))`)
	if ping != "pong" {
		t.Fatalf("AC12: machine learning ping from immich-server: %q", ping)
	}
	start = time.Now()
	im.waitSmartSearch(everyday, p1ID, 20*time.Minute)
	t.Logf("TIMING smart search found P1 (cold model download included): %s", time.Since(start).Round(time.Second))
	t.Logf("AC12: P1 %s uploaded 201, thumbnail 200 image/*, in the timeline; ML ping pong; smart search finds P1", p1ID)

	// AC13: a later account, through KyIdentity only.
	appID := idAdmin.app("immich")
	lPW := randomPassword()
	secrets["L's password"] = lPW
	leaver := idAdmin.createUser("leaver", lPW)
	idAdmin.assign(appID, leaver)
	start = time.Now()
	lWeb := im.waitLogin("leaver", lPW)
	t.Logf("TIMING SCIM delivery of a later assignment, to its first sign-in: %s", time.Since(start).Round(time.Second))
	if lWeb.me.IsAdmin || lWeb.me.Email != "leaver@example.test" || ids["owner"] == lWeb.me.ID || ids["everyday"] == lWeb.me.ID || ids["service"] == lWeb.me.ID {
		t.Fatalf("AC13: L is %+v", lWeb.me)
	}
	ids["L"] = lWeb.me.ID
	p2 := testJPEG(2)
	p2ID := im.upload(lWeb, "P2.jpg", p2, 201)
	var created struct{ Secret string }
	if code, b := im.call(lWeb, "POST", "/api/api-keys", []byte(`{"name":"e2e","permissions":["all"]}`), "application/json"); code != 201 || json.Unmarshal(b, &created) != nil || created.Secret == "" {
		t.Fatalf("AC13: L's API key: %d %s", code, b)
	}
	keyK := created.Secret
	secrets["K"] = keyK
	if me := im.meWith("x-api-key", keyK); me.ID != ids["L"] {
		t.Fatalf("AC13: K is %+v", me)
	}
	socket := im.socket(lWeb.cookie())
	if !socket.authenticated {
		t.Fatal("AC13: L's authenticated socket was refused")
	}
	phone := im.phoneLogin("leaver", lPW)
	secrets["L's phone token"] = phone
	if me := im.meWith("Authorization", "Bearer "+phone); me.ID != ids["L"] {
		t.Fatalf("AC13: phone token is %+v", me)
	}
	secrets["L's web session"] = lWeb.token
	t.Logf("AC13: L %s signed in on the web (isAdmin false), uploaded P2 %s, K works, socket authenticated, phone flow 307 -> callback -> accessToken -> /api/users/me", ids["L"], p2ID)

	// AC18 before the disaster: every Immich step skipped, the same containers.
	checkImmichRerun(t, apply("rerun before the disaster"), containers)

	// AC16: backup after AC13, so L and P2 are in it.
	receipts := immichReceipts(t, containers["database"])
	start = time.Now()
	w := watchRestic()
	backupOut, err := kyq(t, ctx, state, catalogDir, trust, "", "backup", "immich")
	inspect, _ := w.end(t)
	outputs = append(outputs, backupOut)
	t.Logf("TIMING kyquickstart backup immich: %s", time.Since(start).Round(time.Second))
	sm := regexp.MustCompile(`(?m)^immich: snapshot ([0-9a-f]{64}) in `).FindStringSubmatch(backupOut)
	if err != nil || sm == nil || strings.Contains(backupOut, "backup_unsupported") {
		t.Fatalf("AC16: backup immich: %v", err)
	}
	snapshot := sm[1]
	pwFile := app + "/secrets/restic_password"
	secrets["restic_password"] = docker(t, "exec", target.name, "cat", pwFile)
	secrets["the rest-server password"] = srv.passwords["nas"]
	checkResticInspect(t, inspect, secrets)
	ls, err := restic(dest, "immich", pwFile, "ls", snapshot)
	if err != nil {
		t.Fatalf("AC16: restic ls: %v\n%s", err, ls)
	}
	lines := "\n" + ls + "\n"
	for _, p := range []string{"/kyq/dump", "/kyq/volumes/library", "/kyq/volumes/library/upload", "/kyq/volumes/setup", "/kyq/volumes/setup/service.key", "/kyq/volumes/directory", "/kyq/volumes/directory/config/runtime.json", "/kyq/secrets/db_password", "/kyq/secrets/scim_token"} {
		if !strings.Contains(lines, "\n"+p+"\n") {
			t.Fatalf("AC16: snapshot lacks %s:\n%s", p, ls)
		}
	}
	for _, p := range []string{"/kyq/volumes/library/thumbs", "/kyq/volumes/library/encoded-video", "/kyq/volumes/library/backups", "model-cache", "/kyq/volumes/postgres", "restic_password"} {
		if strings.Contains(ls, p) {
			t.Fatalf("AC16: snapshot holds %s", p)
		}
	}
	if out, err := restic(dest, "immich", pwFile, "check"); err != nil {
		t.Fatalf("AC16: restic check: %v\n%s", err, out)
	}
	t.Logf("AC16: snapshot %s holds the dump, library, setup and directory, not thumbs, encoded-video or backups; restic check passed", snapshot)

	// AC11: a role change reaches Immich through the bridge before the next sign-in.
	var roles struct{ Roles []struct{ ID, Name string } }
	idAdmin.get("/api/admin/app-registry/"+appID+"/roles", &roles)
	adminRole := ""
	for _, r := range roles.Roles {
		if r.Name == "admin" {
			adminRole = r.ID
		}
	}
	if adminRole == "" {
		t.Fatalf("AC11: Immich's app has no admin role: %+v", roles)
	}
	rolePath := "/api/admin/app-registry/" + appID + "/roles/" + adminRole + "/assignments/users/" + leaver
	idAdmin.change("PUT", rolePath, nil)
	im.waitAdmin(admin, ids["L"], true)
	lAdmin := im.login("leaver", lPW)
	if !lAdmin.me.IsAdmin || im.status(lAdmin, "GET", "/api/admin/users") != 200 {
		t.Fatalf("AC11: L after the grant: %+v", lAdmin.me)
	}
	idAdmin.change("DELETE", rolePath, nil)
	im.waitAdmin(admin, ids["L"], false)
	lUser := im.login("leaver", lPW)
	if lUser.me.IsAdmin || im.status(lUser, "GET", "/api/admin/users") != 403 {
		t.Fatalf("AC11: L after the revocation: %+v", lUser.me)
	}
	if code := im.status(lAdmin, "GET", "/api/admin/users"); code != 403 {
		t.Fatalf("AC11: L's administrator session after the demotion: %d", code)
	}
	t.Log("AC11: granted admin -> next sign-in isAdmin true, admin API 200; revoked -> next sign-in isAdmin false, 403, and the earlier admin session 403; no sign-in refused; single role holders restored")

	// AC14.1: unassigning L ends every credential and keeps the account and P2.
	jobs := bridgeJobs{t: t, dir: app + "/directory/state"}
	lBinding := jobs.binding(ids["L"])
	old := []immichCredential{{"L's web session", "cookie", lWeb.cookie()}, {"L's later web session", "cookie", lUser.cookie()}, {"L's former administrator session", "cookie", lAdmin.cookie()},
		{"L's phone token", "Authorization", "Bearer " + phone}, {"K", "x-api-key", keyK}}
	idAdmin.change("DELETE", "/api/admin/app-registry/"+appID+"/assignments/users/"+leaver, nil)
	start = time.Now()
	jobs.wait(lBinding, 1, "complete")
	t.Logf("TIMING unassignment to completed revocation in the bridge: %s", time.Since(start).Round(time.Second))
	im.denied(old, "after the unassignment")
	if !socket.waitClosed(30 * time.Second) {
		t.Fatal("AC14: L's socket stayed connected after the unassignment")
	}
	if where := im.loginRefused("leaver", lPW); where == "" {
		t.Fatal("AC14: L signed in after the unassignment")
	}
	im.retained(admin, ids["L"], 1)
	t.Log("AC14.1: unassigned: L's web sessions, phone token and K 401, socket disconnected, new sign-in refused, bridge job complete, account and P2 retained")

	// AC14.2: re-assignment reactivates the retained account; no old credential comes back.
	idAdmin.assign(appID, leaver)
	start = time.Now()
	outcome := jobs.waitSettled(lBinding, 2)
	t.Logf("TIMING re-assignment to settled reactivation epoch in the bridge: %s (%s)", time.Since(start).Round(time.Second), outcome)
	old = append(old, immichCredential{"L's socket session", "socket", lWeb.cookie()})
	if outcome == "complete" {
		lBack := im.login("leaver", lPW)
		if lBack.me.ID != ids["L"] || lBack.me.IsAdmin {
			t.Fatalf("AC14: L after reactivation: %+v", lBack.me)
		}
		if got := im.original(lBack, p2ID); !bytes.Equal(got, p2) {
			t.Fatal("AC14: P2 differs after reactivation")
		}
		im.denied(old, "after the reactivation")
		t.Logf("AC14.2: reactivation epoch 2 audited %s; L signed in fresh on %s, isAdmin false, P2 sha256 %s identical; every pre-revocation credential still denied", jobs.audit(lBinding, 2), ids["L"], shaHex(p2))
	} else {
		t.Fatalf("AC14.2: the bridge refused reactivation (%s, audited %s): the fallback (Work 2c) is not built", outcome, jobs.audit(lBinding, 2))
	}

	// AC14.3: disabling prepared-user globally, then enabling it again.
	everydayBinding := jobs.binding(ids["everyday"])
	before := []immichCredential{{"prepared-user's session", "cookie", everyday.cookie()}}
	everydayID := idAdmin.findUser("prepared-user")
	idAdmin.change("PUT", "/api/admin/users/"+everydayID, map[string]string{"status": "disabled"})
	jobs.wait(everydayBinding, 1, "complete")
	im.denied(before, "after prepared-user was disabled")
	idAdmin.change("PUT", "/api/admin/users/"+everydayID, map[string]string{"status": "active"})
	if outcome := jobs.waitSettled(everydayBinding, 2); outcome != "complete" {
		t.Fatalf("AC14.3: prepared-user's reactivation: %s", outcome)
	}
	everyday = im.login("prepared-user", userPW)
	if everyday.me.ID != ids["everyday"] || everyday.me.IsAdmin {
		t.Fatalf("AC14.3: prepared-user after reactivation: %+v", everyday.me)
	}
	if got := im.original(everyday, p1ID); !bytes.Equal(got, p1) {
		t.Fatal("AC14.3: P1 differs after reactivation")
	}
	im.denied(before, "after prepared-user's reactivation")
	t.Log("AC14.3: prepared-user disabled -> session denied; enabled -> reactivated on the same UUID, P1 intact, the old session still denied")

	// AC17.3: L's state is fixed before the disaster: unassigned, so the restored L (active in
	// the snapshot) must be denied.
	idAdmin.change("DELETE", "/api/admin/app-registry/"+appID+"/assignments/users/"+leaver, nil)
	jobs.wait(lBinding, 3, "complete")

	// AC17: the disaster and the restore.
	password := handoverPassword(t, ctx, state, out, nas)
	disaster := func() {
		t.Helper()
		docker(t, "compose", "-p", immichProject, "down")
		docker(t, "exec", target.name, "rm", "-rf", app)
	}
	paths := "/kyq/dump /kyq/secrets/db_password /kyq/secrets/scim_token /kyq/volumes/directory /kyq/volumes/library /kyq/volumes/setup"
	restore := func(label, stdin string, extra ...string) string {
		t.Helper()
		watch := im.watchKey(keyK)
		start := time.Now()
		o, err := kyq(t, ctx, state, catalogDir, trust, stdin, append([]string{"restore", "immich", "--snapshot", snapshot}, extra...)...)
		outputs = append(outputs, o)
		first, denied := watch()
		if err != nil {
			t.Fatalf("AC17: restore (%s): %v", label, err)
		}
		t.Logf("TIMING kyquickstart restore immich (%s): %s", label, time.Since(start).Round(time.Second))
		if first.IsZero() {
			t.Logf("TIMING readmission window (%s): K was never accepted after the restore", label)
		} else {
			t.Logf("TIMING readmission window (%s): K accepted for %s, until the replayed removal", label, denied.Sub(first).Round(10*time.Millisecond))
		}
		for _, want := range []string{"Restore immich from snapshot ", " on nas\n  paths " + paths + "\n", "immich: KyIdentity replayed its immich connector's state, removals included, and delivered it (", "immich: restored from snapshot ", "\nimmich: thumbnails were not backed up; Immich makes them again at its nightly task, or at once from Administration → Jobs → Generate Thumbnails.\n"} {
			if !strings.Contains(o, want) {
				t.Fatalf("AC17: restore (%s) output lacks %q", label, want)
			}
		}
		return o
	}
	// want is the receipts the restore must bring back: the snapshot's, as KyIdentity's replay
	// converges them (the setup and creation receipts are immutable).
	restored := func(label, want string) {
		t.Helper()
		admin = im.login("prepared-admin", adminPW)
		if im.status(admin, "GET", "/api/admin/users") != 200 {
			t.Fatalf("AC17 (%s): prepared-admin's admin API", label)
		}
		everyday = im.login("prepared-user", userPW)
		if im.status(everyday, "GET", "/api/admin/users") != 403 {
			t.Fatalf("AC17 (%s): prepared-user's admin API", label)
		}
		got := map[string]bool{}
		for _, u := range im.adminUsers(admin) {
			got[u.ID] = true
		}
		for who, id := range ids {
			if !got[id] {
				t.Fatalf("AC17 (%s): %s's account %s did not come back", label, who, id)
			}
		}
		if after := immichReceipts(t, immichContainer(t, "database")); after != want {
			t.Fatalf("AC17 (%s): receipts differ:\n%s\nwant:\n%s", label, after, want)
		}
		if got := im.original(everyday, p1ID); !bytes.Equal(got, p1) {
			t.Fatalf("AC17 (%s): P1 differs", label)
		}
		if got := immichOriginal(t, immichContainer(t, "database"), app, p2ID); !bytes.Equal(got, p2) {
			t.Fatalf("AC17 (%s): P2 differs on disk", label)
		}
		im.regenerateThumbnails(admin)
		im.waitThumbnail(everyday, p1ID, 5*time.Minute)
		im.denied(append(old, immichCredential{"K", "x-api-key", keyK}), label)
		if where := im.loginRefused("leaver", lPW); where == "" {
			t.Fatalf("AC17 (%s): L signed in after the restore", label)
		}
		// The bridge serves a KyIdentity change after the restore.
		display := "Everyday " + label
		idAdmin.change("PUT", "/api/admin/users/"+everydayID, map[string]string{"displayName": display})
		im.waitName(admin, ids["everyday"], display)
		checkImmichRerun(t, apply(label+" rerun"), immichContainerIDs(t))
		t.Logf("AC17 (%s): UUIDs %v back, receipts byte-identical, P1 %s and P2 %s sha256-identical, thumbnails regenerated, admin 200 / everyday 403, L and K denied, the bridge delivered a name change", label, ids, shaHex(p1), shaHex(p2))
	}
	disaster()
	o := restore("disaster", password+"\nimmich\n")
	if !strings.Contains(o, "Type immich to restore it: ") {
		t.Fatal("AC17: restore did not ask for the typed name")
	}
	// The snapshot predates AC11 and AC14; the replay changes no profile it holds.
	restored("disaster", receipts)

	// AC17.4: stopped, without --replace: refused, nothing changed; with it, restored. The
	// replay brings back the name KyIdentity gave prepared-user after the snapshot.
	preDisaster := immichReceipts(t, immichContainer(t, "database"))
	docker(t, "compose", "-p", immichProject, "stop")
	listing := dirDigest(t, target.name, app)
	o, err = kyq(t, ctx, state, catalogDir, trust, "", "restore", "immich", "--snapshot", snapshot, "--yes")
	outputs = append(outputs, o)
	refused(t, err, "rerun with --replace to move them aside")
	if after := dirDigest(t, target.name, app); after != listing {
		t.Fatalf("AC17: a refused restore changed Immich's directory:\n%s\nwas:\n%s", after, listing)
	}
	o = restore("replace", "immich\n", "--replace")
	if m := regexp.MustCompile(`Moved the current data and secrets aside to (\S+)\.\n`).FindStringSubmatch(o); m == nil || !strings.HasPrefix(m[1], app+"/.replaced-") {
		t.Fatal("AC17: --replace did not print where the previous data went")
	}
	restored("replace", preDisaster)

	// AC19: no secret in any output or state file; every secret file is private.
	noSecrets(t, state, outputs, secrets)
	modes := docker(t, "exec", target.name, "sh", "-c", `cd "$1" && stat -c '%n %a' secrets/db_password secrets/scim_token secrets/restic_password setup/service.key setup/installer/*/setup.json directory/config/immich-token directory/config/scim-token directory/config/tls.key`, "sh", app)
	for _, l := range strings.Split(modes, "\n") {
		if !strings.HasSuffix(l, " 600") {
			t.Fatalf("AC19: secret file modes:\n%s", modes)
		}
	}
	t.Logf("AC19: none of %d secrets in %d outputs or the state directory; secret files 600:\n%s", len(secrets), len(outputs), modes)
	t.Logf("TIMING TestKubernetesImmich: %s", time.Since(began).Round(time.Second))
}

// immichImages starts a local registry and pushes the module server image and a bridge built
// from this checkout to it, returning their digest references. Test plumbing: Docker's
// compose pull cannot pull a local image ID, and a registry keeps every reference a real
// repository digest the deployed containers' RepoDigests must match. The tags it pushes are
// removed at cleanup; the bridge image stays tagged kyq-e2e-kyoffboard for reuse.
func immichImages(t *testing.T, serverImage string) (string, string) {
	t.Helper()
	id := make([]byte, 4)
	rand.Read(id)
	name := "kyq-e2e-registry-" + hex.EncodeToString(id)
	t.Cleanup(func() { exec.Command("docker", "rm", "-fv", name).Run() })
	docker(t, "run", "-d", "--name", name, "--memory", "256m", "-p", "127.0.0.1::5000", registryImage)
	_, port, _ := net.SplitHostPort(docker(t, "port", name, "5000"))
	reg := "127.0.0.1:" + port
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		r, err := http.Get("http://" + reg + "/v2/")
		if err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("registry at %s: %v", reg, err)
		}
	}
	start := time.Now()
	docker(t, "build", "-q", "--build-arg", "VERSION=e2e", "-f", "../../cmd/kyoffboard/Dockerfile", "-t", "kyq-e2e-kyoffboard", "../..")
	t.Logf("TIMING kyoffboard image build: %s", time.Since(start).Round(time.Second))
	push := func(local, repo string) string {
		ref := reg + "/" + repo
		docker(t, "tag", local, ref)
		start := time.Now()
		out := docker(t, "push", ref)
		m := regexp.MustCompile(`digest: (sha256:[0-9a-f]{64})`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("push %s: %s", ref, out)
		}
		pinned := ref + "@" + m[1]
		repoName, _, _ := strings.Cut(repo, ":")
		t.Cleanup(func() { exec.Command("docker", "image", "rm", ref, reg+"/"+repoName+"@"+m[1]).Run() })
		t.Logf("TIMING push %s to the local registry: %s", local, time.Since(start).Round(time.Second))
		return pinned
	}
	return push(serverImage, "kyq-immich-server:3.2.0-ky.1"), push("kyq-e2e-kyoffboard", "kyoffboard:0.1.0")
}

// immichTarget is the Docker target Immich runs on: an sshd container on the host's Docker
// socket, signed in to as a non-root user (the test's own uid, in the socket's group), with
// its root on disk in the test's work directory, which the host and the container share.
type immichTarget struct {
	name, fp, base, root, owner string
	port                        int
	forward                     string // the kind network's IPv4 gateway
	forwardPort                 int    // Immich's; the bridge's is the next
	relay                       string
	pins                        []string // the five images the compose file names
}

func (d immichTarget) relayIP(t *testing.T) string {
	t.Helper()
	return docker(t, "inspect", d.relay, "--format", `{{(index .NetworkSettings.Networks "kind").IPAddress}}`)
}

// startImmichTarget starts the target and the relay to KyIdentity's edge (as
// startDriveTarget does), and copies the pending Immich entry into catalogDir with the two
// local images and test plumbing: identity.example.test is the relay, and the server trusts
// the edge's throwaway certificate. Every edit fails on drift.
func startImmichTarget(t *testing.T, work, catalogDir, nodeIP string, certPEM []byte, server, bridge string) immichTarget {
	t.Helper()
	keys := filepath.Join(work, "immich-keys")
	os.Mkdir(keys, 0o700)
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(hostPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(keys, "host_ed25519"), pem.EncodeToMemory(block), 0o600)
	hostSigner, _ := ssh.NewSignerFromKey(hostPriv)
	_, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	clientSigner, _ := ssh.NewSignerFromKey(clientPriv)
	os.WriteFile(filepath.Join(keys, "authorized_keys"), ssh.MarshalAuthorizedKey(clientSigner.PublicKey()), 0o644)
	serveAgent(t, work, clientPriv)

	id := make([]byte, 4)
	rand.Read(id)
	name := "kyq-e2e-immich-" + hex.EncodeToString(id)
	docker(t, "build", "-q", "-t", "kyq-e2e-sshd", "sshd")
	// On disk, not /tmp: tmpfs pages count against the writing container's memory limit.
	base := filepath.Join(work, "target")
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	// Root-owned files (restic, the sshd container) are removed before the work directory.
	t.Cleanup(func() {
		exec.Command("docker", "run", "--rm", "--memory", "64m", "--memory-swap", "64m", "--cpus", "1", "--pids-limit", "32", "-v", work+":"+work, "--entrypoint", "rm", "kyq-e2e-sshd", "-rf", base).Run()
	})
	docker(t, "run", "-d", "--name", name, "--memory", "256m", "--memory-swap", "256m", "--cpus", "1", "--pids-limit", "128", "-p", "127.0.0.1::22",
		"-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", keys+":/keys:ro", "-v", work+":"+work, "kyq-e2e-sshd")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	t.Cleanup(func() {
		if t.Failed() {
			for _, s := range immichServices {
				logs, _ := exec.Command("docker", "logs", "--tail", "200", immichProject+"-"+s+"-1").CombinedOutput()
				t.Logf("%s logs:\n%s", s, logs)
			}
		}
		exec.Command("docker", "compose", "-p", immichProject, "down", "-v").Run()
	})
	// The target user: the test's uid and gid, in the Docker socket's group, signed in to by key.
	docker(t, "exec", name, "sh", "-ec", `uid=$1 gid=$2 sock=$(stat -c %g /var/run/docker.sock)
getent group "$gid" >/dev/null || addgroup -g "$gid" kyq
adduser -D -u "$uid" -G "$(getent group "$gid" | cut -d: -f1)" -s /bin/sh kyq
getent group "$sock" >/dev/null || addgroup -g "$sock" dockersock
addgroup kyq "$(getent group "$sock" | cut -d: -f1)"
sed -i 's/^kyq:!/kyq:*/' /etc/shadow
mkdir -p "$3/root" && chown "$uid:$gid" "$3" "$3/root" && chmod 700 "$3" "$3/root"`, "sh", uid, gid, base)
	_, port, _ := net.SplitHostPort(docker(t, "port", name, "22"))
	fp := ssh.FingerprintSHA256(hostSigner.PublicKey())
	waitForSSH(t, port, fp)
	p, _ := strconv.Atoi(port)

	gateway := ""
	for _, gw := range strings.Fields(docker(t, "network", "inspect", "kind", "--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}")) {
		if strings.Contains(gw, ".") {
			gateway = gw
		}
	}
	if gateway == "" {
		t.Fatal("network kind has no IPv4 gateway")
	}
	forwardPort := 0
	for range 50 {
		p := freePort(t, gateway)
		if l, err := net.Listen("tcp", net.JoinHostPort(gateway, strconv.Itoa(p+1))); err == nil {
			l.Close()
			forwardPort = p
			break
		}
	}
	if forwardPort == 0 {
		t.Fatal("no two adjacent free ports on kind's gateway")
	}
	conf := filepath.Join(work, "immich-relay.conf")
	os.WriteFile(conf, []byte("events {}\nstream {\n  server {\n    listen 30443;\n    proxy_pass "+nodeIP+":30443;\n  }\n}\n"), 0o644)
	relay := name + "-relay"
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", relay).Run() })
	docker(t, "run", "-d", "--name", relay, "--network", "kind", "--memory", "64m", "-p", gateway+":30443:30443", "-v", conf+":/etc/nginx/nginx.conf:ro", relayImage)

	ca := filepath.Join(work, "immich-ca.pem")
	os.WriteFile(ca, certPEM, 0o644)
	dir := filepath.Join(catalogDir, "immich")
	if err := os.CopyFS(dir, os.DirFS("../../internal/catalog/pending/immich")); err != nil {
		t.Fatal(err)
	}
	edit := func(file string, edits [][2]string) string {
		f := filepath.Join(dir, file)
		b, _ := os.ReadFile(f)
		s := string(b)
		for _, r := range edits {
			if strings.Count(s, r[0]) != 1 {
				t.Fatalf("Immich's %s changed; test plumbing for %q", file, r[0])
			}
			s = strings.Replace(s, r[0], r[1], 1)
		}
		os.WriteFile(f, []byte(s), 0o600)
		return s
	}
	manifest := edit("manifest.yaml", [][2]string{
		{"  server: ghcr.io/busnes-app/kyq-immich-server:3.2.0-ky.1@sha256:pending\n", "  server: " + server + "\n"},
		{"  directory: ghcr.io/busnes-app/kyoffboard:0.1.0@sha256:pending\n", "  directory: " + bridge + "\n"},
	})
	edit("compose.yaml.tmpl", [][2]string{
		{`    ports: ["{{forward}}:2283"]` + "\n", `    ports: ["{{forward}}:2283"]` + "\n" + `    extra_hosts: ["` + catalogIdentityHost + ":" + gateway + `"]` + "\n"},
		{"      IMMICH_MACHINE_LEARNING_URL: http://immich-machine-learning:3003\n", "      IMMICH_MACHINE_LEARNING_URL: http://immich-machine-learning:3003\n      NODE_EXTRA_CA_CERTS: /test-ca/ca.pem\n"},
		{`    volumes: ["./library:/data", "./setup:/kyq-setup"]`, `    volumes: ["./library:/data", "./setup:/kyq-setup", "` + ca + `:/test-ca/ca.pem:ro"]`},
	})
	var pins []string
	for _, k := range []string{"server", "machine_learning", "postgres", "valkey", "directory"} {
		m := regexp.MustCompile(`(?m)^  ` + k + `: (\S+@sha256:[0-9a-f]{64})$`).FindStringSubmatch(manifest)
		if m == nil {
			t.Fatalf("Immich's manifest has no pinned %s image", k)
		}
		pins = append(pins, m[1])
	}
	return immichTarget{name: name, fp: fp, base: base, root: base + "/root", owner: uid + ":" + gid, port: p, forward: gateway, forwardPort: forwardPort, relay: relay, pins: pins}
}

// immichContainer is the running container of service in Immich's project.
func immichContainer(t *testing.T, service string) string {
	t.Helper()
	c := docker(t, "ps", "-q", "--no-trunc", "--filter", "label=com.docker.compose.project="+immichProject, "--filter", "label=com.docker.compose.service="+service)
	if c == "" || strings.Contains(c, "\n") {
		t.Fatalf("Immich's %s containers: %q", service, c)
	}
	return c
}

func immichContainerIDs(t *testing.T) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for _, s := range immichServices {
		ids[s] = immichContainer(t, s)
	}
	return ids
}

// checkImmichContainers proves AC9's five containers: managed, healthy, never restarted, each
// image the pin its compose file names.
func checkImmichContainers(t *testing.T, d immichTarget) map[string]string {
	t.Helper()
	ids := immichContainerIDs(t)
	for i, s := range immichServices {
		got := docker(t, "inspect", ids[s], "--format", `{{index .Config.Labels "ky.managed-by"}} {{.State.Health.Status}} {{.RestartCount}} {{.Config.Image}}`)
		if want := "kyquickstart healthy 0 " + d.pins[i]; got != want {
			t.Fatalf("AC9: %s is %q, want %q", s, got, want)
		}
		repo, digest, _ := strings.Cut(d.pins[i], "@")
		if c := strings.LastIndex(repo, ":"); c > strings.LastIndex(repo, "/") {
			repo = repo[:c]
		}
		repo = strings.TrimPrefix(strings.TrimPrefix(repo, "docker.io/"), "library/") // as Docker names it
		image := docker(t, "inspect", ids[s], "--format", "{{.Image}}")
		if digests := docker(t, "image", "inspect", image, "--format", "{{json .RepoDigests}}"); !strings.Contains(digests, `"`+repo+"@"+digest+`"`) {
			t.Fatalf("AC9: %s's image %s has digests %s, not its pin %s", s, image, digests, d.pins[i])
		}
	}
	t.Logf("AC9: five containers healthy, restarts 0, image digests equal their pins: %v", d.pins)
	return ids
}

// immichSecrets reads the secrets apply generated or placed on the target, from their files.
func immichSecrets(t *testing.T, d immichTarget) map[string]string {
	t.Helper()
	app := d.root + "/immich"
	read := func(f string) string { return docker(t, "exec", d.name, "cat", app+"/"+f) }
	var bundle struct{ Provider struct{ ClientSecret string } }
	if json.Unmarshal([]byte(read("setup/installer/"+immichSetupID+"/setup.json")), &bundle) != nil || bundle.Provider.ClientSecret == "" {
		t.Fatal("Immich's setup bundle has no client secret")
	}
	return map[string]string{"db_password": read("secrets/db_password"), "scim_token": read("secrets/scim_token"),
		"KyIdentity's immich client secret": bundle.Provider.ClientSecret, "the service API key": read("setup/service.key")}
}

// checkImmichHardening is AC4 on the deployed containers.
func checkImmichHardening(t *testing.T, d immichTarget, ids map[string]string, secrets map[string]string) {
	t.Helper()
	fwd := func(port int) string {
		return `[{"HostIp":"` + d.forward + `","HostPort":"` + strconv.Itoa(port) + `"}]`
	}
	wantPorts := map[string]string{"immich-server": `{"2283/tcp":` + fwd(d.forwardPort) + `}`, "directory": `{"8443/tcp":` + fwd(d.forwardPort+1) + `}`}
	wantNets := map[string]string{"immich-server": "kyq-immich_default kyq-immich_internal", "immich-machine-learning": "kyq-immich_default",
		"database": "kyq-immich_internal", "redis": "kyq-immich_internal", "directory": "kyq-immich_default kyq-immich_internal"}
	for _, s := range immichServices {
		got := docker(t, "inspect", ids[s], "--format", `{{.Config.User}}|{{.HostConfig.CapDrop}}|{{.HostConfig.SecurityOpt}}|{{.HostConfig.Memory}}|{{if .Config.Healthcheck}}{{.Config.Healthcheck.Test}}{{end}}|{{json .HostConfig.PortBindings}}|{{range $n, $_ := .NetworkSettings.Networks}}{{$n}} {{end}}`)
		f := strings.Split(got, "|")
		ports := wantPorts[s]
		if ports == "" {
			ports = "{}"
		}
		if len(f) != 7 || f[0] != d.owner || strings.HasPrefix(f[0], "0:") || f[1] != "[ALL]" || f[2] != "[no-new-privileges:true]" || f[3] == "0" || f[4] == "" || f[4] == "[NONE]" || f[5] != ports || strings.TrimSpace(f[6]) != wantNets[s] {
			t.Fatalf("AC4: %s is %s", s, got)
		}
		env := docker(t, "inspect", ids[s], "--format", "{{json .Config.Env}}")
		for name, v := range secrets {
			if strings.Contains(env, v) {
				t.Fatalf("AC4: %s's environment holds %s", s, name)
			}
		}
		t.Logf("AC4: %s %s", s, got)
	}
}

// checkImmichRerun is AC18: every Immich step skipped, the same containers.
func checkImmichRerun(t *testing.T, out string, containers map[string]string) {
	t.Helper()
	n := 0
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && (strings.HasPrefix(f[0], "immich.") || f[0] == "kyidentity.amend.immich" || f[0] == "kyidentity.scim.immich" || f[0] == immichRouteStep) {
			n++
			if f[1] != "skipped" {
				t.Fatalf("AC18: %s", line)
			}
		}
	}
	if n < 8 {
		t.Fatalf("AC18: only %d Immich steps in the rerun", n)
	}
	if got := immichContainerIDs(t); !maps.Equal(got, containers) {
		t.Fatalf("AC18: containers changed across the rerun: %v, were %v", got, containers)
	}
	t.Logf("AC18: %d Immich steps skipped, container IDs unchanged", n)
}

// checkImmichHandover asserts Immich's handover block line by line.
func checkImmichHandover(t *testing.T, out string, d immichTarget) {
	t.Helper()
	port, bridge := strconv.Itoa(d.forwardPort), strconv.Itoa(d.forwardPort+1)
	want := []string{
		"Immich at https://" + catalogImmichURLHost + "/: sign in through KyIdentity as prepared-admin (administers Immich) or prepared-user",
		"  (your photos). Use prepared-user for photos: Immich lets its administrator keep photos too, and",
		"  prepared-admin should only administer.",
		`  Phone app: enter the server URL https://` + catalogImmichURLHost + `, then choose "Login with OAuth".`,
		"  Anyone you assign to Immich in KyIdentity later signs in the same way; their account is",
		"  created for them.",
		"  Removing someone from Immich in KyIdentity ends their Immich sessions and API keys and keeps",
		"  their account and photos.",
		"  Assigning them again restores their sign-in to the same account, with none of their old",
		"  sessions or keys.",
		"  Its accounts have no local password: every sign-in goes through KyIdentity.",
		"",
		"  Notes:",
		"  - Ports " + d.forward + ":" + port + " (Immich) and " + d.forward + ":" + bridge + " (its KyIdentity bridge) are open to your whole",
		"    LAN: at your firewall, restrict both to the edge's address as the Docker host sees it,",
		"    with a rule tested on the Docker host (docs/guided-setup.md, Catalog Immich on Docker).",
		"    The edge reaches the bridge over TLS whose self-signed certificate it does not verify.",
		"  - Immich connects out to GitHub (version check), Hugging Face (machine learning models) and",
		"    tiles.immich.cloud (map tiles).",
		"  - Source of its KyQuickStart module (AGPL):",
		"    https://github.com/Busnes-app/KyQuickStart/tree/immich-v3.2.0-ky.1/internal/adapters/immich/module",
	}
	lines := strings.Split(out, "\n")
	at := slices.Index(lines, want[0])
	if at < 0 || len(lines) < at+len(want) || strings.Join(lines[at:at+len(want)], "\n") != strings.Join(want, "\n") {
		t.Fatalf("AC20: Immich's handover:\n%s\nwant:\n%s", strings.Join(lines[max(at, 0):min(len(lines), max(at, 0)+len(want))], "\n"), strings.Join(want, "\n"))
	}
	t.Logf("AC20: the Immich handover's %d lines and the backup lines match", len(want))
}

// immichReceipts are the setup, creation and directory receipts in Immich's database.
func immichReceipts(t *testing.T, database string) string {
	t.Helper()
	return docker(t, "exec", database, "psql", "-U", "postgres", "-d", "immich", "-At", "-v", "ON_ERROR_STOP=1", "-c",
		`SELECT key || ' ' || value::text FROM system_metadata WHERE key = 'kyquickstart-setup' UNION ALL (SELECT "userId" || ' ' || key || ' ' || value::text FROM user_metadata WHERE key IN ('kyquickstart-creation', 'kyquickstart-directory') ORDER BY 1)`)
}

// immichOriginal reads asset's original file on the target, where the database says it is.
func immichOriginal(t *testing.T, database, app, asset string) []byte {
	t.Helper()
	p := docker(t, "exec", database, "psql", "-U", "postgres", "-d", "immich", "-At", "-c", `SELECT "originalPath" FROM asset WHERE id = '`+asset+`'`)
	rest, ok := strings.CutPrefix(p, "/data/")
	if !ok {
		t.Fatalf("asset %s is at %q", asset, p)
	}
	b, err := os.ReadFile(filepath.Join(app, "library", rest))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// bridgeJobs reads the bridge's state on the target.
type bridgeJobs struct {
	t   *testing.T
	dir string
}

func (j bridgeJobs) binding(uuid string) string {
	j.t.Helper()
	files, _ := filepath.Glob(filepath.Join(j.dir, "*.binding"))
	for _, f := range files {
		var r struct {
			Binding struct {
				NativeUUID string `json:"native_uuid"`
			}
		}
		b, _ := os.ReadFile(f)
		if json.Unmarshal(b, &r) == nil && r.Binding.NativeUUID == uuid {
			return strings.TrimSuffix(filepath.Base(f), ".binding")
		}
	}
	j.t.Fatalf("the bridge has no binding for %s (%d bindings)", uuid, len(files))
	return ""
}

func (j bridgeJobs) job(id string, epoch int) (string, bool) {
	name := id + ".job"
	if epoch > 1 {
		name = id + ".epoch-" + strings.Repeat("0", 10-len(strconv.Itoa(epoch))) + strconv.Itoa(epoch)
	}
	b, err := os.ReadFile(filepath.Join(j.dir, name))
	var job struct{ Status string }
	if err != nil || json.Unmarshal(b, &job) != nil {
		return "", false
	}
	return job.Status, true
}

// wait waits until binding id's epoch has status want.
func (j bridgeJobs) wait(id string, epoch int, want string) {
	j.t.Helper()
	start := time.Now()
	for {
		if s, ok := j.job(id, epoch); ok && s == want {
			j.t.Logf("bridge: %s epoch %d %s after %s", id, epoch, want, time.Since(start).Round(time.Second))
			return
		}
		if time.Since(start) > 6*time.Minute {
			s, _ := j.job(id, epoch)
			j.t.Fatalf("bridge: %s epoch %d is %q, not %s", id, epoch, s, want)
		}
		time.Sleep(time.Second)
	}
}

// waitSettled waits for epoch's terminal status: complete or refused.
func (j bridgeJobs) waitSettled(id string, epoch int) string {
	j.t.Helper()
	start := time.Now()
	for {
		if s, ok := j.job(id, epoch); ok && (s == "complete" || s == "refused") {
			return s
		}
		if time.Since(start) > 6*time.Minute {
			s, _ := j.job(id, epoch)
			j.t.Fatalf("bridge: %s epoch %d is %q", id, epoch, s)
		}
		time.Sleep(time.Second)
	}
}

// audit returns epoch's result audit outcomes.
func (j bridgeJobs) audit(id string, epoch int) string {
	j.t.Helper()
	files, _ := filepath.Glob(filepath.Join(j.dir, id+".epoch-"+strings.Repeat("0", 10-len(strconv.Itoa(epoch)))+strconv.Itoa(epoch)+"-audit-*-result"))
	var outcomes []string
	for _, f := range files {
		var a struct {
			Epoch   int
			Outcome string
		}
		b, _ := os.ReadFile(f)
		if json.Unmarshal(b, &a) != nil || a.Epoch != epoch {
			j.t.Fatalf("bridge audit %s: %s", f, b)
		}
		outcomes = append(outcomes, a.Outcome)
	}
	if len(outcomes) == 0 {
		j.t.Fatalf("bridge: no result audit for %s epoch %d", id, epoch)
	}
	return strings.Join(outcomes, ",")
}

// immichTransport dials KyIdentity and Immich at the lan edge's NodePort, verifying the edge's
// certificate.
func immichTransport(t *testing.T, certPEM []byte, nodeIP string) *http.Transport {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("invalid edge certificate")
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != catalogIdentityURLHost && address != catalogImmichURLHost {
			return nil, errors.New("unselected destination " + address)
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(nodeIP, "30443"))
	}
	t.Cleanup(tr.CloseIdleConnections)
	return tr
}

type immichUser struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	IsAdmin bool   `json:"isAdmin"`
	OAuthID string `json:"oauthId"`
}

// immichLogin is one signed-in browser: its jar holds KyIdentity's and Immich's cookies.
type immichLogin struct {
	c     *http.Client
	me    immichUser
	token string // Immich's access token, its session cookie's value
}

func (l *immichLogin) cookie() string { return "immich_access_token=" + l.token }

// immichCredential is one credential the test expects Immich to refuse: a header and its value,
// or a socket opened with a cookie.
type immichCredential struct{ name, header, value string }

type immichAPI struct {
	t   *testing.T
	ctx context.Context
	tr  *http.Transport
}

func (im *immichAPI) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Transport: im.tr, Jar: jar, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (im *immichAPI) send(c *http.Client, method, endpoint string, body []byte, ctype string, header ...string) (*http.Response, []byte) {
	im.t.Helper()
	req, _ := http.NewRequestWithContext(im.ctx, method, endpoint, bytes.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	r, err := c.Do(req)
	if err != nil {
		im.t.Fatalf("%s %s: %v", method, endpoint, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	return r, b
}

// call sends one request with l's session.
func (im *immichAPI) call(l *immichLogin, method, path string, body []byte, ctype string) (int, []byte) {
	im.t.Helper()
	r, b := im.send(l.c, method, immichOrigin+path, body, ctype)
	return r.StatusCode, b
}

func (im *immichAPI) status(l *immichLogin, method, path string) int {
	im.t.Helper()
	code, _ := im.call(l, method, path, nil, "")
	return code
}

func (im *immichAPI) anon(method, path, body string) (int, []byte) {
	im.t.Helper()
	r, b := im.send(im.client(), method, immichOrigin+path, []byte(body), "application/json")
	return r.StatusCode, b
}

// meWith is /api/users/me with one header; a zero user when refused.
func (im *immichAPI) meWith(header, value string) immichUser {
	im.t.Helper()
	r, b := im.send(im.client(), "GET", immichOrigin+"/api/users/me", nil, "", header, value)
	var me immichUser
	if r.StatusCode == 200 {
		json.Unmarshal(b, &me)
	} else if r.StatusCode != 401 {
		im.t.Fatalf("/api/users/me with %s: %d %s", header, r.StatusCode, b)
	}
	return me
}

// identitySignIn signs username in to KyIdentity in c's jar.
func (im *immichAPI) identitySignIn(c *http.Client, username, password string) bool {
	im.t.Helper()
	_, b := im.send(c, "GET", identityOrigin+"/api/auth/csrf", nil, "")
	var csrf struct {
		CSRFToken string `json:"csrfToken"`
	}
	if json.Unmarshal(b, &csrf) != nil || csrf.CSRFToken == "" {
		im.t.Fatalf("KyIdentity CSRF: %s", b)
	}
	payload, _ := json.Marshal(map[string]string{"username": username, "password": password})
	r, b := im.send(c, "POST", identityOrigin+"/api/auth/login", payload, "application/json", "X-CSRF-Token", csrf.CSRFToken)
	var login struct{ Success bool }
	return r.StatusCode == 200 && json.Unmarshal(b, &login) == nil && login.Success
}

// authorize starts Immich's OAuth sign-in for redirect in c and follows KyIdentity's answer:
// the callback URL, or "" when KyIdentity gave no code.
func (im *immichAPI) authorize(c *http.Client, redirect string, extra map[string]string) (*url.URL, *url.URL) {
	im.t.Helper()
	body := map[string]string{"redirectUri": redirect}
	for k, v := range extra {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	r, b := im.send(c, "POST", immichOrigin+"/api/oauth/authorize", raw, "application/json")
	var start struct{ URL string }
	if r.StatusCode != 201 || json.Unmarshal(b, &start) != nil {
		im.t.Fatalf("Immich OAuth authorize: %d %s", r.StatusCode, b)
	}
	authorize, err := url.Parse(start.URL)
	if err != nil || authorize.Host != catalogIdentityURLHost {
		im.t.Fatalf("Immich's authorize URL: %s", start.URL)
	}
	r, _ = im.send(c, "GET", authorize.String(), nil, "")
	callback, _ := r.Location()
	if r.StatusCode != 302 || callback == nil || callback.Query().Get("code") == "" {
		im.t.Logf("KyIdentity gave no code for Immich: %d %v", r.StatusCode, callback)
		return authorize, nil
	}
	return authorize, callback
}

// login signs username in to Immich through KyIdentity in a new browser, as Immich's web app
// does, and checks the authorize request and the session cookie.
func (im *immichAPI) login(username, password string) *immichLogin {
	im.t.Helper()
	l, refusal := im.tryLogin(username, password)
	if l == nil {
		im.t.Fatalf("%s's sign-in to Immich was refused (%s)", username, refusal)
	}
	return l
}

func (im *immichAPI) tryLogin(username, password string) (*immichLogin, string) {
	im.t.Helper()
	c := im.client()
	if !im.identitySignIn(c, username, password) {
		return nil, "password"
	}
	redirect := immichOrigin + "/auth/login"
	authorize, callback := im.authorize(c, redirect, nil)
	q := authorize.Query()
	if q.Get("client_id") != "immich" || q.Get("redirect_uri") != redirect || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
		im.t.Fatalf("AC10: authorization request: %s", authorize)
	}
	if callback == nil {
		return nil, "authorization"
	}
	if callback.Scheme+"://"+callback.Host+callback.Path != redirect || callback.Query().Get("state") != q.Get("state") {
		im.t.Fatalf("KyIdentity's callback: %s", callback)
	}
	raw, _ := json.Marshal(map[string]string{"url": callback.String()})
	r, b := im.send(c, "POST", immichOrigin+"/api/oauth/callback", raw, "application/json")
	if r.StatusCode != 201 {
		return nil, "callback " + strconv.Itoa(r.StatusCode) + " " + string(b)
	}
	l := &immichLogin{c: c}
	for _, ck := range r.Cookies() {
		if ck.Name == "immich_access_token" {
			if !ck.Secure || !ck.HttpOnly {
				im.t.Fatalf("AC10: Immich's session cookie is not Secure and HttpOnly: %s", ck.String())
			}
			l.token = ck.Value
		}
	}
	if l.token == "" {
		im.t.Fatal("AC10: Immich set no session cookie")
	}
	code, b := im.call(l, "GET", "/api/users/me", nil, "")
	if code != 200 || json.Unmarshal(b, &l.me) != nil {
		im.t.Fatalf("Immich /api/users/me: %d %s", code, b)
	}
	return l, ""
}

// waitLogin retries a sign-in refused while KyIdentity's connector delivers the account
// through the bridge (it retries with backoff).
func (im *immichAPI) waitLogin(username, password string) *immichLogin {
	im.t.Helper()
	start := time.Now()
	for {
		l, refusal := im.tryLogin(username, password)
		if l != nil {
			im.t.Logf("%s signed in to Immich %s after the wait began", username, time.Since(start).Round(time.Second))
			return l
		}
		if time.Since(start) > 10*time.Minute {
			im.t.Fatalf("%s never signed in to Immich: %s", username, refusal)
		}
		time.Sleep(5 * time.Second)
	}
}

// loginRefused is where username's fresh sign-in is refused, "" when it is not.
func (im *immichAPI) loginRefused(username, password string) string {
	im.t.Helper()
	l, refusal := im.tryLogin(username, password)
	if l != nil {
		return ""
	}
	im.t.Logf("%s's fresh sign-in refused: %s", username, refusal)
	return refusal
}

// phoneLogin is the phone app's sign-in: its custom-scheme redirect, replaced by Immich's
// https one, answered 307 back to the app, whose callback returns a bearer token.
func (im *immichAPI) phoneLogin(username, password string) string {
	im.t.Helper()
	c := im.client()
	if !im.identitySignIn(c, username, password) {
		im.t.Fatalf("%s's KyIdentity sign-in failed", username)
	}
	v := make([]byte, 32)
	rand.Read(v)
	verifier := base64.RawURLEncoding.EncodeToString(v)
	sum := sha256.Sum256([]byte(verifier))
	state := randomPassword()
	authorize, callback := im.authorize(c, "app.immich:///oauth-callback", map[string]string{"state": state, "codeChallenge": base64.RawURLEncoding.EncodeToString(sum[:])})
	mobile := immichOrigin + "/api/oauth/mobile-redirect"
	if authorize.Query().Get("redirect_uri") != mobile || callback == nil || callback.Scheme+"://"+callback.Host+callback.Path != mobile {
		im.t.Fatalf("AC13: phone authorization %s, callback %v", authorize, callback)
	}
	r, _ := im.send(c, "GET", callback.String(), nil, "")
	app := r.Header.Get("Location")
	if r.StatusCode != 307 || !strings.HasPrefix(app, "app.immich:///oauth-callback?") || !strings.Contains(app, "code=") || !strings.Contains(app, "state="+state) {
		im.t.Fatalf("AC13: mobile-redirect: %d %s", r.StatusCode, app)
	}
	raw, _ := json.Marshal(map[string]string{"url": app, "state": state, "codeVerifier": verifier})
	r, b := im.send(&http.Client{Transport: im.tr, Timeout: 60 * time.Second}, "POST", immichOrigin+"/api/oauth/callback", raw, "application/json")
	var token struct{ AccessToken string }
	if r.StatusCode != 201 || json.Unmarshal(b, &token) != nil || token.AccessToken == "" {
		im.t.Fatalf("AC13: phone callback: %d", r.StatusCode)
	}
	return token.AccessToken
}

func (im *immichAPI) adminUsers(admin *immichLogin) []immichUser {
	im.t.Helper()
	var users []immichUser
	if code, b := im.call(admin, "GET", "/api/admin/users", nil, ""); code != 200 || json.Unmarshal(b, &users) != nil {
		im.t.Fatalf("Immich admin users: %d %s", code, b)
	}
	return users
}

func (im *immichAPI) adminUser(admin *immichLogin, id string) immichUser {
	im.t.Helper()
	for _, u := range im.adminUsers(admin) {
		if u.ID == id {
			return u
		}
	}
	im.t.Fatalf("Immich has no account %s", id)
	return immichUser{}
}

// waitAdmin waits for the bridge's directory projection of id's administrator right.
func (im *immichAPI) waitAdmin(admin *immichLogin, id string, want bool) {
	im.t.Helper()
	start := time.Now()
	for im.adminUser(admin, id).IsAdmin != want {
		if time.Since(start) > 6*time.Minute {
			im.t.Fatalf("AC11: %s's isAdmin never became %v", id, want)
		}
		time.Sleep(2 * time.Second)
	}
	im.t.Logf("TIMING role change to Immich's isAdmin %v: %s", want, time.Since(start).Round(time.Second))
}

func (im *immichAPI) waitName(admin *immichLogin, id, name string) {
	im.t.Helper()
	start := time.Now()
	for im.adminUser(admin, id).Name != name {
		if time.Since(start) > 6*time.Minute {
			im.t.Fatalf("Immich never named %s %q", id, name)
		}
		time.Sleep(2 * time.Second)
	}
}

// retained proves, as admin, that id is still there with assets.
func (im *immichAPI) retained(admin *immichLogin, id string, images int) {
	im.t.Helper()
	im.adminUser(admin, id)
	var stats struct{ Images int }
	if code, b := im.call(admin, "GET", "/api/admin/users/"+id+"/statistics", nil, ""); code != 200 || json.Unmarshal(b, &stats) != nil || stats.Images < images {
		im.t.Fatalf("AC14: %s's statistics: %d %s", id, code, b)
	}
}

// denied fails unless Immich refuses every credential.
func (im *immichAPI) denied(creds []immichCredential, when string) {
	im.t.Helper()
	for _, c := range creds {
		if c.header == "socket" {
			if s := im.socket(c.value); s.authenticated {
				s.conn.Close()
				im.t.Fatalf("AC14: a socket with %s was accepted %s", c.name, when)
			}
			continue
		}
		start := time.Now()
		for {
			r, _ := im.send(im.client(), "GET", immichOrigin+"/api/users/me", nil, "", headerFor(c)...)
			if r.StatusCode == 401 {
				break
			}
			if time.Since(start) > 5*time.Minute {
				im.t.Fatalf("%s: %s answers %d %s", when, c.name, r.StatusCode, when)
			}
			time.Sleep(time.Second)
		}
	}
	im.t.Logf("%s: every one of %d credentials refused", when, len(creds))
}

func headerFor(c immichCredential) []string {
	if c.header == "cookie" {
		return []string{"Cookie", c.value}
	}
	return []string{c.header, c.value}
}

// watchKey polls /api/users/me with key until stopped, and reports when it was first accepted
// and when it was refused after that.
func (im *immichAPI) watchKey(key string) func() (time.Time, time.Time) {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var first, denied time.Time
	wg.Add(1)
	go func() {
		defer wg.Done()
		c := &http.Client{Transport: im.tr, Timeout: 5 * time.Second}
		for {
			select {
			case <-stop:
				return
			case <-time.After(200 * time.Millisecond):
			}
			req, _ := http.NewRequestWithContext(im.ctx, "GET", immichOrigin+"/api/users/me", nil)
			req.Header.Set("x-api-key", key)
			r, err := c.Do(req)
			if err != nil {
				continue
			}
			r.Body.Close()
			switch now := time.Now(); {
			case r.StatusCode == 200 && first.IsZero():
				first = now
			case r.StatusCode == 401 && !first.IsZero() && denied.IsZero():
				denied = now
			}
		}
	}()
	return func() (time.Time, time.Time) {
		close(stop)
		wg.Wait()
		return first, denied
	}
}

// testJPEG is a small photo, different for each seed.
func testJPEG(seed byte) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := range 240 {
		for x := range 320 {
			img.Set(x, y, color.RGBA{byte(x) + seed*40, byte(y) + seed*70, byte(x+y) * seed, 255})
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 90})
	return b.Bytes()
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (im *immichAPI) uploadStatus(l *immichLogin, name string, data []byte) (int, []byte) {
	im.t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	now := time.Now().UTC().Format(time.RFC3339)
	w.WriteField("fileCreatedAt", now)
	w.WriteField("fileModifiedAt", now)
	w.WriteField("filename", name)
	f, _ := w.CreateFormFile("assetData", name)
	f.Write(data)
	w.Close()
	return im.call(l, "POST", "/api/assets", body.Bytes(), w.FormDataContentType())
}

func (im *immichAPI) upload(l *immichLogin, name string, data []byte, want int) string {
	im.t.Helper()
	code, b := im.uploadStatus(l, name, data)
	var a struct{ ID string }
	if code != want || json.Unmarshal(b, &a) != nil || a.ID == "" {
		im.t.Fatalf("upload %s: %d %s", name, code, b)
	}
	return a.ID
}

func (im *immichAPI) original(l *immichLogin, id string) []byte {
	im.t.Helper()
	code, b := im.call(l, "GET", "/api/assets/"+id+"/original", nil, "")
	if code != 200 {
		im.t.Fatalf("original of %s: %d", id, code)
	}
	return b
}

func (im *immichAPI) waitThumbnail(l *immichLogin, id string, limit time.Duration) {
	im.t.Helper()
	start := time.Now()
	for {
		r, _ := im.send(l.c, "GET", immichOrigin+"/api/assets/"+id+"/thumbnail", nil, "")
		if r.StatusCode == 200 && strings.HasPrefix(r.Header.Get("Content-Type"), "image/") {
			im.t.Logf("thumbnail of %s: 200 %s after %s", id, r.Header.Get("Content-Type"), time.Since(start).Round(time.Second))
			return
		}
		if time.Since(start) > limit {
			im.t.Fatalf("thumbnail of %s: %d %s", id, r.StatusCode, r.Header.Get("Content-Type"))
		}
		time.Sleep(2 * time.Second)
	}
}

// regenerateThumbnails starts Immich's thumbnail job for every asset, as the owner's
// administrator does after a restore: thumbnails are not backed up.
func (im *immichAPI) regenerateThumbnails(admin *immichLogin) {
	im.t.Helper()
	if code, b := im.call(admin, "PUT", "/api/jobs/thumbnailGeneration", []byte(`{"command":"start","force":true}`), "application/json"); code/100 != 2 {
		im.t.Fatalf("thumbnail job: %d %s", code, b)
	}
}

func (im *immichAPI) inTimeline(l *immichLogin, id string) bool {
	im.t.Helper()
	var buckets []struct{ TimeBucket string }
	if code, b := im.call(l, "GET", "/api/timeline/buckets", nil, ""); code != 200 || json.Unmarshal(b, &buckets) != nil {
		im.t.Fatalf("timeline buckets: %d %s", code, b)
	}
	for _, bucket := range buckets {
		var assets struct{ ID []string }
		if code, b := im.call(l, "GET", "/api/timeline/bucket?timeBucket="+url.QueryEscape(bucket.TimeBucket), nil, ""); code != 200 || json.Unmarshal(b, &assets) != nil {
			im.t.Fatalf("timeline bucket: %d %s", code, b)
		}
		if slices.Contains(assets.ID, id) {
			return true
		}
	}
	return false
}

func (im *immichAPI) waitSmartSearch(l *immichLogin, id string, limit time.Duration) {
	im.t.Helper()
	start := time.Now()
	for {
		code, b := im.call(l, "POST", "/api/search/smart", []byte(`{"query":"a colourful gradient"}`), "application/json")
		var found struct {
			Assets struct{ Items []struct{ ID string } }
		}
		if code == 200 && json.Unmarshal(b, &found) == nil && slices.ContainsFunc(found.Assets.Items, func(a struct{ ID string }) bool { return a.ID == id }) {
			return
		}
		if time.Since(start) > limit {
			im.t.Fatalf("AC12: smart search never found %s: %d %s", id, code, b)
		}
		time.Sleep(10 * time.Second)
	}
}

// immichSocket is one socket.io connection to Immich.
type immichSocket struct {
	conn          *websocket.Conn
	authenticated bool
	closed        chan struct{}
}

// socket opens Immich's socket.io websocket with cookie. It is authenticated when Immich sends
// its server version, which it does only after authenticating the connection.
func (im *immichAPI) socket(cookie string) *immichSocket {
	im.t.Helper()
	d := websocket.Dialer{TLSClientConfig: im.tr.TLSClientConfig, NetDialContext: im.tr.DialContext, HandshakeTimeout: 10 * time.Second}
	conn, r, err := d.DialContext(im.ctx, "wss://"+catalogImmichURLHost+"/api/socket.io/?EIO=4&transport=websocket", http.Header{"Cookie": {cookie}})
	if err != nil {
		code := 0
		if r != nil {
			code = r.StatusCode
		}
		im.t.Fatalf("socket.io: %d %v", code, err)
	}
	if _, open, err := conn.ReadMessage(); err != nil || !bytes.HasPrefix(open, []byte("0{")) {
		im.t.Fatalf("socket.io open: %s %v", open, err)
	}
	conn.WriteMessage(websocket.TextMessage, []byte("40"))
	s := &immichSocket{conn: conn, closed: make(chan struct{})}
	verdict := make(chan bool, 1)
	go func() {
		defer close(s.closed)
		defer conn.Close()
		decided := false
		decide := func(ok bool) {
			if !decided {
				decided = true
				verdict <- ok
			}
		}
		defer decide(false)
		for {
			_, msg, err := conn.ReadMessage()
			switch {
			case err != nil:
				return
			case string(msg) == "2":
				conn.WriteMessage(websocket.TextMessage, []byte("3"))
			case bytes.HasPrefix(msg, []byte(`42["on_server_version"`)):
				decide(true)
			case bytes.HasPrefix(msg, []byte(`42["error"`)), bytes.HasPrefix(msg, []byte("41")), bytes.HasPrefix(msg, []byte("44")):
				decide(false)
				return
			}
		}
	}()
	select {
	case s.authenticated = <-verdict:
	case <-time.After(20 * time.Second):
		conn.Close()
		im.t.Fatal("socket.io: no verdict within 20s")
	}
	return s
}

func (s *immichSocket) waitClosed(limit time.Duration) bool {
	select {
	case <-s.closed:
		return true
	case <-time.After(limit):
		return false
	}
}
