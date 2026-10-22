package kube

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

type deployStep struct {
	a    App
	hash string
}

func (d deployStep) ID() string        { return d.a.Name + ".deploy" }
func (d deployStep) InputHash() string { return d.hash }

// check returns why the deployment is not current, or "" if it is.
func (d deployStep) check(ctx context.Context) (string, error) {
	ns := namespaceOf(d.a.Name)
	cs := d.a.Client.cs
	want := render(d.a)
	// Pod security and the policy are security controls: drift in either is repaired.
	n, err := cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return "namespace missing", nil
	case err != nil:
		return "", fmt.Errorf("namespace %s: %w", ns, err)
	}
	for k, v := range d.a.namespaceLabels() {
		if n.Labels[k] != v {
			return "namespace label " + k + " differs", nil
		}
	}
	dep, found, err := getOwned(ctx, cs.AppsV1().Deployments(ns), "deployment", appName)
	switch {
	case err != nil:
		return "", err
	case !found:
		return "deployment missing", nil
	case dep.Annotations[hashAnnotation] != d.hash:
		return "deployed objects differ from this release", nil
	case dep.Spec.Replicas == nil || *dep.Spec.Replicas != 1:
		return "deployment is not at one replica", nil
	}
	for _, svc := range want.services() {
		have, found, err := getOwned(ctx, cs.CoreV1().Services(ns), "service", svc.Name)
		switch {
		case err != nil || !found:
			return "service " + svc.Name + " missing", err
		case exposure(have) != exposure(svc):
			return "service " + svc.Name + " exposes other ports", nil
		}
	}
	for _, c := range want.Claims {
		if _, found, err := getOwned(ctx, cs.CoreV1().PersistentVolumeClaims(ns), "claim", c.Name); err != nil || !found {
			return "claim " + c.Name + " missing", err
		}
	}
	pol, found, err := getOwned(ctx, cs.NetworkingV1().NetworkPolicies(ns), "network policy", policyName)
	switch {
	case err != nil:
		return "", err
	case !found:
		return "network policy missing", nil
	case !equality.Semantic.DeepEqual(pol.Spec, want.Policy.Spec):
		return "network policy differs", nil
	}
	return "", nil
}

// exposure is what a Service publishes, in a form the API server's defaults do not change.
func exposure(s *corev1.Service) string {
	t := s.Spec.Type
	if t == "" {
		t = corev1.ServiceTypeClusterIP
	}
	out := fmt.Sprint(t, s.Spec.ExternalTrafficPolicy, s.Spec.ExternalIPs)
	for _, p := range s.Spec.Ports {
		out += fmt.Sprintf(" %d:%d->%s", p.Port, p.NodePort, p.TargetPort.String())
	}
	return out
}

func (d deployStep) Inspect(ctx context.Context) (bool, error) {
	why, err := d.check(ctx)
	return why == "", err
}

// Apply writes the policy, claims and service, then the Deployment carrying the input hash, so
// a run that dies early redeploys next time.
func (d deployStep) Apply(ctx context.Context) error {
	if err := d.a.ensureNamespace(ctx); err != nil {
		return err
	}
	ns := namespaceOf(d.a.Name)
	cs := d.a.Client.cs
	o := render(d.a)
	if err := upsert(ctx, cs.NetworkingV1().NetworkPolicies(ns), "network policy", o.Policy, nil); err != nil {
		return err
	}
	// An app that relies on its egress policy runs only where the cluster enforces it.
	if d.a.Catalog.Kubernetes.Egress == "edge" {
		if err := d.a.Client.proveEgress(ctx, ns, d.a.ReleaseSet); err != nil {
			return err
		}
	}
	for _, c := range o.Claims {
		// A claim's spec is immutable once bound: create it if missing, never rewrite it.
		have, found, err := getOwned(ctx, cs.CoreV1().PersistentVolumeClaims(ns), "claim", c.Name)
		switch {
		case err != nil:
			return err
		case !found:
			if _, err := cs.CoreV1().PersistentVolumeClaims(ns).Create(ctx, c, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("claim %s: %w", c.Name, err)
			}
		case c.Spec.StorageClassName != nil && ptr.Deref(have.Spec.StorageClassName, "") != *c.Spec.StorageClassName:
			return fmt.Errorf("claim %s has StorageClass %q but storage_class names %q; a claim's class cannot change",
				c.Name, ptr.Deref(have.Spec.StorageClassName, ""), *c.Spec.StorageClassName)
		}
	}
	for _, svc := range o.services() {
		err := upsert(ctx, cs.CoreV1().Services(ns), "service", svc, func(have *corev1.Service) {
			svc.Spec.ClusterIP, svc.Spec.ClusterIPs = have.Spec.ClusterIP, have.Spec.ClusterIPs
			svc.Spec.IPFamilies, svc.Spec.IPFamilyPolicy = have.Spec.IPFamilies, have.Spec.IPFamilyPolicy
		})
		if err != nil {
			return err
		}
	}
	o.Deployment.Annotations = map[string]string{hashAnnotation: d.hash}
	return upsert(ctx, cs.AppsV1().Deployments(ns), "deployment", o.Deployment, nil)
}

func (d deployStep) Verify(ctx context.Context) error {
	why, err := d.check(ctx)
	if err != nil {
		return err
	}
	if why != "" {
		return errors.New(why)
	}
	return nil
}
