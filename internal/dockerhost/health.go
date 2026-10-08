package dockerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var pollInterval = 2 * time.Second

type container struct {
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
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
	return unhealthy(cs, h.a.Catalog.Services), nil
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
