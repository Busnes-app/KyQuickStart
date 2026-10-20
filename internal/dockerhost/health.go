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
	// skip are the services apply's setup configures (health.after_setup); restore and
	// backup wait for them too.
	skip []string
}

func (h healthStep) ID() string        { return h.a.Name + ".health" }
func (h healthStep) InputHash() string { return h.hash }

func (h healthStep) state(ctx context.Context) (string, error) {
	cs, err := h.a.ps(ctx)
	if err != nil {
		return "", err
	}
	cs = slices.DeleteFunc(cs, func(c container) bool { return slices.Contains(h.skip, c.Service) })
	return unhealthy(cs, without(h.a.Catalog.Services, h.skip)), nil
}

// without is services less skip.
func without(services, skip []string) []string {
	return slices.DeleteFunc(slices.Clone(services), func(s string) bool { return slices.Contains(skip, s) })
}

// WaitHealthy waits, up to the app's health timeout, until service runs and is healthy.
func WaitHealthy(ctx context.Context, a App, service string) error {
	a.Catalog.Services = []string{service}
	return healthStep{a: a}.Apply(ctx)
}

// Healthy reports why service is not running and healthy, nil if it is.
func Healthy(ctx context.Context, a App, service string) error {
	a.Catalog.Services = []string{service}
	return healthStep{a: a}.Verify(ctx)
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
