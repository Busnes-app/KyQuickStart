package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost/fakedocker"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

type driveResetRunner struct {
	t                   *testing.T
	fake                string
	capabilities        string
	resetErr, replayErr error
	reset, replayed     bool
	failPublish         bool
}

func (r *driveResetRunner) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	switch {
	case r.failPublish && strings.Contains(cmd, `mv "$f/volumes/$v" "$v"`):
		r.failPublish = false
		return nil, errors.New("interrupted restored-file publication")
	case strings.Contains(cmd, "'apply-setup' '--capabilities'"):
		if strings.Contains(cmd, " -v ") {
			r.t.Fatal("capability probe mounted restored data", cmd)
		}
		return []byte(r.capabilities), nil
	case strings.Contains(cmd, "'restore-reset' '-to'"):
		if !strings.Contains(r.capabilities, `"restore-reset"`) {
			r.t.Fatal("unknown reset command invoked", cmd)
		}
		if fakedocker.Running(r.t, r.fake)["app"] {
			r.t.Fatal("reset ran with Drive still running")
		}
		r.reset = r.resetErr == nil
		return nil, r.resetErr
	case strings.Contains(cmd, "'resync-system'"):
		if !r.reset || !fakedocker.Running(r.t, r.fake)["app"] {
			r.t.Fatal("directory replay ran before reset and startup")
		}
		r.replayed = true
		status := "delivered"
		if r.replayErr != nil {
			status = "failed"
		}
		return []byte(`{"schema":"kyidentity.resync-system/1","clientId":"kydrive","status":"` + status + `","pending":0,"failed":0}`), r.replayErr
	case strings.Contains(cmd, "docker compose") && strings.Contains(cmd, " up -d"):
		if !r.reset {
			r.t.Fatal("restored Drive started before its access grants were reset")
		}
	}
	return remote.Local{}.Run(ctx, cmd, stdin)
}
func (r *driveResetRunner) Close() error { return nil }

const driveResetCapabilities = `{"schema":"ky.setup-capabilities/1","service":"kydrive","bundleSchema":"ky.product-setup/1","features":["restore-reset"]}`

func driveResetFixture(t *testing.T) (*session, dockerhost.Store, *driveResetRunner) {
	t.Helper()
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "kydrive")
	for _, sub := range []string{"data", "backups", "secrets"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	r := &driveResetRunner{t: t, fake: fakedocker.Install(t, "app"), capabilities: driveResetCapabilities}
	s := &session{state: t.TempDir(), o: Options{Out: io.Discard}, redact: &engine.Redactor{}, placed: map[string]stack.Target{"kyidentity": {Name: "id"}}, drivers: map[string]driver{"id": sshDriver{c: r}}}
	st := dockerhost.Store{App: dockerhost.App{Name: "kydrive", Root: root, Runner: r, Catalog: cat["kydrive"], Redact: s.redact}}
	return s, st, r
}

func TestDriveRestoreResetBeforeStartup(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		missing, resetFail, replayFail bool
	}{
		{name: "old pinned image", missing: true}, {name: "reset fails", resetFail: true}, {name: "replay fails", replayFail: true}, {name: "success"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, st, r := driveResetFixture(t)
			if tc.missing {
				r.capabilities = strings.ReplaceAll(driveResetCapabilities, `["restore-reset"]`, `[]`)
			}
			if tc.resetFail {
				r.resetErr = errors.New("native reset failed")
			}
			if tc.replayFail {
				r.replayErr = errors.New("directory replay failed")
			}
			id := strings.Repeat("a", 64)
			if err := st.SetPending(context.Background(), id, "files"); err != nil {
				t.Fatal(err)
			}
			err := s.finishRestore(context.Background(), st, id, "files")
			failed := tc.missing || tc.resetFail || tc.replayFail
			if failed != (err != nil) {
				t.Fatalf("failure=%v: %v", failed, err)
			}
			if fakedocker.Running(t, r.fake)["app"] == failed {
				t.Fatal("restore did not leave the expected stopped/running state")
			}
			pending, _, perr := st.Pending(context.Background())
			if perr != nil || failed != (pending == id) {
				t.Fatalf("pending=%q: %v", pending, perr)
			}
			if tc.missing || tc.resetFail {
				if r.replayed {
					t.Fatal("failed reset reached directory replay")
				}
			}
			if failed {
				// A resumed restore may have been exposed by an older build. Stop it before resetting.
				r.capabilities, r.resetErr, r.replayErr, r.reset, r.replayed = driveResetCapabilities, nil, nil, false, false
				if err := os.WriteFile(filepath.Join(r.fake, "compose.json"), []byte(`{"app":true}`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := s.finishRestore(context.Background(), st, id, "files"); err != nil {
					t.Fatal(err)
				}
				if !r.reset || !r.replayed || !fakedocker.Running(t, r.fake)["app"] {
					t.Fatal("resumed restore skipped reset/replay")
				}
			}
		})
	}
}

func TestDriveRestoreChecksCapabilityBeforeReplacingData(t *testing.T) {
	s, st, r := driveResetFixture(t)
	r.capabilities = strings.ReplaceAll(driveResetCapabilities, `["restore-reset"]`, `[]`)
	if err := st.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(st.App.Root, "kydrive", "data", "keep.txt")
	if err := os.WriteFile(file, []byte("existing data"), 0600); err != nil {
		t.Fatal(err)
	}
	err := s.restoreLocked(context.Background(), st, restoreRequest{app: "kydrive", snapshot: strings.Repeat("a", 64), replace: true, yes: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "restore-reset") {
		t.Fatalf("missing native reset not refused first: %v", err)
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "existing data" {
		t.Fatal("unsupported restore changed existing data", err)
	}
	if paths, _ := filepath.Glob(filepath.Join(st.App.Root, "kydrive", ".replaced-*")); len(paths) != 0 {
		t.Fatal("unsupported restore moved data aside")
	}
}

// A crash while replacing restored files must fence ordinary startup and never claim the
// incomplete set is ready for reset/deploy. --replace explicitly retries full extraction.
func TestDriveInterruptedPublicationStaysFenced(t *testing.T) {
	ctx := context.Background()
	s, st, r := driveResetFixture(t)
	cred := filepath.Join(st.App.Root, "rest-password")
	if err := os.WriteFile(cred, []byte("public test credential"), 0600); err != nil {
		t.Fatal(err)
	}
	st.Dest = stack.BackupDestination{Repository: "rest:https://backup.lan/nas", CredentialFile: cred}
	st.Installation, st.Host, st.Version = installation, "nas", st.App.Catalog.Version
	dir := filepath.Join(st.App.Root, "kydrive")
	for _, name := range st.App.Catalog.Secrets {
		if err := os.WriteFile(filepath.Join(dir, "secrets", name), []byte(strings.Repeat("A", 43)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(dir, "data", "kept.txt")
	if err := os.WriteFile(file, []byte("snapshot data"), 0600); err != nil {
		t.Fatal(err)
	}
	id, _, err := st.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	r.failPublish = true
	request := restoreRequest{app: "kydrive", snapshot: id, replace: true, yes: true}
	if err := s.restoreLocked(ctx, st, request, nil); err == nil || !strings.Contains(err.Error(), "interrupted restored-file publication") {
		t.Fatalf("publication did not interrupt: %v", err)
	}
	pending, stage, err := st.Pending(ctx)
	if err != nil || pending != id || stage != "preparing" {
		t.Fatalf("incomplete publication claimed complete: %q %q %v", pending, stage, err)
	}
	e := &engine.Engine{Dir: filepath.Join(s.state, "ordinary"), Out: io.Discard, Redact: s.redact}
	for _, steps := range [][]engine.Step{dockerhost.Steps(st.App), {dockerhost.StartStep(st.App)}} {
		if err := e.Run(ctx, steps); err == nil {
			t.Fatal("ordinary startup bypassed interrupted restore")
		}
	}
	if fakedocker.Running(t, r.fake)["app"] {
		t.Fatal("interrupted publication started Drive")
	}
	for _, name := range st.App.Catalog.Secrets {
		if _, err := os.Stat(filepath.Join(dir, "secrets", name)); !os.IsNotExist(err) {
			t.Fatal("ordinary apply minted a secret over incomplete restored files", name, err)
		}
	}
	for _, request := range []restoreRequest{{app: "kydrive"}, {app: "kydrive", snapshot: id}} {
		if err := s.restoreLocked(ctx, st, request, nil); err == nil || !strings.Contains(err.Error(), "--replace") {
			t.Fatalf("partial files automatically resumed: %v", err)
		}
	}
	if err := s.restoreLocked(ctx, st, restoreRequest{app: "kydrive", snapshot: strings.Repeat("b", 64), replace: true, yes: true}, nil); err == nil || !strings.Contains(err.Error(), "a restore of snapshot") {
		t.Fatalf("partial restore changed snapshots: %v", err)
	}
	if r.reset || r.replayed {
		t.Fatal("partial files reached reset/replay")
	}
	if err := s.restoreLocked(ctx, st, request, nil); err != nil {
		t.Fatal("explicit extraction retry failed", err)
	}
	if !r.reset || !r.replayed {
		t.Fatal("retried restore skipped reset/replay")
	}
	if pending, _, err := st.Pending(ctx); err != nil || pending != "" {
		t.Fatalf("successful retry left pending: %q %v", pending, err)
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "snapshot data" {
		t.Fatalf("retried data=%q %v", b, err)
	}
}
