package bookstack

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRoleInspectionRequiresExactNativeAuthority(t *testing.T) {
	scope := RoleScope{ProfileScope: ProfileScope{Binding: Binding{ID: 3, Issuer: testSpec.Issuer, Subject: testSpec.Subject}, SetupID: testSpec.SetupID, ClientID: testSpec.ClientID}, EverydayRole: "editor", OrdinaryRoleIDs: []int64{2, 4}}
	base := map[string]any{"contract": roleContract, "module_version": moduleVersion, "setup_id": scope.SetupID, "user_id": scope.ID,
		"binding_digest": profileBindingDigest(scope.ProfileScope), "revision": 1, "administrator": false, "disabled": false,
		"everyday_role": "editor", "ordinary_role_ids": []int64{2, 4}, "role_ids": []int64{2, 4}, "administrator_role_id": 1, "policy_digest": strings.Repeat("ab", 32)}
	var wire []byte
	c, _ := New(func(context.Context, []string, []byte) ([]byte, error) { return wire, nil }, "/config", "/app/www/artisan")
	wire, _ = json.Marshal(base)
	state, err := c.InspectRoles(context.Background(), scope)
	if err != nil || state.Administrator || state.Disabled || state.Revision != 1 || state.AdministratorRoleID != 1 {
		t.Fatal("valid owned role state refused", err)
	}
	for _, field := range []string{"contract", "module_version", "setup_id", "user_id", "binding_digest", "revision", "administrator", "disabled", "everyday_role", "ordinary_role_ids", "role_ids", "administrator_role_id", "policy_digest"} {
		t.Run(field, func(t *testing.T) {
			copy := make(map[string]any)
			for k, v := range base {
				copy[k] = v
			}
			delete(copy, field)
			wire, _ = json.Marshal(copy)
			if _, err := c.InspectRoles(context.Background(), scope); err == nil {
				t.Fatal("missing native authority proof accepted")
			}
		})
	}
	for _, bad := range []struct {
		field string
		value any
	}{
		{"binding_digest", digest(scope.Binding)}, {"administrator", true}, {"role_ids", []int64{1, 2, 4}},
		{"ordinary_role_ids", []int64{4, 2}}, {"administrator_role_id", 2}, {"policy_digest", "wrong"},
		{"policy_digest", strings.Repeat("AB", 32)}, {"revision", -1}, {"revision", 1.5}, {"revision", maxProfileRevision + 2},
	} {
		t.Run(bad.field+"-invalid", func(t *testing.T) {
			copy := make(map[string]any)
			for k, v := range base {
				copy[k] = v
			}
			copy[bad.field] = bad.value
			wire, _ = json.Marshal(copy)
			if _, err := c.InspectRoles(context.Background(), scope); err == nil {
				t.Fatal("foreign or contradictory native authority accepted")
			}
		})
	}
	base["disabled"] = true
	wire, _ = json.Marshal(base)
	if _, err := c.ApplyRoles(context.Background(), scope, true); err == nil {
		t.Fatal("revoked native role projected")
	}
}

func TestRoleScopeRefusesBeforeNativeCommand(t *testing.T) {
	calls := 0
	c, _ := New(func(context.Context, []string, []byte) ([]byte, error) { calls++; return nil, nil }, "/config", "/app/www/artisan")
	scope := RoleScope{ProfileScope: ProfileScope{Binding: Binding{ID: 3, Issuer: testSpec.Issuer, Subject: testSpec.Subject}, SetupID: testSpec.SetupID, ClientID: testSpec.ClientID}, EverydayRole: "editor"}
	for _, ids := range [][]int64{nil, {0}, {2, 2}, {4, 2}} {
		scope.OrdinaryRoleIDs = ids
		if _, err := c.InitializeRoles(context.Background(), scope, false); err == nil {
			t.Fatal("invalid ordinary role policy accepted")
		}
	}
	scope.OrdinaryRoleIDs = []int64{2}
	scope.EverydayRole = "admin"
	if _, err := c.InitializeRoles(context.Background(), scope, false); err == nil {
		t.Fatal("overlapping role aliases accepted")
	}
	if calls != 0 {
		t.Fatal("invalid role policy crossed native command boundary")
	}
}

func TestEmptyOrdinaryPolicyRequiresExplicitNativeProof(t *testing.T) {
	scope := RoleScope{ProfileScope: ProfileScope{Binding: Binding{ID: 3, Issuer: testSpec.Issuer, Subject: testSpec.Subject}, SetupID: testSpec.SetupID, ClientID: testSpec.ClientID}, EverydayRole: "editor", OrdinaryRoleIDs: []int64{}}
	base := map[string]any{"contract": roleContract, "module_version": moduleVersion, "setup_id": scope.SetupID, "user_id": scope.ID,
		"binding_digest": profileBindingDigest(scope.ProfileScope), "revision": 0, "administrator": false, "disabled": false,
		"everyday_role": "editor", "ordinary_role_ids": []int64{}, "role_ids": []int64{}, "administrator_role_id": 1, "policy_digest": strings.Repeat("ab", 32)}
	var wire []byte
	c, _ := New(func(context.Context, []string, []byte) ([]byte, error) { return wire, nil }, "/config", "/app/www/artisan")
	wire, _ = json.Marshal(base)
	if _, err := c.InspectRoles(context.Background(), scope); err != nil {
		t.Fatal("explicit empty policy refused", err)
	}
	base["administrator"], base["role_ids"] = true, []int64{1}
	wire, _ = json.Marshal(base)
	if _, err := c.InitializeRoles(context.Background(), scope, true); err != nil {
		t.Fatal("administrator-only capture refused", err)
	}
	for _, field := range []string{"ordinary_role_ids", "role_ids"} {
		copy := make(map[string]any)
		for k, v := range base {
			copy[k] = v
		}
		delete(copy, field)
		wire, _ = json.Marshal(copy)
		if _, err := c.InspectRoles(context.Background(), scope); err == nil {
			t.Fatal("missing policy accepted")
		}
		copy[field] = nil
		wire, _ = json.Marshal(copy)
		if _, err := c.InspectRoles(context.Background(), scope); err == nil {
			t.Fatal("null policy accepted")
		}
	}
}
