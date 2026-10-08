package dockerhost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
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

func deployOf(a App) deployStep { return Steps(a)[1].(deployStep) }

func TestOverrideLabelsEveryService(t *testing.T) {
	var o struct {
		Services map[string]struct{ Labels map[string]string }
	}
	if err := yaml.Unmarshal(override([]string{"web", "db"}, "rs-1"), &o); err != nil {
		t.Fatal(err)
	}
	for _, svc := range []string{"web", "db"} {
		l := o.Services[svc].Labels
		if l[ManagedLabel] != ManagedValue || l[ReleaseSetLabel] != "rs-1" {
			t.Errorf("%s labels = %v", svc, l)
		}
	}
}

func TestDeployApplyOrder(t *testing.T) {
	f := &fakeRunner{}
	a := fakeApp(f)
	d := deployOf(a)
	if err := d.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"cat > '/opt/kyq/hello/compose.yaml'", "cat > '/opt/kyq/hello/compose.kyq.yaml'", "pull --quiet", "up -d --remove-orphans", "> '/opt/kyq/hello/.input-hash'"}
	if len(f.calls) != len(want) {
		t.Fatalf("calls = %v", f.calls)
	}
	for i, w := range want {
		if !strings.Contains(f.calls[i].cmd, w) {
			t.Errorf("call %d = %q, want %q", i, f.calls[i].cmd, w)
		}
	}
	if f.calls[0].stdin != string(a.Catalog.Compose) || !strings.Contains(f.calls[1].stdin, ManagedValue) {
		t.Errorf("stdin: %q / %q", f.calls[0].stdin, f.calls[1].stdin)
	}
	if !strings.Contains(f.calls[3].cmd, "-p 'kyq-hello'") {
		t.Errorf("project name missing: %q", f.calls[3].cmd)
	}
	if !strings.Contains(f.calls[4].cmd, d.hash) {
		t.Errorf("hash not written: %q", f.calls[4].cmd)
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
