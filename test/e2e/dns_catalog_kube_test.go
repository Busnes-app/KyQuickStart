//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// catalogDNSHost is where TestKubernetesNPMEdge routes the catalog KyDNS, which runs on the
// Docker target behind the cluster's lan edge.
const catalogDNSHost = "dns.example.test"

const catalogDNSURLHost = catalogDNSHost + ":30443"

const dnsSetupDir = "/data/installer/9c4e2a7b-3d1f-4b6a-8e5c-7f0a1b2c3d4e"

// dnsRouteStep is the edge step that routes KyDNS (the NPM host step's ID).
var dnsRouteStep = func() string {
	sum := sha256.Sum256([]byte(catalogDNSHost))
	return "k8s.edge." + hex.EncodeToString(sum[:8])
}()

// gateMessage is KyDNS's transport gate's refusal of a peer it does not trust.
const gateMessage = "KyDNS serves its admin UI only on this host's loopback or through an HTTPS reverse proxy listed in KYDNS_TRUSTED_PROXIES."

// copyCatalogDNS copies the catalog KyDNS into catalogDir with test-only plumbing, as KyDrive's:
// identity.example.test is the relay to the lan edge, and KyDNS trusts the edge's throwaway
// certificate (SSL_CERT_FILE) for its sign-in's discovery and token exchange.
func copyCatalogDNS(t *testing.T, catalogDir, ca string, d driveTarget) {
	t.Helper()
	dir := filepath.Join(catalogDir, "kydns")
	if err := os.CopyFS(dir, os.DirFS("../../internal/catalog/apps/kydns")); err != nil {
		t.Fatal(err)
	}
	tmpl := filepath.Join(dir, "compose.yaml.tmpl")
	b, _ := os.ReadFile(tmpl)
	patched := string(b)
	for _, r := range [][2]string{
		{"    environment:\n", "    extra_hosts: [\"" + catalogIdentityHost + ":" + d.forward + "\"]\n    environment:\n"},
		{"      GOMEMLIMIT: 192MiB\n", "      GOMEMLIMIT: 192MiB\n      SSL_CERT_FILE: /test-ca/ca.pem\n"},
		{`volumes: ["./data:/data"]`, `volumes: ["./data:/data", "` + ca + `:/test-ca/ca.pem:ro"]`},
	} {
		if !strings.Contains(patched, r[0]) {
			t.Fatalf("KyDNS's compose template changed; test plumbing for %q", r[0])
		}
		patched = strings.Replace(patched, r[0], r[1], 1)
	}
	os.WriteFile(tmpl, []byte(patched), 0o600)
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := exec.Command("docker", "logs", "kyq-kydns-app-1").CombinedOutput()
			t.Logf("kydns logs:\n%s", logs)
		}
		exec.Command("docker", "compose", "-p", "kyq-kydns", "down").Run()
	})
}

// freePort is a TCP port free on ip now.
func freePort(t *testing.T, ip string) int {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// addCatalogDNS places KyDNS on the Docker target, routed through the cluster's lan edge to
// the target's forward address on port, and selects its setup.
func addCatalogDNS(st *stack.Stack, certID int, d driveTarget, port int) {
	st.Apps = append(st.Apps, stack.App{Name: "kydns", Target: "nas"})
	st.Edges[0].Routes = append(st.Edges[0].Routes, stack.Route{App: "kydns", Hostname: catalogDNSHost,
		ForwardHost: d.forward, ForwardPort: port, CertificateID: certID, StatusPath: "/healthz"})
	st.Integrations = append(st.Integrations, stack.Integration{Product: "kydns", Target: "nas",
		Bundle: dnsSetupDir + "/setup.json", IdentityClient: "kydns"})
}

// edgeClient sends requests for KyIdentity and KyDNS to the lan edge, trusting its certificate.
func edgeClient(t *testing.T, certPEM []byte, nodeIP string) (*http.Transport, func() *http.Client) {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("invalid edge certificate")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != catalogIdentityURLHost && address != catalogDNSURLHost {
			return nil, errors.New("unselected destination " + address)
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(nodeIP, "30443"))
	}
	t.Cleanup(transport.CloseIdleConnections)
	return transport, func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Transport: transport, Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
}

func get(t *testing.T, ctx context.Context, c *http.Client, url string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	r, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	return r, b
}

// dnsNetwork is KyDNS's report of how it sees a request through the edge.
type dnsNetwork struct {
	ClientIP            string `json:"clientIp"`
	ProxyHeadersTrusted bool   `json:"proxyHeadersTrusted"`
	Scheme              string `json:"scheme"`
}

// dnsEdgePeer is KyDNS's report of a request through the edge. A refused one (403) reports
// nothing; the trusted one has the scheme.
func dnsEdgePeer(t *testing.T, ctx context.Context, c *http.Client) (int, dnsNetwork) {
	t.Helper()
	r, b := get(t, ctx, c, "https://"+catalogDNSURLHost+"/api/v1/installer/network")
	var n dnsNetwork
	if r.StatusCode == 200 && json.Unmarshal(b, &n) != nil {
		t.Fatalf("network report: %s", b)
	}
	return r.StatusCode, n
}

// checkCatalogDNS proves KyDNS's first apply: the amendment and every step ok, the handover,
// the hardened container publishing only on the forward address, its setup files on the host
// and its live check. It returns the secrets read from the host.
func checkCatalogDNS(t *testing.T, out string, d driveTarget, port int, passed bool) []string {
	t.Helper()
	if !strings.Contains(out, "ran without kydns's client: adding it with setup amendment ") {
		t.Fatal("KyDNS amendment not announced")
	}
	for _, id := range []string{"kyidentity.amend.kydns", "kydns.deploy", "kydns.health", "kydns.setup", "kydns.gate", dnsRouteStep} {
		if status(out, id) != "ok" {
			t.Fatalf("%s = %q", id, status(out, id))
		}
	}
	// Apply prints the handover only once every step passed.
	for _, want := range []string{
		"KyDNS at https://" + catalogDNSURLHost + "/: sign in through KyIdentity as prepared-admin, the one account it admits",
		"It answers DNS at " + d.forward + ":53 (UDP and TCP)",
		"Firewall: ports 53 and " + strconv.Itoa(port) + " are open on " + d.forward,
		"trusts forwarded\n    requests only from egress_address ",
		"restrict both ports to your LAN and the edge at\n    the host or network firewall",
		"Point clients at " + d.forward + " for DNS",
		"From an interactive shell on nas\n     (ssh -t, so the password does not echo), set one:\n       docker exec -it kyq-kydns-app-1 /usr/local/bin/kydns admin reset-password",
		"No catalog product can pair with KyRecovery in this release",
	} {
		if passed && !strings.Contains(out, want) {
			t.Fatalf("KyDNS handover lacks %q", want)
		}
	}
	if !passed && strings.Contains(out, "KyDNS at https://") {
		t.Fatal("handover printed by a failed apply")
	}
	c := docker(t, "ps", "-q", "--filter", "label=com.docker.compose.project=kyq-kydns", "--filter", "label=ky.managed-by=kyquickstart")
	if strings.Contains(c, "\n") || c == "" {
		t.Fatalf("KyDNS containers: %q", c)
	}
	hc := docker(t, "inspect", c, "--format", "{{.Config.User}} {{.HostConfig.ReadonlyRootfs}} {{.HostConfig.CapDrop}} {{.HostConfig.SecurityOpt}} {{json .HostConfig.Sysctls}} {{json .HostConfig.PortBindings}}")
	p := strconv.Itoa(port)
	want := `0:0 true [ALL] [no-new-privileges:true] {"net.ipv4.ip_unprivileged_port_start":"0"} {"53/tcp":[{"HostIp":"` + d.forward + `","HostPort":"53"}],"53/udp":[{"HostIp":"` + d.forward + `","HostPort":"53"}],"8053/tcp":[{"HostIp":"` + d.forward + `","HostPort":"` + p + `"}]}`
	if hc != want {
		t.Fatalf("container = %s, want %s", hc, want)
	}
	// The setup files on the host, as the target user (root in this fixture) placed them.
	host := d.root + "/kydns" + dnsSetupDir
	stat := docker(t, "exec", d.name, "stat", "-c", "%n %a %u %F", host, host+"/client.secret", host+"/directory.secret", host+"/setup.json")
	wantStat := host + " 700 0 directory\n" + host + "/client.secret 600 0 regular file\n" + host + "/directory.secret 600 0 regular file\n" + host + "/setup.json 600 0 regular file"
	if stat != wantStat {
		t.Fatalf("setup files:\n%s", stat)
	}
	report := docker(t, "exec", c, "/usr/local/bin/kydns", "apply-setup", "--file", dnsSetupDir+"/setup.json", "--check")
	var r struct {
		Created, Pending []json.RawMessage
		Present          []struct{ Kind, Name string }
	}
	if json.Unmarshal([]byte(report), &r) != nil || r.Created == nil || len(r.Created) != 0 || len(r.Pending) != 0 {
		t.Fatalf("KyDNS check:\n%s", report)
	}
	identities := 0
	for _, o := range r.Present {
		if o.Kind == "identity" {
			identities++
		}
	}
	if identities != 1 {
		t.Fatalf("KyDNS check names %d identities:\n%s", identities, report)
	}
	t.Logf("KyDNS check: %s", report)
	var secrets []string
	for _, f := range []string{host + "/client.secret", host + "/directory.secret"} {
		s := docker(t, "exec", d.name, "cat", f)
		if len(s) < 16 {
			t.Fatalf("%s is %d bytes", f, len(s))
		}
		secrets = append(secrets, s)
	}
	if token := docker(t, "exec", d.name, "sh", "-c", "cat "+d.root+"/kydns/data/setup-token 2>/dev/null || true"); token != "" {
		secrets = append(secrets, token)
	} else {
		t.Log("KyDNS left no setup-token file")
	}
	return secrets
}

// dnsQuery asks KyDNS at addr, over network, for an A record of a name its private zone does
// not hold. It returns the response's AA bit and RCODE.
func dnsQuery(t *testing.T, network, addr string) (bool, int) {
	t.Helper()
	const name = "kyq-e2e-absent.home.arpa"
	q := []byte{0x4b, 0x51, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0} // ID, RD, one question
	for _, label := range strings.Split(name, ".") {
		q = append(append(q, byte(len(label))), label...)
	}
	q = append(q, 0, 0, 1, 0, 1) // root, A, IN
	conn, err := net.DialTimeout(network, addr, 5*time.Second)
	if err != nil {
		t.Fatalf("%s %s: %v", network, addr, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	resp := make([]byte, 4096)
	var n int
	if network == "tcp" {
		conn.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(q))), q...))
		var size [2]byte
		if _, err = io.ReadFull(conn, size[:]); err == nil {
			n, err = io.ReadFull(conn, resp[:binary.BigEndian.Uint16(size[:])])
		}
	} else {
		conn.Write(q)
		n, err = conn.Read(resp)
	}
	if err != nil || n < 12 || resp[0] != q[0] || resp[1] != q[1] || resp[2]&0x80 == 0 {
		t.Fatalf("%s answer from %s: %v %x", network, addr, err, resp[:n])
	}
	return resp[2]&0x04 != 0, int(resp[3] & 0x0f)
}

// checkDNSGate proves AC10 and the gate half of AC11 from this host, which is not the edge:
// DNS over UDP and TCP on the forward address, and KyDNS's transport gate refusing a direct
// request, plain or carrying the edge's X-Forwarded-Proto.
func checkDNSGate(t *testing.T, ctx context.Context, d driveTarget, port int) {
	t.Helper()
	for _, network := range []string{"udp", "tcp"} {
		if aa, rcode := dnsQuery(t, network, net.JoinHostPort(d.forward, "53")); !aa || rcode != 3 {
			t.Fatalf("%s: AA %v, RCODE %d; want an authoritative NXDOMAIN", network, aa, rcode)
		}
	}
	direct := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := "http://" + net.JoinHostPort(d.forward, strconv.Itoa(port))
	for _, p := range []string{"/login", "/api/v1/installer/network", "/setup"} {
		for _, forged := range []bool{false, true} {
			req, _ := http.NewRequestWithContext(ctx, "GET", base+p, nil)
			if forged {
				req.Header.Set("X-Forwarded-Proto", "https")
				req.Header.Set("X-Forwarded-For", "192.168.1.20")
			}
			r, err := direct.Do(req)
			if err != nil {
				t.Fatalf("direct %s: %v", p, err)
			}
			b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
			r.Body.Close()
			if r.StatusCode != 403 || strings.TrimSpace(string(b)) != gateMessage {
				t.Fatalf("direct %s (forged headers %v): %d %s", p, forged, r.StatusCode, b)
			}
		}
	}
}

// checkDNSEdge proves the edge half of AC11 where KyDNS sees the edge at egress_address:
// health, trusted HTTPS headers and the closed /setup through the edge.
func checkDNSEdge(t *testing.T, ctx context.Context, newClient func() *http.Client) {
	t.Helper()
	c := newClient()
	if r, b := get(t, ctx, c, "https://"+catalogDNSURLHost+"/healthz"); r.StatusCode != 200 {
		t.Fatalf("health through the edge: %d %s", r.StatusCode, b)
	}
	if code, n := dnsEdgePeer(t, ctx, c); code != 200 || !n.ProxyHeadersTrusted || n.Scheme != "https" {
		t.Fatalf("network through the edge: %d %+v", code, n)
	}
	if r, _ := get(t, ctx, c, "https://"+catalogDNSURLHost+"/setup"); r.StatusCode != 303 || r.Header.Get("Location") != "/login" {
		t.Fatalf("/setup through the edge: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
}

// checkDNSFailClosed proves the edge sees only health and the gate's refusal where KyDNS does
// not see the edge at egress_address.
func checkDNSFailClosed(t *testing.T, ctx context.Context, newClient func() *http.Client) {
	t.Helper()
	c := newClient()
	if r, b := get(t, ctx, c, "https://"+catalogDNSURLHost+"/healthz"); r.StatusCode != 200 {
		t.Fatalf("health through the edge: %d %s", r.StatusCode, b)
	}
	for _, p := range []string{"/", "/setup", "/auth/sso/login", "/api/v1/installer/network"} {
		if r, b := get(t, ctx, c, "https://"+catalogDNSURLHost+p); r.StatusCode != 403 || strings.TrimSpace(string(b)) != gateMessage {
			t.Fatalf("%s through an untrusted edge: %d %s", p, r.StatusCode, b)
		}
	}
}

// dnsSignOn signs the owner's admin in to KyDNS through KyIdentity behind the edge, as a
// browser would, and proves KyIdentity refuses the everyday account KyDNS's client.
func dnsSignOn(t *testing.T, ctx context.Context, c *kube.Client, newClient func() *http.Client) {
	t.Helper()
	identity, dns := "https://"+catalogIdentityURLHost, "https://"+catalogDNSURLHost
	setupDir := "/data/installer/" + identityFixturePreparation().SetupID
	if r, _ := get(t, ctx, newClient(), dns+"/"); r.StatusCode/100 != 3 || r.Header.Get("Location") != "/login" {
		t.Fatalf("GET / without a session: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
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
		client := newClient()
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

		r, b = send("GET", dns+"/auth/sso/login", nil, "")
		authorize, err := r.Location()
		if err != nil || r.StatusCode/100 != 3 || authorize.Host != catalogIdentityURLHost {
			t.Fatalf("KyDNS sign-in start: %d %s", r.StatusCode, b)
		}
		q := authorize.Query()
		if q.Get("client_id") != "kydns" || q.Get("redirect_uri") != dns+"/auth/sso/callback" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
			t.Fatalf("authorization request: %s", authorize)
		}
		r, b = send("GET", authorize.String(), nil, "")
		callback, _ := r.Location()
		if !account.admin {
			// KyIdentity refuses: no code reaches KyDNS's callback and no KyDNS session results.
			if r.StatusCode == 302 && callback != nil && "https://"+callback.Host == dns && callback.Query().Get("code") != "" {
				t.Fatalf("KyIdentity authorized %s for KyDNS: %v", account.username, callback)
			}
			if after, _ := send("GET", dns+"/", nil, ""); after.StatusCode/100 != 3 || after.Header.Get("Location") != "/login" {
				t.Fatalf("%s has a KyDNS session: %d", account.username, after.StatusCode)
			}
			t.Logf("KyIdentity refused %s for KyDNS: HTTP %d %v", account.username, r.StatusCode, callback)
			continue
		}
		if r.StatusCode != 302 || callback == nil || "https://"+callback.Host != dns || callback.Path != "/auth/sso/callback" || callback.Query().Get("code") == "" || callback.Query().Get("state") != q.Get("state") {
			t.Fatalf("KyIdentity authorization of %s for KyDNS: %d %v %s", account.username, r.StatusCode, callback, b)
		}
		r, b = send("GET", callback.String(), nil, "")
		if r.StatusCode/100 != 3 || r.Header.Get("Location") != "/" {
			t.Fatalf("KyDNS callback for %s: %d %s", account.username, r.StatusCode, b)
		}
		secure := false
		for _, ck := range r.Cookies() {
			if ck.Name == "kydns_session" {
				secure = ck.Secure && ck.HttpOnly
			}
		}
		if !secure {
			t.Fatalf("KyDNS session cookie is not Secure and HttpOnly: %v", r.Header["Set-Cookie"])
		}
		for _, p := range []string{"/", "/records"} {
			if r, b := send("GET", dns+p, nil, ""); r.StatusCode != 200 {
				t.Fatalf("%s as %s: %d %s", p, account.username, r.StatusCode, b)
			}
		}
		t.Logf("%s signed in to KyDNS through KyIdentity: / and /records 200", account.username)
	}
}
