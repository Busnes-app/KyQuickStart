//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/kube"
)

// identityAdmin is a browser signed in to KyIdentity's admin API as the owner's administrator.
// Every change takes a one-use step-up grant for its exact method and path, from the password.
type identityAdmin struct {
	t        *testing.T
	ctx      context.Context
	c        *http.Client
	password string
}

const identityOrigin = "https://" + catalogIdentityURLHost

// ownerPassword reads one of the owner's installer-created passwords in KyIdentity's pod.
func ownerPassword(t *testing.T, ctx context.Context, c *kube.Client, file string) []byte {
	t.Helper()
	ns, pod, container, err := c.AppPod(ctx, "kyidentity")
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Exec(ctx, ns, pod, container, []string{"cat", "/data/installer/" + identityFixturePreparation().SetupID + "/" + file})
	if err != nil || len(b) == 0 {
		t.Fatal("installer-created account credential missing", err)
	}
	return bytes.TrimSpace(b)
}

func newIdentityAdmin(t *testing.T, ctx context.Context, transport http.RoundTripper, username string, password []byte) *identityAdmin {
	t.Helper()
	a := &identityAdmin{t: t, ctx: ctx, c: newJarClient(transport), password: string(password)}
	code, b := a.send("GET", "/api/auth/csrf", nil, "")
	if code != 200 {
		t.Fatalf("KyIdentity CSRF: %d %s", code, b)
	}
	payload, _ := json.Marshal(map[string]string{"username": username, "password": a.password})
	var login struct{ Success, MFARequired bool }
	if code, b := a.send("POST", "/api/auth/login", payload, ""); code != 200 || json.Unmarshal(b, &login) != nil || !login.Success {
		t.Fatalf("%s KyIdentity sign-in: %d", username, code)
	}
	return a
}

// newJarClient is a browser that keeps cookies and follows no redirect.
func newJarClient(transport http.RoundTripper) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Transport: transport, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// send makes one request with the session's CSRF cookie as its header, and stepUp's grant.
func (a *identityAdmin) send(method, path string, body []byte, stepUp string) (int, []byte) {
	a.t.Helper()
	req, _ := http.NewRequestWithContext(a.ctx, method, identityOrigin+path, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	u, _ := url.Parse(identityOrigin)
	for _, c := range a.c.Jar.Cookies(u) {
		if c.Name == "kyidentity_csrf" {
			req.Header.Set("X-CSRF-Token", c.Value)
		}
	}
	if stepUp != "" {
		req.Header.Set("X-KyIdentity-StepUp", stepUp)
	}
	r, err := a.c.Do(req)
	if err != nil {
		a.t.Fatalf("KyIdentity %s %s: %v", method, path, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return r.StatusCode, b
}

// change makes a change that needs a step-up grant and wants a 2xx answer.
func (a *identityAdmin) change(method, path string, body any) []byte {
	a.t.Helper()
	op, _ := json.Marshal(map[string]string{"password": a.password, "method": "", "operation": method + " " + path})
	var grant struct {
		Kind        string `json:"kind"`
		StepUpToken string `json:"stepUpToken"`
	}
	if code, b := a.send("POST", "/api/auth/step-up", op, ""); code != 200 || json.Unmarshal(b, &grant) != nil || grant.Kind != "grant" || grant.StepUpToken == "" {
		a.t.Fatalf("KyIdentity step-up for %s %s: %d %s", method, path, code, b)
	}
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	code, b := a.send(method, path, raw, grant.StepUpToken)
	if code/100 != 2 {
		a.t.Fatalf("KyIdentity %s %s: %d %s", method, path, code, b)
	}
	return b
}

func (a *identityAdmin) get(path string, v any) {
	a.t.Helper()
	code, b := a.send("GET", path, nil, "")
	if code != 200 || json.Unmarshal(b, v) != nil {
		a.t.Fatalf("KyIdentity GET %s: %d %s", path, code, b)
	}
}

// app returns the ID of client's app record.
func (a *identityAdmin) app(client string) string {
	a.t.Helper()
	var reg struct {
		Records []struct{ ID, ClientID string }
	}
	a.get("/api/admin/app-registry?q="+url.QueryEscape(client)+"&limit=100", &reg)
	for _, r := range reg.Records {
		if r.ClientID == client {
			return r.ID
		}
	}
	a.t.Fatalf("KyIdentity has no app for client %s", client)
	return ""
}

// createUser creates an active person who signs in with password and returns their ID.
func (a *identityAdmin) createUser(username, password string) string {
	return a.createPerson(username, username+"@example.test", password)
}

// createPerson is createUser with the person's address, which KyIdentity leaves unverified.
func (a *identityAdmin) createPerson(username, email, password string) string {
	a.t.Helper()
	var created struct {
		User struct{ ID string }
	}
	b := a.change("POST", "/api/admin/users", map[string]string{"username": username, "displayName": strings.ToUpper(username[:1]) + username[1:], "email": email, "password": password, "role": "user", "status": "active"})
	if json.Unmarshal(b, &created) != nil || created.User.ID == "" {
		a.t.Fatalf("KyIdentity created no user: %s", b)
	}
	return created.User.ID
}

// assign gives user access to app, which the app's connector provisions. No role: the
// installer's roles hold exactly the owner's accounts, and its checks refuse any other.
func (a *identityAdmin) assign(appID, user string) {
	a.t.Helper()
	a.change("PUT", "/api/admin/app-registry/"+appID+"/assignments/users/"+user, map[string]string{})
}

// findUser returns the ID of username in KyIdentity.
func (a *identityAdmin) findUser(username string) string {
	a.t.Helper()
	var list struct {
		Users []struct{ ID, Username string }
	}
	a.get("/api/admin/users", &list)
	for _, u := range list.Users {
		if u.Username == username {
			return u.ID
		}
	}
	a.t.Fatalf("KyIdentity has no user %s", username)
	return ""
}
