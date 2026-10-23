package cli

import (
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// Administrator and suite apps stay internal; an external app that signs in needs an external
// KyIdentity.
func TestExposurePlan(t *testing.T) {
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kyrecovery", "kypulse", "kydns", "kyyard"} {
		if cat[name].Exposure != "internal" {
			t.Errorf("%s: catalog exposure %q, want internal", name, cat[name].Exposure)
		}
	}
	plan := func(routes ...stack.Route) *stack.Stack {
		return &stack.Stack{Edges: []stack.Edge{{Target: "k80", Provider: "cloudflare", Routes: routes}}}
	}
	route := func(app, exposure string) stack.Route {
		return stack.Route{App: app, Hostname: app + ".example.com", Exposure: exposure}
	}
	for name, c := range map[string]struct {
		st   *stack.Stack
		want string
	}{
		"internal admin app":        {plan(route("kypulse", "internal")), ""},
		"external admin app":        {plan(route("kypulse", "external")), "kypulse is internal only"},
		"admin app by edge default": {plan(route("kyrecovery", "")), "kyrecovery is internal only"},
		"both external":             {plan(route("kyidentity", "external"), route("kynotes", "external")), ""},
		"identity internal only":    {plan(route("kyidentity", "internal"), route("kynotes", "external")), "KyIdentity's route must be external"},
		"all internal":              {plan(route("kyidentity", "internal"), route("kynotes", "internal")), ""},
		"identity not routed here":  {plan(route("kynotes", "external")), ""},
		"lan edge":                  {&stack.Stack{Edges: []stack.Edge{{Provider: "lan", Routes: []stack.Route{route("kyidentity", ""), route("kypulse", "")}}}}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			err := exposurePlan(c.st, cat)
			if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

// Catalog routes get their app's status path; a plan's own path stays, and routes of apps
// without one, or a multi-route app's other routes, get none (verify-edge skips them).
func TestRouteStatusPaths(t *testing.T) {
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	in := []stack.Edge{{Routes: []stack.Route{
		{App: "kynotes", StatusPath: "/api/status"},
		{App: "kycalendar", StatusPath: "/custom"},
		{App: "kyidentity", StatusPath: "/api/status"},
		{App: "kymessage", StatusPath: "/api/status"},
		{App: "kymessage", Role: cat["kymessage"].Routes[1].Role, StatusPath: "/api/status"},
		{App: "hello", StatusPath: "/api/status"},
	}}}
	got := routePaths(in, cat)[0].Routes
	for i, want := range []string{"/api/v1/installer/network", "/custom", "", "/api/installer/network", "", "/api/status"} {
		if got[i].StatusPath != want {
			t.Errorf("route %d (%s %s): status path %q, want %q", i, got[i].App, got[i].Role, got[i].StatusPath, want)
		}
	}
	if in[0].Routes[0].StatusPath != "/api/status" {
		t.Error("routePaths changed the plan's routes")
	}
}
