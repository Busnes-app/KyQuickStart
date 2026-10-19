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
	// A setup amendment that added the client is the app's own canonical UUID.
	const amendment = "5f4d3125-a512-5221-ae16-90e33394dd45"
	amended := plan + "  identity_amendment: " + amendment + "\n"
	if s, err := Parse([]byte(amended)); err != nil || s.Integrations[0].IdentityAmendment != amendment {
		t.Fatal("amended integration rejected", err)
	}
	identity := "- product: kyidentity\n  target: k8s\n  bundle: /data/installer/" + amendment + "/setup.json\n  identity:\n    schema: kyidentity.setup/1\n    setup_id: " + amendment +
		"\n    admin: {username: owner-admin, email: admin@example.test}\n    everyday: {username: owner, email: owner@example.test}\n    clients: []\n"
	for name, bad := range map[string]string{
		"not uuid":        strings.Replace(amended, "identity_amendment: "+amendment, "identity_amendment: amend", 1),
		"upper":           strings.Replace(amended, "identity_amendment: 5f4d3125", "identity_amendment: 5F4D3125", 1),
		"own setup id":    strings.Replace(amended, "identity_amendment: "+amendment, "identity_amendment: 0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c", 1),
		"kyidentity's id": strings.Replace(amended, "integrations:\n", "integrations:\n"+identity, 1),
		"repeated":        strings.Replace(amended, "- name: kyrecovery\n", "- name: kypulse\n  target: k8s\n- name: kyrecovery\n", 1) + "- product: kypulse\n  target: k8s\n  bundle: /app/data/installer/1f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c/setup.json\n  identity_client: kypulse\n  identity_amendment: " + amendment + "\n",
		"not installed":   strings.Replace(strings.Replace(amended, "- name: kyrecovery\n  target: k8s\n", "", 1), "  identity_client:", "  container: app\n  namespace: kyq-kyrecovery\n  pod: app-1\n  identity_client:", 1) + "existing:\n- product: kyrecovery\n  target: k8s\n  url: https://recovery.example.test\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), "identity_amendment") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Any other installed app takes the same shape.
	pulse := strings.ReplaceAll(plan, "kyrecovery", "kypulse")
	if _, err := Parse([]byte(pulse)); err != nil {
		t.Fatal("kyPulse integration rejected", err)
	}
}

// AC4: an installed KyNotes stages only at its fixed path, and apply derives its settings.
func TestInstalledNotesIntegration(t *testing.T) {
	plan := "version: 1\ntargets:\n- name: k8s\n  kubeconfig: /tmp/fixture-kubeconfig\napps:\n- name: kyidentity\n  target: k8s\n- name: kynotes\n  target: k8s\n" +
		"integrations:\n- product: kynotes\n  target: k8s\n  bundle: /data/installer/setup.json\n  identity_client: kynotes\n"
	if _, err := Parse([]byte(plan)); err != nil {
		t.Fatal("installed KyNotes integration rejected", err)
	}
	if _, err := Parse([]byte(plan + "  identity_amendment: 5f4d3125-a512-5221-ae16-90e33394dd45\n")); err != nil {
		t.Fatal("amended KyNotes integration rejected", err)
	}
	notes := "  notes:\n    version: 1\n    sso:\n      issuer_url: https://identity.example.test\n      client_id: kynotes\n      client_secret_file: /data/installer/client.secret\n      redirect_uri: https://notes.example.test/api/v1/auth/oidc/callback\n"
	for name, bad := range map[string]string{
		"notes block": plan + notes,
		"container":   plan + "  container: app\n",
		"pod":         plan + "  namespace: kyq-kynotes\n  pod: app-1\n",
		"no client":   strings.Replace(plan, "  identity_client: kynotes\n", "", 1),
		"uuid bundle": strings.Replace(plan, "/data/installer/setup.json", "/data/installer/0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c/setup.json", 1),
		"data bundle": strings.Replace(plan, "/data/installer/setup.json", "/data/setup.json", 1),
		"other data":  strings.Replace(plan, "/data/installer/setup.json", "/app/data/installer/setup.json", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// An installed KyCalendar takes the generic installed-app rule: its client and a UUID bundle.
func TestInstalledCalendarIntegration(t *testing.T) {
	plan := "version: 1\ntargets:\n- name: k8s\n  kubeconfig: /tmp/fixture-kubeconfig\napps:\n- name: kyidentity\n  target: k8s\n- name: kycalendar\n  target: k8s\n" +
		"integrations:\n- product: kycalendar\n  target: k8s\n  bundle: /app/data/installer/0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c/setup.json\n  identity_client: kycalendar\n"
	if _, err := Parse([]byte(plan)); err != nil {
		t.Fatal("installed KyCalendar integration rejected", err)
	}
	notes := "  notes:\n    version: 1\n    sso:\n      issuer_url: https://identity.example.test\n      client_id: kycalendar\n      client_secret_file: /app/data/installer/client.secret\n      redirect_uri: https://calendar.example.test/api/sso/kysignon/callback\n"
	for name, bad := range map[string]string{
		"notes block":  plan + notes,
		"container":    plan + "  container: app\n",
		"pod":          plan + "  namespace: kyq-kycalendar\n  pod: app-1\n",
		"no client":    strings.Replace(plan, "  identity_client: kycalendar\n", "", 1),
		"no uuid":      strings.Replace(plan, "0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c/", "", 1),
		"not a uuid":   strings.Replace(plan, "0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c", "setup", 1),
		"notes bundle": strings.Replace(plan, "/app/data/installer/0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c/setup.json", "/data/installer/setup.json", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// An installed KyPost on a Docker host takes the generic installed-app rule: its client and a
// UUID bundle under /kypost/private/installer, in its one container found per call.
func TestInstalledPostIntegration(t *testing.T) {
	plan := "version: 1\ntargets:\n- name: k8s\n  kubeconfig: /tmp/fixture-kubeconfig\n- name: nas\n  host: nas.example.test\n  user: kyq\napps:\n- name: kyidentity\n  target: k8s\n- name: kypost\n  target: nas\n" +
		"integrations:\n- product: kypost\n  target: nas\n  bundle: /kypost/private/installer/0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c/setup.json\n  identity_client: kypost\n"
	if _, err := Parse([]byte(plan)); err != nil {
		t.Fatal("installed KyPost integration rejected", err)
	}
	notes := "  notes:\n    version: 1\n    sso:\n      issuer_url: https://identity.example.test\n      client_id: kypost\n      client_secret_file: /kypost/private/installer/client.secret\n      redirect_uri: https://post.example.test/api/auth/oidc/callback\n"
	for name, bad := range map[string]string{
		"notes block": plan + notes,
		"container":   plan + "  container: app\n",
		"no client":   strings.Replace(plan, "  identity_client: kypost\n", "", 1),
		"no uuid":     strings.Replace(plan, "0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c/", "", 1),
		"not a uuid":  strings.Replace(plan, "0f9a3c1e-5b7d-4e2a-9c8b-1d2e3f4a5b6c", "setup", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
