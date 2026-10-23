package dockerhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost/fakedocker"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
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

const (
	restSecret = "rest-server-password-0123456789"
	dbSecret   = "db-password-0123456789abcdefghij"
)

// backupFixture is an app at a real root on the local filesystem: one bind volume with a
// skip path, a db_password secret and, with dump, a dump command in service db.
func backupFixture(t *testing.T, dump bool) (Store, string) {
	t.Helper()
	root := realTemp(t)
	cred := filepath.Join(root, "rest-password")
	os.WriteFile(cred, []byte(restSecret), 0o600)
	b := &catalog.Backup{Volumes: []string{"files"}, Skip: []string{"files/cache"}, CapsuleSecrets: []string{"db_password"}}
	services := []string{"app"}
	if dump {
		b.Service, b.Command, b.RestoreCommand, b.RestoreOrder = "db", []string{"dump", "--clean"}, []string{"load"}, []string{"db", "app"}
		services = []string{"app", "db"}
	}
	app := catalog.App{Manifest: catalog.Manifest{Name: "fixture", Version: "1.2.3", Secrets: []string{"db_password", "other"}, Backup: b, Health: catalog.Health{TimeoutSeconds: 5}}, Services: services, Binds: []string{"files"}}
	dir := filepath.Join(root, "fixture")
	for _, d := range []string{dir, filepath.Join(dir, "secrets"), filepath.Join(dir, "files", "cache"), filepath.Join(dir, "files", "docs")} {
		os.MkdirAll(d, 0o700)
	}
	os.WriteFile(filepath.Join(dir, "secrets", "db_password"), []byte(dbSecret), 0o600)
	os.WriteFile(filepath.Join(dir, "files", "docs", "a.txt"), []byte("kept"), 0o600)
	os.WriteFile(filepath.Join(dir, "files", "cache", "tmp"), []byte("skipped"), 0o600)
	fake := fakedocker.Install(t, services...)
	os.WriteFile(filepath.Join(fake, "dump"), []byte("CREATE TABLE t; INSERT 1;"), 0o600)
	st := Store{
		App:  App{Name: "fixture", Root: root, Runner: remote.Local{}, Catalog: app},
		Dest: stack.BackupDestination{Repository: "rest:https://backup.lan:8000/nas", CredentialFile: cred, CAFile: cred},
		Host: "nas", Installation: "6f1c2d3e-4a5b-4c6d-8e7f-9a0b1c2d3e4f", Version: "1.2.3",
	}
	return st, fake
}

// AC3: every restic run is confined and pinned, and no argv or container env holds a
// password; restic_password is never a backup path.
func TestResticRunsConfined(t *testing.T) {
	st, fake := backupFixture(t, true)
	ctx := context.Background()
	id, created, err := st.Backup(ctx)
	if err != nil || !created || len(id) != 64 {
		t.Fatalf("backup: %s %v %v", id, created, err)
	}
	password, err := os.ReadFile(st.PasswordFile())
	if err != nil || len(password) != 43 {
		t.Fatal("restic_password", err)
	}
	var restics int
	for _, c := range fakedocker.Calls(t, fake) {
		all := strings.Join(append(append([]string{}, c.Args...), c.Env...), "\x00")
		for _, secret := range []string{restSecret, string(password), dbSecret} {
			if strings.Contains(all, secret) {
				t.Fatalf("a password in docker argv or env: %v", c)
			}
		}
		if c.Args[0] != "run" {
			continue
		}
		restics++
		cmd := strings.Join(c.Args, " ")
		for _, want := range []string{"run --rm --user ", " --read-only --cap-drop ALL --security-opt no-new-privileges --network bridge --tmpfs /tmp:size=512m --memory 1g --memory-swap 1g --cpus 1 --pids-limit 128 ",
			" --entrypoint /bin/sh " + ResticImage + " -c " + resticWrapper + " restic --cacert /run/kyq/ca.pem "} {
			if !strings.Contains(cmd, want) {
				t.Errorf("restic run lacks %q: %s", want, cmd)
			}
		}
		if !slices.Equal(c.Env, []string{"RESTIC_REPOSITORY=rest:https://backup.lan:8000/nas/fixture", "RESTIC_PASSWORD_FILE=/run/kyq/restic_password", "RESTIC_REST_USERNAME=nas"}) {
			t.Errorf("env = %v", c.Env)
		}
		for i, a := range c.Args {
			if a != "-v" {
				continue
			}
			src, dst, _ := strings.Cut(c.Args[i+1], ":")
			if src == st.PasswordFile() && dst != ctrPassword+":ro" || strings.Contains(dst, catalog.ResticPassword) && dst != ctrPassword+":ro" {
				t.Errorf("restic_password bound as %s", c.Args[i+1])
			}
			if !strings.HasSuffix(dst, ":ro") {
				t.Errorf("backup bind %s is writable", c.Args[i+1])
			}
		}
		if i := slices.Index(c.Args, "backup"); i > 0 {
			got := strings.Join(c.Args[i:], " ")
			want := "backup --json --host nas --tag kyquickstart --tag app=fixture --tag installation=6f1c2d3e-4a5b-4c6d-8e7f-9a0b1c2d3e4f --tag version=1.2.3 --exclude /kyq/volumes/files/cache /kyq/dump /kyq/volumes/files /kyq/secrets/db_password"
			if got != want {
				t.Errorf("backup args = %s", got)
			}
		}
	}
	if restics != 4 { // cat config, init, backup, check
		t.Errorf("%d restic runs", restics)
	}
	if _, err := os.Stat(st.scratch()); !errors.Is(err, os.ErrNotExist) {
		t.Error("scratch dump left behind")
	}
}

// AC4's dump path, faked: the dump, the volume minus its skip path and the secret travel and
// come back; a failing dump takes no snapshot.
func TestBackupRestoreRoundTrip(t *testing.T) {
	st, fake := backupFixture(t, true)
	ctx := context.Background()
	id, _, err := st.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	failing := st
	failing.App.Catalog.Backup = &catalog.Backup{Service: "db", Command: []string{"crash"}, RestoreCommand: []string{"load"}, Volumes: []string{"files"}}
	if _, _, err := failing.Backup(ctx); err == nil {
		t.Fatal("failing dump command backed up")
	}
	list, err := st.Snapshots(ctx, st.PasswordFile(), "")
	if err != nil || len(list) != 1 || list[0].ID != id || !st.Bound(list[0]) {
		t.Fatalf("snapshots = %+v, %v", list, err)
	}
	dir := st.App.dir()
	os.RemoveAll(dir) // the disaster: data, secrets and restic_password
	running, _ := st.Running(ctx)
	if !running {
		t.Fatal("fake project stopped")
	}
	pw, _ := os.ReadFile(filepath.Join(fake, "repo", "rest_https___backup.lan_8000_nas_fixture", "config"))
	received, err := st.ReceivePassword(ctx, pw)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RestoreFiles(ctx, received, id[:8]); err != nil {
		t.Fatal(err)
	}
	if err := st.PublishPassword(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.RestoreDump(ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(fake, "loaded")); string(b) != "CREATE TABLE t; INSERT 1;" {
		t.Fatalf("restore_command got %q", b)
	}
	if err := st.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{"files/docs/a.txt": "kept", "secrets/db_password": dbSecret, "secrets/restic_password": string(pw)} {
		b, err := os.ReadFile(filepath.Join(dir, file))
		info, _ := os.Stat(filepath.Join(dir, file))
		if err != nil || string(b) != want || strings.HasPrefix(file, "secrets/") && info.Mode().Perm() != 0o600 {
			t.Errorf("%s = %q, %v", file, b, err)
		}
	}
	for _, gone := range []string{"files/cache", ".restore", ".restore-password", ".restore-pending", ".backup"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after restore", gone)
		}
	}
}

// killRunner fails its n-th command, before running it or, with after, once it ran.
type killRunner struct {
	n, calls int
	after    bool
	killed   string
}

func (k *killRunner) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	k.calls++
	if k.calls != k.n {
		return remote.Local{}.Run(ctx, cmd, stdin)
	}
	k.killed = cmd
	if k.after {
		remote.Local{}.Run(ctx, cmd, stdin)
	}
	return nil, errors.New("killed")
}

// AC7: a backup killed at any command converges on rerun, creates the password and the
// repository once, removes a stale scratch dump and, cold, always restarts the app.
func TestBackupConvergesAfterKill(t *testing.T) {
	for _, dump := range []bool{true, false} {
		for _, after := range []bool{false, true} {
			for n := 1; ; n++ {
				st, fake := backupFixture(t, dump)
				ctx := context.Background()
				k := &killRunner{n: n, after: after}
				st.App.Runner = k
				_, _, err := st.Backup(ctx)
				if err == nil {
					if k.calls < n {
						break // every command was killed once
					}
					t.Fatalf("dump %v n %d: backup passed through its kill", dump, n)
				}
				restartKilled := strings.Contains(k.killed, " start") || strings.Contains(k.killed, " ps ")
				if !dump && !slices.Equal(upServices(t, fake), []string{"app"}) && !restartKilled {
					t.Fatalf("cold backup killed at %d left the app stopped", n)
				}
				// The plaintext dump never outlives a run, unless the kill hit its removal.
				if _, err := os.Stat(st.scratch()); !errors.Is(err, os.ErrNotExist) && !strings.Contains(k.killed, "rm -rf") {
					t.Fatalf("n %d: the scratch dump outlived a failed backup (killed %s)", n, k.killed)
				}
				pw, _ := os.ReadFile(st.PasswordFile())
				os.MkdirAll(st.scratch(), 0o700)
				os.WriteFile(filepath.Join(st.scratch(), "dump"), []byte("stale"), 0o600)
				st.App.Runner = remote.Local{}
				id, created, err := st.Backup(ctx)
				if err != nil || len(id) != 64 {
					t.Fatalf("dump %v after %v n %d: rerun: %v", dump, after, n, err)
				}
				if again, _ := os.ReadFile(st.PasswordFile()); len(pw) > 0 && (created || string(again) != string(pw)) {
					t.Fatalf("n %d: the password was replaced", n)
				}
				if _, err := os.Stat(st.scratch()); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("n %d: stale scratch dump left", n)
				}
				if !slices.Equal(upServices(t, fake), st.App.Catalog.Services) {
					t.Fatalf("n %d: not running after rerun", n)
				}
				inits := slices.DeleteFunc(fakedocker.Calls(t, fake), func(c fakedocker.Call) bool { return !slices.Contains(c.Args, "init") })
				if len(inits) != 1 {
					t.Fatalf("n %d: %d inits", n, len(inits))
				}
			}
		}
	}
}

// upServices lists the fake project's running services.
func upServices(t *testing.T, fake string) []string {
	var up []string
	for s, ok := range fakedocker.Running(t, fake) {
		if ok {
			up = append(up, s)
		}
	}
	slices.Sort(up)
	return up
}

// A snapshot from a compromised target cannot point the app at another host path: a
// symlinked volume, secret or dump, or a symlinked directory above them (relative, as restic
// restores it), is refused, and nothing is moved into place or changed through the link.
func TestRestoreRefusesSymlinks(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ entry, link string }{
		{"kyq/volumes/files", "VICTIM/files"},
		{"kyq/secrets/db_password", "VICTIM/secrets/db_password"},
		{"kyq/dump", "VICTIM/dump"},
		{"kyq/volumes", "../../../victim"},
		{"kyq/secrets", "../../../victim/secrets"},
		{"kyq", "../../victim"},
	} {
		st, fake := backupFixture(t, true)
		id, _, err := st.Backup(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// Another app's data on the new target, which the link reaches.
		victim := filepath.Join(st.App.Root, "victim")
		for _, d := range []string{"files/docs", "secrets", "volumes/files", "kyq"} {
			os.MkdirAll(filepath.Join(victim, d), 0o700)
		}
		for _, f := range []string{"files/docs/v.txt", "volumes/files/v.txt", "secrets/db_password", "dump", "volumes/x"} {
			os.WriteFile(filepath.Join(victim, f), []byte("victim"), 0o644)
		}
		os.MkdirAll(filepath.Join(victim, "secrets", "db_password.d"), 0o700)
		snap := filepath.Join(fake, "repo", "rest_https___backup.lan_8000_nas_fixture", "snapshots", id, c.entry)
		os.RemoveAll(snap)
		if err := os.Symlink(strings.Replace(c.link, "VICTIM", victim, 1), snap); err != nil {
			t.Fatal(err)
		}
		os.RemoveAll(st.App.dir())
		pw, _ := os.ReadFile(filepath.Join(fake, "repo", "rest_https___backup.lan_8000_nas_fixture", "config"))
		received, err := st.ReceivePassword(ctx, pw)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.RestoreFiles(ctx, received, id); err == nil {
			t.Fatalf("%s -> %s was restored", c.entry, c.link)
		}
		for _, f := range []string{"files/docs/v.txt", "secrets/db_password", "dump"} {
			info, err := os.Stat(filepath.Join(victim, f))
			if err != nil || info.Mode().Perm() != 0o644 {
				t.Fatalf("%s: the victim's %s moved or changed", c.entry, f)
			}
		}
		if _, err := os.Lstat(filepath.Join(st.App.dir(), "files")); err == nil {
			t.Fatalf("%s: a volume was moved into place", c.entry)
		}
		if _, err := os.Lstat(filepath.Join(st.App.dir(), "secrets", "db_password")); err == nil {
			t.Fatalf("%s: a secret was linked into place", c.entry)
		}
		if b, _ := os.ReadFile(filepath.Join(fake, "loaded")); len(b) > 0 {
			t.Fatalf("%s: a dump was read", c.entry)
		}
	}
}

// Apply cannot start an app whose restore is unfinished: its removal replay was not delivered.
func TestDeployRefusesAnUnfinishedRestore(t *testing.T) {
	ctx := context.Background()
	st, fake := backupFixture(t, false)
	os.WriteFile(filepath.Join(fake, "compose.json"), []byte(`{"app":false}`), 0o600)
	if err := st.SetPending(ctx, strings.Repeat("a", 64), "files"); err != nil {
		t.Fatal(err)
	}
	deploy := Steps(st.App)[1]
	if err := deploy.Apply(ctx); err == nil || !strings.Contains(err.Error(), "finish it with kyquickstart restore fixture") {
		t.Fatalf("deploy over an unfinished restore: %v", err)
	}
	if fakedocker.Running(t, fake)["app"] {
		t.Fatal("the app started")
	}
	st.App.Restoring = true
	if err := Steps(st.App)[1].Apply(ctx); err != nil || !fakedocker.Running(t, fake)["app"] {
		t.Fatalf("restore's own deploy: %v", err)
	}
}

// RestoreDump checks its staged dump itself, whatever staged it.
func TestRestoreDumpRefusesASymlinkedDump(t *testing.T) {
	ctx := context.Background()
	st, fake := backupFixture(t, true)
	staged := filepath.Join(st.staging(), "kyq")
	os.MkdirAll(staged, 0o700)
	victim := filepath.Join(t.TempDir(), "dump")
	os.WriteFile(victim, []byte("host file"), 0o644)
	os.Symlink(victim, filepath.Join(staged, "dump"))
	if err := st.RestoreDump(ctx); err == nil {
		t.Fatal("a symlinked dump was read")
	}
	os.Remove(filepath.Join(staged, "dump"))
	os.RemoveAll(staged)
	os.Symlink(filepath.Dir(victim), staged)
	if err := st.RestoreDump(ctx); err == nil {
		t.Fatal("a dump under a symlinked kyq was read")
	}
	if b, _ := os.ReadFile(filepath.Join(fake, "loaded")); len(b) > 0 {
		t.Fatal("restore_command got the host file")
	}
}

func TestRestorePendingFailsClosedAndPublishesAtomically(t *testing.T) {
	st, _ := backupFixture(t, false)
	ctx := context.Background()
	if err := os.WriteFile(st.pending(), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Pending(ctx); err == nil {
		t.Fatal("empty restore fence treated as no restore")
	}
	id := strings.Repeat("a", 64)
	if err := st.SetPending(ctx, id, "preparing"); err != nil {
		t.Fatal(err)
	}
	bin := realTemp(t)
	if err := os.WriteFile(filepath.Join(bin, "mv"), []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := st.SetPending(ctx, id, "files"); err == nil {
		t.Fatal("failed marker publication succeeded")
	}
	if pending, stage, err := st.Pending(ctx); err != nil || pending != id || stage != "preparing" {
		t.Fatalf("failed publication lost the existing fence: %q %q %v", pending, stage, err)
	}
	if tmp, _ := filepath.Glob(st.pending() + ".*"); len(tmp) != 0 {
		t.Fatalf("marker temporary files left: %v", tmp)
	}
}
