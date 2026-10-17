package kube

import (
	"context"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/stack"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestMarkerRoundTrip(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	c := New(cs)
	if m, err := c.Marker(ctx); err != nil || m != (stack.Marker{}) {
		t.Fatalf("absent marker = %+v, %v", m, err)
	}
	release, err := Acquire(ctx, c, "run")
	if err != nil {
		t.Fatal(err)
	}
	defer release(ctx)
	if m, err := c.Marker(ctx); err != nil || m != (stack.Marker{}) {
		t.Fatalf("unmarked namespace = %+v, %v", m, err)
	}
	want := stack.Marker{Revision: 3, Owner: "cli"}
	if err := c.WriteMarker(ctx, want); err != nil {
		t.Fatal(err)
	}
	if m, err := c.Marker(ctx); err != nil || m != want {
		t.Fatalf("marker = %+v, %v", m, err)
	}
	ns, _ := cs.CoreV1().Namespaces().Get(ctx, lockNamespace, metav1.GetOptions{})
	ns.Annotations[revisionAnnotation] = "3x"
	cs.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{})
	if _, err := c.Marker(ctx); err == nil {
		t.Error("accepted a non-numeric revision")
	}
}
