package bookstack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func creationAbsenceReport(spec CreationSpec, absent bool) map[string]any {
	source := sha256.Sum256([]byte(spec.Account.Issuer + "\x00" + spec.Account.ClientID))
	return map[string]any{"contract": "ky.bookstack-creation-absence/1", "module_version": moduleVersion,
		"installation_id": spec.InstallationID, "account_setup_id": spec.Account.SetupID,
		"source_digest": hex.EncodeToString(source[:]), "binding_digest": digest(Binding{Issuer: spec.Account.Issuer, Subject: spec.Account.Subject}),
		"everyday_role": spec.EverydayRole, "ordinary_role_ids": spec.OrdinaryRoleIDs, "absent": absent}
}

func TestCreationHTTPSRecoversUnknownCommitWithoutAnotherWrite(t *testing.T) {
	spec := CreationSpec{InstallationID: testSpec.SetupID, Account: testSpec, EverydayRole: "editor", OrdinaryRoleIDs: []int64{2}}
	spec.Account.Administrator = false
	for _, outcome := range []string{"lost", "bad-proof", "known-refusal", "foreign-account"} {
		t.Run(outcome, func(t *testing.T) {
			committed, writes := outcome == "foreign-account", 0
			calls := []string{}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if r.Method != "POST" || r.URL.Path != "/api/kyquickstart/creation" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("c", 64) || json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("wrong scoped creation request")
					w.WriteHeader(400)
					return
				}
				action, _ := request["action"].(string)
				account, _ := request["account"].(map[string]any)
				if account["action"] != action || account["administrator"] != false || account["subject"] != spec.Account.Subject {
					t.Error("lost immutable account scope")
				}
				calls = append(calls, action)
				if action == "probe" {
					json.NewEncoder(w).Encode(creationAbsenceReport(spec, !committed))
					return
				}
				if action == "create" {
					writes++
					if outcome == "known-refusal" {
						http.Error(w, "private-server-body", 400)
						return
					}
					committed = true
					if outcome == "lost" {
						http.Error(w, "private-server-body", 503)
					} else {
						json.NewEncoder(w).Encode(map[string]any{"contract": "foreign"})
					}
					return
				}
				if outcome == "foreign-account" {
					http.Error(w, "private-server-body", 400)
					return
				}
				report := creationFixtureReport(spec)
				report["account"].(map[string]any)["created"] = false
				json.NewEncoder(w).Encode(report)
			}))
			defer server.Close()
			client, err := NewCreationHTTP(server.URL, strings.Repeat("c", 64))
			if err != nil {
				t.Fatal(err)
			}
			client.client.http.Transport = server.Client().Transport
			_, err = client.CreateManagedAccount(context.Background(), spec)
			if err == nil || strings.Contains(err.Error(), "private-server-body") {
				t.Fatal("unsafe success/error", err)
			}
			if outcome == "known-refusal" || outcome == "foreign-account" {
				if !errors.Is(err, ErrAccountConflict) || errors.Is(err, ErrCreationUncertain) {
					t.Fatal("known ownership refusal misclassified", err)
				}
				if outcome == "foreign-account" && writes != 0 {
					t.Fatal("foreign account mutated")
				}
				return
			}
			if !errors.Is(err, ErrCreationUncertain) {
				t.Fatal("unknown commit not held", err)
			}
			state, err := client.CreateManagedAccount(context.Background(), spec)
			if err != nil || state.Account.Created || writes != 1 || strings.Join(calls, ",") != "probe,create,probe,check" {
				t.Fatal("recovery repeated creation", err, calls)
			}
		})
	}
}

func TestCreationHTTPSRefusesIncompleteAbsenceBeforeWrite(t *testing.T) {
	spec := CreationSpec{InstallationID: testSpec.SetupID, Account: testSpec, EverydayRole: "editor", OrdinaryRoleIDs: []int64{2}}
	spec.Account.Administrator = false
	for _, field := range []string{"module_version", "installation_id", "account_setup_id", "source_digest", "binding_digest", "everyday_role", "ordinary_role_ids", "absent"} {
		t.Run(field, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				report := creationAbsenceReport(spec, true)
				report[field] = nil
				json.NewEncoder(w).Encode(report)
			}))
			defer server.Close()
			client, _ := NewCreationHTTP(server.URL, strings.Repeat("c", 64))
			client.client.http.Transport = server.Client().Transport
			if _, err := client.CreateManagedAccount(context.Background(), spec); err == nil || calls != 1 {
				t.Fatal("unverified absence used for creation", err)
			}
			spec.Account.Administrator = true
			if _, err := client.CreateManagedAccount(context.Background(), spec); err == nil || calls != 1 {
				t.Fatal("invalid intent crossed HTTP boundary", err)
			}
			spec.Account.Administrator = false
		})
	}
}

func TestCreatedScopeRequiresJoinedNativeIntentAndSource(t *testing.T) {
	scope := CreationScope{InstallationID: testSpec.SetupID, AccountSetupID: testSpec.SetupID, Issuer: testSpec.Issuer, ClientID: testSpec.ClientID, Subject: testSpec.Subject, EverydayRole: "editor", OrdinaryRoleIDs: []int64{2}}
	for _, field := range []string{"valid", "installation_id", "source_digest", "binding_digest", "account_setup_id", "user_id", "account_intent_digest", "creation_intent_digest", "ordinary_role_ids"} {
		t.Run(field, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if json.NewDecoder(r.Body).Decode(&request) != nil || request["action"] != "resolve" || request["subject"] != scope.Subject || request["account"] != nil || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("c", 64) {
					t.Error("not a scoped read-only resolution")
				}
				source := sha256.Sum256([]byte(scope.Issuer + "\x00" + scope.ClientID))
				accountIntent := strings.Repeat("ab", 32)
				body, _ := json.Marshal([]any{scope.InstallationID, accountIntent, scope.EverydayRole, scope.OrdinaryRoleIDs})
				intent := sha256.Sum256(body)
				report := map[string]any{"contract": "ky.bookstack-creation-scope/1", "module_version": moduleVersion, "installation_id": scope.InstallationID,
					"source_digest": hex.EncodeToString(source[:]), "binding_digest": digest(Binding{Issuer: scope.Issuer, Subject: scope.Subject}), "everyday_role": scope.EverydayRole, "ordinary_role_ids": scope.OrdinaryRoleIDs,
					"user_id": 3, "account_setup_id": testSpec.SetupID, "account_intent_digest": accountIntent, "creation_intent_digest": hex.EncodeToString(intent[:])}
				if field != "valid" {
					report[field] = nil
				}
				json.NewEncoder(w).Encode(report)
			}))
			defer server.Close()
			client, _ := NewCreationHTTP(server.URL, strings.Repeat("c", 64))
			client.client.http.Transport = server.Client().Transport
			resolved, err := client.ResolveCreatedScope(context.Background(), scope)
			if field == "valid" {
				if err != nil || resolved.ID != 3 || resolved.SetupID != testSpec.SetupID || resolved.Subject != scope.Subject {
					t.Fatal("native scope lost", err)
				}
			} else if err == nil {
				t.Fatal("unproved scope accepted")
			}
		})
	}
}
