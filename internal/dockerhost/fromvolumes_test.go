package dockerhost

import (
	"context"
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
	if !strings.Contains(f.calls[0].cmd, `grep -Fqx "kyq-post_$v"`) || !strings.Contains(f.calls[0].cmd, `[ -n "$(ls -A "$d/$v")" ]`) || !strings.Contains(f.calls[0].cmd, "d='/srv/kyq/post'") {
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
		`if [ -L "$v" ]`, "*src=0*dst=0*", `[ "$n" = "$m" ]`, `chmod 700 "$t"`, `mv "$t" "$v"`,
	} {
		if !strings.Contains(copied, want) {
			t.Errorf("copy lacks %q:\n%s", want, copied)
		}
	}
	if strings.Contains(copied, "kyq-post_config") || strings.Contains(copied, "volume rm") {
		t.Errorf("copy touched more than the pending volume:\n%s", copied)
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
