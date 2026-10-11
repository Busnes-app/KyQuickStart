package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

const immichCaps = `{"schema":"ky.setup-capabilities/1","service":"immich","bundleSchema":"immich.setup/1","nativeVersion":"3.2.0","moduleContract":"ky.immich-setup/1","features":["native-oidc","owned-initial-accounts","immutable-receipts","check"]}`

func immichReport(check bool) string {
	return fmt.Sprintf(`{"schema":"immich.setup-report/1","setupId":"271333e2-c221-4601-b249-563b3d828530","check":%t,"created":[],"planned":[],"accounts":[{"id":"271333e2-c221-4601-b249-563b3d828531","subject":"private-owner-subject","administrator":true},{"id":"271333e2-c221-4601-b249-563b3d828532","subject":"private-everyday-subject","administrator":false}],"secret":"must-not-render"}`, check)
}

func TestImmichInstallerCapabilityAndOwnership(t *testing.T) {
	for _, mode := range []string{"apply", "check", "missing-capability", "foreign-version", "foreign-installation", "foreign-account", "foreign-subject", "missing-role", "mixed-role", "planned", "mutated-check"} {
		t.Run(mode, func(t *testing.T) {
			check := mode == "check" || mode == "mutated-check"
			calls := 0
			out, err := immichSetup(context.Background(), func(_ context.Context, args []string) ([]byte, error) {
				calls++
				if calls == 1 {
					if strings.Join(args, " ") != "apply-setup --capabilities" {
						t.Fatal("capability query mutated")
					}
					caps := immichCaps
					if mode == "missing-capability" {
						caps = strings.Replace(caps, `"immutable-receipts",`, "", 1)
					}
					if mode == "foreign-version" {
						caps = strings.Replace(caps, "3.2.0", "3.3.0", 1)
					}
					return []byte(caps), nil
				}
				inspect := args[len(args)-1] == "--check"
				if calls == 2 && inspect != check || calls == 3 && !inspect {
					t.Fatal("wrong native apply/inspect mode")
				}
				r := immichReport(inspect)
				if calls == 3 {
					switch mode {
					case "foreign-installation":
						r = strings.Replace(r, "271333e2-c221-4601-b249-563b3d828530", "371333e2-c221-4601-b249-563b3d828530", 1)
					case "foreign-account":
						r = strings.Replace(r, "271333e2-c221-4601-b249-563b3d828531", "371333e2-c221-4601-b249-563b3d828531", 1)
					case "foreign-subject":
						r = strings.Replace(r, "private-owner-subject", "foreign-subject", 1)
					}
				}
				switch mode {
				case "missing-role":
					r = strings.Replace(r, `,"administrator":false`, "", 1)
				case "mixed-role":
					r = strings.Replace(r, `"administrator":false`, `"administrator":true`, 1)
				case "planned":
					r = strings.Replace(r, `"planned":[]`, `"planned":["provider"]`, 1)
				case "mutated-check":
					r = strings.Replace(r, `"created":[]`, `"created":["provider","administrator","everyday"]`, 1)
				}
				return []byte(r), nil
			}, "/protected/setup.json", check)
			if mode == "apply" || mode == "check" {
				if err != nil || strings.Contains(string(out), "private-") || strings.Contains(string(out), "must-not-render") || !strings.Contains(string(out), `"service":"immich"`) {
					t.Fatal("verified report failed or leaked native fields", err)
				}
			} else if err == nil || len(out) != 0 {
				t.Fatal("incomplete/foreign native setup accepted")
			}
			if (mode == "missing-capability" || mode == "foreign-version") && calls != 1 {
				t.Fatal("unsupported module reached a mutation")
			}
		})
	}
}

func TestImmichDockerRestrictionBeforeBatchMutation(t *testing.T) {
	st := &stack.Stack{Targets: []stack.Target{{Name: "nas", Host: "nas.example.test"}, {Name: "cluster", Kubeconfig: "/outside/config"}},
		Integrations: []stack.Integration{{Product: "kyidentity", Target: "nas"}, {Product: "immich", Target: "cluster"}}}
	if _, err := integrationOrder(st); err == nil || !strings.Contains(err.Error(), "Docker target") {
		t.Fatal("later unsupported target did not reject whole batch", err)
	}
	st.Integrations[1].Target = "nas"
	if _, err := integrationOrder(st); err != nil {
		t.Fatal("Docker module integration unavailable", err)
	}
}
