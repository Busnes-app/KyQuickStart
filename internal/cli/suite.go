package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	"go.yaml.in/yaml/v3"
)

// Request is one suite operation. Apps holds exactly one app, except for Upgrade, where none
// means every installed app. Target is for Install only.
type Request struct {
	Apps                []string
	Target              string
	AcknowledgeNoBackup bool
	Holder              string // the lock holder; empty names this process
}

// Install adds an app and its missing dependencies to the plan at state and deploys them.
func Install(ctx context.Context, state string, r Request, o Options) error {
	return install(ctx, state, r, nil, o)
}

// Uninstall removes an app's workloads and drops it from the plan; data and secrets stay.
func Uninstall(ctx context.Context, state string, r Request, o Options) error {
	return remove(ctx, state, r, nil, o, false)
}

// Unmanage hands an app to its owner: managed labels go, the plan drops it, handover.md says so.
func Unmanage(ctx context.Context, state string, r Request, o Options) error {
	return remove(ctx, state, r, nil, o, true)
}

// Upgrade redeploys installed apps from this release set, gated by their upgrade rules.
func Upgrade(ctx context.Context, state string, r Request, o Options) error {
	return upgrade(ctx, state, r, nil, o)
}

// Status reports every installed app; it reads targets and changes nothing.
func Status(ctx context.Context, state string, o Options) (suite.Status, error) {
	return status(ctx, state, nil, o)
}

// Catalog lists this release set's apps for the plan at state; it connects to nothing.
func Catalog(state string, o Options) (suite.Catalog, error) {
	s, err := load(state, nil, o)
	if err != nil {
		return suite.Catalog{}, err
	}
	out := suite.Catalog{ReleaseSet: o.ReleaseSet, Apps: []suite.CatalogApp{}}
	for _, name := range slices.Sorted(maps.Keys(s.cat)) {
		a := s.cat[name]
		_, installed := s.placed[name]
		out.Apps = append(out.Apps, suite.CatalogApp{
			Name: name, Description: a.Description, Category: a.Category, Version: a.Version,
			DependsOn: append([]string{}, a.DependsOn...), Installed: installed,
			Upgrade: suite.Upgrade{MinFrom: a.Upgrade.MinFrom, NoSkip: append([]string{}, a.Upgrade.NoSkip...), MigratesData: a.Upgrade.MigratesData},
		})
	}
	return out, nil
}

// suiteCommand parses an install, uninstall, unmanage, upgrade or status command line.
func suiteCommand(ctx context.Context, cmd string, args []string, o Options) error {
	fl := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fl.SetOutput(o.Out)
	state := fl.String("state", ".", "state directory holding stack.yaml")
	trust := trustFlag{}
	fl.Var(trust, "trust-host-key", "accept an unknown host key: <target>=SHA256:<fingerprint>")
	var r Request
	if cmd == "install" {
		fl.StringVar(&r.Target, "target", "", "target to install on")
	}
	if cmd == "install" || cmd == "upgrade" {
		fl.BoolVar(&r.AcknowledgeNoBackup, "acknowledge-no-backup", false, "upgrade data without a pre-upgrade backup")
	}
	asJSON := fl.Bool("json", false, "status: print JSON")
	// App names and flags may come in any order.
	for rest := args; ; rest = fl.Args()[1:] {
		if err := fl.Parse(rest); err != nil {
			return err
		}
		if fl.NArg() == 0 {
			break
		}
		r.Apps = append(r.Apps, fl.Arg(0))
	}
	if *asJSON && cmd != "status" {
		return errors.New("--json is only for status")
	}
	switch cmd {
	case "install":
		return install(ctx, *state, r, trust, o)
	case "uninstall", "unmanage":
		return remove(ctx, *state, r, trust, o, cmd == "unmanage")
	case "upgrade":
		return upgrade(ctx, *state, r, trust, o)
	}
	if len(r.Apps) > 0 {
		return ErrUsage
	}
	st, err := status(ctx, *state, trust, o)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(o.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	fmt.Fprintf(o.Out, "plan revision %d, owner %s\n", st.Runner.PlanRevision, st.Runner.PlanOwner)
	for _, a := range st.Apps {
		fmt.Fprintf(o.Out, "%-16s %-10s %-12s %s\n", a.Name, a.Version, a.Target, a.State)
	}
	return nil
}

func oneApp(r Request) (string, error) {
	if len(r.Apps) != 1 {
		return "", suite.Errorf(suite.InvalidRequest, "name exactly one app")
	}
	return r.Apps[0], nil
}

// installed refuses an app that is not in the plan.
func (s *session) installed(name string) (stack.Target, error) {
	t, ok := s.placed[name]
	if ok {
		return t, nil
	}
	if _, known := s.cat[name]; known {
		return t, suite.Errorf(suite.NotInstalled, "%s is not installed", name)
	}
	return t, suite.Errorf(suite.UnknownApp, "%s is not in the catalog", name)
}

func install(ctx context.Context, state string, r Request, trust trustFlag, o Options) error {
	name, err := oneApp(r)
	if err != nil {
		return err
	}
	s, err := load(state, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
	if _, ok := s.cat[name]; !ok {
		return suite.Errorf(suite.UnknownApp, "%s is not in the catalog", name)
	}
	if _, ok := s.placed[name]; ok {
		return suite.Errorf(suite.AlreadyInstalled, "%s is already installed on %s", name, s.placed[name].Name)
	}
	if !slices.ContainsFunc(s.allTargets, func(t stack.Target) bool { return t.Name == r.Target }) {
		return suite.Errorf(suite.UnknownTarget, "target %q is not in stack.yaml", r.Target)
	}
	// The app and every dependency not yet installed, all on the requested target.
	added := map[string]bool{}
	var add func(string)
	add = func(a string) {
		if _, ok := s.placed[a]; ok || added[a] {
			return
		}
		added[a] = true
		for _, d := range s.cat[a].DependsOn {
			add(d)
		}
	}
	add(name)
	next, data, err := changePlan(state, s.revision, func(st *stack.Stack) {
		for _, a := range slices.Sorted(maps.Keys(added)) {
			st.Apps = append(st.Apps, stack.App{Name: a, Target: r.Target})
		}
	})
	if err != nil {
		return err
	}
	ns, err := newSession(next, state, trust, o)
	if err != nil {
		return suite.Errorf(suite.InvalidRequest, "%w", err)
	}
	defer ns.close()
	ns.revision, ns.saved, ns.ack, ns.edges, ns.changesPlan = s.revision, s.saved, r.AcknowledgeNoBackup, nil, true
	ns.order = slices.DeleteFunc(ns.order, func(a string) bool { return !added[a] })
	ns.targets = []stack.Target{ns.placed[name]}
	if err := ns.connect(ctx, trust); err != nil {
		return err
	}
	return ns.operate(ctx, r.Holder, func(ctx context.Context) ([]engine.Step, error) {
		if err := ns.preflight(ctx); err != nil {
			return nil, err
		}
		return ns.deploySteps(ctx)
	}, func() error { return publish(state, s.revision, data) })
}

func upgrade(ctx context.Context, state string, r Request, trust trustFlag, o Options) error {
	s, err := load(state, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
	for _, name := range r.Apps {
		if _, err := s.installed(name); err != nil {
			return err
		}
	}
	if len(r.Apps) > 0 {
		s.order = slices.DeleteFunc(s.order, func(a string) bool { return !slices.Contains(r.Apps, a) })
	}
	if len(s.order) == 0 {
		return suite.Errorf(suite.InvalidRequest, "no apps are installed")
	}
	s.targets = slices.DeleteFunc(s.targets, func(t stack.Target) bool {
		return !slices.ContainsFunc(s.order, func(a string) bool { return s.placed[a].Name == t.Name })
	})
	s.ack, s.edges = r.AcknowledgeNoBackup, nil
	if err := s.connect(ctx, trust); err != nil {
		return err
	}
	return s.operate(ctx, r.Holder, func(ctx context.Context) ([]engine.Step, error) {
		if err := s.preflight(ctx); err != nil {
			return nil, err
		}
		return s.deploySteps(ctx)
	}, nil)
}

// remove uninstalls or unmanages one app, disabling its public routes first.
func remove(ctx context.Context, state string, r Request, trust trustFlag, o Options, unmanage bool) error {
	name, err := oneApp(r)
	if err != nil {
		return err
	}
	s, err := load(state, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
	t, err := s.installed(name)
	if err != nil {
		return err
	}
	for _, other := range s.order {
		if slices.Contains(s.cat[other].DependsOn, name) {
			return suite.Errorf(suite.Dependency, "%s depends on %s", other, name)
		}
	}
	routed := func(r stack.Route) bool { return r.App == name }
	next, data, err := changePlan(state, s.revision, func(st *stack.Stack) {
		st.Apps = slices.DeleteFunc(st.Apps, func(a stack.App) bool { return a.Name == name })
		for i := range st.Edges {
			st.Edges[i].Routes = slices.DeleteFunc(st.Edges[i].Routes, routed)
		}
		st.Edges = slices.DeleteFunc(st.Edges, func(e stack.Edge) bool { return len(e.Routes) == 0 })
	})
	if err != nil {
		return err
	}
	// The app's edges without its routes: their removed-route steps disable them in NPM. An
	// edge may be on another target, which is then locked too.
	var edges []stack.Edge
	var hostnames []string
	targets := []stack.Target{t}
	for _, e := range s.edges {
		if slices.ContainsFunc(e.Routes, routed) {
			if e.Target != t.Name {
				targets = append(targets, s.allTargets[slices.IndexFunc(s.allTargets, func(x stack.Target) bool { return x.Name == e.Target })])
			}
			for _, r := range e.Routes {
				if routed(r) {
					hostnames = append(hostnames, r.Hostname)
				}
			}
			e.Routes = slices.DeleteFunc(slices.Clone(e.Routes), routed)
			edges = append(edges, e)
		}
	}
	s.targets, s.edges, s.changesPlan, s.installation = targets, edges, true, next.InstallationID
	if err := s.connect(ctx, trust); err != nil {
		return err
	}
	d := s.drivers[t.Name]
	var handover string
	return s.operate(ctx, r.Holder, func(ctx context.Context) ([]engine.Step, error) {
		var steps []engine.Step
		for _, e := range edges {
			steps = append(steps, s.npms[e.Target].Steps(e)[0]) // the removed-route step
		}
		if !unmanage {
			return append(steps, d.removeSteps(name, s.cat[name])...), nil
		}
		deployed, err := d.deployed(ctx, name)
		if err != nil {
			return nil, err
		}
		handover = handoverSection(name, t, installedVersion(deployed, s.cat[name], s.o.ReleaseSet), hostnames)
		return append(steps, d.unmanageSteps(name, s.cat[name])...), nil
	}, func() error {
		if unmanage {
			if err := appendHandover(state, handover); err != nil {
				return err
			}
		}
		return publish(state, s.revision, data)
	})
}

func handoverSection(name string, t stack.Target, version string, hostnames []string) string {
	if version == "" {
		version = "(version unknown)"
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, "\n## %s %s on %s is no longer managed\n\n", name, version, t.Name)
	fmt.Fprintf(b, "kyquickstart and KyYard no longer upgrade, back up or remove %s. Its data and secrets stay on %s.\n", name, t.Name)
	if t.Kubernetes() {
		fmt.Fprintf(b, "It runs in namespace %s; its objects no longer carry %s.\n", "kyq-"+name, managed.Label)
	} else {
		fmt.Fprintf(b, "Its Compose project kyq-%s is in %s; run it with `docker compose -p kyq-%s -f compose.yaml -f compose.kyq.yaml up -d` there.\n", name, path.Join(t.Root, name), name)
	}
	if len(hostnames) > 0 {
		fmt.Fprintf(b, "Its public hostnames (%s) were disabled in Nginx Proxy Manager. They still carry the installer's ownership marks, so a later apply keeps them disabled; serve the app through a proxy host you create yourself.\n", strings.Join(hostnames, ", "))
	}
	return b.String()
}

// appendHandover adds section to <state>/handover.md unless a rerun already did.
func appendHandover(state, section string) error {
	file := filepath.Join(state, "handover.md")
	b, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if strings.Contains(string(b), section) {
		return nil
	}
	return atomicFile(file, append(b, section...))
}

// changePlan applies change to the saved plan, which must still be at revision, and validates
// the result. Nothing is written.
func changePlan(state string, revision int, change func(*stack.Stack)) (*stack.Stack, []byte, error) {
	st, err := stack.Load(filepath.Join(state, "stack.yaml"))
	if err != nil {
		return nil, nil, err
	}
	if st.Revision != revision {
		return nil, nil, suite.Errorf(suite.PlanStale, "stack.yaml changed to revision %d while this command ran; rerun it", st.Revision)
	}
	change(st)
	if st.InstallationID == "" { // written once
		st.InstallationID = stack.NewInstallationID()
	}
	st.Revision = revision + 1
	data, err := yaml.Marshal(st)
	if err != nil {
		return nil, nil, err
	}
	next, err := stack.Parse(data)
	if err != nil {
		return nil, nil, suite.Errorf(suite.InvalidRequest, "%w", err)
	}
	return next, data, nil
}

// publish writes data as stack.yaml, holding the plan editor lock, if the saved plan is
// still at revision.
func publish(state string, revision int, data []byte) error {
	lock := filepath.Join(state, "plan.lock")
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("plan not saved: another plan editor may be active; if it exited unexpectedly, remove plan.lock and rerun")
	}
	f.Close()
	defer os.Remove(lock)
	st, err := stack.Load(filepath.Join(state, "stack.yaml"))
	if err != nil {
		return err
	}
	if st.Revision != revision {
		return suite.Errorf(suite.PlanStale, "plan not saved: stack.yaml changed to revision %d while this command ran; rerun it", st.Revision)
	}
	return atomicFile(filepath.Join(state, "stack.yaml"), data)
}

// installedVersion is the deployed version, or "" when unknown. A deployment by this
// release set that recorded no version predates recording but ran this catalog.
func installedVersion(d managed.Deployed, app catalog.App, releaseSet string) string {
	if d.Found && d.Version == "" && d.ReleaseSet == releaseSet {
		return app.Version
	}
	return d.Version
}

// gate refuses deploying app over what its target holds unless the upgrade rules allow it.
// It returns the note to record for an upgrade, or "" when there is nothing to upgrade.
func gate(name string, app catalog.App, d managed.Deployed, releaseSet string, ack bool) (string, error) {
	if !d.Found {
		return "", nil
	}
	if !d.Managed {
		return "", suite.Errorf(suite.AlreadyInstalled, "%s exists on its target but is not managed by kyquickstart; remove it by hand first", name)
	}
	from := installedVersion(d, app, releaseSet)
	switch {
	case from == "":
		return "", suite.Errorf(suite.UpgradeRefused, "%s: installed version unknown (deployed by release set %q, before versions were recorded), so its upgrade rules cannot be checked", name, d.ReleaseSet)
	case from == app.Version:
		return "", nil
	}
	if err := app.Upgrade.Allows(from, app.Version); err != nil {
		return "", suite.Errorf(suite.UpgradeRefused, "%s: %w", name, err)
	}
	if app.Upgrade.MigratesData {
		return "", suite.Errorf(suite.BackupRequired, "%s %s migrates data and needs a verified pre-upgrade backup, which this build cannot take yet", name, app.Version)
	}
	if !ack {
		return "", suite.Errorf(suite.AckRequired, "upgrading %s %s to %s takes no pre-upgrade backup; pass --acknowledge-no-backup to proceed", name, from, app.Version)
	}
	return fmt.Sprintf("upgrade %s to %s; operator acknowledged no pre-upgrade backup", from, app.Version), nil
}

// noteStep changes nothing; its ok result records the note.
type noteStep struct{ id, note string }

func (n noteStep) ID() string                            { return n.id }
func (n noteStep) InputHash() string                     { return n.note }
func (n noteStep) Inspect(context.Context) (bool, error) { return true, nil }
func (n noteStep) Apply(context.Context) error           { return nil }
func (n noteStep) Verify(context.Context) error          { return nil }
func (n noteStep) Note() string                          { return n.note }

func status(ctx context.Context, state string, trust trustFlag, o Options) (suite.Status, error) {
	s, err := load(state, trust, o)
	if err != nil {
		return suite.Status{}, err
	}
	defer s.close()
	s.edges = nil
	if err := s.connect(ctx, trust); err != nil {
		return suite.Status{}, err
	}
	st := suite.Status{
		Runner:  suite.Runner{ReleaseSet: o.ReleaseSet, PlanRevision: s.revision, PlanOwner: "cli"},
		Targets: []suite.Target{},
		Apps:    []suite.App{},
	}
	for _, t := range s.allTargets {
		kind := "docker"
		if t.Kubernetes() {
			kind = "kubernetes"
		}
		st.Targets = append(st.Targets, suite.Target{Name: t.Name, Kind: kind})
	}
	for _, t := range s.targets {
		m, err := s.drivers[t.Name].marker(ctx)
		if err != nil {
			return suite.Status{}, fmt.Errorf("%s: %w", t.Name, err)
		}
		// The runner reports whatever it finds, including a plan it does not own.
		if s.o.RunnerID == "" {
			if err := s.guard(t.Name, m); err != nil {
				return suite.Status{}, err
			}
		}
		if m.Owner == "runner" {
			st.Runner.PlanOwner, st.Runner.RunnerID = m.Owner, m.RunnerID
		}
	}
	dir := filepath.Join(state, "results")
	for _, name := range s.order {
		t := s.placed[name]
		deployed, err := s.drivers[t.Name].deployed(ctx, name)
		if err != nil {
			return suite.Status{}, fmt.Errorf("%s: %w", name, err)
		}
		a := suite.App{Name: name, Category: s.cat[name].Category, Version: installedVersion(deployed, s.cat[name], o.ReleaseSet), Target: t.Name, State: "unknown"}
		files, _ := filepath.Glob(filepath.Join(dir, name+".*.json"))
		for _, f := range files {
			id := strings.TrimSuffix(filepath.Base(f), ".json")
			step := strings.TrimPrefix(id, name+".")
			r, ok := engine.Read(dir, id)
			if strings.Contains(step, ".") || !ok { // a target's edge steps share the prefix
				continue
			}
			if step == "health" {
				a.Health = &suite.Health{OK: r.Status == "ok", Checked: r.Time}
			}
			if a.LastResult == nil || r.Time.After(a.LastResult.Time) {
				a.LastResult = &suite.Result{Step: id, Status: r.Status, Time: r.Time}
			}
		}
		switch {
		case a.LastResult != nil && a.LastResult.Status == "failed":
			a.State = "failed"
		case a.Health != nil && a.Health.OK:
			a.State = "installed"
		}
		st.Apps = append(st.Apps, a)
	}
	return st, nil
}
