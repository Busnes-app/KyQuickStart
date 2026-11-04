package dockerhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// fromVolumesApp is a KyPost-like app with two bind directories its earlier version kept in
// named volumes; the fake host lists volumes for both and reports state's copy still empty.
func fromVolumesApp(t *testing.T, pending string) (App, *fakeRunner) {
	t.Helper()
	f := &fakeRunner{reply: func(cmd string) ([]byte, error) {
		if strings.HasPrefix(cmd, "vols=$(docker volume ls -q)") {
			return []byte(pending), nil
		}
		return nil, nil
	}}
	a := App{Name: "post", Root: "/srv/kyq", Runner: f, Catalog: catalog.App{
		Manifest: catalog.Manifest{Name: "post", FromVolumes: []string{"config", "state"}, Backup: &catalog.Backup{Volumes: []string{"config", "state"}}},
		Services: []string{"app"}, Binds: []string{"config", "state"},
	}}
	return a, f
}

// #151: an app with from_volumes gets <app>.volumes before its deploy; it is done when no
// volume waits, and otherwise stops the earlier deployment and copies each pending volume
// read-only, as root with only CAP_DAC_READ_SEARCH, into a copy the target user writes, both
// ends offline in the pinned image, renamed into place only after both succeeded.
func TestFromVolumesStep(t *testing.T) {
	a, f := fromVolumesApp(t, "")
	steps := Steps(a)
	if steps[0].ID() != "post.volumes" || steps[1].ID() != "post.deploy" {
		t.Fatalf("steps = %s, %s", steps[0].ID(), steps[1].ID())
	}
	plain := a
	plain.Catalog.FromVolumes = nil
	if Steps(plain)[0].ID() != "post.deploy" {
		t.Fatal("an app without from_volumes got a volumes step")
	}
	done, err := steps[0].Inspect(context.Background())
	if err != nil || !done {
		t.Fatalf("nothing pending: done %v, %v", done, err)
	}
	if !strings.Contains(f.calls[0].cmd, `grep -Fqx "kyq-post_$v"`) || !strings.Contains(f.calls[0].cmd, `if [ -e "$d/$v" ] || [ -L "$d/$v" ]; then continue; fi`) || !strings.Contains(f.calls[0].cmd, "d='/srv/kyq/post'") {
		t.Fatalf("pending check: %s", f.calls[0].cmd)
	}

	a, f = fromVolumesApp(t, "state\n")
	step := Steps(a)[0]
	if err := step.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	var stop, copied string
	for _, c := range f.calls {
		switch {
		case strings.Contains(c.cmd, "docker compose -p 'kyq-post'") && strings.HasSuffix(c.cmd, " stop; fi"):
			stop = c.cmd
		case strings.Contains(c.cmd, "t=.$v.from-volume"):
			if copied != "" {
				t.Fatal("copied more than the pending volume")
			}
			copied = c.cmd
		}
	}
	if !strings.HasPrefix(stop, "if [ -e '/srv/kyq/post/compose.yaml' ]; then cd '/srv/kyq/post' && ") {
		t.Fatalf("stop: %q", stop)
	}
	// Without an earlier Compose file the stop is skipped as a whole, never run elsewhere.
	if out, err := (remote.Local{}).Run(context.Background(), strings.Replace(stop, "/srv/kyq/post", t.TempDir(), -1), nil); err != nil || len(out) != 0 {
		t.Fatalf("stop without compose.yaml: %q %v", out, err)
	}
	for _, want := range []string{
		"v='state'",
		"--user 0:0 --cap-add DAC_READ_SEARCH -v 'kyq-post_state:/from:ro' --entrypoint tar " + ResticImage + " -C /from -cf - .",
		`-i --user "$(id -u):$(id -g)" -v "$PWD/$t:/to" --entrypoint tar ` + ResticImage + " -C /to -xpf -",
		"--network none --read-only --cap-drop ALL --security-opt no-new-privileges",
		`if [ -L "$v" ]`, "*src=0*dst=0*", `[ "$n" = "$m" ]`, `chmod 700 "$t"`,
	} {
		if !strings.Contains(copied, want) {
			t.Errorf("copy lacks %q:\n%s", want, copied)
		}
	}
	if strings.Contains(copied, "kyq-post_config") || strings.Contains(copied, "volume rm") {
		t.Errorf("copy touched more than the pending volume:\n%s", copied)
	}
	if strings.Contains(copied, "mv ") {
		t.Errorf("a copy is renamed into place before every copy succeeded:\n%s", copied)
	}
	note := step.(engine.Noter).Note()
	if !strings.Contains(note, "copied Docker volumes kyq-post_state into post's bind directories under /srv/kyq/post") || !strings.Contains(note, "docker volume rm kyq-post_state") {
		t.Errorf("note = %q", note)
	}
	if err := step.Verify(context.Background()); err == nil || !strings.Contains(err.Error(), "state") {
		t.Errorf("verify with a volume still pending: %v", err)
	}
}

// Until apply copies an earlier install's volumes, backup refuses instead of taking a
// snapshot of empty bind directories.
func TestBackupRefusesUncopiedVolumes(t *testing.T) {
	a, f := fromVolumesApp(t, "config\nstate\n")
	_, _, err := Store{App: a}.Backup(context.Background())
	if err == nil || !strings.Contains(err.Error(), "still in Docker volumes (config, state)") || !strings.Contains(err.Error(), "run kyquickstart apply") {
		t.Fatalf("backup: %v", err)
	}
	for _, c := range f.calls {
		if strings.Contains(c.cmd, "restic") || strings.Contains(c.cmd, " stop") {
			t.Fatalf("backup went on: %s", c.cmd)
		}
	}
}

// volumesRunner runs pending's real shell locally, its Docker volume list given.
type volumesRunner struct{ vols string }

func (r volumesRunner) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	return remote.Local{}.Run(ctx, strings.Replace(cmd, "docker volume ls -q", "printf '%s' "+remote.Quote(r.vols), 1), stdin)
}

// An empty volume's copy is an empty directory, which is done, so apply does not stop KyPost
// and copy again on every run; only a missing directory is pending.
func TestFromVolumesEmptyCopyIsDone(t *testing.T) {
	root := realTemp(t)
	os.MkdirAll(filepath.Join(root, "post", "config"), 0o700)
	a, _ := fromVolumesApp(t, "")
	a.Root, a.Runner = root, volumesRunner{vols: "kyq-post_config\nkyq-post_state\nother\n"}
	p, err := fromVolumesStep{a: a}.pending(context.Background())
	if err != nil || !slices.Equal(p, []string{"state"}) {
		t.Fatalf("pending = %v, %v", p, err)
	}
}

// A restore moves every bind directory aside and restores only those it backs up: the step
// neither copies an old volume over the rest nor refuses the restore's own record.
func TestFromVolumesSkippedByRestore(t *testing.T) {
	a, f := fromVolumesApp(t, "logs\n")
	a.Restoring = true
	step := Steps(a)[0]
	if done, err := step.Inspect(context.Background()); err != nil || !done {
		t.Fatalf("inspect while restoring: %v %v", done, err)
	}
	if err := step.Verify(context.Background()); err != nil {
		t.Fatalf("verify while restoring: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("ran %v", f.calls)
	}
}

// Opus pass 2 R1: a copy that fails leaves every bind directory missing, including those
// copied before it, so the next apply copies the volumes whole, never partly stale.
func TestFromVolumesFailedCopyPlacesNothing(t *testing.T) {
	a, f := fromVolumesApp(t, "config\nstate\n")
	f.reply = func(cmd string) ([]byte, error) {
		switch {
		case strings.HasPrefix(cmd, "vols=$(docker volume ls -q)"):
			return []byte("config\nstate\n"), nil
		case strings.Contains(cmd, "v='state'"):
			return nil, errors.New("no space left on device")
		}
		return nil, nil
	}
	if err := Steps(a)[0].Apply(context.Background()); err == nil {
		t.Fatal("a failed copy passed")
	}
	for _, c := range f.calls {
		if strings.Contains(c.cmd, `mv ".$v.from-volume" "$v"`) {
			t.Fatalf("renamed into place after a failed copy: %s", c.cmd)
		}
	}
	f.reply = func(cmd string) ([]byte, error) {
		if strings.HasPrefix(cmd, "vols=$(docker volume ls -q)") {
			return []byte("config\nstate\n"), nil
		}
		return nil, nil
	}
	f.calls = nil
	if err := Steps(a)[0].Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if last := f.calls[len(f.calls)-3].cmd; !strings.Contains(last, "for v in 'config' 'state'; do") || !strings.Contains(last, `mv ".$v.from-volume" "$v"`) || !strings.Contains(last, "rm -f compose.yaml .input-hash") {
		t.Fatalf("placing: %v", f.calls)
	}
}
