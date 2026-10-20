package dockerhost

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/dockerhost/fakedocker"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// localDocker runs shell commands for real and answers docker commands from containers.
type localDocker struct {
	calls      []string
	containers string // `docker ps -aq` output
	labelled   string // the same, filtered to the managed label
}

func (l *localDocker) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	if !strings.Contains(cmd, "docker ") {
		return remote.Local{}.Run(ctx, cmd, stdin)
	}
	l.calls = append(l.calls, cmd)
	switch {
	case strings.Contains(cmd, " up "):
		l.labelled = ""
	case strings.Contains(cmd, " ps --all --format json") && l.containers != "":
		return []byte(`{"Service":"web","State":"running","Health":""}` + "\n"), nil
	case strings.Contains(cmd, " down"):
		l.containers, l.labelled = "", ""
	case strings.Contains(cmd, "label="+managed.Label):
		return []byte(l.labelled), nil
	case strings.Contains(cmd, "docker ps -aq"):
		return []byte(l.containers), nil
	}
	return nil, nil
}

func installed(t *testing.T, r remote.Runner) App {
	t.Helper()
	a := fakeApp(r)
	a.Root = realTemp(t)
	a.Catalog.Version = "1.2.3"
	os.MkdirAll(a.dir(), 0o700)
	os.WriteFile(filepath.Join(a.dir(), "compose.kyq.yaml"), override(a.Catalog.Services, labels(a.ReleaseSet, a.Catalog.Version)), 0o600)
	os.WriteFile(filepath.Join(a.dir(), ".input-hash"), []byte("h"), 0o600)
	return a
}

func TestDeployedReadsOverride(t *testing.T) {
	ctx := context.Background()
	a := installed(t, &localDocker{})
	if d, err := Deployed(ctx, a); err != nil || d != (managed.Deployed{Found: true, Managed: true, ReleaseSet: "rs-1", Version: "1.2.3"}) {
		t.Fatalf("deployed = %+v, %v", d, err)
	}
	os.Remove(filepath.Join(a.dir(), "compose.kyq.yaml"))
	if d, err := Deployed(ctx, a); err != nil || d.Found {
		t.Fatalf("absent = %+v, %v", d, err)
	}
	os.WriteFile(filepath.Join(a.dir(), "compose.kyq.yaml"), []byte("not: [an override"), 0o600)
	if _, err := Deployed(ctx, a); err == nil {
		t.Fatal("accepted a malformed override")
	}
}

func TestRemoveKeepsVolumesAndFiles(t *testing.T) {
	ctx := context.Background()
	r := &localDocker{containers: "c1\n", labelled: "c1\n"}
	a := installed(t, r)
	s := RemoveSteps(a)[0]
	if done, err := s.Inspect(ctx); err != nil || done {
		t.Fatalf("Inspect before = %v %v", done, err)
	}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	down := r.calls[len(r.calls)-2]
	if !strings.Contains(down, "down --remove-orphans") || strings.Contains(down, " -v") || strings.Contains(down, "--volumes") {
		t.Errorf("down = %q", down)
	}
	if _, err := os.Stat(filepath.Join(a.dir(), "compose.kyq.yaml")); err != nil {
		t.Error("app directory not kept")
	}
	if done, _ := s.Inspect(ctx); !done {
		t.Error("rerun would remove again")
	}
}

func TestUnmanageRelabelsAndRecreates(t *testing.T) {
	ctx := context.Background()
	r := &localDocker{containers: "c1\n", labelled: "c1\n"}
	a := installed(t, r)
	s := UnmanageSteps(a)[0]
	if done, err := s.Inspect(ctx); err != nil || done {
		t.Fatalf("Inspect before = %v %v", done, err)
	}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if d, _ := Deployed(ctx, a); !d.Found || d.Managed || d.ReleaseSet != "" || d.Version != "1.2.3" {
		t.Errorf("deployed after unmanage = %+v", d)
	}
	if _, err := os.Stat(filepath.Join(a.dir(), ".input-hash")); !os.IsNotExist(err) {
		t.Error("input hash kept: a later apply would treat the files as its own")
	}
	var ups []string
	for _, c := range r.calls {
		if strings.Contains(c, " up ") {
			ups = append(ups, c)
		}
	}
	if len(ups) != 1 || !strings.Contains(ups[0], "up --no-start") || strings.Contains(ups[0], "remove-orphans") {
		t.Errorf("up calls = %q", ups)
	}
	// A rerun after the files were rewritten but before the recreate finishes the job.
	r.labelled = "c1\n"
	if done, _ := s.Inspect(ctx); done {
		t.Fatal("labelled container counted as unmanaged")
	}
	if err := s.Apply(ctx); err != nil || s.Verify(ctx) != nil {
		t.Fatalf("rerun: %v", err)
	}
}

func TestUnmanageStartsNothing(t *testing.T) {
	ctx := context.Background()
	r := &localDocker{}
	a := installed(t, r)
	s := UnmanageSteps(a)[0]
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls {
		if strings.Contains(c, " up ") {
			t.Errorf("started a removed app: %q", c)
		}
	}
}

// Unmanage recreates every container unstarted and starts again only the services that ran.
func TestUnmanageStartsOnlyWhatRan(t *testing.T) {
	ctx := context.Background()
	fake := fakedocker.Install(t, "app", "db")
	os.WriteFile(filepath.Join(fake, "compose.json"), []byte(`{"app":true,"db":false}`), 0o600)
	a := installed(t, remote.Local{})
	a.Catalog.Services = []string{"app", "db"}
	if err := UnmanageSteps(a)[0].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if up := fakedocker.Running(t, fake); !up["app"] || up["db"] {
		t.Fatalf("running after unmanage: %v", up)
	}
	var cmds []string
	for _, c := range fakedocker.Calls(t, fake) {
		if c.Args[0] == "compose" {
			cmds = append(cmds, strings.Join(c.Args[7:], " "))
		}
	}
	if !slices.Equal(cmds, []string{"ps --all --format json", "up --no-start --no-deps app db", "start app"}) {
		t.Fatalf("compose calls = %q", cmds)
	}
}

// Unmanage killed at any command, before or after it ran, finishes on rerun with the
// original sets: the running service runs, the stopped one stays stopped, the record goes.
func TestUnmanageConvergesAfterKill(t *testing.T) {
	ctx := context.Background()
	for _, after := range []bool{false, true} {
		for n := 1; ; n++ {
			fake := fakedocker.Install(t, "app", "db")
			os.WriteFile(filepath.Join(fake, "compose.json"), []byte(`{"app":true,"db":false}`), 0o600)
			k := &killRunner{n: n, after: after}
			a := installed(t, k)
			a.Catalog.Services = []string{"app", "db"}
			step := UnmanageSteps(a)[0]
			err := step.Apply(ctx)
			if err == nil && k.calls < n {
				break
			}
			k.n = 0
			if done, _ := step.Inspect(ctx); done && err != nil {
				if _, serr := os.Stat(filepath.Join(a.dir(), unmanageRecord)); serr == nil {
					t.Fatalf("after %v n %d: an unfinished recreate inspected as done", after, n)
				}
			}
			if err := step.Apply(ctx); err != nil {
				t.Fatalf("after %v n %d (killed %q): rerun: %v", after, n, k.killed, err)
			}
			if up := fakedocker.Running(t, fake); !up["app"] || up["db"] {
				t.Fatalf("after %v n %d (killed %q): running %v", after, n, k.killed, up)
			}
			if _, err := os.Stat(filepath.Join(a.dir(), unmanageRecord)); !os.IsNotExist(err) {
				t.Fatalf("after %v n %d: the record stayed", after, n)
			}
		}
	}
}

// psRunner answers compose ps with fixed states and records the up and start commands.
type psRunner struct {
	ps    string
	calls []string
}

func (p *psRunner) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	if !strings.Contains(cmd, "docker ") {
		return remote.Local{}.Run(ctx, cmd, stdin)
	}
	switch {
	case strings.Contains(cmd, " ps --all --format json"):
		return []byte(p.ps), nil
	case strings.Contains(cmd, "docker ps -aq"):
		return []byte("c1\n"), nil
	}
	p.calls = append(p.calls, cmd[strings.Index(cmd, "compose.kyq.yaml ")+len("compose.kyq.yaml "):])
	return nil, nil
}

// A restarting service counts as running; a paused one stays stopped; a service without a
// container is not created.
func TestUnmanageKeepsRestartingServices(t *testing.T) {
	r := &psRunner{ps: `{"Service":"app","State":"restarting","Health":""}` + "\n" + `{"Service":"db","State":"paused","Health":""}` + "\n"}
	a := installed(t, r)
	a.Catalog.Services = []string{"app", "db", "extra"}
	if err := UnmanageSteps(a)[0].Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.calls, []string{"up --no-start --no-deps 'app' 'db'", "start 'app'"}) {
		t.Fatalf("calls = %q", r.calls)
	}
}

// A recorded, unfinished recreate keeps the step unfinished even with no label left.
func TestUnmanageRecordKeepsTheStepOpen(t *testing.T) {
	ctx := context.Background()
	r := &localDocker{}
	a := installed(t, r)
	s := UnmanageSteps(a)[0]
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if done, _ := s.Inspect(ctx); !done {
		t.Fatal("unmanaged app not done")
	}
	os.WriteFile(filepath.Join(a.dir(), unmanageRecord), []byte("recreate web\nstart web\n"), 0o600)
	if done, _ := s.Inspect(ctx); done {
		t.Fatal("a recorded recreate inspected as done")
	}
}
