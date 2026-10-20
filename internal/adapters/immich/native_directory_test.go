package immich

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"testing"
)

func qualifyNativeDirectory(t *testing.T, ctx context.Context, c *Client, container string, b Binding, session string, client *http.Client, callback func() string) {
	t.Helper()
	const setupID = "271333e2-c221-4601-b249-563b3d828530"
	original := Profile{"daily", "daily@example.test", false}
	before, err := c.InspectDirectory(ctx, b, setupID)
	if err != nil || before.Revision != 0 || before.Revoked || before.Profile != original {
		t.Fatal("native initial directory receipt unavailable", err)
	}
	profile := Profile{"Directory daily", "directory-daily@example.test", false}
	if c.ApplyDirectory(ctx, b, setupID, profile) != nil {
		t.Fatal("native directory profile projection failed")
	}
	changed, err := c.InspectDirectory(ctx, b, setupID)
	if err != nil || changed.Revision != 1 || changed.Profile != profile || c.ApplyDirectory(ctx, b, setupID, profile) != nil {
		t.Fatal("native directory projection/repeat not verified", err)
	}
	if repeated, err := c.InspectDirectory(ctx, b, setupID); err != nil || repeated != changed {
		t.Fatal("native directory repeat changed revision")
	}
	// Setup resumes the original immutable intent while verifying the current
	// independently owned directory projection; it must not reset the new profile.
	output, err := exec.CommandContext(ctx, "docker", "exec", "-e", "NODE_OPTIONS=--max-old-space-size=512", container, "immich-admin", "apply-setup", "--file", "/usr/src/app/kyquickstart/setup.json", "--check").Output()
	var setup nativeSetupReport
	if err != nil || json.Unmarshal(output, &setup) != nil || len(setup.Created) != 0 || len(setup.Accounts) != 2 {
		t.Fatal("native setup refused a legitimate owned directory profile")
	}
	if repeated, err := c.InspectDirectory(ctx, b, setupID); err != nil || repeated != changed {
		t.Fatal("native setup rewrote the projected profile")
	}
	scoped := map[string]any{"setupId": setupID, "issuer": b.Issuer, "clientId": b.ClientID, "subject": b.Subject, "expectedRevision": changed.Revision, "profile": original}
	for _, field := range []string{"setupId", "subject", "expectedRevision", "email-conflict"} {
		bad := map[string]any{}
		for key, value := range scoped {
			bad[key] = value
		}
		switch field {
		case "setupId":
			bad[field] = "371333e2-c221-4601-b249-563b3d828530"
		case "subject":
			bad[field] = "foreign-subject"
		case "expectedRevision":
			bad[field] = 0
		case "email-conflict":
			bad["profile"] = Profile{"Conflict", "owner@example.test", true}
		}
		var ignored json.RawMessage
		if c.call(ctx, http.MethodPost, "/admin/users/"+b.ID+"/kyquickstart/directory", bad, &ignored) == nil {
			t.Fatal("native directory accepted a foreign/stale/conflicting update")
		}
		if retained, err := c.InspectDirectory(ctx, b, setupID); err != nil || retained != changed {
			t.Fatal("native directory conflict mutated profile/receipt")
		}
	}
	// A native API edit without the receipt is not silently adopted.
	var ignored json.RawMessage
	if c.call(ctx, http.MethodPut, "/admin/users/"+b.ID, map[string]string{"name": "Unreceipted edit"}, &ignored) != nil {
		t.Fatal("native foreign-edit fixture failed")
	}
	if c.ApplyDirectory(ctx, b, setupID, original) == nil {
		t.Fatal("native directory adopted an unreceipted edit")
	}
	if c.call(ctx, http.MethodPut, "/admin/users/"+b.ID, map[string]string{"name": profile.Name}, &ignored) != nil {
		t.Fatal("native foreign-edit fixture reset failed")
	}
	profile.Administrator = true
	if c.ApplyDirectory(ctx, b, setupID, profile) != nil {
		t.Fatal("native directory role grant failed")
	}
	probe := func(method, path string, body any) int {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		req, _ := http.NewRequestWithContext(ctx, method, c.origin+"/api"+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+session)
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("native directory session probe failed")
		}
		response.Body.Close()
		return response.StatusCode
	}
	if probe(http.MethodGet, "/admin/users", nil) != http.StatusOK {
		t.Fatal("native projected grant did not authorize existing session")
	}
	if probe(http.MethodGet, "/admin/users/"+b.ID+"/kyquickstart/directory?setupId="+setupID, nil) != http.StatusForbidden {
		t.Fatal("native directory accepted session-only authority")
	}
	if probe(http.MethodPost, "/oauth/callback", map[string]string{"url": callback(), "state": "owned-daily", "codeVerifier": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}) != http.StatusUnauthorized {
		t.Fatal("stale signed role overwrote owned directory authority")
	}
	profile.Administrator = false
	if c.ApplyDirectory(ctx, b, setupID, profile) != nil {
		t.Fatal("native directory role demotion failed")
	}
	if status := probe(http.MethodGet, "/admin/users", nil); status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Fatal("native directory demotion left old-session administration")
	}
	if c.ApplyDirectory(ctx, b, setupID, original) != nil {
		t.Fatal("native directory fixture could not restore owned profile")
	}
	final, err := c.InspectDirectory(ctx, b, setupID)
	if err != nil || final.Profile != original || final.Revision != 4 || final.Revoked {
		t.Fatal("native directory final receipt not verified", err)
	}
	t.Log("native atomic profile/role receipts, setup compatibility, stale revision and foreign-edit refusal, rollback, old-session demotion and stale OIDC role denial verified")
}
