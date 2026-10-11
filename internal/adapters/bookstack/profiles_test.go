package bookstack

import (
	"context"
	"encoding/json"
	"testing"
)

func TestProfileInspectionRequiresExactProof(t *testing.T) {
	scope := ProfileScope{Binding: Binding{ID: 3, Issuer: testSpec.Issuer, Subject: testSpec.Subject}, SetupID: testSpec.SetupID, ClientID: testSpec.ClientID}
	base := map[string]any{"contract": profileContract, "module_version": moduleVersion, "setup_id": scope.SetupID,
		"user_id": scope.ID, "binding_digest": profileBindingDigest(scope), "name": "Owned 雪", "email": "owned@example.test", "revision": 1, "disabled": false}
	var wire []byte
	c, _ := New(func(context.Context, []string, []byte) ([]byte, error) { return wire, nil }, "/config", "/app/www/artisan")
	wire, _ = json.Marshal(base)
	state, err := c.InspectProfile(context.Background(), scope)
	if err != nil || state.Profile != (Profile{"Owned 雪", "owned@example.test"}) || state.Revision != 1 || state.Disabled {
		t.Fatal("valid profile inspection refused", err)
	}
	for _, field := range []string{"contract", "module_version", "setup_id", "user_id", "binding_digest", "name", "email", "revision", "disabled"} {
		t.Run(field, func(t *testing.T) {
			copy := make(map[string]any)
			for k, v := range base {
				copy[k] = v
			}
			delete(copy, field)
			wire, _ = json.Marshal(copy)
			if _, err := c.InspectProfile(context.Background(), scope); err == nil {
				t.Fatal("missing profile proof accepted")
			}
		})
	}
	for _, bad := range []struct {
		field string
		value any
	}{
		{"user_id", 4}, {"setup_id", "foreign"}, {"binding_digest", digest(scope.Binding)},
		{"revision", -1}, {"revision", 1.5}, {"revision", maxProfileRevision + 2}, {"name", "unsafe\nprofile"}, {"email", "malformed"},
	} {
		t.Run(bad.field+"-invalid", func(t *testing.T) {
			copy := make(map[string]any)
			for k, v := range base {
				copy[k] = v
			}
			copy[bad.field] = bad.value
			wire, _ = json.Marshal(copy)
			if _, err := c.InspectProfile(context.Background(), scope); err == nil {
				t.Fatal("foreign or malformed profile proof accepted")
			}
		})
	}
	base["disabled"] = true
	wire, _ = json.Marshal(base)
	if _, err := c.ApplyProfile(context.Background(), scope, Profile{"Changed", "changed@example.test"}); err == nil {
		t.Fatal("revoked profile projected")
	}
}

func TestProfileBoundaryRefusesBeforeCommand(t *testing.T) {
	calls := 0
	c, _ := New(func(context.Context, []string, []byte) ([]byte, error) { calls++; return nil, nil }, "/config", "/app/www/artisan")
	scope := ProfileScope{Binding: Binding{ID: 3, Issuer: testSpec.Issuer, Subject: testSpec.Subject}, SetupID: testSpec.SetupID, ClientID: testSpec.ClientID}
	if _, err := c.ApplyProfile(context.Background(), scope, Profile{"Unsafe\n", "user@example.test"}); err == nil {
		t.Fatal("invalid profile accepted")
	}
	scope.ID = 0
	if _, err := c.InitializeProfile(context.Background(), scope, Profile{"Name", "user@example.test"}); err == nil {
		t.Fatal("unbound profile initialized")
	}
	if calls != 0 {
		t.Fatal("invalid profile crossed native boundary")
	}
}
