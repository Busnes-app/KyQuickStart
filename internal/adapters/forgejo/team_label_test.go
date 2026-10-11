package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTeamLabelChangesOnlyDescriptionAndHoldsLostWrites(t *testing.T) {
	for _, mode := range []string{"complete", "lost", "drift"} {
		t.Run(mode, func(t *testing.T) {
			label, writes := "Old label", 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPatch {
					writes++
					var body map[string]string
					if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["description"] != "New label" {
						t.Error("label changed native authority")
					}
					label = "New label"
					if mode == "lost" {
						w.WriteHeader(503)
						return
					}
					w.Write([]byte(`{}`))
					return
				}
				permission := "read"
				if mode == "drift" && writes > 0 {
					permission = "admin"
				}
				json.NewEncoder(w).Encode(map[string]any{"id": 11, "name": "readers", "description": label, "organization": map[string]any{"id": 12, "name": "org", "repo_admin_change_team_access": false}, "permission": permission, "units": []string{"repo.code"}, "units_map": map[string]string{"repo.code": "read"}, "includes_all_repositories": false, "can_create_org_repo": false})
			}))
			defer server.Close()
			c, _ := New(server.URL, "fixture-token")
			c.http.Transport = server.Client().Transport
			b := TeamBinding{ID: 11, OrganizationID: 12, SourceID: 7, Name: "readers", Organization: "org", Permission: "read", Permissions: map[string]string{"repo.code": "read"}}
			err := c.UpdateTeamLabel(context.Background(), b, "New label")
			if mode == "complete" {
				if err != nil || writes != 1 {
					t.Fatal("label not verified", err)
				}
				if err := c.UpdateTeamLabel(context.Background(), b, "New label"); err != nil || writes != 1 {
					t.Fatal("label repeat rewrote metadata", err)
				}
			} else if !errors.Is(err, ErrTeamUncertain) || writes != 1 {
				t.Fatal("unknown label outcome not fenced", err)
			}
		})
	}
}
