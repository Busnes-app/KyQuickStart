package offboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBookStackDirectoryUsesOwnedAccountSetupAndHoldsLostWrites(t *testing.T) {
	b := fixtureBinding()
	b.Kind = "bookstack"
	accountSetup := "43f864f0-991c-43bf-8b94-d93c2916a98f"
	product, directory := strings.Repeat("a", 64), strings.Repeat("b", 64)
	profile := DirectoryProfile{Name: "Everyday", Email: "daily@example.test"}
	pr, rr, writes, calls := int64(0), int64(0), 0, 0
	lost := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/api/kyquickstart/offboard" {
			if r.Header.Get("Authorization") != "Bearer "+product {
				t.Error("wrong revocation authority")
			}
			sum := sha256.Sum256([]byte(b.Issuer + "\x00" + b.Subject))
			json.NewEncoder(w).Encode(map[string]any{"contract": "ky.bookstack-offboard/1", "module_version": "0.15.0", "user_id": b.NativeID, "binding_digest": hex.EncodeToString(sum[:]), "disabled": false, "revocation_complete": false, "tokens_remaining": 0})
			return
		}
		var req struct{ Profile, Roles map[string]any }
		if r.URL.Path != "/api/kyquickstart/directory" || r.Header.Get("Authorization") != "Bearer "+directory || json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("wrong directory authority")
			w.WriteHeader(400)
			return
		}
		for _, part := range []map[string]any{req.Profile, req.Roles} {
			if part["setup_id"] != accountSetup || part["client_id"] != "books-client" || part["subject"] != b.Subject || part["user_id"] != float64(b.NativeID) {
				t.Error("foreign native account scope")
				w.WriteHeader(400)
				return
			}
		}
		if req.Profile["action"] == "apply" {
			writes++
			if req.Profile["expected_revision"] != float64(pr) || req.Roles["expected_revision"] != float64(rr) {
				t.Error("stale native revisions")
				w.WriteHeader(400)
				return
			}
			name, email, admin := req.Profile["name"].(string), req.Profile["email"].(string), req.Roles["administrator"].(bool)
			if profile.Name != name || profile.Email != email {
				pr++
			}
			if profile.Administrator != admin {
				rr++
			}
			profile = DirectoryProfile{Name: name, Email: email, Administrator: admin}
			if lost {
				w.WriteHeader(503)
				return
			}
		}
		sum := sha256.Sum256([]byte(b.Issuer + "\x00books-client\x00" + b.Subject))
		common := func(contract string, revision int64) map[string]any {
			return map[string]any{"contract": contract, "module_version": "0.15.0", "setup_id": accountSetup, "user_id": b.NativeID, "binding_digest": hex.EncodeToString(sum[:]), "revision": revision, "disabled": false}
		}
		p, roles := common("ky.bookstack-profile/1", pr), common("ky.bookstack-roles/1", rr)
		p["name"], p["email"] = profile.Name, profile.Email
		ids := []int64{2}
		if profile.Administrator {
			ids = []int64{1, 2}
		}
		roles["administrator"], roles["everyday_role"], roles["ordinary_role_ids"], roles["role_ids"], roles["administrator_role_id"], roles["policy_digest"] = profile.Administrator, "editor", []int64{2}, ids, 1, strings.Repeat("ab", 32)
		json.NewEncoder(w).Encode(map[string]any{"contract": "ky.bookstack-directory/1", "module_version": "0.15.0", "profile": p, "roles": roles})
	}))
	defer server.Close()
	trustFixture(t, server)
	b.Origin = server.URL
	spec := TargetSpec{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, SetupID: b.SetupID, AdministratorRole: "admin", EverydayRole: "editor", BookStackDirectory: &BookStackDirectoryConfig{ClientID: "books-client", OrdinaryRoleIDs: []int64{9}, Accounts: map[string]BookStackAccount{b.Subject: {ID: b.NativeID, SetupID: accountSetup, OrdinaryRoleIDs: []int64{2}}}}}
	target, err := newTarget(spec, product, directory, "")
	if err != nil {
		t.Fatal(err)
	}
	// Caller changes must not alter the protected per-account policy.
	spec.BookStackDirectory.OrdinaryRoleIDs[0] = 8
	spec.BookStackDirectory.Accounts[b.Subject].OrdinaryRoleIDs[0] = 7
	state := stateDir(t)
	store := openStore(t, state)
	defer func() { store.Close() }()
	id, err := store.Register(context.Background(), target, b)
	if err != nil {
		t.Fatal("owned native scope did not register", err)
	}
	foreign := b
	foreign.Subject = "foreign"
	before := calls
	if _, err := store.Register(context.Background(), target, foreign); err == nil || calls != before {
		t.Fatal("foreign account reached native service")
	}
	desired := DirectoryProfile{Name: "Projected", Email: "projected@example.test", Administrator: true}
	if err := store.ApplyDirectory(context.Background(), target, id, desired, time.Now()); err != nil || profile != desired || writes != 1 {
		t.Fatal("directory projection failed", err)
	}
	if got, err := store.Binding(id); err != nil || got != b {
		t.Fatal("numeric binding was rewritten")
	}
	lost = true
	desired.Name = "Lost outcome"
	if err := store.ApplyDirectory(context.Background(), target, id, desired, time.Now()); !errors.Is(err, ErrDirectoryUncertain) {
		t.Fatal("unknown native write not held", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(state)
	if err != nil {
		t.Fatal(err)
	}
	before = calls
	if err := store.ApplyDirectory(context.Background(), target, id, desired, time.Now()); !errors.Is(err, ErrDirectoryUncertain) || calls != before || writes != 2 {
		t.Fatal("held write replayed")
	}
}

func TestBookStackAccountRolePoliciesAreExplicitAndCanonical(t *testing.T) {
	p := &BookStackDirectoryConfig{ClientID: "books-client", OrdinaryRoleIDs: []int64{2}, Accounts: map[string]BookStackAccount{"subject": {ID: 2, SetupID: "43f864f0-991c-43bf-8b94-d93c2916a98f"}}}
	for _, roles := range [][]int64{nil, {}, {3, 5}} {
		account := p.Accounts["subject"]
		account.OrdinaryRoleIDs = roles
		p.Accounts["subject"] = account
		if !validBookStackDirectory(p) {
			t.Fatal("valid per-account policy refused")
		}
	}
	for _, roles := range [][]int64{{0}, {2, 2}, {5, 3}, make([]int64, 33)} {
		account := p.Accounts["subject"]
		account.OrdinaryRoleIDs = roles
		p.Accounts["subject"] = account
		if validBookStackDirectory(p) {
			t.Fatal("invalid per-account policy accepted")
		}
	}
}
