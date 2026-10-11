package immich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
			bad["profile"] = map[string]string{"name": profile.Name, "email": "bootstrap@example.test"}
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
	if c.RevokeAccess(ctx, created, setupID) != nil || c.SubjectAbsent(ctx, scope, setupID) == nil {
		t.Fatal("revoked created account became an absence/adoption candidate")
	}
	var ignored json.RawMessage
	if c.call(ctx, http.MethodPost, "/admin/users/kyquickstart/creation", body, &ignored) == nil {
		t.Fatal("creation repeat reactivated permanently revoked account")
	}
	if state, err := c.InspectDirectory(ctx, created, setupID); err != nil || !state.Revoked || state.Profile != changed {
		t.Fatal("revoked created profile/ownership not retained", err)
	}
	t.Log("native atomic ordinary creation, scoped collisions, receipt repeat, independent role projection and permanent retained-account refusal verified")
}
