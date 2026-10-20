package offboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// immichModule is a TLS fixture of the community module's access, directory, revoke and reactivate routes.
type immichModule struct {
	mu                  sync.Mutex
	b                   Binding
	revoked             bool
	epoch               *uint32
	name, email         string
	admin               bool
	revision            int64
	revokes, reactivate int
	reactivateStatus    int
	calls               int
}

func (m *immichModule) serve(t *testing.T) *httptest.Server {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.calls++
		if r.Header.Get("x-api-key") != "private-fixture-key" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		b, base := m.b, "/api/admin/users/"+m.b.NativeUUID
		profile := map[string]any{"name": m.name, "email": m.email, "administrator": m.admin}
		switch r.URL.Path {
		case "/api/server/version":
			json.NewEncoder(w).Encode(map[string]int{"major": 3, "minor": 2, "patch": 0})
		case "/api/system-config":
			json.NewEncoder(w).Encode(map[string]any{"oauth": map[string]any{"enabled": true, "allowInsecureRequests": false, "issuerUrl": b.Issuer, "clientId": b.ClientID}})
		case base:
			json.NewEncoder(w).Encode(map[string]any{"id": b.NativeUUID, "oauthId": b.Subject, "name": m.name, "email": m.email, "status": "active", "isAdmin": m.admin, "deletedAt": nil})
		case base + "/kyquickstart/access":
			state := map[string]any{"contract": "ky.immich-access/1", "userId": b.NativeUUID, "issuer": b.Issuer, "clientId": b.ClientID, "subject": b.Subject,
				"setupId": nil, "revokedAt": nil, "revoked": m.revoked, "apiKeys": 0, "sessions": 0, "reactivation": nil}
			if m.revoked {
				state["setupId"], state["revokedAt"] = b.SetupID, "2026-10-10T00:00:00Z"
			}
			if m.epoch != nil {
				state["reactivation"] = map[string]any{"epoch": *m.epoch, "reactivatedAt": "2026-10-11T00:00:00Z"}
			}
			json.NewEncoder(w).Encode(state)
		case base + "/kyquickstart/directory":
			if r.Method != http.MethodGet || r.URL.Query().Get("setupId") != b.SetupID {
				t.Error("unexpected directory write")
			}
			json.NewEncoder(w).Encode(map[string]any{"contract": "ky.immich-directory/1", "setupId": b.SetupID, "userId": b.NativeUUID, "issuer": b.Issuer, "clientId": b.ClientID,
				"subject": b.Subject, "revision": m.revision, "revoked": m.revoked, "profile": profile})
		case base + "/kyquickstart/revoke":
			m.revokes++
			m.revoked = true
			json.NewEncoder(w).Encode(map[string]any{})
		case base + "/kyquickstart/reactivate":
			var body struct {
				SetupID, Issuer, ClientID, Subject string
				Epoch                              uint32
				Profile                            struct {
					Name, Email   string
					Administrator bool
				}
			}
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body.SetupID != b.SetupID || body.Subject != b.Subject || body.ClientID != b.ClientID || body.Issuer != b.Issuer {
				t.Error("unscoped reactivation")
			}
			m.reactivate++
			if m.reactivateStatus == 409 {
				w.WriteHeader(409)
				return
			}
			// Roles and profile first, then the fence, in one native transaction.
			m.name, m.email, m.admin, m.revision = body.Profile.Name, body.Profile.Email, body.Profile.Administrator, m.revision+1
			m.revoked, m.epoch = false, &body.Epoch
			w.WriteHeader(m.reactivateStatus)
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Error("unexpected native route", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	trustFixture(t, server)
	m.b.Origin = server.URL
	return server
}

func immichDirectoryFixture(t *testing.T, status int) (*Store, *Receiver, *immichModule, string) {
	t.Helper()
	m := &immichModule{b: immichBinding(), name: "Later", email: "later@example.test", reactivateStatus: status}
	m.serve(t)
	b := m.b
	target, err := NewTarget(TargetSpec{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, SetupID: b.SetupID, ClientID: b.ClientID, AdministratorRole: "admin", EverydayRole: "user"}, "private-fixture-key")
	if err != nil || target.reactivate == nil {
		t.Fatal("Immich directory target has no native reactivation", err)
	}
	s := openStore(t, stateDir(t))
	t.Cleanup(func() { s.Close() })
	id, err := s.Register(context.Background(), target, b)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewReceiver(s, target, strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	if w := userRequest(r, "PATCH", id, deactivateBody); w.Code != 204 || !m.revoked {
		t.Fatal("revocation failed", w.Code)
	}
	return s, r, m, id
}

func TestImmichReactivationOpensEvenEpochsAndDelivers(t *testing.T) {
	s, r, m, id := immichDirectoryFixture(t, 200)
	put := kyIdentityPut(m.b.Subject, m.b.Subject, "Later", "later@example.test", "admin")
	if w := userRequest(r, "PUT", id, put); w.Code != 204 || m.reactivate != 1 || m.revoked || *m.epoch != 2 || !m.admin || !reportedActive(t, r, id) {
		t.Fatal("re-assignment did not reactivate natively", w.Code)
	}
	if a, err := s.epochAudit(id, 2, 1, "result"); err != nil || a.Outcome != "success" {
		t.Fatal("reactivation not audited", err)
	}
	if w := userRequest(r, "PUT", id, put); w.Code != 204 || m.reactivate != 1 {
		t.Fatal("repeated PUT replayed native reactivation", w.Code)
	}
	if w := userRequest(r, "PATCH", id, deactivateBody); w.Code != 204 || m.revokes != 2 || !m.revoked || reportedActive(t, r, id) {
		t.Fatal("revocation after reactivation failed", w.Code)
	}
	put = kyIdentityPut(m.b.Subject, m.b.Subject, "Later", "later@example.test", "user")
	if w := userRequest(r, "PUT", id, put); w.Code != 204 || m.reactivate != 2 || *m.epoch != 4 || m.admin {
		t.Fatal("second reactivation did not follow the PUT's roles", w.Code)
	}
}

func TestImmichReactivationHoldsLostResponseAndRefusesConflict(t *testing.T) {
	t.Run("lost-response", func(t *testing.T) {
		s, r, m, id := immichDirectoryFixture(t, 503)
		put := kyIdentityPut(m.b.Subject, m.b.Subject, "Later", "later@example.test", "user")
		if w := userRequest(r, "PUT", id, put); w.Code != 429 || m.reactivate != 1 || reportedActive(t, r, id) {
			t.Fatal("uncertain reactivation was reported done", w.Code)
		}
		j, err := s.epochJob(id, 2)
		if a, auditErr := s.epochAudit(id, 2, 1, "result"); err != nil || auditErr != nil || j.Status != "pending" || a.Outcome != "failed" {
			t.Fatal("uncertain reactivation not held for retry", j.Status)
		}
		if err := s.Deliver(context.Background(), r.target, id, j.Next); err != nil || m.reactivate != 1 || !reportedActive(t, r, id) {
			t.Fatal("held reactivation did not reconcile without another write", err, m.reactivate)
		}
	})
	t.Run("refused", func(t *testing.T) {
		s, r, m, id := immichDirectoryFixture(t, 409)
		put := kyIdentityPut(m.b.Subject, m.b.Subject, "Later", "later@example.test", "user")
		if w := userRequest(r, "PUT", id, put); w.Code != 409 || m.reactivate != 1 || reportedActive(t, r, id) {
			t.Fatal("native refusal not reported", w.Code)
		}
		if a, err := s.epochAudit(id, 2, 1, "result"); err != nil || a.Outcome != "native_refused" {
			t.Fatal("native refusal not audited", err)
		}
		if j, _, _ := s.Inspect(id); j.Status != "refused" {
			t.Fatal("refusal left the epoch due for the worker", j.Status)
		}
		// Only another delivery (a PUT) is another audited attempt.
		if err := s.Deliver(context.Background(), r.target, id, time.Now()); !errors.Is(err, ErrNativeRefused) || m.reactivate != 2 {
			t.Fatal("refusal not repeated as a refusal", err)
		}
	})
}

func TestOnlyImmichDirectoryTargetsReactivate(t *testing.T) {
	b := immichBinding()
	for _, test := range []struct {
		spec TargetSpec
		want bool
	}{
		{TargetSpec{App: b.App, Kind: "immich", Origin: b.Origin, Issuer: b.Issuer, SetupID: b.SetupID, ClientID: b.ClientID, AdministratorRole: "admin", EverydayRole: "user"}, true},
		{TargetSpec{App: b.App, Kind: "immich", Origin: b.Origin, Issuer: b.Issuer, SetupID: b.SetupID, ClientID: b.ClientID}, false},
		{TargetSpec{App: "code", Kind: "forgejo", Origin: b.Origin, Issuer: b.Issuer, SourceID: 1, AdministratorRole: "admin", EverydayRole: "user"}, false},
		{TargetSpec{App: "wiki", Kind: "bookstack", Origin: b.Origin, Issuer: b.Issuer}, false},
	} {
		target, err := NewTarget(test.spec, strings.Repeat("ab", 32))
		if err != nil || (target.reactivate != nil) != test.want {
			t.Fatal("native reactivation wiring", test.spec.Kind, err)
		}
	}
}

// KyIdentity 0.4.4 delivers an inactive update (restore replay included) as an optional
// GET of the linked ID or an externalId lookup, then PATCH active=false; no User PUT.
func TestImmichDirectoryAcceptsKyIdentityInactiveReplay(t *testing.T) {
	s, r, m, id := immichDirectoryFixture(t, 200)
	lookup := func() {
		t.Helper()
		query := url.Values{"filter": {`externalId eq "` + m.b.Subject + `"`}, "startIndex": {"1"}, "count": {"2"}}
		if w := groupRequest(r, "GET", r.base+"/Users?"+query.Encode(), ""); w.Code != 200 {
			t.Fatal("externalId lookup refused", w.Code)
		}
		var resource struct{ ID, ExternalID string }
		if w := userRequest(r, "GET", id, ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &resource) != nil || resource.ID != id || resource.ExternalID != m.b.Subject {
			t.Fatal("linked read does not verify the mapping", w.Code)
		}
	}
	// Replay over a completed revocation is idempotent.
	lookup()
	if w := userRequest(r, "PATCH", id, deactivateBody); w.Code != 204 || m.revokes != 1 {
		t.Fatal("replayed deactivation refused", w.Code)
	}
	// Replay over a restored active account revokes it.
	if userRequest(r, "PUT", id, kyIdentityPut(m.b.Subject, m.b.Subject, "Later", "later@example.test", "user")).Code != 204 {
		t.Fatal("reactivation failed")
	}
	lookup()
	if w := userRequest(r, "PATCH", id, deactivateBody); w.Code != 204 || m.revokes != 2 || !m.revoked || reportedActive(t, r, id) {
		t.Fatal("replayed deactivation did not revoke", w.Code)
	}
	if j, epoch, _, err := s.latest(id); err != nil || epoch != 3 || j.Status != "complete" {
		t.Fatal("replay did not open the next revocation epoch", err)
	}
}
