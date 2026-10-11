package bookstack

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func creationFixtureReport(spec CreationSpec) map[string]any {
	account := map[string]any{"contract": accountReportContract, "module_version": moduleVersion, "setup_id": spec.Account.SetupID, "intent_digest": intentDigest(spec.Account), "user_id": 3, "name": spec.Account.Name, "email": spec.Account.Email,
		"binding_digest": digest(Binding{Issuer: spec.Account.Issuer, Subject: spec.Account.Subject}), "created": true, "disabled": false, "administrator": false, "oidc_verified": false, "activation_pending": false, "admin_grant_pending": false}
	scope := creationScope(spec, 3)
	base := map[string]any{"module_version": moduleVersion, "setup_id": spec.Account.SetupID, "user_id": 3, "binding_digest": profileBindingDigest(scope.ProfileScope), "revision": 0, "disabled": false}
	profile, roles := make(map[string]any), make(map[string]any)
	for k, v := range base {
		profile[k] = v
		roles[k] = v
	}
	profile["contract"], profile["name"], profile["email"] = profileContract, spec.Account.Name, spec.Account.Email
	roles["contract"], roles["everyday_role"], roles["ordinary_role_ids"], roles["role_ids"], roles["administrator"], roles["administrator_role_id"], roles["policy_digest"] = roleContract, "editor", []int64{2}, []int64{2}, false, 1, strings.Repeat("ab", 32)
	return map[string]any{"contract": creationContract, "module_version": moduleVersion, "installation_id": spec.InstallationID, "intent_digest": creationIntent(spec), "account": account, "directory": map[string]any{"contract": directoryContract, "module_version": moduleVersion, "profile": profile, "roles": roles}}
}

func TestManagedCreationReportRequiresJoinedOwnership(t *testing.T) {
	spec := CreationSpec{InstallationID: testSpec.SetupID, Account: testSpec, EverydayRole: "editor", OrdinaryRoleIDs: []int64{2}}
	spec.Account.Administrator = false
	report := creationFixtureReport(spec)
	account := report["account"].(map[string]any)
	directory := report["directory"].(map[string]any)
	profile, roles := directory["profile"].(map[string]any), directory["roles"].(map[string]any)
	read := func() error {
		raw, _ := json.Marshal(report)
		_, err := readCreationReport(raw, spec, false)
		return err
	}
	if err := read(); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"installation_id", "intent_digest", "account", "directory"} {
		old := report[field]
		delete(report, field)
		if read() == nil {
			t.Fatal("missing joined creation proof accepted")
		}
		report[field] = old
	}
	roles["administrator"] = true
	roles["role_ids"] = []int64{1, 2}
	account["administrator"] = true
	if read() == nil {
		t.Fatal("administrator authority at creation accepted")
	}
	account["created"] = false
	if err := read(); err != nil {
		t.Fatal("separately projected retained authority refused", err)
	}
	profile["disabled"] = true
	if read() == nil {
		t.Fatal("inconsistent native lifecycle accepted")
	}
}
func TestManagedCreationInvalidIntentNeverCallsNative(t *testing.T) {
	calls := 0
	c, _ := New(func(context.Context, []string, []byte) ([]byte, error) { calls++; return nil, nil }, "/config", "/app/www/artisan")
	spec := CreationSpec{InstallationID: testSpec.SetupID, Account: testSpec, EverydayRole: "editor", OrdinaryRoleIDs: []int64{2}}
	if _, err := c.CreateManagedAccount(context.Background(), spec); err == nil {
		t.Fatal("administrator creation accepted")
	}
	spec.Account.Administrator = false
	for _, ids := range [][]int64{nil, {}, {0}, {2, 2}, {4, 2}} {
		spec.OrdinaryRoleIDs = ids
		if _, err := c.CreateManagedAccount(context.Background(), spec); err == nil {
			t.Fatal("invalid role policy accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid creation crossed native boundary")
	}
}
