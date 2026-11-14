package kube

import (
	"context"
	"errors"
	"fmt"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Deployed reads the app namespace's labels, which uninstall keeps.
func (c *Client) Deployed(ctx context.Context, app string) (managed.Deployed, error) {
	ns, err := c.cs.CoreV1().Namespaces().Get(ctx, namespaceOf(app), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return managed.Deployed{}, nil
	}
	if err != nil {
		return managed.Deployed{}, fmt.Errorf("namespace %s: %w", namespaceOf(app), err)
	}
	l := ns.Labels
	return managed.Deployed{Found: true, Managed: owned(ns), ReleaseSet: l[managed.ReleaseSetLabel], Version: l[managed.VersionLabel]}, nil
}

// AppEnv reads env var name of the app's Deployment container; found is false when the
// Deployment or the variable is absent.
func (c *Client) AppEnv(ctx context.Context, app, name string) (value string, found bool, err error) {
	dep, found, err := getOwned(ctx, c.cs.AppsV1().Deployments(namespaceOf(app)), "deployment", appName)
	if !found || err != nil {
		return "", false, err
	}
	for _, ctr := range dep.Spec.Template.Spec.Containers {
		for _, e := range ctr.Env {
			if ctr.Name == appName && e.Name == name {
				return e.Value, true, nil
			}
		}
	}
	return "", false, nil
}

// RemoveSteps deletes the app's Deployment (the nginx edge's DaemonSet) and Services and waits
// for its pods to go. The namespace, its claims, Secret, default-deny policy and the edge's
// configuration and certificates stay, so a later install finds the data.
func RemoveSteps(a App) []engine.Step { return []engine.Step{removeStep{a}} }

type removeStep struct{ a App }

func (s removeStep) ID() string        { return s.a.Name + ".uninstall" }
func (s removeStep) InputHash() string { return hashOf("uninstall") }

// parts are the pod names a replicated app adds beside its own.
func parts(app string) []string { return []string{app, replicaPart(app), databasePart(app)} }

// objects returns the app's workload (found false where absent) and its present Services.
func (s removeStep) objects(ctx context.Context) (workload, bool, []*corev1.Service, error) {
	ns := namespaceOf(s.a.Name)
	w, found, err := s.a.Client.workload(ctx, s.a.Name)
	if err != nil {
		return w, false, nil, err
	}
	var svcs []*corev1.Service
	for _, name := range []string{appName, publicName, databaseName} {
		svc, found, err := getOwned(ctx, s.a.Client.cs.CoreV1().Services(ns), "service", name)
		if err != nil {
			return w, false, nil, err
		}
		if found {
			svcs = append(svcs, svc)
		}
	}
	return w, found, svcs, nil
}

// Inspect is done once no pod of the app is left either: a terminating one may still write
// to the claims a reinstall mounts.
func (s removeStep) Inspect(ctx context.Context) (bool, error) {
	_, found, svcs, err := s.objects(ctx)
	if err != nil || found || len(svcs) > 0 {
		return false, err
	}
	if extras, err := s.a.Client.extras(ctx, s.a.Name); err != nil || len(extras) > 0 {
		return false, err
	}
	for _, part := range parts(s.a.Name) {
		if left, err := s.a.Client.podsLeft(ctx, s.a.Name, &metav1.LabelSelector{MatchLabels: selector(part)}); err != nil || left {
			return false, err
		}
	}
	if left, err := s.a.Client.podsLeft(ctx, s.a.Name, clusterPods); err != nil || left {
		return false, err
	}
	return true, nil
}

// Apply deletes by UID, so an object replaced in between is left alone.
func (s removeStep) Apply(ctx context.Context) error {
	w, found, svcs, err := s.objects(ctx)
	if err != nil {
		return err
	}
	ns := namespaceOf(s.a.Name)
	if found {
		if err := s.a.Client.deleteWorkload(ctx, w); err != nil {
			return err
		}
	}
	// The replicas before the database they write to.
	extras, err := s.a.Client.extras(ctx, s.a.Name)
	if err != nil {
		return err
	}
	for _, d := range extras {
		if err := s.a.Client.deleteWorkload(ctx, workload{object: d}); err != nil {
			return err
		}
	}
	// A Cluster owns its claims, so it is stopped, never deleted.
	if s.a.clustered() {
		if err := s.a.Client.hibernate(ctx, s.a.Name, true); err != nil {
			return err
		}
	}
	for _, svc := range svcs {
		uid := svc.GetUID()
		if err := s.a.Client.cs.CoreV1().Services(ns).Delete(ctx, svc.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete service %s: %w", svc.Name, err)
		}
	}
	return waitGone(ctx, s.a, s.Verify)
}

func (s removeStep) Verify(ctx context.Context) error {
	done, err := s.Inspect(ctx)
	if err == nil && !done {
		err = errors.New("deployment, service or pod remains (a pod stuck terminating: check its node and finalizers)")
	}
	return err
}

// UnmanageSteps removes the managed labels from every object the installer made for the app,
// the Deployment's pod template included, so its pods are replaced once.
func UnmanageSteps(a App) []engine.Step { return []engine.Step{unmanageStep{a}} }

type unmanageStep struct{ a App }

func (s unmanageStep) ID() string        { return s.a.Name + ".unmanage" }
func (s unmanageStep) InputHash() string { return hashOf("unmanage") }

var (
	unlabel         = []byte(`{"metadata":{"labels":{"` + managed.Label + `":null,"` + managed.ReleaseSetLabel + `":null}}}`)
	unlabelTemplate = []byte(`{"metadata":{"labels":{"` + managed.Label + `":null,"` + managed.ReleaseSetLabel + `":null}},"spec":{"template":{"metadata":{"labels":{"` + managed.Label + `":null,"` + managed.ReleaseSetLabel + `":null}}}}}`)
)

type patcher[T metav1.Object] interface {
	getter[T]
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (T, error)
}

// relabel reports whether name still carries a managed label, and with fix removes them.
func relabel[T metav1.Object](ctx context.Context, api patcher[T], name string, patch []byte, labels func(T) []map[string]string, fix bool) (bool, error) {
	o, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	labelled := false
	for _, l := range labels(o) {
		_, a := l[managed.Label]
		_, b := l[managed.ReleaseSetLabel]
		labelled = labelled || a || b
	}
	if labelled && fix {
		if _, err := api.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return true, fmt.Errorf("%s: %w", name, err)
		}
	}
	return labelled, nil
}

func meta[T metav1.Object](o T) []map[string]string { return []map[string]string{o.GetLabels()} }

// sweep checks, and with fix relabels, every object; it names the first still labelled.
func (s unmanageStep) sweep(ctx context.Context, fix bool) (string, error) {
	ns := namespaceOf(s.a.Name)
	cs := s.a.Client.cs
	o := render(s.a)
	type check struct {
		what string
		run  func() (bool, error)
	}
	checks := []check{
		{"deployment", func() (bool, error) {
			return relabel(ctx, cs.AppsV1().Deployments(ns), appName, unlabelTemplate, func(d *appsv1.Deployment) []map[string]string {
				return []map[string]string{d.Labels, d.Spec.Template.Labels}
			}, fix)
		}},
		{"service", func() (bool, error) { return relabel(ctx, cs.CoreV1().Services(ns), appName, unlabel, meta, fix) }},
		{"public service", func() (bool, error) { return relabel(ctx, cs.CoreV1().Services(ns), publicName, unlabel, meta, fix) }},
		{"network policy", func() (bool, error) {
			return relabel(ctx, cs.NetworkingV1().NetworkPolicies(ns), policyName, unlabel, meta, fix)
		}},
		{"secret", func() (bool, error) { return relabel(ctx, cs.CoreV1().Secrets(ns), secretName, unlabel, meta, fix) }},
	}
	if k := s.a.Catalog.Kubernetes; k != nil && k.Replicas != nil { // whatever the plan now says
		for _, name := range []string{replicaName, databaseName} {
			checks = append(checks, check{"deployment " + name, func() (bool, error) {
				return relabel(ctx, cs.AppsV1().Deployments(ns), name, unlabelTemplate, func(d *appsv1.Deployment) []map[string]string {
					return []map[string]string{d.Labels, d.Spec.Template.Labels}
				}, fix)
			}})
		}
		if s.a.clustered() {
			checks = append(checks, check{"cluster " + databaseName, func() (bool, error) { return s.a.Client.unmanageCluster(ctx, s.a.Name, fix) }})
		}
		for _, name := range []string{keysSecret, databaseSecret, clusterTLSSecret} {
			checks = append(checks, check{"secret " + name, func() (bool, error) { return relabel(ctx, cs.CoreV1().Secrets(ns), name, unlabel, meta, fix) }})
		}
		checks = append(checks,
			check{"database service", func() (bool, error) { return relabel(ctx, cs.CoreV1().Services(ns), databaseName, unlabel, meta, fix) }},
			check{"database network policy", func() (bool, error) {
				return relabel(ctx, cs.NetworkingV1().NetworkPolicies(ns), databaseName, unlabel, meta, fix)
			}},
			check{"claim vol-" + databaseName, func() (bool, error) {
				return relabel(ctx, cs.CoreV1().PersistentVolumeClaims(ns), "vol-"+databaseName, unlabel, meta, fix)
			}})
	}
	if o.DaemonSet != nil { // the nginx edge alone: other plans hold no permission for these
		checks = append(checks,
			check{"daemonset", func() (bool, error) {
				return relabel(ctx, cs.AppsV1().DaemonSets(ns), appName, unlabelTemplate, func(d *appsv1.DaemonSet) []map[string]string {
					return []map[string]string{d.Labels, d.Spec.Template.Labels}
				}, fix)
			}},
			check{"edge configuration", func() (bool, error) { return relabel(ctx, cs.CoreV1().ConfigMaps(ns), edgeConfig, unlabel, meta, fix) }})
	}
	for _, c := range o.Claims {
		checks = append(checks, check{"claim " + c.Name, func() (bool, error) {
			return relabel(ctx, cs.CoreV1().PersistentVolumeClaims(ns), c.Name, unlabel, meta, fix)
		}})
	}
	// The namespace last: while it is labelled, a rerun still finds the app as ours.
	checks = append(checks, check{"namespace", func() (bool, error) {
		return relabel(ctx, cs.CoreV1().Namespaces(), ns, unlabel, meta, fix)
	}})
	for _, c := range checks {
		labelled, err := c.run()
		if err != nil {
			return c.what, err
		}
		if labelled && !fix {
			return c.what, nil
		}
	}
	return "", nil
}

func (s unmanageStep) Inspect(ctx context.Context) (bool, error) {
	what, err := s.sweep(ctx, false)
	return what == "", err
}

func (s unmanageStep) Apply(ctx context.Context) error {
	_, err := s.sweep(ctx, true)
	return err
}

func (s unmanageStep) Verify(ctx context.Context) error {
	what, err := s.sweep(ctx, false)
	if err == nil && what != "" {
		err = fmt.Errorf("%s still carries %s", what, managed.Label)
	}
	return err
}
