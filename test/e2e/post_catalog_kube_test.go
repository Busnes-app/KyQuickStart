//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// catalogPostHost is where TestKubernetesNPMEdge routes the catalog KyPost, which runs on the
// Docker target beside KyDrive, behind the cluster's lan edge.
const catalogPostHost = "post.example.test"

const catalogPostURLHost = catalogPostHost + ":30443"

const postSetupDir = "/kypost/private/installer/5a8c1e3f-7b2d-4e6a-9f0c-1d2b3a4c5e6f"

// postContainer is the name the handover prints: Compose project kyq-kypost, service app.
const postContainer = "kyq-kypost-app-1"

// postRouteStep is the edge step that routes KyPost (the NPM host step's ID).
var postRouteStep = func() string {
	sum := sha256.Sum256([]byte(catalogPostHost))
	return "k8s.edge." + hex.EncodeToString(sum[:8])
}()

// postPlumbing is the test-only plumbing patched into the copied catalog KyPost.
var postPlumbing = []string{
	"extra_hosts: the identity host is the relay to the lan edge; Ollama's registry is unreachable",
	"SSL_CERT_FILE: the edge's throwaway certificate",
	"volume: that certificate, read-only",
}

// preparePost copies the catalog KyPost into catalogDir with test-only plumbing, each patch
// asserted present: identity.example.test is the relay to the lan edge, KyPost trusts the
// edge's throwaway certificate, and registry.ollama.ai is loopback, so the model pull (about
// 2.9 GB) fails and retries instead of downloading. It returns KyPost's forward port on d.forward.
func preparePost(t *testing.T, work, catalogDir string, d driveTarget) int {
	t.Helper()
	dir := filepath.Join(catalogDir, "kypost")
	if err := os.CopyFS(dir, os.DirFS("../../internal/catalog/apps/kypost")); err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(work, "drive-ca.pem")
	tmpl := filepath.Join(dir, "compose.yaml.tmpl")
	b, _ := os.ReadFile(tmpl)
	patched := string(b)
	for i, r := range [][2]string{
		{`    ports: ["{{forward}}:5866"]` + "\n", `    ports: ["{{forward}}:5866"]` + "\n    extra_hosts: [\"" + catalogIdentityHost + ":" + d.forward + "\", \"registry.ollama.ai:127.0.0.1\"]\n"},
		{`      KYPOST_BACKUP_KEEP: "7"` + "\n", `      KYPOST_BACKUP_KEEP: "7"` + "\n      SSL_CERT_FILE: /test-ca/ca.pem\n"},
		{"      - ollama-models:/kypost/ollama-models\n", "      - ollama-models:/kypost/ollama-models\n      - " + ca + ":/test-ca/ca.pem:ro\n"},
	} {
		if !strings.Contains(patched, r[0]) {
			t.Fatalf("KyPost's compose template changed; test plumbing (%s) for %q", postPlumbing[i], r[0])
		}
		patched = strings.Replace(patched, r[0], r[1], 1)
		t.Logf("test plumbing: %s", postPlumbing[i])
	}
	os.WriteFile(tmpl, []byte(patched), 0o600)
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := exec.Command("docker", "logs", postContainer).CombinedOutput()
			t.Logf("kypost logs:\n%s", logs)
			logs, _ = exec.Command("docker", "exec", postContainer, "sh", "-c", "tail -n 20 /kypost/logs/api.err.log /kypost/logs/daemon.err.log").CombinedOutput()
			t.Logf("kypost api/daemon logs:\n%s", logs)
		}
		exec.Command("docker", "compose", "-p", "kyq-kypost", "down", "-v").Run()
	})
	l, err := net.Listen("tcp", net.JoinHostPort(d.forward, "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// addCatalogPost places KyPost on the Docker target beside KyDrive, routes it through the
// cluster's lan edge and selects its setup, after KyIdentity's first setup ran: apply adds its
// client with a setup amendment.
func addCatalogPost(st *stack.Stack, certID int, d driveTarget, port int) {
	st.Apps = append(st.Apps, stack.App{Name: "kypost", Target: "nas"})
	st.Edges[0].Routes = append(st.Edges[0].Routes, stack.Route{App: "kypost", Hostname: catalogPostHost,
		ForwardHost: d.forward, ForwardPort: port, CertificateID: certID, StatusPath: "/api/installer/network"})
	st.Integrations = append(st.Integrations, stack.Integration{Product: "kypost", Target: "nas",
		Bundle: postSetupDir + "/setup.json", IdentityClient: "kypost"})
}

// checkCatalogPost proves the deployed catalog KyPost after its first apply: every step ok, the
// test plumbing present, the container without root or capabilities, published only on the
// forward address, no mail listener, private setup files, its live check, health and network
// status through the edge, and the handover. It returns its client secret and pairing key.
func checkCatalogPost(t *testing.T, out string, d driveTarget, port int, get, code func(host, path string) string) []string {
	t.Helper()
	if !strings.Contains(out, "ran without kypost's client: adding it with setup amendment ") {
		t.Fatal("KyPost amendment not announced")
	}
	for _, id := range []string{"kyidentity.amend.kypost", "kypost.deploy", "kypost.health", "kypost.setup", postRouteStep} {
		if status(out, id) != "ok" {
			t.Fatalf("%s = %q", id, status(out, id))
		}
	}
	if status(out, "kypost.secrets") != "" || status(out, "kyidentity.scim.kypost") != "" {
		t.Fatal("KyPost got a secrets or SCIM step")
	}
	for _, want := range []string{
		"KyPost at https://" + catalogPostURLHost + "/: webmail", "as prepared-admin, for administration only", "can also add mail", "or as prepared-user for mail",
		"KyIdentity role kypost.user", "opens no mail port", "SANDBOX_PRIVATE_HOSTS", "Pair it with KyRecovery",
		"on nas:\n       docker exec --user kypost " + postContainer + " cat /kypost/config/first-run-password.txt",
		"docker exec --user kypost " + postContainer + " rm /kypost/config/first-run-password.txt",
		"To end every live session, on nas:\n       docker restart " + postContainer,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("KyPost handover lacks %q", want)
		}
	}
	c := docker(t, "ps", "-q", "--filter", "label=com.docker.compose.project=kyq-kypost", "--filter", "label=ky.managed-by=kyquickstart")
	if strings.Contains(c, "\n") || c == "" {
		t.Fatalf("KyPost containers: %q", c)
	}
	if name := docker(t, "inspect", c, "--format", "{{.Name}}"); name != "/"+postContainer {
		t.Fatalf("KyPost container is %s, the handover names %s", name, postContainer)
	}
	hc := docker(t, "inspect", c, "--format", "{{.Config.User}} {{.HostConfig.ReadonlyRootfs}} {{.HostConfig.CapDrop}} {{len .HostConfig.CapAdd}} {{.HostConfig.SecurityOpt}} {{json .HostConfig.PortBindings}}")
	want := `kypost true [ALL] 0 [no-new-privileges:true] {"5866/tcp":[{"HostIp":"` + d.forward + `","HostPort":"` + strconv.Itoa(port) + `"}]}`
	if hc != want {
		t.Fatalf("container = %s, want %s", hc, want)
	}
	// Test plumbing, named as such: present in the deployed container, and nothing else added.
	var extra []string
	json.Unmarshal([]byte(docker(t, "inspect", c, "--format", "{{json .HostConfig.ExtraHosts}}")), &extra)
	if !slices.Equal(extra, []string{catalogIdentityHost + ":" + d.forward, "registry.ollama.ai:127.0.0.1"}) {
		t.Fatalf("extra hosts (test plumbing) = %v", extra)
	}
	var env []string
	json.Unmarshal([]byte(docker(t, "inspect", c, "--format", "{{json .Config.Env}}")), &env)
	for _, want := range []string{"KYPOST_NATIVE_MAIL=false", "KYPOST_NATIVE_RECEIVING=false", "KYPOST_NATIVE_RECEIVER=false", "SSL_CERT_FILE=/test-ca/ca.pem", "SERVER_BASE_URL=https://" + catalogPostURLHost} {
		if !slices.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	for _, e := range env {
		for _, forbidden := range []string{"TRUSTED_PROXY_CIDRS=", "PAIRING_SECRET", "BOOTSTRAP_ADMIN_PASS", "POW_SECRET"} {
			if strings.HasPrefix(e, forbidden) {
				t.Errorf("KyPost runs with %s", forbidden)
			}
		}
	}
	// No mail listener: only 5866 and Ollama's loopback 11434 listen.
	tcp := docker(t, "exec", c, "cat", "/proc/net/tcp", "/proc/net/tcp6")
	var listening []int
	for _, line := range strings.Split(tcp, "\n") {
		f := strings.Fields(line)
		if len(f) > 3 && f[3] == "0A" {
			p, _ := strconv.ParseInt(f[1][strings.LastIndex(f[1], ":")+1:], 16, 32)
			listening = append(listening, int(p))
		}
	}
	for _, mail := range []int{25, 143, 465, 587, 993, 2525} {
		if slices.Contains(listening, mail) {
			t.Fatalf("KyPost listens on mail port %d (listening %v)", mail, listening)
		}
	}
	t.Logf("KyPost listens on %v", listening)
	stat := docker(t, "exec", "--user", "kypost", c, "stat", "-c", "%n %a %u", "/kypost/config", "/kypost/private", "/kypost/state", postSetupDir, postSetupDir+"/client.secret", postSetupDir+"/setup.json")
	wantStat := "/kypost/config 700 1000\n/kypost/private 700 1000\n/kypost/state 700 1000\n" + postSetupDir + " 700 1000\n" + postSetupDir + "/client.secret 600 1000\n" + postSetupDir + "/setup.json 600 1000"
	if stat != wantStat {
		t.Fatalf("setup files:\n%s", stat)
	}
	if missing := docker(t, "exec", c, "sh", "-c", "test ! -e "+postSetupDir+"/directory.secret && echo absent"); missing != "absent" {
		t.Fatal("KyPost was given a directory secret")
	}
	report := docker(t, "exec", "--user", "kypost", c, "/usr/local/bin/kypost-server", "apply-setup", "--file", postSetupDir+"/setup.json", "--check")
	var r struct {
		Applied, Pending     []string
		ConfigurationApplied bool
	}
	if json.Unmarshal([]byte(report), &r) != nil || r.Applied == nil || len(r.Applied) != 0 || !r.ConfigurationApplied || len(r.Pending) < 5 {
		t.Fatalf("KyPost check:\n%s", report)
	}
	t.Logf("KyPost check: pending %v", r.Pending)
	var health struct{ Healthy bool }
	if body := get(catalogPostHost, "/api/health"); code(catalogPostHost, "/api/health") != "200" || json.Unmarshal([]byte(body), &health) != nil || !health.Healthy {
		t.Fatalf("health through the edge:\n%s", body)
	}
	var network struct {
		ClientIP            string `json:"clientIp"`
		ProxyHeadersTrusted bool   `json:"proxyHeadersTrusted"`
		Scheme              string `json:"scheme"`
	}
	if body := get(catalogPostHost, "/api/installer/network"); code(catalogPostHost, "/api/installer/network") != "200" || json.Unmarshal([]byte(body), &network) != nil || network.ProxyHeadersTrusted || network.Scheme != "https" {
		t.Fatalf("network status through the edge:\n%s", body)
	}
	t.Logf("KyPost sees clients from %s through the edge", network.ClientIP)
	var secrets []string
	for _, f := range []string{postSetupDir + "/client.secret", "/kypost/private/pairing.key"} {
		s := docker(t, "exec", "--user", "kypost", c, "cat", f)
		if len(s) < 32 {
			t.Fatalf("%s is %d bytes", f, len(s))
		}
		secrets = append(secrets, s)
	}
	return secrets
}

// postSignOn signs the owner's accounts in to KyPost through KyIdentity behind the edge, as a
// browser would: KyPost sends each to KyIdentity as client kypost with PKCE, nonce and state,
// and creates the admin as administrator and the everyday account as a user at first sign-in.
// It returns the admin's session. Test-only plumbing: KyPost trusts the edge's certificate and
// resolves the identity host to the relay.
func postSignOn(t *testing.T, ctx context.Context, c *kube.Client, certPEM []byte, nodeIP string) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("invalid edge certificate")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != catalogIdentityURLHost && address != catalogPostURLHost {
			return nil, errors.New("unselected destination " + address)
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(nodeIP, "30443"))
	}
	t.Cleanup(transport.CloseIdleConnections)
	identity, post := "https://"+catalogIdentityURLHost, "https://"+catalogPostURLHost
	setupDir := "/data/installer/" + identityFixturePreparation().SetupID
	var admin *http.Client
	for _, account := range []struct {
		username, passwordFile string
		admin                  bool
	}{{"prepared-admin", "admin.password", true}, {"prepared-user", "everyday.password", false}} {
		ns, pod, container, err := c.AppPod(ctx, "kyidentity")
		if err != nil {
			t.Fatal(err)
		}
		password, err := c.Exec(ctx, ns, pod, container, []string{"cat", setupDir + "/" + account.passwordFile})
		if err != nil || len(password) == 0 {
			t.Fatal("installer-created account credential missing", err)
		}
		jar, _ := cookiejar.New(nil)
		client := &http.Client{Transport: transport, Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		send := func(method, endpoint string, body []byte, csrf string) (*http.Response, []byte) {
			t.Helper()
			req, _ := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			if csrf != "" {
				req.Header.Set("X-CSRF-Token", csrf)
			}
			r, err := client.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", method, endpoint, err)
			}
			defer r.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
			return r, b
		}
		_, b := send("GET", identity+"/api/auth/csrf", nil, "")
		var csrf struct {
			CSRFToken string `json:"csrfToken"`
		}
		if json.Unmarshal(b, &csrf) != nil || csrf.CSRFToken == "" {
			t.Fatalf("KyIdentity CSRF: %s", b)
		}
		payload, _ := json.Marshal(map[string]string{"username": account.username, "password": strings.TrimSpace(string(password))})
		clear(password)
		r, b := send("POST", identity+"/api/auth/login", payload, csrf.CSRFToken)
		clear(payload)
		var login struct{ Success, MFARequired bool }
		if r.StatusCode != 200 || json.Unmarshal(b, &login) != nil || !login.Success || login.MFARequired {
			t.Fatalf("%s password sign-in: %d", account.username, r.StatusCode)
		}

		r, b = send("GET", post+"/api/auth/oidc/login", nil, "")
		authorize, err := r.Location()
		if err != nil || r.StatusCode != 302 || authorize.Host != catalogIdentityURLHost {
			t.Fatalf("KyPost sign-in start: %d %s", r.StatusCode, b)
		}
		q := authorize.Query()
		if q.Get("client_id") != "kypost" || q.Get("redirect_uri") != post+"/api/auth/oidc/callback" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
			t.Fatalf("authorization request: %s", authorize)
		}
		r, _ = send("GET", authorize.String(), nil, "")
		callback, _ := r.Location()
		if r.StatusCode != 302 || callback == nil || "https://"+callback.Host != post || callback.Path != "/api/auth/oidc/callback" || callback.Query().Get("code") == "" || callback.Query().Get("state") != q.Get("state") {
			t.Fatalf("KyIdentity authorization of %s for KyPost: %d %v", account.username, r.StatusCode, callback)
		}
		r, b = send("GET", callback.String(), nil, "")
		if r.StatusCode != 302 || r.Header.Get("Location") != "/read" {
			t.Fatalf("KyPost callback for %s: %d %s", account.username, r.StatusCode, b)
		}
		if !slices.ContainsFunc(r.Cookies(), func(c *http.Cookie) bool {
			return c.Name == "kypost_session" && c.Secure && c.HttpOnly && c.Value != ""
		}) {
			t.Fatalf("KyPost session cookie for %s: %v", account.username, r.Header.Values("Set-Cookie"))
		}
		_, b = send("GET", post+"/api/auth/me", nil, "")
		var me struct {
			Authenticated  bool
			Username, Role string
		}
		role := map[bool]string{true: "admin", false: "user"}[account.admin]
		if json.Unmarshal(b, &me) != nil || !me.Authenticated || me.Username != account.username || me.Role != role {
			t.Fatalf("KyPost session for %s: %s", account.username, b)
		}
		users, _ := send("GET", post+"/api/users", nil, "")
		if want := map[bool]int{true: 200, false: 403}[account.admin]; users.StatusCode != want {
			t.Fatalf("%s: /api/users %d, want %d", account.username, users.StatusCode, want)
		}
		t.Logf("%s signed in to KyPost through KyIdentity: username %s, role %s, /api/users %d", account.username, me.Username, me.Role, users.StatusCode)
		if account.admin {
			admin = client
		}
	}
	return admin
}

// handoverLine is the one line of apply's output that runs command, as printed.
func handoverLine(t *testing.T, out, command string) string {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, command) {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if len(lines) != 1 {
		t.Fatalf("handover lines with %q: %q", command, lines)
	}
	return lines[0]
}

// checkPostHandover runs the handover's break-glass read and session-ending restart verbatim
// in the Docker target's shell. The password file is KyPost's own, 0600; the restart ends the
// admin's session, and KyPost answers through the edge again. It returns the password.
func checkPostHandover(t *testing.T, ctx context.Context, out string, d driveTarget, admin *http.Client, code func(host, path string) string) string {
	t.Helper()
	read := handoverLine(t, out, "cat /kypost/config/first-run-password.txt")
	b, err := exec.CommandContext(ctx, "docker", "exec", d.name, "sh", "-c", read).Output()
	got := string(b)
	if err != nil || !strings.HasPrefix(got, "username: admin\npassword: ") || len(strings.TrimSpace(strings.TrimPrefix(got, "username: admin\npassword: "))) < 12 {
		t.Fatalf("handover read %q: %v", read, err)
	}
	if stat := docker(t, "exec", postContainer, "stat", "-c", "%a %u", "/kypost/config/first-run-password.txt"); stat != "600 1000" {
		t.Fatalf("first-run password file: %s", stat)
	}
	status := func() int {
		req, _ := http.NewRequestWithContext(ctx, "GET", "https://"+catalogPostURLHost+"/api/status", nil)
		r, err := admin.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if s := status(); s != 200 {
		t.Fatalf("admin session before restart: /api/status %d", s)
	}
	restart := handoverLine(t, out, "docker restart ")
	if b, err := exec.CommandContext(ctx, "docker", "exec", d.name, "sh", "-c", restart).CombinedOutput(); err != nil {
		t.Fatalf("handover restart %q: %v\n%s", restart, err, b)
	}
	start := time.Now()
	for code(catalogPostHost, "/api/health") != "200" {
		if time.Since(start) > 5*time.Minute {
			t.Fatal("KyPost not healthy through the edge after the handover's restart")
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("KyPost answered through the edge %s after the handover's restart", time.Since(start).Round(time.Second))
	if s := status(); s != 401 {
		t.Fatalf("admin session after restart: /api/status %d, want 401", s)
	}
	return strings.TrimSpace(strings.TrimPrefix(got, "username: admin\npassword: "))
}
