package cli

import (
	"cmp"
	"context"
	"fmt"
	"net/netip"
	"path"
	"slices"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
)

// retired says why an app left the catalog, for the refusal of a plan that still names it.
var retired = map[string]string{
	"kymessage": "KyMessages left the catalog on 2026-11-25: KyMessage-Server is archived, and KyMessages-Native has no release yet",
}

// missingApps lists the plan's apps that cat lacks, in plan order.
func missingApps(st *stack.Stack, cat catalog.Catalog) []string {
	var out []string
	for _, a := range st.Apps {
		if _, ok := cat[a.Name]; !ok {
			out = append(out, a.Name)
		}
	}
	return out
}

// refuseMissing refuses a plan naming an app this catalog lacks: nothing of it is touched,
// and unmanage is the way to forget it.
func refuseMissing(st *stack.Stack, cat catalog.Catalog) error {
	gone := missingApps(st, cat)
	if len(gone) == 0 {
		return nil
	}
	name := gone[0]
	t, _ := st.Target(st.Apps[slices.IndexFunc(st.Apps, func(a stack.App) bool { return a.Name == name })].Target)
	why := cmp.Or(retired[name], name+" is not in this release's catalog")
	return suite.Errorf(suite.UnknownApp, "stack.yaml names %s, which this release cannot manage. %s. Nothing was changed: %s keeps running on %s and its routes keep serving. `kyquickstart unmanage %s` forgets it and keeps it running and reachable: its routes are handed to you as they are; nothing of it is stopped, recreated or deleted", name, why, name, t.Name, name)
}

// planCatalog is the catalog st is managed with: the demo catalog for a demo plan.
func planCatalog(st *stack.Stack, o Options) (catalog.Catalog, error) {
	fsys := o.Catalog
	if st.Demo {
		fsys = catalog.Demo()
	}
	cat, err := catalog.Load(fsys)
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	return cat, nil
}

// forget is unmanage of an app the catalog lacks. It touches nothing of the app: no
// container, Compose project, directory, volume or Kubernetes object. Its routes are released
// to the operator (edge.Release: still serving, never disabled or deleted), then the plan drops
// it. An edge whose connector configuration is rebuilt from the plan (a Cloudflare tunnel, frp
// or cloudflare-direct) would lose the route, so a route there refuses.
func forget(ctx context.Context, state string, st *stack.Stack, name string, r Request, trust trustFlag, o Options) error {
	app := st.Apps[slices.IndexFunc(st.Apps, func(a stack.App) bool { return a.Name == name })]
	t, _ := st.Target(app.Target)
	var held []stack.Edge
	var hostnames []string
	for _, e := range st.Edges {
		routes := slices.DeleteFunc(slices.Clone(e.Routes), func(r stack.Route) bool { return r.App != name })
		if len(routes) == 0 {
			continue
		}
		if e.Provider != "lan" {
			return suite.Errorf(suite.Dependency, "edge %s (%s) carries %s's route %s. Its connector's configuration is rebuilt from stack.yaml, so forgetting %s would take the route off it, and on frp the released hosts would block later applies. Nothing was changed. This release cannot forget an app with routes on a tunnel, frp or cloudflare-direct edge; manage this installation with an earlier kyquickstart release that still has %s until one can", e.Target, e.Provider, name, routes[0].Hostname, name, name)
		}
		for _, r := range routes {
			hostnames = append(hostnames, r.Hostname)
		}
		held = append(held, e)
	}
	// The rest of the plan, without every app the catalog lacks, makes the session; only name
	// leaves the published plan.
	rest := *st
	rest.Apps, rest.Edges, rest.Integrations = slices.Clone(st.Apps), cloneEdges(st.Edges), slices.Clone(st.Integrations)
	cat, err := planCatalog(st, o)
	if err != nil {
		return err
	}
	for _, gone := range missingApps(st, cat) {
		a := st.Apps[slices.IndexFunc(st.Apps, func(a stack.App) bool { return a.Name == gone })]
		withoutApp(&rest, gone, a.Target)
	}
	s, err := newSession(&rest, state, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
	next, data, err := changePlan(state, s.revision, func(st *stack.Stack) {
		recordReleased(st, name, t.Name)
		withoutApp(st, name, t.Name)
	})
	if err != nil {
		return err
	}
	targets := []stack.Target{t}
	for _, e := range held {
		if !slices.ContainsFunc(targets, func(x stack.Target) bool { return x.Name == e.Target }) {
			et, _ := st.Target(e.Target)
			targets = append(targets, et)
		}
	}
	s.targets, s.edges, s.changesPlan, s.installation = targets, held, true, next.InstallationID
	if err := s.connect(ctx, trust); err != nil {
		return err
	}
	var kubeEdges []string
	for _, e := range held {
		if et, _ := st.Target(e.Target); et.Kubernetes() {
			kubeEdges = append(kubeEdges, e.Target)
		}
	}
	handover := forgetHandover(name, t, hostnames, kubeEdges)
	note := strings.Join(strings.Fields(strings.TrimPrefix(strings.TrimSpace(handover), "## ")), " ")
	return s.operate(ctx, r.Holder, func(context.Context) ([]engine.Step, error) {
		var steps []engine.Step
		for _, e := range held {
			steps = append(steps, s.proxy(e).Release(e, name)...)
		}
		return append(steps, noteStep{id: name + ".handover", note: note}), nil
	}, func() error {
		if err := appendHandover(state, handover); err != nil {
			return err
		}
		return publish(state, s.revision, data)
	})
}

// recordReleased lists app's routes on each Kubernetes edge under its released, so the edge's
// egress policy keeps admitting exactly the upstreams off the cluster it admitted (session.
// upstreams) and the edge stays in the plan once it has no route of its own.
func recordReleased(st *stack.Stack, app, appTarget string) {
	for i := range st.Edges {
		e := &st.Edges[i]
		if et, _ := st.Target(e.Target); !et.Kubernetes() {
			continue
		}
		for _, r := range e.Routes {
			if r.App != app {
				continue
			}
			rel := stack.Released{Hostname: r.Hostname}
			if ip, err := netip.ParseAddr(r.ForwardHost); err == nil && appTarget != e.Target {
				rel.Upstream = netip.AddrPortFrom(ip, uint16(r.ForwardPort)).String()
			}
			e.Released = append(e.Released, rel)
		}
	}
}

func cloneEdges(edges []stack.Edge) []stack.Edge {
	out := slices.Clone(edges)
	for i := range out {
		out[i].Routes = slices.Clone(out[i].Routes)
	}
	return out
}

func forgetHandover(name string, t stack.Target, hostnames, kubeEdges []string) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "\n## %s on %s is no longer managed: it left the catalog\n\n", name, t.Name)
	fmt.Fprintf(b, "kyquickstart and KyYard no longer upgrade, back up, restore or remove %s. Nothing of it was stopped, recreated or deleted: its workload, data, secrets and backups stay as they were.\n", name)
	if t.Kubernetes() {
		fmt.Fprintf(b, "It runs in namespace %s; its objects keep their labels, which nothing acts on now.\n", "kyq-"+name)
	} else {
		fmt.Fprintf(b, "Its Compose project kyq-%s is in %s; its containers keep their labels, which nothing acts on now. Run it with `%s docker compose -p kyq-%s -f compose.yaml -f compose.kyq.yaml up -d` there.\n", name, path.Join(t.Root, name), dockerhost.ComposeEnv, name)
	}
	if len(hostnames) > 0 {
		fmt.Fprintf(b, "Its hostnames (%s) keep serving. Their proxy hosts and LAN DNS records no longer carry this installation's marks: they are yours, and kyquickstart never changes them again. Change or remove them in your proxy and DNS yourself.\n", strings.Join(hostnames, ", "))
	}
	if len(kubeEdges) > 0 {
		fmt.Fprintf(b, "On edge %s, stack.yaml lists them under released: each upstream off the cluster stays in the edge's egress NetworkPolicy, so the hostnames keep reaching %s. Once you have removed a hostname's proxy host or file, delete its released entry and run apply to drop that allowance.\n", strings.Join(kubeEdges, ", "), name)
	}
	return b.String()
}
