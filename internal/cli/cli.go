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
	"slices"
	"sort"
	"strconv"
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
              [--take-over-target T] claim target T from the plan that last changed it
  install APP --target T   add APP and its missing dependencies to the plan and deploy them
  uninstall APP            remove APP's workloads; its data and secrets stay on the target
  unmanage APP             hand APP to its owner: drop the managed labels, record it in handover.md
  upgrade [APP...]         redeploy installed apps (default all) under their upgrade rules
  stop APP                 stop APP (Kubernetes: scale to zero) and record it in stack.yaml;
                           apply, upgrade, restart and backup then refuse until start
  start APP                start APP and wait until it is healthy; clears the stop
  restart APP              restart APP and wait until it is healthy
  logs APP [--tail N] [--service S]
                           print APP's recent log lines (default 200; S: a Compose service or
                           pod container). Installer-generated secrets are redacted; anything
                           else the product logs is printed as it is
  status [--json]          per installed app: version, target, state, live workload, last results
  backup APP [--state DIR]  snapshot a Docker app to its target's rest-server (restic)
  restore APP [--state DIR] [--snapshot ID [--replace] [--yes]]
                           list APP's snapshots, or restore one; a lost password comes on stdin
                           (its own line, then the typed app name, or the password alone with --yes)
  handoff --kyyard-target T [--runner-container NAME] [--revoke]
                           hand the plan and target access to the suite runner on T, or take them back
  pull-plan --kyyard-target T [--runner-container NAME]
                           copy the runner's plan here (stack.yaml.bak keeps the previous one)
  integrate-identity configure a selected existing KyIdentity container from a protected bundle
  integrate-product  configure a selected product through its container-local setup adapter
  integrate-stack    run explicitly selected product integrations saved in stack.yaml
  verify-edge check NPM and product forwarded headers through public HTTPS (read-only)
  version [--runner-image]  print the release set, or the suite runner image this build pins

flags for preflight, apply, verify-edge, backup, restore and the app commands:
  --state DIR                          state directory holding stack.yaml (default .)
  --trust-host-key NAME=SHA256:<fp>    accept this unknown host key for target NAME
  --expected-client-ip ADDR           required for verify-edge: this workstation's address as the edge sees it
  --acknowledge-no-backup             apply, install, upgrade: upgrade without a pre-upgrade backup`

type Options struct {
	Catalog    fs.FS
	ReleaseSet string
	In         io.Reader // answers host key prompts; nil when not interactive
	// Stdin is restore's password and typed confirmation, a terminal or not; nil for none.
	Stdin    io.Reader
	Out      io.Writer
	Progress func(engine.Event) // optional: every step's start and end
	Redact   *engine.Redactor   // optional: shared with the caller, which redacts its own output
	// The suite runner sets these: its ID, which it requires in every target's plan marker,
	// and its SSH key file in place of the agent.
	RunnerID string
	SSHKey   string
	// RunnerImage is the digest-pinned suite runner image the catalog KyYard runs beside it.
	// Without one the workstation refuses to deploy KyYard.
	RunnerImage string
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
	case "install", "uninstall", "unmanage", "upgrade", "status", "stop", "start", "restart", "logs":
		return suiteCommand(ctx, cmd, args[1:], o)
	case "handoff", "pull-plan":
		return handoffCommand(ctx, cmd, args[1:], o)
	case "backup", "restore":
		return backupCommand(ctx, cmd, args[1:], o)
	case "version":
		return versionCommand(args[1:], o)
	case "preflight", "apply", "verify-edge":
	default:
		return ErrUsage
	}
	fl := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fl.SetOutput(o.Out)
	state := fl.String("state", ".", "state directory holding stack.yaml")
	clientIP := fl.String("expected-client-ip", "", "verify-edge: this workstation's address as the edge sees it")
	ack := fl.Bool("acknowledge-no-backup", false, "apply: upgrade data without a pre-upgrade backup")
	adopt := fl.Bool("adopt-edge-hosts", false, "apply: claim and re-mark this target's NPM hosts from builds without installation_id")
	takeOver := fl.String("take-over-target", "", "apply: claim this target from the plan that last changed it")
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
	if (*adopt || *takeOver != "") && cmd != "apply" {
		return errors.New("--adopt-edge-hosts and --take-over-target are only for apply")
	}
	s, err := load(*state, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
	if *takeOver != "" && !slices.ContainsFunc(s.targets, func(t stack.Target) bool { return t.Name == *takeOver }) {
		return suite.Errorf(suite.UnknownTarget, "--take-over-target %q is not a target this plan deploys to", *takeOver)
	}
	s.adopt, s.takeOver, s.deploys = *adopt, *takeOver, cmd == "apply"
	if cmd != "verify-edge" {
		if err := s.runnerImage(); err != nil {
			return err
		}
	}
	var publishPlan func() error
	if cmd == "apply" {
		if err := s.refuseStopped(); err != nil {
			return err
		}
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
	if cmd == "apply" {
		if publishPlan, err = s.amendLate(ctx, *state, publishPlan); err != nil {
			return err
		}
	}
	if err := s.preflight(ctx); err != nil || cmd == "preflight" {
		return err
	}
	s.ack = *ack
	return s.apply(ctx, publishPlan)
}

// ensureInstallation gives a plan its installation_id. That is a plan change, refused on a
// runner-owned target like any other; publish saves it once apply passed.
func (s *session) ensureInstallation(state string) (func() error, error) {
	if s.installation != "" {
		return nil, nil
	}
	next, data, err := changePlan(state, s.revision, func(*stack.Stack) {})
	if err != nil {
		return nil, err
	}
	s.installation, s.changesPlan = next.InstallationID, true
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
	deploys      bool          // apply or upgrade: redeploys every app in order
	owner        *stack.Marker // set by a published handoff: the owner markers now record
	integrations []stack.Integration
	allTargets   []stack.Target
	revision     int    // of the loaded plan; markers on targets record it
	installation string // stack.yaml's installation_id; marks this installation's NPM hosts
	saved        string // the published plan's installation_id, which markers record
	takeOver     string // a target to claim from another plan (apply --take-over-target)
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

	plan          *stack.Stack        // as loaded, or with the amendments amendLate records
	derived       []string            // KyIdentity clients composeClients took from the catalog
	amendments    []identityAmendment // setup amendments adding the clients of later apps
	bootstrapNote string              // printed after apply while KyIdentity keeps its bootstrap admin
	pulseTargets  []pulseTarget       // kyPulse's targets, when the plan installs it
	handover      string              // printed after apply: what installed apps leave to a human
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
	s := &session{plan: st, revision: st.Revision, installation: st.InstallationID, saved: st.InstallationID, trust: trust, redact: redact, allTargets: st.Targets, integrations: st.Integrations, o: o, state: state, demo: st.Demo, cat: cat, placed: map[string]stack.Target{}, drivers: map[string]driver{}, edges: st.Edges, npms: map[string]*edge.NPM{}}
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
	if err := yardPlan(st); err != nil {
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
	for name, t := range s.placed {
		if cat[name], err = withRoute(st, name, t, cat[name]); err != nil {
			return nil, err
		}
		if !t.Kubernetes() && hostsRunner(cat[name]) && catalog.Pinned(o.RunnerImage) {
			cat[name], err = cat[name].WithDockerRunner(o.RunnerImage)
			if err != nil {
				return nil, err
			}
		}
	}
	// A cluster edge's egress policy admits an upstream off the cluster by address only.
	for _, e := range st.Edges {
		for _, r := range e.Routes {
			if t, _ := st.Target(e.Target); t.Kubernetes() && s.placed[r.App].Name != e.Target {
				if ip, err := netip.ParseAddr(r.ForwardHost); err != nil || ip.Zone() != "" {
					return nil, fmt.Errorf("edge %s: route %s forwards off the cluster, so its forward_host must be an IP address", e.Target, r.Hostname)
				}
			}
		}
	}
	for _, i := range st.Integrations {
		supported := i.Product == "kyidentity" && i.Identity != nil || cat[i.Product].OIDC != nil && productAdapters[i.Product].setup != nil
		if i.Installed(st) && !supported {
			return nil, fmt.Errorf("integration %s: apply configures only an installed KyIdentity (from its identity settings) and the installed Ky apps that sign in through it", i.Product)
		}
	}
	if err := routedNeedsSetup(st); err != nil {
		return nil, err
	}
	if err := calendarAccounts(st); err != nil {
		return nil, err
	}
	if err := messagePlan(st, cat); err != nil {
		return nil, err
	}
	s.edges = routePaths(st.Edges, cat)
	if err := s.composeClients(st); err != nil {
		return nil, err
	}
	return s, nil
}

// withRoute fills app's route references from its one edge route: the URL host, and on Docker
// the address it publishes on and its edge's egress_address. It needs no connection.
func withRoute(st *stack.Stack, name string, t stack.Target, app catalog.App) (catalog.App, error) {
	for _, a := range st.Apps {
		if a.Name != name {
			continue
		}
		switch {
		case app.Media != nil && a.MediaAddress == "":
			return app, fmt.Errorf("app %q publishes call media (%s) straight to clients: set media_address to one LAN IPv4 address of %s", name, strings.Join(app.Media.Ports, ", "), t.Name)
		case app.Media == nil && a.MediaAddress != "":
			return app, fmt.Errorf("app %q publishes no media: remove its media_address", name)
		}
	}
	if len(app.Routes) > 0 && !t.Kubernetes() {
		return withRoutes(st, name, t, app)
	}
	for _, e := range st.Edges {
		for _, r := range e.Routes {
			if r.App == name && r.Role != "" {
				return app, fmt.Errorf("app %q has one route: remove role %q from route %s", name, r.Role, r.Hostname)
			}
		}
	}
	if t.Kubernetes() && !app.Kubernetes.UsesHost() || !t.Kubernetes() && !app.UsesRoute() {
		return app, nil
	}
	var hosts []string
	var routes []stack.Route
	var edges []stack.Edge
	for _, e := range st.Edges {
		for _, r := range e.Routes {
			if r.App == name {
				hosts, routes, edges = append(hosts, e.URLHost(r)), append(routes, r), append(edges, e)
			}
		}
	}
	if len(hosts) != 1 {
		return app, fmt.Errorf("app %q takes its URL from its route hostname; give it exactly one edge route", name)
	}
	if t.Kubernetes() {
		app.Kubernetes = app.Kubernetes.WithHost(hosts[0])
		return app, nil
	}
	// Docker publishes the port on exactly the address the edge forwards to.
	ip, err := netip.ParseAddr(routes[0].ForwardHost)
	if err != nil || ip.IsUnspecified() || ip.Zone() != "" {
		return app, fmt.Errorf("app %q publishes its port on its route's forward_host, which must be an IP address of %s", name, t.Name)
	}
	if app.UsesEdgeAddress() && edges[0].EgressAddress == "" {
		return app, fmt.Errorf("app %q trusts exactly the address its edge connects from: set egress_address on edge %s to the edge's own address as %s sees it (for NPM on a cluster, the IP of the node running NPM)", name, edges[0].Target, t.Name)
	}
	addr := ip.String()
	if strings.Contains(addr, ":") { // not reached while stack refuses an IPv6 forward_host
		addr = "[" + addr + "]" // Docker's port syntax
	}
	if app.Kubernetes != nil {
		app.Kubernetes = app.Kubernetes.WithHost(hosts[0])
	}
	return app.WithRoute(catalog.Route{Host: hosts[0], Forward: addr + ":" + strconv.Itoa(routes[0].ForwardPort), ForwardAddr: addr, EdgeAddress: edges[0].EgressAddress}), nil
}

func (s *session) connect(ctx context.Context, trust trustFlag) error {
	confirm := s.confirmer()
	for _, t := range s.targets {
		d, err := dialTarget(ctx, t, remote.SSHConfig{KnownHosts: filepath.Join(s.state, "known_hosts"), Trust: trust[t.Name], Confirm: confirm, KeyFile: s.o.SSHKey}, s.needsStorage(t.Name))
		if err != nil {
			return err
		}
		if k, ok := d.(kubeDriver); ok {
			k.edge, k.nodePorts, k.upstreams, k.runnerImage = s.edgeApp(t.Name), s.nodePorts(t.Name), s.upstreams(t.Name), s.o.RunnerImage
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
		n.Adopt, n.Secrets, n.Redact = s.adopt, s.drivers[e.Target].edgeSecrets(), s.redact
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

// upstreams are the addresses the edge on target forwards to on other targets, sorted.
// newSession has checked each is an IP.
func (s *session) upstreams(target string) []netip.AddrPort {
	var out []netip.AddrPort
	for _, e := range s.edges {
		for _, r := range e.Routes {
			if e.Target == target && s.placed[r.App].Name != target {
				out = append(out, netip.AddrPortFrom(netip.MustParseAddr(r.ForwardHost), uint16(r.ForwardPort)))
			}
		}
	}
	slices.SortFunc(out, func(a, b netip.AddrPort) int { return a.Compare(b) })
	return slices.Compact(out)
}

// nodePorts maps each NodePort an app on target publishes, node_ports applied, to the app.
func (s *session) nodePorts(target string) map[int]string {
	out := map[int]string{}
	for name, t := range s.placed {
		if k := s.cat[name].Kubernetes; t.Name == target && k != nil {
			for _, node := range k.NodePorts {
				out[node] = name
			}
		}
	}
	return out
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
		findings := s.drivers[t.Name].preflight(ctx)
		for _, a := range s.plan.Apps {
			if host, ok := s.drivers[t.Name].(sshDriver); ok && a.Target == t.Name && a.MediaAddress != "" {
				findings = append(findings, finding(dockerhost.AddressCheck(ctx, host.c, a.Name, a.MediaAddress)))
			}
		}
		for _, f := range findings {
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
	fmt.Fprint(s.o.Out, s.handover+s.bootstrapNote)
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
	s.revision, s.saved = s.revision+1, s.installation
	return nil
}

// routedFirst are the apps whose setup runs after the edges route to KyIdentity and before
// their own route: KyNotes' setup probes the issuer through its route, and its first-run web
// setup stays open until that setup creates its administrator.
var routedFirst = []string{"kynotes"}

// deploySteps refuses a plan that moves a completed KyIdentity setup's URLs and gates every
// app in s.order on its upgrade rules, then returns its install steps and the edges'.
// Nothing has changed when either refuses.
func (s *session) deploySteps(ctx context.Context) ([]engine.Step, error) {
	if err := s.refuseMovedSetup(ctx); err != nil {
		return nil, err
	}
	if err := s.refuseMovedServerName(ctx); err != nil {
		return nil, err
	}
	var steps, held, connectors []engine.Step
	for _, name := range s.order {
		if s.o.RunnerID != "" && hostsRunner(s.cat[name]) {
			// Neither read nor written: redeploying it would stop this runner mid-operation.
			steps = append(steps, noteStep{id: name + ".runner", note: runnerSelfNote})
			continue
		}
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
		app := d.steps(name, s.cat[name], s.o.ReleaseSet, s.redact)
		for _, i := range s.integrations {
			if i.Product == name && i.Target == s.placed[name].Name && i.ChatClient != "" {
				before, setup, err := s.messageSteps(d, i)
				if err != nil {
					return nil, err
				}
				// Before its deploy: Compose refuses MAS without the configuration they write.
				k := slices.IndexFunc(app, func(st engine.Step) bool { return st.ID() == name+".deploy" })
				app = append(append(append(append([]engine.Step{}, app[:k]...), before...), app[k:]...), setup)
				connectors = append(connectors, directoryStep{app: name, sync: func(ctx context.Context) (string, string) { return s.resync(ctx, name) }})
			}
		}
		steps = append(steps, app...)
		for _, i := range s.integrations {
			if i.Product == name && i.Target == s.placed[name].Name && i.ChatClient == "" {
				setup, connector, err := s.setupSteps(d, i)
				if err != nil {
					return nil, err
				}
				if slices.Contains(routedFirst, name) {
					held = append(held, setup...)
				} else {
					steps = append(steps, setup...)
				}
				if connector != nil {
					connectors = append(connectors, connector)
				}
			}
		}
	}
	var routes []engine.Step // the routes of routedFirst apps, after their setup
	for _, e := range s.edges {
		for _, st := range s.npms[e.Target].Steps(e) {
			if slices.Contains(routedFirst, edge.RouteApp(st)) {
				routes = append(routes, st)
			} else {
				steps = append(steps, st)
			}
		}
		if e.Connector != nil {
			connector, err := s.drivers[e.Target].connectorSteps(ctx, e, s.o.ReleaseSet, s.redact)
			if err != nil {
				return nil, fmt.Errorf("edge %s: %w", e.Target, err)
			}
			steps = append(steps, connector...)
		}
	}
	steps = append(append(steps, held...), routes...)
	// Connectors last: their first delivery goes through the routes the edges just made.
	return append(steps, connectors...), nil
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
			// An unpublished id stays off markers, so a failed run's rerun is not another plan.
			if s.saved != "" {
				m.InstallationID = s.saved
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

// guard refuses a target whose marker records another installation, a newer plan or another
// owner. The runner acts only on targets handed to it; the CLI may not change a plan the
// runner owns, and runs anything else only from a plan at least as new as every marker it
// locks (after pull-plan). A marker without an installation_id predates them.
func (s *session) guard(target string, m stack.Marker) error {
	if target == s.takeOver && m.InstallationID != s.installation {
		if m.Owner == "runner" {
			return suite.Errorf(suite.OwnedByRunner, "%s is managed by the KyYard suite runner; run kyquickstart handoff --revoke from the plan that owns it first", target)
		}
		previous := m.InstallationID
		if previous == "" {
			previous = "with no installation id"
		}
		fmt.Fprintf(s.o.Out, "warning: taking %s over from installation %s (plan revision %d)\n", target, previous, m.Revision)
		return nil
	}
	switch {
	case m.InstallationID != "" && m.InstallationID != s.installation:
		return suite.Errorf(suite.OwnedByOtherPlan, "target %s is managed by another plan (installation %s); run apply --take-over-target %s from this plan to claim it", target, m.InstallationID, target)
	case m.Revision > s.revision:
		return suite.Errorf(suite.PlanStale, "%s holds plan revision %d (owner %s), newer than this plan's %d; run kyquickstart pull-plan", target, m.Revision, m.Owner, s.revision)
	case s.o.RunnerID != "" && m.Owner != "runner":
		return suite.Errorf(suite.OwnedByCLI, "%s is managed from the kyquickstart CLI; hand it to this runner with kyquickstart handoff", target)
	case s.o.RunnerID != "" && m.RunnerID != s.o.RunnerID:
		return suite.Errorf(suite.OwnedByRunner, "%s is owned by runner %s, not this runner; rerun kyquickstart handoff to move it here", target, m.RunnerID)
	case s.o.RunnerID == "" && m.Owner == "runner" && s.changesPlan:
		return suite.Errorf(suite.OwnedByRunner, "%s is managed by the KyYard suite runner; change it from KyYard's Suite page, or run kyquickstart handoff --revoke", target)
	case s.o.RunnerID == "" && m.Owner == "runner" && s.deploys && slices.ContainsFunc(s.order, func(a string) bool { return hostsRunner(s.cat[a]) }):
		return suite.Errorf(suite.OwnedByRunner, "%s is managed by the suite runner in KyYard's pod, which this command could replace mid-operation; run kyquickstart handoff --revoke, then this command, then handoff again", target)
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

func versionCommand(args []string, o Options) error {
	fl := flag.NewFlagSet("version", flag.ContinueOnError)
	fl.SetOutput(o.Out)
	pin := fl.Bool("runner-image", false, "print the suite runner image this build installs KyYard with")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if fl.NArg() > 0 {
		return ErrUsage
	}
	if !*pin {
		_, err := fmt.Fprintln(o.Out, o.ReleaseSet)
		return err
	}
	if o.RunnerImage == "" {
		return errors.New("this build pins no suite runner image (source build); use a release binary")
	}
	_, err := fmt.Fprintln(o.Out, o.RunnerImage)
	return err
}
