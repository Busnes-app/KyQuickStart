package kube

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// copyFixture is hello deployed with two volumes, its Deployment's status following its
// replicas as a controller's would, and every exec recorded and answered by run.
func copyFixture(t *testing.T, run func(args []string) ([]byte, error)) (App, *fake.Clientset, *[][]string) {
	t.Helper()
	pollInterval = time.Millisecond
	a, cs := testApp(t, "token")
	extra := a.Catalog.Kubernetes.Volumes[0]
	extra.Name, extra.Size = "cache", "8Mi"
	a.Catalog.Kubernetes.Volumes = append(a.Catalog.Kubernetes.Volumes, extra)
	for _, s := range Steps(a)[:2] {
		if err := s.Apply(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	cs.PrependReactor("get", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		o, err := cs.Tracker().Get(appsv1.SchemeGroupVersion.WithResource("deployments"), action.GetNamespace(), action.(k8stesting.GetAction).GetName())
		if err != nil {
			return true, nil, err
		}
		d := o.(*appsv1.Deployment).DeepCopy()
		d.Status = appsv1.DeploymentStatus{}
		if replicas(d) == 1 {
			d.Status = readyStatus()
		}
		return true, d, nil
	})
	var calls [][]string
	a.Client.execCommand = func(_ context.Context, ns, pod, container string, args []string) ([]byte, error) {
		if ns != "kyq-hello" || !strings.HasPrefix(pod, "kyq-pre-upgrade-") || container != "copy" {
			return nil, errors.New("exec outside the copy pod")
		}
		calls = append(calls, args)
		return run(args)
	}
	return a, cs, &calls
}

func claim(t *testing.T, cs *fake.Clientset, name string) *corev1.PersistentVolumeClaim {
	t.Helper()
	c, err := cs.CoreV1().PersistentVolumeClaims("kyq-hello").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("claim %s: %v", name, err)
	}
	return c
}

func TestPreUpgradeCopy(t *testing.T) {
	a, cs, calls := copyFixture(t, func([]string) ([]byte, error) { return nil, nil })
	var pod *corev1.Pod
	cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pod = action.(k8stesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
		return false, nil, nil // the fixture's reactor creates it
	})
	ctx := context.Background()
	claims, err := PreUpgradeCopy(ctx, a, "1.10.0")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(claims, []string{"pre-upgrade-1.10.0-vol-data", "pre-upgrade-1.10.0-vol-cache"}) {
		t.Fatalf("claims = %v", claims)
	}
	for _, v := range []string{"data", "cache"} {
		c, src := claim(t, cs, PreUpgradeClaim("1.10.0", v)), claim(t, cs, "vol-"+v)
		if c.Annotations[copiedAnnotation] != "verified" || !owned(c) || !c.Spec.Resources.Requests.Storage().Equal(*src.Spec.Resources.Requests.Storage()) {
			t.Errorf("%s: %+v", c.Name, c.ObjectMeta)
		}
	}
	if len(*calls) != 1 || !slices.Equal((*calls)[0][3:], []string{"copy", "data", "cache"}) || (*calls)[0][2] != copyCommand {
		t.Fatalf("exec = %v", *calls)
	}
	// The app's uid, restricted, no token, the originals read-only through their private directory.
	c := pod.Spec.Containers[0]
	if *pod.Spec.AutomountServiceAccountToken || *c.SecurityContext.RunAsUser != 65532 || !*c.SecurityContext.RunAsNonRoot || *c.SecurityContext.AllowPrivilegeEscalation || !*c.SecurityContext.ReadOnlyRootFilesystem || c.Image != CredentialGeneratorImage {
		t.Errorf("pod security: %+v", c.SecurityContext)
	}
	for _, m := range c.VolumeMounts {
		if strings.HasPrefix(m.Name, "src-") && (!m.ReadOnly || m.SubPath != privateDir) {
			t.Errorf("source mount %+v", m)
		}
	}
	if list, _ := cs.CoreV1().Pods("kyq-hello").List(ctx, metav1.ListOptions{}); len(list.Items) != 0 {
		t.Error("the copy pod was left behind")
	}
	if replicas(deployment(t, a)) != 1 {
		t.Error("the app was not started again")
	}

	// A rerun keeps verified copies and neither stops the app nor copies again.
	stopped := false
	cs.PrependReactor("patch", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		stopped = true
		return false, nil, nil
	})
	if _, err := PreUpgradeCopy(ctx, a, "1.10.0"); err != nil || len(*calls) != 1 || stopped {
		t.Fatalf("rerun: %v, %d execs, stopped %v", err, len(*calls), stopped)
	}
}

func TestPreUpgradeCopyFailure(t *testing.T) {
	a, cs, calls := copyFixture(t, func([]string) ([]byte, error) {
		return []byte("cache: copied modes, owners or times differ from the original\n"), errors.New("exit 1")
	})
	ctx := context.Background()
	_, err := PreUpgradeCopy(ctx, a, "1.10.0")
	if err == nil || !strings.Contains(err.Error(), "pre-upgrade copy failed: cache: copied modes, owners or times differ") {
		t.Fatalf("err = %v", err)
	}
	if replicas(deployment(t, a)) != 1 {
		t.Error("a failed copy left the app stopped")
	}
	if c := claim(t, cs, PreUpgradeClaim("1.10.0", "data")); c.Annotations[copiedAnnotation] != "" {
		t.Error("an unverified copy was marked verified")
	}

	// Once data alone is verified, the next run copies only cache.
	b := []byte(`{"metadata":{"annotations":{"` + copiedAnnotation + `":"verified"}}}`)
	if _, err := cs.CoreV1().PersistentVolumeClaims("kyq-hello").Patch(ctx, PreUpgradeClaim("1.10.0", "data"), "application/merge-patch+json", b, metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	a.Client.execCommand = func(_ context.Context, _, _, _ string, args []string) ([]byte, error) {
		*calls = append(*calls, args)
		return nil, nil
	}
	if _, err := PreUpgradeCopy(ctx, a, "1.10.0"); err != nil || !slices.Equal((*calls)[len(*calls)-1][3:], []string{"copy", "cache"}) {
		t.Fatalf("partial rerun: %v %v", err, *calls)
	}
}

func TestPreUpgradeRefusal(t *testing.T) {
	a, _ := testApp(t)
	if err := PreUpgradeRefusal(a); err != nil {
		t.Fatal(err)
	}
	root := a
	k := *a.Catalog.Kubernetes
	k.RunAs = 0
	root.Catalog.Kubernetes = &k
	if err := PreUpgradeRefusal(root); err == nil || !strings.Contains(err.Error(), "runs as root") {
		t.Errorf("root: %v", err)
	}
	none := a
	k2 := *a.Catalog.Kubernetes
	k2.Volumes = nil
	none.Catalog.Kubernetes = &k2
	if err := PreUpgradeRefusal(none); err == nil || !strings.Contains(err.Error(), "keeps no volumes") {
		t.Errorf("no volumes: %v", err)
	}
}
