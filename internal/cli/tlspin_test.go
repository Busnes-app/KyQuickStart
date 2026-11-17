package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// KyDrive and KyPost pin what a probe of their URL reaches only where Cloudflare alone
// terminates the TLS clients see; every edge that serves its own certificate leaves it off.
func TestTLSPinProbe(t *testing.T) {
	route := func(exposure string) stack.Route {
		return stack.Route{App: "kydrive", Hostname: "drive.example.com", Exposure: exposure}
	}
	for name, c := range map[string]struct {
		edge stack.Edge
		want bool
	}{
		"cloudflare-direct":             {stack.Edge{Provider: "cloudflare-direct", Routes: []stack.Route{route("")}}, true},
		"cloudflare external":           {stack.Edge{Provider: "cloudflare", Routes: []stack.Route{route("")}}, true},
		"cloudflare external, nginx":    {stack.Edge{Provider: "cloudflare", Proxy: "nginx", Routes: []stack.Route{route("external")}}, true},
		"cloudflare internal":           {stack.Edge{Provider: "cloudflare", Routes: []stack.Route{route("internal")}}, false},
		"cloudflare with lan_dns":       {stack.Edge{Provider: "cloudflare", LANDNS: &stack.LANDNS{}, Routes: []stack.Route{route("")}}, false},
		"frp":                           {stack.Edge{Provider: "frp", Routes: []stack.Route{route("")}}, false},
		"lan":                           {stack.Edge{Provider: "lan", Routes: []stack.Route{route("")}}, false},
		"another app behind cloudflare": {stack.Edge{Provider: "cloudflare", Routes: []stack.Route{{App: "kypost", Hostname: "post.example.com"}}}, false},
	} {
		if got := tlsPinProbe(&stack.Stack{Edges: []stack.Edge{c.edge}}, "kydrive"); got != c.want {
			t.Errorf("%s: probe = %v, want %v", name, got, c.want)
		}
	}
	if tlsPinProbe(&stack.Stack{}, "kydrive") {
		t.Error("an app with no route probes")
	}
}

// The plan's edge reaches the rendered Compose file: behind the catalog NPM the probe is off and
// the copied chain is named; behind cloudflare-direct the probe is on and no chain is named.
// Neither names a private key, and a second load renders the same bytes, so a rerun
// redeploys nothing.
func TestTLSPinProbeRendered(t *testing.T) {
	f := newFixture(t, "../catalog/apps")
	drivePlan(t, f, "192.0.2.1")
	lan := renderedDrive(t, f)
	if !strings.Contains(lan, `KYDRIVE_TLS_PIN_PROBE: "false"`) || !strings.Contains(lan, `KYDRIVE_TLS_CERT_FILE: "`+catalog.TLSCertFile+`"`) {
		t.Errorf("lan edge: probe not off:\n%s", lan)
	}
	if again := renderedDrive(t, f); again != lan {
		t.Error("a second load rendered KyDrive differently")
	}

	f.plan(t, strings.Replace(directPlan, "apps:\n", "apps:\n  - name: kydrive\n    target: nas\n", 1)+
		"      - {app: kydrive, hostname: drive.example.com, forward_host: 127.0.0.1, forward_port: 18443}\n")
	direct := renderedDrive(t, f)
	if !strings.Contains(direct, `KYDRIVE_TLS_PIN_PROBE: "true"`) || !strings.Contains(direct, `KYDRIVE_TLS_CERT_FILE: ""`) {
		t.Errorf("cloudflare-direct: probe not on:\n%s", direct)
	}

	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{lan, direct} {
		if !strings.Contains(c, `volumes: ["./data:/data", "./backups:/backups", "./tls:/run/kyq-tls:ro"]`) {
			t.Error("KyDrive's mounts changed")
		}
		for _, bad := range []string{"TLS_KEY_FILE", "privkey", ".key", catalog.TLSPinProbeRef, catalog.TLSCertFileRef} {
			if strings.Contains(c, bad) {
				t.Errorf("rendered KyDrive contains %q", bad)
			}
		}
	}
	if !bytes.Contains(cat["kydrive"].Compose, []byte(catalog.TLSPinProbeRef)) {
		t.Error("the catalog's KyDrive no longer takes the probe from the plan")
	}
}

func renderedDrive(t *testing.T, f *fixture) string {
	t.Helper()
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	return string(s.cat["kydrive"].Compose)
}
