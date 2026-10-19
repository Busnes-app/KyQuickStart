package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost/fakedocker"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
)

func TestMain(m *testing.M) {
	clean, err := fakedocker.Build()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	clean()
	os.Exit(code)
}

// dumpCatalog is a two-service app whose dump is fakedocker's dump and load.
var dumpCatalog = fstest.MapFS{
	"dumpdemo/manifest.yaml": {Data: []byte("name: dumpdemo\ncategory: ky\nversion: 1.0.0\nimages:\n  whoami: " + whoami + "\nsecrets: [db_password, token]\nhealth:\n  timeout_seconds: 5\n" +
		"backup:\n  service: db\n  command: [dump]\n  restore_command: [load]\n  volumes: [files]\n  skip: [files/cache]\n  capsule_secrets: [db_password]\n  restore_order: [db, web]\n")},
	"dumpdemo/compose.yaml.tmpl": {Data: []byte("services:\n  db:\n    image: {{ .Images.whoami }}\n  web:\n    image: {{ .Images.whoami }}\n    volumes: [\"./files:/files:ro\"]\n")},
}

const installation = "6f1c2d3e-4a5b-4c6d-8e7f-9a0b1c2d3e4f"

// backupFixture is dumpdemo deployed on nas with a destination, docker faked by fakedocker
// and every other command run by a real sh through runner.
func backupFixture(t *testing.T) (*fixture, string, *killRunner) {
	t.Helper()
	f := newFixture(t, ".")
	f.o.Catalog = dumpCatalog
	cred := filepath.Join(f.h.root, "rest-password")
	os.WriteFile(cred, []byte("rest-server-password"), 0o600)
	f.plan(t, "    backup:\n      repository: rest:https://backup.lan/nas\n      credential_file: "+cred+"\ninstallation_id: "+installation+"\napps:\n  - name: dumpdemo\n    target: nas\n")
	dir := filepath.Join(f.h.root, "dumpdemo")
	for _, d := range []string{"secrets", "files/docs", "files/cache"} {
		os.MkdirAll(filepath.Join(dir, d), 0o700)
	}
	os.WriteFile(filepath.Join(dir, "compose.kyq.yaml"), []byte("services:\n  db:\n    labels:\n      "+managed.Label+": "+managed.Value+"\n      "+managed.VersionLabel+": 1.0.0\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "secrets", "db_password"), []byte("a9f2GnkjXjNnjQmSlhOsb9XnNVDeJ5tJRTnqLCj1RZw"), 0o600)
	os.WriteFile(filepath.Join(dir, "files", "docs", "a.txt"), []byte("kept"), 0o600)
	os.WriteFile(filepath.Join(dir, "files", "cache", "x"), []byte("skipped"), 0o600)
	fake := fakedocker.Install(t, "db", "web")
	os.WriteFile(filepath.Join(fake, "dump"), []byte("the dump"), 0o600)
	k := &killRunner{}
	dialTarget = func(_ context.Context, t stack.Target, _ remote.SSHConfig, _ bool) (driver, error) {
		return sshDriver{t: t, c: k}, nil
	}
	return f, fake, k
}

// killRunner runs commands for real, failing its n-th (counting from arm) when n > 0.
type killRunner struct {
	n, calls int
	killed   string
	match    string // also fail every command containing it
}

func (k *killRunner) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	k.calls++
	if k.calls == k.n || k.match != "" && strings.Contains(cmd, k.match) {
		k.killed = cmd
		return nil, errors.New("killed")
	}
	return remote.Local{}.Run(ctx, cmd, stdin)
}
func (k *killRunner) Close() error { return nil }

func (f *fixture) runIn(stdin string, args ...string) error {
	f.out.Reset()
	o := f.o
	o.Stdin = strings.NewReader(stdin)
	return Run(context.Background(), append(args, "--state", f.state), o)
}

// snapshotID returns the newest snapshot ID restore lists, given the password on stdin.
func snapshotID(t *testing.T, f *fixture, stdin string) string {
	t.Helper()
	if err := f.runIn(stdin, "restore", "dumpdemo"); err != nil {
		t.Fatal(err, f.out.String())
	}
	lines := strings.Split(strings.TrimSpace(f.out.String()), "\n")
	if len(lines) < 4 || !strings.HasPrefix(lines[0], "dumpdemo snapshots in rest:https://backup.lan/nas/dumpdemo:") || strings.Fields(lines[1])[0] != "ID" || lines[len(lines)-1] != "Restore one with: kyquickstart restore dumpdemo --snapshot <ID>" {
		t.Fatalf("listing:\n%s", f.out.String())
	}
	return strings.Fields(lines[len(lines)-2])[0]
}

// The CLI's backup and restore over a real sh: dump, cold-free restore order, refusals and
// --replace, with the password lost and given back on stdin.
func TestBackupAndRestoreCommands(t *testing.T) {
	f, fake, _ := backupFixture(t)
	dir := filepath.Join(f.h.root, "dumpdemo")
	if err := f.run("backup", "dumpdemo"); err != nil {
		t.Fatal(err, f.out.String())
	}
	out := f.out.String()
	if !strings.Contains(out, "dumpdemo: snapshot ") || !strings.Contains(out, "This run created dumpdemo's repository password. Read it once into your printed record:\n  ssh -p 22 kyq@192.0.2.1 cat "+dir+"/secrets/restic_password\n") {
		t.Fatalf("backup output:\n%s", out)
	}
	if err := f.run("backup", "dumpdemo"); err != nil || strings.Contains(f.out.String(), "created") {
		t.Fatalf("second backup: %v\n%s", err, f.out.String())
	}
	password, _ := os.ReadFile(filepath.Join(dir, "secrets", "restic_password"))
	id := snapshotID(t, f, "")

	// Refusals change nothing: running, a foreign snapshot, an unconfirmed restore.
	if err := f.runIn("", "restore", "dumpdemo", "--snapshot", id, "--yes"); code(err) != suite.RestoreRefused || !strings.Contains(err.Error(), "is running on nas; stop it first (ssh -p 22 kyq@192.0.2.1 docker compose -p kyq-dumpdemo stop)") {
		t.Fatalf("running: %v", err)
	}
	os.WriteFile(filepath.Join(fake, "compose.json"), []byte(`{"db":false,"web":false}`), 0o600)
	if err := f.runIn("", "restore", "dumpdemo", "--snapshot", id, "--yes"); code(err) != suite.RestoreRefused || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("occupied: %v", err)
	}
	if err := f.runIn("other\n", "restore", "dumpdemo", "--snapshot", id, "--replace"); code(err) != suite.RestoreRefused || err.Error() != `restore_refused: not confirmed: type "dumpdemo" exactly, or pass --yes; nothing was changed` {
		t.Fatalf("unconfirmed: %v", err)
	}
	if err := f.runIn("", "restore", "dumpdemo", "--replace"); code(err) != suite.InvalidRequest || !strings.Contains(err.Error(), "add --snapshot ID") {
		t.Fatalf("--replace without --snapshot: %v", err)
	}
	if err := f.runIn("", "restore", "dumpdemo", "--snapshot", "XYZ"); code(err) != suite.InvalidRequest {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "files", "docs", "a.txt")); err != nil {
		t.Fatal("a refusal changed the app")
	}

	// --replace moves the current data aside and restores; the typed name confirms.
	os.WriteFile(filepath.Join(dir, "files", "docs", "a.txt"), []byte("changed after the backup"), 0o600)
	if err := f.runIn("dumpdemo\n", "restore", "dumpdemo", "--snapshot", id, "--replace"); err != nil {
		t.Fatal(err, f.out.String())
	}
	out = f.out.String()
	for _, want := range []string{"Restore dumpdemo from snapshot ", "\n  taken ", " on nas\n  paths /kyq/dump /kyq/volumes/files /kyq/secrets/db_password\nType dumpdemo to restore it: ", "Moved the current data and secrets aside to " + dir + "/.replaced-", "dumpdemo: dump restored into db.", "dumpdemo: restored from snapshot "} {
		if !strings.Contains(out, want) {
			t.Errorf("restore output lacks %q:\n%s", want, out)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "files", "docs", "a.txt")); string(b) != "kept" {
		t.Errorf("restored file = %q", b)
	}
	aside, _ := filepath.Glob(filepath.Join(dir, ".replaced-*", "files", "docs", "a.txt"))
	if len(aside) != 1 {
		t.Fatal("the replaced data is gone")
	}
	if b, _ := os.ReadFile(filepath.Join(fake, "loaded")); string(b) != "the dump" {
		t.Errorf("loaded = %q", b)
	}
	if up := fakedocker.Running(t, fake); !up["db"] || !up["web"] {
		t.Errorf("not running after restore: %v", up)
	}

	// The disaster: the whole directory, password included. The recorded password comes on stdin.
	os.WriteFile(filepath.Join(fake, "compose.json"), []byte(`{"db":false,"web":false}`), 0o600)
	os.RemoveAll(dir)
	if err := f.runIn("", "restore", "dumpdemo"); code(err) != suite.RestoreRefused || !strings.Contains(err.Error(), "password is not on its target") {
		t.Fatalf("no password: %v", err)
	}
	if got := snapshotID(t, f, string(password)+"\n"); got != id {
		t.Fatalf("listing with the given password = %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".restore-password")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("listing left the given password on the target")
	}
	if err := f.runIn(string(password)+"\ndumpdemo\n", "restore", "dumpdemo", "--snapshot", id); err != nil {
		t.Fatal(err, f.out.String())
	}
	for file, want := range map[string]string{"files/docs/a.txt": "kept", "secrets/db_password": "a9f2GnkjXjNnjQmSlhOsb9XnNVDeJ5tJRTnqLCj1RZw", "secrets/restic_password": string(password)} {
		b, _ := os.ReadFile(filepath.Join(dir, file))
		if string(b) != want {
			t.Errorf("%s = %q", file, b)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "files", "cache")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the skip path came back")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "secrets", "token")); err != nil || len(b) != 43 {
		t.Error("a secret outside capsule_secrets was not generated again")
	}
	if strings.Contains(f.out.String(), string(password)) {
		t.Error("restore printed the password")
	}
}

// AC7: a restore killed at any command converges on rerun, or refuses naming --replace, and a
// rerun with it converges.
func TestRestoreConvergesAfterKill(t *testing.T) {
	f, fake, k := backupFixture(t)
	dir := filepath.Join(f.h.root, "dumpdemo")
	if err := f.run("backup", "dumpdemo"); err != nil {
		t.Fatal(err)
	}
	password, _ := os.ReadFile(filepath.Join(dir, "secrets", "restic_password"))
	id := snapshotID(t, f, "")
	for n := 1; ; n++ {
		os.WriteFile(filepath.Join(fake, "compose.json"), []byte(`{"db":false,"web":false}`), 0o600)
		os.RemoveAll(dir)
		stdin := string(password) + "\n"
		k.n, k.calls = n, 0
		err := f.runIn(stdin, "restore", "dumpdemo", "--snapshot", id, "--yes")
		if err == nil && k.calls < n {
			return // every command was killed once
		}
		k.n = 0
		if err != nil { // a kill the engine retried (a transient pull) needs no rerun
			err = f.runIn(stdin, "restore", "dumpdemo", "--snapshot", id, "--yes")
			if code(err) == suite.TargetLocked {
				os.RemoveAll(filepath.Join(f.h.root, ".lock")) // as the message says, once no run is active
				err = f.runIn(stdin, "restore", "dumpdemo", "--snapshot", id, "--yes")
			}
		}
		if err != nil && code(err) == suite.RestoreRefused && strings.Contains(err.Error(), "--replace") {
			os.WriteFile(filepath.Join(fake, "compose.json"), []byte(`{"db":false,"web":false}`), 0o600)
			err = f.runIn(stdin, "restore", "dumpdemo", "--snapshot", id, "--yes", "--replace")
		}
		// A kill after the restore finished (releasing the lock) leaves a running, restored app.
		if err != nil && !(code(err) == suite.RestoreRefused && strings.Contains(err.Error(), "is running")) {
			t.Fatalf("n %d: rerun after killing %s: %v\n%s", n, k.killed, err, f.out.String())
		}
		b, _ := os.ReadFile(filepath.Join(dir, "files", "docs", "a.txt"))
		pw, _ := os.ReadFile(filepath.Join(dir, "secrets", "restic_password"))
		pending, _ := filepath.Glob(filepath.Join(dir, ".restore*"))
		if string(b) != "kept" || string(pw) != string(password) || len(pending) != 0 || !fakedocker.Running(t, fake)["web"] {
			t.Fatalf("n %d: not converged: file %q, password ok %v, left %v", n, b, string(pw) == string(password), pending)
		}
	}
}

// Backup and restore refuse before connecting what they cannot cover.
func TestBackupRefusals(t *testing.T) {
	f, _, _ := backupFixture(t)
	dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
		t.Fatal("connected")
		return nil, nil
	}
	if err := f.run("backup", "nope"); code(err) != suite.UnknownApp {
		t.Errorf("unknown app: %v", err)
	}
	if err := f.run("backup", "dumpdemo", "extra"); !errors.Is(err, ErrUsage) {
		t.Errorf("two apps: %v", err)
	}
	f.plan(t, "installation_id: "+installation+"\napps:\n  - name: dumpdemo\n    target: nas\n")
	if err := f.run("backup", "dumpdemo"); code(err) != suite.BackupUnconfigured || !strings.Contains(err.Error(), "no backup destination") {
		t.Errorf("no destination: %v", err)
	}
	named := fstest.MapFS{
		"named/manifest.yaml":     {Data: []byte("name: named\ncategory: ky\nversion: 1.0.0\nimages:\n  whoami: " + whoami + "\nhealth:\n  timeout_seconds: 5\nbackup:\n  volumes: [data]\n")},
		"named/compose.yaml.tmpl": {Data: []byte("services:\n  web:\n    image: {{ .Images.whoami }}\n    volumes: [\"data:/data\"]\nvolumes:\n  data: {}\n")},
	}
	f.o.Catalog = named
	f.plan(t, "    backup:\n      repository: rest:https://backup.lan/nas\n      credential_file: /a\ninstallation_id: "+installation+"\napps:\n  - name: named\n    target: nas\n")
	if err := f.run("backup", "named"); code(err) != suite.BackupUnsupported {
		t.Errorf("named volume: %v", err)
	}
}

// AC5a: the removal-replay gate.
func TestRemovalGate(t *testing.T) {
	gated := catalog.App{Manifest: catalog.Manifest{OIDC: &catalog.OIDC{}}}
	for _, c := range []struct {
		app      catalog.App
		existing bool
		d        managed.Deployed
		want     string
	}{
		{gated, false, managed.Deployed{Found: true, Version: "0.4.3"}, "restore needs KyIdentity v0.4.4 or later, which replays removals on resync; installed: 0.4.3. Upgrade KyIdentity first"},
		{gated, false, managed.Deployed{Found: true, Version: "0.4.4"}, ""},
		{gated, false, managed.Deployed{Found: true, Version: "0.5.0"}, ""},
		{gated, false, managed.Deployed{}, "installed: unknown (no deployment record)"},
		{gated, true, managed.Deployed{}, "installed: unknown (an existing KyIdentity this plan does not deploy)"},
		{catalog.App{}, false, managed.Deployed{}, ""},
	} {
		err := removalGate(c.app, c.existing, c.d)
		if c.want == "" && err != nil || c.want != "" && (code(err) != suite.RestoreRefused || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%+v: %v", c.d, err)
		}
	}
}

// AC8: the handover's backup lines for a dump app on a target with a destination; none
// without one.
func TestBackupHandoverLines(t *testing.T) {
	f, _, _ := backupFixture(t)
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	want := "\ndumpdemo backups: kyquickstart backup dumpdemo takes an encrypted snapshot to rest:https://backup.lan/nas/dumpdemo; nothing runs on a schedule.\n" +
		"  After the first backup, read its repository password once into your printed record (a lost\n" +
		"  target cannot be restored without it): ssh -p 22 kyq@192.0.2.1 cat " + f.h.root + "/dumpdemo/secrets/restic_password\n" +
		"  Restore: kyquickstart restore dumpdemo lists its snapshots; add --snapshot ID to restore one.\n"
	if s.handover != want {
		t.Fatalf("handover = %q", s.handover)
	}
	f.plan(t, "installation_id: "+installation+"\napps:\n  - name: dumpdemo\n    target: nas\n")
	if s, _ = load(f.state, nil, f.o); s.handover != "" {
		t.Fatalf("handover without a destination = %q", s.handover)
	}
}

func TestUsageListsBackupAndRestore(t *testing.T) {
	for _, want := range []string{"\n  backup APP [--state DIR] ", "\n  restore APP [--state DIR] [--snapshot ID [--replace] [--yes]]"} {
		if !strings.Contains(Usage, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"restore"}, Options{Out: &out}); !errors.Is(err, ErrUsage) {
		t.Errorf("restore without an app: %v", err)
	}
}

// AC8/H6: restore's replay runs KyIdentity's resync-system for the app's client and succeeds
// only on delivered; anything else is restore_incomplete naming the connector.
func TestReplayRemovals(t *testing.T) {
	ctx := context.Background()
	for status, want := range map[string]suite.Code{"delivered": "", "failed": suite.RestoreIncomplete, "timeout": suite.RestoreIncomplete, "blocked": suite.RestoreIncomplete} {
		f := newFixture(t, "../catalog/testdata/apps")
		f.o.Catalog = catalog.Embedded()
		drivePlan(t, f, "192.0.2.1")
		s, err := load(f.state, nil, f.o)
		if err != nil {
			t.Fatal(err)
		}
		f.cs.CoreV1().Pods("kyq-kyidentity").Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "app-1", Labels: map[string]string{"app.kubernetes.io/name": "kyidentity", managed.Label: managed.Value}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
		}, metav1.CreateOptions{})
		var got []string
		s.drivers["k1"] = kubeDriver{c: kube.Fake(f.cs, "https://k1.test", func(_ context.Context, _, _, _ string, args []string, o remotecommand.StreamOptions) error {
			got = args
			io.WriteString(o.Stdout, `{"schema":"kyidentity.resync-system/1","clientId":"kydrive","systemId":"s1","status":"`+status+`","pending":2,"failed":1}`+"\n")
			if status != "delivered" {
				return errors.New("exit status 1")
			}
			return nil
		})}
		err = s.replayRemovals(ctx, "kydrive")
		if !slices.Equal(got, []string{identityBinary, "resync-system", "--client", "kydrive", "--wait", "10m0s"}) {
			t.Fatalf("exec = %v", got)
		}
		if want == "" && err != nil || want != "" && (code(err) != want || !strings.Contains(err.Error(), "KyIdentity's kydrive connector did not deliver the replay of removals ("+status+", 2 pending, 1 failed)") || !strings.Contains(err.Error(), "rerun kyquickstart restore kydrive")) {
			t.Errorf("%s: %v", status, err)
		}
	}
}

// Decision 11: when KyIdentity does not deliver the replay, restore stops the restored app
// (restore_incomplete); a rerun resumes, starts it and replays again.
// failedReplay restores dumpdemo, made to sign in through KyIdentity (cold: without its
// dump command), and KyIdentity does not deliver the replay. delivered makes it deliver.
func failedReplay(t *testing.T, cold bool) (f *fixture, fake string, k *killRunner, id string, delivered func()) {
	t.Helper()
	ctx := context.Background()
	f, fake, k = backupFixture(t)
	gated := fstest.MapFS{}
	for name, file := range dumpCatalog {
		gated[name] = file
	}
	if cold {
		m := strings.Replace(string(dumpCatalog["dumpdemo/manifest.yaml"].Data), "  service: db\n  command: [dump]\n  restore_command: [load]\n", "", 1)
		gated["dumpdemo/manifest.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(m, "  restore_order: [db, web]\n", "", 1))}
	}
	gated["dumpdemo/manifest.yaml"] = &fstest.MapFile{Data: append(append([]byte{}, gated["dumpdemo/manifest.yaml"].Data...), "oidc:\n  name: Dump\n  redirect_paths: [/cb]\n  admin_role: dump.admin\n"...)}
	for _, app := range []string{"kyidentity", "npm"} {
		b, err := fs.ReadFile(catalog.Embedded(), app+"/manifest.yaml")
		if err != nil {
			t.Fatal(err)
		}
		gated[app+"/manifest.yaml"] = &fstest.MapFile{Data: b}
	}
	f.o.Catalog = gated
	plan, _ := os.ReadFile(filepath.Join(f.state, "stack.yaml"))
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	y := strings.Replace(string(plan), "installation_id:", "  - name: k1\n    kubeconfig: "+kc+"\ninstallation_id:", 1)
	y = strings.Replace(y, "apps:\n", "apps:\n  - name: npm\n    target: k1\n  - name: kyidentity\n    target: k1\n", 1) +
		"edges:\n  - target: k1\n    provider: lan\n    npm_url: kube://kyq-npm/app:81\n    allow_cidrs: [192.168.1.0/24]\n    routes:\n" +
		"      - app: kyidentity\n        hostname: identity.example.com\n        forward_host: app.kyq-kyidentity.svc.cluster.local\n        forward_port: 5867\n        certificate_id: 1\n"
	os.WriteFile(filepath.Join(f.state, "stack.yaml"), []byte(y), 0o600)
	f.cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kyq-kyidentity", Labels: map[string]string{managed.Label: managed.Value, managed.VersionLabel: "0.4.4"}}}, metav1.CreateOptions{})
	f.cs.CoreV1().Pods("kyq-kyidentity").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Labels: map[string]string{"app.kubernetes.io/name": "kyidentity", managed.Label: managed.Value}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}, metav1.CreateOptions{})
	status := "failed"
	identity := kube.Fake(f.cs, "https://k1.test", func(_ context.Context, _, _, _ string, args []string, o remotecommand.StreamOptions) error {
		io.WriteString(o.Stdout, `{"schema":"kyidentity.resync-system/1","clientId":"dumpdemo","systemId":"s1","status":"`+status+`","pending":0,"failed":1}`+"\n")
		if status != "delivered" {
			return errors.New("exit status 1")
		}
		return nil
	})
	dialTarget = func(_ context.Context, t stack.Target, _ remote.SSHConfig, _ bool) (driver, error) {
		if t.Kubernetes() {
			return kubeDriver{c: identity}, nil
		}
		return sshDriver{t: t, c: k}, nil
	}
	dir := filepath.Join(f.h.root, "dumpdemo")
	if err := f.run("backup", "dumpdemo"); err != nil {
		t.Fatal(err, f.out.String())
	}
	password, _ := os.ReadFile(filepath.Join(dir, "secrets", "restic_password"))
	id = snapshotID(t, f, "")
	os.WriteFile(filepath.Join(fake, "compose.json"), []byte(`{"db":false,"web":false}`), 0o600)
	os.RemoveAll(dir)
	err := f.runIn(string(password)+"\n", "restore", "dumpdemo", "--snapshot", id, "--yes")
	if code(err) != suite.RestoreIncomplete || !strings.Contains(err.Error(), "dumpdemo is restored but stopped") {
		t.Fatalf("failed replay: %v\n%s", err, f.out.String())
	}
	if up := fakedocker.Running(t, fake); up["db"] || up["web"] {
		t.Fatalf("the app runs without the replay: %v", up)
	}
	return f, fake, k, id, func() { status = "delivered" }
}

func TestFailedReplayStopsTheApp(t *testing.T) {
	ctx := context.Background()
	f, fake, k, id, delivered := failedReplay(t, false)
	// Apply's own steps for the app (the plan's edges need a live NPM here) refuse to start it.
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	nas := s.placed["dumpdemo"]
	e := &engine.Engine{Dir: filepath.Join(f.state, "results"), Out: io.Discard, Redact: &engine.Redactor{}}
	if err := e.Run(ctx, sshDriver{t: nas, c: k}.steps("dumpdemo", s.cat["dumpdemo"], "test", s.redact)); err == nil || !strings.Contains(err.Error(), "finish it with kyquickstart restore dumpdemo") {
		t.Fatalf("apply over the unfinished restore: %v", err)
	}
	if up := fakedocker.Running(t, fake); up["db"] || up["web"] {
		t.Fatalf("apply started the app without the replay: %v", up)
	}
	delivered()
	if err := f.runIn("", "restore", "dumpdemo", "--snapshot", id, "--yes"); err != nil {
		t.Fatal(err, f.out.String())
	}
	if up := fakedocker.Running(t, fake); !up["db"] || !up["web"] || !strings.Contains(f.out.String(), "Resuming the restore of dumpdemo") {
		t.Fatalf("rerun did not resume and start the app: %v\n%s", up, f.out.String())
	}
}

// A cold backup says it stops the app before it does, and whether it runs again after a failure.
func TestColdBackupSaysItStops(t *testing.T) {
	f, _, k := backupFixture(t)
	cold := fstest.MapFS{"dumpdemo/compose.yaml.tmpl": dumpCatalog["dumpdemo/compose.yaml.tmpl"]}
	m := strings.Replace(string(dumpCatalog["dumpdemo/manifest.yaml"].Data), "  service: db\n  command: [dump]\n  restore_command: [load]\n", "", 1)
	cold["dumpdemo/manifest.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(m, "  restore_order: [db, web]\n", "", 1))}
	f.o.Catalog = cold
	if err := f.run("backup", "dumpdemo"); err != nil || !strings.HasPrefix(f.out.String(), "dumpdemo: stopping for a cold backup; it starts again when the snapshot ends.\n") {
		t.Fatalf("cold backup: %v\n%s", err, f.out.String())
	}
	k.match = "'backup' '--json'" // restic backup, after the stop
	defer func() { k.match = "" }()
	if err := f.run("backup", "dumpdemo"); err == nil || !strings.Contains(f.out.String(), "dumpdemo is running again.") {
		t.Fatalf("failed cold backup: %v\n%s", err, f.out.String())
	}
}

// Round 3: backup and unmanage refuse an unfinished restore too, before they stop, start or
// hand over anything (a cold backup would otherwise start the unreplayed app); restore then
// still resumes.
func TestUnfinishedRestoreRefusesBackupAndUnmanage(t *testing.T) {
	f, fake, _, id, delivered := failedReplay(t, true)
	for _, cmd := range []string{"backup", "unmanage"} {
		if err := f.run(cmd, "dumpdemo"); err == nil || !strings.Contains(err.Error(), "a restore of dumpdemo is unfinished and its removal replay not delivered; finish it with kyquickstart restore dumpdemo") {
			t.Fatalf("%s over the unfinished restore: %v\n%s", cmd, err, f.out.String())
		}
		if up := fakedocker.Running(t, fake); up["db"] || up["web"] {
			t.Fatalf("%s started the app without the replay: %v", cmd, up)
		}
	}
	if _, err := os.Stat(filepath.Join(f.state, "handover.md")); !os.IsNotExist(err) || len(f.stack(t).Apps) != 3 {
		t.Fatal("the refused unmanage dropped the app or recorded a handover")
	}
	delivered()
	if err := f.runIn("", "restore", "dumpdemo", "--snapshot", id, "--yes"); err != nil || !strings.Contains(f.out.String(), "Resuming the restore of dumpdemo") {
		t.Fatalf("rerun: %v\n%s", err, f.out.String())
	}
	if up := fakedocker.Running(t, fake); !up["db"] || !up["web"] {
		t.Fatalf("not running after the rerun: %v", up)
	}
}
