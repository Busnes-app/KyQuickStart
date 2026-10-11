package immich

import (
	"context"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/adapters/forgejo"
)

func qualifyNativeProducerCreation(t *testing.T, ctx context.Context, c *Client, identity *nativeIdentityProvider, owner *http.Client, signin func(string) Binding, request func(string, string, string, any, any), client *http.Client, sessions map[string]string) {
	t.Helper()
	const setupID = "271333e2-c221-4601-b249-563b3d828530"
	const actor = "unassigned"
	scope := Binding{Issuer: identity.issuer, ClientID: "fixture-client", Subject: identity.subjects[actor]}
	if !uuid.MatchString(scope.Subject) || scope.Subject != identity.producer.NewSubject || c.SubjectAbsent(ctx, scope, setupID) != nil {
		t.Fatal("owned newly assigned identity already has native account or conflicts")
	}
	path := "/api/admin/app-registry/" + identity.appID + "/assignments/users/" + scope.Subject
	identity.mutate(t, owner, http.MethodPut, path, nil)
	revision := identity.waitDelivery(t, owner, actor, true, 0)
	var crossBinding forgejo.Binding
	var crossToken string
	var crossBrowser *http.Client
	var crossRevision int
	if cross := identity.producer.Cross; cross != nil {
		identity.waitConnectorDelivery(t, owner, cross.ConnectorID, "owner", true, 0)
		identity.waitConnectorDelivery(t, owner, cross.ConnectorID, "daily", true, 0)
		if cross.Client.SubjectAbsent(ctx, cross.SourceID, scope.Subject) != nil {
			t.Fatal("cross-product new subject already exists")
		}
		identity.mutate(t, owner, http.MethodPut, "/api/admin/app-registry/"+cross.AppID+"/assignments/users/"+scope.Subject, nil)
		crossRevision = identity.waitConnectorDelivery(t, owner, cross.ConnectorID, actor, true, 0)
		docker := func(args ...string) string {
			t.Helper()
			raw, err := exec.CommandContext(ctx, "docker", args...).Output()
			if err != nil {
				t.Fatal("owned cross-product credential fixture failed")
			}
			return strings.TrimSpace(string(raw))
		}
		crossBinding, crossToken, crossBrowser = cross.created(t, ctx, identity, docker)
	}
	query := url.Values{"setupId": {setupID}, "issuer": {scope.Issuer}, "clientId": {scope.ClientID}, "subject": {scope.Subject}}
	var proof creationProof
	if c.call(ctx, http.MethodGet, "/admin/users/kyquickstart/creation?"+query.Encode(), nil, &proof) != nil || !matchesCreation(proof, scope, setupID) || *proof.Absent || !uuid.MatchString(proof.UserID) {
		t.Fatal("actual worker native creation receipt unavailable")
	}
	created := scope
	created.ID = proof.UserID
	profile, err := c.InspectDirectory(ctx, created, setupID)
	if err != nil || profile.Revoked || profile.Revision != 0 || profile.Profile != (Profile{"unassigned", "unassigned@example.test", false}) {
		t.Fatal("actual worker ordinary profile/role not verified", err)
	}
	if signed := signin(actor); signed != created {
		t.Fatal("new assigned subject signed into a different native account")
	}
	var key struct{ Secret string }
	request(http.MethodPost, "/api-keys", sessions[actor], map[string]any{"name": "created-personal", "permissions": []string{"all"}}, &key)
	if key.Secret == "" {
		t.Fatal("created-account credential fixture missing")
	}
	probe := func(token string, apiKey bool) int {
		r, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.origin+"/api/users/me", nil)
		if apiKey {
			r.Header.Set("x-api-key", token)
		} else {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal("created-account access probe failed")
		}
		response.Body.Close()
		return response.StatusCode
	}
	if probe(sessions[actor], false) != http.StatusOK || probe(key.Secret, true) != http.StatusOK {
		t.Fatal("created native credentials were not usable before global disablement")
	}
	identity.mutate(t, owner, http.MethodPut, "/api/admin/users/"+scope.Subject, map[string]string{"status": "disabled"})
	identity.waitDelivery(t, owner, actor, false, revision)
	if cross := identity.producer.Cross; cross != nil {
		identity.waitConnectorDelivery(t, owner, cross.ConnectorID, actor, false, crossRevision)
		cross.revoked(t, ctx, crossBinding, crossToken, crossBrowser)
		t.Log("one actual global disablement reached native UUID Immich and numeric Forgejo, revoked both old sessions/keys and retained profiles, ownership and repository")
	}
	for _, status := range []int{probe(sessions[actor], false), probe(key.Secret, true)} {
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Fatal("actual worker creation revocation retained native access", status)
		}
	}
	state, err := c.InspectAccess(ctx, created, setupID)
	if err != nil || !state.Revoked || state.APIKeys != 0 || state.Sessions != 0 {
		t.Fatal("actual worker created-account credential removal unverified", err)
	}
	retained, err := c.InspectDirectory(ctx, created, setupID)
	if err != nil || !retained.Revoked || retained.Profile != profile.Profile || retained.Revision != profile.Revision {
		t.Fatal("created account profile/ownership changed during revocation", err)
	}
	t.Log("actual KyIdentity assignment created an owned ordinary UUID account, signed in through PKCE and global disablement revoked native session/key access while retaining profile and ownership")
}
