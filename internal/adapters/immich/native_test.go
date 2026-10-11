package immich

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const nativeImage = "ghcr.io/immich-app/immich-server@sha256:949bbf9ff32543539f471bc275437e6d1a1a4fee43c55bb9e4c06b946909bf1c"
const databaseImage = "ghcr.io/immich-app/postgres@sha256:bcf63357191b76a916ae5eb93464d65c07511da41e3bf7a8416db519b40b1c23"
const cacheImage = "valkey/valkey@sha256:70739f85ad2ee01a726a965584a0f94895f01b0c60b3cc8b0aeef11eaa6888cf"

func TestNativeProviderAccountAndAdministrator(t *testing.T) {
	if os.Getenv("KYQ_TEST_IMMICH_NATIVE") != "1" {
		t.Skip("set KYQ_TEST_IMMICH_NATIVE=1 for the pinned disposable native fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	nonce := fmt.Sprintf("kyq-immich-%d", time.Now().UnixNano())
	owned := []string{}
	docker := func(args ...string) string {
		t.Helper()
		raw, err := exec.CommandContext(ctx, "docker", args...).Output()
		if err != nil {
			t.Fatalf("owned Immich Docker %s failed", args[0])
		}
		return strings.TrimSpace(string(raw))
	}
	serverImage := nativeImage
	moduleImage := os.Getenv("KYQ_TEST_IMMICH_MODULE_IMAGE")
	identityBinary := os.Getenv("KYQ_TEST_IMMICH_IDENTITY_BINARY")
	if os.Getenv("KYQ_TEST_IMMICH_CREATE") == "1" && os.Getenv("KYQ_TEST_IMMICH_SETUP") != "1" {
		t.Fatal("native Immich creation qualification requires owned setup")
	}
	if os.Getenv("KYQ_TEST_IMMICH_PRODUCER") == "1" && (identityBinary == "" || os.Getenv("KYQ_TEST_IMMICH_BRIDGE") != "1" || os.Getenv("KYQ_TEST_IMMICH_SETUP") != "1") {
		t.Fatal("Immich producer requires real identity, setup and bridge modes")
	}
	if os.Getenv("KYQ_TEST_IMMICH_MULTI") == "1" && (os.Getenv("KYQ_TEST_IMMICH_PRODUCER") != "1" || os.Getenv("KYQ_TEST_IMMICH_CREATE") != "1") {
		t.Fatal("native multi-product fixture requires producer and creation modes")
	}
	if identityBinary != "" {
		st, err := os.Lstat(identityBinary)
		if err != nil || !filepath.IsAbs(identityBinary) || filepath.Clean(identityBinary) != identityBinary || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 || os.Getenv("KYQ_TEST_IMMICH_SETUP") != "1" {
			t.Fatal("real identity qualification needs an explicit static binary and native setup")
		}
	}
	if os.Getenv("KYQ_TEST_IMMICH_INSTALLER") == "1" && os.Getenv("KYQ_TEST_IMMICH_SETUP") != "1" {
		t.Fatal("native installer qualification requires native setup qualification")
	}
	if (os.Getenv("KYQ_TEST_IMMICH_BRIDGE") == "1" || os.Getenv("KYQ_TEST_IMMICH_SETUP") == "1") && moduleImage == "" {
		t.Fatal("native Immich bridge qualification requires a community module image")
	}
	if moduleImage != "" {
		if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(moduleImage) {
			t.Fatal("module fixture requires an immutable local image digest")
		}
		patch, err := os.ReadFile("module/source.patch")
		if err != nil {
			t.Fatal("module fixture patch unavailable")
		}
		digest := sha256.Sum256(patch)
		for label, expected := range map[string]string{
			"contract": accessContract, "source": "1b6098c9dbfffe978bec2d414606ed7a4c8e019a", "patch-sha256": fmt.Sprintf("%x", digest),
		} {
			if docker("image", "inspect", "--format", "{{index .Config.Labels \"kyq.immich-access."+label+"\"}}", moduleImage) != expected {
				t.Fatal("module fixture source/patch label conflicts")
			}
		}
		serverImage = moduleImage
	}
	network := docker("network", "create", "--label", "kyq.fixture=immich", "--label", "kyq.fixture-owner="+nonce, nonce)
	t.Cleanup(func() {
		for i := len(owned) - 1; i >= 0; i-- {
			if exec.Command("docker", "rm", "-fv", owned[i]).Run() != nil {
				t.Error("owned fixture container cleanup failed")
			}
		}
		if exec.Command("docker", "network", "rm", network).Run() != nil {
			t.Error("owned fixture network cleanup failed")
		}
	})
	material, ca := providerMaterial(t, nonce+"-provider")
	providerURL := material.Issuer
	if identityBinary != "" {
		material.IdentityBackend = "http://" + nonce + "-identity:8080"
	}
	directory := t.TempDir()
	if os.Chmod(directory, 0700) != nil {
		t.Fatal("private fixture directory failed")
	}
	caFile := filepath.Join(directory, "provider-ca.pem")
	if os.WriteFile(caFile, ca, 0600) != nil {
		t.Fatal("private fixture trust failed")
	}
	env := filepath.Join(directory, "fixture.env")
	if os.WriteFile(env, []byte("POSTGRES_PASSWORD=disposable-database-password\nPOSTGRES_USER=postgres\nPOSTGRES_DB=immich\nDB_HOSTNAME="+nonce+"-db\nDB_USERNAME=postgres\nDB_PASSWORD=disposable-database-password\nDB_DATABASE_NAME=immich\nREDIS_HOSTNAME="+nonce+"-cache\nIMMICH_LOG_LEVEL=error\nNODE_EXTRA_CA_CERTS=/fixture/provider-ca.pem\n"), 0600) != nil {
		t.Fatal("private fixture configuration failed")
	}
	run := func(name, image string, args ...string) {
		t.Helper()
		base := []string{"run", "-d", "--name", name, "--network", nonce, "--label", "kyq.fixture=immich", "--label", "kyq.fixture-owner=" + nonce}
		base = append(base, args...)
		base = append(base, image)
		if strings.HasSuffix(name, "-provider") {
			base = append(base, "-test.run=^TestNativeContainerProvider$", "-test.v")
		}
		docker(base...)
		owned = append(owned, name)
	}
	providerConfig := filepath.Join(directory, "provider.json")
	raw, _ := json.Marshal(material)
	if os.WriteFile(providerConfig, raw, 0600) != nil {
		t.Fatal("private provider config failed")
	}
	binary := filepath.Join(directory, "provider.test")
	compile := exec.CommandContext(ctx, "go", "test", "-p", "1", "-c", "-o", binary, ".")
	compile.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("provider fixture compile failed: %s", output)
	}
	providerArgs := []string{"--memory", "256m", "--memory-swap", "256m", "--entrypoint", "/fixture/provider.test", "-v", binary + ":/fixture/provider.test:ro", "-v", providerConfig + ":/fixture/provider.json:ro", "-e", "KYQ_IMMICH_PROVIDER_FILE=/fixture/provider.json"}
	if identityBinary != "" {
		providerArgs = append(providerArgs, "-p", "127.0.0.1::4443")
	}
	run(nonce+"-provider", nativeImage, providerArgs...)
	var identity *nativeIdentityProvider
	var producer *nativeImmichProducerPlan
	var producerStop func()
	var producerAdmin *http.Client
	var producerDailyRevision int
	var producerFreshCallback string
	subjects := map[string]string{"owner": "owned-owner", "daily": "owned-daily"}
	if identityBinary != "" {
		identityDir := filepath.Join(directory, "identity")
		if os.Mkdir(identityDir, 0700) != nil {
			t.Fatal("owned identity data directory failed")
		}
		if os.Getenv("KYQ_TEST_IMMICH_PRODUCER") == "1" {
			producer = newNativeImmichProducerPlan(t, ctx, nonce, os.Getenv("KYQ_TEST_IMMICH_SSH_IMAGE"))
			if os.WriteFile(filepath.Join(identityDir, "connector.pem"), producer.Certificate, 0600) != nil {
				t.Fatal("owned connector public trust failed")
			}
		}
		proxyIP := docker("inspect", "--format", "{{(index .NetworkSettings.Networks \""+nonce+"\").IPAddress}}", nonce+"-provider")
		identityArgs := []string{"--memory", "512m", "--memory-swap", "512m", "--cpus", "1", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
			"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--entrypoint", "/fixture/kyidentity", "-v", identityBinary + ":/fixture/kyidentity:ro", "-v", identityDir + ":/data",
			"-e", "KYIDENTITY_DATA_DIR=/data", "-e", "KYIDENTITY_PORT=8080", "-e", "KYIDENTITY_ISSUER_URL=" + providerURL, "-e", "TRUSTED_PROXY_CIDRS=" + proxyIP + "/32", "-e", "KYIDENTITY_ALLOW_PRIVATE_CALLBACKS=true"}
		if producer != nil {
			identityArgs = append(identityArgs, "-e", "SSL_CERT_FILE=/data/connector.pem")
		}
		run(nonce+"-identity", nativeImage, identityArgs...)
		identity = newNativeIdentityProvider(t, ctx, nonce, providerURL, ca, identityDir)
		identity.producer = producer
	}
	run(nonce+"-db", databaseImage, "--memory", "768m", "--memory-swap", "768m", "--env-file", env)
	run(nonce+"-cache", cacheImage, "--memory", "128m", "--memory-swap", "128m")
	for deadline := time.Now().Add(30 * time.Second); ; {
		dbReady := exec.CommandContext(ctx, "docker", "exec", nonce+"-db", "pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-d", "immich").Run() == nil
		cache, err := exec.CommandContext(ctx, "docker", "exec", nonce+"-cache", "valkey-cli", "ping").Output()
		if dbReady && err == nil && strings.TrimSpace(string(cache)) == "PONG" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned native database/cache prerequisites did not become ready")
		}
		time.Sleep(200 * time.Millisecond)
	}
	run(nonce+"-server", serverImage, "--memory", "2g", "--memory-swap", "2g", "--cpus", "2", "--env-file", env, "-p", "127.0.0.1::2283", "-v", caFile+":/fixture/provider-ca.pem:ro")
	var ports map[string][]struct{ HostIP, HostPort string }
	if json.Unmarshal([]byte(docker("inspect", "--format", "{{json .NetworkSettings.Ports}}", nonce+"-server")), &ports) != nil || len(ports["2283/tcp"]) != 1 || ports["2283/tcp"][0].HostIP != "127.0.0.1" {
		t.Fatal("native fixture port is not loopback")
	}
	backend, _ := url.Parse("http://127.0.0.1:" + ports["2283/tcp"][0].HostPort)
	proxy := httputil.NewSingleHostReverseProxy(backend)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "fixture backend unavailable", http.StatusServiceUnavailable)
	}
	var failBridgeWrite atomic.Bool
	var providerWrites atomic.Int32
	front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/api/system-config" {
			providerWrites.Add(1)
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/kyquickstart/revoke") && failBridgeWrite.Swap(false) {
			http.Error(w, "disposable write unavailable", http.StatusServiceUnavailable)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()
	client := front.Client()
	client.Timeout = 40 * time.Second
	for deadline := time.Now().Add(time.Minute); ; {
		response, err := client.Get(front.URL + "/api/server/version")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Log("owned server state:", docker("inspect", "--format", "{{.State.Status}} exit={{.State.ExitCode}} oom={{.State.OOMKilled}}", nonce+"-server"))
			logs, _ := exec.Command("docker", "logs", nonce+"-server").CombinedOutput()
			if file, err := os.CreateTemp(os.TempDir(), "kyq-immich-startup-*.log"); err == nil {
				if _, err := file.Write(logs); err == nil {
					t.Log("private startup diagnostic:", file.Name())
				}
				file.Close()
			}
			for _, code := range regexp.MustCompile(`\b(?:[A-Z][A-Z0-9_]{3,}|[A-Za-z]+Error)\b`).FindAllString(string(logs), 12) {
				t.Log("owned startup error category:", code)
			}
			t.Fatal("owned native Immich did not start")
		}
		time.Sleep(200 * time.Millisecond)
	}
	request := func(method, path, token string, body any, out any) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		req, _ := http.NewRequestWithContext(ctx, method, front.URL+"/api"+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("native fixture %s %s did not complete", method, path)
		}
		defer response.Body.Close()
		raw, err = io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if err != nil || len(raw) > 1<<20 || response.StatusCode < 200 || response.StatusCode >= 300 {
			log, _ := exec.Command("docker", "logs", nonce+"-server").CombinedOutput()
			for _, code := range regexp.MustCompile(`(?:OAUTH|ERR|UND_ERR)_[A-Z_]+`).FindAllString(string(log), -1) {
				t.Log("native discovery error code: " + code)
			}
			for _, reason := range []string{"ETIMEDOUT", "ECONNREFUSED", "EACCES", "ENOTFOUND", "self-signed certificate", "unable to get local issuer certificate", "Error in OAuth discovery", "certificate verify failed", "fetch failed", "invalid URL", "unexpected HTTP response status code", "operation has timed out", "discovery response"} {
				if bytes.Contains(log, []byte(reason)) || bytes.Contains(raw, []byte(reason)) {
					t.Log("native failure category: " + reason)
				}
			}
			t.Fatalf("native fixture %s %s refused (HTTP %d)", method, path, response.StatusCode)
		}
		if out != nil && json.Unmarshal(raw, out) != nil {
			t.Fatal("native fixture response malformed")
		}
	}
	var bootstrap struct{ ID string }
	request(http.MethodPost, "/auth/admin-sign-up", "", map[string]string{"name": "Fixture bootstrap", "email": "bootstrap@example.test", "password": "disposable-bootstrap-password"}, &bootstrap)
	var login struct{ AccessToken string }
	request(http.MethodPost, "/auth/login", "", map[string]string{"email": "bootstrap@example.test", "password": "disposable-bootstrap-password"}, &login)
	if bootstrap.ID == "" || login.AccessToken == "" {
		t.Fatal("native bootstrap receipt missing")
	}
	var key struct{ Secret string }
	request(http.MethodPost, "/api-keys", login.AccessToken, map[string]any{"name": "fixture-adapter", "permissions": []string{"all"}}, &key)
	if key.Secret == "" {
		t.Fatal("native API credential missing")
	}
	c, err := New(front.URL, key.Secret)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = client.Transport
	preflight, err := c.InspectProviderConfiguration(ctx)
	if err != nil {
		t.Fatal("native provider preflight unavailable", err)
	}
	if os.Getenv("KYQ_TEST_IMMICH_MULTI") == "1" {
		producer.Cross = newNativeCrossForgejo(t, ctx, nonce, ca, run, docker)
	}
	provider := ProviderSpec{Issuer: providerURL, ClientID: "fixture-client", ClientSecret: "disposable-client-secret", RolesClaim: "roles"}
	if identity != nil {
		provider.ClientSecret = nativeIdentityClientSecret
		subjects = identity.setup(t, front.URL)
		if producer != nil && producer.Cross != nil {
			producer.Cross.prepare(t, ctx, identity)
		}
	}
	var setup nativeSetupReport
	var expectedProviderWrites int32 = 1
	if os.Getenv("KYQ_TEST_IMMICH_SETUP") == "1" {
		setup = qualifyNativeSetup(t, ctx, c, nonce+"-server", provider, subjects)
		preflight, err = c.InspectProviderConfiguration(ctx)
		if err != nil {
			t.Fatal("native setup provider readback failed")
		}
		expectedProviderWrites = 0
	}
	configured, err := c.ConfigureProvider(ctx, preflight, provider)
	if err != nil || (expectedProviderWrites == 1 && configured == preflight) || providerWrites.Load() != expectedProviderWrites {
		t.Fatal("native provider configuration not verified", err)
	}
	if repeated, err := c.ConfigureProvider(ctx, configured, provider); err != nil || repeated != configured || providerWrites.Load() != expectedProviderWrites {
		t.Fatal("native provider repeat rewrote configuration", err)
	}
	foreignProvider := provider
	foreignProvider.ClientID = "foreign-client"
	if _, err := c.ConfigureProvider(ctx, configured, foreignProvider); err == nil || providerWrites.Load() != expectedProviderWrites {
		t.Fatal("native configured provider was replaced")
	}
	if _, err := c.ConfigureProvider(ctx, strings.Repeat("0", 64), provider); err == nil || providerWrites.Load() != expectedProviderWrites {
		t.Fatal("native stale preflight authorized configuration")
	}
	if os.Getenv("KYQ_TEST_IMMICH_CREATE") == "1" {
		qualifyNativeCreation(t, ctx, c, provider, subjects["daily"], login.AccessToken)
	}
	sessions := map[string]string{}
	callbackURL := func(actor string) string {
		t.Helper()
		verifier := strings.Repeat("a", 43)
		digest := sha256.Sum256([]byte(verifier))
		challenge := base64.RawURLEncoding.EncodeToString(digest[:])
		state := "owned-" + actor
		var authorize struct{ URL string }
		redirect := front.URL + "/auth/login"
		request(http.MethodPost, "/oauth/authorize", "", map[string]string{"redirectUri": redirect, "state": state, "codeChallenge": challenge}, &authorize)
		parsed, err := url.Parse(authorize.URL)
		if err != nil || parsed.Scheme+"://"+parsed.Host != providerURL || parsed.Query().Get("state") != state || parsed.Query().Get("code_challenge") != challenge {
			t.Fatal("native OAuth authorization not pinned")
		}
		if identity != nil {
			return identity.authorize(t, actor, authorize.URL, redirect, true)
		}
		return redirect + "?code=" + actor + "&state=" + state
	}
	if len(setup.Accounts) != 0 && identity == nil {
		body, _ := json.Marshal(map[string]string{"url": callbackURL("unprovisioned"), "state": "owned-unprovisioned", "codeVerifier": strings.Repeat("a", 43)})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, front.URL+"/api/oauth/callback", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("managed first-login refusal probe failed")
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatal("managed first login bypassed owned provisioning", response.StatusCode)
		}
		unprovisioned := Binding{Issuer: providerURL, ClientID: "fixture-client", Subject: "owned-unprovisioned"}
		if c.SubjectAbsent(ctx, unprovisioned, "271333e2-c221-4601-b249-563b3d828530") != nil {
			t.Fatal("refused first login created or adopted a native account")
		}
		t.Log("managed first login refuses signed unprovisioned subject and preserves native absence")
	}
	signin := func(actor string) Binding {
		t.Helper()
		var result struct {
			UserID, AccessToken string
			IsAdmin             bool
		}
		request(http.MethodPost, "/oauth/callback", "", map[string]string{"url": callbackURL(actor), "state": "owned-" + actor, "codeVerifier": strings.Repeat("a", 43)}, &result)
		if result.UserID == "" || result.AccessToken == "" || result.IsAdmin != (actor == "owner") {
			t.Fatal("native OAuth account or authority incorrect")
		}
		sessions[actor] = result.AccessToken
		return Binding{ID: result.UserID, Issuer: providerURL, ClientID: "fixture-client", Subject: subjects[actor]}
	}
	owner, daily := signin("owner"), signin("daily")
	if len(setup.Accounts) != 0 {
		for _, route := range []string{"link", "unlink"} {
			body, _ := json.Marshal(map[string]string{"url": front.URL + "/auth/login?code=disposable&state=owned", "state": "owned", "codeVerifier": strings.Repeat("a", 43)})
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, front.URL+"/api/oauth/"+route, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+sessions["daily"])
			response, err := client.Do(req)
			if err != nil {
				t.Fatal("managed native identity mutation probe failed")
			}
			response.Body.Close()
			if response.StatusCode != http.StatusForbidden {
				t.Fatal("managed identity mutation was not refused", route, response.StatusCode)
			}
		}
		if _, err := c.Inspect(ctx, daily); err != nil {
			t.Fatal("managed identity mutation changed owned subject", err)
		}
		t.Log("native managed OAuth link/unlink refusal retains subject binding")
	}
	if identity != nil {
		verifier := strings.Repeat("a", 43)
		digest := sha256.Sum256([]byte(verifier))
		for _, actor := range []string{"unassigned", "other-owner"} {
			var authorize struct{ URL string }
			request(http.MethodPost, "/oauth/authorize", "", map[string]string{"redirectUri": front.URL + "/auth/login", "state": "owned-" + actor, "codeChallenge": base64.RawURLEncoding.EncodeToString(digest[:])}, &authorize)
			identity.authorize(t, actor, authorize.URL, front.URL+"/auth/login", false)
		}
		for _, actor := range []string{"owner", "daily"} {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, front.URL+"/api/admin/users", nil)
			req.Header.Set("Authorization", "Bearer "+sessions[actor])
			response, err := client.Do(req)
			if err != nil {
				t.Fatal("native administrator authorization probe failed")
			}
			response.Body.Close()
			if actor == "owner" && response.StatusCode != http.StatusOK || actor == "daily" && response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
				t.Fatal("real identity native administrator authorization incorrect")
			}
		}
		t.Log("real KyIdentity PKCE code exchange verifies native administrator API access, everyday denial and assigned-only refusal for ordinary and global-administrator identities")
	}
	if len(setup.Accounts) != 0 && (setup.Accounts[0].ID != owner.ID || setup.Accounts[1].ID != daily.ID) {
		t.Fatal("signed login did not use product-owned setup receipts")
	}
	account, err := c.Inspect(ctx, owner)
	if err != nil || !account.Administrator {
		t.Fatal("native owner administration not verified", err)
	}
	account, err = c.Inspect(ctx, daily)
	if err != nil || account.Administrator {
		t.Fatal("native everyday identity incorrect", err)
	}
	for _, admin := range []bool{true, true, false, false} {
		if c.SetAdministrator(ctx, daily, admin) != nil {
			t.Fatal("native grant/repeat/demotion failed")
		}
	}
	wrong := daily
	wrong.Subject = "foreign-subject"
	if c.SetAdministrator(ctx, wrong, true) == nil {
		t.Fatal("native foreign account accepted")
	}
	account, err = c.Inspect(ctx, daily)
	if err != nil || account.Administrator || account.Name != "daily" || account.Email != "daily@example.test" {
		t.Fatal("native identity/profile changed", err)
	}

	if moduleImage != "" && os.Getenv("KYQ_TEST_IMMICH_SETUP") == "1" {
		qualifyNativeDirectory(t, ctx, c, nonce+"-server", daily, sessions["daily"], client, func() string { return callbackURL("daily") })
	}

	if producer != nil {
		if os.Getenv("KYQ_TEST_IMMICH_CREATE") == "1" {
			producer.NewSubject = subjects["unassigned"]
		}
		if producer.Cross != nil && (producer.Cross.AppID == "" || producer.Cross.ConnectorID == "") {
			t.Fatal("cross-product identity receipts missing")
		}
		producerStop = startNativeImmichProducerBridge(t, ctx, producer, nonce, moduleImage, key.Secret, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: front.Certificate().Raw}), front.URL, owner, daily)
		defer producerStop()
		producerAdmin = identity.client()
		identity.login(t, producerAdmin, "owner")
		identity.waitDelivery(t, producerAdmin, "owner", true, 0)
		producerDailyRevision = identity.waitDelivery(t, producerAdmin, "daily", true, 0)
		profilePath := "/api/admin/users/" + daily.Subject
		identity.mutate(t, producerAdmin, http.MethodPut, profilePath, map[string]string{"displayName": "Producer daily", "email": "producer-daily@example.test"})
		producerDailyRevision = identity.waitDelivery(t, producerAdmin, "daily", true, producerDailyRevision)
		projected, err := c.InspectDirectory(ctx, daily, "271333e2-c221-4601-b249-563b3d828530")
		if err != nil || projected.Profile != (Profile{"Producer daily", "producer-daily@example.test", false}) {
			t.Fatal("actual producer native profile not verified", err)
		}
		rolePath := identity.administratorRolePath(t, producerAdmin, "daily")
		identity.mutate(t, producerAdmin, http.MethodPut, rolePath, nil)
		producerDailyRevision = identity.waitDelivery(t, producerAdmin, "daily", true, producerDailyRevision)
		adminProbe := func() int {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, front.URL+"/api/admin/users", nil)
			req.Header.Set("Authorization", "Bearer "+sessions["daily"])
			response, err := client.Do(req)
			if err != nil {
				t.Fatal("actual producer old-session administrator probe failed")
			}
			response.Body.Close()
			return response.StatusCode
		}
		if adminProbe() != http.StatusOK {
			t.Fatal("actual producer role grant did not authorize old session")
		}
		identity.mutate(t, producerAdmin, http.MethodDelete, rolePath, nil)
		producerDailyRevision = identity.waitDelivery(t, producerAdmin, "daily", true, producerDailyRevision)
		if status := adminProbe(); status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Fatal("actual producer role removal retained old-session administration")
		}
		identity.mutate(t, producerAdmin, http.MethodPut, profilePath, map[string]string{"displayName": "daily", "email": "daily@example.test"})
		producerDailyRevision = identity.waitDelivery(t, producerAdmin, "daily", true, producerDailyRevision)
		projected, err = c.InspectDirectory(ctx, daily, "271333e2-c221-4601-b249-563b3d828530")
		if err != nil || projected.Profile != (Profile{"daily", "daily@example.test", false}) || projected.Revision != 8 {
			t.Fatal("actual producer final profile/role revision conflicts", err)
		}
		t.Log("actual KyIdentity worker acknowledged owned initial profiles, name/email updates, role grant/removal and old-session administrator denial")
		if producer.NewSubject != "" {
			qualifyNativeProducerCreation(t, ctx, c, identity, producerAdmin, signin, request, client, sessions)
		}
	}
	var ordinaryKey struct{ Secret string }
	request(http.MethodPost, "/api-keys", sessions["daily"], map[string]any{"name": "retained-personal-key", "permissions": []string{"all"}}, &ordinaryKey)
	if ordinaryKey.Secret == "" {
		t.Fatal("native ordinary key missing")
	}
	var photoPath, photoDigest string
	if moduleImage != "" {
		var photo bytes.Buffer
		if png.Encode(&photo, image.NewRGBA(image.Rect(0, 0, 2, 2))) != nil {
			t.Fatal("fixture photo encoding failed")
		}
		digest := sha256.Sum256(photo.Bytes())
		photoDigest = fmt.Sprintf("%x", digest)
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		for field, value := range map[string]string{"deviceAssetId": nonce, "deviceId": "owned-fixture", "fileCreatedAt": time.Now().UTC().Format(time.RFC3339), "fileModifiedAt": time.Now().UTC().Format(time.RFC3339)} {
			if form.WriteField(field, value) != nil {
				t.Fatal("fixture photo metadata failed")
			}
		}
		file, err := form.CreateFormFile("assetData", "retained.png")
		if err != nil {
			t.Fatal("fixture photo form failed")
		}
		if _, err := file.Write(photo.Bytes()); err != nil || form.Close() != nil {
			t.Fatal("fixture photo body failed")
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, front.URL+"/api/assets", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+sessions["daily"])
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("fixture photo upload failed")
		}
		var uploaded struct{ ID string }
		err = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&uploaded)
		response.Body.Close()
		if response.StatusCode != http.StatusCreated || err != nil || !uuid.MatchString(uploaded.ID) {
			t.Fatal("fixture photo upload not verified")
		}
		var asset struct{ OriginalPath string }
		request(http.MethodGet, "/assets/"+uploaded.ID, sessions["daily"], nil, &asset)
		photoPath = asset.OriginalPath
		if !strings.HasPrefix(photoPath, "/data/upload/") || !strings.HasSuffix(photoPath, ".png") || !strings.HasPrefix(docker("exec", nonce+"-server", "sha256sum", photoPath), photoDigest+" ") {
			t.Fatal("fixture native photo ownership/storage not verified")
		}
	}
	if identity == nil {
		private, err := x509.ParsePKCS1PrivateKey(material.Key)
		if err != nil {
			t.Fatal(err)
		}
		logout, err := signedNativeToken(private, map[string]any{"iss": providerURL, "aud": "fixture-client", "sub": daily.Subject, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(), "jti": nonce + "-logout", "events": map[string]any{"http://schemas.openid.net/event/backchannel-logout": map[string]any{}}})
		if err != nil {
			t.Fatal(err)
		}
		request(http.MethodPost, "/oauth/backchannel-logout", "", map[string]string{"logout_token": logout}, nil)
	}
	probe := func(token string, apiKey bool) int {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, front.URL+"/api/users/me", nil)
		if apiKey {
			req.Header.Set("x-api-key", token)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("native access probe failed")
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if identity == nil && probe(sessions["daily"], false) != http.StatusUnauthorized {
		t.Fatal("native back-channel logout did not revoke old session")
	}
	if probe(ordinaryKey.Secret, true) != http.StatusOK {
		t.Fatal("native personal key retention assumption changed; requalify offboarding")
	}
	if probe(sessions["owner"], false) != http.StatusOK {
		t.Fatal("native logout revoked unrelated owner")
	}
	if identity == nil {
		t.Log("native signed back-channel logout denies old everyday session and preserves unrelated owner; personal API key remains usable, so complete offboarding requires an additional native interface")
	}
	t.Log("native signed OAuth owner/everyday receipts, provider inspection, administrator grant/repeat/demotion and foreign-subject refusal verified; content deletion was not called")
	if moduleImage != "" {
		setupID := "271333e2-c221-4601-b249-563b3d828530"
		if fresh := signin("daily"); fresh != daily {
			t.Fatal("native repeat login changed owned identity")
		}
		before, err := c.InspectAccess(ctx, daily, setupID)
		if err != nil || before.Revoked || before.APIKeys != 1 || before.Sessions < 1 {
			t.Fatal("native module initial access proof failed", err)
		}
		var stored struct{ Images, Videos, Total *int64 }
		if c.call(ctx, http.MethodGet, "/admin/users/"+daily.ID+"/statistics", nil, &stored) != nil || stored.Images == nil || *stored.Images != 1 || stored.Total == nil || *stored.Total != 1 {
			t.Fatal("native uploaded photo record not verified")
		}
		dialer := websocket.Dialer{TLSClientConfig: client.Transport.(*http.Transport).TLSClientConfig.Clone(), HandshakeTimeout: 5 * time.Second}
		socket, _, err := dialer.DialContext(ctx, strings.Replace(front.URL, "https://", "wss://", 1)+"/api/socket.io/?EIO=4&transport=websocket", http.Header{"X-Api-Key": []string{ordinaryKey.Secret}})
		if err != nil {
			t.Fatal("native owned socket connection failed")
		}
		defer socket.Close()
		socket.SetReadLimit(8192)
		socket.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, frame, err := socket.ReadMessage()
		if err != nil || !bytes.HasPrefix(frame, []byte("0{")) || socket.WriteMessage(websocket.TextMessage, []byte("40")) != nil {
			t.Fatal("native socket transport handshake failed")
		}
		_, frame, err = socket.ReadMessage()
		if err != nil || !bytes.HasPrefix(frame, []byte("40{")) {
			t.Fatal("native socket account subscription failed")
		}
		socket.SetReadDeadline(time.Now().Add(3 * time.Minute))
		socketClosed := make(chan error, 1)
		socketJoined := make(chan struct{})
		go func() {
			defer close(socketJoined)
			for {
				_, message, err := socket.ReadMessage()
				if err != nil {
					socketClosed <- err
					return
				}
				if bytes.Equal(message, []byte("2")) {
					if err := socket.WriteMessage(websocket.TextMessage, []byte("3")); err != nil {
						socketClosed <- err
						return
					}
				}
			}
		}()
		defer func() { socket.Close(); <-socketJoined }()
		body := map[string]string{"setupId": setupID, "issuer": daily.Issuer, "clientId": daily.ClientID, "subject": "foreign-subject"}
		var refused json.RawMessage
		if c.call(ctx, http.MethodPost, "/admin/users/"+daily.ID+"/kyquickstart/revoke", body, &refused) == nil || probe(ordinaryKey.Secret, true) != http.StatusOK {
			t.Fatal("native API accepted a foreign subject or changed access")
		}
		body["subject"] = daily.Subject
		var ownerKey struct{ Secret string }
		request(http.MethodPost, "/api-keys", sessions["owner"], map[string]any{"name": "self-revocation-probe", "permissions": []string{"all"}}, &ownerKey)
		ownerClient, err := New(front.URL, ownerKey.Secret)
		if err != nil {
			t.Fatal("native self-revocation authority unavailable")
		}
		ownerClient.http.Transport = client.Transport
		ownerBody := map[string]string{"setupId": setupID, "issuer": owner.Issuer, "clientId": owner.ClientID, "subject": owner.Subject}
		if ownerClient.call(ctx, http.MethodPost, "/admin/users/"+owner.ID+"/kyquickstart/revoke", ownerBody, &refused) == nil || probe(sessions["owner"], false) != http.StatusOK {
			t.Fatal("native module accepted self-revocation")
		}
		encoded, _ := json.Marshal(body)
		cookieRequest, _ := http.NewRequestWithContext(ctx, http.MethodPost, front.URL+"/api/admin/users/"+daily.ID+"/kyquickstart/revoke", bytes.NewReader(encoded))
		cookieRequest.Header.Set("Content-Type", "application/json")
		cookieRequest.Header.Set("Authorization", "Bearer "+sessions["owner"])
		cookieResponse, err := client.Do(cookieRequest)
		if err != nil {
			t.Fatal("native session-only authority probe failed")
		}
		cookieResponse.Body.Close()
		if cookieResponse.StatusCode != http.StatusForbidden || probe(ordinaryKey.Secret, true) != http.StatusOK {
			t.Fatal("native module accepted session-only authority")
		}
		wrong := daily
		wrong.Subject = "foreign-subject"
		if c.RevokeAccess(ctx, wrong, setupID) == nil || probe(ordinaryKey.Secret, true) != http.StatusOK {
			t.Fatal("foreign revocation mutated native access")
		}
		if producer != nil {
			producerFreshCallback = callbackURL("daily")
			failBridgeWrite.Store(true)
			identity.mutate(t, producerAdmin, http.MethodDelete, "/api/admin/app-registry/"+identity.appID+"/assignments/users/"+daily.Subject, nil)
			producerDailyRevision = identity.waitDelivery(t, producerAdmin, "daily", false, producerDailyRevision)
			if failBridgeWrite.Load() {
				t.Fatal("actual producer did not exercise unavailable revocation")
			}
			if producer.Cross != nil {
				cross := producer.Cross
				account, err := cross.Client.InspectDirectory(ctx, cross.Bindings[1])
				if err != nil || !account.Active || account.LoginProhibited || cross.probe(t, cross.DailySession, "/user/settings") != http.StatusOK {
					t.Fatal("single-app assignment loss changed other product access", err)
				}
				t.Log("Immich assignment loss preserves the same actor's native Forgejo account and existing session")
			}
			producerStop()
		} else if os.Getenv("KYQ_TEST_IMMICH_BRIDGE") == "1" {
			binary := filepath.Join(directory, "immich-bridge.test")
			compile := exec.CommandContext(ctx, "go", "test", "-race", "-p", "1", "-c", "-o", binary, "../../offboard")
			if output, err := compile.CombinedOutput(); err != nil {
				t.Fatalf("native bridge fixture compile failed: %s", output)
			}
			input, err := json.Marshal(map[string]any{"Container": nonce + "-server", "Image": moduleImage, "Owner": nonce, "Token": key.Secret,
				"Directory": os.Getenv("KYQ_TEST_IMMICH_SETUP") == "1",
				"CA":        pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: front.Certificate().Raw}),
				"Binding":   map[string]any{"setup_id": setupID, "app": "photos", "kind": "immich", "origin": front.URL, "issuer": daily.Issuer, "subject": daily.Subject, "native_id": 0, "native_uuid": daily.ID, "client_id": daily.ClientID}})
			if err != nil {
				t.Fatal("native bridge fixture input failed")
			}
			bridge := exec.CommandContext(ctx, binary, "-test.run=^TestNativeImmichQueue$", "-test.v")
			bridge.Stdin = bytes.NewReader(input)
			failBridgeWrite.Store(true)
			if output, err := bridge.CombinedOutput(); err != nil {
				t.Fatalf("native bridge fixture failed: %s", output)
			}
			if failBridgeWrite.Load() {
				t.Fatal("native bridge fixture did not exercise the unavailable write")
			}
		} else if c.RevokeAccess(ctx, daily, setupID) != nil {
			t.Fatal("native content-preserving revocation failed")
		}
		select {
		case err := <-socketClosed:
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived, websocket.CloseAbnormalClosure) {
				t.Fatal("native revoked socket did not close")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("native revoked socket kept receiving events")
		}
		proof, err := c.InspectAccess(ctx, daily, setupID)
		if err != nil || !proof.Revoked || proof.APIKeys != 0 || proof.Sessions != 0 || probe(ordinaryKey.Secret, true) != http.StatusUnauthorized || probe(sessions["daily"], false) != http.StatusUnauthorized || probe(sessions["owner"], false) != http.StatusOK {
			t.Fatal("native revoked access or unrelated owner not verified", err)
		}
		if os.Getenv("KYQ_TEST_IMMICH_SETUP") == "1" {
			directory, err := c.InspectDirectory(ctx, daily, setupID)
			expectedRevision := int64(4)
			if os.Getenv("KYQ_TEST_IMMICH_BRIDGE") == "1" {
				expectedRevision += 4
			}
			if err != nil || !directory.Revoked || directory.Revision != expectedRevision || c.ApplyDirectory(ctx, daily, setupID, directory.Profile) == nil {
				t.Fatal("native directory reactivated or modified a revoked account", err)
			}
		}

		if c.RevokeAccess(ctx, daily, setupID) != nil {
			t.Fatal("native repeat revocation failed")
		}
		repeated, err := c.InspectAccess(ctx, daily, setupID)
		if err != nil || repeated != proof || c.RevokeAccess(ctx, daily, "371333e2-c221-4601-b249-563b3d828530") == nil {
			t.Fatal("native fence changed on repeat or foreign installation accepted")
		}
		body["setupId"] = "371333e2-c221-4601-b249-563b3d828530"
		if c.call(ctx, http.MethodPost, "/admin/users/"+daily.ID+"/kyquickstart/revoke", body, &refused) == nil {
			t.Fatal("native API accepted a foreign installation fence")
		}
		retained, err := c.Inspect(ctx, daily)
		if err != nil || retained != account || !strings.HasPrefix(docker("exec", nonce+"-server", "sha256sum", photoPath), photoDigest+" ") {
			t.Fatal("native revocation changed account or photo bytes", err)
		}
		var retainedStats struct{ Images, Videos, Total *int64 }
		if c.call(ctx, http.MethodGet, "/admin/users/"+daily.ID+"/statistics", nil, &retainedStats) != nil || retainedStats.Images == nil || retainedStats.Total == nil || *retainedStats.Images != *stored.Images || *retainedStats.Total != *stored.Total {
			t.Fatal("native revocation changed retained photo records")
		}
		freshCallback := producerFreshCallback
		if freshCallback == "" {
			freshCallback = callbackURL("daily")
		}
		callback, _ := json.Marshal(map[string]string{"url": freshCallback, "state": "owned-daily", "codeVerifier": strings.Repeat("a", 43)})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, front.URL+"/api/oauth/callback", bytes.NewReader(callback))
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("native revoked login probe failed")
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("revoked OAuth callback did not refuse the retained authorization code (HTTP %d)", response.StatusCode)
		}
		if producer != nil {
			// Assignment loss can reject code exchange before Immich sees signed
			// claims. Separately prove that identity refuses a new authorization.
			digest := sha256.Sum256([]byte(strings.Repeat("a", 43)))
			var authorize struct{ URL string }
			request(http.MethodPost, "/oauth/authorize", "", map[string]string{"redirectUri": front.URL + "/auth/login", "state": "owned-daily", "codeChallenge": base64.RawURLEncoding.EncodeToString(digest[:])}, &authorize)
			identity.authorize(t, "daily", authorize.URL, front.URL+"/auth/login", false)
			t.Log("actual assignment loss refuses retained code exchange and new identity authorization; native credentials and content checks passed separately")
		}
		if os.Getenv("KYQ_TEST_IMMICH_SETUP") == "1" {
			command := exec.CommandContext(ctx, "docker", "exec", "-e", "NODE_OPTIONS=--max-old-space-size=512", nonce+"-server", "immich-admin", "apply-setup", "--file", "/usr/src/app/kyquickstart/setup.json", "--resume")
			output, err := command.CombinedOutput()
			if err == nil || len(output) > 65536 || strings.Contains(string(output), provider.ClientSecret) {
				t.Fatal("native setup resumed a revoked account or leaked credentials")
			}
			afterRefusal, err := c.InspectAccess(ctx, daily, setupID)
			if err != nil || !afterRefusal.Revoked || afterRefusal.RevokedAt != repeated.RevokedAt || afterRefusal.APIKeys != 0 || afterRefusal.Sessions != 0 {
				t.Fatal("native setup refusal changed retained revocation")
			}
		}
		if producer == nil {
			t.Log("community module denies personal key and new signed login, preserves owner/account/photo bytes and verifies permanent installation-bound repeat")
		} else {
			t.Log("actual producer verifies scoped native credential denial, owner/account/photo retention and permanent installation-bound repeat")
		}
	}
}
