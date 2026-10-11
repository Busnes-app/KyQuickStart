package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// servePage mimics Forgejo v16.0.5: limit is clamped to the default MAX_RESPONSE_ITEMS (50).
func servePage[T any](w http.ResponseWriter, r *http.Request, items []T, total int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit = min(max(limit, 1), 50)
	start := min(max(page-1, 0)*limit, len(items))
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	json.NewEncoder(w).Encode(items[start:min(start+limit, len(items))])
}

const pageTeamJSON = `{"id":11,"name":"readers","organization":{"id":12,"name":"org","repo_admin_change_team_access":false},"permission":"read","units":["repo.code"],"units_map":{"repo.code":"read"},"includes_all_repositories":false,"can_create_org_repo":false}`

var pageTeam = TeamBinding{ID: 11, OrganizationID: 12, SourceID: 7, Name: "readers", Organization: "org", Permission: "read", Permissions: map[string]string{"repo.code": "read"}}

func newPageClient(t *testing.T, h http.HandlerFunc) *Client {
	server := httptest.NewTLSServer(h)
	t.Cleanup(server.Close)
	c, _ := New(server.URL, "fixture-token")
	c.http.Transport = server.Client().Transport
	return c
}

func TestTeamAbsencePagesPastServerClamp(t *testing.T) {
	for _, mode := range []string{"absent", "collision-page-3", "unstable-total", "duplicate", "short", "over-cap"} {
		t.Run(mode, func(t *testing.T) {
			teams := make([]namedEntry, 120)
			for i := range teams {
				teams[i] = namedEntry{ID: int64(i + 1), Name: fmt.Sprintf("team-%d", i+1)}
			}
			if mode == "collision-page-3" {
				teams[110].Name = "READERS"
			}
			if mode == "over-cap" {
				teams = append(teams, make([]namedEntry, maxListed)...)
			}
			pages := 0
			c := newPageClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("absence proof wrote", r.Method)
				}
				if r.URL.Path == "/api/v1/orgs/org" {
					w.Write([]byte(`{"id":12,"name":"org","repo_admin_change_team_access":false}`))
					return
				}
				pages++
				total := len(teams)
				switch {
				case mode == "unstable-total" && pages == 2:
					total++
				case mode == "duplicate" && pages == 2:
					r.URL.RawQuery = "limit=50&page=1"
				case mode == "short":
					total++
				}
				servePage(w, r, teams, total)
			})
			err := c.TeamAbsent(context.Background(), TeamBinding{OrganizationID: 12, SourceID: 7, Organization: "org", Name: "readers", Permission: "read", Permissions: map[string]string{"repo.code": "read"}})
			switch mode {
			case "absent":
				if err != nil || pages != 3 {
					t.Fatal("absence across clamped pages not proven", err, pages)
				}
			case "collision-page-3":
				if !errors.Is(err, ErrTeamConflict) {
					t.Fatal("collision past the first page missed", err)
				}
			default:
				if err == nil || errors.Is(err, ErrTeamConflict) {
					t.Fatal("inconsistent listing proved absence", err)
				}
			}
		})
	}
}

func TestTeamMembershipPagesPastServerClamp(t *testing.T) {
	for _, mode := range []string{"withdraw-page-2", "foreign-page-2", "foreign-over-scope"} {
		t.Run(mode, func(t *testing.T) {
			owned := make([]Binding, 60)
			members := make([]memberEntry, 60)
			for i := range owned {
				owned[i] = Binding{ID: int64(101 + i), Username: fmt.Sprintf("user-%d", i), SourceID: 7, Subject: fmt.Sprintf("subject-%d", i)}
				members[i] = memberEntry{ID: owned[i].ID, Login: owned[i].Username}
			}
			switch mode {
			case "foreign-page-2":
				members[55] = memberEntry{ID: 999, Login: "foreign"}
			case "foreign-over-scope":
				members = append(members, memberEntry{ID: 999, Login: "foreign"})
			}
			writes := []string{}
			c := newPageClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete || r.Method == http.MethodPut {
					writes = append(writes, r.Method+" "+r.URL.Path)
					login := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
					members = slices.DeleteFunc(members, func(m memberEntry) bool { return m.Login == login })
					w.WriteHeader(204)
					return
				}
				switch {
				case r.URL.Path == "/api/v1/teams/11":
					w.Write([]byte(pageTeamJSON))
				case r.URL.Path == "/api/v1/teams/11/members":
					servePage(w, r, members, len(members))
				case strings.HasPrefix(r.URL.Path, "/api/v1/users/user-"):
					i, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/users/user-"))
					a := owned[i]
					json.NewEncoder(w).Encode(map[string]any{"id": a.ID, "login": a.Username, "source_id": 7, "login_name": a.Subject, "is_admin": false, "active": true, "prohibit_login": false})
				default:
					t.Error("unexpected API path", r.URL.Path)
					w.WriteHeader(404)
				}
			})
			err := c.ReplaceTeamMembers(context.Background(), pageTeam, owned, owned[:59])
			if mode != "withdraw-page-2" {
				if err == nil || len(writes) != 0 {
					t.Fatal("foreign member past the first page authorized mutation", err, writes)
				}
				return
			}
			if err != nil || !slices.Equal(writes, []string{"DELETE /api/v1/teams/11/members/user-59"}) {
				t.Fatal("withdrawal past the first page not completed", err, writes)
			}
		})
	}
}

func TestTeamWithdrawsBeforeRefusingUnfitDesiredMember(t *testing.T) {
	for _, mode := range []string{"verified", "unverified"} {
		t.Run(mode, func(t *testing.T) {
			old := Binding{ID: 31, Username: "old", SourceID: 7, Subject: "old-subject"}
			daily := Binding{ID: 32, Username: "daily", SourceID: 7, Subject: "daily-subject"}
			current := []memberEntry{{ID: 31, Login: "old"}}
			writes := []string{}
			c := newPageClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete || r.Method == http.MethodPut {
					writes = append(writes, r.Method+" "+r.URL.Path)
					if mode == "verified" {
						current = []memberEntry{}
					}
					w.WriteHeader(204)
					return
				}
				switch r.URL.Path {
				case "/api/v1/teams/11":
					w.Write([]byte(pageTeamJSON))
				case "/api/v1/teams/11/members":
					servePage(w, r, current, len(current))
				case "/api/v1/users/old":
					json.NewEncoder(w).Encode(map[string]any{"id": 31, "login": "old", "source_id": 7, "login_name": "old-subject", "is_admin": false, "active": true, "prohibit_login": false})
				case "/api/v1/users/daily":
					json.NewEncoder(w).Encode(map[string]any{"id": 32, "login": "daily", "source_id": 7, "login_name": "daily-subject", "is_admin": false, "active": false, "prohibit_login": true})
				default:
					t.Error("unexpected API path", r.URL.Path)
					w.WriteHeader(404)
				}
			})
			err := c.ReplaceTeamMembers(context.Background(), pageTeam, []Binding{daily, old}, []Binding{daily})
			if !slices.Equal(writes, []string{"DELETE /api/v1/teams/11/members/old"}) {
				t.Fatal("withdrawal did not precede desired-member refusal, or an unfit member was added", writes)
			}
			if mode == "verified" && (err == nil || errors.Is(err, ErrTeamUncertain)) {
				t.Fatal("verified withdrawal with unfit addition must be a definite refusal", err)
			}
			if mode == "unverified" && !errors.Is(err, ErrTeamUncertain) {
				t.Fatal("unverified withdrawal was not held", err)
			}
		})
	}
}
