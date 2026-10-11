package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTeamMembershipScopeAndUncertainty(t *testing.T) {
	for _, mode := range []string{"complete", "foreign", "truncated", "admin", "inactive", "drift", "uncertain", "post-drift", "post-admin"} {
		t.Run(mode, func(t *testing.T) {
			writes, member := 0, false
			b := TeamBinding{ID: 11, OrganizationID: 12, SourceID: 7, Name: "readers", Organization: "org", Permission: "read", Permissions: map[string]string{"repo.code": "read"}}
			account := Binding{ID: 31, Username: "daily", SourceID: 7, Subject: "daily-subject"}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut || r.Method == http.MethodDelete {
					writes++
					if mode == "uncertain" {
						w.WriteHeader(503)
						return
					}
					member = r.Method == http.MethodPut
					w.WriteHeader(204)
					return
				}
				switch r.URL.Path {
				case "/api/v1/teams/11":
					permission := "read"
					if mode == "drift" || mode == "post-drift" && writes > 0 {
						permission = "admin"
					}
					json.NewEncoder(w).Encode(map[string]any{"id": 11, "name": "readers", "organization": map[string]any{"id": 12, "name": "org", "repo_admin_change_team_access": false}, "permission": permission, "units": []string{"repo.code"}, "units_map": b.Permissions, "includes_all_repositories": false, "can_create_org_repo": false})
				case "/api/v1/teams/11/members":
					if r.URL.Query().Get("limit") != "50" || r.URL.Query().Get("page") != "1" {
						t.Error("unbounded member read")
					}
					w.Header().Set("X-Total-Count", "0")
					if mode == "foreign" {
						w.Header().Set("X-Total-Count", "1")
						w.Write([]byte(`[{"id":90,"login":"foreign"}]`))
						return
					}
					if mode == "truncated" {
						w.Header().Set("X-Total-Count", "101")
					}
					if member {
						w.Header().Set("X-Total-Count", "1")
						w.Write([]byte(`[{"id":31,"login":"daily"}]`))
						return
					}
					w.Write([]byte(`[]`))
				case "/api/v1/users/daily":
					json.NewEncoder(w).Encode(map[string]any{"id": 31, "login": "daily", "source_id": 7, "login_name": "daily-subject", "is_admin": mode == "admin" || mode == "post-admin" && writes > 0, "active": mode != "inactive", "prohibit_login": false})
				default:
					t.Error("unexpected API path", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			c, _ := New(server.URL, "fixture-token")
			c.http.Transport = server.Client().Transport
			err := c.ReplaceTeamMembers(context.Background(), b, []Binding{account}, []Binding{account})
			switch mode {
			case "complete":
				if err != nil || writes != 1 || !member {
					t.Fatal("membership not verified", err)
				}
				if err = c.ReplaceTeamMembers(context.Background(), b, []Binding{account}, []Binding{account}); err != nil || writes != 1 {
					t.Fatal("repeat rewrote membership", err)
				}
				if err = c.ReplaceTeamMembers(context.Background(), b, []Binding{account}, nil); err != nil || writes != 2 || member {
					t.Fatal("withdrawal not verified", err)
				}
			case "uncertain", "post-drift", "post-admin":
				if !errors.Is(err, ErrTeamUncertain) || writes != 1 {
					t.Fatal("unknown outcome not held", err)
				}
			default:
				if err == nil || writes != 0 {
					t.Fatal("unsafe scope authorized mutation", err)
				}
			}
		})
	}
}

func TestTeamCreationRequiresAbsenceAndClassifiesUnknown(t *testing.T) {
	for _, mode := range []string{"collision", "incomplete", "org-drift", "uncertain", "rejected", "malformed-success"} {
		t.Run(mode, func(t *testing.T) {
			writes := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					writes++
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["can_create_org_repo"] != false || body["includes_all_repositories"] != false || body["permission"] != "read" {
						t.Error("team create grants unexpected authority")
					}
					switch mode {
					case "uncertain":
						w.WriteHeader(503)
					case "rejected":
						w.WriteHeader(422)
					default:
						w.Write([]byte(`{}`))
					}
					return
				}
				if r.URL.Path == "/api/v1/orgs/org" {
					json.NewEncoder(w).Encode(map[string]any{"id": 12, "name": "org", "repo_admin_change_team_access": mode == "org-drift"})
					return
				}
				if mode == "collision" {
					w.Header().Set("X-Total-Count", "1")
					w.Write([]byte(`[{"id":11,"name":"READERS"}]`))
					return
				}
				if mode != "incomplete" {
					w.Header().Set("X-Total-Count", "0")
				}
				w.Write([]byte(`[]`))
			}))
			defer server.Close()
			c, _ := New(server.URL, "fixture-token")
			c.http.Transport = server.Client().Transport
			_, err := c.CreateTeam(context.Background(), TeamBinding{OrganizationID: 12, SourceID: 7, Organization: "org", Name: "readers", Permission: "read", Permissions: map[string]string{"repo.code": "read"}})
			switch mode {
			case "uncertain", "malformed-success":
				if !errors.Is(err, ErrTeamUncertain) || writes != 1 {
					t.Fatal("unknown team creation released", err)
				}
			case "rejected":
				if err == nil || errors.Is(err, ErrTeamUncertain) || writes != 1 {
					t.Fatal("known refusal misclassified", err)
				}
			default:
				if err == nil || writes != 0 {
					t.Fatal("unproved team absence authorized creation", err)
				}
			}
		})
	}
}
