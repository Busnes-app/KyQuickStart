package cli

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"go.yaml.in/yaml/v3"
)

// Every new runtime path is planned together, with actual per-app setup intents.
// This catches the old Kubernetes-only gates and unresolved issuer/callback metadata.
func TestDockerCatalogAllKyProducts(t *testing.T) {
	for _, edge := range []string{"192.168.1.20", "fd00::20"} {
		t.Run(edge, func(t *testing.T) {
			f := newFixture(t, "../catalog/testdata/apps")
			f.o.Catalog = catalog.Embedded()
			f.o.RunnerImage = yardRunner
			names := []string{"kyidentity", "kynotes", "kycalendar", "kyrecovery", "kypulse", "kyyard"}
			var plan strings.Builder
			plan.WriteString("apps:\n")
			for _, name := range names {
				plan.WriteString("  - name: " + name + "\n    target: nas\n")
			}
			plan.WriteString("edges:\n  - target: nas\n    provider: lan\n    npm_url: https://npm.example.com\n    allow_cidrs: [192.168.1.0/24]\n    egress_address: " + edge + "\n    routes:\n")
			for n, name := range names {
				plan.WriteString("      - app: " + name + "\n        hostname: " + name + ".example.com\n        forward_host: 192.0.2.1\n        forward_port: " + strconv.Itoa(18000+n) + "\n        certificate_id: 1\n")
			}
			plan.WriteString("integrations:\n  - product: kyidentity\n    target: nas\n    bundle: /data/installer/271333e2-c221-4601-b249-563b3d828530/setup.json\n" + identityPrep)
			for _, name := range names[1:] {
				bundle := "/data/installer/11111111-2222-4333-8444-555555555555/setup.json"
				if name == "kynotes" {
					bundle = "/data/installer/setup.json"
				}
				if name == "kycalendar" || name == "kyrecovery" || name == "kypulse" {
					bundle = "/app/data/installer/11111111-2222-4333-8444-555555555555/setup.json"
				}
				plan.WriteString("  - product: " + name + "\n    target: nas\n    bundle: " + bundle + "\n    identity_client: " + name + "\n")
			}
			f.plan(t, plan.String())
			s, err := load(f.state, nil, f.o)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.cat["kyidentity"].Kubernetes.Env[issuerEnv]; got != "https://kyidentity.example.com" {
				t.Fatal("unresolved issuer", got)
			}
			s.drivers["nas"] = sshDriver{t: s.placed["kyidentity"], c: f.h}
			s.edges = nil
			steps, err := s.deploySteps(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			for _, step := range steps {
				ids = append(ids, step.ID())
			}
			for _, name := range names {
				if !slices.Contains(ids, name+".setup") || !slices.Contains(ids, name+".deploy") {
					t.Fatal("missing setup/deploy", name, ids)
				}
				if s.cat[name].UsesRoute() {
					t.Fatal("unresolved route", name)
				}
			}
			app := s.cat["kyyard"]
			var compose map[string]any
			if err = yaml.Unmarshal(app.Compose, &compose); err != nil {
				t.Fatal(err)
			}
			services := compose["services"].(map[string]any)
			application := services["app"].(map[string]any)
			if got := application["environment"].(map[string]any)["KY_TRUSTED_PROXIES"]; got != edge {
				t.Fatal("proxy trust must name exactly the edge IP", got)
			}
			runner := services["runner"].(map[string]any)
			if runner["image"] != yardRunner || app.Images["suite_runner"] != yardRunner || !slices.Contains(app.Services, "runner") || !slices.Contains(app.Binds, "runner-state") {
				t.Fatal("runner missing", app)
			}
			if strings.Contains(string(app.Compose), "docker.sock") {
				t.Fatal("runner received host Docker socket")
			}
			if !strings.Contains(s.handover, "--runner-container kyq-kyyard-runner-1") || strings.Contains(s.handover, "kubectl") {
				t.Fatal("wrong runtime handover", s.handover)
			}
			// A runner may not mutate either container in its own app.
			s.o.RunnerID = "runner-test"
			if err = s.refuseOwnPod([]string{"kyyard"}); err == nil {
				t.Fatal("runner can redeploy itself")
			}
		})
	}
}
