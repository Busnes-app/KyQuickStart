package bookstack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPRevocationChecksBeforeMutationAndRepairsPartialState(t *testing.T) {
	b := Binding{ID: 2, Issuer: "https://identity.example.test", Subject: "subject"}
	disabled, complete, tokens := true, false, int64(0)
	calls := []string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/kyquickstart/offboard" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) {
			t.Error("wrong scoped native request")
		}
		var req struct {
			Action  string `json:"action"`
			ID      int64  `json:"user_id"`
			Subject string `json:"subject"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ID != b.ID || req.Subject != b.Subject {
			t.Error("missing identity binding")
		}
		calls = append(calls, req.Action)
		if req.Action == "deprovision" {
			complete = true
		}
		json.NewEncoder(w).Encode(map[string]any{"contract": contract, "module_version": moduleVersion, "user_id": b.ID, "binding_digest": digest(b), "disabled": disabled, "revocation_complete": complete, "tokens_remaining": tokens})
	}))
	defer server.Close()
	client, err := NewHTTP(server.URL, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = server.Client().Transport
	if err := client.Deprovision(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "check,deprovision,check" {
		t.Fatal("partial publication not repaired", calls)
	}
	if err := client.Deprovision(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 || calls[3] != "check" {
		t.Fatal("completed revocation mutated on resume", calls)
	}
}

func TestHTTPRefusesRedirectsErrorBodiesAndWrongReceipts(t *testing.T) {
	for _, mode := range []string{"redirect", "error", "wrong-version", "wrong-binding", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			b := Binding{ID: 2, Issuer: "https://identity.example.test", Subject: "subject"}
			mutations := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Action string `json:"action"`
				}
				json.NewDecoder(r.Body).Decode(&body)
				if body.Action == "deprovision" {
					mutations++
				}
				switch mode {
				case "redirect":
					w.Header().Set("Location", "https://must-not-contact.example/credential")
					w.WriteHeader(307)
				case "error":
					http.Error(w, "credential-must-not-print", 500)
				case "oversized":
					w.Write([]byte(strings.Repeat("x", 16385)))
				default:
					version, binding := moduleVersion, digest(b)
					if mode == "wrong-version" {
						version = "unsupported"
					} else {
						binding = strings.Repeat("b", 64)
					}
					json.NewEncoder(w).Encode(map[string]any{"contract": contract, "module_version": version, "user_id": b.ID, "binding_digest": binding, "disabled": false, "revocation_complete": false, "tokens_remaining": 1})
				}
			}))
			defer server.Close()
			client, err := NewHTTP(server.URL, strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			client.http.Transport = server.Client().Transport
			err = client.Deprovision(context.Background(), b)
			if err == nil || strings.Contains(err.Error(), "credential-must-not-print") || mutations != 0 {
				t.Fatal("unsafe response accepted, reflected or mutated", err)
			}
		})
	}
	for _, origin := range []string{"http://bookstack.test", "https://credential@bookstack.test", "https://bookstack.test/path", "https://bookstack.test?", "https://bookstack.test#"} {
		if _, err := NewHTTP(origin, strings.Repeat("a", 64)); err == nil {
			t.Fatal("unsafe origin accepted")
		}
	}
}

func TestDirectoryHTTPSRefusesUnsafeRepliesBeforeProjection(t *testing.T) {
	scope := RoleScope{ProfileScope: ProfileScope{Binding: Binding{ID: 3, Issuer: testSpec.Issuer, Subject: testSpec.Subject}, SetupID: testSpec.SetupID, ClientID: testSpec.ClientID}, EverydayRole: "editor", OrdinaryRoleIDs: []int64{2}}
	for _, mode := range []string{"redirect", "error", "oversized", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var request struct {
					Profile struct{ Action string } `json:"profile"`
				}
				if r.URL.Path != "/api/kyquickstart/directory" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("b", 64) ||
					json.NewDecoder(r.Body).Decode(&request) != nil || request.Profile.Action != "check" {
					t.Error("unsafe or unexpected directory projection")
				}
				switch mode {
				case "redirect":
					w.Header().Set("Location", "https://must-not-contact.example/credential")
					w.WriteHeader(http.StatusTemporaryRedirect)
				case "error":
					http.Error(w, "credential-must-not-print", 503)
				case "oversized":
					w.Write([]byte(strings.Repeat("x", 16385)))
				default:
					json.NewEncoder(w).Encode(map[string]any{"contract": directoryContract, "module_version": "foreign"})
				}
			}))
			defer server.Close()
			client, err := NewDirectoryHTTP(server.URL, strings.Repeat("b", 64))
			if err != nil {
				t.Fatal(err)
			}
			client.client.http.Transport = server.Client().Transport
			_, err = client.ApplyDirectory(context.Background(), scope, Profile{"Changed", "changed@example.test"}, true)
			if err == nil || strings.Contains(err.Error(), "credential-must-not-print") || calls != 1 {
				t.Fatal("unsafe directory reply followed, reflected or projected", err)
			}
		})
	}
}
