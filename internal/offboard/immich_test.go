package offboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func immichBinding() Binding {
	b := fixtureBinding()
	b.App, b.Kind, b.NativeID = "photos", "immich", 0
	b.NativeUUID, b.ClientID = "271333e2-c221-4601-b249-563b3d828530", "photos-client"
	return b
}

func TestDisjointNativeBindingIdentities(t *testing.T) {
	valid := immichBinding()
	if validate(valid) != nil || validate(fixtureBinding()) != nil {
		t.Fatal("valid UUID or legacy numeric binding refused")
	}
	for _, change := range []func(*Binding){
		func(b *Binding) { b.NativeID = 1 }, func(b *Binding) { b.NativeUUID = "" },
		func(b *Binding) { b.NativeUUID = "../foreign" }, func(b *Binding) { b.ClientID = "" },
		func(b *Binding) { b.SourceID = 1 }, func(b *Binding) { b.Username = "foreign" },
		func(b *Binding) { b.Kind = "bookstack" }, func(b *Binding) { b.Kind = "forgejo" },
	} {
		b := valid
		change(&b)
		if validate(b) == nil {
			t.Fatal("mixed/foreign native identity accepted")
		}
	}
	legacy := fixtureBinding()
	legacy.NativeUUID = valid.NativeUUID
	if validate(legacy) == nil {
		t.Fatal("UUID authorized a numeric product")
	}
	legacy = fixtureBinding()
	legacy.ClientID = valid.ClientID
	if validate(legacy) == nil {
		t.Fatal("client pin accepted for a numeric product")
	}
	path := stateDir(t)
	s := openStore(t, path)
	id, err := s.Bind(valid)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openStore(t, path)
	defer s.Close()
	if got, err := s.Binding(id); err != nil || got != valid {
		t.Fatal("UUID receipt not retained across restart", err)
	}
	for _, change := range []func(*Binding){func(b *Binding) { b.NativeUUID = "371333e2-c221-4601-b249-563b3d828530" }, func(b *Binding) { b.ClientID = "foreign-client" }} {
		b := valid
		change(&b)
		if _, err := s.Bind(b); !errors.Is(err, ErrState) {
			t.Fatal("foreign UUID/client replaced a retained receipt")
		}
	}
}

func TestImmichTargetScopeAndNativeDispatch(t *testing.T) {
	b := immichBinding()
	calls, writes, revoked := 0, 0, false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("x-api-key") != "private-fixture-key" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/server/version":
			json.NewEncoder(w).Encode(map[string]int{"major": 3, "minor": 2, "patch": 0})
		case "/api/system-config":
			json.NewEncoder(w).Encode(map[string]any{"oauth": map[string]any{"enabled": true, "allowInsecureRequests": false, "issuerUrl": b.Issuer, "clientId": b.ClientID}})
		case "/api/admin/users/" + b.NativeUUID:
			json.NewEncoder(w).Encode(map[string]any{"id": b.NativeUUID, "oauthId": b.Subject, "name": "Everyday", "email": "daily@example.test", "status": "active", "isAdmin": false, "deletedAt": nil})
		case "/api/admin/users/" + b.NativeUUID + "/kyquickstart/revoke":
			var body map[string]string
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 4 || body["setupId"] != b.SetupID || body["issuer"] != b.Issuer || body["clientId"] != b.ClientID || body["subject"] != b.Subject {
				t.Error("bridge mutated an unowned native scope")
			}
			writes++
			revoked = true
			json.NewEncoder(w).Encode(map[string]any{})
		case "/api/admin/users/" + b.NativeUUID + "/kyquickstart/access":
			state := map[string]any{"contract": "ky.immich-access/1", "userId": b.NativeUUID, "issuer": b.Issuer, "clientId": b.ClientID, "subject": b.Subject, "setupId": nil, "revokedAt": nil, "revoked": revoked, "apiKeys": 0, "sessions": 0}
			if revoked {
				state["setupId"], state["revokedAt"] = b.SetupID, "2026-10-10T00:00:00Z"
			}
			json.NewEncoder(w).Encode(state)
		default:
			t.Error("unexpected native route")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	trustFixture(t, server)
	b.Origin = server.URL
	spec := TargetSpec{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, SetupID: b.SetupID, ClientID: b.ClientID}
	target, err := NewTarget(spec, "private-fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	s := openStore(t, stateDir(t))
	defer s.Close()
	wrong := b
	wrong.ClientID = "foreign"
	if _, err := s.Register(context.Background(), target, wrong); err == nil || calls != 0 {
		t.Fatal("foreign client authorized native I/O")
	}
	id, err := s.Register(context.Background(), target, b)
	if err != nil {
		t.Fatal("verified native binding refused", err)
	}
	if _, err := s.Request(id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if s.Deliver(context.Background(), target, id, time.Now()) != nil || writes != 1 || !revoked {
		t.Fatal("native revocation dispatch failed")
	}
	if s.Deliver(context.Background(), target, id, time.Now()) != nil || writes != 1 {
		t.Fatal("completed bridge job replayed a native mutation")
	}
}
