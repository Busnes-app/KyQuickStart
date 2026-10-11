package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The real provider owns disposable target files and one labeled container. Only
// its selected binary/image run; no existing service or workstation credentials
// are consulted. The image supplies a userspace, not an identity implementation.
type nativeIdentityFixture struct {
	subjects    map[string]string
	connectorID string
	groupID     string
}

func newNativeFixtureOIDC(t *testing.T, ctx context.Context, appOrigin, image, binary string, connector *nativeProducerPlan) *fixtureOIDC {
	t.Helper()
	st, err := os.Stat(binary)
	if err != nil || !filepath.IsAbs(binary) || filepath.Clean(binary) != binary || !st.Mode().IsRegular() {
		t.Fatal("native identity fixture needs an explicit absolute regular binary")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_, port, _ := net.SplitHostPort(address)
	_ = listener.Close()
	backend, _ := url.Parse("http://" + address)
	f := newFixtureOIDC(t, backend)
	f.native = &nativeIdentityFixture{subjects: map[string]string{}}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("kyq-forgejo-identity-%d", time.Now().UnixNano())
	docker := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).Output()
		if err != nil {
			t.Fatalf("owned identity fixture Docker %s failed", args[0])
		}
		return out
	}
	gateway := strings.TrimSpace(string(docker("network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}")))
	if net.ParseIP(gateway).To4() == nil {
		t.Fatal("owned identity fixture needs a pinned IPv4 Docker gateway")
	}
	trust, err := os.ReadFile(f.caFile)
	if err != nil {
		t.Fatal(err)
	}
	if connector != nil {
		trust = append(trust, connector.Certificate...)
	}
	if err := os.WriteFile(filepath.Join(directory, "fixture-ca.pem"), trust, 0600); err != nil {
		t.Fatal(err)
	}
	network := []string{"-p", "127.0.0.1:" + port + ":" + port}
	listenHost := ""
	privateCallbacks := "false"
	if connector != nil {
		// Refuse an older binary before granting host-network access. An isolated
		// no-network probe must reject the malformed bind setting explicitly.
		probeName := name + "-bind-probe"
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		probe := exec.CommandContext(probeCtx, "docker", "run", "--rm", "--name", probeName, "--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
			"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--entrypoint", "/fixture/kyidentity", "-v", binary+":/fixture/kyidentity:ro",
			"-e", "KYIDENTITY_DATA_DIR=/tmp/kyq-bind-probe", "-e", "KYIDENTITY_LISTEN_HOST=invalid-bind-host", image)
		out, err := probe.CombinedOutput()
		cancel()
		_ = exec.Command("docker", "rm", "-fv", probeName).Run()
		if err == nil || !strings.Contains(string(out), "KYIDENTITY_LISTEN_HOST must be a literal IP address") {
			t.Fatal("producer fixture binary lacks verified explicit bind support")
		}
		network = []string{"--network", "host"}
		listenHost = "127.0.0.1"
		gateway = "127.0.0.1"
		privateCallbacks = "true"
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", name).Run() })
	args := []string{"run", "-d", "--name", name, "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--label", "kyq.fixture=forgejo-identity", "--label", "kyq.fixture-owner=" + name,
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--entrypoint", "/fixture/kyidentity",
		"-v", binary + ":/fixture/kyidentity:ro", "-v", directory + ":/data",
		"-e", "KYIDENTITY_DATA_DIR=/data", "-e", "KYIDENTITY_PORT=" + port,
		"-e", "KYIDENTITY_ISSUER_URL=" + f.server.URL, "-e", "TRUSTED_PROXY_CIDRS=" + gateway + "/32",
		"-e", "SSL_CERT_FILE=/data/fixture-ca.pem", "-e", "KYIDENTITY_ALLOW_PRIVATE_CALLBACKS=" + privateCallbacks, "-e", "KYIDENTITY_LISTEN_HOST=" + listenHost}
	args = append(args, network...)
	args = append(args, image)
	docker(args...)
	ready := &http.Client{Timeout: time.Second}
	for deadline := time.Now().Add(30 * time.Second); ; {
		response, err := ready.Get(backend.String() + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("owned native identity fixture did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	pairs := [][]string{{"owner", "daily"}, {"other-owner", "unassigned"}}
	if os.Getenv("KYQ_TEST_FORGEJO_CREATE") == "1" {
		pairs = append(pairs, []string{"new-owner", "new-daily"})
	}
	for _, actors := range pairs {
		folder := filepath.Join(directory, "setup-"+actors[0])
		if err := os.Mkdir(folder, 0700); err != nil {
			t.Fatal(err)
		}
		write := func(filename string, data []byte) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(folder, filename), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		prefix := "/data/" + filepath.Base(folder) + "/"
		accounts := []map[string]string{}
		for _, actor := range actors {
			write(actor+".password", []byte("disposable-identity-"+actor+"-password"))
			accounts = append(accounts, map[string]string{"username": actor, "email": actor + "@example.test", "passwordFile": prefix + actor + ".password"})
		}
		write("client.secret", []byte("disposable-fixture-client-secret"))
		clients := []any{}
		if actors[0] == "owner" {
			client := map[string]any{"id": "fixture-client", "name": "Forgejo fixture", "secretFile": prefix + "client.secret",
				"redirectUris": []string{appOrigin + "/user/oauth2/kyidentity-fixture/callback"}, "postLogoutUris": []string{}, "administratorRole": "admin", "everydayRole": "editor"}
			if connector != nil {
				write("connector.token", []byte(connector.Token))
				client["scim"] = map[string]any{"url": connector.BaseURL, "tokenFile": prefix + "connector.token", "groups": os.Getenv("KYQ_TEST_FORGEJO_GROUPS") == "1"}
				if os.Getenv("KYQ_TEST_FORGEJO_GROUPS") == "1" {
					client["groups"] = []string{"Forgejo developers"}
				}
			}
			clients = append(clients, client)
		}
		// Separate setup UUIDs preserve two independently owned account pairs.
		setup := map[string]any{"schema": "kyidentity.setup/1", "setupId": map[string]string{"owner": "271333e2-c221-4601-b249-563b3d828530", "other-owner": "271333e2-c221-4601-b249-563b3d828531", "new-owner": "271333e2-c221-4601-b249-563b3d828532"}[actors[0]],
			"admin": accounts[0], "everyday": accounts[1], "clients": clients}
		if actors[0] == "owner" && os.Getenv("KYQ_TEST_FORGEJO_GROUPS") == "1" {
			setup["groups"] = []any{map[string]any{"name": "Forgejo developers", "members": []string{actors[1]}}}
		}
		data, _ := json.Marshal(setup)
		write("bundle.json", data)
		var report struct {
			Created []struct{ Kind, Name, ID string }
			Reused  []struct{ Kind, Name, ID string }
		}
		remote := prefix + "bundle.json"
		if json.Unmarshal(docker("exec", name, "/fixture/kyidentity", "apply-setup", "--file", remote), &report) != nil || len(report.Created) == 0 {
			t.Fatal("native identity setup receipt missing")
		}
		for _, resource := range report.Created {
			if resource.Kind == "user" {
				f.native.subjects[resource.Name] = resource.ID
			}
			if resource.Kind == "group" && resource.Name == "Forgejo developers" {
				f.native.groupID = resource.ID
			}
			if resource.Kind == "connector" && resource.Name == "fixture-client" {
				f.native.connectorID = resource.ID
			}
		}
		for _, flags := range [][]string{{"--check"}, {}} {
			args := append([]string{"exec", name, "/fixture/kyidentity", "apply-setup", "--file", remote}, flags...)
			var repeat struct{ Created, Reused []json.RawMessage }
			if json.Unmarshal(docker(args...), &repeat) != nil || len(repeat.Created) != 0 || len(repeat.Reused) != len(report.Created) {
				t.Fatal("native identity read-only check/resume changed owned resources")
			}
		}
	}
	if len(f.native.subjects) != 2*len(pairs) {
		t.Fatal("native identity subject receipts incomplete")
	}
	if os.Getenv("KYQ_TEST_FORGEJO_GROUPS") == "1" && (connector == nil || f.native.groupID == "") {
		t.Fatal("owned group connector receipt missing")
	}
	t.Log("real KyIdentity owned accounts/client/roles created, checked and resumed")
	return f
}

func (f *nativeIdentityFixture) login(t *testing.T, client *http.Client, issuer, who string) {
	t.Helper()
	response, err := client.Get(issuer + "/api/auth/csrf")
	if err != nil {
		t.Fatal("native identity CSRF request failed")
	}
	var csrf struct {
		Token string `json:"csrfToken"`
	}
	err = json.NewDecoder(response.Body).Decode(&csrf)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || csrf.Token == "" {
		t.Fatal("native identity CSRF token missing")
	}
	body, _ := json.Marshal(map[string]string{"username": who, "password": "disposable-identity-" + who + "-password"})
	request, _ := http.NewRequest(http.MethodPost, issuer+"/api/auth/login", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", issuer)
	request.Header.Set("X-CSRF-Token", csrf.Token)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal("native identity login request failed")
	}
	var result struct{ Success, MFARequired, Restricted bool }
	err = json.NewDecoder(response.Body).Decode(&result)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !result.Success || result.MFARequired || result.Restricted || strings.TrimSpace(f.subjects[who]) == "" {
		t.Fatal("native identity fixture login failed")
	}
}

func (f *nativeIdentityFixture) request(t *testing.T, client *http.Client, issuer, method, path string, data any, headers map[string]string, out any) {
	t.Helper()
	csrf := ""
	if method != http.MethodGet {
		var response struct {
			Token string `json:"csrfToken"`
		}
		f.request(t, client, issuer, http.MethodGet, "/api/auth/csrf", nil, nil, &response)
		csrf = response.Token
		if csrf == "" {
			t.Fatal("native identity operation CSRF missing")
		}
	}
	var body []byte
	if data != nil {
		body, _ = json.Marshal(data)
	}
	request, _ := http.NewRequest(method, issuer+path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", issuer)
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("native identity operation did not complete")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 || response.StatusCode != http.StatusOK || json.Unmarshal(raw, out) != nil {
		t.Fatalf("native identity operation refused or malformed (HTTP %d)", response.StatusCode)
	}
}

// changeAccess uses native, audited operation-bound step-up. It changes only
// this fixture's owned app or role assignment, not the global administrator.
func (f *nativeIdentityFixture) changeAccess(t *testing.T, client *http.Client, issuer, actor string, role bool) {
	t.Helper()
	var registry struct {
		Records []struct{ ID, ClientID, AccessMode string }
	}
	f.request(t, client, issuer, http.MethodGet, "/api/admin/app-registry", nil, nil, &registry)
	appID := ""
	for _, record := range registry.Records {
		if record.ClientID == "fixture-client" {
			if appID != "" || record.AccessMode != "assigned_only" || record.ID == "" {
				t.Fatal("native identity client scope conflicts")
			}
			appID = record.ID
		}
	}
	if appID == "" || f.subjects[actor] == "" {
		t.Fatal("native identity owned assignment missing")
	}
	path := "/api/admin/app-registry/" + appID
	if role {
		var roles struct{ Roles []struct{ ID, Name string } }
		f.request(t, client, issuer, http.MethodGet, path+"/roles", nil, nil, &roles)
		roleID := ""
		for _, record := range roles.Roles {
			if record.Name == "admin" {
				if roleID != "" || record.ID == "" {
					t.Fatal("native identity role scope conflicts")
				}
				roleID = record.ID
			}
		}
		if roleID == "" {
			t.Fatal("native identity owned administrator role missing")
		}
		path += "/roles/" + roleID
	}
	path += "/assignments/users/" + f.subjects[actor]
	var grant struct{ Kind, StepUpToken string }
	f.request(t, client, issuer, http.MethodPost, "/api/auth/step-up",
		map[string]string{"password": "disposable-identity-owner-password", "operation": "DELETE " + path}, nil, &grant)
	if grant.Kind != "grant" || grant.StepUpToken == "" {
		t.Fatal("native identity operation-bound grant missing")
	}
	var result struct{ Success bool }
	f.request(t, client, issuer, http.MethodDelete, path, nil, map[string]string{"X-KyIdentity-StepUp": grant.StepUpToken}, &result)
	if !result.Success {
		t.Fatal("native identity assignment removal failed")
	}
}

func (f *nativeIdentityFixture) changeProfile(t *testing.T, client *http.Client, issuer, actor string, profile Profile) {
	t.Helper()
	if f.subjects[actor] == "" {
		t.Fatal("native profile subject receipt missing")
	}
	path := "/api/admin/users/" + f.subjects[actor]
	var grant struct{ Kind, StepUpToken string }
	f.request(t, client, issuer, http.MethodPost, "/api/auth/step-up", map[string]string{"password": "disposable-identity-owner-password", "operation": "PUT " + path}, nil, &grant)
	if grant.Kind != "grant" || grant.StepUpToken == "" {
		t.Fatal("native profile operation-bound grant missing")
	}
	var result struct{ Success bool }
	f.request(t, client, issuer, http.MethodPut, path, map[string]string{"displayName": profile.Name, "email": profile.Email}, map[string]string{"X-KyIdentity-StepUp": grant.StepUpToken}, &result)
	if !result.Success {
		t.Fatal("native profile mutation failed")
	}
}

func (f *nativeIdentityFixture) assignAccess(t *testing.T, client *http.Client, issuer, actor string) {
	t.Helper()
	var registry struct {
		Records []struct{ ID, ClientID, AccessMode string }
	}
	f.request(t, client, issuer, http.MethodGet, "/api/admin/app-registry", nil, nil, &registry)
	appID := ""
	for _, record := range registry.Records {
		if record.ClientID == "fixture-client" {
			if appID != "" || record.AccessMode != "assigned_only" {
				t.Fatal("native assignment scope conflicts")
			}
			appID = record.ID
		}
	}
	if appID == "" || f.subjects[actor] == "" {
		t.Fatal("native assignment receipt missing")
	}
	path := "/api/admin/app-registry/" + appID + "/assignments/users/" + f.subjects[actor]
	var grant struct{ Kind, StepUpToken string }
	f.request(t, client, issuer, http.MethodPost, "/api/auth/step-up", map[string]string{"password": "disposable-identity-owner-password", "operation": "PUT " + path}, nil, &grant)
	if grant.Kind != "grant" || grant.StepUpToken == "" {
		t.Fatal("native assignment grant missing")
	}
	var result struct{ Success bool }
	f.request(t, client, issuer, http.MethodPut, path, nil, map[string]string{"X-KyIdentity-StepUp": grant.StepUpToken}, &result)
	if !result.Success {
		t.Fatal("native assignment failed")
	}
}
