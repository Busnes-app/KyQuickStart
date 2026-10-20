package immich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"testing"
)

func qualifyNativeCreation(t *testing.T, ctx context.Context, c *Client, provider ProviderSpec, initialSubject, administratorSession string) {
	t.Helper()
	const setupID = "271333e2-c221-4601-b249-563b3d828530"
	scope := Binding{Issuer: provider.Issuer, ClientID: provider.ClientID, Subject: "owned-created-subject"}
	profile := Profile{"Created daily", "created-daily@example.test", false}
	var before []struct{ ID string }
	if c.call(ctx, http.MethodGet, "/admin/users", nil, &before) != nil || c.SubjectAbsent(ctx, scope, setupID) != nil {
		t.Fatal("native creation absence precondition unavailable")
	}
	body := map[string]any{"setupId": setupID, "issuer": scope.Issuer, "clientId": scope.ClientID, "subject": scope.Subject, "profile": map[string]string{"name": profile.Name, "email": profile.Email}}
	raw, _ := json.Marshal(body)
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.origin+"/api/admin/users/kyquickstart/creation", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+administratorSession)
	response, err := c.http.Do(r)
	if err != nil {
		t.Fatal("native session authority probe failed")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("native creation accepted session-only administrator authority", response.StatusCode)
	}
	for _, conflict := range []string{"setupId", "issuer", "clientId", "subject", "email", "role"} {
		bad := map[string]any{}
		for k, v := range body {
			bad[k] = v
		}
		switch conflict {
		case "setupId":
			bad[conflict] = "371333e2-c221-4601-b249-563b3d828530"
		case "issuer":
			bad[conflict] = "https://foreign.example.test"
		case "clientId":
			bad[conflict] = "foreign-client"
		case "subject":
			bad[conflict] = initialSubject
		case "email":
			bad["profile"] = map[string]string{"name": profile.Name, "email": "owner@example.test"}
		case "role":
			bad["profile"] = map[string]any{"name": profile.Name, "email": profile.Email, "administrator": true}
		}
		var ignored json.RawMessage
		if c.call(ctx, http.MethodPost, "/admin/users/kyquickstart/creation", bad, &ignored) == nil || c.SubjectAbsent(ctx, scope, setupID) != nil {
			t.Fatal("native creation conflict mutated ownership", conflict)
		}
	}
	created, err := c.CreateDirectoryAccount(ctx, scope, setupID, profile)
	if err != nil || !uuid.MatchString(created.ID) || created.ID == scope.ID {
		t.Fatal("native ordinary creation receipt not verified", err)
	}
	if err := c.SubjectAbsent(ctx, scope, setupID); !errors.Is(err, ErrAccountConflict) {
		t.Fatal("owned native account was reported absent", err)
	}
	var repeated creationProof
	if c.call(ctx, http.MethodPost, "/admin/users/kyquickstart/creation", body, &repeated) != nil || repeated.UserID != created.ID {
		t.Fatal("native exact creation repeat changed identity")
	}
	changed := Profile{"Projected created", "projected-created@example.test", true}
	if c.ApplyDirectory(ctx, created, setupID, changed) != nil || c.call(ctx, http.MethodPost, "/admin/users/kyquickstart/creation", body, &repeated) != nil {
		t.Fatal("created account cannot use owned directory projection/repeat")
	}
	state, err := c.InspectDirectory(ctx, created, setupID)
	if err != nil || state.Profile != changed || state.Revision != 1 || state.Revoked {
		t.Fatal("creation repeat rolled back projected authority", err)
	}
	var after []struct{ ID string }
	if c.call(ctx, http.MethodGet, "/admin/users", nil, &after) != nil || len(after) != len(before)+1 {
		t.Fatal("native creation repeated or removed existing accounts")
	}
	retained := map[string]bool{}
	for _, b := range after {
		retained[b.ID] = true
	}
	for _, b := range before {
		if !retained[b.ID] {
			t.Fatal("native creation lost an existing account")
		}
	}
	// Offboarding must not depend on the state it cleans up: revoke a soft-deleted account
	// with OIDC disabled and the provider rotated, then restore both.
	var config map[string]any
	var ignored json.RawMessage
	fingerprint, err := c.InspectProviderConfiguration(ctx)
	if err != nil || c.call(ctx, http.MethodGet, "/system-config", nil, &config) != nil {
		t.Fatal("native configuration readback unavailable", err)
	}
	oauth, _ := config["oauth"].(map[string]any)
	original := maps.Clone(oauth)
	oauth["enabled"], oauth["issuerUrl"], oauth["clientId"] = false, "https://rotated.example.test", "rotated"
	if c.call(ctx, http.MethodDelete, "/admin/users/"+created.ID, map[string]any{}, &ignored) != nil || c.call(ctx, http.MethodPut, "/system-config", config, &ignored) != nil {
		t.Fatal("native soft-deleted/disabled/rotated precondition failed")
	}
	rotated := map[string]string{"setupId": setupID, "issuer": "https://rotated.example.test", "clientId": "rotated", "subject": scope.Subject}
	if c.call(ctx, http.MethodPost, "/admin/users/"+created.ID+"/kyquickstart/revoke", rotated, &ignored) == nil {
		t.Fatal("rotated provider binding revoked a receipted account")
	}
	if err := c.RevokeAccess(ctx, created, setupID); err != nil {
		t.Fatal("revocation depended on provider configuration or account activity", err)
	}
	config["oauth"] = original
	if c.call(ctx, http.MethodPut, "/system-config", config, &ignored) != nil || c.call(ctx, http.MethodPost, "/admin/users/"+created.ID+"/restore", nil, &ignored) != nil {
		t.Fatal("native provider/account restore failed")
	}
	if restored, err := c.InspectProviderConfiguration(ctx); err != nil || restored != fingerprint {
		t.Fatal("native provider configuration was not restored", err)
	}
	// Restore must not reopen access; the repeat runs on the restored provider.
	if c.RevokeAccess(ctx, created, setupID) != nil || c.SubjectAbsent(ctx, scope, setupID) == nil {
		t.Fatal("revoked created account became an absence/adoption candidate")
	}
	if c.call(ctx, http.MethodPost, "/admin/users/kyquickstart/creation", body, &ignored) == nil {
		t.Fatal("creation repeat reactivated permanently revoked account")
	}
	if state, err := c.InspectDirectory(ctx, created, setupID); err != nil || !state.Revoked || state.Profile != changed {
		t.Fatal("revoked created profile/ownership not retained", err)
	}
	t.Log("native atomic ordinary creation, scoped collisions, receipt repeat, independent role projection and permanent retained-account refusal verified")
}
