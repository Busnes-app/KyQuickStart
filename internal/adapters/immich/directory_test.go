package immich

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDirectoryOwnershipRevisionAndUncertainOutcome(t *testing.T) {
	for _, mode := range []string{"verified-repeat", "foreign-setup", "foreign-user", "missing-revision", "missing-role", "profile-drift", "revoked", "lost-write", "post-revision", "post-revoked", "post-profile"} {
		t.Run(mode, func(t *testing.T) {
			b := Binding{"271333e2-c221-4601-b249-563b3d828530", "https://identity.example.test", "photos", "owned-subject"}
			const setupID = "371333e2-c221-4601-b249-563b3d828530"
			current := Profile{"Before", "before@example.test", true}
			desired := Profile{"After", "after@example.test", false}
			revision, writes := int64(7), 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("x-api-key") != "private-fixture-key" {
					t.Error("native credential absent")
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
					json.NewEncoder(w).Encode(map[string]any{"id": b.ID, "oauthId": b.Subject, "name": current.Name, "email": current.Email, "isAdmin": current.Administrator, "status": "active", "deletedAt": nil})
				case "/api/admin/users/" + b.ID + "/kyquickstart/directory":
					if r.Method == http.MethodPost {
						var body struct {
							SetupID, Issuer, ClientID, Subject string
							ExpectedRevision                   int64
							Profile                            Profile
						}
						if json.NewDecoder(r.Body).Decode(&body) != nil || body.SetupID != setupID || body.Issuer != b.Issuer || body.ClientID != b.ClientID || body.Subject != b.Subject || body.ExpectedRevision != revision || body.Profile != desired {
							t.Error("native directory mutation not scoped")
						}
						current = body.Profile
						revision++
						writes++
						if mode == "lost-write" {
							w.WriteHeader(503)
							return
						}
						json.NewEncoder(w).Encode(map[string]any{})
						return
					}
					if r.Method != http.MethodGet || r.URL.Query().Get("setupId") != setupID {
						t.Error("unscoped directory inspection")
					}
					profile := map[string]any{"name": current.Name, "email": current.Email, "administrator": current.Administrator}
					state := map[string]any{"contract": directoryContract, "setupId": setupID, "userId": b.ID, "issuer": b.Issuer, "clientId": b.ClientID, "subject": b.Subject, "revision": revision, "revoked": false, "profile": profile}
					switch mode {
					case "foreign-setup":
						state["setupId"] = b.ID
					case "foreign-user":
						state["subject"] = "foreign"
					case "missing-revision":
						delete(state, "revision")
					case "missing-role":
						delete(profile, "administrator")
					case "profile-drift":
						profile["email"] = "foreign@example.test"
					case "revoked":
						state["revoked"] = true
					case "post-revision":
						if writes > 0 {
							state["revision"] = revision + 1
						}
					case "post-revoked":
						if writes > 0 {
							state["revoked"] = true
						}
					case "post-profile":
						if writes > 0 {
							profile["name"] = "foreign"
						}
					}
					json.NewEncoder(w).Encode(state)
				default:
					t.Error("unexpected native directory path")
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			c, err := New(server.URL, "private-fixture-key")
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = server.Client().Transport
			err = c.ApplyDirectory(context.Background(), b, setupID, desired)
			switch mode {
			case "verified-repeat":
				if err != nil || writes != 1 || c.ApplyDirectory(context.Background(), b, setupID, desired) != nil || writes != 1 {
					t.Fatal("directory update/repeat not verified", err, writes)
				}
			case "lost-write":
				if !errors.Is(err, ErrUncertain) || writes != 1 || c.ApplyDirectory(context.Background(), b, setupID, desired) != nil || writes != 1 {
					t.Fatal("lost directory result not held/reconciled", err, writes)
				}
			case "post-revision", "post-revoked", "post-profile":
				if !errors.Is(err, ErrUncertain) || writes != 1 {
					t.Fatal("unproved directory mutation accepted", err, writes)
				}
			default:
				if err == nil || writes != 0 {
					t.Fatal("foreign/incomplete/revoked profile mutated", err, writes)
				}
			}
		})
	}
}
