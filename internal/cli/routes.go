package cli

import (
	"fmt"
	"slices"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// withRoutes fills a multi-route app's role references from its plan routes, after checking
// they cover its catalog roles exactly once each, on one edge and one forward_host, with one
// forward_port per published service port. The primary route has no role.
func withRoutes(st *stack.Stack, name string, t stack.Target, app catalog.App) (catalog.App, error) {
	var edge *stack.Edge
	byRole := map[string]stack.Route{}
	for i, e := range st.Edges {
		for _, r := range e.Routes {
			if r.App != name {
				continue
			}
			if r.Role == app.Primary() {
				return app, fmt.Errorf("app %q: route %s has role %q; remove the role: the %s is the route with no role", name, r.Hostname, r.Role, r.Role)
			}
			role := r.Role
			if role == "" {
				role = app.Primary()
			}
			if _, dup := byRole[role]; dup || !slices.ContainsFunc(app.Routes, func(c catalog.RouteRole) bool { return c.Role == role }) {
				return app, fmt.Errorf("app %q: route %s: role %q must be one of its roles, once", name, r.Hostname, r.Role)
			}
			if edge != nil && edge.Target != e.Target {
				return app, fmt.Errorf("app %q: all its routes go through one edge", name)
			}
			edge, byRole[role] = &st.Edges[i], r
		}
	}
	ports, byKey := map[int]string{}, map[string]string{} // forward_port -> service:port -> role
	var c catalog.Route
	for _, cr := range app.Routes {
		r, ok := byRole[cr.Role]
		switch {
		case !ok && cr.Role == app.Primary():
			return app, fmt.Errorf("app %q needs a route with no role: its %s (service %s, port %d)", name, cr.Role, cr.Service, cr.Port)
		case !ok:
			return app, fmt.Errorf("app %q needs a route with role %s (service %s, port %d)", name, cr.Role, cr.Service, cr.Port)
		case !stack.MediaAddress(r.ForwardHost):
			return app, fmt.Errorf("app %q: route %s: forward_host %s must be one LAN IPv4 address of %s that the edge reaches, not a wildcard, loopback, link-local, multicast, IPv6 address or a name: Docker publishes every routed port on it", name, r.Hostname, r.ForwardHost, t.Name)
		}
		key := fmt.Sprint(cr.Service, ":", cr.Port)
		if other, used := ports[r.ForwardPort]; used && other != key {
			return app, fmt.Errorf("app %q: forward_port %d is given to two services; each published service port needs its own", name, r.ForwardPort)
		}
		if role, seen := byKey[key]; seen && byRole[role].ForwardPort != r.ForwardPort {
			return app, fmt.Errorf("app %q: routes %s and %s are the same service port (%s) and need one forward_port: set both to %d", name, byRole[role].Hostname, r.Hostname, key, byRole[role].ForwardPort)
		}
		ports[r.ForwardPort], byKey[key] = key, cr.Role
		if r.ForwardHost != byRole[app.Primary()].ForwardHost {
			return app, fmt.Errorf("app %q: its routes forward to one forward_host, an address of %s", name, t.Name)
		}
		forward := r.ForwardHost + ":" + fmt.Sprint(r.ForwardPort)
		if cr.Role == app.Primary() {
			c = catalog.Route{Host: edge.URLHost(r), Hostname: r.Hostname, Forward: forward, ForwardAddr: r.ForwardHost, EdgeAddress: edge.TrustedAddress(), Roles: map[string]catalog.RoleAddress{}}
			continue
		}
		c.Roles[cr.Role] = catalog.RoleAddress{Host: edge.URLHost(r), Hostname: r.Hostname, Forward: forward}
	}
	if app.UsesEdgeAddress() && edge.TrustedAddress() == "" {
		return app, fmt.Errorf("app %q trusts exactly the address its edge connects from: set egress_address on edge %s to the edge's own address as %s sees it (for NPM on a cluster, the IP of the node running NPM)", name, edge.Target, t.Name)
	}
	for _, sa := range st.Apps {
		if sa.Name == name {
			c.MediaAddress = sa.MediaAddress
		}
	}
	return app.WithRoute(c), nil
}

// routePaths copies each route of a multi-route app with its catalog paths and deny_paths, and
// gives a catalog app's routes the catalog's status path: its primary route keeps a status
// path the plan set, and takes the catalog's instead of the default; any other route, or an
// app without one, gets none, which verify-edge skips.
const defaultStatusPath = "/api/status" // stack's default for a route without status_path

func routePaths(edges []stack.Edge, cat catalog.Catalog) []stack.Edge {
	out := slices.Clone(edges)
	for i := range out {
		out[i].Routes = slices.Clone(out[i].Routes)
		for j, r := range out[i].Routes {
			app := cat[r.App]
			role := r.Role
			if role == "" {
				role = app.Primary()
			}
			if len(app.Routes) == 0 {
				out[i].Routes[j].DenyPaths = app.DenyPaths
			}
			if _, ok := cat[r.App]; ok {
				switch {
				case role != app.Primary() || app.StatusPath == "":
					out[i].Routes[j].StatusPath = ""
				case r.StatusPath == "" || r.StatusPath == defaultStatusPath:
					out[i].Routes[j].StatusPath = app.StatusPath
				}
			}
			for _, cr := range app.Routes {
				if cr.Role == role {
					out[i].Routes[j].Paths, out[i].Routes[j].DenyPaths = cr.Paths, cr.DenyPaths
				}
			}
		}
	}
	return out
}
