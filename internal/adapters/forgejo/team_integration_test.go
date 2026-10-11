package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// Fixture resources are created through native APIs in the disposable container.
// The eventual setup adapter must persist these receipts before directory use.
func qualifyNativeTeam(t *testing.T, ctx context.Context, c *Client, owner, daily, foreign Binding, container, image, issuer string, ca []byte) int64 {
	t.Helper()
	var org struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := c.call(ctx, http.MethodPost, "/admin/users/fixture-admin/orgs", map[string]any{"username": "fixture-org", "visibility": "private", "repo_admin_change_team_access": false}, &org); err != nil || org.ID <= 0 || org.Name != "fixture-org" {
		t.Fatal("native fixture organization creation failed", err)
	}
	spec := TeamBinding{OrganizationID: org.ID, SourceID: daily.SourceID, Name: "readers", Organization: org.Name, Permission: "read", Permissions: map[string]string{"repo.code": "read"}}
	b, err := c.CreateTeam(ctx, spec)
	if err != nil || b.ID <= 0 {
		t.Fatal("native fixture team creation failed", err)
	}
	if _, err := c.CreateTeam(ctx, spec); err == nil {
		t.Fatal("existing native team was adopted")
	}
	if err := c.InspectTeam(ctx, b); err != nil {
		t.Fatal("native team receipt refused", err)
	}
	var repo struct {
		ID int64 `json:"id"`
	}
	if err := c.call(ctx, http.MethodPost, "/orgs/fixture-org/repos", map[string]any{"name": "shared-content", "private": true, "auto_init": true}, &repo); err != nil || repo.ID <= 0 {
		t.Fatal("native organization repository failed", err)
	}
	base := "/teams/" + strconv.FormatInt(b.ID, 10)
	if err := c.call(ctx, http.MethodPut, base+"/repos/fixture-org/shared-content", nil, nil); err != nil {
		t.Fatal("native fixture repository assignment failed", err)
	}
	var credential struct {
		Token string `json:"sha1"`
	}
	if err := c.call(ctx, http.MethodPost, "/admin/users/"+daily.Username+"/tokens", map[string]any{"name": "team-fixture", "scopes": []string{"read:repository"}}, &credential); err != nil || credential.Token == "" {
		t.Fatal("native team fixture token failed", err)
	}
	ordinary, err := New(c.origin, credential.Token)
	if err != nil {
		t.Fatal(err)
	}
	ordinary.http.Transport = c.http.Transport
	qualifyNativeGroupOwnership(t, ctx, c, container, image, issuer, ca, org.ID, daily.SourceID, repo.ID, credential.Token, owner, daily, foreign)
	probe := func(allowed bool) {
		t.Helper()
		var got struct {
			ID int64 `json:"id"`
		}
		err := ordinary.call(ctx, http.MethodGet, "/repos/fixture-org/shared-content", nil, &got)
		if allowed && (err != nil || got.ID != repo.ID) {
			t.Fatal("native team did not grant scoped repository read", err)
		}
		if !allowed {
			var refused *apiError
			if !errors.As(err, &refused) || (refused.status != http.StatusForbidden && refused.status != http.StatusNotFound) {
				t.Fatal("native repository denial was not proved", err)
			}
		}
	}
	probe(false)
	if err = c.ReplaceTeamMembers(ctx, b, []Binding{owner, daily}, []Binding{owner}); err == nil {
		t.Fatal("native administrator accepted as ordinary team member")
	}
	if err = c.ReplaceTeamMembers(ctx, b, []Binding{daily}, []Binding{daily}); err != nil {
		t.Fatal("native membership grant failed", err)
	}
	probe(true)
	if err = c.ReplaceTeamMembers(ctx, b, []Binding{daily}, []Binding{daily}); err != nil {
		t.Fatal("native membership repeat failed", err)
	}
	if err = c.call(ctx, http.MethodPut, base+"/members/"+foreign.Username, nil, nil); err != nil {
		t.Fatal("native foreign fixture member failed", err)
	}
	if err = c.ReplaceTeamMembers(ctx, b, []Binding{daily}, nil); err == nil {
		t.Fatal("foreign native member did not stop replacement")
	}
	probe(true)
	if err = c.call(ctx, http.MethodDelete, base+"/members/"+foreign.Username, nil, nil); err != nil {
		t.Fatal("native fixture foreign cleanup failed", err)
	}
	if err = c.ReplaceTeamMembers(ctx, b, []Binding{daily}, nil); err != nil {
		t.Fatal("native membership withdrawal failed", err)
	}
	probe(false)
	var retained struct {
		ID int64 `json:"id"`
	}
	if err = c.call(ctx, http.MethodGet, "/repos/fixture-org/shared-content", nil, &retained); err != nil || retained.ID != repo.ID {
		t.Fatal("membership withdrawal lost organization content", err)
	}
	t.Log("native scoped team grant/repeat/withdrawal, admin and foreign-member refusal, and repository retention verified")
	return org.ID
}

func qualifyNativeGroupOwnership(t *testing.T, ctx context.Context, c *Client, container, image, issuer string, ca []byte, organizationID, sourceID, repositoryID int64, ordinaryToken string, owner, daily, foreign Binding) {
	t.Helper()
	bindings := []map[string]any{}
	for _, b := range []Binding{owner, daily, foreign} {
		bindings = append(bindings, map[string]any{"setup_id": "271333e2-c221-4601-b249-563b3d828530", "app": "forgejo", "kind": "forgejo", "origin": c.origin, "issuer": issuer, "subject": b.Subject, "native_id": b.ID, "source_id": b.SourceID, "username": b.Username})
	}
	input, err := json.Marshal(map[string]any{"container": container, "image": image, "origin": c.origin, "issuer": issuer, "token": c.token, "ca": ca, "organization_id": organizationID, "source_id": sourceID, "repository_id": repositoryID, "ordinary_token": ordinaryToken, "bindings": bindings})
	if err != nil {
		t.Fatal("group fixture input failed")
	}
	binary := filepath.Join(t.TempDir(), "group-bridge.test")
	compile := exec.CommandContext(ctx, "go", "test", "-race", "-c", "-o", binary, "./internal/offboard")
	compile.Dir = "../../.."
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("group fixture compile failed: %s", output)
	}
	command := exec.CommandContext(ctx, binary, "-test.run", "^TestNativeForgejoGroupOwnership$", "-test.v")
	command.Env = append(os.Environ(), "KYQ_TEST_FORGEJO_GROUP_OWNERSHIP=1")
	command.Stdin = bytes.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Log(string(output))
		t.Fatal("native group ownership qualification failed")
	}
	t.Log(string(output))
}
