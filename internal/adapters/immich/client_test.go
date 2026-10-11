package immich

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifiedAdministratorProjection(t *testing.T) {
	for _, mode := range []string{"grant-repeat-demote", "foreign-subject", "foreign-id", "provider-drift", "insecure-provider", "missing-admin", "missing-deleted", "disabled", "unqualified-version", "lost-write", "post-write-drift", "redirect", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			b := Binding{ID: "271333e2-c221-4601-b249-563b3d828530", Issuer: "https://identity.example.test", ClientID: "photos", Subject: "owned-subject"}
			admin, writes := false, 0
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("x-api-key") != "private-fixture-key" {
					t.Error("private credential absent")
					w.WriteHeader(401)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/server/version":
					minor := 2
					if mode == "unqualified-version" {
						minor = 3
					}
					_ = json.NewEncoder(w).Encode(map[string]int{"major": 3, "minor": minor, "patch": 0})
				case "/api/system-config":
					issuer := b.Issuer
					if mode == "provider-drift" {
						issuer = "https://foreign.example.test"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"oauth": map[string]any{"enabled": true, "allowInsecureRequests": mode == "insecure-provider", "issuerUrl": issuer, "clientId": b.ClientID}, "privateField": "must-not-leak"})
				case "/api/admin/users/" + b.ID:
					if r.Method == http.MethodPut {
						writes++
						var body map[string]bool
						if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 {
							t.Error("extra mutation fields")
						}
						admin = body["isAdmin"]
						if mode == "lost-write" {
							w.WriteHeader(503)
							return
						}
					} else if r.Method != http.MethodGet {
						t.Error("unsupported method")
					}
					result := map[string]any{"id": b.ID, "oauthId": b.Subject, "name": "Everyday photos", "email": "daily@example.test", "status": "active", "isAdmin": admin, "deletedAt": nil}
					switch mode {
					case "foreign-subject":
						result["oauthId"] = "foreign"
					case "foreign-id":
						result["id"] = "271333e2-c221-4601-b249-563b3d828531"
					case "missing-admin":
						delete(result, "isAdmin")
					case "missing-deleted":
						delete(result, "deletedAt")
					case "disabled":
						result["status"] = "deleted"
					case "post-write-drift":
						if writes > 0 {
							result["oauthId"] = "foreign"
						}
					case "redirect":
						http.Redirect(w, r, "https://foreign.example.test", http.StatusFound)
						return
					case "malformed":
						_, _ = w.Write([]byte(`{"id":`))
						return
					}
					_ = json.NewEncoder(w).Encode(result)
				default:
					t.Error("unexpected native route")
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			c, err := New(srv.URL, "private-fixture-key")
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = srv.Client().Transport
			err = c.SetAdministrator(context.Background(), b, true)
			switch mode {
			case "grant-repeat-demote":
				if err != nil || writes != 1 {
					t.Fatal("grant not verified", err, writes)
				}
				if c.SetAdministrator(context.Background(), b, true) != nil || writes != 1 {
					t.Fatal("repeat rewrote account")
				}
				if c.SetAdministrator(context.Background(), b, false) != nil || writes != 2 || admin {
					t.Fatal("demotion not verified")
				}
			case "lost-write", "post-write-drift":
				if err != ErrUncertain || writes != 1 {
					t.Fatal("unknown mutation not held", err, writes)
				}
			default:
				if err == nil || writes != 0 {
					t.Fatal("unverified account mutated", err, writes)
				}
			}
			if err != nil && strings.Contains(err.Error(), "must-not-leak") {
				t.Fatal("native body leaked")
			}
		})
	}
}

func TestPrivateTransportAndReceiptBoundaries(t *testing.T) {
	for _, origin := range []string{"http://photos.example.test", "https://user@photos.example.test", "https://photos.example.test/", "https://photos.example.test?", "https://photos.example.test#"} {
		if _, err := New(origin, "fixture"); err == nil {
			t.Fatal("invalid origin accepted", origin)
		}
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid receipt crossed boundary") }))
	defer srv.Close()
	c, _ := New(srv.URL, "fixture")
	c.http.Transport = srv.Client().Transport
	if _, err := c.Inspect(context.Background(), Binding{ID: "../../foreign"}); err == nil {
		t.Fatal("invalid binding accepted")
	}
}
