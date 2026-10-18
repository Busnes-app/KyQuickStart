//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// catalogPulseHost is where TestKubernetesNPMEdge routes the catalog kyPulse.
const catalogPulseHost = "pulse.example.test"

// catalogPulseURLHost is its URL host behind the lan edge.
const catalogPulseURLHost = catalogPulseHost + ":30443"

const pulseSetupDir = "/app/data/installer/3d8f1a6b-7c2e-4b9d-a051-6e4f2c8b9a17"

// pulseWatched are the routed apps kyPulse watches in that test: all but NPM and itself.
var pulseWatched = []string{"hello", "kyidentity", "kyrecovery"}

// addCatalogPulse installs the catalog kyPulse behind the edge and selects its setup; apply
// adds its client to the catalog KyIdentity's first setup beside KyRecovery's.
func addCatalogPulse(st *stack.Stack, certID int) {
	st.Apps = append(st.Apps, stack.App{Name: "kypulse", Target: "k8s"})
	st.Edges[0].Routes = append(st.Edges[0].Routes, stack.Route{App: "kypulse", Hostname: catalogPulseHost,
		ForwardHost: "app.kyq-kypulse.svc.cluster.local", ForwardPort: 8080, CertificateID: certID})
	st.Integrations = append(st.Integrations, stack.Integration{Product: "kypulse", Target: "k8s",
		Bundle: pulseSetupDir + "/setup.json", IdentityClient: "kypulse"})
}

// checkCatalogPulse proves the deployed catalog kyPulse: restricted pod as its image's user
// on private data and secrets directories, setup applied from private files with a target per
// routed app, and health through the edge. It returns the transferred secrets.
func checkCatalogPulse(t *testing.T, ctx context.Context, cs kubernetes.Interface, kubeconfig, out string, get func(host, path string) string) []string {
	t.Helper()
	for _, id := range []string{"kypulse.deploy", "kypulse.health", "kypulse.setup"} {
		if status(out, id) != "ok" {
			t.Fatalf("%s = %q", id, status(out, id))
		}
	}
	if !strings.Contains(out, "kyPulse at https://"+catalogPulseURLHost+"/ (sign in through KyIdentity as prepared-admin) watches: "+strings.Join(pulseWatched, ", ")+".") || !strings.Contains(out, "Not watched (no readiness path or single edge route): npm, kydrive.") {
		t.Fatal("kyPulse handover missing")
	}
	c, err := kube.Connect(kubeconfig, "")
	if err != nil {
		t.Fatal(err)
	}
	namespace, pod, container, err := c.AppPod(ctx, "kypulse")
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
	stat, err := c.Exec(ctx, namespace, pod, container, []string{"stat", "-c", "%n %a %u", "/app/data", "/app/secrets", "/app/secrets/audit.key", pulseSetupDir, pulseSetupDir + "/client.secret", pulseSetupDir + "/directory.secret", pulseSetupDir + "/setup.json"})
	want := "/app/data 700 10001\n/app/secrets 700 10001\n/app/secrets/audit.key 600 10001\n" + pulseSetupDir + " 700 10001\n" + pulseSetupDir + "/client.secret 600 10001\n" + pulseSetupDir + "/directory.secret 600 10001\n" + pulseSetupDir + "/setup.json 600 10001"
	if err != nil || strings.TrimSpace(string(stat)) != want {
		t.Fatalf("setup files:\n%s %v", stat, err)
	}
	// The product's own read-only inspection of the live server agrees, target by target.
	report, err := c.Exec(ctx, namespace, pod, container, []string{"/app/kypulse", "apply-setup", "--file", pulseSetupDir + "/setup.json", "--check"})
	var r struct {
		Created, Pending []json.RawMessage
		Present          []struct{ Kind, Name, ID string }
	}
	if err != nil || json.Unmarshal(report, &r) != nil || len(r.Created) != 0 || len(r.Pending) != 0 {
		t.Fatalf("kyPulse check:\n%s %v", report, err)
	}
	var targets []string
	for _, o := range r.Present {
		if name, ok := strings.CutPrefix(o.Name, "monitoring.target."); ok && o.ID != "" {
			targets = append(targets, name)
		}
	}
	if slices.Sort(targets); !slices.Equal(targets, pulseWatched) {
		t.Fatalf("kyPulse targets = %v:\n%s", targets, report)
	}
	var health struct{ Service, Status string }
	if body := get(catalogPulseHost, "/healthz"); json.Unmarshal([]byte(body), &health) != nil || health.Service != "kypulse" || health.Status != "ok" {
		t.Fatalf("health through the edge:\n%s", body)
	}
	var secrets []string
	for _, f := range []string{"client.secret", "directory.secret"} {
		b, err := c.Exec(ctx, namespace, pod, container, []string{"cat", pulseSetupDir + "/" + f})
		if err != nil || len(b) < 32 {
			t.Fatalf("%s: %v", f, err)
		}
		secrets = append(secrets, string(b))
	}
	return secrets
}

// pulseTargetsHealthy waits, as kyPulse's signed-in admin, until every target apply registered
// reports ok: kyPulse polled each app through the edge.
func pulseTargetsHealthy(t *testing.T, ctx context.Context, send func(method, endpoint string, body []byte, csrf string) (*http.Response, []byte), origin string) {
	t.Helper()
	var last []byte
	for deadline := time.Now().Add(4 * time.Minute); time.Now().Before(deadline); time.Sleep(5 * time.Second) {
		r, b := send("GET", origin+"/api/targets", nil, "")
		var list struct {
			Targets []struct{ Name, URL, State string }
		}
		if r.StatusCode != 200 || json.Unmarshal(b, &list) != nil {
			t.Fatalf("kyPulse targets: %d %s", r.StatusCode, b)
		}
		last = b
		var ok []string
		for _, x := range list.Targets {
			if x.State == "ok" {
				ok = append(ok, x.Name)
			}
		}
		if slices.Sort(ok); slices.Equal(ok, pulseWatched) && len(list.Targets) == len(pulseWatched) {
			t.Logf("kyPulse reports every registered target ok: %v", ok)
			return
		}
	}
	t.Fatalf("kyPulse targets not healthy:\n%s", last)
}
