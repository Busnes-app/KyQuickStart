package kube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

var pollInterval = 2 * time.Second

type healthStep struct {
	a    App
	hash string
}

func (h healthStep) ID() string        { return h.a.Name + ".health" }
func (h healthStep) InputHash() string { return h.hash }

// state returns why the app is not ready ("" when it is) and whether the controller gave up.
func (h healthStep) state(ctx context.Context) (string, bool, error) {
	w, found, err := h.a.Client.workload(ctx, h.a.Name)
	switch {
	case err != nil:
		return "", false, err
	case !found:
		return "deployment missing", false, nil
	case w.replicas != 1:
		return "deployment is not at one replica", false, nil
	case w.why != "":
		return w.why, w.gaveUp, nil
	}
	for _, e := range render(h.a).extras() {
		d, found, err := getOwned(ctx, h.a.Client.cs.AppsV1().Deployments(namespaceOf(h.a.Name)), "deployment", e.Name)
		switch {
		case err != nil:
			return "", false, err
		case !found:
			return "deployment " + e.Name + " missing", false, nil
		case failed(d):
			return e.Name + " rollout failed: " + reasons(d), true, nil
		case replicas(d) != *e.Spec.Replicas || !ready(d):
			return fmt.Sprintf("%s: %d of %d replicas ready", e.Name, d.Status.ReadyReplicas, *e.Spec.Replicas), false, nil
		}
	}
	return "", false, nil
}

func (h healthStep) Inspect(ctx context.Context) (bool, error) {
	why, _, err := h.state(ctx)
	return why == "", err
}

// Apply waits for the rollout; it changes nothing. A failure must hold on two consecutive polls:
// right after a write the controller can still report the previous ReplicaSet's failure. A
// transient read error is not an answer, so the wait polls on.
func (h healthStep) Apply(ctx context.Context) error {
	timeout := time.Duration(h.a.Catalog.Health.TimeoutSeconds) * time.Second
	deadline := time.Now().Add(timeout)
	strikes := 0
	last := "no answer from the API server"
	for {
		why, gaveUp, err := h.state(ctx)
		switch {
		case err != nil && !transient(err):
			return err
		case err == nil && why == "":
			return nil
		case err == nil:
			last = why
			if gaveUp {
				if strikes++; strikes >= 2 {
					return errors.New(why)
				}
			} else {
				strikes = 0
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not ready after %s: %s", timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (h healthStep) Verify(ctx context.Context) error {
	why, _, err := h.state(ctx)
	if err != nil {
		return err
	}
	if why != "" {
		return errors.New(why)
	}
	return nil
}

// transient is a read error worth another poll: no answer from the API server, or one that says
// try again (429, 5xx). Any other answer (403, 404) will not change by waiting.
func transient(err error) bool {
	if errors.Is(err, errNotOwned) {
		return false
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return true
	}
	code := status.Status().Code
	return code == http.StatusTooManyRequests || code >= http.StatusInternalServerError
}

func ready(d *appsv1.Deployment) bool {
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	s := d.Status
	return s.ObservedGeneration >= d.Generation && s.Replicas == want && s.UpdatedReplicas == want && s.ReadyReplicas == want && s.AvailableReplicas == want
}

// failed is a rollout the controller gave up on for the current generation: a pod the cluster
// refused to create (ReplicaFailure), or the progress deadline passed.
func failed(d *appsv1.Deployment) bool {
	if d.Status.ObservedGeneration < d.Generation {
		return false
	}
	return slices.ContainsFunc(d.Status.Conditions, func(c appsv1.DeploymentCondition) bool {
		return c.Type == appsv1.DeploymentReplicaFailure && c.Status == corev1.ConditionTrue ||
			c.Type == appsv1.DeploymentProgressing && c.Reason == "ProgressDeadlineExceeded"
	})
}

// reasons names the failing conditions, for the operator.
func reasons(d *appsv1.Deployment) string {
	var out []string
	for _, c := range d.Status.Conditions {
		if c.Reason != "" && (c.Type == appsv1.DeploymentReplicaFailure || c.Type == appsv1.DeploymentProgressing) {
			out = append(out, string(c.Type)+"="+c.Reason)
		}
	}
	return fmt.Sprint(out)
}
