//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

// This fixture uses the rebuilt product binary and native apply-setup only. The init
// container prepares an owned disposable data directory; no live installation is used.
func testIdentityKubernetesPreparation(t *testing.T, ctx context.Context, cs kubernetes.Interface, cluster, kubeconfig string) {
	binary := os.Getenv("KYQ_TEST_IDENTITY_BINARY")
	if binary == "" {
		return
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("KYQ_TEST_IDENTITY_BINARY must be absolute")
	}
	build := t.TempDir()
	in, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.OpenFile(filepath.Join(build, "kyidentity"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if err != nil {
		in.Close()
		t.Fatal(err)
	}
	_, err = io.Copy(out, in)
	in.Close()
	closeErr := out.Close()
	if err != nil || closeErr != nil {
		t.Fatal("copy native fixture binary", err, closeErr)
	}
	image := "kyq-identity-kube-fixture:" + cluster
	if err := os.WriteFile(filepath.Join(build, "Dockerfile"), []byte("FROM gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a\nCOPY --chmod=0755 kyidentity /usr/local/bin/kyidentity\nENTRYPOINT [\"/usr/local/bin/kyidentity\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	docker(t, "build", "-q", "-t", image, build)
	t.Cleanup(func() { docker(t, "image", "rm", image) })
	kind(t, "load", "docker-image", image, "--name", cluster)
	labels := map[string]string{managed.Label: managed.Value, "pod-security.kubernetes.io/enforce": "restricted"}
	const namespace = "kyq-identity-fixture"
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: labels}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{}) })
	if _, err := cs.NetworkingV1().NetworkPolicies(namespace).Create(ctx, &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "default-deny", Labels: map[string]string{managed.Label: managed.Value}}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	security := &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
	mounts := []corev1.VolumeMount{{Name: "data", MountPath: "/work"}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "identity", Labels: map[string]string{managed.Label: managed.Value}}, Spec: corev1.PodSpec{
		AutomountServiceAccountToken: ptr.To(false), EnableServiceLinks: ptr.To(false),
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(65532)), RunAsGroup: ptr.To(int64(65532)), FSGroup: ptr.To(int64(65532)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},

		InitContainers: []corev1.Container{{Name: "fixture-data", Image: kube.CredentialGeneratorImage, SecurityContext: security, VolumeMounts: mounts, Command: []string{"sh", "-c", "umask 077; mkdir /work/product"}, Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi"), corev1.ResourceCPU: resource.MustParse("100m")}}}},
		Containers:     []corev1.Container{{Name: "identity", Image: image, ImagePullPolicy: corev1.PullNever, SecurityContext: security, VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data", SubPath: "product"}}, Env: []corev1.EnvVar{{Name: "KYIDENTITY_DATA_DIR", Value: "/data"}, {Name: "KYIDENTITY_ISSUER_URL", Value: "https://identity.example.test"}, {Name: "GOMAXPROCS", Value: "2"}, {Name: "GOMEMLIMIT", Value: "256MiB"}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt32(5867)}}, PeriodSeconds: 1}, Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("384Mi"), corev1.ResourceCPU: resource.MustParse("1")}}}},
		Volumes:        []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr.To(resource.MustParse("64Mi"))}}}},
	}}
	if _, err := cs.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		observed, err := cs.CoreV1().Pods(namespace).Get(ctx, "identity", metav1.GetOptions{})
		ready := false
		if err == nil {
			for _, condition := range observed.Status.Conditions {
				ready = ready || condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disposable native Identity Kubernetes fixture did not become ready")
		}
		time.Sleep(500 * time.Millisecond)
	}

	n := identityFixturePreparation()
	st := &stack.Stack{Version: 1, Targets: []stack.Target{{Name: "k8s", Kubeconfig: kubeconfig}}, Existing: []stack.Existing{{Product: "kyidentity", Target: "k8s", URL: "https://identity.example.test"}}, Integrations: []stack.Integration{{Product: "kyidentity", Target: "k8s", Namespace: namespace, Pod: "identity", Container: "identity", Bundle: "/data/installer/" + n.SetupID + "/setup.json", Identity: n}}}
	runPreparedIdentityStack(t, ctx, st, nil)
}

// catalogIdentityHost is where TestKubernetesNPMEdge routes the catalog KyIdentity.
const catalogIdentityHost = "identity.example.test"

// catalogIdentityURLHost is its URL host: the lan edge's clients reach NPM on NodePort 30443.
const catalogIdentityURLHost = catalogIdentityHost + ":30443"

// loadCatalogApp pulls a catalog app's attested image and loads it into kind.
func loadCatalogApp(t *testing.T, cluster string, cs kubernetes.Interface, app string) {
	t.Helper()
	logAppOnFailure(t, cs, app)
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	ref := cat[app].Images[cat[app].Kubernetes.Image]
	docker(t, "pull", "-q", ref)
	// kind load of a digest reference leaves a broken image record; load the tag. The pod
	// still names the digest, which containerd checks.
	tag, _, _ := strings.Cut(ref, "@")
	docker(t, "tag", ref, tag)
	kind(t, "load", "docker-image", tag, "--name", cluster)
}

// logAppOnFailure logs app's pods and events if the test fails, before the cluster goes.
func logAppOnFailure(t *testing.T, cs kubernetes.Interface, app string) {
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ctx := context.Background()
		ns := "kyq-" + app
		pods, _ := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		for _, p := range pods.Items {
			t.Logf("pod %s: phase %s, init %+v, containers %+v", p.Name, p.Status.Phase, p.Status.InitContainerStatuses, p.Status.ContainerStatuses)
			for _, c := range []string{"private-volumes", "app"} {
				logs, _ := cs.CoreV1().Pods(ns).GetLogs(p.Name, &corev1.PodLogOptions{Container: c}).DoRaw(ctx)
				t.Logf("%s/%s logs:\n%s", p.Name, c, logs)
			}
		}
		events, _ := cs.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
		for _, e := range events.Items {
			t.Logf("event %s %s: %s", e.InvolvedObject.Name, e.Reason, e.Message)
		}
	})
}

// addCatalogIdentity installs the catalog KyIdentity behind the edge and selects its setup.
func addCatalogIdentity(st *stack.Stack, certID int) {
	n := identityFixturePreparation()
	st.Apps = append(st.Apps, stack.App{Name: "kyidentity", Target: "k8s"})
	st.Edges[0].Routes = append(st.Edges[0].Routes, stack.Route{App: "kyidentity", Hostname: catalogIdentityHost,
		ForwardHost: "app.kyq-kyidentity.svc.cluster.local", ForwardPort: 5867, CertificateID: certID})
	st.Integrations = append(st.Integrations, stack.Integration{Product: "kyidentity", Target: "k8s",
		Bundle: "/data/installer/" + n.SetupID + "/setup.json", Identity: n})
}

// checkCatalogIdentity proves the deployed catalog KyIdentity: restricted pod as uid 100 on
// a private data directory, setup applied, its first-start administrator retired (disabled,
// password file gone), and discovery through the edge naming the route's issuer.
func checkCatalogIdentity(t *testing.T, ctx context.Context, cs kubernetes.Interface, kubeconfig, out string, get func(host, path string) string) {
	t.Helper()
	for _, id := range []string{"kyidentity.deploy", "kyidentity.health", "kyidentity.setup", "kyidentity.retire-bootstrap"} {
		if status(out, id) != "ok" {
			t.Fatalf("%s = %q", id, status(out, id))
		}
	}
	ns, err := cs.CoreV1().Namespaces().Get(ctx, "kyq-kyidentity", metav1.GetOptions{})
	if err != nil || ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Fatalf("namespace = %v, %v", ns.Labels, err)
	}
	c, err := kube.Connect(kubeconfig, "")
	if err != nil {
		t.Fatal(err)
	}
	namespace, pod, container, err := c.AppPod(ctx, "kyidentity")
	if err != nil {
		t.Fatal(err)
	}
	p, err := cs.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil || *p.Spec.Containers[0].SecurityContext.RunAsUser != 100 || p.Spec.InitContainers[0].Name != "private-volumes" {
		t.Fatalf("pod spec: %v", err)
	}
	stat, err := c.Exec(ctx, namespace, pod, container, []string{"stat", "-c", "%n %a %u", "/data", "/data/installer"})
	if err != nil || strings.TrimSpace(string(stat)) != "/data 700 100\n/data/installer 700 100" {
		t.Fatalf("data directory:\n%s %v", stat, err)
	}
	// disable-user removes the password file naming the account it retires.
	if _, err := c.Exec(ctx, namespace, pod, container, []string{"test", "!", "-e", "/data/first-run-password.txt"}); err != nil {
		t.Fatal("kyq-bootstrap's first-run password file survived its retirement")
	}
	// A rerun answers unchanged: the account is already disabled.
	if b, err := c.Exec(ctx, namespace, pod, container, []string{"/usr/local/bin/kyidentity", "disable-user", "--username", "kyq-bootstrap", "--require-active-admin", identityFixturePreparation().Admin.Username}); err != nil || !strings.Contains(string(b), `"status":"unchanged"`) {
		t.Fatalf("kyq-bootstrap is not disabled: %s %v", b, err)
	}
	events, _ := cs.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=" + pod})
	for _, e := range events.Items {
		if e.Reason == "Pulling" || e.Reason == "Pulled" {
			t.Logf("image: %s", e.Message)
		}
	}
	var discovery struct {
		Issuer string `json:"issuer"`
	}
	body := get(catalogIdentityHost, "/.well-known/openid-configuration")
	if json.Unmarshal([]byte(body), &discovery) != nil || discovery.Issuer != "https://"+catalogIdentityURLHost {
		t.Fatalf("discovery through the edge:\n%s", body)
	}
}
