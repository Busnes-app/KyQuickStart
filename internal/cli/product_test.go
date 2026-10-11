package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

const driveCaps = `{"schema":"ky.setup-capabilities/1","service":"kydrive","bundleSchema":"ky.product-setup/1","features":["kyidentity-oidc","scim","dedicated-admin","check","restart"]}`

func driveReport(pending string) string {
	return fmt.Sprintf(`{"schema":"ky.product-setup-report/1","service":"kydrive","setupId":"271333e2-c221-4601-b249-563b3d828530","created":[],"present":[{"kind":"identity","name":"owner-admin","id":"usr_example","token":"never-print"}],"pending":%s,"secret":"never-print"}`, pending)
}

func TestProductAdapterActivatesAndVerifiesRuntime(t *testing.T) {
	var calls []string
	out, err := driveSetup(context.Background(), func(_ context.Context, args []string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch len(calls) {
		case 1:
			return []byte(driveCaps), nil
		case 2:
			return []byte(driveReport(`[{"kind":"activation","name":"restart"}]`)), nil
		case 3:
			return nil, nil
		case 4:
			return []byte(driveReport(`[]`)), nil
		}
		t.Fatal("unexpected extra command")
		return nil, nil
	}, "/app/data/setup.json", false)
	if err != nil || len(calls) != 4 || calls[2] != "apply-setup --restart" || !strings.HasSuffix(calls[3], "--check") || strings.Contains(string(out), "never-print") || !strings.Contains(string(out), `"pending":[]`) {
		t.Fatal(calls, string(out), err)
	}
}

func TestProductAdapterRefusesPendingInspectionAndMissingCapability(t *testing.T) {
	for _, caps := range []string{driveCaps, strings.Replace(driveCaps, `"dedicated-admin",`, "", 1)} {
		calls := 0
		_, err := driveSetup(context.Background(), func(_ context.Context, args []string) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte(caps), nil
			}
			if !strings.HasSuffix(strings.Join(args, " "), "--check") {
				t.Fatal("read-only inspection mutated")
			}
			return []byte(driveReport(`[{"kind":"activation","name":"restart"}]`)), nil
		}, "/data/setup.json", true)
		if err == nil {
			t.Fatal("incomplete setup accepted")
		}
		if caps != driveCaps && calls != 1 {
			t.Fatal("unsupported product invoked")
		}
	}
	for _, bad := range []string{strings.Replace(driveReport(`[]`), `"created":[]`, `"created":[{"kind":"identity","name":"new"}]`, 1), strings.Replace(driveReport(`[]`), "owner-admin", "owner\\u001b[31m", 1)} {
		if _, err := readProductReport([]byte(bad), "kydrive", true); err == nil {
			t.Fatal("unsafe report accepted")
		}
	}
}

func TestPulseAdapterRequiresMonitoringEnrollmentCapabilities(t *testing.T) {
	caps := `{"schema":"ky.setup-capabilities/1","service":"kypulse","bundleSchema":"ky.product-setup/1","features":["kyidentity-oidc","directory-webhook","dedicated-admin","monitoring-targets","log-sources","offboarding-alerts","check","restart"]}`
	for _, missing := range []string{"", "monitoring-targets", "log-sources", "offboarding-alerts"} {
		calls := 0
		fixture := caps
		if missing != "" {
			fixture = strings.Replace(caps, `"`+missing+`",`, "", 1)
		}
		out, err := pulseSetup(context.Background(), func(_ context.Context, _ []string) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte(fixture), nil
			}
			return []byte(`{"schema":"ky.product-setup-report/1","service":"kypulse","setupId":"271333e2-c221-4601-b249-563b3d828530","created":[],"pending":[],"present":[{"kind":"configuration","name":"monitoring.target.identity","id":"target"},{"kind":"configuration","name":"monitoring.source.offboard","id":"source","token":"private"}]}`), nil
		}, "/data/installer/bundle.json", true)
		if missing == "" {
			if err != nil || calls != 2 || strings.Contains(string(out), "private") {
				t.Fatal("enrollment inspection failed", err)
			}
		} else if err == nil || calls != 1 {
			t.Fatal("missing enrollment capability reached setup", missing, calls, err)
		}
	}
}

func TestProductTargetRefusesKubernetesWhenARuntimeUserIsRequired(t *testing.T) {
	kube := stack.Target{Kubeconfig: "/home/user/.kube/config"}
	for _, product := range []string{"kypost", "kyrecovery", "immich"} {
		if validateProductTarget(product, kube) == nil {
			t.Fatal("Kubernetes accepted for", product)
		}
		if validateProductTarget(product, stack.Target{}) != nil {
			t.Fatal("Docker refused for", product)
		}
	}
	if validateProductTarget("kynotes", kube) != nil {
		t.Fatal("Kubernetes refused for a product without a runtime user")
	}
}
