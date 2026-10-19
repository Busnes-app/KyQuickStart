package dockerhost

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"go.yaml.in/yaml/v3"
)

// readOverride returns the app's compose.kyq.yaml, nil when absent.
func (a App) readOverride(ctx context.Context) (map[string]map[string]map[string]string, error) {
	f := remote.Quote(a.dir() + "/compose.kyq.yaml")
	out, err := a.Runner.Run(ctx, "test ! -e "+f+" || head -c 65537 "+f, nil)
	if err != nil {
		return nil, fmt.Errorf("read compose.kyq.yaml: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	var o struct {
		Services map[string]map[string]map[string]string // service -> "labels" -> labels
	}
	if len(out) > 65536 || yaml.Unmarshal(out, &o) != nil || len(o.Services) == 0 {
		return nil, errors.New("compose.kyq.yaml is not an installer override")
	}
	return o.Services, nil
}

// Deployed reads the labels of the app's last deployment from its compose.kyq.yaml, which
// uninstall keeps.
func Deployed(ctx context.Context, a App) (managed.Deployed, error) {
	svcs, err := a.readOverride(ctx)
	if err != nil || svcs == nil {
		return managed.Deployed{}, err
	}
	l := svcs[slices.Sorted(maps.Keys(svcs))[0]]["labels"]
	return managed.Deployed{Found: true, Managed: l[managed.Label] == managed.Value,
		ReleaseSet: l[managed.ReleaseSetLabel], Version: l[managed.VersionLabel]}, nil
}

// containers lists the project's containers, only those carrying label when it is set.
func (a App) containers(ctx context.Context, label string) (string, error) {
	cmd := "docker ps -aq --filter " + remote.Quote("label=com.docker.compose.project=kyq-"+a.Name)
	if label != "" {
		cmd += " --filter " + remote.Quote("label="+label)
	}
	out, err := a.Runner.Run(ctx, cmd, nil)
	if err != nil {
		return "", fmt.Errorf("docker ps: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// RemoveSteps removes the app's containers and networks. Its directory, data volumes and
// secrets stay, so a later install finds them.
func RemoveSteps(a App) []engine.Step { return []engine.Step{removeStep{a}} }

type removeStep struct{ a App }

func (s removeStep) ID() string        { return s.a.Name + ".uninstall" }
func (s removeStep) InputHash() string { return hashOf("uninstall") }

func (s removeStep) Inspect(ctx context.Context) (bool, error) {
	ids, err := s.a.containers(ctx, "")
	return ids == "", err
}

// Apply never passes -v: named volumes hold the app's data.
func (s removeStep) Apply(ctx context.Context) error {
	if _, err := s.a.Runner.Run(ctx, s.a.compose("down --remove-orphans"), nil); err != nil {
		return fmt.Errorf("docker compose down: %w", err)
	}
	return nil
}

func (s removeStep) Verify(ctx context.Context) error {
	ids, err := s.a.containers(ctx, "")
	if err == nil && ids != "" {
		err = errors.New("containers remain")
	}
	return err
}

// UnmanageSteps rewrites compose.kyq.yaml without the managed labels, keeping the rest, and
// recreates existing containers: Docker cannot relabel a container in place.
func UnmanageSteps(a App) []engine.Step { return []engine.Step{unmanageStep{a}} }

type unmanageStep struct{ a App }

func (s unmanageStep) ID() string        { return s.a.Name + ".unmanage" }
func (s unmanageStep) InputHash() string { return hashOf("unmanage") }

// check returns what still carries the managed label, or "".
func (s unmanageStep) check(ctx context.Context) (string, error) {
	d, err := Deployed(ctx, s.a)
	if err != nil || d.Found && d.Managed {
		return "compose.kyq.yaml", err
	}
	ids, err := s.a.containers(ctx, managed.Label)
	if err != nil || ids != "" {
		return "a container", err
	}
	return "", nil
}

func (s unmanageStep) Inspect(ctx context.Context) (bool, error) {
	what, err := s.check(ctx)
	return what == "", err
}

// Apply starts nothing that was not running: only a project with containers is recreated,
// unstarted, and only the services that ran are started again.
func (s unmanageStep) Apply(ctx context.Context) error {
	if err := refuseUnfinishedRestore(ctx, s.a); err != nil {
		return err
	}
	svcs, err := s.a.readOverride(ctx)
	if err != nil {
		return err
	}
	if svcs != nil {
		for _, svc := range svcs {
			delete(svc["labels"], managed.Label)
			delete(svc["labels"], managed.ReleaseSetLabel)
		}
		b, err := yaml.Marshal(map[string]any{"services": svcs})
		if err != nil {
			return err
		}
		// WriteConfig also drops .input-hash: this deployment is no longer the installer's.
		if err := WriteConfig(ctx, s.a.Runner, s.a.dir()+"/compose.kyq.yaml", b); err != nil {
			return err
		}
	}
	ids, err := s.a.containers(ctx, "")
	if err != nil || ids == "" {
		return err
	}
	// Recreate every container unstarted, then start only the services that were running.
	cs, err := s.a.ps(ctx)
	if err != nil {
		return err
	}
	var running []string
	for _, c := range cs {
		if c.State == "running" && !slices.Contains(running, c.Service) {
			running = append(running, c.Service)
		}
	}
	if _, err := s.a.Runner.Run(ctx, s.a.compose("up --no-start"), nil); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}
	if len(running) == 0 {
		return nil
	}
	if _, err := s.a.Runner.Run(ctx, s.a.compose("start "+quoteAll(running)), nil); err != nil {
		return fmt.Errorf("docker compose start: %w", err)
	}
	return nil
}

func (s unmanageStep) Verify(ctx context.Context) error {
	what, err := s.check(ctx)
	if err == nil && what != "" {
		err = fmt.Errorf("%s still carries %s", what, managed.Label)
	}
	return err
}
