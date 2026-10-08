package kube

import (
	"context"
	"strings"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
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
	for _, f := range Preflight(ctx, New(ok), true) {
		if !f.OK {
			t.Errorf("healthy cluster failed %+v", f)
		}
	}
	if f := find(Preflight(ctx, New(cluster("29", "")), false), "kubernetes"); f.OK {
		t.Errorf("1.29 accepted: %+v", f)
	}
	if f := find(Preflight(ctx, New(cluster("37", "create deployments")), false), "permissions"); f.OK || !strings.Contains(f.Detail, "create deployments") {
		t.Errorf("denied permission not reported: %+v", f)
	}
	if f := find(Preflight(ctx, New(cluster("37", "list storageclasses")), false), "permissions"); f.OK {
		t.Errorf("denied storage class listing not reported: %+v", f)
	}
	if f := find(Preflight(ctx, New(cluster("37", "")), true), "storage"); f.OK {
		t.Errorf("missing default StorageClass accepted: %+v", f)
	}
	if f := find(Preflight(ctx, New(cluster("37", "")), false), "storage"); f.Check != "absent" {
		t.Errorf("storage checked when no app needs it: %+v", f)
	}
}
