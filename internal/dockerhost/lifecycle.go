package dockerhost

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// State reads the app's containers: running (every service), stopped (none), missing (no
// containers) or unknown (some services running).
func State(ctx context.Context, a App) (string, error) {
	cs, err := a.ps(ctx)
	switch {
	case err != nil:
		return "", err
	case len(cs) == 0:
		return "missing", nil
	case notRunning(cs, a.Catalog.Services) == "":
		return "running", nil
	case !slices.ContainsFunc(cs, func(c container) bool { return c.State == "running" }):
		return "stopped", nil
	}
	return "unknown", nil
}

// StopStep stops the app's containers, keeping them; a stopped app is left alone.
func StopStep(a App) engine.Step { return lifecycleStep{a: a, op: "stop"} }

// StartStep starts the app (creating containers a hand removal lost) and waits for health.
func StartStep(a App) engine.Step { return lifecycleStep{a: a, op: "start"} }

// RestartStep restarts the app and waits for health. run makes each restart a new step, so a
// rerun restarts again.
func RestartStep(a App, run string) engine.Step { return lifecycleStep{a: a, op: "restart", run: run} }

type lifecycleStep struct {
	a       App
	op, run string
}

func (s lifecycleStep) ID() string        { return s.a.Name + "." + s.op }
func (s lifecycleStep) InputHash() string { return hashOf(s.op, s.run) }

func (s lifecycleStep) Inspect(ctx context.Context) (bool, error) {
	if err := refuseUnfinishedRestore(ctx, s.a); err != nil {
		return false, err
	}
	if s.op == "restart" {
		return false, nil
	}
	return s.Verify(ctx) == nil, nil
}

func (s lifecycleStep) Apply(ctx context.Context) error {
	cmd := map[string]string{"stop": "stop", "start": "up -d", "restart": "restart"}[s.op]
	if _, err := s.a.Runner.Run(ctx, s.a.compose(cmd), nil); err != nil {
		return fmt.Errorf("docker compose %s: %w", cmd, err)
	}
	if s.op == "stop" {
		return nil
	}
	return healthStep{a: s.a}.Apply(ctx)
}

func (s lifecycleStep) Verify(ctx context.Context) error {
	if s.op != "stop" {
		return healthStep{a: s.a}.Verify(ctx)
	}
	state, err := State(ctx, s.a)
	if err == nil && state != "stopped" && state != "missing" {
		err = fmt.Errorf("%s is %s", s.a.Name, state)
	}
	return err
}

// Logs returns the last tail lines of each of the app's containers, or of service alone.
// It changes nothing.
func Logs(ctx context.Context, a App, service string, tail int) ([]byte, error) {
	cmd := "logs --no-color --tail " + strconv.Itoa(tail)
	if service != "" {
		cmd += " " + remote.Quote(service)
	}
	out, err := a.Runner.Run(ctx, a.compose(cmd), nil)
	if err != nil {
		return nil, fmt.Errorf("docker compose logs: %w", err)
	}
	return out, nil
}
