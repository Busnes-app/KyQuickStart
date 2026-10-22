package dockerhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/suite"
)

// composeHost runs shell commands for real and keeps one "web" container's state for docker.
type composeHost struct {
	state string // running, exited or "" (no container)
	calls []string
}

func (h *composeHost) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	if !strings.Contains(cmd, "docker ") {
		return remote.Local{}.Run(ctx, cmd, stdin)
	}
	h.calls = append(h.calls, cmd)
	switch {
	case strings.Contains(cmd, " ps --all --format json"):
		if h.state == "" {
			return nil, nil
		}
		return []byte(`{"Service":"web","State":"` + h.state + `","Health":""}` + "\n"), nil
	case strings.HasSuffix(cmd, " stop"):
		h.state = "exited"
	case strings.HasSuffix(cmd, " up -d"), strings.HasSuffix(cmd, " restart"):
		h.state = "running"
	case strings.Contains(cmd, " logs "):
		return []byte("web-1  | ready\n"), nil
	}
	return nil, nil
}

func lifecycleApp(t *testing.T, state string) (App, *composeHost) {
	t.Helper()
	pollInterval = time.Millisecond
	h := &composeHost{state: state}
	a := fakeApp(h)
	a.Root = realTemp(t)
	os.MkdirAll(a.dir(), 0o700)
	return a, h
}

func TestState(t *testing.T) {
	for state, want := range map[string]string{"running": "running", "exited": "stopped", "": "missing", "restarting": "stopped"} {
		a, _ := lifecycleApp(t, state)
		if got, err := State(context.Background(), a); err != nil || got != want {
			t.Errorf("%q: %s, %v; want %s", state, got, err, want)
		}
	}
}

func TestStopAndStartAreRerunnable(t *testing.T) {
	ctx := context.Background()
	a, h := lifecycleApp(t, "running")
	stop, start := StopStep(a), StartStep(a)
	if done, err := start.Inspect(ctx); err != nil || !done {
		t.Fatalf("start on a running app: %v %v", done, err)
	}
	if done, err := stop.Inspect(ctx); err != nil || done {
		t.Fatalf("stop on a running app: %v %v", done, err)
	}
	if err := stop.Apply(ctx); err != nil || stop.Verify(ctx) != nil || h.state != "exited" {
		t.Fatalf("stop: %v, state %s", err, h.state)
	}
	if done, err := stop.Inspect(ctx); err != nil || !done {
		t.Fatalf("stop on a stopped app: %v %v", done, err)
	}
	if err := start.Apply(ctx); err != nil || start.Verify(ctx) != nil || h.state != "running" {
		t.Fatalf("start: %v, state %s", err, h.state)
	}
	if !slices.ContainsFunc(h.calls, func(c string) bool {
		return strings.HasSuffix(c, "docker compose -p 'kyq-hello' -f compose.yaml -f compose.kyq.yaml up -d")
	}) {
		t.Errorf("start ran %q", h.calls)
	}
	// A stop with no containers left has nothing to stop.
	h.state = ""
	if done, err := stop.Inspect(ctx); err != nil || !done {
		t.Fatalf("stop without containers: %v %v", done, err)
	}
}

func TestRestartAlwaysRuns(t *testing.T) {
	ctx := context.Background()
	a, h := lifecycleApp(t, "running")
	r := RestartStep(a, "1")
	if done, err := r.Inspect(ctx); err != nil || done {
		t.Fatalf("restart inspect: %v %v", done, err)
	}
	if err := r.Apply(ctx); err != nil || !strings.HasSuffix(h.calls[0], " restart") {
		t.Fatalf("restart: %v %q", err, h.calls)
	}
	if RestartStep(a, "2").InputHash() == r.InputHash() || r.ID() != "hello.restart" {
		t.Error("two restarts share an input hash, so the engine would skip the second")
	}
}

func TestLifecycleRefusesUnfinishedRestore(t *testing.T) {
	a, h := lifecycleApp(t, "exited")
	os.WriteFile(filepath.Join(a.dir(), ".restore-pending"), []byte(strings.Repeat("ab", 32)+" files\n"), 0o600)
	for _, s := range []interface {
		Inspect(context.Context) (bool, error)
	}{StopStep(a), StartStep(a), RestartStep(a, "1")} {
		if _, err := s.Inspect(context.Background()); err == nil || !strings.Contains(err.Error(), "restore of hello is unfinished") {
			t.Errorf("err = %v", err)
		}
	}
	if len(h.calls) != 0 {
		t.Errorf("docker ran: %q", h.calls)
	}
}

func TestLogs(t *testing.T) {
	a, h := lifecycleApp(t, "running")
	out, err := Logs(context.Background(), a, "web", 5)
	if err != nil || string(out) != "web-1  | ready\n" || !strings.HasSuffix(h.calls[0], " logs --no-color --tail 5 'web'") {
		t.Fatalf("logs %q, %v, ran %q", out, err, h.calls)
	}
}

func TestBackupRefusesStoppedApp(t *testing.T) {
	a, h := lifecycleApp(t, "exited")
	a.Stopped = true
	_, _, err := Store{App: a}.Backup(context.Background())
	var se *suite.Error
	if !errors.As(err, &se) || se.Code != suite.AppStopped || !strings.Contains(err.Error(), "kyquickstart start hello") {
		t.Fatalf("err = %v", err)
	}
	if len(h.calls) != 0 {
		t.Errorf("docker ran: %q", h.calls)
	}
}
