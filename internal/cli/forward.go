package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// Forward-auth for operator routes (docs/edge.md, "Operator routes"): KyIdentity registers
// each route's host as a proxy app, and the edge asks it about every request.

// forwardAuths gives each operator route (in edges, which routePaths has cloned) the KyIdentity upstream its edge reaches: the
// KyIdentity route on the same edge, which stack validation requires. The secret is filled
// by the route's registration step.
func forwardAuths(edges []stack.Edge, st *stack.Stack) ([]stack.Edge, error) {
	for i, e := range edges {
		k := slices.IndexFunc(e.Routes, func(r stack.Route) bool { return r.App == "kyidentity" && r.Role == "" })
		for j, r := range e.Routes {
			if !r.Operator() {
				continue
			}
			if !slices.ContainsFunc(st.Integrations, func(x stack.Integration) bool {
				return x.Product == "kyidentity" && x.Identity != nil && x.Installed(st)
			}) {
				return nil, fmt.Errorf("route %s: protect: kyidentity needs KyIdentity's identity integration, which registers the route as a proxy app", r.Hostname)
			}
			id := e.Routes[k]
			edges[i].Routes[j].ForwardAuth = &stack.ForwardAuth{Upstream: id.ForwardScheme + "://" + net.JoinHostPort(id.ForwardHost, strconv.Itoa(id.ForwardPort))}
		}
	}
	return edges, nil
}

// proxyAppSteps register e's operator routes in KyIdentity, before the edge renders them.
func (s *session) proxyAppSteps(e stack.Edge) ([]engine.Step, error) {
	var steps []engine.Step
	for _, r := range e.Routes {
		if !r.Operator() {
			continue
		}
		k := slices.IndexFunc(s.integrations, func(i stack.Integration) bool { return i.Product == "kyidentity" && i.Identity != nil })
		d := s.drivers[s.placed["kyidentity"].Name]
		if k < 0 || d == nil {
			return nil, fmt.Errorf("route %s: registering it needs KyIdentity's target connected", r.Hostname)
		}
		exec, _, err := nativeProductExec(d, s.integrations[k], productAdapters["kyidentity"])
		if err != nil {
			return nil, err
		}
		npm, route := s.npms[e.Target], r
		existing := func(ctx context.Context) (string, error) { return npm.ForwardSecret(ctx, e, route) }
		steps = append(steps, proxyAppStep{installation: &s.installation, redact: s.redact, r: r, exec: exec, existing: existing})
	}
	return steps, nil
}

// proxyAppStep runs `kyidentity proxy-app` for one operator route and keeps the edge secret
// in memory for the route's host step. It runs on every apply: the secret is never saved,
// so a rerun reads it back from the edge, and only a lost one is replaced.
type proxyAppStep struct {
	installation *string // set by ensureInstallation before steps run
	redact       *engine.Redactor
	r            stack.Route // r.ForwardAuth is shared with the host step
	exec         identityExec
	existing     func(context.Context) (string, error) // the secret in the edge's current route
}

func (p proxyAppStep) ID() string { return "kyidentity.proxy-app." + p.r.Hostname }
func (p proxyAppStep) InputHash() string {
	return strings.Join(append([]string{*p.installation, p.r.Hostname, p.r.LoginHeader}, p.r.Bypass...), "\n")
}
func (p proxyAppStep) Inspect(context.Context) (bool, error) { return false, nil }

// Verify passes only once Apply has read the secret in this run.
func (p proxyAppStep) Verify(context.Context) error {
	if p.r.ForwardAuth.Secret == "" {
		return errors.New("edge secret not read in this run")
	}
	return nil
}

func (p proxyAppStep) Note() string {
	note := "assign its users and groups in KyIdentity (App connections); it admits assigned users only and requires MFA"
	if p.r.LoginHeader != "" {
		note += "; the app trusts " + p.r.LoginHeader + ", so it must be reachable only through the edge"
	}
	return note
}

func (p proxyAppStep) Apply(ctx context.Context) error {
	var missing missingCapability
	if err := identityCapabilities(ctx, p.exec, false, "proxy-apps"); errors.As(err, &missing) {
		return fmt.Errorf("route %s: KyIdentity has no proxy apps; upgrade KyIdentity to a release with forward-auth", p.r.Hostname)
	} else if err != nil {
		return err
	}
	secret, err := p.register(ctx, false)
	if err != nil || secret != "" {
		p.r.ForwardAuth.Secret = secret
		return err
	}
	if secret, err = p.existing(ctx); err != nil {
		return err
	}
	if secret == "" { // the edge lost it: only a replacement can restore the route
		if secret, err = p.register(ctx, true); err == nil && secret == "" {
			err = fmt.Errorf("route %s: KyIdentity did not issue a replacement edge secret", p.r.Hostname)
		}
	}
	p.r.ForwardAuth.Secret = secret
	return err
}

var hexSecret = regexp.MustCompile(`^[0-9a-f]{64}$`)

// register runs proxy-app and returns the edge secret it issued, if any. The secret is
// registered with the redactor before anything else reads it.
func (p proxyAppStep) register(ctx context.Context, rotate bool) (string, error) {
	args := []string{"proxy-app", "--owner", *p.installation, "--host", p.r.Hostname, "--name", p.r.Hostname,
		"--login-header", cmp.Or(p.r.LoginHeader, "X-Ky-User")}
	for _, b := range p.r.Bypass {
		args = append(args, "--bypass", b)
	}
	if rotate {
		args = append(args, "--rotate-secret")
	}
	b, err := p.exec(ctx, args)
	if err != nil {
		return "", fmt.Errorf("route %s: KyIdentity refused the proxy app (a host it did not register for this installation, or a field it rejects); see KyIdentity's audit log", p.r.Hostname)
	}
	var out struct {
		Schema, Host, ID, Status string
		EdgeSecret               string `json:"edgeSecret"`
	}
	if len(b) > 4096 || json.Unmarshal(b, &out) != nil {
		return "", fmt.Errorf("route %s: invalid KyIdentity proxy-app report", p.r.Hostname)
	}
	p.redact.Add(out.EdgeSecret)
	if out.Schema != "kyidentity.proxy-app/1" || out.Host != p.r.Hostname || !slices.Contains([]string{"created", "updated", "unchanged"}, out.Status) ||
		out.EdgeSecret != "" && !hexSecret.MatchString(out.EdgeSecret) {
		return "", fmt.Errorf("route %s: invalid KyIdentity proxy-app report", p.r.Hostname)
	}
	return out.EdgeSecret, nil
}

// dialUpstream opens a TCP connection to addr; tests replace it.
var dialUpstream = func(ctx context.Context, addr string) error {
	c, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", addr)
	if err == nil {
		c.Close()
	}
	return err
}

// isolation checks, from the workstation, that each header-login operator route's upstream
// is not reachable around the edge, and counts the failures. It proves only that this
// workstation cannot reach it; every other machine is unproven (docs/edge.md).
func (s *session) isolation(ctx context.Context, e stack.Edge) (failed int) {
	for _, r := range e.Routes {
		if !r.Operator() {
			continue
		}
		addr := net.JoinHostPort(r.ForwardHost, strconv.Itoa(r.ForwardPort))
		ip, ipErr := netip.ParseAddr(r.ForwardHost)
		switch {
		case ipErr == nil && ip.IsLoopback():
			fmt.Fprintf(s.o.Out, "note  %-12s %s  upstream on loopback: whether it is reachable around the edge is unproven\n", e.Target, r.Hostname)
		case dialUpstream(ctx, addr) != nil:
			fmt.Fprintf(s.o.Out, "ok    %-12s %s  upstream not reachable from this workstation; other machines unproven\n", e.Target, r.Hostname)
		case r.LoginHeader != "":
			failed++
			fmt.Fprintf(s.o.Out, "FAIL  %-12s %s  this workstation reaches %s without the edge, so anyone here could send %s and sign in as anyone; bind or firewall the app to the edge only\n", e.Target, r.Hostname, addr, r.LoginHeader)
		default:
			fmt.Fprintf(s.o.Out, "warn  %-12s %s  this workstation reaches %s without the edge, around KyIdentity's gate\n", e.Target, r.Hostname, addr)
		}
	}
	return failed
}
