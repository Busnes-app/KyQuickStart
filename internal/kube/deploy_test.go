package kube

import (
	"context"
	"errors"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func deployOf(a App) deployStep { return Steps(a)[1].(deployStep) }

func TestDeployCreatesThenIsDone(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token")
	d := deployOf(a)
	if done, err := d.Inspect(ctx); err != nil || done {
		t.Fatalf("Inspect before = %v %v", done, err)
	}
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	dep, err := cs.AppsV1().Deployments("kyq-hello").Get(ctx, appName, metav1.GetOptions{})
	if err != nil || dep.Annotations[hashAnnotation] != d.hash {
		t.Fatalf("deployment = %+v, %v", dep, err)
	}
	for kind, err := range map[string]error{
		"service": get(cs.CoreV1().Services("kyq-hello").Get(ctx, appName, metav1.GetOptions{})),
		"claim":   get(cs.CoreV1().PersistentVolumeClaims("kyq-hello").Get(ctx, "vol-data", metav1.GetOptions{})),
		"policy":  get(cs.NetworkingV1().NetworkPolicies("kyq-hello").Get(ctx, policyName, metav1.GetOptions{})),
	} {
		if err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
	if err := d.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if done, err := d.Inspect(ctx); err != nil || !done {
		t.Fatalf("Inspect after = %v %v", done, err)
	}
	// A second Apply updates in place without error (Service keeps its cluster IP).
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
}

func get[T any](_ T, err error) error { return err }

func TestDeployInspectSeesDrift(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token")
	d := deployOf(a)
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	deps := cs.AppsV1().Deployments("kyq-hello")
	dep, _ := deps.Get(ctx, appName, metav1.GetOptions{})
	dep.Spec.Replicas = ptr.To(int32(0))
	deps.Update(ctx, dep, metav1.UpdateOptions{})
	if done, _ := d.Inspect(ctx); done {
		t.Fatal("scaled-to-zero deployment counted as deployed")
	}
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	cs.CoreV1().Services("kyq-hello").Delete(ctx, appName, metav1.DeleteOptions{})
	if err := d.Verify(ctx); err == nil || !strings.Contains(err.Error(), "service") {
		t.Fatalf("Verify after service deletion = %v", err)
	}
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if done, _ := d.Inspect(ctx); !done {
		t.Fatal("not restored")
	}
}

func TestDeployRefusesForeignObject(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token")
	if err := a.ensureNamespace(ctx); err != nil {
		t.Fatal(err)
	}
	foreign := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: "kyq-hello", Labels: map[string]string{"team": "other"}}}
	cs.AppsV1().Deployments("kyq-hello").Create(ctx, foreign, metav1.CreateOptions{})
	err := deployOf(a).Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "not managed by kyquickstart") {
		t.Fatalf("err = %v", err)
	}
	got, _ := cs.AppsV1().Deployments("kyq-hello").Get(ctx, appName, metav1.GetOptions{})
	if got.Labels["team"] != "other" {
		t.Fatal("foreign deployment was overwritten")
	}
}

func TestDeployKeepsExistingClaim(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token")
	if err := a.ensureNamespace(ctx); err != nil {
		t.Fatal(err)
	}
	claim := render("hello", a.Catalog, "rs-0").Claims[0]
	claim.Spec.VolumeName = "bound-pv"
	cs.CoreV1().PersistentVolumeClaims("kyq-hello").Create(ctx, claim, metav1.CreateOptions{})
	if err := deployOf(a).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := cs.CoreV1().PersistentVolumeClaims("kyq-hello").Get(ctx, "vol-data", metav1.GetOptions{})
	if got.Spec.VolumeName != "bound-pv" {
		t.Fatal("existing claim was rewritten")
	}
}

func TestUpsertConflictIsTransient(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token")
	d := deployOf(a)
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	cs.PrependReactor("update", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, appName, errors.New("changed"))
	})
	if err := d.Apply(ctx); !errors.Is(err, engine.ErrTransient) {
		t.Fatalf("err = %v", err)
	}
}
