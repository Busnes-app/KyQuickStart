package stockbookstack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"
)

const (
	testToken  = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA:SecretSecretSecretSecretSecretSe"
	testSecret = "directory-signing-secret-of-32-bytes"
	admin      = 1
	editor     = 2
)

// fakeBookStack is BookStack's API: users with roles, the system roles, and every call.
type fakeBookStack struct {
	mu    sync.Mutex
	users map[int]*User
	calls []string
	down  bool
	stuck bool          // a role update that does not take
	delay time.Duration // before every answer
}

func (f *fakeBookStack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if f.down {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if r.Header.Get("Authorization") != "Token "+testToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	roles := map[int]Role{admin: {ID: admin, DisplayName: "Admin", SystemName: "admin"}, editor: {ID: editor, DisplayName: "Editor"}}
	var id int
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/users":
		var out []User
		for _, u := range f.users {
			if v := r.URL.Query().Get("filter[external_auth_id]"); v != "" && strings.EqualFold(u.ExternalAuthID, v) ||
				r.URL.Query().Get("filter[email]") == u.Email && u.Email != "" {
				out = append(out, User{ID: u.ID, Email: u.Email, ExternalAuthID: u.ExternalAuthID})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": out})
	case r.Method == "GET" && r.URL.Path == "/api/roles":
		json.NewEncoder(w).Encode(map[string]any{"data": []Role{{ID: admin}, {ID: editor}}})
	case r.Method == "GET" && scan(r.URL.Path, "/api/roles/%d", &id):
		json.NewEncoder(w).Encode(roles[id])
	case scan(r.URL.Path, "/api/users/%d", &id) && f.users[id] != nil:
		u := f.users[id]
		if r.Method == "PUT" {
			var in struct{ Roles []int }
			json.NewDecoder(r.Body).Decode(&in)
			f.calls[len(f.calls)-1] += fmt.Sprint(" ", in.Roles)
			if !f.stuck {
				u.Roles = nil
				for _, rid := range in.Roles {
					u.Roles = append(u.Roles, roles[rid])
				}
			}
		}
		json.NewEncoder(w).Encode(u)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeBookStack) puts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(f.calls), func(c string) bool { return !strings.HasPrefix(c, "PUT") })
}

func newReceiver(t *testing.T) (*Receiver, *fakeBookStack, *bytes.Buffer) {
	t.Helper()
	f := &fakeBookStack{users: map[int]*User{
		1: {ID: 1, Email: ServiceEmail, Roles: []Role{{ID: admin}}},
		2: {ID: 2, Email: "ann@example.test", ExternalAuthID: "user-ann", Roles: []Role{{ID: admin}, {ID: editor}}},
		3: {ID: 3, Email: "bob@example.test", ExternalAuthID: "user-bob", Roles: []Role{{ID: editor}}},
	}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	file := filepath.Join(t.TempDir(), "token")
	os.WriteFile(file, []byte(testToken), 0o600)
	var log bytes.Buffer
	return &Receiver{API: API{URL: srv.URL, TokenFile: file}, Secret: []byte(testSecret), AdminClaim: AdminClaim, Log: &log}, f, &log
}

// send posts a signed event, signed with key at the time at.
func send(rc *Receiver, kind, body string, key []byte, at time.Time) int {
	req := httptest.NewRequest("POST", DirectoryPath, strings.NewReader(body))
	if key != nil {
		h, _ := syncauth.Sign(key, at, kind, "evt-1", []byte(body))
		h.Apply(req)
	}
	w := httptest.NewRecorder()
	rc.ServeHTTP(w, req)
	return w.Code
}

func user(external string, active bool, roles ...string) string {
	var rs []map[string]string
	for _, r := range roles {
		rs = append(rs, map[string]string{"value": r})
	}
	b, _ := json.Marshal(map[string]any{"externalId": external, "active": active, "roles": rs, "userName": "secret-name"})
	return string(b)
}

// AC12: anything not KyIdentity's, fresh and small is refused before any BookStack call.
func TestReceiverRefusesBeforeBookStack(t *testing.T) {
	rc, f, _ := newReceiver(t)
	body := user("user-ann", false)
	now := time.Now()
	for name, c := range map[string]struct {
		body string
		key  []byte
		at   time.Time
		want int
	}{
		"no signature":  {body, nil, now, 401},
		"other key":     {body, []byte("another-signing-secret-of-32-byte"), now, 401},
		"stale":         {body, []byte(testSecret), now.Add(-10 * time.Minute), 401},
		"future":        {body, []byte(testSecret), now.Add(10 * time.Minute), 401},
		"over 64 KiB":   {strings.Repeat(" ", MaxBody) + body, []byte(testSecret), now, 413},
		"unknown event": {body, []byte(testSecret), now, 400},
	} {
		kind := "user.updated"
		if name == "unknown event" {
			kind = "group.deleted"
		}
		if got := send(rc, kind, c.body, c.key, c.at); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	// A body altered after signing.
	req := httptest.NewRequest("POST", DirectoryPath, strings.NewReader(user("user-bob", false)))
	h, _ := syncauth.Sign([]byte(testSecret), now, "user.updated", "evt-1", []byte(body))
	h.Apply(req)
	w := httptest.NewRecorder()
	rc.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("altered body: %d", w.Code)
	}
	if len(f.calls) != 0 {
		t.Fatalf("BookStack was called: %v", f.calls)
	}
	for _, c := range []struct{ method, path string }{{"GET", DirectoryPath}, {"POST", "/kyq/other"}, {"POST", "/"}} {
		w := httptest.NewRecorder()
		rc.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != 404 && w.Code != 405 {
			t.Errorf("%s %s: %d", c.method, c.path, w.Code)
		}
	}
}

// AC12: a removal strips every role, answering only after the read-back; it never deletes.
func TestReceiverRemoval(t *testing.T) {
	for _, kind := range []string{"user.updated", "user.deleted"} {
		rc, f, _ := newReceiver(t)
		if got := send(rc, kind, user("user-ann", false), []byte(testSecret), time.Now()); got != 204 {
			t.Fatalf("%s: %d", kind, got)
		}
		if p := f.puts(); !slices.Equal(p, []string{"PUT /api/users/2 []"}) || len(f.users[2].Roles) != 0 {
			t.Fatalf("%s: puts %v roles %v", kind, p, f.users[2].Roles)
		}
		if slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "DELETE") }) || f.calls[len(f.calls)-1] != "GET /api/users/2" {
			t.Fatalf("%s: calls %v", kind, f.calls)
		}
	}
	// A read-back that still shows a role is not delivered.
	rc, f, _ := newReceiver(t)
	f.stuck = true
	if got := send(rc, "user.updated", user("user-ann", false), []byte(testSecret), time.Now()); got != 503 {
		t.Fatalf("stuck update: %d", got)
	}
}

// AC12: no match is 404; anything but one exact match other than the service account is 409.
func TestReceiverMatching(t *testing.T) {
	for name, c := range map[string]struct {
		external string
		users    map[int]*User
		want     int
	}{
		"no match":        {"user-zed", nil, 404},
		"two matches":     {"user-ann", map[int]*User{9: {ID: 9, Email: "x@example.test", ExternalAuthID: "user-ann"}}, 409},
		"case only":       {"USER-ANN", nil, 409},
		"service account": {"svc", map[int]*User{1: {ID: 1, Email: ServiceEmail, ExternalAuthID: "svc", Roles: []Role{{ID: admin}}}}, 409},
		"empty":           {"", nil, 409},
	} {
		rc, f, _ := newReceiver(t)
		for id, u := range c.users {
			f.users[id] = u
		}
		if got := send(rc, "user.updated", user(c.external, false), []byte(testSecret), time.Now()); got != c.want || len(f.puts()) != 0 {
			t.Errorf("%s: %d, puts %v", name, got, f.puts())
		}
	}
	// No account is nothing to change for an active update or a creation.
	rc, f, _ := newReceiver(t)
	for _, kind := range []string{"user.updated", "user.created", "user.mfa_reset"} {
		if got := send(rc, kind, user("user-zed", true), []byte(testSecret), time.Now()); got != 204 {
			t.Errorf("%s of an unknown account: %d", kind, got)
		}
	}
	if len(f.puts()) != 0 {
		t.Fatal(f.puts())
	}
}

// AC12: an active update never adds a role; it removes Admin only without the admin claim.
func TestReceiverActiveUpdate(t *testing.T) {
	rc, f, _ := newReceiver(t)
	for _, claims := range [][]string{{AdminClaim}, {"BookStack.Admin", UserClaim}} {
		if got := send(rc, "user.updated", user("user-ann", true, claims...), []byte(testSecret), time.Now()); got != 204 || len(f.puts()) != 0 {
			t.Fatalf("%v: %d %v", claims, got, f.puts())
		}
	}
	// Bob holds Editor alone and claims the admin role: nothing is granted.
	if got := send(rc, "user.updated", user("user-bob", true, AdminClaim), []byte(testSecret), time.Now()); got != 204 || len(f.puts()) != 0 {
		t.Fatalf("grant: %d %v", got, f.puts())
	}
	if got := send(rc, "user.updated", user("user-bob", true, UserClaim), []byte(testSecret), time.Now()); got != 204 || len(f.puts()) != 0 {
		t.Fatalf("no admin to remove: %d %v", got, f.puts())
	}
	if got := send(rc, "user.updated", user("user-ann", true, UserClaim), []byte(testSecret), time.Now()); got != 204 || !slices.Equal(f.puts(), []string{"PUT /api/users/2 [2]"}) {
		t.Fatalf("demotion: %d %v", got, f.puts())
	}
	if !slices.Equal([]int{f.users[2].Roles[0].ID}, []int{editor}) || len(f.users[2].Roles) != 1 {
		t.Fatalf("roles after demotion %v", f.users[2].Roles)
	}
}

// AC12: BookStack down, or refusing the token, is 503 so KyIdentity retries; logs hold no
// token, secret or body.
func TestReceiverUnavailableAndLogs(t *testing.T) {
	rc, f, log := newReceiver(t)
	f.down = true
	if got := send(rc, "user.updated", user("user-ann", false), []byte(testSecret), time.Now()); got != 503 {
		t.Fatalf("down: %d", got)
	}
	f.down = false
	os.WriteFile(rc.API.TokenFile, []byte("BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB:OtherOtherOtherOtherOtherOtherOt"), 0o600)
	if got := send(rc, "user.updated", user("user-ann", false), []byte(testSecret), time.Now()); got != 503 {
		t.Fatalf("token refused: %d", got)
	}
	os.WriteFile(rc.API.TokenFile, []byte(testToken), 0o600)
	send(rc, "user.updated", user("user-ann", false), []byte(testSecret), time.Now())
	send(rc, "user.updated", user("user-ann", false), []byte("another-signing-secret-of-32-byte"), time.Now())
	out := log.String()
	for _, secret := range []string{"Secret", "Other", testSecret, "secret-name", "user-ann", "externalId"} {
		if strings.Contains(out, secret) {
			t.Errorf("log holds %q:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, `event="evt-1" type="user.updated" user=2 outcome="removed every role"`) || strings.Count(out, "\n") != 4 {
		t.Fatalf("log:\n%s", out)
	}
}

// MapRoles maps the admin role alone and refuses a role either claim would also grant.
func TestMapRoles(t *testing.T) {
	ctx := context.Background()
	roles := []Role{{ID: admin, SystemName: "admin", DisplayName: "Admin"}, {ID: editor, DisplayName: "Editor"}}
	var puts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id int
		switch {
		case r.URL.Path == "/api/roles":
			json.NewEncoder(w).Encode(map[string]any{"data": roles})
		case r.Method == "PUT" && scan(r.URL.Path, "/api/roles/%d", &id):
			var in Role
			json.NewDecoder(r.Body).Decode(&in)
			puts = append(puts, fmt.Sprint(id, " ", in.ExternalAuthID))
			roles[id-1].ExternalAuthID = in.ExternalAuthID
			w.Write([]byte("{}"))
		case scan(r.URL.Path, "/api/roles/%d", &id):
			json.NewEncoder(w).Encode(roles[id-1])
		}
	}))
	defer srv.Close()
	a := API{URL: srv.URL, Token: testToken}
	if err := MapRoles(ctx, a, AdminClaim, UserClaim, true); err == nil {
		t.Fatal("check passed before mapping")
	}
	if err := MapRoles(ctx, a, AdminClaim, UserClaim, false); err != nil || !slices.Equal(puts, []string{"1 bookstack.admin"}) {
		t.Fatalf("map: %v %v", err, puts)
	}
	if err := MapRoles(ctx, a, AdminClaim, UserClaim, false); err != nil || len(puts) != 1 {
		t.Fatalf("rerun: %v %v", err, puts)
	}
	for _, other := range []Role{{ID: editor, DisplayName: "Bookstack.User"}, {ID: editor, DisplayName: "Editor", ExternalAuthID: "staff, BookStack.Admin"}} {
		roles[1] = other
		if err := MapRoles(ctx, a, AdminClaim, UserClaim, true); err == nil {
			t.Errorf("%+v would also be granted", other)
		}
	}
}

func scan(path, format string, id *int) bool {
	_, err := fmt.Sscanf(path, format, id)
	return err == nil
}

// A slow BookStack is a 503 within the deadline, which KyIdentity retries, never a timeout it
// would hold as uncertain.
func TestReceiverDeadline(t *testing.T) {
	saved := Deadline
	Deadline = 100 * time.Millisecond
	defer func() { Deadline = saved }()
	rc, f, log := newReceiver(t)
	f.delay = 300 * time.Millisecond
	start := time.Now()
	if got := send(rc, "user.updated", user("user-ann", false), []byte(testSecret), time.Now()); got != 503 || time.Since(start) > 250*time.Millisecond {
		t.Fatalf("slow BookStack: %d after %s", got, time.Since(start))
	}
	if !strings.Contains(log.String(), "retry") {
		t.Fatalf("log: %s", log)
	}
}
