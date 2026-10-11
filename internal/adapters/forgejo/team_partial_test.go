package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTeamReplacementWithdrawsFirstAndHoldsPartialFailure(t *testing.T) {
	old := Binding{ID: 31, Username: "old", SourceID: 7, Subject: "old-subject"}
	next := Binding{ID: 32, Username: "new", SourceID: 7, Subject: "new-subject"}
	writes := []string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete || r.Method == http.MethodPut {
			writes = append(writes, r.Method+" "+r.URL.Path)
			if r.Method == http.MethodPut {
				w.WriteHeader(422)
				return
			}
			w.WriteHeader(204)
			return
		}
		switch r.URL.Path {
		case "/api/v1/teams/11":
			w.Write([]byte(`{"id":11,"name":"readers","organization":{"id":12,"name":"org","repo_admin_change_team_access":false},"permission":"read","units":["repo.code"],"units_map":{"repo.code":"read"},"includes_all_repositories":false,"can_create_org_repo":false}`))
		case "/api/v1/teams/11/members":
			w.Header().Set("X-Total-Count", "1")
			w.Write([]byte(`[{"id":31,"login":"old"}]`))
		default:
			account := old
			if strings.HasSuffix(r.URL.Path, "/new") {
				account = next
			}
			json.NewEncoder(w).Encode(map[string]any{"id": account.ID, "login": account.Username, "source_id": account.SourceID, "login_name": account.Subject, "is_admin": false, "active": true, "prohibit_login": false})
		}
	}))
	defer server.Close()
	c, _ := New(server.URL, "fixture-token")
	c.http.Transport = server.Client().Transport
	b := TeamBinding{ID: 11, OrganizationID: 12, SourceID: 7, Name: "readers", Organization: "org", Permission: "read", Permissions: map[string]string{"repo.code": "read"}}
	// New-first input must still revoke old access before its first addition.
	err := c.ReplaceTeamMembers(context.Background(), b, []Binding{next, old}, []Binding{next})
	if !errors.Is(err, ErrTeamUncertain) || len(writes) != 2 || writes[0] != "DELETE /api/v1/teams/11/members/old" || writes[1] != "PUT /api/v1/teams/11/members/new" {
		t.Fatal("partial replacement is not fenced or removal order changed", err, writes)
	}
}
