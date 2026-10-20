package immich

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// reactivationModule is a TLS fixture of the module's access, directory and reactivate routes.
type reactivationModule struct {
	mu         sync.Mutex
	b          Binding
	setupID    string
	revoked    bool
	epoch      *uint32
	profile    Profile
	revision   int64
	sessions   int64
	posts      int
	directory  int
	legacy     bool // a module without the reactivation receipt
	status     int  // reply status for a committed POST
	refuse     bool // 409 without committing
	applyAdmin *bool
}

func (m *reactivationModule) serve(t *testing.T) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if r.Header.Get("x-api-key") != "private-fixture-key" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		base := "/api/admin/users/" + m.b.ID
		switch r.URL.Path {
		case "/api/server/version":
			json.NewEncoder(w).Encode(map[string]int{"major": 3, "minor": 2, "patch": 0})
		case "/api/system-config":
			json.NewEncoder(w).Encode(map[string]any{"oauth": map[string]any{"enabled": true, "allowInsecureRequests": false, "issuerUrl": m.b.Issuer, "clientId": m.b.ClientID}})
		case base:
			json.NewEncoder(w).Encode(map[string]any{"id": m.b.ID, "oauthId": m.b.Subject, "name": m.profile.Name, "email": m.profile.Email, "status": "active", "isAdmin": m.profile.Administrator, "deletedAt": nil})
		case base + "/kyquickstart/access":
			state := map[string]any{"contract": accessContract, "userId": m.b.ID, "issuer": m.b.Issuer, "clientId": m.b.ClientID, "subject": m.b.Subject,
				"setupId": nil, "revokedAt": nil, "revoked": m.revoked, "apiKeys": 0, "sessions": m.sessions, "reactivation": nil}
			if m.revoked {
				state["setupId"], state["revokedAt"] = m.setupID, "2026-10-09T00:00:00Z"
			}
			if m.epoch != nil {
				state["reactivation"] = map[string]any{"epoch": *m.epoch, "reactivatedAt": "2026-10-10T00:00:00.000Z"}
			}
			if m.legacy {
				delete(state, "reactivation")
			}
			json.NewEncoder(w).Encode(state)
		case base + "/kyquickstart/directory":
			if r.Method == http.MethodPost {
				var body struct{ Profile Profile }
				json.NewDecoder(r.Body).Decode(&body)
				m.directory++
				m.profile, m.revision = body.Profile, m.revision+1
				json.NewEncoder(w).Encode(map[string]any{})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"contract": directoryContract, "setupId": m.setupID, "userId": m.b.ID, "issuer": m.b.Issuer, "clientId": m.b.ClientID, "subject": m.b.Subject,
				"revision": m.revision, "revoked": m.revoked, "profile": m.profile})
		case base + "/kyquickstart/reactivate":
			var body struct {
				SetupID, Issuer, ClientID, Subject string
				Epoch                              uint32
				Profile                            Profile
			}
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body.SetupID != m.setupID || body.Issuer != m.b.Issuer || body.ClientID != m.b.ClientID || body.Subject != m.b.Subject {
				t.Error("unscoped reactivation body")
			}
			m.posts++
			if m.refuse || !m.revoked {
				w.WriteHeader(409)
				return
			}
			m.revoked, m.epoch, m.profile, m.revision = false, &body.Epoch, body.Profile, m.revision+1
			if m.applyAdmin != nil {
				m.profile.Administrator = *m.applyAdmin
			}
			w.WriteHeader(m.status)
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Error("unexpected native route", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

func TestReactivateVerifiesFenceEpochAndProfile(t *testing.T) {
	wanted := Profile{Name: "Later", Email: "later@example.test", Administrator: true}
	for _, mode := range []string{"success", "repeat", "lost-response", "refused", "never-revoked", "legacy-module", "role-mismatch", "session-restored", "later-profile", "invalid-epoch"} {
		t.Run(mode, func(t *testing.T) {
			m := &reactivationModule{b: Binding{ID: "271333e2-c221-4601-b249-563b3d828530", Issuer: "https://identity.example.test", ClientID: "photos", Subject: "owned-subject"},
				setupID: "371333e2-c221-4601-b249-563b3d828530", revoked: true, profile: Profile{Name: "Before", Email: "later@example.test"}, status: 200}
			epoch := uint32(2)
			switch mode {
			case "lost-response":
				m.status = 503
			case "refused":
				m.refuse = true
			case "never-revoked":
				m.revoked = false
			case "legacy-module":
				m.legacy = true
			case "role-mismatch":
				m.applyAdmin = new(bool)
			case "session-restored":
				m.sessions = 1
			case "invalid-epoch":
				epoch = 3
			}
			server := m.serve(t)
			defer server.Close()
			c, err := New(server.URL, "private-fixture-key")
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = server.Client().Transport
			ctx := context.Background()
			err = c.Reactivate(ctx, m.b, m.setupID, epoch, wanted)
			switch mode {
			case "success":
				if err != nil || m.posts != 1 || m.revoked || *m.epoch != 2 || m.profile != wanted {
					t.Fatal("reactivation not verified", err, m.posts)
				}
			case "repeat":
				if err != nil || c.Reactivate(ctx, m.b, m.setupID, epoch, wanted) != nil || m.posts != 1 || m.directory != 0 {
					t.Fatal("repeated reactivation was not idempotent", err, m.posts)
				}
				if !errors.Is(c.Reactivate(ctx, m.b, m.setupID, 4, wanted), ErrRefused) || m.posts != 1 {
					t.Fatal("reactivation under another epoch accepted without revocation")
				}
			case "lost-response":
				if !errors.Is(err, ErrUncertain) || m.posts != 1 {
					t.Fatal("lost response was not held", err)
				}
				// The retried epoch verifies the committed write instead of repeating it.
				if err := c.Reactivate(ctx, m.b, m.setupID, epoch, wanted); err != nil || m.posts != 1 {
					t.Fatal("held reactivation did not reconcile", err, m.posts)
				}
			case "refused", "never-revoked", "legacy-module":
				if !errors.Is(err, ErrRefused) || m.epoch != nil {
					t.Fatal("definite refusal not reported", err)
				}
				if mode != "refused" && m.posts != 0 {
					t.Fatal("refused reactivation reached the module")
				}
			case "role-mismatch", "session-restored":
				if !errors.Is(err, ErrUncertain) || m.posts != 1 {
					t.Fatal("unverified readback accepted", err)
				}
			case "later-profile":
				later := Profile{Name: "Renamed", Email: "later@example.test"}
				if err != nil || c.Reactivate(ctx, m.b, m.setupID, epoch, later) != nil || m.posts != 1 || m.directory != 1 || m.profile != later {
					t.Fatal("committed epoch did not converge to the later profile", err, m.posts, m.directory)
				}
			case "invalid-epoch":
				if err == nil || m.posts != 0 {
					t.Fatal("odd epoch reached the module")
				}
			}
		})
	}
}

func TestInternalOriginIsAComposeServiceName(t *testing.T) {
	for origin, want := range map[string]bool{
		"http://immich-server:2283": true, "https://immich-server": true, "http://immich-server": true,
		"http://immich-server.lan:2283": false, "http://127.0.0.1:2283": false, "http://[::1]:2283": false, "http://localhost:2283": false,
		"http://Immich-server:2283": false, "http://immich-server:2283/api": false, "http://immich-server:2283/": false, "http://immich-server:2283?x=1": false,
		"http://user@immich-server:2283": false, "http://immich-server:0": false, "http://immich-server:65536": false, "http://immich-server:02283": false,
		"http://immich-server:": false, "ftp://immich-server": false, "http://-immich:2283": false, "http://1234:2283": false, "http://immich-server:2283#x": false,
	} {
		if InternalOrigin(origin) != want {
			t.Error("internal origin", origin)
		}
		if _, err := NewInternal(origin, "private-fixture-key"); (err == nil) != want {
			t.Error("internal client", origin)
		}
	}
	if _, err := NewInternal("http://immich-server:2283", "bad key"); err == nil {
		t.Fatal("whitespace key accepted")
	}
	c, err := NewInternal("http://immich-server:2283", "private-fixture-key")
	if err != nil || c.http.Transport.(*http.Transport).Proxy != nil || c.http.CheckRedirect(nil, nil) == nil {
		t.Fatal("internal client allows a proxy or redirects")
	}
}
