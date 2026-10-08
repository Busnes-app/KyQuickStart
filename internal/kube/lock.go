package kube

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/managed"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	lockNamespace = "kyquickstart"
	lockName      = "kyquickstart-lock"
	runAnnotation = "kyquickstart/run"
)

// LockedError is a cluster held by another run. The Lease is never broken automatically.
type LockedError struct {
	Holder  string
	Started time.Time
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("cluster is locked by %q since %s (%s ago); delete Lease %s/%s by hand once you are sure that run is gone",
		e.Holder, e.Started.Format(time.RFC3339), time.Since(e.Started).Round(time.Second), lockNamespace, lockName)
}

var errNotHeld = errors.New("lock is not held by this run")

// Acquire creates the lock Lease for one run; release deletes it only while this run holds it.
func Acquire(ctx context.Context, c *Client, holder string) (release func(context.Context) error, err error) {
	b := make([]byte, 16)
	rand.Read(b)
	run := hex.EncodeToString(b)
	labels := map[string]string{managed.Label: managed.Value, podSecurity: "restricted"}
	if err := c.ensureNamespace(ctx, lockNamespace, labels); err != nil {
		return nil, err
	}
	leases := c.cs.CoordinationV1().Leases(lockNamespace)
	release = func(ctx context.Context) error {
		l, err := leases.Get(ctx, lockName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) || err == nil && l.Annotations[runAnnotation] != run {
			return fmt.Errorf("release %s/%s: %w", lockNamespace, lockName, errNotHeld)
		}
		if err != nil {
			return fmt.Errorf("release %s/%s: %w", lockNamespace, lockName, err)
		}
		uid, rv := l.UID, l.ResourceVersion
		return leases.Delete(ctx, lockName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})
	}
	_, err = leases.Create(ctx, &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: lockName, Namespace: lockNamespace,
			Labels: map[string]string{managed.Label: managed.Value}, Annotations: map[string]string{runAnnotation: run}},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder, AcquireTime: &metav1.MicroTime{Time: time.Now()}},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		le := &LockedError{}
		if l, err := leases.Get(ctx, lockName, metav1.GetOptions{}); err == nil {
			if l.Spec.HolderIdentity != nil {
				le.Holder = *l.Spec.HolderIdentity
			}
			if l.Spec.AcquireTime != nil {
				le.Started = l.Spec.AcquireTime.Time
			}
		}
		return nil, le
	}
	if err != nil {
		// The create may have landed before the error (a cancelled run): remove it if it is ours.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if rerr := release(cctx); rerr != nil && !errors.Is(rerr, errNotHeld) {
			return nil, errors.Join(fmt.Errorf("acquire lock: %w", err), rerr)
		}
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	return release, nil
}
