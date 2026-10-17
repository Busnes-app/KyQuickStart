package dockerhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	case strings.Contains(cmd, " up -d"):
		l.labelled = ""
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
		if strings.Contains(c, " up -d") {
			ups = append(ups, c)
		}
	}
	if len(ups) != 1 || strings.Contains(ups[0], "remove-orphans") {
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
