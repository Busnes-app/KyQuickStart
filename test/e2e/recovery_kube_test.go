//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
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
)

// catalogRecoveryHost is where TestKubernetesNPMEdge routes the catalog KyRecovery.
const catalogRecoveryHost = "recovery.example.test"

// catalogRecoveryURLHost is its URL host behind the lan edge.
const catalogRecoveryURLHost = catalogRecoveryHost + ":30443"

const recoverySetupDir = "/app/data/installer/6a1f0c2e-3b4d-4e5f-8a9b-0c1d2e3f4a5b"

// addCatalogRecovery installs the catalog KyRecovery behind the edge and selects its setup;
// apply adds its client to the catalog KyIdentity's first setup.
func addCatalogRecovery(st *stack.Stack, certID int) {
	st.Apps = append(st.Apps, stack.App{Name: "kyrecovery", Target: "k8s"})
	st.Edges[0].Routes = append(st.Edges[0].Routes, stack.Route{App: "kyrecovery", Hostname: catalogRecoveryHost,
		ForwardHost: "app.kyq-kyrecovery.svc.cluster.local", ForwardPort: 8095, CertificateID: certID})
	st.Integrations = append(st.Integrations, stack.Integration{Product: "kyrecovery", Target: "k8s",
		Bundle: recoverySetupDir + "/setup.json", IdentityClient: "kyrecovery"})
}

// checkCatalogRecovery proves the deployed catalog KyRecovery: restricted pod as its image's
// user on a private data directory, setup applied from private files, health through the
// edge and the custodian ceremony left to a human. It returns the transferred secrets.
func checkCatalogRecovery(t *testing.T, ctx context.Context, cs kubernetes.Interface, kubeconfig, out string, get func(host, path string) string) []string {
	t.Helper()
	for _, id := range []string{"kyrecovery.deploy", "kyrecovery.health", "kyrecovery.setup"} {
		if status(out, id) != "ok" {
			t.Fatalf("%s = %q", id, status(out, id))
		}
	}
	if !strings.Contains(out, "Outstanding human step, unless already done: the KyRecovery custodian ceremony.") || !strings.Contains(out, "https://"+catalogRecoveryURLHost+"/ through KyIdentity as prepared-admin") {
		t.Fatal("custodian ceremony not handed over")
	}
	c, err := kube.Connect(kubeconfig, "")
	if err != nil {
		t.Fatal(err)
	}
	namespace, pod, container, err := c.AppPod(ctx, "kyrecovery")
	if err != nil {
		t.Fatal(err)
	}
	p, err := cs.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil || *p.Spec.Containers[0].SecurityContext.RunAsUser != 10001 || p.Spec.InitContainers[0].Name != "private-volumes" {
		t.Fatalf("pod spec: %v", err)
	}
	if ns, _ := cs.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{}); ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Fatalf("namespace = %v", ns.Labels)
	}
	stat, err := c.Exec(ctx, namespace, pod, container, []string{"stat", "-c", "%n %a %u", "/app/data", recoverySetupDir, recoverySetupDir + "/client.secret", recoverySetupDir + "/directory.secret", recoverySetupDir + "/setup.json"})
	want := "/app/data 700 10001\n" + recoverySetupDir + " 700 10001\n" + recoverySetupDir + "/client.secret 600 10001\n" + recoverySetupDir + "/directory.secret 600 10001\n" + recoverySetupDir + "/setup.json 600 10001"
	if err != nil || strings.TrimSpace(string(stat)) != want {
		t.Fatalf("setup files:\n%s %v", stat, err)
	}
	// The product's own read-only inspection of the live server agrees.
	report, err := c.Exec(ctx, namespace, pod, container, []string{"/app/kyrecovery", "apply-setup", "--file", recoverySetupDir + "/setup.json", "--check"})
	var r struct {
		Schema                    string
		Created, Present, Pending []json.RawMessage
	}
	if err != nil || json.Unmarshal(report, &r) != nil || len(r.Created) != 0 || len(r.Pending) != 0 || len(r.Present) == 0 {
		t.Fatalf("KyRecovery check:\n%s %v", report, err)
	}
	var health struct{ Schema, Service, Status string }
	if body := get(catalogRecoveryHost, "/healthz"); json.Unmarshal([]byte(body), &health) != nil || health.Service != "kyrecovery" || health.Status != "ok" {
		t.Fatalf("health through the edge:\n%s", body)
	}
	var secrets []string
	for _, f := range []string{"client.secret", "directory.secret"} {
		b, err := c.Exec(ctx, namespace, pod, container, []string{"cat", recoverySetupDir + "/" + f})
		if err != nil || len(b) < 32 {
			t.Fatalf("%s: %v", f, err)
		}
		secrets = append(secrets, string(b))
	}
	return secrets
}

// testClusterDNS makes cluster DNS name every routed host after the node, whose NodePort is
// the lan edge. Test-only plumbing.
func testClusterDNS(t *testing.T, ctx context.Context, cs kubernetes.Interface, nodeIP string) {
	t.Helper()
	dns, err := cs.CoreV1().ConfigMaps("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hosts := strings.Join([]string{catalogIdentityHost, catalogRecoveryHost, catalogPulseHost, catalogDriveHost, catalogNotesHost, catalogCalendarHost, catalogDNSHost, catalogMessageHost, "hello.example.test"}, " ")
	corefile := strings.Replace(dns.Data["Corefile"], "    kubernetes cluster.local", "    hosts {\n       "+nodeIP+" "+hosts+"\n       fallthrough\n    }\n    kubernetes cluster.local", 1)
	if corefile == dns.Data["Corefile"] {
		t.Fatalf("unexpected Corefile:\n%s", corefile)
	}
	dns.Data["Corefile"] = corefile
	if _, err := cs.CoreV1().ConfigMaps("kube-system").Update(ctx, dns, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	dnsSel := metav1.ListOptions{LabelSelector: "k8s-app=kube-dns"}
	old, err := cs.CoreV1().Pods("kube-system").List(ctx, dnsSel)
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.CoreV1().Pods("kube-system").DeleteCollection(ctx, metav1.DeleteOptions{}, dnsSel); err != nil {
		t.Fatal(err)
	}
	// Lookups fail until replacements serve the new Corefile: wait for every replica to be a
	// new, ready pod.
	stale := map[string]bool{}
	for _, p := range old.Items {
		stale[p.Name] = true
	}
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Second) {
		dep, err := cs.AppsV1().Deployments("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
		pods, perr := cs.CoreV1().Pods("kube-system").List(ctx, dnsSel)
		ready := 0
		if err == nil && perr == nil {
			for _, p := range pods.Items {
				for _, c := range p.Status.Conditions {
					if !stale[p.Name] && p.DeletionTimestamp == nil && c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
						ready++
					}
				}
			}
			if ready == int(*dep.Spec.Replicas) && len(pods.Items) == ready {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("CoreDNS did not come back with the test hosts: %d ready", ready)
		}
	}
}

// catalogSignOn signs the owner's accounts in to KyRecovery and kyPulse through KyIdentity,
// all behind the edge, as a browser would: each app must send the admin to KyIdentity with its
// own client and accept them back as its administrator, and KyIdentity must refuse the everyday
// account. Test-only plumbing: testClusterDNS ran, and both apps' pods trust the edge's
// throwaway certificate.
func catalogSignOn(t *testing.T, ctx context.Context, cs kubernetes.Interface, kubeconfig string, certPEM []byte, nodeIP string) {
	t.Helper()
	c, err := kube.Connect(kubeconfig, "")
	if err != nil {
		t.Fatal(err)
	}
	trustTestCA(t, ctx, cs, c, "kyrecovery", certPEM)
	trustTestCA(t, ctx, cs, c, "kypulse", certPEM)

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("invalid edge certificate")
	}
	// Certificate and hostname verification stay on; only the dial goes to the NodePort.
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != catalogIdentityURLHost && address != catalogRecoveryURLHost && address != catalogPulseURLHost {
			return nil, errors.New("unselected destination " + address)
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(nodeIP, "30443"))
	}
	t.Cleanup(transport.CloseIdleConnections)
	identity := "https://" + catalogIdentityURLHost
	apps := []struct{ client, origin, login, callback string }{
		{"kyrecovery", "https://" + catalogRecoveryURLHost, "/api/auth/login", "/api/auth/callback"},
		{"kypulse", "https://" + catalogPulseURLHost, "/api/sso/kysignon/login", "/api/sso/kysignon/callback"},
	}
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

		for _, app := range apps {
			r, b = send("GET", app.origin+app.login, nil, "")
			authorize, err := r.Location()
			if err != nil || r.StatusCode != 302 || authorize.Host != catalogIdentityURLHost {
				t.Fatalf("%s sign-in start: %d %s", app.client, r.StatusCode, b)
			}
			q := authorize.Query()
			if q.Get("client_id") != app.client || q.Get("redirect_uri") != app.origin+app.callback || q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("state") == "" {
				t.Fatalf("authorization request: %s", authorize)
			}
			r, _ = send("GET", authorize.String(), nil, "")
			callback, _ := r.Location()
			got := callback != nil && "https://"+callback.Host == app.origin && callback.Path == app.callback && callback.Query().Get("code") != "" && callback.Query().Get("state") == q.Get("state")
			if !account.admin {
				if got {
					t.Fatalf("KyIdentity issued a %s code to the everyday account", app.client)
				}
				t.Logf("everyday account refused %s by KyIdentity: HTTP %d %v", app.client, r.StatusCode, callback)
				continue
			}
			if r.StatusCode != 302 || !got {
				t.Fatalf("KyIdentity authorization for %s: %d %v", app.client, r.StatusCode, callback)
			}
			r, b = send("GET", callback.String(), nil, "")
			if r.StatusCode != 302 || r.Header.Get("Location") != "/" {
				t.Fatalf("%s callback: %d %s", app.client, r.StatusCode, b)
			}
			_, b = send("GET", app.origin+"/api/auth/me", nil, "")
			var me struct {
				Authenticated bool
				User          struct{ Name, Username, Role string }
			}
			if json.Unmarshal(b, &me) != nil || !me.Authenticated || me.User.Role != "admin" {
				t.Fatalf("%s session: %s", app.client, b)
			}
			t.Logf("owner admin signed in to %s through KyIdentity as %q, role %s", app.client, me.User.Name+me.User.Username, me.User.Role)
			if app.client == "kypulse" {
				pulseTargetsHealthy(t, ctx, send, app.origin)
			}
		}
	}
}

// trustTestCA restarts app with the edge's throwaway certificate as its only trusted root.
func trustTestCA(t *testing.T, ctx context.Context, cs kubernetes.Interface, c *kube.Client, app string, certPEM []byte) {
	t.Helper()
	ns := "kyq-" + app
	_, before, _, err := c.AppPod(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().ConfigMaps(ns).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "test-ca"}, Data: map[string]string{"ca.pem": string(certPEM)}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	dep, err := cs.AppsV1().Deployments(ns).Get(ctx, "app", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec := &dep.Spec.Template.Spec
	spec.Volumes = append(spec.Volumes, corev1.Volume{Name: "test-ca", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "test-ca"}}}})
	spec.Containers[0].VolumeMounts = append(spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "test-ca", MountPath: "/test-ca", ReadOnly: true})
	spec.Containers[0].Env = append(spec.Containers[0].Env, corev1.EnvVar{Name: "SSL_CERT_FILE", Value: "/test-ca/ca.pem"})
	if _, err := cs.AppsV1().Deployments(ns).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Minute); ; time.Sleep(2 * time.Second) {
		_, pod, _, err := c.AppPod(ctx, app)
		if err == nil && pod != before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not restart with the test CA", app)
		}
	}
}
