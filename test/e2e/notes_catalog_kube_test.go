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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

// catalogNotesHost is where TestKubernetesNPMEdge routes the catalog KyNotes.
const catalogNotesHost = "notes.example.test"

// catalogNotesURLHost is its URL host behind the lan edge.
const catalogNotesURLHost = catalogNotesHost + ":30443"

// notesRouteStep is the edge step that routes KyNotes (the NPM host step's ID).
var notesRouteStep = func() string {
	sum := sha256.Sum256([]byte(catalogNotesHost))
	return "k8s.edge." + hex.EncodeToString(sum[:8])
}()

// addCatalogNotes installs the catalog KyNotes behind the edge and selects its setup, after
// KyIdentity's first setup ran: apply adds its client with a setup amendment.
func addCatalogNotes(st *stack.Stack, certID int) {
	st.Apps = append(st.Apps, stack.App{Name: "kynotes", Target: "k8s"})
	st.Edges[0].Routes = append(st.Edges[0].Routes, stack.Route{App: "kynotes", Hostname: catalogNotesHost,
		ForwardHost: "app.kyq-kynotes.svc.cluster.local", ForwardPort: 8080, CertificateID: certID})
	st.Integrations = append(st.Integrations, stack.Integration{Product: "kynotes", Target: "k8s",
		Bundle: "/data/installer/setup.json", IdentityClient: "kynotes"})
}

// notesInspector adds a BusyBox ephemeral container, as KyNotes' user, to KyNotes' distroless
// pod with its data claim read-only at /v (ephemeral containers take no subPath, so the
// claim's private directory, KyNotes' /data, is /v/private). It returns a command runner.
func notesInspector(t *testing.T, ctx context.Context, cs kubernetes.Interface, c *kube.Client) func(args ...string) string {
	t.Helper()
	ns, pod, _, err := c.AppPod(ctx, "kynotes")
	if err != nil {
		t.Fatal(err)
	}
	p, err := cs.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p.Spec.EphemeralContainers = append(p.Spec.EphemeralContainers, corev1.EphemeralContainer{EphemeralContainerCommon: corev1.EphemeralContainerCommon{
		Name: "inspect", Image: kube.CredentialGeneratorImage, Command: []string{"sleep", "600"},
		VolumeMounts: []corev1.VolumeMount{{Name: "vol-data", MountPath: "/v", ReadOnly: true}},
		SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(65532)), RunAsGroup: ptr.To(int64(65532)), AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
	}})
	if _, err := cs.CoreV1().Pods(ns).UpdateEphemeralContainers(ctx, pod, p, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Second) {
		p, err := cs.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
		if err == nil && len(p.Status.EphemeralContainerStatuses) > 0 && p.Status.EphemeralContainerStatuses[0].State.Running != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("KyNotes inspector did not start")
		}
	}
	return func(args ...string) string {
		t.Helper()
		b, err := c.Exec(ctx, ns, pod, "inspect", args)
		if err != nil {
			t.Fatalf("inspect %v: %v", args, err)
		}
		return strings.TrimSpace(string(b))
	}
}

// checkCatalogNotes proves the deployed catalog KyNotes after kynotes.setup passed: restricted
// pod that drains for 17 minutes, private setup files, the product's own present-only check,
// health through the edge, the handover and no local password. It returns the transferred
// secrets.
func checkCatalogNotes(t *testing.T, ctx context.Context, cs kubernetes.Interface, c *kube.Client, out string, code func(host, path string) string) []string {
	t.Helper()
	if status(out, "kynotes.setup") != "ok" || status(out, notesRouteStep) != "ok" {
		t.Fatalf("kynotes.setup = %q, route = %q", status(out, "kynotes.setup"), status(out, notesRouteStep))
	}
	for _, want := range []string{"KyNotes at https://" + catalogNotesURLHost + "/: sign in through KyIdentity as prepared-admin", "or prepared-user, whose account", recoveryLine(out)} {
		if !strings.Contains(out, want) {
			t.Fatalf("KyNotes handover lacks %q", want)
		}
	}
	ns, pod, container, err := c.AppPod(ctx, "kynotes")
	if err != nil {
		t.Fatal(err)
	}
	p, err := cs.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil || *p.Spec.TerminationGracePeriodSeconds != 1020 || *p.Spec.Containers[0].SecurityContext.RunAsUser != 65532 || p.Spec.InitContainers[0].Name != "private-volumes" {
		t.Fatalf("pod spec: %v", err)
	}
	for _, e := range p.Spec.Containers[0].Env {
		if strings.HasPrefix(e.Name, "BOOTSTRAP_") {
			t.Fatalf("KyNotes runs with %s: a local password", e.Name)
		}
	}
	if logs, _ := cs.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Container: "app"}).DoRaw(ctx); bytes.Contains(logs, []byte("Initial administrator account created")) {
		t.Fatal("KyNotes created a local administrator")
	}
	if n, _ := cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); n.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Fatalf("namespace = %v", n.Labels)
	}
	inspect := notesInspector(t, ctx, cs, c)
	dir := "/v/private/installer"
	want := dir + " 700 65532\n" + dir + "/client.secret 600 65532\n" + dir + "/directory.secret 600 65532\n" + dir + "/setup.json 600 65532"
	if got := inspect("stat", "-c", "%n %a %u", dir, dir+"/client.secret", dir+"/directory.secret", dir+"/setup.json"); got != want {
		t.Fatalf("setup files:\n%s", got)
	}
	report, err := c.Exec(ctx, ns, pod, container, []string{"/kynotes-server", "apply-setup", "--file", "/data/installer/setup.json", "--check"})
	var r struct {
		Results []struct{ Section, Status string }
	}
	if err != nil || json.Unmarshal(report, &r) != nil || len(r.Results) == 0 {
		t.Fatalf("KyNotes check:\n%s %v", report, err)
	}
	for _, x := range r.Results {
		if x.Status != "present" {
			t.Fatalf("KyNotes check reports %s %s:\n%s", x.Section, x.Status, report)
		}
	}
	if got := code(catalogNotesHost, "/healthz"); got != "200" {
		t.Fatalf("KyNotes health through the edge: HTTP %s", got)
	}
	return []string{inspect("cat", dir+"/client.secret"), inspect("cat", dir+"/directory.secret")}
}

// notesSignOn signs the owner's accounts in to KyNotes through KyIdentity behind the edge, as
// a browser would, and proves KyNotes' admin separation: the admin may administer but opens no
// notes, the everyday account opens notes but cannot administer. Test-only plumbing:
// testClusterDNS ran and KyNotes' pod trusts the edge's throwaway certificate.
func notesSignOn(t *testing.T, ctx context.Context, c *kube.Client, certPEM []byte, nodeIP string) {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("invalid edge certificate")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != catalogIdentityURLHost && address != catalogNotesURLHost {
			return nil, errors.New("unselected destination " + address)
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(nodeIP, "30443"))
	}
	t.Cleanup(transport.CloseIdleConnections)
	identity, notes := "https://"+catalogIdentityURLHost, "https://"+catalogNotesURLHost
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

		r, b = send("GET", notes+"/api/v1/auth/oidc/login", nil, "")
		authorize, err := r.Location()
		if err != nil || r.StatusCode != 302 || authorize.Host != catalogIdentityURLHost {
			t.Fatalf("KyNotes sign-in start: %d %s", r.StatusCode, b)
		}
		q := authorize.Query()
		if q.Get("client_id") != "kynotes" || q.Get("redirect_uri") != notes+"/api/v1/auth/oidc/callback" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
			t.Fatalf("authorization request: %s", authorize)
		}
		r, _ = send("GET", authorize.String(), nil, "")
		callback, _ := r.Location()
		if r.StatusCode != 302 || callback == nil || "https://"+callback.Host != notes || callback.Path != "/api/v1/auth/oidc/callback" || callback.Query().Get("code") == "" || callback.Query().Get("state") != q.Get("state") {
			t.Fatalf("KyIdentity authorization of %s for KyNotes: %d %v", account.username, r.StatusCode, callback)
		}
		if r, b = send("GET", callback.String(), nil, ""); r.StatusCode != 302 || r.Header.Get("Location") != "/" {
			t.Fatalf("KyNotes callback for %s: %d %s", account.username, r.StatusCode, b)
		}
		admin, _ := send("GET", notes+"/api/v1/admin/backup/status", nil, "")
		r, b = send("GET", notes+"/api/v1/containers", nil, "")
		var e struct{ Error struct{ Code string } }
		json.Unmarshal(b, &e)
		if account.admin && (admin.StatusCode != 200 || r.StatusCode != 403 || e.Error.Code != "admin_account") ||
			!account.admin && (admin.StatusCode != 403 || r.StatusCode != 200) {
			t.Fatalf("%s: backup status %d, containers %d %s", account.username, admin.StatusCode, r.StatusCode, b)
		}
		t.Logf("%s signed in to KyNotes through KyIdentity: backup status %d, containers %d", account.username, admin.StatusCode, r.StatusCode)
	}
}
