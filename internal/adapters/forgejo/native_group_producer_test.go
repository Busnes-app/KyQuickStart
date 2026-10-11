package forgejo

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// Only native, audited identity mutations produce these requests. The observer
// performs verified TLS GETs and cannot create or replace bridge state.
func qualifyNativeGroupProducer(t *testing.T, ctx context.Context, c *Client, plan *nativeProducerPlan, f *nativeIdentityFixture, identity *http.Client, issuer string, daily Binding) {
	t.Helper()
	if plan.GroupID == "" || plan.OrganizationID <= 0 {
		t.Fatal("owned producer group receipts missing")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(plan.Certificate) {
		t.Fatal("producer group trust missing")
	}
	observer := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}, Timeout: time.Second}
	defer observer.CloseIdleConnections()
	sum := sha256.Sum256([]byte("group\x00forgejo\x00" + issuer + "\x00" + plan.GroupID))
	id := hex.EncodeToString(sum[:])
	account := sha256.Sum256([]byte("forgejo\x00" + issuer + "\x00" + daily.Subject))
	member := hex.EncodeToString(account[:])
	observe := func(label string, count int, deleted bool) {
		t.Helper()
		for deadline := time.Now().Add(65 * time.Second); ; {
			path := plan.BaseURL + "/Groups/" + id
			if label == "Forgejo developers" {
				path = plan.BaseURL + "/Groups?filter=" + url.QueryEscape("externalId eq "+strconv.Quote(plan.GroupID)) + "&startIndex=1&count=2"
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+plan.Token)
			response, err := observer.Do(req)
			if err == nil {
				raw, readErr := io.ReadAll(io.LimitReader(response.Body, 16385))
				response.Body.Close()
				if deleted && response.StatusCode == http.StatusNotFound {
					t.Log("actual worker group retirement observed")
					return
				}
				var resource struct {
					ID, ExternalID, DisplayName string
					Members                     []struct{ Value string }
				}
				if label == "Forgejo developers" {
					var page struct{ Resources []json.RawMessage }
					if json.Unmarshal(raw, &page) == nil && len(page.Resources) == 1 {
						raw = page.Resources[0]
					}
				}
				if readErr == nil && response.StatusCode == http.StatusOK && json.Unmarshal(raw, &resource) == nil && resource.ID == id && resource.ExternalID == plan.GroupID && resource.DisplayName == label && len(resource.Members) == count && (count == 0 || resource.Members[0].Value == member) {
					t.Logf("actual worker group %q with %d member(s) observed", label, count)
					return
				}
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				t.Fatal("actual identity group event did not reach verified native state")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	observe("Forgejo developers", 1, false)
	var teams []struct {
		ID   int64
		Name string
	}
	if c.call(ctx, http.MethodGet, "/orgs/fixture-org/teams?limit=100&page=1", nil, &teams) != nil {
		t.Fatal("producer native team list failed")
	}
	var teamID int64
	for _, team := range teams {
		if team.Name == "kyq-"+id[:32] {
			if teamID != 0 {
				t.Fatal("producer native team ambiguous")
			}
			teamID = team.ID
		}
	}
	if teamID <= 0 {
		t.Fatal("producer owned team missing")
	}
	if c.call(ctx, http.MethodPut, "/teams/"+strconv.FormatInt(teamID, 10)+"/repos/fixture-org/shared-content", nil, nil) != nil {
		t.Fatal("producer repository assignment failed")
	}
	var token struct {
		Value string `json:"sha1"`
	}
	if c.call(ctx, http.MethodPost, "/admin/users/"+daily.Username+"/tokens", map[string]any{"name": "group-producer", "scopes": []string{"read:repository"}}, &token) != nil || token.Value == "" {
		t.Fatal("producer group ordinary token missing")
	}
	ordinary, err := New(c.origin, token.Value)
	if err != nil {
		t.Fatal(err)
	}
	ordinary.http.Transport = c.http.Transport
	probe := func(allowed bool) {
		t.Helper()
		var repo struct{ ID int64 }
		err := ordinary.call(ctx, http.MethodGet, "/repos/fixture-org/shared-content", nil, &repo)
		if allowed {
			if err != nil || repo.ID <= 0 {
				t.Fatal("producer group access missing")
			}
			return
		}
		var refusal *apiError
		if !errors.As(err, &refusal) || (refusal.status != http.StatusForbidden && refusal.status != http.StatusNotFound) {
			t.Fatal("producer group removal did not withdraw access")
		}
	}
	mutate := func(method, path string, body any) {
		t.Helper()
		var grant struct{ Kind, StepUpToken string }
		f.request(t, identity, issuer, http.MethodPost, "/api/auth/step-up", map[string]string{"password": "disposable-identity-owner-password", "operation": method + " " + path}, nil, &grant)
		if grant.Kind != "grant" || grant.StepUpToken == "" {
			t.Fatal("producer group operation grant missing")
		}
		var result struct{ Success bool }
		f.request(t, identity, issuer, method, path, body, map[string]string{"X-KyIdentity-StepUp": grant.StepUpToken}, &result)
		if !result.Success {
			t.Fatal("producer native group mutation failed")
		}
	}
	path := "/api/admin/groups/" + plan.GroupID
	probe(true)
	mutate(http.MethodPut, path, map[string]string{"name": "Renamed developers", "description": "fixture"})
	observe("Renamed developers", 1, false)
	probe(true)
	mutate(http.MethodDelete, path+"/members/"+daily.Subject, nil)
	observe("Renamed developers", 0, false)
	probe(false)
	mutate(http.MethodPut, path+"/members/"+daily.Subject, nil)
	observe("Renamed developers", 1, false)
	probe(true)
	mutate(http.MethodDelete, path, nil)
	observe("Renamed developers", 0, true)
	probe(false)
	var retained struct{ ID int64 }
	if c.call(ctx, http.MethodGet, "/repos/fixture-org/shared-content", nil, &retained) != nil || retained.ID <= 0 {
		t.Fatal("producer group retirement lost repository")
	}
	t.Log("actual KyIdentity group worker creation, rename, membership removal/regrant and retirement verified with ordinary access and retained repository")
}
