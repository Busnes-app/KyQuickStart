package kube

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	want := render(d.a.Name, d.a.Catalog, d.a.ReleaseSet)
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
	if want.Service != nil {
		if _, found, err := getOwned(ctx, cs.CoreV1().Services(ns), "service", appName); err != nil || !found {
			return "service missing", err
		}
	}
	for _, c := range want.Claims {
		if _, found, err := getOwned(ctx, cs.CoreV1().PersistentVolumeClaims(ns), "claim", c.Name); err != nil || !found {
			return "claim " + c.Name + " missing", err
		}
	}
	if _, found, err := getOwned(ctx, cs.NetworkingV1().NetworkPolicies(ns), "network policy", policyName); err != nil || !found {
		return "network policy missing", err
	}
	return "", nil
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
	o := render(d.a.Name, d.a.Catalog, d.a.ReleaseSet)
	if err := upsert(ctx, cs.NetworkingV1().NetworkPolicies(ns), "network policy", o.Policy, nil); err != nil {
		return err
	}
	for _, c := range o.Claims {
		// A claim's spec is immutable once bound: create it if missing, never rewrite it.
		_, found, err := getOwned(ctx, cs.CoreV1().PersistentVolumeClaims(ns), "claim", c.Name)
		if err != nil {
			return err
		}
		if !found {
			if _, err := cs.CoreV1().PersistentVolumeClaims(ns).Create(ctx, c, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("claim %s: %w", c.Name, err)
			}
		}
	}
	if o.Service != nil {
		svc := o.Service
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
