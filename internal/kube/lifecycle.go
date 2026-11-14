package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const restartedAnnotation = "kyquickstart/restarted-at"

// State reads the app's Deployment: running (its one replica ready), stopped (scaled to zero,
// no pods left), missing, or unknown (rolling out, failing, or not managed by kyquickstart).
func (c *Client) State(ctx context.Context, app string) (string, error) {
	state, err := c.appState(ctx, app)
	if err != nil || state == "missing" || state == "unknown" {
		return state, err
	}
	// A replicated app is running, or stopped, only when its replicas and database are too.
	extras, err := c.extras(ctx, app)
	if errors.Is(err, errNotOwned) {
		return "unknown", nil
	}
	if err != nil {
		return "", err
	}
	for _, d := range extras {
		switch {
		case state == "running" && replicas(d) > 0 && ready(d):
		case state == "stopped" && replicas(d) == 0 && d.Status.Replicas == 0:
			if left, err := c.podsLeft(ctx, app, d.Spec.Selector); err != nil || left {
				return "unknown", err
			}
		default:
			return "unknown", nil
		}
	}
	// A hibernated Cluster's pods, which no Deployment owns, must be gone too.
	if state == "stopped" {
		if left, err := c.podsLeft(ctx, app, clusterPods); err != nil || left {
			return "unknown", err
		}
	}
	return state, nil
}

func (c *Client) appState(ctx context.Context, app string) (string, error) {
	w, found, err := c.workload(ctx, app)
	switch {
	case errors.Is(err, errNotOwned):
		return "unknown", nil
	case err != nil:
		return "", err
	case !found:
		return "missing", nil
	case w.replicas == 0 && w.pods == 0:
		// status.replicas leaves out terminating pods, which may still write for their whole
		// grace period: stopped only once no pod of the app is left.
		left, err := c.podsLeft(ctx, app, w.selector)
		if err != nil || left {
			return "unknown", err
		}
		return "stopped", nil
	case w.replicas == 1 && w.why == "":
		return "running", nil
	}
	return "unknown", nil
}

// podsLeft reports whether a pod of sel in app's namespace may still run (an evicted or
// finished pod does not count).
func (c *Client) podsLeft(ctx context.Context, app string, sel *metav1.LabelSelector) (bool, error) {
	pods, err := c.cs.CoreV1().Pods(namespaceOf(app)).List(ctx, metav1.ListOptions{LabelSelector: metav1.FormatLabelSelector(sel)})
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(pods.Items, func(p corev1.Pod) bool {
		return p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed
	}), nil
}

func replicas(d *appsv1.Deployment) int32 {
	if d.Spec.Replicas == nil {
		return 1
	}
	return *d.Spec.Replicas
}

// StopStep scales the app's Deployment to zero and waits for its pods to go; its claims and
// Secret stay. A stopped or missing app is left alone.
func StopStep(a App) engine.Step { return lifecycleStep{a: a, op: "stop"} }

// StartStep scales the app back to one replica and waits until it is ready. A missing
// Deployment is left to apply.
func StartStep(a App) engine.Step { return lifecycleStep{a: a, op: "start"} }

// RestartStep replaces the app's pod, as kubectl rollout restart does, and waits until it is
// ready. run makes each restart a new step, so a rerun restarts again.
func RestartStep(a App, run string) engine.Step {
	return lifecycleStep{a: a, op: "restart", run: run}
}

type lifecycleStep struct {
	a       App
	op, run string
}

func (s lifecycleStep) ID() string        { return s.a.Name + "." + s.op }
func (s lifecycleStep) InputHash() string { return hashOf(s.op, s.run) }

func (s lifecycleStep) Inspect(ctx context.Context) (bool, error) {
	if s.op != "restart" {
		return s.Verify(ctx) == nil, nil
	}
	state, err := s.a.Client.State(ctx, s.a.Name)
	if err == nil && (state == "stopped" || state == "missing") {
		err = fmt.Errorf("%s is %s; start it first", s.a.Name, state)
	}
	return false, err
}

func (s lifecycleStep) Apply(ctx context.Context) error {
	switch s.op {
	case "stop":
		if s.a.operator() {
			if err := s.a.Client.operatorIdle(ctx, s.a.Name); err != nil {
				return err
			}
		}
		if err := s.patch(ctx, map[string]any{"spec": map[string]any{"replicas": 0}}); err != nil {
			return err
		}
		if err := s.patchExtras(ctx, func(*appsv1.Deployment) map[string]any { return map[string]any{"replicas": 0} }); err != nil {
			return err
		}
		if s.a.clustered() {
			if err := s.a.Client.hibernate(ctx, s.a.Name, true); err != nil {
				return err
			}
		}
		return s.waitStopped(ctx)
	case "start":
		if s.a.clustered() {
			if err := s.a.Client.hibernate(ctx, s.a.Name, false); err != nil {
				return err
			}
			if err := s.a.Client.waitCluster(ctx, s.a.Name); err != nil {
				return err
			}
		}
		want := map[string]int32{}
		for _, e := range render(s.a).extras() {
			want[e.Name] = *e.Spec.Replicas
		}
		if err := s.patchExtras(ctx, func(d *appsv1.Deployment) map[string]any {
			// One pod where the plan no longer says: the app must not stay down for that.
			return map[string]any{"replicas": max(want[d.Name], 1)}
		}); err != nil {
			return err
		}
		if err := s.patch(ctx, map[string]any{"spec": map[string]any{"replicas": 1}}); err != nil {
			return err
		}
	default:
		template := map[string]any{"metadata": map[string]any{"annotations": map[string]string{restartedAnnotation: s.run}}}
		if err := s.patch(ctx, map[string]any{"spec": map[string]any{"template": template}}); err != nil {
			return err
		}
		// The replicas restart with it; the database, whose restart is an outage, does not.
		if err := s.patchExtras(ctx, func(d *appsv1.Deployment) map[string]any {
			if d.Name != replicaName {
				return nil
			}
			return map[string]any{"template": template}
		}); err != nil {
			return err
		}
	}
	return healthStep{a: s.a}.Apply(ctx)
}

func (s lifecycleStep) Verify(ctx context.Context) error {
	state, err := s.a.Client.State(ctx, s.a.Name)
	switch {
	case err != nil:
		return err
	case state == "missing", s.op == "stop" && state == "stopped", s.op != "stop" && state == "running":
		return nil
	}
	return fmt.Errorf("%s is %s", s.a.Name, state)
}

// patch merges change into the app's workload, only the managed one it read: the read's
// resourceVersion makes the API server refuse one replaced in between.
func (s lifecycleStep) patch(ctx context.Context, change map[string]any) error {
	w, found, err := s.a.Client.workload(ctx, s.a.Name)
	if err != nil || !found {
		return err
	}
	if w.daemon && s.op != "restart" {
		return fmt.Errorf("%s runs on every node and has no stopped state; uninstall it to stop it", s.a.Name)
	}
	change["metadata"] = map[string]any{"resourceVersion": w.object.GetResourceVersion()}
	b, err := json.Marshal(change)
	if err != nil {
		return err
	}
	if err := s.a.Client.patchWorkload(ctx, w, b); err != nil {
		return fmt.Errorf("%s %s: %w", w.kind(), s.op, err)
	}
	return nil
}

// patchExtras merges spec(d) into the spec of each Deployment beside "app", as patch does.
func (s lifecycleStep) patchExtras(ctx context.Context, spec func(*appsv1.Deployment) map[string]any) error {
	extras, err := s.a.Client.extras(ctx, s.a.Name)
	if err != nil {
		return err
	}
	for _, d := range extras {
		change := spec(d)
		if change == nil {
			continue
		}
		b, err := json.Marshal(map[string]any{"metadata": map[string]any{"resourceVersion": d.ResourceVersion}, "spec": change})
		if err != nil {
			return err
		}
		if _, err := s.a.Client.cs.AppsV1().Deployments(d.Namespace).Patch(ctx, d.Name, types.MergePatchType, b, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("deployment %s %s: %w", d.Name, s.op, err)
		}
	}
	return nil
}

func (s lifecycleStep) waitStopped(ctx context.Context) error { return waitGone(ctx, s.a, s.Verify) }

// waitGone waits until done passes, for as long as a's pods may take to drain.
func waitGone(ctx context.Context, a App, done func(context.Context) error) error {
	// Its pods drain for up to their grace period (Kubernetes' default is 30 s) before they go.
	grace := int64(30)
	if k := a.Catalog.Kubernetes; k != nil && k.TerminationGraceSeconds > 0 {
		grace = k.TerminationGraceSeconds
	}
	timeout := time.Duration(a.Catalog.Health.TimeoutSeconds+int(grace)) * time.Second
	deadline := time.Now().Add(timeout)
	for {
		err := done(ctx)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not done after %s: %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// Logs returns the last tail lines of container (default the app's own) in each of the app's
// managed pods. Reading them needs get on pods/log, which install does not.
func (c *Client) Logs(ctx context.Context, app, container string, tail int64) ([]byte, error) {
	ns := namespaceOf(app)
	w, found, err := c.workload(ctx, app)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%s has no deployment in %s", app, ns)
	}
	pods, err := c.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: metav1.FormatLabelSelector(w.selector)})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	if container == "" {
		container = appName
	}
	var out []byte
	for _, p := range pods.Items {
		if !owned(&p) {
			continue
		}
		b, err := c.cs.CoreV1().Pods(ns).GetLogs(p.Name, &corev1.PodLogOptions{Container: container, TailLines: &tail}).DoRaw(ctx)
		if apierrors.IsForbidden(err) {
			return nil, fmt.Errorf("reading logs needs get on pods/log in namespace %s, which kyquickstart's install permissions do not include; grant it to this kubeconfig's user: %w", ns, err)
		}
		if err != nil {
			return nil, fmt.Errorf("pod %s: %w", p.Name, err)
		}
		out = append(append(out, "==> pod "+p.Name+" <==\n"...), b...)
	}
	return out, nil
}
