package bookstack

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

var testSpec = AccountSpec{SetupID: "2c676879-9c71-4913-b8a7-995521d9ed20", Issuer: "https://identity.example.test",
	ClientID: "fixture-client", Subject: "setup-owner-subject", Name: "Owner", Email: "owner@example.test", Administrator: true}

func TestAccountCannotMutateBeforeSourceQualification(t *testing.T) {
	var actions []string
	c, _ := New(func(_ context.Context, _ []string, input []byte) ([]byte, error) {
		var request struct {
			Action string `json:"action"`
		}
		json.Unmarshal(input, &request)
		actions = append(actions, request.Action)
		return []byte(`{"contract":"ky.bookstack-account-source/1","module_version":"unsupported","source_digest":"wrong"}`), nil
	}, "/config", "/app/www/artisan")
	if _, err := c.Apply(context.Background(), testSpec); err == nil || !reflect.DeepEqual(actions, []string{"probe"}) {
		t.Fatal("unqualified source reached account creation")
	}
}

func TestAccountInspectionRejectsUnprovedAuthority(t *testing.T) {
	base := map[string]any{
		"contract": accountReportContract, "name": testSpec.Name, "email": testSpec.Email, "module_version": moduleVersion, "setup_id": testSpec.SetupID,
		"intent_digest": intentDigest(testSpec), "user_id": 3, "binding_digest": digest(Binding{Issuer: testSpec.Issuer, Subject: testSpec.Subject}),
		"created": false, "disabled": false, "administrator": false, "oidc_verified": false, "activation_pending": true, "admin_grant_pending": true,
	}
	for _, field := range []string{"name", "email", "intent_digest", "binding_digest", "created", "disabled", "administrator", "oidc_verified", "activation_pending", "admin_grant_pending"} {
		t.Run(field, func(t *testing.T) {
			copy := make(map[string]any)
			for key, value := range base {
				copy[key] = value
			}
			delete(copy, field)
			wire, _ := json.Marshal(copy)
			c, _ := New(func(context.Context, []string, []byte) ([]byte, error) { return wire, nil }, "/config", "/app/www/artisan")
			if _, err := c.CheckAccount(context.Background(), testSpec); err == nil {
				t.Fatal("incomplete native authority was accepted")
			}
		})
	}
	wire, _ := json.Marshal(base)
	c, _ := New(func(context.Context, []string, []byte) ([]byte, error) { return wire, nil }, "/config", "/app/www/artisan")
	if err := c.VerifyAdmin(context.Background(), testSpec); err == nil {
		t.Fatal("unactivated/ungranted owner was accepted")
	}
	base["administrator"], base["activation_pending"] = true, false
	wire, _ = json.Marshal(base)
	if err := c.VerifyAdmin(context.Background(), testSpec); err == nil {
		t.Fatal("an administrator role without verified OIDC proof was accepted")
	}
}

// A retained profile is observed, not repaired back to immutable setup input.
func TestAccountReportRetainedProfileAndBoundary(t *testing.T) {
	base := map[string]any{
		"contract": accountReportContract, "module_version": moduleVersion, "setup_id": testSpec.SetupID,
		"intent_digest": intentDigest(testSpec), "user_id": 3, "binding_digest": digest(Binding{Issuer: testSpec.Issuer, Subject: testSpec.Subject}),
		"name": "Changed 雪", "email": "changed@example.test", "created": false, "disabled": false,
		"administrator": false, "oidc_verified": false, "activation_pending": true, "admin_grant_pending": true,
	}
	wire, _ := json.Marshal(base)
	c, _ := New(func(context.Context, []string, []byte) ([]byte, error) { return wire, nil }, "/config", "/app/www/artisan")
	account, err := c.CheckAccount(context.Background(), testSpec)
	if err != nil || account.Name != "Changed 雪" || account.Email != "changed@example.test" || account.ID != 3 {
		t.Fatal("retained profile not observed", err)
	}
	for _, bad := range []struct {
		field string
		value any
	}{
		{"contract", accountContract}, {"name", ""}, {"name", "unsafe\nname"}, {"name", 123},
		{"email", "wrong"}, {"email", "Person <changed@example.test>"}, {"email", nil},
		{"created", true},
	} {
		t.Run(bad.field+"-boundary", func(t *testing.T) {
			copy := make(map[string]any)
			for k, v := range base {
				copy[k] = v
			}
			copy[bad.field] = bad.value
			wire, _ = json.Marshal(copy)
			if _, err := c.account(context.Background(), testSpec, false); err == nil {
				t.Fatal("invalid or contradictory native profile accepted")
			}
		})
	}
}
