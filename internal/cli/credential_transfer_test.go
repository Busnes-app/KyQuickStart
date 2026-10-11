package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

func transferFixture() (stack.Integration, stack.Integration, clientCredentials) {
	callback := "https://notes.example.test/api/v1/auth/oidc/callback"
	source := stack.Integration{Product: "kyidentity", Target: "identity-host", Container: "identity", Bundle: "/data/installer/271333e2-c221-4601-b249-563b3d828530/setup.json", Identity: &stack.IdentityPreparation{SetupID: "271333e2-c221-4601-b249-563b3d828530", Admin: stack.IdentityAccount{Username: "owner-admin"}, Clients: []stack.IdentityClient{{ID: "notes", RedirectURIs: []string{callback}, AdministratorRole: "kynotes.admin", EverydayRole: "kynotes.user", SuiteWebhook: &stack.IdentityWebhook{URL: "https://notes.example.test/api/v1/sync/events"}}}}}
	destination := stack.Integration{Product: "kynotes", Target: "notes-host", Container: "notes", Bundle: "/data/installer/setup.json", IdentityClient: "notes", Notes: &stack.NotesPreparation{Version: 1, SSO: &stack.NotesSSO{IssuerURL: "https://identity.example.test", ClientID: "notes", RedirectURI: callback, ClientSecretFile: "/data/installer/client.secret", DirectoryHMACSecretFile: "/data/installer/directory.secret"}}}
	frame := clientCredentials{Schema: "kyidentity.client-credentials/1", SetupID: source.Identity.SetupID, IssuerURL: destination.Notes.SSO.IssuerURL, ClientID: "notes", RedirectURIs: []string{callback}, DirectoryURL: source.Identity.Clients[0].SuiteWebhook.URL, ClientSecret: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{'c'}, 32)), DirectorySecret: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{'d'}, 32))}
	frame.Administrator.Subject, frame.Administrator.Username, frame.Administrator.Role = "371333e2-c221-4601-b249-563b3d828530", "owner-admin", "kynotes.admin"
	return source, destination, frame
}

func transferCaps(service string) []byte {
	features := []string{"local-accounts", "confidential-oidc-clients", "assigned-only", "everyday-assignment", "app-role-assignments", "scim-connectors", "suite-webhook-connectors", "directory-groups", "app-group-assignments", "scim-groups", "check", "protected-setup-files", "bundle-preparation", "target-credentials", "client-credential-export", "sso", "issuer-bound-admin", "dedicated-admin", "backup", "client-credential-import"}
	raw, _ := json.Marshal(map[string]any{"schema": "ky.setup-capabilities/1", "service": service, "bundleSchema": service + ".setup/1", "features": features})
	return raw
}

func TestCredentialTransferStagesResolvedOwnerAndChecksImport(t *testing.T) {
	for _, check := range []bool{false, true} {
		source, destination, frame := transferFixture()
		var exported []byte
		sourceExec := func(_ context.Context, args []string) ([]byte, error) {
			if args[1] == "--capabilities" {
				return transferCaps("kyidentity"), nil
			}
			if !reflect.DeepEqual(args, []string{"apply-setup", "--file", source.Bundle, "--export-client", "notes"}) {
				t.Fatal("source export used an unselected bundle or client")
			}
			exported, _ = json.Marshal(frame)
			return exported, nil
		}
		destinationExec := func(context.Context, []string) ([]byte, error) { return transferCaps("kynotes"), nil }
		calls := []string{}
		inputExec := func(_ context.Context, args []string, input []byte) ([]byte, error) {
			inspection := args[len(args)-1] == "--check"
			if check && !inspection {
				t.Fatal("inspection attempted mutation")
			}
			calls = append(calls, args[1])
			schema := "kynotes.setup-preparation/1"
			if args[1] == "--stage" {
				var n stack.NotesPreparation
				if json.Unmarshal(input, &n) != nil || len(n.Admins) != 1 || n.Admins[0].Subject != frame.Administrator.Subject || n.Admins[0].Username != "owner-admin" || bytes.Contains(input, []byte(frame.ClientSecret)) || bytes.Contains(input, []byte(frame.DirectorySecret)) {
					t.Fatal("stage did not use secret-free canonical native owner metadata")
				}
			} else if args[1] == "--import-client" {
				schema = "kynotes.client-import/1"
				expected, _ := json.Marshal(frame)
				if !bytes.Equal(input, expected) || strings.Contains(strings.Join(args, " "), frame.ClientSecret) {
					t.Fatal("private frame did not stay on stdin")
				}
			} else {
				t.Fatal("unexpected native command")
			}
			status := "created"
			if inspection {
				status = "present"
			}
			return []byte(`{"schema":"` + schema + `","status":"` + status + `"}`), nil
		}
		if err := transferNotesCredentials(context.Background(), sourceExec, destinationExec, inputExec, source, destination, check); err != nil {
			t.Fatal(err)
		}
		expected := []string{"--stage", "--stage", "--import-client", "--import-client"}
		if check {
			expected = []string{"--stage", "--import-client"}
		}
		if !reflect.DeepEqual(calls, expected) || len(destination.Notes.Admins) != 0 {
			t.Fatal("transfer order or saved plan changed")
		}
		if !bytes.Equal(exported, make([]byte, len(exported))) {
			t.Fatal("private source buffer was not cleared")
		}
	}
}

func TestCredentialTransferRefusesBeforeDestinationWrites(t *testing.T) {
	for _, mode := range []string{"source-capability", "destination-capability", "setup-id", "issuer", "client", "callback", "owner", "role", "secret", "directory", "unknown", "large", "source-error"} {
		t.Run(mode, func(t *testing.T) {
			source, destination, frame := transferFixture()
			switch mode {
			case "setup-id":
				frame.SetupID = "other"
			case "issuer":
				frame.IssuerURL = "https://other.example.test"
			case "client":
				frame.ClientID = "other"
			case "callback":
				frame.RedirectURIs = []string{"https://other.example.test/callback"}
			case "owner":
				frame.Administrator.Username = "other"
			case "role":
				frame.Administrator.Role = "admin"
			case "secret":
				frame.ClientSecret = "never-echo-private"
			case "directory":
				frame.DirectoryURL = "https://other.example.test/api/v1/sync/events"
			}
			sourceExec := func(_ context.Context, args []string) ([]byte, error) {
				if args[1] == "--capabilities" {
					raw := transferCaps("kyidentity")
					if mode == "source-capability" {
						raw = bytes.ReplaceAll(raw, []byte("client-credential-export"), []byte("missing"))
					}
					return raw, nil
				}
				if mode == "source-error" {
					return nil, errors.New("never-echo-private")
				}
				raw, _ := json.Marshal(frame)
				if mode == "unknown" {
					raw = append(raw[:len(raw)-1], []byte(`,"password":"never-echo-private"}`)...)
				}
				if mode == "large" {
					raw = bytes.Repeat([]byte{'x'}, (64<<10)+1)
				}
				return raw, nil
			}
			destinationExec := func(context.Context, []string) ([]byte, error) {
				raw := transferCaps("kynotes")
				if mode == "destination-capability" {
					raw = bytes.ReplaceAll(raw, []byte("client-credential-import"), []byte("missing"))
				}
				return raw, nil
			}
			writes := 0
			err := transferNotesCredentials(context.Background(), sourceExec, destinationExec, func(context.Context, []string, []byte) ([]byte, error) { writes++; return nil, nil }, source, destination, false)
			if err == nil || writes != 0 || strings.Contains(err.Error(), "never-echo-private") {
				t.Fatal("unsafe transfer mutated destination or disclosed private data")
			}
		})
	}
}

func TestCredentialTransferGuidedResumeAndReview(t *testing.T) {
	_, destination, _ := transferFixture()
	st := &stack.Stack{Targets: []stack.Target{{Name: destination.Target, Host: "notes.example.test", User: "kyq"}}, Integrations: []stack.Integration{destination}}
	answers := map[string]string{}
	seedAnswers(answers, st)
	var out bytes.Buffer
	next := &stack.Stack{Targets: st.Targets}
	p := &prompts{ctx: context.Background(), in: bufio.NewReader(strings.NewReader("\n\n\n\n")), out: &out, answers: answers, draft: filepath.Join(t.TempDir(), "answers.json")}
	if err := guidedIntegration(p, next, stack.Existing{Product: "kynotes", Target: destination.Target}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.Integrations, st.Integrations) {
		t.Fatal("guided resume lost explicit transfer selection")
	}
	showPlan(&out, next, nil)
	if !strings.Contains(out.String(), "Transfer the selected KyIdentity client notes credentials in memory") {
		t.Fatal("review hid credential transfer")
	}
	if len(next.Integrations[0].Notes.Admins) != 0 {
		t.Fatal("guided state guessed an owner subject")
	}
}

func TestCredentialTransferRejectsImportAndChangedSource(t *testing.T) {
	for _, mode := range []string{"import-error", "mutating-inspection", "source-change"} {
		t.Run(mode, func(t *testing.T) {
			source, destination, frame := transferFixture()
			exports := 0
			sourceExec := func(_ context.Context, args []string) ([]byte, error) {
				if args[1] == "--capabilities" {
					return transferCaps("kyidentity"), nil
				}
				exports++
				if mode == "source-change" && exports == 2 {
					frame.ClientSecret = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, 32))
				}
				raw, _ := json.Marshal(frame)
				return raw, nil
			}
			destinationExec := func(context.Context, []string) ([]byte, error) { return transferCaps("kynotes"), nil }
			imports := 0
			err := transferNotesCredentials(context.Background(), sourceExec, destinationExec, func(_ context.Context, args []string, _ []byte) ([]byte, error) {
				schema := "kynotes.setup-preparation/1"
				status := "created"
				if args[len(args)-1] == "--check" {
					status = "present"
				}
				if args[1] == "--import-client" {
					imports++
					schema = "kynotes.client-import/1"
					if mode == "import-error" {
						return nil, errors.New("never-echo-private")
					}
					if mode == "mutating-inspection" && status == "present" {
						status = "created"
					}
				}
				return []byte(`{"schema":"` + schema + `","status":"` + status + `"}`), nil
			}, source, destination, false)
			if err == nil || imports == 0 || strings.Contains(err.Error(), "never-echo-private") {
				t.Fatal("unverified transfer accepted or disclosed private error")
			}
			if mode == "source-change" && exports != 2 {
				t.Fatal("source not reverified after destination import")
			}
			if len(destination.Notes.Admins) != 0 {
				t.Fatal("failed transfer changed saved owner metadata")
			}
		})
	}
}
