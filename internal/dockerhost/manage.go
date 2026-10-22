package dockerhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"go.yaml.in/yaml/v3"
)

// readOverride returns the app's compose.kyq.yaml, nil when absent. Any key override does
// not write makes it not ours.
func (a App) readOverride(ctx context.Context) (*overrideFile, error) {
	f := remote.Quote(a.dir() + "/compose.kyq.yaml")
	out, err := a.Runner.Run(ctx, "test ! -e "+f+" || head -c 65537 "+f, nil)
	if err != nil {
		return nil, fmt.Errorf("read compose.kyq.yaml: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	var o overrideFile
	dec := yaml.NewDecoder(bytes.NewReader(out))
	dec.KnownFields(true)
	if len(out) > 65536 || dec.Decode(&o) != nil || len(o.Services) == 0 {
		return nil, errors.New("compose.kyq.yaml is not an installer override")
	}
	return &o, nil
}

// Deployed reads the labels of the app's last deployment from its compose.kyq.yaml, which
// uninstall keeps.
func Deployed(ctx context.Context, a App) (managed.Deployed, error) {
	o, err := a.readOverride(ctx)
	if err != nil || o == nil {
		return managed.Deployed{}, err
	}
	l := o.Services[slices.Sorted(maps.Keys(o.Services))[0]].Labels
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

// check returns what still carries the managed label, or an unfinished recreate, or "".
func (s unmanageStep) check(ctx context.Context) (string, error) {
	out, err := s.a.Runner.Run(ctx, "if test -e "+remote.Quote(s.record())+"; then echo yes; fi", nil)
	if err != nil || strings.TrimSpace(string(out)) == "yes" {
		return "an unfinished recreate (" + unmanageRecord + ")", err
	}
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

// unmanageRecord holds, while unmanage recreates the containers, the services to recreate
// and those to start again, so a rerun after dying halfway finishes with the original sets.
const unmanageRecord = ".unmanage-running"

func (s unmanageStep) record() string { return path.Join(s.a.dir(), unmanageRecord) }

// Apply starts nothing that was not running: the services that have containers are
// recreated unstarted, and those that were running or restarting are started again. A
// paused service stays stopped.
func (s unmanageStep) Apply(ctx context.Context) error {
	if err := refuseUnfinishedRestore(ctx, s.a); err != nil {
		return err
	}
	o, err := s.a.readOverride(ctx)
	if err != nil {
		return err
	}
	if o != nil {
		for _, svc := range o.Services {
			delete(svc.Labels, managed.Label)
			delete(svc.Labels, managed.ReleaseSetLabel)
		}
		b, err := yaml.Marshal(o)
		if err != nil {
			return err
		}
		// WriteConfig also drops .input-hash: this deployment is no longer the installer's.
		if err := WriteConfig(ctx, s.a.Runner, s.a.dir()+"/compose.kyq.yaml", b); err != nil {
			return err
		}
	}
	recreate, start, err := s.plan(ctx)
	if err != nil || len(recreate) == 0 {
		return err
	}
	if _, err := s.a.Runner.Run(ctx, s.a.compose("up --no-start --no-deps "+quoteAll(recreate)), nil); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}
	if len(start) > 0 {
		if _, err := s.a.Runner.Run(ctx, s.a.compose("start "+quoteAll(start)), nil); err != nil {
			return fmt.Errorf("%s's %s are recreated but stopped; rerun unmanage, or start them with docker compose -p kyq-%s start %s: %w", s.a.Name, strings.Join(start, ", "), s.a.Name, strings.Join(start, " "), err)
		}
	}
	_, err = s.a.Runner.Run(ctx, "rm -f "+remote.Quote(s.record()), nil)
	return err
}

// plan returns the services to recreate and to start: from the record a dying run left, or
// read from the project and recorded before anything is recreated.
func (s unmanageStep) plan(ctx context.Context) (recreate, start []string, err error) {
	f := remote.Quote(s.record())
	out, err := s.a.Runner.Run(ctx, "if test -e "+f+"; then head -c 4096 "+f+"; fi", nil)
	if err != nil {
		return nil, nil, err
	}
	if len(out) == 0 {
		ids, err := s.a.containers(ctx, "")
		if err != nil || ids == "" {
			return nil, nil, err
		}
		cs, err := s.a.ps(ctx)
		if err != nil || len(cs) == 0 {
			return nil, nil, err
		}
		for _, c := range cs {
			if !slices.Contains(recreate, c.Service) {
				recreate = append(recreate, c.Service)
			}
			if (c.State == "running" || c.State == "restarting") && !slices.Contains(start, c.Service) {
				start = append(start, c.Service)
			}
		}
		body := "recreate " + strings.Join(recreate, " ") + "\nstart " + strings.Join(start, " ") + "\n"
		cmd := "umask 077; t=$(mktemp " + remote.Quote(s.record()+".XXXXXX") + ") && { cat > \"$t\" && mv -f \"$t\" " + f + "; r=$?; rm -f \"$t\"; exit $r; }"
		if _, err := s.a.Runner.Run(ctx, cmd, []byte(body)); err != nil {
			return nil, nil, fmt.Errorf("record the running services: %w", err)
		}
		return recreate, start, nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	valid := func(names []string) bool {
		return !slices.ContainsFunc(names, func(n string) bool { return !slices.Contains(s.a.Catalog.Services, n) })
	}
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "recreate") || !strings.HasPrefix(lines[1], "start") {
		return nil, nil, fmt.Errorf("%s is not an unmanage record; inspect it by hand", s.record())
	}
	recreate, start = strings.Fields(lines[0])[1:], strings.Fields(lines[1])[1:]
	if !valid(recreate) || !valid(start) {
		return nil, nil, fmt.Errorf("%s names a service %s does not have; inspect it by hand", s.record(), s.a.Name)
	}
	return recreate, start, nil
}

func (s unmanageStep) Verify(ctx context.Context) error {
	what, err := s.check(ctx)
	if err == nil && what != "" {
		err = fmt.Errorf("%s still carries %s", what, managed.Label)
	}
	return err
}
