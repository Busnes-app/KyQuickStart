//go:build e2e

package e2e

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

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
