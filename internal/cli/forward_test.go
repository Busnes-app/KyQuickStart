package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

const testSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// fakeProxyIdentity answers apply-setup --capabilities and proxy-app, recording each proxy-app call.
type fakeProxyIdentity struct {
	features []string
	reports  []string // proxy-app answers in order
	calls    [][]string
}

func (f *fakeProxyIdentity) exec(_ context.Context, args []string) ([]byte, error) {
	if args[0] == "apply-setup" {
		b, _ := json.Marshal(map[string]any{"schema": "ky.setup-capabilities/1", "service": "kyidentity", "bundleSchema": "kyidentity.setup/1", "features": f.features})
		return b, nil
	}
	f.calls = append(f.calls, args)
	if len(f.reports) == 0 {
		return nil, errors.New("product setup command failed")
	}
	r := f.reports[0]
	f.reports = f.reports[1:]
	return []byte(r), nil
}

func proxyFeatures(extra ...string) []string {
	return append([]string{"local-accounts", "confidential-oidc-clients", "assigned-only", "everyday-assignment", "app-role-assignments", "scim-connectors",
		"suite-webhook-connectors", "directory-groups", "app-group-assignments", "scim-groups", "check"}, extra...)
}

func proxyReport(status, secret string) string {
	s := `{"schema":"kyidentity.proxy-app/1","host":"grafana.example.com","id":"p1","status":"` + status + `"`
	if secret != "" {
		s += `,"edgeSecret":"` + secret + `"`
	}
	return s + "}\n"
}

func proxyStep(f *fakeProxyIdentity, existing string, redact *engine.Redactor) proxyAppStep {
	id := "0f9e3c8e-1d2b-4c5a-8e7f-6a5b4c3d2e1f"
	return proxyAppStep{installation: &id, redact: redact, exec: f.exec,
		r:        stack.Route{Hostname: "grafana.example.com", Protect: "kyidentity", LoginHeader: "X-WEBAUTH-USER", Bypass: []string{"/api/health"}, ForwardAuth: &stack.ForwardAuth{}},
		existing: func(context.Context) (string, error) { return existing, nil }}
}

// The first run takes the issued secret; a rerun reuses the edge's and never asks for a
// second; a lost one is replaced; nothing else is accepted.
func TestProxyAppStep(t *testing.T) {
	ctx := context.Background()
	t.Run("created", func(t *testing.T) {
		f, redact := &fakeProxyIdentity{features: proxyFeatures("proxy-apps"), reports: []string{proxyReport("created", testSecret)}}, &engine.Redactor{}
		p := proxyStep(f, "", redact)
		if p.Verify(ctx) == nil {
			t.Fatal("verified before reading the secret")
		}
		if err := p.Apply(ctx); err != nil || p.r.ForwardAuth.Secret != testSecret || p.Verify(ctx) != nil {
			t.Fatalf("apply: %v, secret %q", err, p.r.ForwardAuth.Secret)
		}
		want := []string{"proxy-app", "--owner", *p.installation, "--host", "grafana.example.com", "--name", "grafana.example.com", "--login-header", "X-WEBAUTH-USER", "--bypass", "/api/health"}
		if len(f.calls) != 1 || !slices.Equal(f.calls[0], want) {
			t.Fatalf("calls %v", f.calls)
		}
		if strings.Contains(redact.Redact("x "+testSecret), testSecret) {
			t.Fatal("the secret is not redacted")
		}
	})
	t.Run("rerun reuses the edge's secret", func(t *testing.T) {
		f := &fakeProxyIdentity{features: proxyFeatures("proxy-apps"), reports: []string{proxyReport("unchanged", "")}}
		p := proxyStep(f, testSecret, &engine.Redactor{})
		if err := p.Apply(ctx); err != nil || p.r.ForwardAuth.Secret != testSecret || len(f.calls) != 1 {
			t.Fatalf("apply: %v, calls %v", err, f.calls)
		}
	})
	t.Run("lost secret is replaced", func(t *testing.T) {
		other := strings.Repeat("a", 64)
		f := &fakeProxyIdentity{features: proxyFeatures("proxy-apps"), reports: []string{proxyReport("unchanged", ""), proxyReport("updated", other)}}
		p := proxyStep(f, "", &engine.Redactor{})
		if err := p.Apply(ctx); err != nil || p.r.ForwardAuth.Secret != other || len(f.calls) != 2 || !slices.Contains(f.calls[1], "--rotate-secret") {
			t.Fatalf("apply: %v, calls %v", err, f.calls)
		}
	})
	for name, c := range map[string]struct {
		features []string
		reports  []string
	}{
		"no capability":   {proxyFeatures(), nil},
		"refused":         {proxyFeatures("proxy-apps"), nil},
		"other host":      {proxyFeatures("proxy-apps"), []string{strings.Replace(proxyReport("created", testSecret), "grafana.", "evil.", 1)}},
		"short secret":    {proxyFeatures("proxy-apps"), []string{proxyReport("created", "abc")}},
		"injected secret": {proxyFeatures("proxy-apps"), []string{proxyReport("created", strings.Repeat("a", 60)+`\";\n`)}},
		"unknown status":  {proxyFeatures("proxy-apps"), []string{proxyReport("adopted", testSecret)}},
		"no replacement":  {proxyFeatures("proxy-apps"), []string{proxyReport("unchanged", ""), proxyReport("updated", "")}},
		"not json":        {proxyFeatures("proxy-apps"), []string{"edgeSecret=" + testSecret}},
		"oversize":        {proxyFeatures("proxy-apps"), []string{proxyReport("created", testSecret) + strings.Repeat(" ", 5000)}},
		"wrong schema":    {proxyFeatures("proxy-apps"), []string{strings.Replace(proxyReport("created", testSecret), "proxy-app/1", "proxy-app/2", 1)}},
	} {
		t.Run(name, func(t *testing.T) {
			p := proxyStep(&fakeProxyIdentity{features: c.features, reports: c.reports}, "", &engine.Redactor{})
			err := p.Apply(ctx)
			if err == nil || p.Verify(ctx) == nil {
				t.Fatalf("accepted: secret %q", p.r.ForwardAuth.Secret)
			}
			if strings.Contains(err.Error(), testSecret) {
				t.Fatal("the secret is in the error")
			}
		})
	}
}

// A header-login upstream this workstation reaches fails preflight; a gate-only one warns.
func TestOperatorIsolation(t *testing.T) {
	reachable := true
	old := dialUpstream
	dialUpstream = func(context.Context, string) error {
		if reachable {
			return nil
		}
		return errors.New("refused")
	}
	t.Cleanup(func() { dialUpstream = old })
	var out bytes.Buffer
	s := &session{o: Options{Out: &out}}
	e := stack.Edge{Target: "nas", Routes: []stack.Route{
		{App: "kyidentity", Hostname: "id.example.com", ForwardHost: "192.0.2.5", ForwardPort: 8080},
		{Hostname: "grafana.example.com", ForwardHost: "192.0.2.20", ForwardPort: 3000, Protect: "kyidentity", LoginHeader: "X-WEBAUTH-USER"},
		{Hostname: "sonarr.example.com", ForwardHost: "192.0.2.21", ForwardPort: 8989, Protect: "kyidentity"},
		{Hostname: "local.example.com", ForwardHost: "127.0.0.1", ForwardPort: 81, Protect: "kyidentity", LoginHeader: "Remote-User"},
	}}
	if n := s.isolation(context.Background(), e); n != 1 || !strings.Contains(out.String(), "FAIL  nas          grafana.example.com") ||
		!strings.Contains(out.String(), "warn  nas          sonarr.example.com") || !strings.Contains(out.String(), "local.example.com  upstream on loopback") {
		t.Fatalf("failed %d:\n%s", n, out.String())
	}
	reachable, out = false, bytes.Buffer{}
	if n := s.isolation(context.Background(), e); n != 0 || strings.Count(out.String(), "ok ") != 2 || !strings.Contains(out.String(), "other machines unproven") {
		t.Fatalf("failed %d:\n%s", n, out.String())
	}
}

// Each operator route gets the KyIdentity route of its own edge as its upstream; one without
// KyIdentity's identity integration is refused. An external operator route needs an external
// KyIdentity.
func TestForwardAuths(t *testing.T) {
	routes := []stack.Route{
		{App: "kyidentity", Hostname: "id.example.com", ForwardHost: "192.0.2.5", ForwardPort: 8080, ForwardScheme: "http"},
		{Hostname: "grafana.example.com", ForwardHost: "192.0.2.20", ForwardPort: 3000, Protect: "kyidentity"},
	}
	st := &stack.Stack{Apps: []stack.App{{Name: "kyidentity", Target: "nas"}}, Edges: []stack.Edge{{Target: "nas", Provider: "lan", Routes: routes}}}
	if _, err := forwardAuths(slices.Clone(st.Edges), st); err == nil || !strings.Contains(err.Error(), "identity integration") {
		t.Fatalf("no integration: %v", err)
	}
	st.Integrations = []stack.Integration{{Product: "kyidentity", Target: "nas", Identity: &stack.IdentityPreparation{}}}
	edges := routePaths(st.Edges, catalog.Catalog{})
	edges, err := forwardAuths(edges, st)
	if err != nil {
		t.Fatal(err)
	}
	if fa := edges[0].Routes[1].ForwardAuth; fa == nil || fa.Upstream != "http://192.0.2.5:8080" || edges[0].Routes[0].ForwardAuth != nil || st.Edges[0].Routes[1].ForwardAuth != nil {
		t.Fatalf("forward auth: %+v", edges[0].Routes)
	}
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	ext := &stack.Stack{Edges: []stack.Edge{{Target: "k80", Provider: "cloudflare", Routes: []stack.Route{
		{App: "kyidentity", Hostname: "id.example.com", Exposure: "internal"},
		{Hostname: "grafana.example.com", Protect: "kyidentity", Exposure: "external"},
	}}}}
	if err := exposurePlan(ext, cat); err == nil || !strings.Contains(err.Error(), "KyIdentity's route must be external") {
		t.Fatalf("external operator route behind an internal KyIdentity: %v", err)
	}
}
