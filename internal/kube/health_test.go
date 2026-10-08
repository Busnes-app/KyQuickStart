package kube

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func setStatus(t *testing.T, a App, s appsv1.DeploymentStatus) {
	t.Helper()
	deps := a.Client.cs.AppsV1().Deployments("kyq-hello")
	d, err := deps.Get(context.Background(), appName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d.Status = s
	if _, err := deps.UpdateStatus(context.Background(), d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func readyStatus() appsv1.DeploymentStatus {
	return appsv1.DeploymentStatus{Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
}

func TestHealthWaitsForReady(t *testing.T) {
	pollInterval = time.Millisecond
	ctx := context.Background()
	a, _ := testApp(t, "token")
	if err := deployOf(a).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	h := Steps(a)[2]
	if done, _ := h.Inspect(ctx); done {
		t.Fatal("unready deployment counted as healthy")
	}
	go func() { // no t.Fatal off the test goroutine
		time.Sleep(20 * time.Millisecond)
		deps := a.Client.cs.AppsV1().Deployments("kyq-hello")
		d, _ := deps.Get(ctx, appName, metav1.GetOptions{})
		d.Status = readyStatus()
		deps.UpdateStatus(ctx, d, metav1.UpdateOptions{})
	}()
	if err := h.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.Verify(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHealthFailsOnStuckRollout(t *testing.T) {
	pollInterval = time.Millisecond
	ctx := context.Background()
	a, _ := testApp(t, "token")
	if err := deployOf(a).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	setStatus(t, a, appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{{
		Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded",
	}}})
	err := Steps(a)[2].Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "ProgressDeadlineExceeded") {
		t.Fatalf("err = %v", err)
	}
}
