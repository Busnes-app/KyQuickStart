package kube

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"
)

func deployment(t *testing.T, a App) *appsv1.Deployment {
	t.Helper()
	d, err := a.Client.cs.AppsV1().Deployments("kyq-hello").Get(context.Background(), appName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func state(t *testing.T, a App) string {
	t.Helper()
	s, err := a.Client.State(context.Background(), a.Name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStopStartRestart(t *testing.T) {
	pollInterval = time.Millisecond
	a, ctx := deployed(t)
	if s := state(t, a); s != "unknown" { // no ready replica yet
		t.Fatalf("state %s", s)
	}
	setStatus(t, a, readyStatus())
	if s := state(t, a); s != "running" {
		t.Fatalf("state %s", s)
	}
	stop, start := StopStep(a), StartStep(a)
	if done, err := start.Inspect(ctx); err != nil || !done {
		t.Fatalf("start on a running app: %v %v", done, err)
	}
	if done, err := stop.Inspect(ctx); err != nil || done {
		t.Fatalf("stop on a running app: %v %v", done, err)
	}
	setStatus(t, a, appsv1.DeploymentStatus{}) // the controller's answer to zero replicas
	if err := stop.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if d := deployment(t, a); *d.Spec.Replicas != 0 || state(t, a) != "stopped" {
		t.Fatalf("replicas %d, state %s", *d.Spec.Replicas, state(t, a))
	}
	if done, err := stop.Inspect(ctx); err != nil || !done {
		t.Fatalf("stop on a stopped app: %v %v", done, err)
	}
	if _, err := RestartStep(a, "1").Inspect(ctx); err == nil || !strings.Contains(err.Error(), "start it first") {
		t.Fatalf("restart of a stopped app: %v", err)
	}
	setStatus(t, a, readyStatus())
	if err := start.Apply(ctx); err != nil || start.Verify(ctx) != nil {
		t.Fatal(err)
	}
	r := RestartStep(a, "2026-10-22T00:00:00Z")
	if done, err := r.Inspect(ctx); err != nil || done {
		t.Fatalf("restart inspect: %v %v", done, err)
	}
	if err := r.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if got := deployment(t, a).Spec.Template.Annotations[restartedAnnotation]; got != "2026-10-22T00:00:00Z" {
		t.Errorf("template annotation %q", got)
	}
	if RestartStep(a, "other").InputHash() == r.InputHash() {
		t.Error("two restarts share an input hash, so the engine would skip the second")
	}
	// Apply's drift check does not see a restart.
	if why, err := deployOf(a).check(ctx); err != nil || why != "" {
		t.Errorf("restart reads as drift: %q %v", why, err)
	}
}

func TestLifecycleRefusesForeignDeployment(t *testing.T) {
	a, ctx := deployed(t)
	d := deployment(t, a)
	d.Labels = nil
	if _, err := a.Client.cs.AppsV1().Deployments("kyq-hello").Update(ctx, d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if s := state(t, a); s != "unknown" {
		t.Errorf("state %s", s)
	}
	if err := StopStep(a).Apply(ctx); err == nil || !strings.Contains(err.Error(), "not managed by kyquickstart") {
		t.Fatalf("stop of a foreign deployment: %v", err)
	}
	if *deployment(t, a).Spec.Replicas != 1 {
		t.Error("foreign deployment scaled")
	}
	if _, err := a.Client.Logs(ctx, "hello", "", 10); err == nil {
		t.Error("read a foreign deployment's logs")
	}
}

func TestStartLeavesMissingDeploymentToApply(t *testing.T) {
	a, _ := testApp(t, "token")
	if s := state(t, a); s != "missing" {
		t.Fatalf("state %s", s)
	}
	if done, err := StartStep(a).Inspect(context.Background()); err != nil || !done {
		t.Fatalf("start without a deployment: %v %v", done, err)
	}
}

func TestLogs(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token")
	for _, s := range Steps(a)[:2] {
		if err := s.Apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for name, labels := range map[string]map[string]string{"app-1": podLabels("hello", "rs-1"), "foreign": selector("hello")} {
		if err := cs.Tracker().Add(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kyq-hello", Labels: labels}}); err != nil {
			t.Fatal(err)
		}
	}
	var asked []string
	cs.PrependReactor("get", "pods/log", func(action k8stesting.Action) (bool, runtime.Object, error) {
		o := action.(k8stesting.GenericAction).GetValue().(*corev1.PodLogOptions)
		asked = append(asked, o.Container)
		if *o.TailLines != 10 {
			t.Errorf("tail %d", *o.TailLines)
		}
		return true, &runtime.Unknown{Raw: []byte("ready\n")}, nil
	})
	out, err := a.Client.Logs(ctx, "hello", "", 10)
	if err != nil || string(out) != "==> pod app-1 <==\nready\n" || strings.Join(asked, " ") != "app" {
		t.Fatalf("logs %q, %v, asked %v", out, err, asked)
	}
	cs.PrependReactor("get", "pods/log", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods/log"}, "app-1", nil)
	})
	if _, err := a.Client.Logs(ctx, "hello", "", 10); err == nil || !strings.Contains(err.Error(), "needs get on pods/log in namespace kyq-hello") {
		t.Fatalf("forbidden: %v", err)
	}
}
