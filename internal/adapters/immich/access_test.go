package immich

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccessProofRefusalAndUncertainWrites(t *testing.T) {
	for _, mode := range []string{"version7-installation", "foreign-installation", "missing-counts", "negative-counts", "lost-write", "post-missing-counts", "post-not-revoked", "post-profile-drift"} {
		t.Run(mode, func(t *testing.T) {
			b := Binding{ID: "271333e2-c221-4601-b249-563b3d828530", Issuer: "https://identity.example.test", ClientID: "photos", Subject: "owned-subject"}
			setupID := "371333e2-c221-4601-b249-563b3d828530"
			if mode == "version7-installation" {
				setupID = "371333e2-c221-7601-b249-563b3d828530"
			}
			writes, revoked := 0, false
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("x-api-key") != "private-fixture-key" {
					t.Error("private credential absent")
					w.WriteHeader(401)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/server/version":
					json.NewEncoder(w).Encode(map[string]int{"major": 3, "minor": 2, "patch": 0})
				case "/api/system-config":
					json.NewEncoder(w).Encode(map[string]any{"oauth": map[string]any{"enabled": true, "allowInsecureRequests": false, "issuerUrl": b.Issuer, "clientId": b.ClientID}})
				case "/api/admin/users/" + b.ID:
					name := "Everyday photos"
					if mode == "post-profile-drift" && writes > 0 {
						name = "changed"
					}
					json.NewEncoder(w).Encode(map[string]any{"id": b.ID, "oauthId": b.Subject, "name": name, "email": "daily@example.test", "status": "active", "isAdmin": false, "deletedAt": nil})
				case "/api/admin/users/" + b.ID + "/kyquickstart/revoke":
					if r.Method != http.MethodPost {
						t.Error("unexpected mutation method")
					}
					var body map[string]string
					if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 4 || body["setupId"] != setupID || body["issuer"] != b.Issuer || body["clientId"] != b.ClientID || body["subject"] != b.Subject {
						t.Error("unscoped mutation body")
					}
					writes++
					revoked = true
					if mode == "lost-write" {
						w.WriteHeader(503)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{})
				case "/api/admin/users/" + b.ID + "/kyquickstart/access":
					state := map[string]any{"contract": accessContract, "userId": b.ID, "issuer": b.Issuer, "clientId": b.ClientID, "subject": b.Subject, "setupId": nil, "revokedAt": nil, "revoked": revoked, "apiKeys": 0, "sessions": 0}
					if revoked {
						state["setupId"], state["revokedAt"] = setupID, "2026-10-09T00:00:00Z"
					}
					switch mode {
					case "foreign-installation":
						state["revoked"], state["setupId"], state["revokedAt"] = true, b.ID, "2026-10-09T00:00:00Z"
					case "missing-counts":
						delete(state, "apiKeys")
					case "negative-counts":
						state["sessions"] = -1
					case "post-missing-counts":
						if writes > 0 {
							delete(state, "sessions")
						}
					case "post-not-revoked":
						state["revoked"], state["setupId"], state["revokedAt"] = false, nil, nil
					}
					json.NewEncoder(w).Encode(state)
				default:
					t.Error("unexpected native route")
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			c, err := New(server.URL, "private-fixture-key")
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = server.Client().Transport
			err = c.RevokeAccess(context.Background(), b, setupID)
			switch mode {
			case "version7-installation":
				if err != nil || writes != 1 || !revoked {
					t.Fatal("installation UUID was confused with native account UUID", err, writes)
				}
			case "foreign-installation", "missing-counts", "negative-counts":
				if err == nil || writes != 0 {
					t.Fatal("invalid proof allowed mutation", err, writes)
				}
			default:
				if err != ErrUncertain || writes != 1 {
					t.Fatal("unknown write was not held", err, writes)
				}
			}
		})
	}
}
