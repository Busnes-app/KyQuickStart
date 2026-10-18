package stack

import (
	"strings"
	"testing"
)

const integrationPlan = `version: 1
targets:
- name: nas
  host: nas.example.test
  user: kyq
existing:
- product: kypulse
  target: nas
  url: https://pulse.example.test
integrations:
- product: kypulse
  target: nas
  container: pulse
  bundle: /data/installer/bundle.json
`

func TestIntegrationBoundary(t *testing.T) {
	s, err := Parse([]byte(integrationPlan))
	if err != nil || len(s.Integrations) != 1 {
		t.Fatal("valid explicit setup rejected", err)
	}
	for name, edit := range map[string]func(string) string{
		"unselected": func(s string) string {
			return strings.Replace(s, "- product: kypulse\n  target: nas\n  url:", "- product: kyidentity\n  target: nas\n  url:", 1)
		},
		"duplicate": func(s string) string {
			return s + "- product: kypulse\n  target: nas\n  container: pulse2\n  bundle: /data/setup.json\n"
		},
		"relative bundle": func(s string) string {
			return strings.Replace(s, "bundle: /data/installer/bundle.json", "bundle: setup.json", 1)
		},
		"unclean bundle": func(s string) string {
			return strings.Replace(s, "bundle: /data/installer/bundle.json", "bundle: /data/../setup.json", 1)
		},
		"invalid container": func(s string) string { return strings.Replace(s, "container: pulse", "container: -pulse", 1) },
		"docker namespace":  func(s string) string { return s + "  namespace: pulse\n" },
		"inline secret":     func(s string) string { return s + "  token: forbidden-credential\n" },
		"wrong placement": func(s string) string {
			s = strings.Replace(s, "existing:\n", "- name: other\n  host: other.example.test\n  user: kyq\nexisting:\n", 1)
			return strings.Replace(s, "  target: nas\n  container: pulse", "  target: other\n  container: pulse", 1)
		},
		"shared container": func(s string) string {
			s = strings.Replace(s, "integrations:\n", "- product: kyidentity\n  target: nas\n  url: https://identity.example.test\nintegrations:\n", 1)
			return s + "- product: kyidentity\n  target: nas\n  container: pulse\n  bundle: /data/identity.json\n"
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(edit(integrationPlan))); err == nil {
				t.Fatal("invalid integration accepted")
			}
		})
	}
	kube := strings.Replace(integrationPlan, "  host: nas.example.test\n  user: kyq", "  kubeconfig: /tmp/fixture-kubeconfig", 1) + "  namespace: kyq-pulse\n  pod: pulse-123\n"
	if _, err := Parse([]byte(kube)); err != nil {
		t.Fatal("valid Kubernetes integration rejected", err)
	}
	for _, bad := range []string{strings.Replace(kube, "namespace: kyq-pulse", "namespace: kyq.pulse", 1), strings.Replace(kube, "container: pulse", "container: pulse.one", 1), strings.Replace(kube, "  pod: pulse-123\n", "", 1)} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatal("invalid Kubernetes integration accepted")
		}
	}
}

// An installed app's integration names no workload: apply finds the app's own pod.
func TestInstalledIntegrationNamesNoWorkload(t *testing.T) {
	plan := "version: 1\ntargets:\n- name: k8s\n  kubeconfig: /tmp/fixture-kubeconfig\napps:\n- name: kyidentity\n  target: k8s\n- name: kypulse\n  target: k8s\n" +
		"integrations:\n- product: kyidentity\n  target: k8s\n  bundle: /data/a.json\n- product: kypulse\n  target: k8s\n  bundle: /data/installer/0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c/setup.json\n  identity_client: kypulse\n"
	s, err := Parse([]byte(plan))
	if err != nil {
		t.Fatal("installed integrations rejected", err)
	}
	if !s.Integrations[0].Installed(s) || (Integration{Product: "kyidentity", Target: "other"}).Installed(s) {
		t.Fatal("Installed misreports placement")
	}
	for _, field := range []string{"container: app", "namespace: kyq-kyidentity", "pod: app-1"} {
		bad := strings.Replace(plan, "  bundle: /data/a.json\n", "  bundle: /data/a.json\n  "+field+"\n", 1)
		if _, err := Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), "names no container") {
			t.Errorf("%s accepted: %v", field, err)
		}
	}
}

// An installed app's setup names its KyIdentity client and a UUID-scoped bundle.
func TestRecoveryIntegration(t *testing.T) {
	const bundle = "/app/data/installer/0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c/setup.json"
	plan := "version: 1\ntargets:\n- name: k8s\n  kubeconfig: /tmp/fixture-kubeconfig\napps:\n- name: kyidentity\n  target: k8s\n- name: kyrecovery\n  target: k8s\n" +
		"integrations:\n- product: kyrecovery\n  target: k8s\n  bundle: " + bundle + "\n  identity_client: kyrecovery\n"
	s, err := Parse([]byte(plan))
	if err != nil {
		t.Fatal("KyRecovery integration rejected", err)
	}
	if s.Integrations[0].SetupID() != "0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c" {
		t.Fatal("setup id not read from the bundle path")
	}
	for name, bad := range map[string]string{
		"no client":      strings.Replace(plan, "  identity_client: kyrecovery\n", "", 1),
		"bad client":     strings.Replace(plan, "identity_client: kyrecovery", "identity_client: -x", 1),
		"no uuid":        strings.Replace(plan, "0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c", "setup", 1),
		"upper uuid":     strings.Replace(plan, "0f9a3c1e", "0F9A3C1E", 1),
		"not installer":  strings.Replace(plan, "/installer/", "/other/", 1),
		"other filename": strings.Replace(plan, "/setup.json", "/bundle.json", 1),
		"not installed":  strings.Replace(plan, "- name: kyrecovery\n  target: k8s\n", "", 1) + "existing:\n- product: kyrecovery\n  target: k8s\n  url: https://recovery.example.test\n",
		"kyidentity":     strings.ReplaceAll(plan, "product: kyrecovery", "product: kyidentity"),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Any other installed app takes the same shape.
	pulse := strings.ReplaceAll(plan, "kyrecovery", "kypulse")
	if _, err := Parse([]byte(pulse)); err != nil {
		t.Fatal("kyPulse integration rejected", err)
	}
}
