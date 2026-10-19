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
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// catalogCalendarHost is where TestKubernetesNPMEdge routes the catalog KyCalendar.
const catalogCalendarHost = "calendar.example.test"

// catalogCalendarURLHost is its URL host behind the lan edge.
const catalogCalendarURLHost = catalogCalendarHost + ":30443"

const calendarSetupDir = "/app/data/installer/6b2e9d4f-1a3c-4e5b-8f7a-2c9d0e1f3a4b"

// calendarRouteStep is the edge step that routes KyCalendar (the NPM host step's ID).
var calendarRouteStep = func() string {
	sum := sha256.Sum256([]byte(catalogCalendarHost))
	return "k8s.edge." + hex.EncodeToString(sum[:8])
}()

// addCatalogCalendar installs the catalog KyCalendar behind the edge and selects its setup,
// after KyIdentity's first setup ran: apply adds its client with a setup amendment.
func addCatalogCalendar(st *stack.Stack, certID int) {
	st.Apps = append(st.Apps, stack.App{Name: "kycalendar", Target: "k8s"})
	st.Edges[0].Routes = append(st.Edges[0].Routes, stack.Route{App: "kycalendar", Hostname: catalogCalendarHost,
		ForwardHost: "app.kyq-kycalendar.svc.cluster.local", ForwardPort: 8080, CertificateID: certID})
	st.Integrations = append(st.Integrations, stack.Integration{Product: "kycalendar", Target: "k8s",
		Bundle: calendarSetupDir + "/setup.json", IdentityClient: "kycalendar"})
}

// checkCatalogCalendar proves the deployed catalog KyCalendar after its first apply: every
// step ok, restricted pod that drains for 20 minutes, private setup files, the product's own
// check naming both owner accounts, readiness through the edge, the handover and the 0600
// break-glass password file. It returns the transferred secrets and that password.
func checkCatalogCalendar(t *testing.T, ctx context.Context, cs kubernetes.Interface, c *kube.Client, kubeconfig, out string, code func(host, path string) string) []string {
	t.Helper()
	if !strings.Contains(out, "ran without kycalendar's client: adding it with setup amendment ") {
		t.Fatal("KyCalendar amendment not announced")
	}
	for _, id := range []string{"kyidentity.amend.kycalendar", "kycalendar.deploy", "kycalendar.health", "kycalendar.setup", calendarRouteStep} {
		if status(out, id) != "ok" {
			t.Fatalf("%s = %q", id, status(out, id))
		}
	}
	for _, want := range []string{
		"KyCalendar at https://" + catalogCalendarURLHost + "/: sign in through KyIdentity as prepared-admin (its console only",
		"or prepared-user (calendars)",
		"kubectl --kubeconfig '" + kubeconfig + "' -n kyq-kycalendar exec deployment/app -- cat /app/data/initial-admin-password",
		"Only after that, delete the file:",
		"pair it with KyRecovery",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("KyCalendar handover lacks %q", want)
		}
	}
	ns, pod, container, err := c.AppPod(ctx, "kycalendar")
	if err != nil {
		t.Fatal(err)
	}
	p, err := cs.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil || *p.Spec.TerminationGracePeriodSeconds != 1200 || *p.Spec.Containers[0].SecurityContext.RunAsUser != 10001 || p.Spec.InitContainers[0].Name != "private-volumes" {
		t.Fatalf("pod spec: %v", err)
	}
	for _, e := range p.Spec.Containers[0].Env {
		if e.Name == "KY_ADMIN_PASSWORD" || e.Name == "KY_TRUSTED_PROXIES" || strings.HasPrefix(e.Name, "KY_SCIM") {
			t.Fatalf("KyCalendar runs with %s", e.Name)
		}
	}
	if n, _ := cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); n.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Fatalf("namespace = %v", n.Labels)
	}
	d := calendarSetupDir
	stat, err := c.Exec(ctx, ns, pod, container, []string{"stat", "-c", "%n %a %u", d, d + "/client.secret", d + "/directory.secret", d + "/setup.json", "/app/data/initial-admin-password"})
	want := d + " 700 10001\n" + d + "/client.secret 600 10001\n" + d + "/directory.secret 600 10001\n" + d + "/setup.json 600 10001\n/app/data/initial-admin-password 600 10001"
	if err != nil || strings.TrimSpace(string(stat)) != want {
		t.Fatalf("setup files:\n%s %v", stat, err)
	}
	report, err := c.Exec(ctx, ns, pod, container, []string{"/app/kycalendar", "apply-setup", "--file", d + "/setup.json", "--check"})
	var r struct {
		Created, Pending []json.RawMessage
		Present          []struct{ Kind, Name string }
	}
	if err != nil || json.Unmarshal(report, &r) != nil || r.Created == nil || len(r.Created) != 0 || len(r.Pending) != 0 {
		t.Fatalf("KyCalendar check:\n%s %v", report, err)
	}
	var identities []string
	for _, o := range r.Present {
		if o.Kind == "identity" {
			identities = append(identities, o.Name)
		}
	}
	if len(identities) != 2 {
		t.Fatalf("KyCalendar check names identities %v:\n%s", identities, report)
	}
	t.Logf("KyCalendar check: present identities %v", identities)
	if got := code(catalogCalendarHost, "/api/installer/network"); got != "200" {
		t.Fatalf("KyCalendar readiness through the edge: HTTP %s", got)
	}
	var secrets []string
	for _, f := range []string{d + "/client.secret", d + "/directory.secret", "/app/data/initial-admin-password"} {
		b, err := c.Exec(ctx, ns, pod, container, []string{"cat", f})
		if err != nil || len(strings.TrimSpace(string(b))) < 12 {
			t.Fatalf("%s: %v", f, err)
		}
		secrets = append(secrets, strings.TrimSpace(string(b)))
	}
	return secrets
}

// calendarSignOn signs the owner's accounts in to KyCalendar and proves its admin separation:
// the admin reaches the console but no calendars, the everyday account its calendars but not
// the console.
func calendarSignOn(t *testing.T, ctx context.Context, c *kube.Client, certPEM []byte, nodeIP string) {
	t.Helper()
	ownerSignOn(t, ctx, c, certPEM, nodeIP, catalogCalendarURLHost, "kycalendar", "/api/sso/kysignon", func(username string, admin bool, get func(string) (*http.Response, []byte)) {
		users, _ := get("/api/admin/users")
		calendars, b := get("/api/calendars")
		if admin && (users.StatusCode != 200 || calendars.StatusCode != 403) || !admin && (users.StatusCode != 403 || calendars.StatusCode != 200) {
			t.Fatalf("%s: admin/users %d, calendars %d %s", username, users.StatusCode, calendars.StatusCode, b)
		}
		t.Logf("%s signed in to KyCalendar through KyIdentity: admin/users %d, calendars %d", username, users.StatusCode, calendars.StatusCode)
	})
}

// ownerSignOn signs the owner's admin and everyday accounts in to the app at urlHost through
// KyIdentity behind the edge, as a browser would: client_id, redirect_uri under sso, S256 PKCE,
// nonce and state. prove then checks each session with get, a GET on the app. Test-only
// plumbing: testClusterDNS ran and the app's pod trusts the edge's throwaway certificate.
func ownerSignOn(t *testing.T, ctx context.Context, c *kube.Client, certPEM []byte, nodeIP, urlHost, clientID, sso string, prove func(username string, admin bool, get func(path string) (*http.Response, []byte))) {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("invalid edge certificate")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != catalogIdentityURLHost && address != urlHost {
			return nil, errors.New("unselected destination " + address)
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(nodeIP, "30443"))
	}
	t.Cleanup(transport.CloseIdleConnections)
	identity, app := "https://"+catalogIdentityURLHost, "https://"+urlHost
	setupDir := "/data/installer/" + identityFixturePreparation().SetupID
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

		r, b = send("GET", app+sso+"/login", nil, "")
		authorize, err := r.Location()
		if err != nil || r.StatusCode != 302 || authorize.Host != catalogIdentityURLHost {
			t.Fatalf("%s sign-in start: %d %s", clientID, r.StatusCode, b)
		}
		q := authorize.Query()
		if q.Get("client_id") != clientID || q.Get("redirect_uri") != app+sso+"/callback" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
			t.Fatalf("authorization request: %s", authorize)
		}
		r, _ = send("GET", authorize.String(), nil, "")
		callback, _ := r.Location()
		if r.StatusCode != 302 || callback == nil || "https://"+callback.Host != app || callback.Path != sso+"/callback" || callback.Query().Get("code") == "" || callback.Query().Get("state") != q.Get("state") {
			t.Fatalf("KyIdentity authorization of %s for %s: %d %v", account.username, clientID, r.StatusCode, callback)
		}
		if r, b = send("GET", callback.String(), nil, ""); r.StatusCode != 302 || r.Header.Get("Location") != "/" {
			t.Fatalf("%s callback for %s: %d %s", clientID, account.username, r.StatusCode, b)
		}
		prove(account.username, account.admin, func(path string) (*http.Response, []byte) { return send("GET", app+path, nil, "") })
	}
}
