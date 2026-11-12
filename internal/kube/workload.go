package kube

import (
	"context"
	"fmt"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// workload is an app's Deployment or, for the nginx edge, its DaemonSet, as the steps read it.
type workload struct {
	daemon   bool
	object   metav1.Object
	selector *metav1.LabelSelector
	replicas int32  // a Deployment's wanted replicas; 1 for a DaemonSet
	pods     int32  // status.replicas, or the nodes a DaemonSet has pods on
	why      string // why it is not ready, "" when it is
	gaveUp   bool   // the controller gave up on this rollout
}

// workload reads app's managed workload; found is false when it has neither kind.
func (c *Client) workload(ctx context.Context, app string) (w workload, found bool, err error) {
	ns := namespaceOf(app)
	d, found, err := getOwned(ctx, c.cs.AppsV1().Deployments(ns), "deployment", appName)
	if err != nil {
		return w, false, err
	}
	if found {
		w = workload{object: d, selector: d.Spec.Selector, replicas: replicas(d), pods: d.Status.Replicas}
		switch {
		case ready(d):
		case failed(d):
			w.why, w.gaveUp = "rollout failed: "+reasons(d), true
		default:
			w.why = fmt.Sprintf("%d of 1 replicas ready", d.Status.ReadyReplicas)
		}
		return w, true, nil
	}
	ds, found, err := getOwned(ctx, c.cs.AppsV1().DaemonSets(ns), "daemonset", appName)
	if err != nil || !found {
		return w, false, err
	}
	w = workload{daemon: true, object: ds, selector: ds.Spec.Selector, replicas: 1, pods: ds.Status.CurrentNumberScheduled}
	w.why, err = c.daemonState(ctx, ds)
	return w, true, err
}

// daemonState returns why the DaemonSet is not serving from every node that answers, "" when
// it is. A node that stopped answering is not waited for: surviving it is what the DaemonSet is
// for, and its pod loads the current configuration when it returns.
func (c *Client) daemonState(ctx context.Context, ds *appsv1.DaemonSet) (string, error) {
	if ds.Status.ObservedGeneration < ds.Generation {
		return "rollout not observed yet", nil
	}
	nodes, err := c.cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("list nodes: %w", err)
	}
	up := map[string]bool{}
	for _, n := range nodes.Items {
		up[n.Name] = slices.ContainsFunc(n.Status.Conditions, func(c corev1.NodeCondition) bool {
			return c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue
		})
	}
	pods, err := c.cs.CoreV1().Pods(ds.Namespace).List(ctx, metav1.ListOptions{LabelSelector: metav1.FormatLabelSelector(ds.Spec.Selector)})
	if err != nil {
		return "", fmt.Errorf("list pods: %w", err)
	}
	// Whether status.desiredNumberScheduled still counts a node that is down varies, so the
	// pods on nodes that answer are counted here.
	nodesUp := map[string]bool{}
	ready := 0
	for _, p := range pods.Items {
		if !metav1.IsControlledBy(&p, ds) || !up[p.Spec.NodeName] || p.DeletionTimestamp != nil {
			continue
		}
		nodesUp[p.Spec.NodeName] = true
		if podReady(&p) {
			ready++
		}
	}
	st, need := ds.Status, min(int(ds.Status.DesiredNumberScheduled), len(nodesUp))
	switch {
	case ready == 0:
		return "no pod is ready on a node that answers", nil
	case int(st.UpdatedNumberScheduled) < need:
		return fmt.Sprintf("%d of %d nodes run the current pod", st.UpdatedNumberScheduled, need), nil
	case ready < need:
		return fmt.Sprintf("%d of %d pods ready", ready, need), nil
	}
	return "", nil
}

func podReady(p *corev1.Pod) bool {
	return p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning && slices.ContainsFunc(p.Status.Conditions, func(c corev1.PodCondition) bool {
		return c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue
	})
}

// kind names the workload's kind in messages.
func (w workload) kind() string {
	if w.daemon {
		return "daemonset"
	}
	return "deployment"
}

// patch merges change into the workload, only the one that was read.
func (c *Client) patchWorkload(ctx context.Context, w workload, change []byte) error {
	ns := w.object.GetNamespace()
	var err error
	if w.daemon {
		_, err = c.cs.AppsV1().DaemonSets(ns).Patch(ctx, appName, types.MergePatchType, change, metav1.PatchOptions{})
	} else {
		_, err = c.cs.AppsV1().Deployments(ns).Patch(ctx, appName, types.MergePatchType, change, metav1.PatchOptions{})
	}
	return err
}

// deleteWorkload deletes the workload by UID and waits, in the foreground, for its pods: no
// ReplicaSet or pod outlives it to start another.
func (c *Client) deleteWorkload(ctx context.Context, w workload) error {
	uid := w.object.GetUID()
	opts := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}, PropagationPolicy: ptr.To(metav1.DeletePropagationForeground)}
	ns := w.object.GetNamespace()
	var err error
	if w.daemon {
		err = c.cs.AppsV1().DaemonSets(ns).Delete(ctx, appName, opts)
	} else {
		err = c.cs.AppsV1().Deployments(ns).Delete(ctx, appName, opts)
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete %s: %w", w.kind(), err)
	}
	return nil
}
