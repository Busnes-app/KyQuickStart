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
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"go.yaml.in/yaml/v3"
)

const (
	running = `{"Service":"web","State":"running","Health":""}`
	exited  = `{"Service":"web","State":"exited","Health":""}`
)

func fakeApp(r remote.Runner) App {
	return App{
		Name:       "hello",
		Root:       "/opt/kyq",
		Runner:     r,
		Catalog:    catalog.App{Manifest: catalog.Manifest{Name: "hello", Health: catalog.Health{TimeoutSeconds: 1}}, Compose: []byte("services:\n  web:\n    image: x\n"), Services: []string{"web"}},
		ReleaseSet: "rs-1",
		Redact:     &engine.Redactor{},
	}
}

func deployOf(a App) deployStep { return Steps(a)[0].(deployStep) }

func TestOverrideLabelsEveryService(t *testing.T) {
	var o struct {
		Services map[string]struct{ Labels map[string]string }
	}
	a := App{Name: "hello"}
	a.Catalog.Services = []string{"web", "db"}
	if err := yaml.Unmarshal(a.override(labels("rs-1", "1.2.3")), &o); err != nil {
		t.Fatal(err)
	}
	for _, svc := range []string{"web", "db"} {
		l := o.Services[svc].Labels
		if l[managed.Label] != managed.Value || l[managed.ReleaseSetLabel] != "rs-1" || l[managed.VersionLabel] != "1.2.3" {
			t.Errorf("%s labels = %v", svc, l)
		}
	}
}

// A cloudflare-direct edge's network joins service "app" under its alias, keeping the default.
func TestOverrideJoinsEdgeNetwork(t *testing.T) {
	var o struct {
		Services map[string]struct {
			Labels   map[string]string
			Networks map[string]struct{ Aliases []string }
		}
		Networks map[string]struct {
			External bool
			Name     string
		}
	}
	a := App{Name: "kyidentity", EdgeNetwork: "edge-net"}
	a.Catalog.Services = []string{"app", "db"}
	if err := yaml.Unmarshal(a.override(labels("rs-1", "")), &o); err != nil {
		t.Fatal(err)
	}
	app := o.Services["app"]
	if _, ok := app.Networks["default"]; !ok || app.Labels[managed.Label] != managed.Value || len(app.Networks["kyq-edge"].Aliases) != 1 || app.Networks["kyq-edge"].Aliases[0] != "kyq-kyidentity" {
		t.Fatalf("app service = %+v", app)
	}
	if o.Services["db"].Networks != nil || !o.Networks["kyq-edge"].External || o.Networks["kyq-edge"].Name != "edge-net" {
		t.Fatalf("override = %+v", o)
	}
	a.EdgeNetwork = ""
	if strings.Contains(string(a.override(labels("rs-1", ""))), "networks") {
		t.Fatal("network without an edge")
	}
}

func TestDeployApplyOrder(t *testing.T) {
	f := &fakeRunner{}
	a := fakeApp(f)
	d := deployOf(a)
	if err := d.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"test -e '/opt/kyq/hello/.restore-pending'", "pwd -P", "cp -p $f .$f.prev", "docker ps -aq", "pwd -P", "rm -f '/opt/kyq/hello/.input-hash'", "cat > '/opt/kyq/hello/compose.yaml'", "cat > '/opt/kyq/hello/compose.kyq.yaml'", "pull --quiet", ".compose.kyq.yaml.prev", "up -d --remove-orphans", "> '/opt/kyq/hello/.input-hash'"}
	if len(f.calls) != len(want) {
		t.Fatalf("calls = %v", f.calls)
	}
	for i, w := range want {
		if !strings.Contains(f.calls[i].cmd, w) {
			t.Errorf("call %d = %q, want %q", i, f.calls[i].cmd, w)
		}
	}
	if f.calls[6].stdin != string(a.Catalog.Compose) || !strings.Contains(f.calls[7].stdin, managed.Value) {
		t.Errorf("stdin: %q / %q", f.calls[6].stdin, f.calls[7].stdin)
	}
	if !strings.Contains(f.calls[10].cmd, "-p 'kyq-hello'") {
		t.Errorf("project name missing: %q", f.calls[10].cmd)
	}
	if !strings.Contains(f.calls[11].cmd, d.hash) {
		t.Errorf("hash not written: %q", f.calls[11].cmd)
	}
}

func TestDeployRefusesSharedAppDir(t *testing.T) {
	root := realTemp(t)
	dir := filepath.Join(root, "hello")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	hash := filepath.Join(dir, ".input-hash")
	if err := os.WriteFile(hash, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := fakeApp(remote.Local{})
	a.Root = root
	if err := deployOf(a).Apply(context.Background()); err == nil {
		t.Fatal("deployed into a world-writable directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.yaml")); !os.IsNotExist(err) {
		t.Errorf("compose.yaml written: %v", err)
	}
	if _, err := os.Stat(hash); err != nil {
		t.Errorf(".input-hash touched: %v", err)
	}
}

func TestDeployPullFailureIsTransient(t *testing.T) {
	f := &fakeRunner{reply: func(cmd string) ([]byte, error) {
		if strings.Contains(cmd, "pull") {
			return nil, &remote.ExitError{Code: 1, Stderr: []byte("toomanyrequests")}
		}
		return nil, nil
	}}
	err := deployOf(fakeApp(f)).Apply(context.Background())
	if !errors.Is(err, engine.ErrTransient) {
		t.Fatalf("err = %v", err)
	}
	for _, c := range f.calls {
		if strings.Contains(c.cmd, " up ") {
			t.Fatal("up ran after a failed pull")
		}
	}
}

func deployReplies(hash, ps string) func(string) ([]byte, error) {
	return func(cmd string) ([]byte, error) {
		if strings.Contains(cmd, ".input-hash") {
			return []byte(hash), nil
		}
		return []byte(ps), nil
	}
}

func TestDeployInspectSeesStoppedService(t *testing.T) {
	f := &fakeRunner{}
	d := deployOf(fakeApp(f))
	ctx := context.Background()

	f.reply = deployReplies(d.hash, running)
	if done, err := d.Inspect(ctx); err != nil || !done {
		t.Fatalf("running: %v %v", done, err)
	}
	f.reply = deployReplies(d.hash, exited)
	if done, err := d.Inspect(ctx); err != nil || done {
		t.Fatalf("exited: %v %v", done, err)
	}
	if err := d.Verify(ctx); err == nil || !strings.Contains(err.Error(), "web is not running") {
		t.Fatalf("Verify = %v", err)
	}
	f.reply = deployReplies(d.hash, "")
	if done, _ := d.Inspect(ctx); done {
		t.Fatal("removed containers counted as deployed")
	}
	f.reply = deployReplies("other", running)
	if done, _ := d.Inspect(ctx); done {
		t.Fatal("stale hash counted as deployed")
	}
}

// Compose project kyq-<app> is the Docker host's: its containers from another root (another
// plan, or this one before its root moved) are never replaced; this root's own are.
func TestDeployRefusesProjectFromAnotherRoot(t *testing.T) {
	for _, here := range []string{"", "c1\n", "c1\nc2\n"} {
		f := &fakeRunner{reply: func(cmd string) ([]byte, error) {
			if strings.Contains(cmd, "working_dir=/opt/kyq/hello'") {
				return []byte(here), nil
			}
			if strings.HasPrefix(cmd, "docker ps -aq") {
				return []byte("c1\nc2\n"), nil
			}
			return nil, nil
		}}
		err := deployOf(fakeApp(f)).Apply(context.Background())
		if here != "c1\nc2\n" && (err == nil || !strings.Contains(err.Error(), "from another directory than /opt/kyq/hello")) {
			t.Fatalf("%q here: %v", here, err)
		}
		if here == "c1\nc2\n" && err != nil {
			t.Fatalf("all here: %v", err)
		}
		if here != "c1\nc2\n" && slices.ContainsFunc(f.calls, func(c call) bool { return strings.Contains(c.cmd, "cat > ") }) {
			t.Fatal("wrote Compose files")
		}
		if here == "c1\nc2\n" {
			continue
		}
		// Sol pass 4 R1: a copied root carries a current input hash; its deploy is not current,
		// and backup and start/stop are refused too, before any compose command.
		a := fakeApp(f)
		d := deployOf(a)
		f.calls = nil
		inner := f.reply
		f.reply = func(cmd string) ([]byte, error) {
			if strings.Contains(cmd, ".input-hash") {
				return []byte(d.hash), nil
			}
			return inner(cmd)
		}
		for name, run := range map[string]func() error{
			"deploy inspect": func() error { _, err := d.Inspect(context.Background()); return err },
			"start":          func() error { _, err := StartStep(a).Inspect(context.Background()); return err },
			"backup":         func() error { _, _, err := (Store{App: a}).Backup(context.Background()); return err },
		} {
			if err := run(); err == nil || !strings.Contains(err.Error(), "from another directory than /opt/kyq/hello") {
				t.Fatalf("%q here, %s: %v", here, name, err)
			}
		}
		if slices.ContainsFunc(f.calls, func(c call) bool { return strings.Contains(c.cmd, "docker compose") }) {
			t.Fatal("ran docker compose on another root's project")
		}
	}
}

// A compose.yaml truncated by a failed write must not wedge re-runs: Inspect has to report
// "not done" rather than fail on `docker compose ps`.
func TestFailedComposeWriteLeavesDeployRetryable(t *testing.T) {
	f := &fakeRunner{}
	d := deployOf(fakeApp(f))
	ctx := context.Background()
	removed := false
	f.reply = func(cmd string) ([]byte, error) {
		switch {
		case strings.HasPrefix(cmd, "rm -f ") && strings.Contains(cmd, ".input-hash"):
			removed = true
		case strings.Contains(cmd, "cat > ") && strings.Contains(cmd, "/compose.yaml"):
			return nil, &remote.ExitError{Code: 1, Stderr: []byte("No space left on device")}
		case strings.Contains(cmd, ".input-hash"):
			if removed {
				return nil, nil
			}
			return []byte(d.hash), nil
		case strings.Contains(cmd, " ps --all"):
			return nil, &remote.ExitError{Code: 15, Stderr: []byte("yaml: unexpected end of stream")}
		}
		return nil, nil
	}
	if err := d.Apply(ctx); err == nil {
		t.Fatal("Apply succeeded with a failed write")
	}
	if done, err := d.Inspect(ctx); err != nil || done {
		t.Fatalf("Inspect = %v, %v; want false, nil", done, err)
	}
}

// Bind directories are created private, as the target user, before Compose runs; compose
// runs with that user's uid and gid. A symlinked bind directory is refused.
func TestDeployCreatesBindDirectories(t *testing.T) {
	root := realTemp(t)
	var compose []string
	r := runFunc(func(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
		if strings.Contains(cmd, "docker compose") {
			compose = append(compose, cmd)
			return nil, nil
		}
		if strings.HasPrefix(cmd, "docker ps") { // no containers yet
			return nil, nil
		}
		return remote.Local{}.Run(ctx, cmd, stdin)
	})
	a := fakeApp(r)
	a.Root, a.Catalog.Binds = root, []string{"backups", "data"}
	if err := deployOf(a).Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, b := range a.Catalog.Binds {
		fi, err := os.Stat(filepath.Join(root, "hello", b))
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", b, fi, err)
		}
	}
	if len(compose) != 2 || !strings.Contains(compose[1], "&& KYQ_UID=$(id -u) KYQ_GID=$(id -g) docker compose -p 'kyq-hello' ") {
		t.Fatalf("compose = %q", compose)
	}
	os.Remove(filepath.Join(root, "hello", "data"))
	os.Symlink(t.TempDir(), filepath.Join(root, "hello", "data"))
	compose = nil
	if err := deployOf(a).Apply(context.Background()); err == nil || len(compose) != 0 {
		t.Fatalf("symlinked bind directory: %v, compose %q", err, compose)
	}
}

type runFunc func(context.Context, string, []byte) ([]byte, error)

func (f runFunc) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	return f(ctx, cmd, stdin)
}

// pullFails runs commands under sh, except docker: pull fails, the rest pass.
type pullFails struct{}

func (pullFails) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	if strings.Contains(cmd, "docker compose") || strings.HasPrefix(cmd, "docker ps") {
		if strings.Contains(cmd, " pull ") {
			return nil, errors.New("registry unreachable")
		}
		return nil, nil
	}
	return remote.Local{}.Run(ctx, cmd, stdin)
}
func (pullFails) Close() error { return nil }

// An upgrade whose pull fails keeps the record of the version that still runs.
func TestFailedPullKeepsDeployedVersion(t *testing.T) {
	ctx := context.Background()
	a := fakeApp(pullFails{})
	a.Root = realTemp(t)
	a.Catalog.Version = "1.0.0"
	if err := a.writeCompose(ctx, a.override(labels(a.ReleaseSet, "1.0.0"))); err != nil {
		t.Fatal(err)
	}
	a.Catalog.Version = "2.0.0"
	if err := deployOf(a).Apply(ctx); err == nil || !strings.Contains(err.Error(), "registry unreachable") {
		t.Fatalf("Apply = %v", err)
	}
	if d, err := Deployed(ctx, a); err != nil || d.Version != "1.0.0" {
		t.Fatalf("Deployed = %+v, %v; want 1.0.0", d, err)
	}
	if left, _ := filepath.Glob(filepath.Join(a.dir(), ".compose*.prev")); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
	// A one-shot init before the deploy (KyMessages' matrix-init) keeps the record too, and
	// the deployed compose.yaml: a later compose up must not start the new images unrecorded.
	old, _ := os.ReadFile(filepath.Join(a.dir(), "compose.yaml"))
	a.Catalog.Compose = []byte("services:\n  web:\n    image: new\n")
	a.Catalog.Init = []string{"init"}
	if _, err := RunInit(ctx, a, "init", nil, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(a.dir(), "compose.yaml")); string(b) != string(old) {
		t.Fatalf("compose.yaml after an init = %q, want the deployed %q", b, old)
	}
	if err := deployOf(a).Apply(ctx); err == nil {
		t.Fatal("Apply passed")
	}
	if d, err := Deployed(ctx, a); err != nil || d.Version != "1.0.0" {
		t.Fatalf("Deployed after an init = %+v, %v; want 1.0.0", d, err)
	}
	if b, _ := os.ReadFile(filepath.Join(a.dir(), "compose.yaml")); string(b) != string(old) {
		t.Fatalf("compose.yaml after an init and a failed pull = %q, want the deployed %q", b, old)
	}
	if left, _ := filepath.Glob(filepath.Join(a.dir(), ".compose*")); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
	// After a volume migration removed the old compose.yaml, a failed pull keeps the new
	// layout: the old one would start the app on its volumes again.
	os.Remove(filepath.Join(a.dir(), "compose.yaml"))
	a.Catalog.Compose = []byte("services:\n  web:\n    image: y\n")
	if err := deployOf(a).Apply(ctx); err == nil {
		t.Fatal("Apply passed")
	}
	if b, _ := os.ReadFile(filepath.Join(a.dir(), "compose.yaml")); string(b) != string(a.Catalog.Compose) {
		t.Fatalf("compose.yaml = %q, want the new layout", b)
	}
}
