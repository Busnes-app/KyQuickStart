package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	driveSetupID = "3c5a7e91-2b4d-4f6a-8c1e-9d0b2a4c6e8f"
	driveBundle  = "/data/installer/" + driveSetupID + "/setup.json"
	driveSecret  = "RHJpdmVTZWNyZXREcml2ZVNlY3JldERyaXZlU2VjcmU"
)

// drivePlan installs KyIdentity on cluster k1 and KyDrive on Docker host nas, both routed
// through k1's lan edge, with KyDrive's client in KyIdentity's first setup. forward is the
// address the edge forwards KyDrive to.
func drivePlan(t *testing.T, f *fixture, forward string) {
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	f.plan(t, "  - name: k1\n    kubeconfig: "+kc+"\napps:\n  - name: npm\n    target: k1\n  - name: kyidentity\n    target: k1\n  - name: kydrive\n    target: nas\n"+
		"edges:\n  - target: k1\n    provider: lan\n    npm_url: kube://kyq-npm/app:81\n    allow_cidrs: [192.168.1.0/24]\n    routes:\n"+
		"      - app: kyidentity\n        hostname: identity.example.com\n        forward_host: app.kyq-kyidentity.svc.cluster.local\n        forward_port: 5867\n        certificate_id: 1\n"+
		"      - app: kydrive\n        hostname: drive.example.com\n        forward_host: "+forward+"\n        forward_port: 18443\n        certificate_id: 1\n"+
		"integrations:\n  - product: kyidentity\n    target: k1\n    bundle: /data/installer/271333e2-c221-4601-b249-563b3d828530/setup.json\n"+identityPrep+
		"  - product: kydrive\n    target: nas\n    bundle: "+driveBundle+"\n    identity_client: kydrive\n")
}

func driveExport(secret string) string {
	return `{"schema":"kyidentity.client-credentials/1","setupId":"271333e2-c221-4601-b249-563b3d828530","issuerUrl":"https://identity.example.com","clientId":"kydrive",` +
		`"redirectUris":["https://drive.example.com/api/sso/kysignon/callback"],"clientSecret":"` + secret + `",` +
		`"administrator":{"subject":"5e0c8a52-6d1b-4f7e-9a3c-2b1d0e9f8a7c","username":"owner-admin","role":"kydrive.admin"}}`
}

// fakeDrive is KyDrive's container behind docker exec: its shell's files and its apply-setup.
type fakeDrive struct {
	files   map[string]string
	applied bool
	bundle  string
	users   []string // --user of each exec
}

// words parses the shell words after the container: the real shell does the unquoting.
func words(t *testing.T, cmd string) []string {
	t.Helper()
	prefix := dockerAppContainer("kydrive")
	rest, ok := strings.CutPrefix(cmd, prefix)
	if !ok {
		t.Fatalf("exec without finding KyDrive's container: %s", cmd)
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
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
}

func (d *fakeDrive) exec(t *testing.T) func(string, []byte) ([]byte, error) {
	return func(cmd string, stdin []byte) ([]byte, error) {
		args := words(t, cmd)
		switch {
		case args[0] == "sh":
			if args[1] != "-c" || args[2] != placeScript {
				return nil, errors.New("unexpected script")
			}
			// placeScript's semantics, which TestPlaceScript proves under a real sh.
			mode, path := args[4], args[5]
			old, ok := d.files[path]
			switch {
			case mode == "replace":
			case ok && old != string(stdin):
				return nil, errors.New("differs")
			case ok:
				return nil, nil
			case mode != "write":
				return nil, errors.New("missing")
			}
			d.files[path] = string(stdin)
			return nil, nil
		case args[0] != "/app/kydrive-server":
			return nil, errors.New("unexpected binary")
		case slices.Contains(args, "--capabilities"):
			return []byte(`{"schema":"ky.setup-capabilities/1","service":"kydrive","bundleSchema":"ky.product-setup/1","features":["kyidentity-oidc","scim","dedicated-admin","local-backup-config","bundle-preparation","check","restart"]}`), nil
		}
		check := slices.Contains(args, "--check")
		d.bundle = d.files[args[slices.Index(args, "--file")+1]]
		if d.bundle == "" || check && !d.applied {
			return nil, errors.New("not applied")
		}
		created := `[]`
		if !check && !d.applied {
			d.applied, created = true, `[{"kind":"configuration","name":"sso"}]`
		}
		return []byte(`{"schema":"ky.product-setup-report/1","service":"kydrive","setupId":"` + driveSetupID + `","created":` + created + `,"present":[],"pending":[]}`), nil
	}
}

func driveSteps(t *testing.T, f *fixture, identity *fakeIdentity, drive *fakeDrive) (*session, map[string]engine.Step) {
	t.Helper()
	ctx := context.Background()
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	f.cs.CoreV1().Pods("kyq-kyidentity").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Labels: map[string]string{"app.kubernetes.io/name": "kyidentity", managed.Label: managed.Value}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}, metav1.CreateOptions{})
	s.drivers["k1"] = kubeDriver{c: kube.Fake(f.cs, "https://k1.test", identity.stream)}
	s.drivers["nas"] = sshDriver{t: s.placed["kydrive"], c: f.h}
	f.h.exec = drive.exec(t)
	s.edges = nil
	list, err := s.deploySteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]engine.Step{}
	var ids []string
	for _, st := range list {
		steps[st.ID()], ids = st, append(ids, st.ID())
	}
	if i := slices.Index(ids, "kydrive.setup"); i < 0 || ids[i-1] != "kydrive.health" || slices.Index(ids, "kyidentity.setup") > i {
		t.Fatalf("steps = %v", ids)
	}
	return s, steps
}

// KyDrive on a Docker host signs in through a KyIdentity in a cluster: its client joins
// KyIdentity's first setup, and its setup execs into its own container over SSH, where the
// client secret KyIdentity exported on the cluster lands without touching the workstation.
func TestKyDriveOnDockerSetup(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	drivePlan(t, f, "192.0.2.1")
	identity := &fakeIdentity{export: driveExport(driveSecret)}
	drive := &fakeDrive{files: map[string]string{}}
	s, steps := driveSteps(t, f, identity, drive)

	// The compose file publishes on the forward address and names the route's URL.
	c := string(s.cat["kydrive"].Compose)
	if !strings.Contains(c, `ports: ["192.0.2.1:18443:8080"]`) || !strings.Contains(c, `KY_APP_URL: "https://drive.example.com"`) || s.cat["kydrive"].UsesRoute() {
		t.Fatalf("compose:\n%s", c)
	}
	const wantClient = `{"id":"kydrive","name":"KyDrive","redirectUris":["https://drive.example.com/api/sso/kysignon/callback"],"postLogoutUris":null,"administratorRole":"kydrive.admin","everydayRole":"kydrive.user"}`
	if b, _ := json.Marshal(s.integrations[0].Identity.Clients); string(b) != "["+wantClient+"]" {
		t.Fatalf("derived clients = %s", b)
	}
	if !strings.Contains(s.handover, "https://drive.example.com/scim/v2") || !strings.Contains(s.handover, f.h.root+"/kydrive/data/initial-admin-password on nas") {
		t.Fatalf("handover = %q", s.handover)
	}

	if err := steps["kyidentity.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(identity.intent, wantClient) {
		t.Fatalf("KyIdentity intent lacks the client: %s", identity.intent)
	}
	setup := steps["kydrive.setup"]
	if done, _ := setup.Inspect(ctx); done {
		t.Fatal("fresh KyDrive inspected as done")
	}
	if err := setup.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if done, _ := setup.Inspect(ctx); !done {
		t.Fatal("applied setup not inspected as done")
	}
	dir := filepath.Dir(driveBundle)
	var b struct {
		SSO    map[string]string
		Admins []map[string]string
	}
	if json.Unmarshal([]byte(drive.bundle), &b) != nil || b.SSO["clientSecretFile"] != dir+"/client.secret" || b.SSO["scimTokenFile"] != "/run/secrets/scim_token" ||
		b.SSO["redirectUri"] != "https://drive.example.com/api/sso/kysignon/callback" || len(b.SSO) != 5 || b.Admins[0]["username"] != "owner-admin" {
		t.Fatalf("bundle = %s", drive.bundle)
	}
	if drive.files[dir+"/client.secret"] != driveSecret || len(drive.files) != 2 {
		t.Fatalf("files = %v", slices.Collect(func(yield func(string) bool) {
			for k := range drive.files {
				yield(k)
			}
		}))
	}
	// The secret crossed in memory: not in apply's output, the state directory or the host root.
	for _, dir := range []string{f.state, f.h.root} {
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if b, _ := os.ReadFile(p); err == nil && !d.IsDir() && bytes.Contains(b, []byte(driveSecret)) {
				t.Errorf("client secret written to %s", p)
			}
			return nil
		})
	}
	if strings.Contains(f.out.String(), driveSecret) {
		t.Error("client secret printed")
	}

	// A changed source credential is refused and the retained file kept.
	identity.export = driveExport("Q2hhbmdlZENoYW5nZWRDaGFuZ2VkQ2hhbmdlZENoYW4")
	if err := setup.Apply(ctx); err == nil || !strings.Contains(err.Error(), "client.secret") || drive.files[dir+"/client.secret"] != driveSecret {
		t.Fatalf("changed client secret: %v", err)
	}
}

func TestKyDriveForwardMustBeAnAddress(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	for _, forward := range []string{"nas.example.com", "0.0.0.0"} {
		drivePlan(t, f, forward)
		if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), "must be an IP address of nas") {
			t.Errorf("forward_host %s: %v", forward, err)
		}
	}
}

// The container lookup takes the app's one running managed container, run for real under sh
// with a docker that answers like docker ps.
func TestDockerAppContainer(t *testing.T) {
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nprintf '%s' \"$IDS\"\n"), 0o700)
	for ids, want := range map[string]string{"c0ffee\n": "c0ffee", "": "", "c0ffee\nbadd00d\n": "", "c0ffee; rm\n": ""} {
		out, err := remote.Local{}.Run(context.Background(), "PATH="+remote.Quote(bin)+":$PATH IDS="+remote.Quote(ids)+"; export PATH IDS; "+dockerAppContainer("kydrive")+`printf %s "$c"`, nil)
		if want == "" && err == nil || want != "" && (err != nil || string(out) != want) {
			t.Errorf("ids %q: %q %v", ids, out, err)
		}
	}
}

// The live path: KyDrive on a Docker host joins a KyIdentity on a cluster whose first setup
// already ran, through a setup amendment, and its export reads that amendment.
func TestKyDriveOnDockerAddedByAmendment(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	identity := setUpIdentity(t, f, amendmentFeatures)
	drivePlan(t, f, "192.0.2.1")
	id := amendmentID(parentSetup, "kydrive")
	bundle := "/data/installer/" + id + "/setup.json"
	amendment := &fakeIdentity{parent: identity, export: strings.Replace(driveExport(driveSecret), parentSetup, id, 1)}
	identity.setups[bundle] = amendment
	drive := &fakeDrive{files: map[string]string{}}

	s := runningSession(t, f, identity, &fakeRecovery{files: map[string]string{}})
	s.drivers["nas"] = sshDriver{t: s.placed["kydrive"], c: f.h}
	f.h.exec = drive.exec(t)
	if _, err := s.amendLate(ctx, f.state, nil); err != nil || len(s.amendments) != 1 || s.amendments[0].app != "kydrive" {
		t.Fatalf("no amendment recorded: %v", err)
	}
	steps := setupStepsOf(t, s)
	if got := strings.Join(ids(steps), " "); got != "kyidentity.setup kyidentity.amend.kydrive kyidentity.retire-bootstrap kydrive.setup" {
		t.Fatalf("steps = %s", got)
	}
	var out bytes.Buffer
	e := &engine.Engine{Dir: t.TempDir(), Out: &out, Redact: &engine.Redactor{}}
	if err := e.Run(ctx, steps); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !slices.Contains(amendment.calls, "/usr/local/bin/kyidentity apply-setup --file "+bundle+" --export-client kydrive") || !drive.applied || drive.files[filepath.Dir(driveBundle)+"/client.secret"] != driveSecret {
		t.Fatalf("export calls %q, KyDrive applied %v", amendment.calls, drive.applied)
	}
}
