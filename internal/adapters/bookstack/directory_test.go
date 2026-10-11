package bookstack

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDirectoryRequiresBothOwnedProofsAndOneLifecycle(t *testing.T) {
	scope := RoleScope{ProfileScope: ProfileScope{Binding: Binding{ID: 3, Issuer: testSpec.Issuer, Subject: testSpec.Subject}, SetupID: testSpec.SetupID, ClientID: testSpec.ClientID}, EverydayRole: "editor", OrdinaryRoleIDs: []int64{2}}
	profile := map[string]any{"contract": profileContract, "module_version": moduleVersion, "setup_id": scope.SetupID,
		"user_id": scope.ID, "binding_digest": profileBindingDigest(scope.ProfileScope), "name": "Owned", "email": "owned@example.test", "revision": 1, "disabled": false}
	roles := map[string]any{"contract": roleContract, "module_version": moduleVersion, "setup_id": scope.SetupID,
		"user_id": scope.ID, "binding_digest": profileBindingDigest(scope.ProfileScope), "revision": 2, "disabled": false,
		"administrator": false, "everyday_role": "editor", "ordinary_role_ids": []int64{2}, "role_ids": []int64{2},
		"administrator_role_id": 1, "policy_digest": strings.Repeat("ab", 32)}
	var wire []byte
	c, _ := New(func(context.Context, []string, []byte) ([]byte, error) { return wire, nil }, "/config", "/app/www/artisan")
	report := map[string]any{"contract": directoryContract, "module_version": moduleVersion, "profile": profile, "roles": roles}
	wire, _ = json.Marshal(report)
	if _, err := c.InspectDirectory(context.Background(), scope); err != nil {
		t.Fatal("valid directory proof refused", err)
	}
	for _, bad := range []struct {
		key   string
		value any
	}{{"profile", nil}, {"roles", nil}, {"module_version", "foreign"}, {"contract", roleContract}} {
		previous := report[bad.key]
		report[bad.key] = bad.value
		wire, _ = json.Marshal(report)
		if _, err := c.InspectDirectory(context.Background(), scope); err == nil {
			t.Fatal("incomplete directory proof accepted", bad.key)
		}
		report[bad.key] = previous
	}
	roles["disabled"] = true
	wire, _ = json.Marshal(report)
	if _, err := c.InspectDirectory(context.Background(), scope); err == nil {
		t.Fatal("contradictory lifecycle accepted")
	}
	profile["disabled"] = true
	wire, _ = json.Marshal(report)
	if _, err := c.ApplyDirectory(context.Background(), scope, Profile{"Changed", "changed@example.test"}, true); err == nil {
		t.Fatal("revoked directory projected")
	}
	roles["binding_digest"] = digest(scope.Binding)
	wire, _ = json.Marshal(report)
	if _, err := c.InspectDirectory(context.Background(), scope); err == nil {
		t.Fatal("foreign role proof combined with owned profile")
	}
}
