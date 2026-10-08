package kube

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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

// A retried create that already landed reports AlreadyExists for our own Lease.
func TestAcquireOwnRetriedCreate(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	gvr := coordinationv1.SchemeGroupVersion.WithResource("leases")
	cs.PrependReactor("create", "leases", func(a k8stesting.Action) (bool, runtime.Object, error) {
		obj := a.(k8stesting.CreateAction).GetObject()
		if err := cs.Tracker().Create(gvr, obj, a.GetNamespace()); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewAlreadyExists(gvr.GroupResource(), lockName)
	})
	release, err := Acquire(ctx, New(cs), "run-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoordinationV1().Leases(lockNamespace).Get(ctx, lockName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("lease after release: %v", err)
	}
}

func TestAcquireHolderUnreadable(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	c := New(cs)
	if _, err := Acquire(ctx, c, "run-a"); err != nil {
		t.Fatal(err)
	}
	cs.PrependReactor("get", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("boom")
	})
	_, err := Acquire(ctx, c, "run-b")
	var le *LockedError
	if err == nil || errors.As(err, &le) || !strings.Contains(err.Error(), "holder unreadable") {
		t.Fatalf("err = %v", err)
	}
}
