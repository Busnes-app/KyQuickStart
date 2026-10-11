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
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/plan"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// ErrUsage means the arguments name no command; main prints Usage.
var ErrUsage = errors.New("usage")

const Usage = `usage: kyquickstart <command> [flags]

commands:
  plan        guided setup; save or edit a secret-free installation plan
  review      display and validate the saved plan without connecting
  preflight   check every target; changes nothing
  apply       preflight, then install the apps in stack.yaml
  integrate-identity configure a selected existing KyIdentity container from a protected bundle
  integrate-product  configure a selected product through its container-local setup adapter
  integrate-stack    run explicitly selected product integrations saved in stack.yaml
  verify-edge check NPM and product forwarded headers through public HTTPS (read-only)
  version     print the release set

flags for preflight, apply and verify-edge:
  --state DIR                          state directory holding stack.yaml (default .)
  --trust-host-key NAME=SHA256:<fp>    accept this unknown host key for target NAME
  --expected-client-ip ADDR           required for verify-edge: expected public client address`

type Options struct {
	Catalog    fs.FS
	ReleaseSet string
	In         io.Reader // answers host key prompts; nil when not interactive
	Out        io.Writer
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
	s, err := load(*state, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
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
	return s.apply(ctx)
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
	integrations []stack.Integration
	allTargets   []stack.Target
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
	s := &session{allTargets: st.Targets, integrations: st.Integrations, o: o, state: state, demo: st.Demo, cat: cat, placed: map[string]stack.Target{}, drivers: map[string]driver{}, edges: st.Edges, npms: map[string]*edge.NPM{}}
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
	return s, nil
}

func (s *session) connect(ctx context.Context, trust trustFlag) error {
	confirm := s.confirmer()
	for _, t := range s.targets {
		if t.Kubernetes() {
			c, err := kube.Connect(t.Kubeconfig, t.Context)
			if err != nil {
				return fmt.Errorf("%s: %w", t.Name, err)
			}
			s.drivers[t.Name] = kubeDriver{c: c, needsStorage: s.needsStorage(t.Name)}
			continue
		}
		c, err := remote.Dial(ctx, remote.SSHConfig{
			Name: t.Name, Host: t.Host, Port: t.Port, User: t.User,
			KnownHosts: filepath.Join(s.state, "known_hosts"),
			Trust:      trust[t.Name],
			Confirm:    confirm,
		})
		if err != nil {
			return err
		}
		s.drivers[t.Name] = sshDriver{t: t, c: c}
	}
	for _, e := range s.edges {
		token, err := s.drivers[e.Target].edgeToken(ctx)
		if err != nil {
			return fmt.Errorf("edge %s: %w", e.Target, err)
		}
		n, err := edge.NewNPM(e.NPMURL, strings.TrimSpace(string(token)))
		if err != nil {
			return fmt.Errorf("edge %s: %w", e.Target, err)
		}
		s.npms[e.Target] = n
	}
	return nil
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

// apply locks every target before the first step and releases them at the end.
func (s *session) apply(ctx context.Context) (err error) {
	if len(s.order) == 0 {
		return errors.New("no installable apps selected; existing references do not authorize integration or deployment")
	}
	holder := holderName()
	var releases []func(context.Context) error
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		for _, release := range releases {
			err = errors.Join(err, release(rctx))
		}
	}()
	for _, t := range s.targets {
		release, err := s.drivers[t.Name].acquire(ctx, holder)
		if err != nil {
			return fmt.Errorf("%s: %w", t.Name, err)
		}
		releases = append(releases, release)
	}
	redact := &engine.Redactor{}
	var steps []engine.Step
	for _, name := range s.order {
		t := s.placed[name]
		steps = append(steps, s.drivers[t.Name].steps(name, s.cat[name], s.o.ReleaseSet, redact)...)
	}
	for _, edge := range s.edges {
		steps = append(steps, s.npms[edge.Target].Steps(edge)...)
		if edge.Connector != nil {
			connector, err := s.drivers[edge.Target].connectorSteps(ctx, edge, s.o.ReleaseSet, redact)
			if err != nil {
				return fmt.Errorf("edge %s: %w", edge.Target, err)
			}
			steps = append(steps, connector...)
		}
	}
	e := &engine.Engine{Dir: filepath.Join(s.state, "results"), Out: s.o.Out, Redact: redact}
	if err := e.Run(ctx, steps); err != nil {
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

func holderName() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("kyquickstart %s@%s pid %d", os.Getenv("USER"), host, os.Getpid())
}
