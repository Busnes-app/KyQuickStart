//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
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
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// TestKubernetesRestore is the KyDrive restore drill: NPM's lan edge and KyIdentity in kind,
// KyDrive on a Docker target with an append-only rest-server. Restore is refused while
// KyIdentity is 0.4.3 (AC5a); upgraded to the catalog's (0.4.4 or later), whose resync replays removals, it backs
// KyDrive up, destroys its directory and restores it (AC5), proves the refusals and
// --replace (AC6), and that a person removed after the snapshot is not readmitted (AC15).
func TestKubernetesRestore(t *testing.T) {
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
	if err := os.CopyFS(filepath.Join(catalogDir, "npm"), os.DirFS("../../internal/catalog/apps/npm")); err != nil {
		t.Fatal(err)
	}
	// KyIdentity starts at the attested 0.4.3 release, whose resync does not replay removals.
	identity043(t, name, catalogDir)
	logAppOnFailure(t, cs, "kyidentity")
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
	client := name + "-client"
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", client).Run() })
	docker(t, "run", "-d", "--name", client, "--network", "kind", "--memory", "64m", "--entrypoint", "sleep", kindNode, "1800")
	viaEdge := func(host, path string) string {
		return docker(t, "exec", client, "curl", "-sk", "-m", "10", "--resolve", host+":30443:"+nodeIP, "https://"+host+":30443"+path)
	}

	drive := startDriveTarget(t, work, catalogDir, nodeIP, certPEM)
	st, err := stack.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	// The lan edge admits the LAN client, the cluster's pods, this host (kind's gateway) and
	// KyDrive's relay to KyIdentity.
	allow := []string{docker(t, "inspect", client, "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}") + "/32", mustNode(t, ctx, cs, node).Spec.PodCIDR, drive.forward + "/32", drive.relayIP(t) + "/32"}
	st.Edges = []stack.Edge{{Target: "k8s", Provider: "lan", NPMURL: "kube://kyq-npm/app:81", AllowCIDRs: allow, ClientPort: 30443}}
	addCatalogIdentity(st, certID)
	addCatalogDrive(st, certID, drive)
	b := giveDriveDestination(t, work, st, drive)
	savePlan(t, plan, st)
	trust = []string{"--trust-host-key", "nas=" + drive.fp}
	testClusterDNS(t, ctx, cs, nodeIP)
	out := apply("identity and drive")
	for _, id := range []string{"kyidentity.deploy", "kyidentity.health", "kyidentity.setup"} {
		if status(out, id) != "ok" {
			t.Fatalf("%s = %q", id, status(out, id))
		}
	}
	driveSecrets := checkCatalogDrive(t, out, drive, viaEdge)
	checkDriveConnector(t, ctx, connected, out, driveSecrets[1])
	nas := nasTarget(t, plan)
	checkBackupHandover(t, out, "kydrive", nas)
	trustTestCA(t, ctx, cs, connected, "kyidentity", certPEM)
	driveSignOn(t, ctx, kc, certPEM, nodeIP, driveSecrets[1])
	secrets := map[string]string{"the rest-server password": b.srv.passwords["nas"], "KyIdentity's kydrive client secret": driveSecrets[0], "scim_token": driveSecrets[1], "session_secret": driveSecrets[2]}

	transport := driveTransport(t, certPEM, nodeIP)
	scim := &driveSCIM{t: t, ctx: ctx, c: &http.Client{Transport: transport, Timeout: 30 * time.Second}, token: driveSecrets[1]}
	owners := func() {
		t.Helper()
		driveLogin(t, ctx, transport, "prepared-admin", ownerPassword(t, ctx, connected, "admin.password"), true)
		driveLogin(t, ctx, transport, "prepared-user", ownerPassword(t, ctx, connected, "everyday.password"), false)
	}

	// AC5 setup: the everyday account uploads F; the SCIM list is recorded.
	everyday := driveLogin(t, ctx, transport, "prepared-user", ownerPassword(t, ctx, connected, "everyday.password"), false)
	f := make([]byte, 300<<10)
	rand.Read(f)
	fileID := driveUpload(t, ctx, everyday, "F.bin", f)
	recorded := scim.list()
	t.Logf("SCIM users before the backup: %+v", recorded)

	// AC5 backup: cold, the same container, healthy, restic check passes; the snapshot holds
	// data, backups and both capsule secrets.
	id, backupOut := backupDrive(t, ctx, state, catalogDir, trust, b, secrets)
	outputs = append(outputs, backupOut)

	// AC5a: on KyIdentity 0.4.3 restore is refused before any restic call, nothing changed.
	outputs = append(outputs, refuseUntouched(t, ctx, state, catalogDir, trust, drive, id[:8], "restore needs KyIdentity v0.4.4 or later, which replays removals on resync; installed: 0.4.3. Upgrade KyIdentity first")...)
	// The catalog's KyIdentity (0.4.6 or later) replays removals; it needs only an acknowledgement.
	upgradeIdentity(t, ctx, cs, connected, name, state, catalogDir, trust, certPEM, &outputs)
	snaps := snapshots(t, b.dest, "kydrive", b.pw)
	if len(snaps) != 1 || snaps[0].ID != id || strings.Join(snaps[0].Paths, " ") != "/kyq/secrets/scim_token /kyq/secrets/session_secret /kyq/volumes/backups /kyq/volumes/data" {
		t.Fatalf("restic snapshots: %+v", snaps)
	}
	// The owner's record of the password, read with the handover's own command.
	password := handoverPassword(t, ctx, state, out, nas)

	// AC6: the target's credential cannot forget a snapshot.
	forgot, err := restic(b.dest, "kydrive", b.pw, "forget", id)
	refusal := regexp.MustCompile(`(?m)^Remove\(<snapshot/.*\) failed: unexpected HTTP response \(403\): 403 Forbidden$`).FindString(forgot)
	if err == nil || refusal == "" {
		t.Fatalf("restic forget with the target's credential: %v\n%s", err, forgot)
	}
	t.Logf("restic forget refused by the append-only rest-server: %s", refusal)
	if snaps := snapshots(t, b.dest, "kydrive", b.pw); len(snaps) != 1 || snaps[0].ID != id {
		t.Fatalf("snapshot gone after a refused forget: %+v", snaps)
	}
	// AC6: a snapshot tagged for another app, written to KyDrive's repository with the
	// target's credential, is refused.
	if out, err := restic(b.dest, "kydrive", b.pw, "backup", "--host", "nas", "--tag", "kyquickstart", "--tag", "app=kyidentity", "--tag", "installation="+installationID(t, plan), "/run/ca.pem"); err != nil {
		t.Fatalf("foreign snapshot: %v\n%s", err, out)
	}
	foreign := ""
	for _, s := range snapshots(t, b.dest, "kydrive", b.pw) {
		if slices.Contains(s.Tags, "app=kyidentity") {
			foreign = s.Short
		}
	}
	// AC6: the running KyDrive is refused.
	listing := dirDigest(t, drive.name, drive.root+"/kydrive")
	o, err := kyq(t, ctx, state, catalogDir, trust, "", "restore", "kydrive", "--snapshot", id, "--yes")
	outputs = append(outputs, o)
	refused(t, err, "kydrive is running on nas")
	if after := dirDigest(t, drive.name, drive.root+"/kydrive"); after != listing {
		t.Fatalf("a refused restore changed KyDrive's directory:\n%s\nwas:\n%s", after, listing)
	}
	// The foreign snapshot is refused on the stopped KyDrive even with --replace, before
	// anything moves aside. Stopping may checkpoint SQLite, so list after the stop.
	docker(t, "compose", "-p", "kyq-kydrive", "stop")
	listing = dirDigest(t, drive.name, drive.root+"/kydrive")
	o, err = kyq(t, ctx, state, catalogDir, trust, "", "restore", "kydrive", "--snapshot", foreign, "--yes", "--replace")
	outputs = append(outputs, o)
	refused(t, err, "snapshot "+foreign+" is not tagged for kydrive of this installation")
	if after := dirDigest(t, drive.name, drive.root+"/kydrive"); after != listing {
		t.Fatalf("a refused restore changed KyDrive's directory:\n%s\nwas:\n%s", after, listing)
	}

	// AC5 disaster and restore: the password on stdin, the name typed back.
	disaster := func() {
		t.Helper()
		docker(t, "compose", "-p", "kyq-kydrive", "down")
		docker(t, "exec", drive.name, "rm", "-rf", drive.root+"/kydrive")
	}
	restore := func(label, snapshot, stdin string, extra ...string) string {
		t.Helper()
		start := time.Now()
		o, err := kyq(t, ctx, state, catalogDir, trust, stdin, append([]string{"restore", "kydrive", "--snapshot", snapshot}, extra...)...)
		outputs = append(outputs, o)
		if err != nil {
			t.Fatalf("restore (%s): %v", label, err)
		}
		for _, want := range []string{"Restore kydrive from snapshot ", "\n  taken ", " on nas\n  paths /kyq/secrets/scim_token /kyq/secrets/session_secret /kyq/volumes/backups /kyq/volumes/data\n", "kydrive: KyIdentity replayed its kydrive connector's state, removals included, and delivered it (", "kydrive: restored from snapshot "} {
			if !strings.Contains(o, want) {
				t.Fatalf("restore (%s) output lacks %q", label, want)
			}
		}
		t.Logf("restore (%s) took %s", label, time.Since(start).Round(time.Second))
		return o
	}
	disaster()
	o = restore("disaster", id, password+"\nkydrive\n")
	if !strings.Contains(o, "Type kydrive to restore it: ") {
		t.Fatal("restore did not ask for the typed name")
	}
	admin := newIdentityAdmin(t, ctx, transport, "prepared-admin", ownerPassword(t, ctx, connected, "admin.password"))
	restored := func(label string) {
		t.Helper()
		if got := scim.list(); !sameUsers(got, recorded) {
			t.Fatalf("%s: SCIM users %+v, recorded %+v", label, got, recorded)
		}
		owners()
		everyday := driveLogin(t, ctx, transport, "prepared-user", ownerPassword(t, ctx, connected, "everyday.password"), false)
		if got := driveDownload(t, ctx, everyday, fileID); !bytes.Equal(got, f) {
			t.Fatalf("%s: F differs after the restore (%d bytes)", label, len(got))
		}
		// The connector delivers again with the restored token.
		user := admin.findUser("prepared-user")
		display := "Everyday " + label
		admin.change("PUT", "/api/admin/users/"+user, map[string]string{"displayName": display})
		scim.wait("prepared-user", func(u scimUser) bool { return u.DisplayName == display }, "displayName "+display)
		out := apply(label + " rerun")
		for _, line := range strings.Split(out, "\n") {
			if f := strings.Fields(line); len(f) >= 2 && (strings.HasPrefix(f[0], "kydrive.") || f[0] == "kyidentity.scim.kydrive") && f[1] != "skipped" {
				t.Errorf("%s rerun: %s", label, line)
			}
		}
		got := docker(t, "exec", drive.name, "stat", "-c", "%n %a %u", b.pw, drive.root+"/kydrive/secrets/session_secret", drive.root+"/kydrive/secrets/scim_token")
		if want := b.pw + " 600 0\n" + drive.root + "/kydrive/secrets/session_secret 600 0\n" + drive.root + "/kydrive/secrets/scim_token 600 0"; got != want {
			t.Fatalf("%s: secrets\n%s", label, got)
		}
	}
	restored("disaster")

	// AC6: stopped, without --replace: refused, nothing changed. With it: restored, the
	// previous data moved aside.
	docker(t, "compose", "-p", "kyq-kydrive", "stop")
	listing = dirDigest(t, drive.name, drive.root+"/kydrive")
	o, err = kyq(t, ctx, state, catalogDir, trust, "", "restore", "kydrive", "--snapshot", id, "--yes")
	outputs = append(outputs, o)
	refused(t, err, "rerun with --replace to move them aside")
	if after := dirDigest(t, drive.name, drive.root+"/kydrive"); after != listing {
		t.Fatalf("a refused restore changed KyDrive's directory:\n%s\nwas:\n%s", after, listing)
	}
	dataBefore := dirDigest(t, drive.name, drive.root+"/kydrive/data")
	o = restore("replace", id, "kydrive\n", "--replace")
	m := regexp.MustCompile(`Moved the current data and secrets aside to (\S+)\.\n`).FindStringSubmatch(o)
	if m == nil || !strings.HasPrefix(m[1], drive.root+"/kydrive/.replaced-") {
		t.Fatal("--replace did not print where the previous data went")
	}
	if got := dirDigest(t, drive.name, m[1]+"/data"); got != dataBefore {
		t.Fatalf("the moved-aside data differs:\n%s\nwas:\n%s", got, dataBefore)
	}
	if got := docker(t, "exec", drive.name, "sh", "-c", `cd "$1" && ls -d backups secrets/session_secret secrets/scim_token`, "sh", m[1]); got != "backups\nsecrets/scim_token\nsecrets/session_secret" {
		t.Fatalf("moved aside: %s", got)
	}
	restored("replace")

	// AC15: L and D are assigned in KyIdentity and provisioned in KyDrive by its connector; L
	// signs in. Snapshot S is taken while both are active.
	appID := admin.app("kydrive")
	pwL, pwD := randomPassword(), randomPassword()
	leaver, departed := admin.createUser("leaver", pwL), admin.createUser("departed", pwD)
	admin.assign(appID, leaver)
	admin.assign(appID, departed)
	active := func(u scimUser) bool { return u.Active && u.ExternalID != "" }
	lID := scim.wait("leaver", active, "active").ID
	dID := scim.wait("departed", active, "active").ID
	lSession := driveLogin(t, ctx, transport, "leaver", []byte(pwL), false)
	driveLogin(t, ctx, transport, "departed", []byte(pwD), false)
	s, backupOut := backupDrive(t, ctx, state, catalogDir, trust, b, nil)
	outputs = append(outputs, backupOut)

	// L is unassigned: KyDrive lists L inactive and L's session is denied.
	admin.change("DELETE", "/api/admin/app-registry/"+appID+"/assignments/users/"+leaver, nil)
	inactive := func(u scimUser) bool { return !u.Active }
	scim.wait("leaver", inactive, "inactive")
	if driveSignedIn(t, ctx, lSession) {
		t.Fatal("L's session survived the unassignment")
	}

	// Disaster; restore S. Restored, L's session row is valid again until the replay arrives:
	// the watch measures how long it stayed signed in.
	disaster()
	watch := watchSession(ctx, lSession)
	restore("removal replay", s, password+"\nkydrive\n")
	first, denied := watch()
	if u := scim.find("leaver"); u == nil || u.ID != lID || u.Active {
		t.Fatalf("L after the restore: %+v (ID was %s)", u, lID)
	}
	if driveSignedIn(t, ctx, lSession) {
		t.Fatal("L's pre-backup session is accepted after the restore")
	}
	// The window: 32.9 s when the replay ran after the setup checks and KyDrive's first health
	// check came after 30 s; 3.8-5.2 s with the replay right after health and a 2 s start
	// interval (decision 12).
	if first.IsZero() {
		t.Log("AC15: L's restored session was never seen signed in: the replayed deactivation arrived before the watch's first answer from KyDrive")
	} else {
		t.Logf("AC15: L stayed signed in for %s, from KyDrive's start (first answer %s) to the replayed deactivation (%s)", denied.Sub(first).Round(10*time.Millisecond), first.Format(time.RFC3339Nano), denied.Format(time.RFC3339Nano))
	}
	if driveLoginRefused(t, ctx, transport, "leaver", pwL) != "authorization" {
		t.Fatal("KyIdentity did not refuse L's fresh sign-in to KyDrive")
	}
	owners()

	// AC15.7: D, deleted in KyIdentity after S, comes back from a second restore of S as a
	// retained, inactive account that cannot sign in.
	admin.change("DELETE", "/api/admin/users/"+departed, nil)
	scim.wait("departed", inactive, "inactive")
	disaster()
	restore("deletion replay", s, password+"\nkydrive\n")
	if u := scim.find("departed"); u == nil || u.ID != dID || u.Active {
		t.Fatalf("D after the restore: %+v (ID was %s)", u, dID)
	}
	if u := scim.find("leaver"); u == nil || u.Active {
		t.Fatalf("L after the second restore: %+v", u)
	}
	if driveLoginRefused(t, ctx, transport, "departed", pwD) != "password" {
		t.Fatal("D signed in to KyIdentity after the deletion")
	}
	owners()

	secrets["restic_password"] = password
	noSecrets(t, state, outputs, secrets)
}

// identity043 copies the catalog's KyIdentity into catalogDir pinned to the attested 0.4.3
// release (v0.4.3^{commit} 65dcae6), and loads that image into kind. Both edits fail on drift.
func identity043(t *testing.T, cluster, catalogDir string) {
	t.Helper()
	dir := filepath.Join(catalogDir, "kyidentity")
	if err := os.CopyFS(dir, os.DirFS("../../internal/catalog/apps/kyidentity")); err != nil {
		t.Fatal(err)
	}
	m, _ := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	// The pin moves with scripts/catalog-follow; whatever it is, it is replaced.
	v := regexp.QuoteMeta(identityVersion(t, m))
	for _, r := range []struct {
		re   *regexp.Regexp
		with string
	}{
		{regexp.MustCompile(`\nversion: ` + v + `\n`), "\nversion: 0.4.3\n"},
		{regexp.MustCompile(`\n  kyidentity: ghcr\.io/busnes-app/kyidentity-server:` + v + `@sha256:[0-9a-f]{64}\n`), "\n  kyidentity: " + identity043Image + "\n"},
	} {
		if len(r.re.FindAllIndex(m, -1)) != 1 {
			t.Fatalf("the catalog's KyIdentity manifest changed; update the 0.4.3 copy for %s", r.re)
		}
		m = r.re.ReplaceAll(m, []byte(r.with))
	}
	os.WriteFile(filepath.Join(dir, "manifest.yaml"), m, 0o600)
	kindLoad(t, cluster, identity043Image)
}

// identityVersion is the version line of KyIdentity's catalog manifest m.
func identityVersion(t *testing.T, m []byte) string {
	t.Helper()
	v := regexp.MustCompile(`(?m)^version: (\S+)$`).FindSubmatch(m)
	if v == nil {
		t.Fatal("KyIdentity's catalog manifest has no version line")
	}
	return string(v[1])
}

const identity043Image = "ghcr.io/busnes-app/kyidentity-server:0.4.3@sha256:dbf81b355d6307f23b3501c40ad368b06f9ea2fdfdd6e1d09f40d5de87bbc419"

// upgradeIdentity puts the catalog's own KyIdentity manifest back and upgrades to it with
// kyquickstart upgrade; the test CA trust survives or is restored.
func upgradeIdentity(t *testing.T, ctx context.Context, cs kubernetes.Interface, c *kube.Client, cluster, state, catalogDir string, trust []string, certPEM []byte, outputs *[]string) {
	t.Helper()
	real, err := os.ReadFile("../../internal/catalog/apps/kyidentity/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(catalogDir, "kyidentity", "manifest.yaml"), real, 0o600)
	loadCatalogApp(t, cluster, cs, "kyidentity")
	start := time.Now()
	out, err := kyq(t, ctx, state, catalogDir, trust, "", "upgrade", "kyidentity", "--acknowledge-no-backup")
	*outputs = append(*outputs, out)
	if err != nil {
		t.Fatalf("upgrade kyidentity: %v", err)
	}
	if !strings.Contains(out, "check each app's accounts for people disabled") {
		t.Errorf("upgrade kyidentity did not print its upgrade note:\n%s", out)
	}
	ns, err := cs.CoreV1().Namespaces().Get(ctx, "kyq-kyidentity", metav1.GetOptions{})
	if err != nil || ns.Labels[managed.VersionLabel] != identityVersion(t, real) {
		t.Fatalf("KyIdentity's deployment record after the upgrade: %v %v", ns.Labels, err)
	}
	t.Logf("kyquickstart upgrade kyidentity 0.4.3 -> %s took %s", identityVersion(t, real), time.Since(start).Round(time.Second))
	dep, err := cs.AppsV1().Deployments("kyq-kyidentity").Get(ctx, "app", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(dep.Spec.Template.Spec.Containers[0].Env, func(e corev1.EnvVar) bool { return e.Name == "SSL_CERT_FILE" }) {
		t.Log("the upgrade dropped the test-only CA trust; restoring it")
		trustTestCA(t, ctx, cs, c, "kyidentity", certPEM)
	}
}

func randomPassword() string {
	b := make([]byte, 18)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func installationID(t *testing.T, plan string) string {
	t.Helper()
	st, err := stack.Load(plan)
	if err != nil || st.InstallationID == "" {
		t.Fatalf("no installation_id: %v", err)
	}
	return st.InstallationID
}

// handoverPassword runs the handover's password command, ssh -p <port> <user>@<host> cat
// <file>, over SSH as the owner would, and returns the password.
func handoverPassword(t *testing.T, ctx context.Context, state, out string, nas stack.Target) string {
	t.Helper()
	m := regexp.MustCompile(`cannot be restored without it\): ssh -p (\d+) (\S+)@(\S+) cat (\S+)\n`).FindStringSubmatch(out)
	if m == nil || m[1] != strconv.Itoa(nas.Port) || m[2] != nas.User || m[3] != nas.Host {
		t.Fatal("no password command in the handover")
	}
	c, err := remote.Dial(ctx, remote.SSHConfig{Name: nas.Name, Host: nas.Host, Port: nas.Port, User: nas.User, KnownHosts: filepath.Join(state, "known_hosts")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	pw, err := c.Run(ctx, "cat "+remote.Quote(m[4]), nil)
	if err != nil || len(pw) != 43 {
		t.Fatalf("the handover's password command: %v (%d bytes)", err, len(pw))
	}
	return string(pw)
}

type scimUser struct {
	ID          string `json:"id"`
	ExternalID  string `json:"externalId"`
	UserName    string `json:"userName"`
	DisplayName string `json:"displayName"`
	Active      bool   `json:"active"`
}

// sameUsers compares SCIM lists by ID, externalId, userName and active; display names follow
// KyIdentity's later profile changes.
func sameUsers(a, b []scimUser) bool {
	return slices.EqualFunc(a, b, func(x, y scimUser) bool {
		return x.ID == y.ID && x.ExternalID == y.ExternalID && x.UserName == y.UserName && x.Active == y.Active
	})
}

// driveSCIM reads KyDrive's SCIM users with its token; the test never writes there.
type driveSCIM struct {
	t     *testing.T
	ctx   context.Context
	c     *http.Client
	token string
}

func (s *driveSCIM) list() []scimUser {
	s.t.Helper()
	req, _ := http.NewRequestWithContext(s.ctx, "GET", "https://"+catalogDriveURLHost+"/scim/v2/Users?count=200", nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	r, err := s.c.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer r.Body.Close()
	var list struct{ Resources []scimUser }
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if r.StatusCode != 200 || json.Unmarshal(b, &list) != nil {
		s.t.Fatalf("KyDrive SCIM users: %d %s", r.StatusCode, b)
	}
	return list.Resources
}

func (s *driveSCIM) find(username string) *scimUser {
	s.t.Helper()
	for _, u := range s.list() {
		if u.UserName == username {
			return &u
		}
	}
	return nil
}

// wait waits until KyDrive lists username matching ok; KyIdentity retries failed deliveries.
func (s *driveSCIM) wait(username string, ok func(scimUser) bool, what string) scimUser {
	s.t.Helper()
	start := time.Now()
	for {
		if u := s.find(username); u != nil && ok(*u) {
			s.t.Logf("KyDrive lists %s %s after %s", username, what, time.Since(start).Round(time.Second))
			return *u
		}
		if time.Since(start) > 5*time.Minute {
			s.t.Fatalf("KyDrive never listed %s %s", username, what)
		}
		time.Sleep(2 * time.Second)
	}
}

// driveRequest sends one request with KyDrive's CSRF cookie as its header.
func driveRequest(t *testing.T, ctx context.Context, c *http.Client, method, path string, body []byte) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, method, "https://"+catalogDriveURLHost+path, bytes.NewReader(body))
	u, _ := url.Parse("https://" + catalogDriveURLHost)
	if jar, ok := c.Jar.(*cookiejar.Jar); ok {
		for _, ck := range jar.Cookies(u) {
			if ck.Name == "__Host-ky_csrf" || ck.Name == "ky_csrf" {
				req.Header.Set("X-CSRF-Token", ck.Value)
			}
		}
	}
	r, err := c.Do(req)
	if err != nil {
		t.Fatalf("KyDrive %s %s: %v", method, path, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	return r.StatusCode, b
}

// driveUpload puts data as name in the signed-in user's personal workspace and returns its ID.
func driveUpload(t *testing.T, ctx context.Context, c *http.Client, name string, data []byte) string {
	t.Helper()
	var ws, file struct{ ID string }
	if code, b := driveRequest(t, ctx, c, "POST", "/api/drive/personal-workspace", []byte("{}")); code/100 != 2 || json.Unmarshal(b, &ws) != nil || ws.ID == "" {
		t.Fatalf("KyDrive personal workspace: %d %s", code, b)
	}
	if code, b := driveRequest(t, ctx, c, "POST", "/api/drive/workspaces/"+ws.ID+"/uploads?name="+url.QueryEscape(name)+"&parent=&revision=0", data); code != 201 || json.Unmarshal(b, &file) != nil || file.ID == "" {
		t.Fatalf("KyDrive upload: %d %s", code, b)
	}
	return file.ID
}

func driveDownload(t *testing.T, ctx context.Context, c *http.Client, id string) []byte {
	t.Helper()
	code, b := driveRequest(t, ctx, c, "GET", "/api/drive/files/"+id+"/download", nil)
	if code != 200 {
		t.Fatalf("KyDrive download: %d", code)
	}
	return b
}

// driveSignedIn reports whether KyDrive accepts the browser's session.
func driveSignedIn(t *testing.T, ctx context.Context, c *http.Client) bool {
	t.Helper()
	code, b := driveRequest(t, ctx, c, "GET", "/api/auth/me", nil)
	var me struct{ Authenticated bool }
	if code != 200 || json.Unmarshal(b, &me) != nil {
		t.Fatalf("KyDrive /api/auth/me: %d %s", code, b)
	}
	return me.Authenticated
}

// watchSession polls KyDrive with c's session until stopped, and reports when it was first
// accepted and when it was first denied after that.
func watchSession(ctx context.Context, c *http.Client) func() (time.Time, time.Time) {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var first, denied time.Time
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
			req, _ := http.NewRequestWithContext(ctx, "GET", "https://"+catalogDriveURLHost+"/api/auth/me", nil)
			r, err := c.Do(req)
			if err != nil {
				continue
			}
			var me struct{ Authenticated bool }
			b, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
			r.Body.Close()
			if r.StatusCode != 200 || json.Unmarshal(b, &me) != nil {
				continue
			}
			now := time.Now()
			switch {
			case me.Authenticated && first.IsZero():
				first = now
			case !me.Authenticated && !first.IsZero() && denied.IsZero():
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

// driveLoginRefused tries username's sign-in to KyDrive through KyIdentity and returns where
// it was refused: "password" (KyIdentity's sign-in), "authorization" (no code for kydrive) or
// "" when it was not.
func driveLoginRefused(t *testing.T, ctx context.Context, transport *http.Transport, username, password string) string {
	t.Helper()
	a := &identityAdmin{t: t, ctx: ctx, password: password}
	jar, _ := cookiejar.New(nil)
	a.c = &http.Client{Transport: transport, Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	a.send("GET", "/api/auth/csrf", nil, "")
	payload, _ := json.Marshal(map[string]string{"username": username, "password": password})
	var login struct{ Success bool }
	if code, b := a.send("POST", "/api/auth/login", payload, ""); code != 200 || json.Unmarshal(b, &login) != nil || !login.Success {
		t.Logf("KyIdentity refused %s's password sign-in: %d", username, code)
		return "password"
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://"+catalogDriveURLHost+"/api/sso/kysignon/login", nil)
	r, err := a.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	authorize, err := r.Location()
	if err != nil {
		t.Fatalf("KyDrive sign-in start: %d", r.StatusCode)
	}
	req, _ = http.NewRequestWithContext(ctx, "GET", authorize.String(), nil)
	r, err = a.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	callback, _ := r.Location()
	if callback == nil || callback.Query().Get("code") == "" {
		t.Logf("KyIdentity refused %s's authorization for kydrive: HTTP %d %v", username, r.StatusCode, callback)
		return "authorization"
	}
	return ""
}
