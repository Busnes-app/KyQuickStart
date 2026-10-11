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
