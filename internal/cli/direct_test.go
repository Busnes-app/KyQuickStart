package cli

import (
	"bufio"
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

// Each directApps port is the container port its template publishes on {{forward}}, on its
// "app" service, which declares no networks of its own (the override adds the edge's).
func TestDirectAppsPorts(t *testing.T) {
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	for name, port := range directApps {
		app, ok := cat[name]
		if !ok || len(app.Routes) > 0 || !strings.Contains(string(app.Compose), `"{{forward}}:`+strconv.Itoa(port)+`"`) || strings.Count(string(app.Compose), "{{forward}}") != 1 {
			t.Errorf("%s: not one {{forward}} route on container port %d", name, port)
			continue
		}
		var c struct {
			Services map[string]map[string]any
			Networks map[string]any
		}
		if err := yaml.Unmarshal(app.Compose, &c); err != nil || c.Services["app"] == nil || c.Services["app"]["networks"] != nil || c.Networks != nil {
			t.Errorf("%s: its app service must exist and declare no networks (%v)", name, err)
		}
	}
	for name := range directRefused {
		if _, ok := cat[name]; !ok {
			t.Errorf("directRefused names %s, not a catalog app", name)
		}
	}
}

const directPlan = "apps:\n  - name: kyidentity\n    target: nas\nedges:\n  - target: nas\n    provider: cloudflare-direct\n    connector_cidr: 172.30.0.3/32\n    connector: {network: edge-net, tunnel_id: 12345678-1234-1234-1234-123456789012}\n    routes:\n      - {app: kyidentity, hostname: id.example.com, forward_host: 127.0.0.1, forward_port: 15867}\n"

func TestDirectEdgeSession(t *testing.T) {
	f := newFixture(t, "../catalog/apps")
	f.plan(t, directPlan)
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	if r := s.edges[0].Routes[0]; r.Origin != "http://kyq-kyidentity:5867" {
		t.Fatalf("origin = %q", r.Origin)
	}
	if !strings.Contains(string(s.cat["kyidentity"].Compose), `"127.0.0.1:15867:5867"`) {
		t.Fatal("app not published on loopback only")
	}
	// No NPM: connect reads no npm_token (the fixture has none) and makes no NPM client.
	if err := s.connect(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(s.npms) != 0 || s.drivers["nas"].(sshDriver).edgeNets["kyidentity"] != "edge-net" {
		t.Fatalf("npms %v, driver %+v", s.npms, s.drivers["nas"])
	}
	steps, err := s.deploySteps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, st := range steps {
		ids = append(ids, st.ID())
	}
	if got := strings.Join(ids, " "); !strings.Contains(got, "kyidentity.deploy") || !strings.Contains(got, "nas.edge-credentials edge-") || strings.Contains(got, "npm") {
		t.Fatalf("steps = %s", got)
	}
	// Admin-only, LAN-only, path-blocked and unassessed apps are refused before connecting.
	for app, why := range map[string]string{"kyrecovery": "internet-facing", "kypulse": "internet-facing", "kyyard": "internet-facing", "kydns": "lan edge", "kymessage": "path", "kynotes": "first-run"} {
		f.plan(t, strings.Replace(directPlan, "apps:\n", "apps:\n  - name: "+app+"\n    target: nas\n", 1)+"      - {app: "+app+", hostname: app.example.com, forward_host: 127.0.0.1, forward_port: 18080}\n")
		if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%s: %v", app, err)
		}
	}
	if err := directAllowed("euro-office"); err == nil || !strings.Contains(err.Error(), "not been assessed") {
		t.Errorf("unassessed app: %v", err)
	}
}

func TestGuidedDirectEdgeRoundTrip(t *testing.T) {
	st, err := stack.Parse([]byte("version: 1\ntargets:\n  - name: nas\n    host: 192.0.2.1\n    user: kyq\napps:\n  - name: kyrecovery\n    target: nas\n  - name: kyidentity\n    target: nas\n"))
	if err != nil {
		t.Fatal(err)
	}
	answers := []string{"cloudflare-direct", "172.30.0.3/32", "edge-net", "12345678-1234-1234-1234-123456789012", "yes", "id.example.com", "15867", "/readyz"}
	var out bytes.Buffer
	p := &prompts{ctx: context.Background(), in: bufio.NewReader(strings.NewReader(strings.Join(answers, "\n") + "\n")), out: &out, answers: map[string]string{}, draft: filepath.Join(t.TempDir(), "answers.json")}
	if err := guidedEdges(p, st, nil); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), `"kyrecovery" cannot be routed`) || strings.Contains(out.String(), "NPM LAN HTTPS") {
		t.Fatalf("prompts: %s", out.String())
	}
	data, _ := yaml.Marshal(st)
	if _, err := stack.Parse(data); err != nil {
		t.Fatal(err, string(data))
	}
	first := st.Edges[0]
	if want := (stack.Route{App: "kyidentity", Hostname: "id.example.com", ForwardHost: "127.0.0.1", ForwardPort: 15867, ForwardScheme: "http", StatusPath: "/readyz"}); len(first.Routes) != 1 || !reflect.DeepEqual(first.Routes[0], want) {
		t.Fatalf("routes = %+v", first.Routes)
	}
	seedEdges(p.answers, st.Edges)
	st.Edges = nil
	p.in = bufio.NewReader(strings.NewReader(strings.Repeat("\n", len(answers))))
	if err := guidedEdges(p, st, nil); err != nil {
		t.Fatal(err, out.String())
	}
	if !reflect.DeepEqual(first, st.Edges[0]) {
		t.Fatal("editing defaults lost an edge choice")
	}
	// A Kubernetes target is not offered it.
	st, _ = stack.Parse([]byte("version: 1\ntargets:\n  - name: k1\n    kubeconfig: /dev/null\napps:\n  - name: kyidentity\n    target: k1\n"))
	p = &prompts{ctx: context.Background(), in: bufio.NewReader(strings.NewReader("cloudflare-direct\n")), out: &out, answers: map[string]string{}, draft: filepath.Join(t.TempDir(), "answers.json")}
	if err := guidedEdges(p, st, nil); err == nil {
		t.Fatal("cloudflare-direct accepted on Kubernetes")
	}
}
