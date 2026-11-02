package kube

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
	// The app runs on node n1; the storage check starts there before the app stops.
	if _, err := cs.CoreV1().Pods("kyq-hello").Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "kyq-hello", Labels: podLabels("hello", "rs")}, Spec: corev1.PodSpec{NodeName: "n1"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	var checked []string // per pod: its node affinity, and whether the app was running then
	cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pod = action.(k8stesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
		node := ""
		if aff := pod.Spec.Affinity; aff != nil {
			node = aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields[0].Values[0]
		}
		// Inside a reactor the fake's lock is held: read through its tracker.
		o, err := cs.Tracker().Get(appsv1.SchemeGroupVersion.WithResource("deployments"), "kyq-hello", appName)
		if err != nil {
			return true, nil, err
		}
		checked = append(checked, fmt.Sprintf("%s %v", node, replicas(o.(*appsv1.Deployment)) == 1))
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
	if !slices.Equal(checked, []string{"n1 true", " false"}) {
		t.Errorf("pods (node, app running) = %v", checked)
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
	if list, _ := cs.CoreV1().Pods("kyq-hello").List(ctx, metav1.ListOptions{}); len(list.Items) != 1 {
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

// Storage that cannot provide the copies fails the check before the app stops, naming the
// cluster's warnings and the earlier copies taking space (#149).
func TestPreUpgradeCopyStorageRefused(t *testing.T) {
	a, cs, calls := copyFixture(t, func([]string) ([]byte, error) { return nil, nil })
	ctx := context.Background()
	older := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: PreUpgradeClaim("1.9.0", "data"), Namespace: "kyq-hello", Labels: workloadLabels("rs")},
		Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")}}}}
	if _, err := cs.CoreV1().PersistentVolumeClaims("kyq-hello").Create(ctx, older, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pod := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
		pod.Name, pod.UID, pod.Status.Phase = pod.GenerateName+"x", "uid-x", corev1.PodFailed
		ev := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "e1", Namespace: "kyq-hello"}, Type: corev1.EventTypeWarning, Reason: "FailedAttachVolume",
			Message: "volume is not ready for workloads:\ninsufficient storage", LastTimestamp: metav1.Now(),
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: pod.Name}}
		if err := cs.Tracker().Create(corev1.SchemeGroupVersion.WithResource("events"), ev, "kyq-hello"); err != nil {
			return true, nil, err
		}
		return true, pod, cs.Tracker().Create(corev1.SchemeGroupVersion.WithResource("pods"), pod, pod.Namespace)
	})
	stopped := false
	cs.PrependReactor("patch", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		stopped = true
		return false, nil, nil
	})
	_, err := PreUpgradeCopy(ctx, a, "1.10.0")
	for _, want := range []string{"did not provide the pre-upgrade copies pre-upgrade-1.10.0-vol-data, pre-upgrade-1.10.0-vol-cache",
		"hello was not stopped", "FailedAttachVolume: volume is not ready for workloads: insufficient storage",
		"1 earlier copies take space: pre-upgrade-1.9.0-vol-data (2Gi)", "kyquickstart prune-copies hello"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %q", err, want)
		}
	}
	if err != nil && strings.Contains(err.Error(), "\n") {
		t.Error("the refusal spans lines")
	}
	if stopped || len(*calls) != 0 || replicas(deployment(t, a)) != 1 {
		t.Errorf("stopped %v, %d execs", stopped, len(*calls))
	}
}

// Only the installer's pre-upgrade claims are listed or deleted, never an app's own.
func TestPreUpgradeCopies(t *testing.T) {
	a, cs, _ := copyFixture(t, func([]string) ([]byte, error) { return nil, nil })
	ctx := context.Background()
	if _, err := PreUpgradeCopy(ctx, a, "1.10.0"); err != nil {
		t.Fatal(err)
	}
	foreign := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: PreUpgradeClaim("1.8.0", "data"), Namespace: "kyq-hello"}}
	if _, err := cs.CoreV1().PersistentVolumeClaims("kyq-hello").Create(ctx, foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	copies, err := PreUpgradeCopies(ctx, a)
	if err != nil || len(copies) != 2 || copies[0] != (Copy{Claim: "pre-upgrade-1.10.0-vol-cache", From: "1.10.0", Size: "8Mi", Verified: true}) || copies[1].Claim != "pre-upgrade-1.10.0-vol-data" {
		t.Fatalf("copies = %+v, %v", copies, err)
	}
	for _, name := range []string{"vol-data", "pre-upgrade-1.10.0-vol-data-x/y", PreUpgradeClaim("1.8.0", "data")} {
		if err := DeletePreUpgradeCopy(ctx, a, name); err == nil {
			t.Errorf("deleted %s", name)
		}
	}
	claim(t, cs, "vol-data")
	claim(t, cs, PreUpgradeClaim("1.8.0", "data"))
	if err := DeletePreUpgradeCopy(ctx, a, "pre-upgrade-1.10.0-vol-data"); err != nil {
		t.Fatal(err)
	}
	if copies, _ := PreUpgradeCopies(ctx, a); len(copies) != 1 {
		t.Errorf("after delete: %+v", copies)
	}
}
