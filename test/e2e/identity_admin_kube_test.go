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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

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
	jar, _ := cookiejar.New(nil)
	a := &identityAdmin{t: t, ctx: ctx, c: &http.Client{Transport: transport, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, password: string(password)}
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
	a.t.Helper()
	var created struct {
		User struct{ ID string }
	}
	b := a.change("POST", "/api/admin/users", map[string]string{"username": username, "displayName": strings.ToUpper(username[:1]) + username[1:], "email": username + "@example.test", "password": password, "role": "user", "status": "active"})
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

// registryImage is distribution's registry 3.0.0, linux/amd64 manifest (index
// sha256:6c5666b861f3…), from `docker buildx imagetools inspect docker.io/library/registry:3.0.0`
// on 2026-10-19.
const registryImage = "docker.io/library/registry:3.0.0@sha256:5b12b22f21522fe69443079df04c0f3f42cb9977857f3c20404386652a6c6d8e"

// replayIdentity wraps the KyIdentity build at binary (KyIdentity-server PR #119) in the
// catalog's own 0.4.3 image, so the pod keeps its user, shell tools and assets, and points
// catalogDir's copy of the kyidentity manifest at it as version 0.4.4. The catalog takes only
// @sha256 references, and kind's containerd resolves a digest reference only from a registry,
// not from an image kind loaded, so the build goes to a disposable registry on kind's network
// that the node pulls from over plain HTTP. Both manifest edits fail on drift.
func replayIdentity(t *testing.T, cluster, node, binary, catalogDir string) {
	t.Helper()
	if !filepath.IsAbs(binary) {
		t.Fatal("KYQ_TEST_IDENTITY_BINARY must be absolute")
	}
	manifest := filepath.Join(catalogDir, "kyidentity", "manifest.yaml")
	if err := os.CopyFS(filepath.Dir(manifest), os.DirFS("../../internal/catalog/apps/kyidentity")); err != nil {
		t.Fatal(err)
	}
	m, _ := os.ReadFile(manifest)
	const pin = "ghcr.io/busnes-app/kyidentity-server:0.4.3@sha256:dbf81b355d6307f23b3501c40ad368b06f9ea2fdfdd6e1d09f40d5de87bbc419"
	if bytes.Count(m, []byte("  kyidentity: "+pin+"\n")) != 1 || bytes.Count(m, []byte("\nversion: 0.4.3\n")) != 1 {
		t.Fatal("the catalog's KyIdentity pin or version changed; update the replay build's manifest edit")
	}
	build := t.TempDir()
	bin, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(build, "kyidentity"), bin, 0o755)
	os.WriteFile(filepath.Join(build, "Dockerfile"), []byte("FROM "+pin+"\nCOPY --chmod=0755 kyidentity /usr/local/bin/kyidentity\n"), 0o600)
	docker(t, "pull", "-q", pin)

	registry := cluster + "-registry"
	port := freePort(t, "127.0.0.1")
	t.Cleanup(func() { exec.Command("docker", "rm", "-fv", registry).Run() })
	docker(t, "run", "-d", "--name", registry, "--network", "kind", "--memory", "256m", "--read-only", "--tmpfs", "/tmp", "-v", "/var/lib/registry",
		"-p", "127.0.0.1:"+strconv.Itoa(port)+":5000", registryImage)
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		r, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/v2/")
		if err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("registry: %v", err)
		}
	}
	local := "127.0.0.1:" + strconv.Itoa(port) + "/kyq-identity-replay:" + cluster
	docker(t, "build", "-q", "-t", local, build)
	t.Cleanup(func() { exec.Command("docker", "image", "rm", local).Run() })
	pushed := docker(t, "push", local)
	d := regexp.MustCompile(`digest: (sha256:[0-9a-f]{64})`).FindStringSubmatch(pushed)
	if d == nil {
		t.Fatalf("docker push reported no digest:\n%s", pushed)
	}
	host := registry + ":5000"
	docker(t, "exec", node, "sh", "-c", `mkdir -p /etc/containerd/certs.d/"$1" && printf 'server = "http://%s"\n[host."http://%s"]\n  capabilities = ["pull", "resolve"]\n' "$1" "$1" > /etc/containerd/certs.d/"$1"/hosts.toml`, "sh", host)
	ref := host + "/kyq-identity-replay:" + cluster + "@" + d[1]
	t.Logf("KyIdentity replay build: %s from %s", ref, binary)
	m = bytes.Replace(m, []byte(pin), []byte(ref), 1)
	m = bytes.Replace(m, []byte("\nversion: 0.4.3\n"), []byte("\nversion: 0.4.4\n"), 1)
	os.WriteFile(manifest, m, 0o600)
}
