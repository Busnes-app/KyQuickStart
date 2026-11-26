package dockerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

var pollInterval = 2 * time.Second

type container struct {
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
	Exit    int    `json:"ExitCode"`
}

// parsePS reads `docker compose ps --format json`: one object per line (Compose 2.21+).
func parsePS(out []byte) ([]container, error) {
	var cs []container
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var c container
		if err := json.Unmarshal(line, &c); err != nil {
			return nil, fmt.Errorf("docker compose ps output: %w", err)
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// notRunning names the first service without a running container, or "".
func notRunning(cs []container, services []string) string {
	up := map[string]bool{}
	for _, c := range cs {
		if c.State == "running" {
			up[c.Service] = true
		}
	}
	for _, s := range services {
		if !up[s] {
			return "service " + s + " is not running"
		}
	}
	return ""
}

// failedOnce names the first one-shot (catalog Once) that exited non-zero, or "". One that
// exited 0 is done: it never has to be running.
func failedOnce(cs []container, once []string) string {
	for _, c := range cs {
		if c.State == "exited" && c.Exit != 0 && slices.Contains(once, c.Service) {
			return c.Service
		}
	}
	return ""
}

// unhealthy names the first service not running or not yet healthy, or "".
// A container without a healthcheck counts as healthy once running.
func unhealthy(cs []container, services []string) string {
	if why := notRunning(cs, services); why != "" {
		return why
	}
	for _, c := range cs {
		if c.State == "running" && c.Health != "" && c.Health != "healthy" {
			return "service " + c.Service + " is " + c.Health
		}
	}
	return ""
}

type healthStep struct {
	a    App
	hash string
}

func (h healthStep) ID() string        { return h.a.Name + ".health" }
func (h healthStep) InputHash() string { return h.hash }

func (h healthStep) state(ctx context.Context) (string, error) {
	cs, err := h.a.ps(ctx)
	if err != nil {
		return "", err
	}
	if s := failedOnce(cs, h.a.Catalog.Once); s != "" {
		return "", h.a.onceFailed(ctx, s)
	}
	return unhealthy(cs, h.a.Catalog.Services), nil
}

// onceFailed is the error for one-shot service s that exited non-zero, with its last logs:
// waiting will not change it, and its output says why (and what the operator can do).
func (a App) onceFailed(ctx context.Context, s string) error {
	logs, err := Logs(ctx, a, s, 40)
	if err != nil {
		return fmt.Errorf("service %s failed; its logs: %w", s, err)
	}
	return fmt.Errorf("service %s failed:\n%s", s, bytes.TrimSpace(logs))
}

// upFailed is the error of docker compose cmd: with the output of a one-shot that failed,
// which stops up and says why.
func (a App) upFailed(ctx context.Context, cmd string, err error) error {
	if cs, perr := a.ps(ctx); perr == nil {
		if s := failedOnce(cs, a.Catalog.Once); s != "" {
			return fmt.Errorf("docker compose %s: %w: %w", cmd, err, a.onceFailed(ctx, s))
		}
	}
	return fmt.Errorf("docker compose %s: %w", cmd, err)
}

func (h healthStep) Inspect(ctx context.Context) (bool, error) {
	why, err := h.state(ctx)
	return why == "", err
}

// Apply waits for health; it changes nothing.
func (h healthStep) Apply(ctx context.Context) error {
	timeout := time.Duration(h.a.Catalog.Health.TimeoutSeconds) * time.Second
	deadline := time.Now().Add(timeout)
	for {
		why, err := h.state(ctx)
		if err != nil || why == "" {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not healthy after %s: %s", timeout, why)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (h healthStep) Verify(ctx context.Context) error {
	why, err := h.state(ctx)
	if err != nil {
		return err
	}
	if why != "" {
		return errors.New(why)
	}
	return nil
}
