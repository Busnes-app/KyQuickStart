//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// catalogMessageHost is where TestKubernetesNPMEdge routes the catalog KyMessages.
const catalogMessageHost = "message.example.test"

// catalogMessageURLHost is its URL host behind the lan edge.
const catalogMessageURLHost = catalogMessageHost + ":30443"

const messageSetupDir = "/app/data/installer/4e7a1c3b-9d2f-4b6e-8a5c-1f0e2d3c4b5a"

// messageRouteStep is the edge step that routes KyMessages (the NPM host step's ID).
var messageRouteStep = func() string {
	sum := sha256.Sum256([]byte(catalogMessageHost))
	return "k8s.edge." + hex.EncodeToString(sum[:8])
}()

// bootstrapPassword is KyMessages' one log line naming its generated break-glass password.
var bootstrapPassword = regexp.MustCompile(`\[SECURITY\] Initial bootstrap: Created admin account\. Username: admin \| Password: (\S+)`)

// addCatalogMessage installs the catalog KyMessages behind the edge and selects its setup,
// after KyIdentity's first setup ran: apply adds its client with a setup amendment.
func addCatalogMessage(st *stack.Stack, certID int) {
	st.Apps = append(st.Apps, stack.App{Name: "kymessage", Target: "k8s"})
	st.Edges[0].Routes = append(st.Edges[0].Routes, stack.Route{App: "kymessage", Hostname: catalogMessageHost,
		ForwardHost: "app.kyq-kymessage.svc.cluster.local", ForwardPort: 8080, CertificateID: certID})
	st.Integrations = append(st.Integrations, stack.Integration{Product: "kymessage", Target: "k8s",
		Bundle: messageSetupDir + "/setup.json", IdentityClient: "kymessage"})
}

// checkCatalogMessage proves the deployed catalog KyMessages after its first apply: every step
// ok, the handover, a restricted pod with no Matrix, admin password or SCIM env, private setup
// files, the product's own check naming both owner accounts, one restart (setup activation),
// readiness through the edge, and the handover's init-admin replacing the break-glass password.
// It returns the transferred secrets and the generated break-glass password.
func checkCatalogMessage(t *testing.T, ctx context.Context, cs kubernetes.Interface, c *kube.Client, node, kubeconfig, out string, code func(host, path string) string) []string {
	t.Helper()
	if !strings.Contains(out, "ran without kymessage's client: adding it with setup amendment ") {
		t.Fatal("KyMessages amendment not announced")
	}
	for _, id := range []string{"kyidentity.amend.kymessage", "kymessage.deploy", "kymessage.health", "kymessage.setup", messageRouteStep} {
		if status(out, id) != "ok" {
			t.Fatalf("%s = %q", id, status(out, id))
		}
	}
	kubectl := "kubectl --kubeconfig '" + kubeconfig + "' -n kyq-kymessage "
	initAdmin := "exec -it deployment/app -- /app/kymessages init-admin"
	for _, want := range []string{
		"KyMessages at https://" + catalogMessageURLHost + "/: sign in through KyIdentity as prepared-admin (its console only) or prepared-user.",
		`Chat (Matrix) is not installed by this release: members see "chat is not available".`,
		kubectl + "logs deployment/app --previous",
		kubectl + initAdmin,
		"pair it with KyRecovery",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("KyMessages handover lacks %q", want)
		}
	}
	ns, pod, container, err := c.AppPod(ctx, "kymessage")
	if err != nil {
		t.Fatal(err)
	}
	p, err := cs.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil || *p.Spec.TerminationGracePeriodSeconds != 1260 || *p.Spec.Containers[0].SecurityContext.RunAsUser != 10001 || p.Spec.InitContainers[0].Name != "private-volumes" {
		t.Fatalf("pod spec: %v", err)
	}
	for _, e := range p.Spec.Containers[0].Env {
		if e.Name == "KY_ADMIN_PASSWORD" || e.Name == "KY_SCIM_TOKEN" || strings.HasPrefix(e.Name, "KY_MATRIX_") {
			t.Fatalf("KyMessages runs with %s", e.Name)
		}
	}
	// Its setup activates by a graceful restart; nothing else restarted it.
	if n := p.Status.ContainerStatuses[0].RestartCount; n != 1 {
		t.Fatalf("KyMessages restart count = %d", n)
	}
	if n, _ := cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); n.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Fatalf("namespace = %v", n.Labels)
	}
	d := messageSetupDir
	stat, err := c.Exec(ctx, ns, pod, container, []string{"stat", "-c", "%n %a %u", d, d + "/client.secret", d + "/directory.secret", d + "/setup.json"})
	want := d + " 700 10001\n" + d + "/client.secret 600 10001\n" + d + "/directory.secret 600 10001\n" + d + "/setup.json 600 10001"
	if err != nil || strings.TrimSpace(string(stat)) != want {
		t.Fatalf("setup files:\n%s %v", stat, err)
	}
	report, err := c.Exec(ctx, ns, pod, container, []string{"/app/kymessages", "apply-setup", "--file", d + "/setup.json", "--check"})
	var r struct {
		Created, Pending []json.RawMessage
		Present          []struct{ Kind, Name string }
	}
	if err != nil || json.Unmarshal(report, &r) != nil || r.Created == nil || len(r.Created) != 0 || len(r.Pending) != 0 {
		t.Fatalf("KyMessages check:\n%s %v", report, err)
	}
	var identities []string
	for _, o := range r.Present {
		if o.Kind == "identity" {
			identities = append(identities, o.Name)
		}
	}
	if len(identities) != 2 {
		t.Fatalf("KyMessages check names identities %v:\n%s", identities, report)
	}
	t.Logf("KyMessages check: present identities %v", identities)
	if got := code(catalogMessageHost, "/api/installer/network"); got != "200" {
		t.Fatalf("KyMessages readiness through the edge: HTTP %s", got)
	}
	var secrets []string
	for _, f := range []string{d + "/client.secret", d + "/directory.secret"} {
		b, err := c.Exec(ctx, ns, pod, container, []string{"cat", f})
		if err != nil || len(strings.TrimSpace(string(b))) < 16 {
			t.Fatalf("%s: %v", f, err)
		}
		secrets = append(secrets, strings.TrimSpace(string(b)))
	}
	logs, err := cs.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Container: container, Previous: true}).DoRaw(ctx)
	m := bootstrapPassword.FindSubmatch(logs)
	if err != nil || m == nil {
		t.Fatalf("no break-glass password in the first container's log: %v", err)
	}
	secrets = append(secrets, string(m[1]))

	// The handover's init-admin, run by the kind node's own kubectl (this host has none) with
	// its admin kubeconfig in place of the handover's: the password goes on stdin only.
	disposable := rand.Text()
	cmd := exec.Command("docker", append([]string{"exec", "-i", node, "kubectl", "--kubeconfig", "/etc/kubernetes/admin.conf", "-n", "kyq-kymessage"}, strings.Fields(initAdmin)...)...)
	cmd.Stdin = strings.NewReader(disposable + "\n")
	reset, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(reset, []byte("password successfully reset")) || bytes.Contains(reset, []byte(disposable)) {
		t.Fatalf("init-admin: %v\n%s", err, reset)
	}
	return secrets
}

// messageSignOn signs the owner's accounts in to KyMessages and proves its admin separation:
// only the admin reads the audit log.
func messageSignOn(t *testing.T, ctx context.Context, c *kube.Client, certPEM []byte, nodeIP string) {
	t.Helper()
	ownerSignOn(t, ctx, c, certPEM, nodeIP, catalogMessageURLHost, "kymessage", "/api/sso/kyidentity", func(username string, admin bool, get func(string) (*http.Response, []byte)) {
		_, b := get("/api/auth/me")
		var me struct {
			Authenticated bool
			User          struct{ Role string }
		}
		role := map[bool]string{true: "admin", false: "user"}[admin]
		if json.Unmarshal(b, &me) != nil || !me.Authenticated || me.User.Role != role {
			t.Fatalf("%s: /api/auth/me %s", username, b)
		}
		audit, b := get("/api/admin/audit")
		if admin && audit.StatusCode != 200 || !admin && audit.StatusCode != 403 {
			t.Fatalf("%s: admin/audit %d %s", username, audit.StatusCode, b)
		}
		t.Logf("%s signed in to KyMessages through KyIdentity: role %s, admin/audit %d", username, me.User.Role, audit.StatusCode)
	})
}
