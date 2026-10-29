//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
)

// TestKubernetesPreUpgradeCopy upgrades hello in kind to a version that migrates data: the
// upgrade copies its volume into a verified claim under the namespace's restricted pod
// security, then deploys, and the copy holds what the app's uid wrote.
func TestKubernetesPreUpgradeCopy(t *testing.T) {
	ctx := context.Background()
	work := t.TempDir()
	state := filepath.Join(work, "state")
	os.MkdirAll(state, 0o700)
	_, kc := kindCluster(t, work)
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	cs := kubernetes.NewForConfigOrDie(cfg)
	catalogDir := filepath.Join(work, "catalog")
	if err := os.CopyFS(catalogDir, os.DirFS("../../internal/catalog/testdata/apps")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(state, "stack.yaml"), []byte("version: 1\ninstallation_id: "+kubeInstallation+"\ntargets:\n  - name: k8s\n    kubeconfig: "+kc+"\napps:\n  - name: hello\n    target: k8s\n"), 0o600)
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		o := cli.Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "e2e", Out: &out}
		err := cli.Run(ctx, append(args, "--state", state), o)
		t.Logf("%s:\n%s", args[0], out.String())
		return out.String(), err
	}
	if _, err := run("apply"); err != nil {
		t.Fatal(err)
	}

	// What the app's uid keeps in its volume, as the app sees it.
	if out := podRun(t, ctx, cs, "vol-data", "mkdir -p sub && echo kept > sub/file && chmod 600 sub/file && ln -sf sub/file link && ls -l sub/file"); !strings.Contains(out, "-rw-------") {
		t.Fatalf("writer: %s", out)
	}

	manifest := filepath.Join(catalogDir, "hello", "manifest.yaml")
	b, _ := os.ReadFile(manifest)
	os.WriteFile(manifest, bytes.Replace(b, []byte("version: 1.11.0\n"), []byte("version: 1.12.0\nupgrade:\n  migrates_data: true\n"), 1), 0o600)
	out, err := run("upgrade")
	if err != nil || status(out, "hello.pre-upgrade-backup") != "ok" || status(out, "hello.deploy") != "ok" || status(out, "hello.health") != "ok" {
		t.Fatalf("upgrade: %v", err)
	}
	claim := kube.PreUpgradeClaim("1.11.0", "data")
	c, err := cs.CoreV1().PersistentVolumeClaims("kyq-hello").Get(ctx, claim, metav1.GetOptions{})
	if err != nil || c.Annotations["kyquickstart/pre-upgrade"] != "verified" {
		t.Fatalf("claim %s: %+v %v", claim, c, err)
	}
	if out := podRun(t, ctx, cs, claim, "cat sub/file && ls -l sub/file && readlink link"); !strings.Contains(out, "kept\n") || !strings.Contains(out, "-rw-------") || !strings.Contains(out, "sub/file") {
		t.Fatalf("copy holds: %s", out)
	}
	if pods, _ := cs.CoreV1().Pods("kyq-hello").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name!=hello"}); len(pods.Items) != 0 {
		t.Errorf("left behind: %d pods", len(pods.Items))
	}
	if out, err := run("upgrade"); err != nil || status(out, "hello.deploy") != "skipped" {
		t.Fatalf("second upgrade: %v", err)
	}
}

// podRun runs script as hello's uid, restricted, in claim's private directory, and returns
// its output.
func podRun(t *testing.T, ctx context.Context, cs kubernetes.Interface, claim, script string) string {
	t.Helper()
	pods := cs.CoreV1().Pods("kyq-hello")
	pod, err := pods.Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-volume-"},
		Spec: corev1.PodSpec{
			RestartPolicy:   corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(65532)), RunAsGroup: ptr.To(int64(65532)), FSGroup: ptr.To(int64(65532)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{Name: "c", Image: kube.CredentialGeneratorImage, Command: []string{"/bin/sh", "-ec", "cd /v && " + script},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "v", MountPath: "/v", SubPath: "private"}},
			}},
			Volumes: []corev1.Volume{{Name: "v", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer pods.Delete(context.WithoutCancel(ctx), pod.Name, metav1.DeleteOptions{GracePeriodSeconds: ptr.To(int64(0))})
	deadline := time.Now().Add(2 * time.Minute)
	for {
		p, err := pods.Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			logs, _ := pods.GetLogs(pod.Name, &corev1.PodLogOptions{}).DoRaw(ctx)
			if p.Status.Phase == corev1.PodFailed {
				t.Fatalf("pod on %s failed: %s", claim, logs)
			}
			return string(logs)
		}
		if time.Now().After(deadline) {
			t.Fatalf("pod on %s: %s", claim, p.Status.Phase)
		}
		time.Sleep(time.Second)
	}
}
