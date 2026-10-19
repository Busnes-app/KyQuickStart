package cli

import (
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
)

var messageApp = everydayApp{"kymessage", "KyMessages", "message.example.com", "8d2f4a6c-1e3b-4c5d-9f7a-0b1c2d3e4f5a", "/api/sso/kyidentity"}

// AC5 and the handover: one apply installing KyIdentity and KyMessages puts KyMessages'
// client in the first setup.
func TestKyMessageClientFromCatalog(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	s := derivedClient(t, f, messageApp)
	kubectl := "kubectl --kubeconfig '" + s.placed["kymessage"].Kubeconfig + "' -n kyq-kymessage "
	for _, want := range []string{"KyMessages at https://message.example.com/", "as owner-admin (its console only) or owner.",
		`Chat (Matrix) is not installed by this release: members see "chat is not available".`, `local "admin" break-glass`,
		kubectl + "logs deployment/app --previous", kubectl + "exec -it deployment/app -- /app/kymessages init-admin", "pair it with KyRecovery"} {
		if !strings.Contains(s.handover, want) {
			t.Errorf("handover lacks %q: %s", want, s.handover)
		}
	}
	handWrittenClient(t, f, messageApp)
}

// AC7: kymessage.setup takes the everyday path KyCalendar's does.
func TestKyMessageSetup(t *testing.T) { everydaySetup(t, messageApp) }

// AC6: KyMessages joining late comes in a setup amendment, with no SCIM step (its setup refuses
// SCIM; the step list is exact).
func TestKyMessageAddedByAmendment(t *testing.T) { everydayAmendment(t, messageApp) }

// AC9: its setup follows its health and precedes every edge step.
func TestKyMessageSetupBeforeEdges(t *testing.T) { setupBeforeEdges(t, messageApp) }

// AC8: owner usernames its setup would refuse are refused before any connection.
func TestKyMessageUsernamesRefused(t *testing.T) { usernamesRefused(t, messageApp) }

// AC10: KyMessages on a Docker host is refused before any connection, naming Kubernetes.
func TestKyMessageOnDockerRefused(t *testing.T) { dockerRefused(t, "kymessage") }
