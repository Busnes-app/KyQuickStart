// Package cli wires the kyquickstart commands.
package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/plan"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
)

// ErrUsage means the arguments name no command; main prints Usage.
var ErrUsage = errors.New("usage")

const Usage = `usage: kyquickstart <command> [flags]

commands:
  plan        guided setup; save or edit a secret-free installation plan
  review      display and validate the saved plan without connecting
  preflight   check every target; changes nothing
  apply       preflight, then install the apps in stack.yaml
              [--adopt-edge-hosts] once: claim this target's NPM hosts marked by builds without installation_id
  install APP --target T   add APP and its missing dependencies to the plan and deploy them
  uninstall APP            remove APP's workloads; its data and secrets stay on the target
  unmanage APP             hand APP to its owner: drop the managed labels, record it in handover.md
  upgrade [APP...]         redeploy installed apps (default all) under their upgrade rules
  status [--json]          per installed app: version, target, state, last results
  handoff --kyyard-target T [--runner-container NAME] [--revoke]
                           hand the plan and target access to the suite runner on T, or take them back
  pull-plan --kyyard-target T [--runner-container NAME]
                           copy the runner's plan here (stack.yaml.bak keeps the previous one)
  integrate-identity configure a selected existing KyIdentity container from a protected bundle
  integrate-product  configure a selected product through its container-local setup adapter
  integrate-stack    run explicitly selected product integrations saved in stack.yaml
  verify-edge check NPM and product forwarded headers through public HTTPS (read-only)
  version     print the release set

flags for preflight, apply, verify-edge and the app commands:
  --state DIR                          state directory holding stack.yaml (default .)
  --trust-host-key NAME=SHA256:<fp>    accept this unknown host key for target NAME
  --expected-client-ip ADDR           required for verify-edge: expected public client address
  --acknowledge-no-backup             apply, install, upgrade: upgrade without a pre-upgrade backup`

type Options struct {
	Catalog    fs.FS
	ReleaseSet string
	In         io.Reader // answers host key prompts; nil when not interactive
	Out        io.Writer
	Progress   func(engine.Event) // optional: every step's start and end
	Redact     *engine.Redactor   // optional: shared with the caller, which redacts its own output
	// The suite runner sets these: its ID, which it requires in every target's plan marker,
	// and its SSH key file in place of the agent.
	RunnerID string
	SSHKey   string
}

func Run(ctx context.Context, args []string, o Options) error {
	if len(args) == 0 {
		if o.In != nil {
			return guided(ctx, []string{}, o)
		}
		return ErrUsage
	}
	cmd := args[0]
	switch cmd {
	case "plan":
		return guided(ctx, args[1:], o)
	case "integrate-identity":
		return integrateIdentity(ctx, args[1:], o)
	case "integrate-product":
		return integrateProduct(ctx, args[1:], o, "")
	case "integrate-stack":
		return integrateStack(ctx, args[1:], o)
	case "review":
		return review(args[1:], o)
	case "install", "uninstall", "unmanage", "upgrade", "status":
		return suiteCommand(ctx, cmd, args[1:], o)
	case "handoff", "pull-plan":
		return handoffCommand(ctx, cmd, args[1:], o)
	case "version":
		_, err := fmt.Fprintln(o.Out, o.ReleaseSet)
		return err
	case "preflight", "apply", "verify-edge":
	default:
		return ErrUsage
	}
	fl := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fl.SetOutput(o.Out)
	state := fl.String("state", ".", "state directory holding stack.yaml")
	clientIP := fl.String("expected-client-ip", "", "verify-edge: this workstation's expected public client address")
	ack := fl.Bool("acknowledge-no-backup", false, "apply: upgrade data without a pre-upgrade backup")
	adopt := fl.Bool("adopt-edge-hosts", false, "apply: claim and re-mark this target's NPM hosts from builds without installation_id")
	trust := trustFlag{}
	fl.Var(trust, "trust-host-key", "accept an unknown host key: <target>=SHA256:<fingerprint>")
	if err := fl.Parse(args[1:]); err != nil {
		return err
	}
	if fl.NArg() > 0 {
		return ErrUsage
	}
	var expected netip.Addr
	if cmd == "verify-edge" {
		var err error
		expected, err = netip.ParseAddr(*clientIP)
		if err != nil || expected.IsUnspecified() || expected.IsMulticast() {
			return errors.New("verify-edge requires --expected-client-ip ADDR")
		}
	} else if *clientIP != "" {
		return errors.New("--expected-client-ip is only for verify-edge")
	}
	if *ack && cmd != "apply" {
		return errors.New("--acknowledge-no-backup is only for apply, install and upgrade")
	}
	if *adopt && cmd != "apply" {
		return errors.New("--adopt-edge-hosts is only for apply")
	}
	s, err := load(*state, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
	s.adopt = *adopt
	var publishPlan func() error
	if cmd == "apply" {
		if publishPlan, err = s.ensureInstallation(*state); err != nil {
			return err
		}
	}
	if cmd == "preflight" && len(s.order) == 0 {
		return errors.New("no installable apps selected; existing references are planning records and cannot be preflighted by this build")
	}
	if err := s.connect(ctx, trust); err != nil {
		return err
	}
	if cmd == "verify-edge" {
		if len(s.edges) == 0 {
			return errors.New("stack.yaml has no edges")
		}
		for _, e := range s.edges {
			for _, step := range s.npms[e.Target].Steps(e) {
				if err := step.Verify(ctx); err != nil {
					return fmt.Errorf("%s: %w", step.ID(), err)
				}
			}
			if err := edge.VerifyRoutes(ctx, e, expected); err != nil {
				return fmt.Errorf("edge %s: %w", e.Target, err)
			}
			fmt.Fprintf(o.Out, "%s: NPM and product forwarded-header checks passed\n", e.Target)
		}
		return nil
	}
	if err := s.preflight(ctx); err != nil || cmd == "preflight" {
		return err
	}
	s.ack = *ack
	return s.apply(ctx, publishPlan)
}

// ensureInstallation gives a plan with edges its installation_id. That is a plan change,
// refused on a runner-owned target like any other; publish saves it once apply passed.
func (s *session) ensureInstallation(state string) (func() error, error) {
	if len(s.edges) == 0 || s.installation != "" {
		return nil, nil
	}
	id := stack.NewInstallationID()
	_, data, err := changePlan(state, s.revision, func(st *stack.Stack) { st.InstallationID = id })
	if err != nil {
		return nil, err
	}
	s.installation, s.changesPlan = id, true
	revision := s.revision
	return func() error { return publish(state, revision, data) }, nil
}

type trustFlag map[string]string

func (t trustFlag) String() string { return "" }

func (t trustFlag) Set(v string) error {
	name, fp, ok := strings.Cut(v, "=")
	if !ok || !stack.ValidName(name) || !strings.HasPrefix(fp, "SHA256:") {
		return errors.New("want <target>=SHA256:<fingerprint>")
	}
	t[name] = fp
	return nil
}

type session struct {
	changesPlan  bool          // install, uninstall or unmanage; refused on a runner-owned target
	owner        *stack.Marker // set by a published handoff: the owner markers now record
	integrations []stack.Integration
	allTargets   []stack.Target
	revision     int    // of the loaded plan; markers on targets record it
	installation string // stack.yaml's installation_id; marks this installation's NPM hosts
	trust        trustFlag
	ack          bool // acknowledge upgrading without a pre-upgrade backup
	adopt        bool // claim legacy-marked NPM hosts (apply --adopt-edge-hosts)
	redact       *engine.Redactor
	demo         bool
	o            Options
	state        string
	cat          catalog.Catalog
	order        []string
	placed       map[string]stack.Target // app -> target
	targets      []stack.Target          // targets holding an app, by name
	drivers      map[string]driver       // target name -> driver
	edges        []stack.Edge
	npms         map[string]*edge.NPM
}

// load validates everything that needs no connection.
func load(state string, trust trustFlag, o Options) (*session, error) {
	st, err := stack.Load(filepath.Join(state, "stack.yaml"))
	if err != nil {
		return nil, err
	}
	return newSession(st, state, trust, o)
}

func newSession(st *stack.Stack, state string, trust trustFlag, o Options) (*session, error) {
	cat, err := catalog.Load(o.Catalog)
	if st.Demo {
		cat, err = catalog.Load(catalog.Demo())
	}
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	for name := range trust {
		if _, ok := st.Target(name); !ok {
			return nil, fmt.Errorf("--trust-host-key names unknown target %q", name)
		}
	}
	// Resolve symlinks on both sides: a link outside the state directory may point into it.
	stateAbs, err := filepath.Abs(state)
	if err == nil {
		stateAbs, err = filepath.EvalSymlinks(stateAbs)
	}
	if err != nil {
		return nil, err
	}
	clusters := map[[2]string]string{} // kubeconfig, context -> target
	for i, t := range st.Targets {
		if !t.Kubernetes() {
			continue
		}
		if _, ok := trust[t.Name]; ok {
			return nil, fmt.Errorf("--trust-host-key names %q, a Kubernetes target", t.Name)
		}
		kc := t.Kubeconfig
		if !filepath.IsAbs(kc) {
			kc = filepath.Join(stateAbs, kc)
		}
		kc, err = filepath.EvalSymlinks(kc)
		if err != nil {
			return nil, fmt.Errorf("target %q: kubeconfig %s: %w", t.Name, t.Kubeconfig, err)
		}
		if rel, err := filepath.Rel(stateAbs, kc); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("target %q: kubeconfig %s is inside the state directory, which must hold no secrets", t.Name, kc)
		}
		// Two targets on one cluster would contend for its single lock.
		if other, ok := clusters[[2]string{kc, t.Context}]; ok {
			return nil, fmt.Errorf("targets %q and %q are the same cluster; use one target per cluster", other, t.Name)
		}
		clusters[[2]string{kc, t.Context}] = t.Name
		st.Targets[i].Kubeconfig = kc
	}
	redact := o.Redact
	if redact == nil {
		redact = &engine.Redactor{}
	}
	s := &session{revision: st.Revision, installation: st.InstallationID, trust: trust, redact: redact, allTargets: st.Targets, integrations: st.Integrations, o: o, state: state, demo: st.Demo, cat: cat, placed: map[string]stack.Target{}, drivers: map[string]driver{}, edges: st.Edges, npms: map[string]*edge.NPM{}}
	var names []string
	used := map[string]bool{}
	for _, a := range st.Apps {
		t, _ := st.Target(a.Target)
		s.placed[a.Name] = t
		names = append(names, a.Name)
		if !used[t.Name] {
			used[t.Name] = true
			s.targets = append(s.targets, t)
		}
	}
	for _, e := range st.Edges {
		if !used[e.Target] {
			t, _ := st.Target(e.Target)
			used[e.Target] = true
			s.targets = append(s.targets, t)
		}
	}
	sort.Slice(s.targets, func(i, j int) bool { return s.targets[i].Name < s.targets[j].Name })
	if s.order, err = plan.Order(names, cat); err != nil {
		return nil, err
	}
	for name, t := range s.placed {
		app := cat[name]
		switch {
		case t.Kubernetes() && app.Kubernetes == nil:
			return nil, fmt.Errorf("app %q has no Kubernetes deployment; place it on a Docker host", name)
		case !t.Kubernetes() && app.Compose == nil:
			return nil, fmt.Errorf("app %q has no Compose deployment; place it on a Kubernetes target", name)
		}
	}
	for _, a := range st.Apps {
		if len(a.NodePorts) == 0 {
			continue
		}
		app := cat[a.Name]
		if !s.placed[a.Name].Kubernetes() {
			return nil, fmt.Errorf("app %q: node_ports are for a Kubernetes target", a.Name)
		}
		k := *app.Kubernetes
		k.NodePorts = maps.Clone(k.NodePorts)
		for port, node := range a.NodePorts {
			if _, ok := k.NodePorts[port]; !ok {
				return nil, fmt.Errorf("app %q: node_ports %d is not one of its published ports", a.Name, port)
			}
			k.NodePorts[port] = node
		}
		app.Kubernetes = &k
		cat[a.Name] = app
	}
	return s, nil
}

func (s *session) connect(ctx context.Context, trust trustFlag) error {
	confirm := s.confirmer()
	for _, t := range s.targets {
		d, err := dialTarget(ctx, t, remote.SSHConfig{KnownHosts: filepath.Join(s.state, "known_hosts"), Trust: trust[t.Name], Confirm: confirm, KeyFile: s.o.SSHKey}, s.needsStorage(t.Name))
		if err != nil {
			return err
		}
		if k, ok := d.(kubeDriver); ok {
			k.edge = s.edgeApp(t.Name)
			d = k
		}
		s.drivers[t.Name] = d
	}
	for _, e := range s.edges {
		token, tunnel, err := s.drivers[e.Target].edgeAccess(ctx, e)
		if err != nil {
			return fmt.Errorf("edge %s: %w", e.Target, err)
		}
		s.redact.Add(strings.TrimSpace(string(token)))
		n, err := edge.NewNPM(e.NPMURL, strings.TrimSpace(string(token)), s.installation, tunnel)
		if err != nil {
			return fmt.Errorf("edge %s: %w", e.Target, err)
		}
		n.Adopt = s.adopt
		s.npms[e.Target] = n
	}
	return nil
}

// dialTarget connects one target; c holds the SSH settings that are not the target's own.
// Tests replace it.
var dialTarget = func(ctx context.Context, t stack.Target, c remote.SSHConfig, needsStorage bool) (driver, error) {
	if t.Kubernetes() {
		c, err := kube.Connect(t.Kubeconfig, t.Context)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.Name, err)
		}
		return kubeDriver{c: c, needsStorage: needsStorage, storageClass: t.StorageClass}, nil
	}
	c.Name, c.Host, c.Port, c.User = t.Name, t.Host, t.Port, t.User
	sc, err := remote.Dial(ctx, c)
	if err != nil {
		return nil, err
	}
	return sshDriver{t: t, c: sc}, nil
}

// edgeApp names the edge component placed on target, "" if none.
func (s *session) edgeApp(target string) string {
	for name, t := range s.placed {
		if t.Name == target && s.cat[name].Category == "edge" {
			return name
		}
	}
	return ""
}

func (s *session) needsStorage(target string) bool {
	for name, t := range s.placed {
		if k := s.cat[name].Kubernetes; t.Name == target && k != nil && len(k.Volumes) > 0 {
			return true
		}
	}
	return false
}

func (s *session) confirmer() func(name, fp string) bool {
	if s.o.In == nil {
		return nil
	}
	in := bufio.NewReader(s.o.In)
	return func(name, fp string) bool {
		fmt.Fprintf(s.o.Out, "Target %s presents host key %s.\nCompare it with `ssh-keygen -lf` on the host. Trust it? [y/N] ", name, fp)
		line, _ := in.ReadString('\n')
		a := strings.ToLower(strings.TrimSpace(line))
		return a == "y" || a == "yes"
	}
}

func (s *session) close() {
	for _, d := range s.drivers {
		d.close()
	}
	for _, n := range s.npms {
		n.Close()
	}
}

func (s *session) preflight(ctx context.Context) error {
	failed := 0
	for _, t := range s.targets {
		for _, f := range s.drivers[t.Name].preflight(ctx) {
			mark := "ok  "
			if !f.OK {
				mark = "FAIL"
				failed++
			}
			fmt.Fprintf(s.o.Out, "%s  %-12s %-16s %s\n", mark, t.Name, f.Check, f.Detail)
		}
	}
	for _, e := range s.edges {
		if e.Connector != nil {
			if err := s.drivers[e.Target].checkConnector(ctx, e); err != nil {
				failed++
				fmt.Fprintf(s.o.Out, "FAIL  %-12s connector        %s\n", e.Target, err)
			}
		}
		if err := s.npms[e.Target].Preflight(ctx, e); err != nil {
			failed++
			fmt.Fprintf(s.o.Out, "FAIL  %-12s edge             %s\n", e.Target, err)
		} else {
			fmt.Fprintf(s.o.Out, "ok    %-12s edge             NPM reachable, hostname ownership checked\n", e.Target)
		}
	}
	if failed > 0 {
		return fmt.Errorf("preflight: %d check(s) failed; nothing was changed", failed)
	}
	return nil
}

// apply locks every target before the first step and releases them at the end; publish, if
// any, saves the plan once every step passed.
func (s *session) apply(ctx context.Context, publish func() error) error {
	if len(s.order) == 0 {
		return errors.New("no installable apps selected; existing references do not authorize integration or deployment")
	}
	if err := s.operate(ctx, "", s.deploySteps, publish); err != nil {
		return err
	}
	if s.demo {
		fmt.Fprintln(s.o.Out, "Demo deployment passed its driver checks. Suite SSO, administrator access and backups are not configured.")
		if t, ok := s.placed["hello"]; ok {
			if t.Kubernetes() {
				fmt.Fprintln(s.o.Out, "Demo endpoint: use kubectl -n kyq-hello port-forward deployment/app 18080:8080, then open http://127.0.0.1:18080")
			} else {
				fmt.Fprintf(s.o.Out, "Demo endpoint on %s: http://127.0.0.1:18080 (target loopback only; use an SSH tunnel from your workstation).\n", t.Name)
			}
		}
	}
	return nil
}

// operate locks s.targets, runs the steps and, after they all pass, publishes the plan. The
// markers then record the plan the targets were left in.
func (s *session) operate(ctx context.Context, holder string, steps func(context.Context) ([]engine.Step, error), publish func() error) (err error) {
	if holder == "" {
		holder = holderName()
	}
	done, err := s.lock(ctx, holder, s.targets)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, finish(ctx, done)) }()
	list, err := steps(ctx)
	if err != nil {
		return err
	}
	e := &engine.Engine{Dir: filepath.Join(s.state, "results"), Out: s.o.Out, Redact: s.redact, Progress: s.o.Progress}
	if err := e.Run(ctx, list); err != nil {
		return &suite.Error{Code: suite.StepFailed, Message: err.Error(), Err: err}
	}
	if publish == nil {
		return nil
	}
	if err := publish(); err != nil {
		return err
	}
	s.revision++
	return nil
}

// deploySteps gates every app in s.order on its upgrade rules, then returns its install
// steps and the edges'. Nothing has changed when a gate refuses.
func (s *session) deploySteps(ctx context.Context) ([]engine.Step, error) {
	var steps []engine.Step
	for _, name := range s.order {
		d := s.drivers[s.placed[name].Name]
		deployed, err := d.deployed(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		note, err := gate(name, s.cat[name], deployed, s.o.ReleaseSet, s.ack)
		if err != nil {
			return nil, err
		}
		if note != "" {
			steps = append(steps, noteStep{id: name + ".upgrade", note: note})
		}
		steps = append(steps, d.steps(name, s.cat[name], s.o.ReleaseSet, s.redact)...)
	}
	for _, edge := range s.edges {
		steps = append(steps, s.npms[edge.Target].Steps(edge)...)
		if edge.Connector != nil {
			connector, err := s.drivers[edge.Target].connectorSteps(ctx, edge, s.o.ReleaseSet, s.redact)
			if err != nil {
				return nil, fmt.Errorf("edge %s: %w", edge.Target, err)
			}
			steps = append(steps, connector...)
		}
	}
	return steps, nil
}

// lock takes every target's lock and refuses a target whose plan marker is newer than this
// plan. done writes this plan's revision to each target's marker, then releases the locks.
func (s *session) lock(ctx context.Context, holder string, targets []stack.Target) (done func(context.Context) error, err error) {
	var releases []func(context.Context) error
	unlock := func(ctx context.Context) (err error) {
		for _, release := range releases {
			err = errors.Join(err, release(ctx))
		}
		return err
	}
	markers := map[string]stack.Marker{}
	for _, t := range targets {
		release, m, err := s.lockOne(ctx, holder, t)
		if err != nil {
			return nil, errors.Join(err, finish(ctx, unlock))
		}
		releases, markers[t.Name] = append(releases, release), m
	}
	return func(ctx context.Context) (err error) {
		for _, t := range targets {
			// Keep the owner: only a published handoff moves a plan between the CLI and the runner.
			m := markers[t.Name]
			if s.owner != nil {
				m.Owner, m.RunnerID = s.owner.Owner, s.owner.RunnerID
			}
			if m.Owner == "" {
				m.Owner = "cli"
			}
			m.Revision = s.revision
			err = errors.Join(err, s.drivers[t.Name].writeMarker(ctx, m))
		}
		return errors.Join(err, unlock(ctx))
	}, nil
}

// lockOne takes one target's lock and reads its marker, releasing the lock on refusal.
func (s *session) lockOne(ctx context.Context, holder string, t stack.Target) (func(context.Context) error, stack.Marker, error) {
	d := s.drivers[t.Name]
	release, err := d.acquire(ctx, holder)
	var dl *dockerhost.LockedError
	var kl *kube.LockedError
	if errors.As(err, &dl) || errors.As(err, &kl) {
		return nil, stack.Marker{}, suite.Errorf(suite.TargetLocked, "%s: %w", t.Name, err)
	}
	if err != nil {
		return nil, stack.Marker{}, fmt.Errorf("%s: %w", t.Name, err)
	}
	m, err := d.marker(ctx)
	if err != nil {
		err = fmt.Errorf("%s: %w", t.Name, err)
	} else {
		err = s.guard(t.Name, m)
	}
	if err != nil {
		return nil, m, errors.Join(err, finish(ctx, release))
	}
	return release, m, nil
}

// guard refuses a target whose marker records a newer plan or another owner. The runner acts
// only on targets handed to it; the CLI may not change a plan the runner owns, and runs
// anything else only from a plan at least as new as every marker it locks (after pull-plan).
func (s *session) guard(target string, m stack.Marker) error {
	switch {
	case m.Revision > s.revision:
		return suite.Errorf(suite.PlanStale, "%s holds plan revision %d (owner %s), newer than this plan's %d; run kyquickstart pull-plan", target, m.Revision, m.Owner, s.revision)
	case s.o.RunnerID != "" && m.Owner != "runner":
		return suite.Errorf(suite.OwnedByCLI, "%s is managed from the kyquickstart CLI; hand it to this runner with kyquickstart handoff", target)
	case s.o.RunnerID != "" && m.RunnerID != s.o.RunnerID:
		return suite.Errorf(suite.OwnedByRunner, "%s is owned by runner %s, not this runner; rerun kyquickstart handoff to move it here", target, m.RunnerID)
	case s.o.RunnerID == "" && m.Owner == "runner" && s.changesPlan:
		return suite.Errorf(suite.OwnedByRunner, "%s is managed by the KyYard suite runner; change it from KyYard's Suite page, or run kyquickstart handoff --revoke", target)
	}
	return nil
}

// finish runs a cleanup that must happen even after ctx is cancelled.
func finish(ctx context.Context, f func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	return f(ctx)
}

func holderName() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("kyquickstart %s@%s pid %d", os.Getenv("USER"), host, os.Getpid())
}
