//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
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
	"golang.org/x/crypto/ssh"
)

// catalogDriveHost is where TestKubernetesNPMEdge routes the catalog KyDrive, which runs on
// the Docker target behind the cluster's lan edge.
const catalogDriveHost = "drive.example.test"

const catalogDriveURLHost = catalogDriveHost + ":30443"

// relayImage carries nginx's stream module.
const relayImage = "nginx:alpine@sha256:3a7edb153fcd67884f51775c8bed68a418fac7328a31d2be4c4ed5cae816ce8d"

const driveSetupDir = "/data/installer/7d2e4f60-1a3b-4c5d-8e9f-0a1b2c3d4e5f"

// driveTarget is the disposable Docker target KyDrive runs on: an sshd container on the host's
// Docker socket, whose root the host shares, and the address the edge forwards KyDrive to.
type driveTarget struct {
	name           string // the sshd container
	port, root, fp string
	forward        string // the kind network's IPv4 gateway: this host, as the kind node reaches it
	forwardPort    int
	relay          string // the container relaying KyDrive's requests to KyIdentity's edge
}

// startDriveTarget starts the sshd target and copies the catalog KyDrive into catalogDir with
// test-only plumbing: identity.example.test is a relay to the lan edge's NodePort, and KyDrive
// trusts the edge's throwaway certificate. Docker forwards only published ports between
// bridges and none back into the bridge they came from, so KyDrive stays off kind's network
// and the relay, on it, publishes 30443 on kind's gateway. Pods leave kind through the node's
// address, as they leave K80.
func startDriveTarget(t *testing.T, work, catalogDir, nodeIP string, certPEM []byte) driveTarget {
	t.Helper()
	keys := filepath.Join(work, "drive-keys")
	os.Mkdir(keys, 0o700)
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(hostPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(keys, "host_ed25519"), pem.EncodeToMemory(block), 0o600)
	hostSigner, _ := ssh.NewSignerFromKey(hostPriv)
	_, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	clientSigner, _ := ssh.NewSignerFromKey(clientPriv)
	os.WriteFile(filepath.Join(keys, "authorized_keys"), ssh.MarshalAuthorizedKey(clientSigner.PublicKey()), 0o600)
	serveAgent(t, work, clientPriv)

	id := make([]byte, 4)
	rand.Read(id)
	name := "kyq-e2e-drive-" + hex.EncodeToString(id)
	docker(t, "build", "-q", "-t", "kyq-e2e-sshd", "sshd")
	// As in TestApply: a root-owned root the host shares, so Compose bind mounts resolve.
	base := "/tmp/" + name
	docker(t, "run", "--rm", "--memory", "64m", "--memory-swap", "64m", "--cpus", "1", "--pids-limit", "32", "-v", "/tmp:/tmp", "--entrypoint", "mkdir", "kyq-e2e-sshd", "-m", "755", base)
	t.Cleanup(func() {
		exec.Command("docker", "run", "--rm", "--memory", "64m", "--memory-swap", "64m", "--cpus", "1", "--pids-limit", "32", "-v", "/tmp:/tmp", "--entrypoint", "rm", "kyq-e2e-sshd", "-rf", base).Run()
	})
	docker(t, "run", "-d", "--name", name, "--memory", "256m", "--memory-swap", "256m", "--cpus", "1", "--pids-limit", "128", "-p", "127.0.0.1::22",
		"-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", keys+":/keys:ro", "-v", base+":"+base, "-v", work+":"+work, "kyq-e2e-sshd")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := exec.Command("docker", "logs", "kyq-kydrive-app-1").CombinedOutput()
			t.Logf("kydrive logs:\n%s", logs)
		}
		exec.Command("docker", "compose", "-p", "kyq-kydrive", "down").Run()
	})
	hostPort := docker(t, "port", name, "22")
	_, port, _ := net.SplitHostPort(hostPort)
	fp := ssh.FingerprintSHA256(hostSigner.PublicKey())
	waitForSSH(t, port, fp)

	gateway := func(network string) string {
		for _, gw := range strings.Fields(docker(t, "network", "inspect", network, "--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}")) {
			if strings.Contains(gw, ".") {
				return gw
			}
		}
		t.Fatalf("network %s has no IPv4 gateway", network)
		return ""
	}
	kindGateway := gateway("kind")
	l, err := net.Listen("tcp", net.JoinHostPort(kindGateway, "0"))
	if err != nil {
		t.Fatal(err)
	}
	forwardPort := l.Addr().(*net.TCPAddr).Port
	l.Close()
	conf := filepath.Join(work, "drive-relay.conf")
	os.WriteFile(conf, []byte("events {}\nstream {\n  server {\n    listen 30443;\n    proxy_pass "+nodeIP+":30443;\n  }\n}\n"), 0o644)
	relay := name + "-relay"
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", relay).Run() })
	docker(t, "run", "-d", "--name", relay, "--network", "kind", "--memory", "64m", "-p", kindGateway+":30443:30443", "-v", conf+":/etc/nginx/nginx.conf:ro", relayImage)

	ca := filepath.Join(work, "drive-ca.pem")
	os.WriteFile(ca, certPEM, 0o644)
	dir := filepath.Join(catalogDir, "kydrive")
	if err := os.CopyFS(dir, os.DirFS("../../internal/catalog/apps/kydrive")); err != nil {
		t.Fatal(err)
	}
	tmpl := filepath.Join(dir, "compose.yaml.tmpl")
	b, _ := os.ReadFile(tmpl)
	patched := string(b)
	for _, r := range [][2]string{
		{`    ports: ["{{forward}}:8080"]` + "\n", `    ports: ["{{forward}}:8080"]` + "\n    extra_hosts: [\"" + catalogIdentityHost + ":" + kindGateway + "\"]\n"},
		{"      GOMEMLIMIT: 768MiB\n", "      GOMEMLIMIT: 768MiB\n      SSL_CERT_FILE: /test-ca/ca.pem\n"},
		{`volumes: ["./data:/data", "./backups:/backups"]`, `volumes: ["./data:/data", "./backups:/backups", "` + ca + `:/test-ca/ca.pem:ro"]`},
	} {
		if !strings.Contains(patched, r[0]) {
			t.Fatalf("KyDrive's compose template changed; test plumbing for %q", r[0])
		}
		patched = strings.Replace(patched, r[0], r[1], 1)
	}
	os.WriteFile(tmpl, []byte(patched), 0o600)
	return driveTarget{name: name, port: port, root: base + "/root", fp: fp, forward: kindGateway, forwardPort: forwardPort, relay: relay}
}

// addCatalogDrive places KyDrive on the Docker target, routes it through the cluster's lan
// edge to the target's forward address and selects its setup; apply adds its client to the
// catalog KyIdentity's first setup.
func addCatalogDrive(st *stack.Stack, certID int, d driveTarget) {
	port, _ := strconv.Atoi(d.port)
	st.Targets = append(st.Targets, stack.Target{Name: "nas", Host: "127.0.0.1", Port: port, User: "root", Root: d.root})
	st.Apps = append(st.Apps, stack.App{Name: "kydrive", Target: "nas"})
	st.Edges[0].Routes = append(st.Edges[0].Routes, stack.Route{App: "kydrive", Hostname: catalogDriveHost,
		ForwardHost: d.forward, ForwardPort: d.forwardPort, CertificateID: certID, StatusPath: "/healthz"})
	st.Integrations = append(st.Integrations, stack.Integration{Product: "kydrive", Target: "nas",
		Bundle: driveSetupDir + "/setup.json", IdentityClient: "kydrive"})
}

// checkCatalogDrive proves the deployed catalog KyDrive: hardened container as the target
// user, published only on the forward address, setup applied from private files the export
// crossed to over SSH, its live check, and health through the cluster's edge. It returns the
// secrets apply placed or generated.
func checkCatalogDrive(t *testing.T, out string, d driveTarget, get func(host, path string) string) []string {
	t.Helper()
	for _, id := range []string{"kydrive.secrets", "kydrive.deploy", "kydrive.health", "kydrive.setup"} {
		if status(out, id) != "ok" {
			t.Fatalf("%s = %q", id, status(out, id))
		}
	}
	if !strings.Contains(out, "KyDrive at https://"+catalogDriveURLHost+"/: sign in through KyIdentity as prepared-admin") || !strings.Contains(out, d.root+"/kydrive/data/initial-admin-password on nas") {
		t.Fatal("KyDrive handover missing")
	}
	c := docker(t, "ps", "-q", "--filter", "label=com.docker.compose.project=kyq-kydrive", "--filter", "label=ky.managed-by=kyquickstart")
	if strings.Contains(c, "\n") || c == "" {
		t.Fatalf("KyDrive containers: %q", c)
	}
	hc := docker(t, "inspect", c, "--format", "{{.Config.User}} {{.HostConfig.ReadonlyRootfs}} {{.HostConfig.CapDrop}} {{.HostConfig.SecurityOpt}} {{json .HostConfig.PortBindings}}")
	want := `0:0 true [ALL] [no-new-privileges:true] {"8080/tcp":[{"HostIp":"` + d.forward + `","HostPort":"` + strconv.Itoa(d.forwardPort) + `"}]}`
	if hc != want {
		t.Fatalf("container = %s, want %s", hc, want)
	}
	stat := docker(t, "exec", c, "stat", "-c", "%n %a %u", "/data", driveSetupDir, driveSetupDir+"/client.secret", driveSetupDir+"/setup.json", "/run/secrets/scim_token", "/run/secrets/session_secret")
	wantStat := "/data 700 0\n" + driveSetupDir + " 700 0\n" + driveSetupDir + "/client.secret 600 0\n" + driveSetupDir + "/setup.json 600 0\n/run/secrets/scim_token 600 0\n/run/secrets/session_secret 600 0"
	if stat != wantStat {
		t.Fatalf("setup files:\n%s", stat)
	}
	if missing := docker(t, "exec", c, "sh", "-c", "test ! -e "+driveSetupDir+"/directory.secret && echo absent"); missing != "absent" {
		t.Fatal("KyDrive was given a directory secret")
	}
	report := docker(t, "exec", c, "/app/kydrive-server", "apply-setup", "--file", driveSetupDir+"/setup.json", "--check")
	var r struct {
		Created, Present, Pending []json.RawMessage
	}
	if json.Unmarshal([]byte(report), &r) != nil || len(r.Created) != 0 || len(r.Pending) != 0 || len(r.Present) == 0 {
		t.Fatalf("KyDrive check:\n%s", report)
	}
	var health struct {
		Service, Status string
	}
	if body := get(catalogDriveHost, "/healthz"); json.Unmarshal([]byte(body), &health) != nil || health.Service != "kydrive" || health.Status != "ok" {
		t.Fatalf("health through the edge:\n%s", body)
	}
	var secrets []string
	for _, f := range []string{driveSetupDir + "/client.secret", "/run/secrets/scim_token", "/run/secrets/session_secret"} {
		s := docker(t, "exec", c, "cat", f)
		if len(s) < 32 {
			t.Fatalf("%s is %d bytes", f, len(s))
		}
		secrets = append(secrets, s)
	}
	return secrets
}

// checkDriveConnector proves kyidentity.scim.kydrive: KyDrive's token, read on its target, in
// a private file in KyIdentity's pod, and the setup pass naming it checks clean with the
// connector KyIdentity created.
func checkDriveConnector(t *testing.T, ctx context.Context, c *kube.Client, out, token string) {
	t.Helper()
	if status(out, "kyidentity.scim.kydrive") != "ok" {
		t.Fatalf("kyidentity.scim.kydrive = %q", status(out, "kyidentity.scim.kydrive"))
	}
	ns, pod, container, err := c.AppPod(ctx, "kyidentity")
	if err != nil {
		t.Fatal(err)
	}
	const tokenFile, pass = "/data/installer/scim-kydrive.token", "/data/installer/scim-kydrive.json"
	stat, err := c.Exec(ctx, ns, pod, container, []string{"stat", "-c", "%n %a %u", tokenFile, pass})
	if err != nil || strings.TrimSpace(string(stat)) != tokenFile+" 600 100\n"+pass+" 600 100" {
		t.Fatalf("connector files:\n%s %v", stat, err)
	}
	if b, err := c.Exec(ctx, ns, pod, container, []string{"cat", tokenFile}); err != nil || string(b) != token {
		t.Fatal("KyIdentity's copy of KyDrive's SCIM token differs", err)
	}
	report, err := c.Exec(ctx, ns, pod, container, []string{"/usr/local/bin/kyidentity", "apply-setup", "--file", pass, "--check"})
	var r struct {
		Created []json.RawMessage
		Reused  []struct{ Kind, Name string }
	}
	if err != nil || json.Unmarshal(report, &r) != nil || len(r.Created) != 0 || !slices.ContainsFunc(r.Reused, func(o struct{ Kind, Name string }) bool { return o.Kind == "connector" && o.Name == "kydrive" }) {
		t.Fatalf("connector check:\n%s %v", report, err)
	}
}

// relayIP is the address KyDrive's requests to KyIdentity reach the lan edge from.
func (d driveTarget) relayIP(t *testing.T) string {
	t.Helper()
	return docker(t, "inspect", d.relay, "--format", `{{(index .NetworkSettings.Networks "kind").IPAddress}}`)
}

// driveSignOn waits for KyIdentity's SCIM connector to provision the owner's accounts in
// KyDrive, then signs both in to KyDrive through KyIdentity, behind the cluster's edge, as a
// browser would: KyDrive must send each to KyIdentity as client kydrive and accept the admin
// back as its administrator and the everyday account as an ordinary user.
func driveSignOn(t *testing.T, ctx context.Context, kubeconfig string, certPEM []byte, nodeIP, scimToken string) {
	t.Helper()
	c, err := kube.Connect(kubeconfig, "")
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("invalid edge certificate")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != catalogIdentityURLHost && address != catalogDriveURLHost {
			return nil, errors.New("unselected destination " + address)
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(nodeIP, "30443"))
	}
	t.Cleanup(transport.CloseIdleConnections)
	identity, drive := "https://"+catalogIdentityURLHost, "https://"+catalogDriveURLHost
	setupDir := "/data/installer/" + identityFixturePreparation().SetupID
	waitForSCIMUsers(t, ctx, &http.Client{Transport: transport, Timeout: 30 * time.Second}, drive, scimToken)
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
		r, _ := send("POST", identity+"/api/auth/login", payload, csrf.CSRFToken)
		clear(payload)
		if r.StatusCode != 200 {
			t.Fatalf("%s password sign-in: %d", account.username, r.StatusCode)
		}

		r, b = send("GET", drive+"/api/sso/kysignon/login", nil, "")
		authorize, err := r.Location()
		if err != nil || r.StatusCode != 302 || authorize.Host != catalogIdentityURLHost {
			t.Fatalf("KyDrive sign-in start: %d %s", r.StatusCode, b)
		}
		q := authorize.Query()
		if q.Get("client_id") != "kydrive" || q.Get("redirect_uri") != drive+"/api/sso/kysignon/callback" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" {
			t.Fatalf("authorization request: %s", authorize)
		}
		r, _ = send("GET", authorize.String(), nil, "")
		callback, _ := r.Location()
		if r.StatusCode != 302 || callback == nil || "https://"+callback.Host != drive || callback.Query().Get("code") == "" || callback.Query().Get("state") != q.Get("state") {
			t.Fatalf("KyIdentity authorization of %s for kydrive: %d %v", account.username, r.StatusCode, callback)
		}
		r, b = send("GET", callback.String(), nil, "")
		if r.StatusCode != 302 || r.Header.Get("Location") != "/" {
			t.Fatalf("KyDrive callback: %d %s", r.StatusCode, b)
		}
		_, b = send("GET", drive+"/api/auth/me", nil, "")
		var me struct {
			Authenticated bool
			User          map[string]any
		}
		if json.Unmarshal(b, &me) != nil || !me.Authenticated {
			t.Fatalf("KyDrive session: %s", b)
		}
		role := map[bool]string{true: "admin", false: "user"}[account.admin]
		t.Logf("%s signed in to KyDrive through KyIdentity: username %v, role %v", account.username, me.User["username"], me.User["role"])
		if me.User["username"] != account.username || me.User["role"] != role {
			t.Fatalf("KyDrive user: %s", b)
		}
	}
}

// waitForSCIMUsers waits until KyDrive's SCIM endpoint lists both of the owner's accounts,
// active and bound to a KyIdentity subject. KyIdentity retries a failed delivery with backoff
// (30 s doubling), and its first ones ran before it trusted the edge's certificate.
func waitForSCIMUsers(t *testing.T, ctx context.Context, client *http.Client, drive, token string) {
	t.Helper()
	start := time.Now()
	for _, username := range []string{"prepared-admin", "prepared-user"} {
		for {
			req, _ := http.NewRequestWithContext(ctx, "GET", drive+"/scim/v2/Users?filter="+url.QueryEscape(`userName eq "`+username+`"`), nil)
			req.Header.Set("Authorization", "Bearer "+token)
			var list struct {
				Resources []struct {
					ExternalID string `json:"externalId"`
					Active     bool   `json:"active"`
				}
			}
			r, err := client.Do(req)
			if err == nil {
				b, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
				r.Body.Close()
				if r.StatusCode == 200 && json.Unmarshal(b, &list) == nil && len(list.Resources) == 1 && list.Resources[0].Active && list.Resources[0].ExternalID != "" {
					break
				}
			}
			if time.Since(start) > 9*time.Minute {
				t.Fatalf("KyIdentity's SCIM connector did not provision %s in KyDrive: %v", username, err)
			}
			time.Sleep(5 * time.Second)
		}
	}
	t.Logf("KyDrive lists both accounts by SCIM %s after the wait began", time.Since(start).Round(time.Second))
}
