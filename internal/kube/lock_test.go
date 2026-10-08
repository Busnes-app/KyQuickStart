package kube

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLeaseLock(t *testing.T) {
	ctx := context.Background()
	c := New(fake.NewClientset())
	release, err := Acquire(ctx, c, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Acquire(ctx, c, "run-b")
	var le *LockedError
	if !errors.As(err, &le) || le.Holder != "run-a" || time.Since(le.Started) > time.Minute {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(le.Error(), "by hand") {
		t.Errorf("message = %q", le.Error())
	}
	if err := release(ctx); err != nil {
		t.Fatal(err)
	}
	release2, err := Acquire(ctx, c, "run-b")
	if err != nil {
		t.Fatal(err)
	}
	release2(ctx)
}

func TestReleaseLeavesSomeoneElsesLease(t *testing.T) {
	ctx := context.Background()
	c := New(fake.NewClientset())
	release, err := Acquire(ctx, c, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	leases := c.cs.CoordinationV1().Leases(lockNamespace)
	l, _ := leases.Get(ctx, lockName, metav1.GetOptions{})
	l.Annotations[runAnnotation] = "someone-else"
	leases.Update(ctx, l, metav1.UpdateOptions{})
	if err := release(ctx); err == nil {
		t.Fatal("released a lease it does not hold")
	}
	if _, err := leases.Get(ctx, lockName, metav1.GetOptions{}); err != nil {
		t.Fatalf("lease gone: %v", err)
	}
}
