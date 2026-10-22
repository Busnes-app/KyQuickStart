package kube

import (
	"context"
	"strings"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func cluster(minor string, deny string) *fake.Clientset {
	cs := fake.NewClientset()
	cs.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{Major: "1", Minor: minor}
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		r := a.(ktesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		attrs := r.Spec.ResourceAttributes
		r.Status.Allowed = attrs.Verb+" "+attrs.Resource != deny
		return true, r, nil
	})
	return cs
}

func find(fs []Finding, check string) Finding {
	for _, f := range fs {
		if f.Check == check {
			return f
		}
	}
	return Finding{Check: "absent"}
}

func TestPreflight(t *testing.T) {
	ctx := context.Background()
	ok := cluster("37+", "")
	ok.StorageV1().StorageClasses().Create(ctx, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{
		Name: "standard", Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"},
	}}, metav1.CreateOptions{})
	for _, f := range Preflight(ctx, New(ok), true, "", nil) {
		if !f.OK {
			t.Errorf("healthy cluster failed %+v", f)
		}
	}
	if f := find(Preflight(ctx, New(cluster("29", "")), false, "", nil), "kubernetes"); f.OK {
		t.Errorf("1.29 accepted: %+v", f)
	}
	if f := find(Preflight(ctx, New(cluster("37", "create deployments")), false, "", nil), "permissions"); f.OK || !strings.Contains(f.Detail, "create deployments") {
		t.Errorf("denied permission not reported: %+v", f)
	}
	if f := find(Preflight(ctx, New(cluster("37", "list storageclasses")), false, "", nil), "permissions"); f.OK {
		t.Errorf("denied storage class listing not reported: %+v", f)
	}
	if f := find(Preflight(ctx, New(cluster("37", "")), true, "", nil), "storage"); f.OK {
		t.Errorf("missing default StorageClass accepted: %+v", f)
	}
	if f := find(Preflight(ctx, New(cluster("37", "")), false, "", nil), "storage"); f.Check != "absent" {
		t.Errorf("storage checked when no app needs it: %+v", f)
	}
}

// A lock namespace someone else created fails preflight, as it would fail apply's lock.
func TestPreflightReservedLockNamespace(t *testing.T) {
	ctx := context.Background()
	cs := cluster("37", "")
	cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: lockNamespace}}, metav1.CreateOptions{})
	if f := find(Preflight(ctx, New(cs), false, "", nil), "lock"); f.OK || !strings.Contains(f.Detail, "not managed by kyquickstart") {
		t.Fatalf("foreign lock namespace: %+v", f)
	}
	ns, _ := cs.CoreV1().Namespaces().Get(ctx, lockNamespace, metav1.GetOptions{})
	ns.Labels = map[string]string{"ky.managed-by": "kyquickstart"}
	cs.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{})
	if f := find(Preflight(ctx, New(cs), false, "", nil), "lock"); !f.OK {
		t.Fatalf("own lock namespace: %+v", f)
	}
}

func TestPreflightRequiresCredentialPodAndExecPermissions(t *testing.T) {
	for _, permission := range []string{"create pods", "get pods", "delete pods", "create pods/exec"} {
		if f := find(Preflight(context.Background(), New(cluster("37", permission)), false, "", nil), "permissions"); f.OK || !strings.Contains(f.Detail, permission) {
			t.Fatal("missing credential generation permission not reported", permission, f)
		}
	}
}

func TestPreflightNamedStorageClass(t *testing.T) {
	ctx := context.Background()
	cs := cluster("37", "")
	cs.StorageV1().StorageClasses().Create(ctx, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "local-path"}}, metav1.CreateOptions{})
	// No default class exists: a named class that exists is enough.
	if f := find(Preflight(ctx, New(cs), true, "local-path", nil), "storage"); !f.OK || !strings.Contains(f.Detail, "local-path") {
		t.Errorf("named present class refused: %+v", f)
	}
	cs.StorageV1().StorageClasses().Create(ctx, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{
		Name: "standard", Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"},
	}}, metav1.CreateOptions{})
	// A default does not stand in for a missing named class.
	if f := find(Preflight(ctx, New(cs), true, "fast", nil), "storage"); f.OK || !strings.Contains(f.Detail, "fast") {
		t.Errorf("named missing class accepted: %+v", f)
	}
}

func TestPreflightNodePorts(t *testing.T) {
	ctx := context.Background()
	want := map[int]string{30080: "npm", 30443: "npm"}
	svc := func(ns, name string, managedBy bool, port int32) *corev1.Service {
		s := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeNodePort, Ports: []corev1.ServicePort{{Port: 80, NodePort: port}}}}
		if managedBy {
			s.Labels = workloadLabels("rs")
		}
		return s
	}
	for name, c := range map[string]struct {
		svc  *corev1.Service
		held string // "" when the check passes
	}{
		"free":               {svc("other", "web", false, 31000), ""},
		"our own, on rerun":  {svc("kyq-npm", publicName, true, 30080), ""},
		"foreign":            {svc("other", "web", false, 30080), "30080 (for npm) is held by Service other/web"},
		"another app's":      {svc("kyq-frpc", publicName, true, 30443), "30443 (for npm) is held by Service kyq-frpc/app-public"},
		"unlabelled, our ns": {svc("kyq-npm", publicName, false, 30080), "Service kyq-npm/app-public"},
	} {
		cs := cluster("37", "")
		cs.CoreV1().Services(c.svc.Namespace).Create(ctx, c.svc, metav1.CreateOptions{})
		f := find(Preflight(ctx, New(cs), false, "", want), "nodeports")
		if f.OK != (c.held == "") || !strings.Contains(f.Detail, c.held) {
			t.Errorf("%s: %+v", name, f)
		}
	}
	if f := find(Preflight(ctx, New(cluster("37", "")), false, "", nil), "nodeports"); f.Check != "absent" {
		t.Errorf("NodePorts checked when no app publishes one: %+v", f)
	}
	if f := find(Preflight(ctx, New(cluster("37", "list services")), false, "", nil), "permissions"); f.OK || !strings.Contains(f.Detail, "list services") {
		t.Errorf("denied service listing not reported: %+v", f)
	}
}
