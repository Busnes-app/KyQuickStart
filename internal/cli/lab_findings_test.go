package cli

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

// The catalog KyRecovery's pod runs as its service user, so review accepts its setup on a
// cluster; an existing KyRecovery there still needs Docker, and the refusal names it.
func TestRecoverySetupOnKubernetes(t *testing.T) {
	st := &stack.Stack{
		Targets:      []stack.Target{{Name: "k1", Kubeconfig: "/kc"}},
		Apps:         []stack.App{{Name: "kyrecovery", Target: "k1"}},
		Integrations: []stack.Integration{{Product: "kyrecovery", Target: "k1", Bundle: "/app/data/installer/setup.json"}},
	}
	if _, err := integrationOrder(st); err != nil {
		t.Fatalf("catalog KyRecovery on a cluster: %v", err)
	}
	st.Apps = nil
	if _, err := integrationOrder(st); err == nil || !strings.Contains(err.Error(), "kyrecovery's setup must run as its service user kyrecovery") {
		t.Fatalf("existing KyRecovery on a cluster: %v", err)
	}
}

// Without installation_id, KyMessages' refusal hands an unattended author a fresh one to add.
func TestMessagePlanSuggestsInstallationID(t *testing.T) {
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	st := &stack.Stack{
		Targets:      []stack.Target{{Name: "nas", Host: "192.0.2.1"}},
		Apps:         []stack.App{{Name: "kymessage", Target: "nas"}},
		Integrations: []stack.Integration{{Product: "kymessage", Target: "nas", ChatClient: "kymessage-" + cat["kymessage"].ChatOIDC.ClientSuffix}},
	}
	err = messagePlan(st, cat)
	m := regexp.MustCompile(`add the line installation_id: (\S+) to stack.yaml`).FindStringSubmatch(errString(err))
	if m == nil {
		t.Fatalf("no installation_id to add: %v", err)
	}
	if st.InstallationID = m[1]; messagePlan(st, cat) != nil {
		t.Fatalf("suggested installation_id %q refused: %v", m[1], messagePlan(st, cat))
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// The handover says where KyIdentity's setup left the two owner passwords.
func TestHandoverNamesOwnerPasswords(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	catalogIdentityPlan(t, f, "kyidentity", "")
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"KyIdentity at https://identity.example.com/: the owner's accounts are owner-admin (administration only) and owner",
		"-n kyq-kyidentity exec deployment/app -- cat /data/installer/271333e2-c221-4601-b249-563b3d828530/admin.password",
		"-n kyq-kyidentity exec deployment/app -- cat /data/installer/271333e2-c221-4601-b249-563b3d828530/everyday.password",
		"MFA: enrol TOTP, a passkey or KyAuth push approval. Push reaches phones through\n  the relays fcm.relay.kyauth.busnes.app",
	} {
		if !strings.Contains(s.handover, want) {
			t.Errorf("handover lacks %q:\n%s", want, s.handover)
		}
	}
}

// Uninstalling an app takes its integration entry with it, so the saved plan stays valid.
func TestUninstallDropsIntegration(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	recoveryPlan(t, f, "      clients: []\n")
	st, err := stack.Load(f.state + "/stack.yaml")
	if err != nil {
		t.Fatal(err)
	}
	withoutApp(st, "kyrecovery", "k1")
	if slices.ContainsFunc(st.Integrations, func(i stack.Integration) bool { return i.Product == "kyrecovery" }) || len(st.Integrations) != 1 {
		t.Fatalf("integrations = %+v", st.Integrations)
	}
	// KyRecovery was the tunnel edge's one internal route, so its allow-list goes too.
	if e := st.Edges[0]; e.AllowCIDRs != nil || e.ClientPort != 0 {
		t.Fatalf("edge kept its allow-list: %+v", e)
	}
	data, _ := yaml.Marshal(st)
	if _, err := stack.Parse(data); err != nil {
		t.Fatalf("plan after uninstall: %v", err)
	}
}
