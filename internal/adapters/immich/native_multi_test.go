package immich

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/adapters/forgejo"
)

const crossForgejoImage = "codeberg.org/forgejo/forgejo:16.0.5-rootless@sha256:5effb7305584aca479b29fde6f9631a6dbe86ae798ae02eeea33a3666f0c0bf8"

// One additional native product, with its own identity client, connector token,
// resources and target. The parent owns its network, containers and cleanup.
type nativeCrossForgejo struct {
	Container, Image, Token, SCIMToken, AppID, ConnectorID string
	API                                                    *httptest.Server
	Client                                                 *forgejo.Client
	SourceID                                               int64
	Bindings                                               []forgejo.Binding
	DailySession                                           *http.Client
	RepositoryID                                           int64
}

func newNativeCrossForgejo(t *testing.T, ctx context.Context, owner string, ca []byte, run func(string, string, ...string), docker func(...string) string) *nativeCrossForgejo {
	t.Helper()
	f := &nativeCrossForgejo{Container: owner + "-forgejo", Image: crossForgejoImage, SCIMToken: strings.Repeat("ef", 32)}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("cross-product owned port unavailable")
	}
	address := listener.Addr().String()
	_, port, _ := net.SplitHostPort(address)
	listener.Close()
	backend, _ := url.Parse("http://" + address)
	proxy := httputil.NewSingleHostReverseProxy(backend)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "owned backend unavailable", http.StatusServiceUnavailable)
	}
	f.API = httptest.NewTLSServer(proxy)
	t.Cleanup(f.API.Close)
	run(f.Container, f.Image, "--label", "kyq.fixture=forgejo", "--memory", "512m", "--memory-swap", "512m", "--cpus", "1", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"-p", "127.0.0.1:"+port+":3000", "-e", "FORGEJO__server__ROOT_URL="+f.API.URL+"/", "-e", "FORGEJO__server__HTTP_ADDR=0.0.0.0", "-e", "FORGEJO__server__HTTP_PORT=3000",
		"-e", "SSL_CERT_FILE=/var/lib/gitea/fixture/ca.pem", "-e", "FORGEJO__oauth2_client__ACCOUNT_LINKING=disabled", "-e", "FORGEJO__oauth2_client__ENABLE_AUTO_REGISTRATION=false",
		"-e", "FORGEJO__security__INSTALL_LOCK=true", "-e", "FORGEJO__database__DB_TYPE=sqlite3", "-e", "FORGEJO__server__DISABLE_SSH=true",
		"-e", "FORGEJO__service__DISABLE_REGISTRATION=true", "-e", "FORGEJO__log__LEVEL=warn")
	var ports map[string][]struct{ HostIP, HostPort string }
	if json.Unmarshal([]byte(docker("inspect", "--format", "{{json .NetworkSettings.Ports}}", f.Container)), &ports) != nil || len(ports["3000/tcp"]) != 1 || ports["3000/tcp"][0].HostIP != "127.0.0.1" {
		t.Fatal("cross-product native port is not loopback")
	}
	client := f.API.Client()
	client.Timeout = 2 * time.Second
	for deadline := time.Now().Add(30 * time.Second); ; {
		response, err := client.Get(f.API.URL + "/api/v1/version")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("owned cross-product service did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The certificate is public. Write it as the container user before native
	// OIDC discovery, rather than widening permissions on host fixture secrets.
	writeCA := exec.CommandContext(ctx, "docker", "exec", "-i", f.Container, "sh", "-c", "umask 022; mkdir -p /var/lib/gitea/fixture; cat > /var/lib/gitea/fixture/ca.pem; chmod 644 /var/lib/gitea/fixture/ca.pem")
	writeCA.Stdin = bytes.NewReader(ca)
	if writeCA.Run() != nil {
		t.Fatal("cross-product public trust write failed")
	}
	config := "/var/lib/gitea/custom/conf/app.ini"
	docker("exec", f.Container, "forgejo", "admin", "user", "create", "--config", config, "--username", "fixture-admin", "--email", "fixture-admin@example.test", "--password", "disposable-fixture-password-only", "--admin", "--must-change-password=false")
	f.Token = docker("exec", f.Container, "forgejo", "admin", "user", "generate-access-token", "--config", config, "--username", "fixture-admin", "--token-name", "fixture-adapter", "--raw", "--scopes", "write:admin,read:user,read:repository")
	previous := http.DefaultTransport
	http.DefaultTransport = f.API.Client().Transport
	c, err := forgejo.New(f.API.URL, f.Token)
	http.DefaultTransport = previous
	if err != nil {
		t.Fatal("cross-product native client unavailable")
	}
	f.Client = c
	return f
}

func (f *nativeCrossForgejo) prepare(t *testing.T, ctx context.Context, identity *nativeIdentityProvider) {
	t.Helper()
	session, err := f.Client.OpenSourceSession(ctx, "fixture-admin", "disposable-fixture-password-only")
	if err != nil {
		t.Fatal("cross-product source session refused", err)
	}
	defer session.Close()
	source, err := session.Apply(ctx, forgejo.SourceSpec{Name: "kyidentity-cross-fixture", Issuer: identity.issuer, ClientID: "fixture-forgejo", AssignedClaim: "aud", AssignedValue: "fixture-forgejo", RolesClaim: "roles", AdminRole: "admin"}, nativeIdentityClientSecret)
	if err != nil || session.Inspect(ctx, source, nativeIdentityClientSecret) != nil {
		t.Fatal("cross-product native provider ownership unverified", err)
	}
	f.SourceID = source.ID
	for _, actor := range []string{"owner", "daily"} {
		b, err := f.Client.Apply(ctx, forgejo.AccountSpec{Username: actor, Name: actor, Email: actor + "@example.test", SourceID: source.ID, Subject: identity.subjects[actor]})
		if err != nil {
			t.Fatal("cross-product owned initial account refused", err)
		}
		f.Bindings = append(f.Bindings, b)
		browser := f.login(t, identity, actor)
		if actor == "daily" {
			f.DailySession = browser
		}
		if f.probe(t, browser, "/user/settings") != http.StatusOK {
			t.Fatal("cross-product native sign-in failed")
		}
	}
	if f.Client.GrantAdmin(ctx, f.Bindings[0]) != nil {
		t.Fatal("cross-product native owner grant missing")
	}
}

func (f *nativeCrossForgejo) login(t *testing.T, identity *nativeIdentityProvider, actor string) *http.Client {
	t.Helper()
	transport := f.API.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: identity.transport.TLSClientConfig.RootCAs.Clone()}
	transport.TLSClientConfig.RootCAs.AddCert(f.API.Certificate())
	issuer, _ := url.Parse(identity.issuer)
	app, _ := url.Parse(f.API.URL)
	transport.DialContext = func(ctx context.Context, network, target string) (net.Conn, error) {
		if target == issuer.Host {
			return identity.transport.DialContext(ctx, network, target)
		}
		if target != app.Host {
			return nil, io.ErrUnexpectedEOF
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, target)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: transport, Jar: jar, Timeout: 15 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	identity.login(t, client, actor)
	response, err := client.Get(f.API.URL + "/user/oauth2/kyidentity-cross-fixture")
	if err != nil {
		t.Fatal("cross-product actual OIDC flow failed")
	}
	response.Body.Close()
	if response.StatusCode >= 400 {
		t.Fatal("cross-product actual OIDC flow refused", response.StatusCode)
	}
	return client
}

func (f *nativeCrossForgejo) probe(t *testing.T, client *http.Client, path string) int {
	t.Helper()
	response, err := client.Get(f.API.URL + path)
	if err != nil {
		t.Fatal("cross-product native access probe failed")
	}
	response.Body.Close()
	return response.StatusCode
}

func (f *nativeCrossForgejo) certificate() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.API.Certificate().Raw})
}

func (f *nativeCrossForgejo) request(t *testing.T, ctx context.Context, method, path, token string, body, out any) int {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req, _ := http.NewRequestWithContext(ctx, method, f.API.URL+"/api/v1"+path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := f.API.Client().Do(req)
	if err != nil {
		t.Fatal("cross-product native request did not complete")
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		t.Fatal("cross-product native response unsafe")
	}
	if out != nil && (response.StatusCode < 200 || response.StatusCode >= 300 || json.Unmarshal(raw, out) != nil) {
		if file, err := os.CreateTemp("", "kyq-cross-native-response-*.json"); err == nil {
			_, _ = file.Write(raw)
			_ = file.Close()
			t.Log("private cross-product diagnostic:", file.Name())
		}
		t.Fatal("cross-product native request refused", method, strings.Split(path, "?")[0], response.StatusCode)
	}
	return response.StatusCode
}

func (f *nativeCrossForgejo) created(t *testing.T, ctx context.Context, identity *nativeIdentityProvider, docker func(...string) string) (forgejo.Binding, string, *http.Client) {
	t.Helper()
	query := url.Values{"source_id": {fmt.Sprint(f.SourceID)}, "login_name": {identity.subjects["unassigned"]}, "limit": {"2"}, "page": {"1"}}
	var users []struct{ ID int64 }
	f.request(t, ctx, http.MethodGet, "/admin/users?"+query.Encode(), f.Token, nil, &users)
	if len(users) != 1 || users[0].ID <= 0 {
		t.Fatal("cross-product created identity ambiguous")
	}
	binding := forgejo.Binding{ID: users[0].ID, Username: "unassigned", SourceID: f.SourceID, Subject: identity.subjects["unassigned"]}
	account, err := f.Client.InspectDirectory(ctx, binding)
	if err != nil || !account.Active || account.Admin || account.LoginProhibited || account.Name != "unassigned" || account.Email != "unassigned@example.test" {
		t.Fatal("cross-product created ordinary profile unverified", err)
	}
	browser := f.login(t, identity, "unassigned")
	if f.probe(t, browser, "/user/settings") != http.StatusOK {
		t.Fatal("cross-product created native session unusable")
	}
	token := docker("exec", f.Container, "forgejo", "admin", "user", "generate-access-token", "--config", "/var/lib/gitea/custom/conf/app.ini", "--username", "unassigned", "--token-name", "cross-personal", "--raw", "--scopes", "write:user,write:repository")
	if f.request(t, ctx, http.MethodGet, "/user", token, nil, nil) != http.StatusOK {
		t.Fatal("cross-product created native key unusable")
	}
	var repository struct{ ID int64 }
	f.request(t, ctx, http.MethodPost, "/user/repos", token, map[string]any{"name": "global-retained", "private": true, "auto_init": true}, &repository)
	if repository.ID <= 0 {
		t.Fatal("cross-product retained repository fixture missing")
	}
	f.RepositoryID = repository.ID
	return binding, token, browser
}

func (f *nativeCrossForgejo) revoked(t *testing.T, ctx context.Context, binding forgejo.Binding, token string, browser *http.Client) {
	t.Helper()
	status := f.request(t, ctx, http.MethodGet, "/user", token, nil, nil)
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		t.Fatal("cross-product global disablement retained API-key access", status)
	}
	response, err := browser.Get(f.API.URL + "/user/settings")
	if err != nil {
		t.Fatal("cross-product old session probe failed")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(raw), `aria-label="Account is suspended"`) {
		t.Fatal("cross-product global disablement retained ordinary session")
	}
	account, err := f.Client.InspectDirectory(ctx, binding)
	if err != nil || account.Active || !account.LoginProhibited || account.Name != "unassigned" || account.Email != "unassigned@example.test" {
		t.Fatal("cross-product revoked profile not retained", err)
	}
	var repository struct{ ID int64 }
	f.request(t, ctx, http.MethodGet, "/repos/unassigned/global-retained", f.Token, nil, &repository)
	if repository.ID != f.RepositoryID || repository.ID <= 0 {
		t.Fatal("cross-product global disablement changed retained repository")
	}
}
