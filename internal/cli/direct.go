package cli

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// directApps may sit behind a cloudflare-direct edge, which has no NPM: no path blocks, no
// allow-list, and the route is public. Each is reached on its Compose service "app" at this
// port inside the container; TestDirectAppsPorts checks them against the templates.
var directApps = map[string]int{"kyidentity": 5867, "kycalendar": 8080, "kypost": 5866, "kydrive": 8080}

// directRefused says why an assessed app may not; any other app is refused as unassessed.
var directRefused = map[string]string{
	"kymessage":  "its routes rely on NPM's path allow-lists and admin-path blocks",
	"kydns":      "its admin API belongs on the LAN; use a lan edge",
	"kyrecovery": "its admin UI and custodian ceremony would be internet-facing",
	"kypulse":    "its admin-only monitoring console would be internet-facing",
	"kyyard":     "it manages the whole suite; its admin UI would be internet-facing",
	"kynotes":    "its first-run setup must finish before its route opens, and one cloudflared opens every route at once",
}

// directRoute refuses app name on a cloudflare-direct edge unless it is in directApps.
func directRoute(st *stack.Stack, name string) error {
	for _, e := range st.Edges {
		if e.Direct() && slices.ContainsFunc(e.Routes, func(r stack.Route) bool { return r.App == name }) {
			return directAllowed(name)
		}
	}
	return nil
}

func directAllowed(name string) error {
	if _, ok := directApps[name]; ok {
		return nil
	}
	why, ok := directRefused[name]
	if !ok {
		why = "it has not been assessed for exposure without NPM"
	}
	return fmt.Errorf("app %q cannot be routed by a cloudflare-direct edge: %s", name, why)
}

// directOrigins sets each cloudflare-direct route's origin: its app's alias and container
// port on the edge network, plain HTTP that never leaves the host.
func directOrigins(edges []stack.Edge) []stack.Edge {
	for i, e := range edges {
		if !e.Direct() {
			continue
		}
		edges[i].Routes = slices.Clone(e.Routes)
		for j, r := range edges[i].Routes {
			edges[i].Routes[j].Origin = "http://" + dockerhost.EdgeAlias(r.App) + ":" + strconv.Itoa(directApps[r.App])
		}
	}
	return edges
}

// edgeNetworks maps each app on target behind a cloudflare-direct edge to that edge's network.
func edgeNetworks(st *stack.Stack, target string) map[string]string {
	out := map[string]string{}
	for _, e := range st.Edges {
		if e.Direct() && e.Target == target {
			for _, r := range e.Routes {
				out[r.App] = e.Connector.Network
			}
		}
	}
	return out
}
