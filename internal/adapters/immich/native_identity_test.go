package immich

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The existing TLS provider container only proxies to a real, explicitly selected
// KyIdentity binary. Identity data, setup bundles and cookies belong to this test.
// Neither the signing key nor the implementation of authorization is simulated.
type nativeIdentityProvider struct {
	ctx                          context.Context
	container, issuer, directory string
	transport                    *http.Transport
	subjects                     map[string]string
	appID, connectorID           string
	producer                     *nativeImmichProducerPlan
}

const nativeIdentityClientSecret = "disposable-owned-identity-client-secret"

func newNativeIdentityProvider(t *testing.T, ctx context.Context, owner, issuer string, ca []byte, directory string) *nativeIdentityProvider {
	t.Helper()
	container := owner + "-identity"
	for _, name := range []string{container, owner + "-provider"} {
		output, err := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "kyq.fixture-owner"}}`, name).Output()
		if err != nil || string(bytes.TrimSpace(output)) != owner {
			t.Fatal("native identity ownership label missing")
		}
	}
	output, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .NetworkSettings.Ports}}", owner+"-provider").Output()
	var ports map[string][]struct{ HostIP, HostPort string }
	if err != nil || json.Unmarshal(output, &ports) != nil || len(ports["4443/tcp"]) != 1 || ports["4443/tcp"][0].HostIP != "127.0.0.1" {
		t.Fatal("native identity proxy is not loopback")
	}
	address := net.JoinHostPort("127.0.0.1", ports["4443/tcp"][0].HostPort)
	parsed, err := url.Parse(issuer)
	if err != nil {
		t.Fatal("native identity issuer malformed")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("native identity trust missing")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
		if target != parsed.Host {
			return nil, fmt.Errorf("outside owned identity origin")
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	f := &nativeIdentityProvider{ctx: ctx, container: container, issuer: issuer, directory: directory, transport: transport, subjects: map[string]string{}}
	client := f.client()
	for deadline := time.Now().Add(30 * time.Second); ; {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/readyz", nil)
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("real owned identity did not start behind TLS")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return f
}

func (f *nativeIdentityProvider) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Transport: f.transport, Jar: jar, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (f *nativeIdentityProvider) setup(t *testing.T, appOrigin string) map[string]string {
	t.Helper()
	for index, actors := range [][]string{{"owner", "daily"}, {"other-owner", "unassigned"}} {
		folder := fmt.Sprintf("setup-%d", index)
		local := filepath.Join(f.directory, folder)
		if os.Mkdir(local, 0700) != nil {
			t.Fatal("private identity setup directory failed")
		}
		remote := "/data/" + folder + "/"
		write := func(name string, raw []byte) {
			t.Helper()
			if os.WriteFile(filepath.Join(local, name), raw, 0600) != nil {
				t.Fatal("private identity setup file failed")
			}
		}
		accounts := []map[string]string{}
		for _, actor := range actors {
			write(actor+".password", []byte("disposable-identity-"+actor+"-password"))
			accounts = append(accounts, map[string]string{"username": actor, "email": actor + "@example.test", "passwordFile": remote + actor + ".password"})
		}
		clients := []any{}
		if index == 0 {
			write("client.secret", []byte(nativeIdentityClientSecret))
			client := map[string]any{"id": "fixture-client", "name": "Immich fixture", "secretFile": remote + "client.secret", "redirectUris": []string{appOrigin + "/auth/login"}, "postLogoutUris": []string{}, "administratorRole": "admin", "everydayRole": "user"}
			if f.producer != nil {
				write("connector.token", []byte(f.producer.Token))
				client["scim"] = map[string]any{"url": f.producer.BaseURL, "tokenFile": remote + "connector.token"}
			}
			clients = append(clients, client)
			if f.producer != nil && f.producer.Cross != nil {
				write("cross-connector.token", []byte(f.producer.Cross.SCIMToken))
				clients = append(clients, map[string]any{"id": "fixture-forgejo", "name": "Forgejo cross fixture", "secretFile": remote + "client.secret", "redirectUris": []string{f.producer.Cross.API.URL + "/user/oauth2/kyidentity-cross-fixture/callback"}, "postLogoutUris": []string{}, "administratorRole": "admin", "everydayRole": "user", "scim": map[string]any{"url": strings.TrimSuffix(f.producer.BaseURL, "/photos") + "/git", "tokenFile": remote + "cross-connector.token"}})
			}
		}
		spec := map[string]any{"schema": "kyidentity.setup/1", "setupId": fmt.Sprintf("271333e2-c221-4601-b249-563b3d82853%d", index), "admin": accounts[0], "everyday": accounts[1], "clients": clients}
		raw, _ := json.Marshal(spec)
		write("bundle.json", raw)
		run := func(flags ...string) []byte {
			t.Helper()
			args := append([]string{"exec", f.container, "/fixture/kyidentity", "apply-setup", "--file", remote + "bundle.json"}, flags...)
			output, err := exec.CommandContext(f.ctx, "docker", args...).Output()
			if err != nil || len(output) > 65536 || bytes.Contains(output, []byte("disposable-")) {
				if failure, ok := err.(*exec.ExitError); ok {
					file, createErr := os.CreateTemp(os.TempDir(), "kyq-immich-identity-setup-*.log")
					if createErr == nil {
						_, _ = file.Write(failure.Stderr)
						_ = file.Close()
						t.Log("private real identity setup diagnostic:", file.Name())
					}
				}
				t.Fatal("owned real identity setup failed or returned credentials")
			}
			return output
		}
		var report struct {
			Created []struct{ Kind, Name, ID string }
			Reused  []json.RawMessage
		}
		if json.Unmarshal(run(), &report) != nil || len(report.Created) == 0 {
			t.Fatal("owned identity creation receipt missing")
		}
		for _, item := range report.Created {
			if item.Kind == "connector" && item.Name == "fixture-client" {
				f.connectorID = item.ID
			}
			if item.Kind == "connector" && item.Name == "fixture-forgejo" {
				f.producer.Cross.ConnectorID = item.ID
			}
			if item.Kind == "user" {
				if !uuid.MatchString(item.ID) || f.subjects[item.Name] != "" {
					t.Fatal("native identity subject receipt conflicts")
				}
				f.subjects[item.Name] = item.ID
			}
		}
		for _, flags := range [][]string{{"--check"}, {}} {
			var repeat struct{ Created, Reused []json.RawMessage }
			if json.Unmarshal(run(flags...), &repeat) != nil || len(repeat.Created) != 0 || len(repeat.Reused) != len(report.Created) {
				t.Fatal("identity check/repeat changed owned objects")
			}
		}
	}
	if len(f.subjects) != 4 || f.subjects["owner"] == "" || f.subjects["daily"] == "" || f.subjects["unassigned"] == "" {
		t.Fatal("real identity native receipts incomplete")
	}
	owner := f.client()
	f.login(t, owner, "owner")
	var registry struct {
		Records []struct{ ID, ClientID, AccessMode string }
	}
	f.request(t, owner, http.MethodGet, "/api/admin/app-registry", nil, &registry)
	found := 0
	for _, item := range registry.Records {
		if item.ClientID == "fixture-forgejo" && f.producer != nil && f.producer.Cross != nil {
			if item.ID == "" || item.AccessMode != "assigned_only" {
				t.Fatal("cross-product identity policy conflicts")
			}
			f.producer.Cross.AppID = item.ID
		}
		if item.ClientID == "fixture-client" {
			if item.ID == "" || item.AccessMode != "assigned_only" {
				t.Fatal("real identity app is not assigned-only")
			}
			f.appID = item.ID
			found++
		}
	}
	if found != 1 {
		t.Fatal("real identity app ownership missing or ambiguous")
	}
	t.Log("real KyIdentity owned accounts, assigned-only client, roles and immutable setup repeats verified")
	return f.subjects
}

func (f *nativeIdentityProvider) request(t *testing.T, client *http.Client, method, path string, body any, out any) {
	f.requestHeaders(t, client, method, path, body, nil, out)
}

func (f *nativeIdentityProvider) requestHeaders(t *testing.T, client *http.Client, method, path string, body any, headers map[string]string, out any) {
	t.Helper()
	csrf := ""
	if method != http.MethodGet {
		var token struct {
			Token string `json:"csrfToken"`
		}
		f.request(t, client, http.MethodGet, "/api/auth/csrf", nil, &token)
		csrf = token.Token
		if csrf == "" {
			t.Fatal("real identity CSRF token missing")
		}
	}
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	request, _ := http.NewRequestWithContext(f.ctx, method, f.issuer+path, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", f.issuer)
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("real identity request failed")
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 || response.StatusCode != http.StatusOK || json.Unmarshal(raw, out) != nil {
		t.Fatalf("real identity %s %s refused or malformed (HTTP %d)", method, path, response.StatusCode)
	}
}

func (f *nativeIdentityProvider) login(t *testing.T, client *http.Client, actor string) {
	t.Helper()
	if f.subjects[actor] == "" {
		t.Fatal("unknown identity actor")
	}
	var result struct{ Success, MFARequired, Restricted bool }
	f.request(t, client, http.MethodPost, "/api/auth/login", map[string]string{"username": actor, "password": "disposable-identity-" + actor + "-password"}, &result)
	if !result.Success || result.MFARequired || result.Restricted {
		t.Fatal("real identity fixture sign-in did not complete")
	}
}

func (f *nativeIdentityProvider) authorize(t *testing.T, actor, authorizeURL, redirect string, allowed bool) string {
	t.Helper()
	parsed, err := url.Parse(authorizeURL)
	if err != nil || parsed.Scheme+"://"+parsed.Host != f.issuer || parsed.Query().Get("client_id") != "fixture-client" || parsed.Query().Get("redirect_uri") != redirect || parsed.Query().Get("state") != "owned-"+actor || parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("real identity authorization scope conflicts")
	}
	client := f.client()
	f.login(t, client, actor)
	request, _ := http.NewRequestWithContext(f.ctx, http.MethodGet, authorizeURL, nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("real identity authorization did not complete")
	}
	response.Body.Close()
	location, err := response.Location()
	if err != nil || response.StatusCode != http.StatusFound || location.Scheme+"://"+location.Host+location.Path != redirect || location.Query().Get("state") != "owned-"+actor {
		t.Fatal("real identity callback not pinned")
	}
	if allowed {
		if location.Query().Get("code") == "" || location.Query().Get("error") != "" {
			t.Fatal("real identity assigned authorization refused")
		}
	} else if location.Query().Get("code") != "" || location.Query().Get("error") != "access_denied" {
		t.Fatal("real identity unassigned actor obtained access")
	}
	return location.String()
}
