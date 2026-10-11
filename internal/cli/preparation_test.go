package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

func TestIdentityPreparationNativeCapabilityAndReadback(t *testing.T) {
	n := &stack.IdentityPreparation{Schema: "kyidentity.setup/1", SetupID: "271333e2-c221-4601-b249-563b3d828530", Admin: stack.IdentityAccount{Username: "owner-admin", Email: "admin@example.test"}, Everyday: stack.IdentityAccount{Username: "owner", Email: "owner@example.test"}}
	for _, mode := range []string{"supported", "unsupported", "mutating-check", "invalid-receipt"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			exec := func(context.Context, []string) ([]byte, error) {
				caps := `{"schema":"ky.setup-capabilities/1","service":"kyidentity","bundleSchema":"kyidentity.setup/1","features":["local-accounts","confidential-oidc-clients","assigned-only","everyday-assignment","app-role-assignments","scim-connectors","suite-webhook-connectors","directory-groups","app-group-assignments","scim-groups","check","protected-setup-files","bundle-preparation","target-credentials"]}`
				if mode == "unsupported" {
					caps = strings.Replace(caps, `,"target-credentials"`, "", 1)
				}
				return []byte(caps), nil
			}
			err := stageIdentity(context.Background(), exec, func(_ context.Context, args []string, input []byte) ([]byte, error) {
				calls++
				if !bytes.Contains(input, []byte(`"setupId"`)) || strings.Contains(strings.Join(args, " "), n.SetupID) && !strings.Contains(strings.Join(args, " "), "/installer/") {
					t.Fatal("metadata not delivered via stdin")
				}
				if bytes.Contains(input, []byte("passwordFile")) || bytes.Contains(input, []byte("secretFile")) {
					t.Fatal("generated credential paths entered saved metadata")
				}
				status := "created"
				if calls == 2 && mode != "mutating-check" {
					status = "present"
				}
				if calls == 2 && args[len(args)-1] != "--check" {
					t.Fatal("preparation readback mutated")
				}
				if mode == "invalid-receipt" {
					status = "conflict"
				}
				return []byte(`{"schema":"kyidentity.setup-preparation/1","status":"` + status + `","detail":"never-print"}`), nil
			}, n, "/data/installer/"+n.SetupID+"/setup.json", false)
			if mode == "supported" && (err != nil || calls != 2) || mode != "supported" && err == nil || mode == "unsupported" && calls != 0 {
				t.Fatal(mode, calls, err)
			}
		})
	}
}

func TestIdentityPreparationPlanResumeAndDisclosure(t *testing.T) {
	n := &stack.IdentityPreparation{Schema: "kyidentity.setup/1", SetupID: "271333e2-c221-4601-b249-563b3d828530", Admin: stack.IdentityAccount{Username: "owner-admin"}, Everyday: stack.IdentityAccount{Username: "owner"}}
	st := &stack.Stack{Integrations: []stack.Integration{{Product: "kyidentity", Target: "nas", Container: "identity", Bundle: "/data/installer/" + n.SetupID + "/setup.json", Identity: n}}}
	answers := map[string]string{}
	seedAnswers(answers, st)
	raw := answers["integration.kyidentity.nas.identity"]
	var saved stack.IdentityPreparation
	if json.Unmarshal([]byte(raw), &saved) != nil || saved.SetupID != n.SetupID {
		t.Fatal("Identity preparation lost on guided resume")
	}
	var out bytes.Buffer
	st.Targets = []stack.Target{{Name: "nas", Host: "nas.example.test", User: "kyq"}}
	next := &stack.Stack{Targets: st.Targets}
	p := &prompts{ctx: context.Background(), in: bufio.NewReader(strings.NewReader("\n\n\n")), out: &out, answers: answers, draft: filepath.Join(t.TempDir(), "answers.json")}
	if err := guidedIntegration(p, next, stack.Existing{Product: "kyidentity", Target: "nas"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.Integrations, st.Integrations) {
		t.Fatal("guided resume lost typed Identity preparation")
	}
	showPlan(&out, next, nil)
	if !strings.Contains(out.String(), "generate private owner/everyday/client credentials on target") {
		t.Fatal("review hides target credential generation")
	}
}
