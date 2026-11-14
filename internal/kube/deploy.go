package kube

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/edge"
	appsv1 "k8s.io/api/apps/v1"
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
	if err := d.a.replicaChange(ctx); err != nil {
		return "", err
	}
	w, found, err := d.a.Client.workload(ctx, d.a.Name)
	switch {
	case err != nil:
		return "", err
	case !found:
		return "deployment missing", nil
	case w.object.GetAnnotations()[hashAnnotation] != d.hash:
		return "deployed objects differ from this release", nil
	case w.daemon != (want.DaemonSet != nil):
		return "the workload is a " + w.kind(), nil
	case !w.daemon && w.replicas != 1:
		return "deployment is not at one replica", nil
	}
	for _, e := range want.extras() {
		have, found, err := getOwned(ctx, cs.AppsV1().Deployments(ns), "deployment", e.Name)
		switch {
		case err != nil || !found:
			return "deployment " + e.Name + " missing", err
		case have.Annotations[hashAnnotation] != d.hash:
			return "deployment " + e.Name + " differs from this release", nil
		case replicas(have) != *e.Spec.Replicas:
			return fmt.Sprintf("deployment %s is not at %d replicas", e.Name, *e.Spec.Replicas), nil
		}
	}
	if want.DatabasePolicy != nil {
		have, found, err := getOwned(ctx, cs.NetworkingV1().NetworkPolicies(ns), "network policy", want.DatabasePolicy.Name)
		if err != nil || !found || !equality.Semantic.DeepEqual(have.Spec, want.DatabasePolicy.Spec) {
			return "database network policy missing or differs", err
		}
	}
	if want.Cluster != nil {
		have, err := d.a.Client.clusterObject(ctx, d.a.Name)
		switch {
		case err != nil:
			return "", err
		case have == nil || have.GetAnnotations()[hashAnnotation] != d.hash:
			return "cluster " + databaseName + " missing or differs from this release", nil
		case have.GetAnnotations()[hibernation] == "on":
			return "cluster " + databaseName + " is hibernated", nil
		}
	}
	if want.Config != nil {
		have, found, err := getOwned(ctx, cs.CoreV1().ConfigMaps(ns), "config map", edgeConfig)
		if err != nil || !found || have.Data[edge.NginxMainFile] != edge.NginxMain {
			return "edge configuration missing or differs", err
		}
		if _, err := d.a.Client.credentials(ctx); err != nil {
			return "edge credential Secret missing", nil
		}
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
	keys := slices.Concat(slices.Collect(maps.Keys(kubeVIPAnnotations(""))), slices.Collect(maps.Keys(loadBalancerAnnotations(""))))
	slices.Sort(keys)
	for _, k := range keys {
		out += " " + k + "=" + s.Annotations[k]
	}
	for _, p := range s.Spec.Ports {
		out += fmt.Sprintf(" %d:%d->%s", p.Port, p.NodePort, p.TargetPort.String())
	}
	return out
}

// keepKubeVIPState keeps the annotations kube-vip writes for its election and egress rules,
// and those MetalLB and Cilium write about the assignment, so an apply does not make the
// load balancer re-announce the address.
func keepKubeVIPState(want, have *corev1.Service) {
	for k, v := range have.Annotations {
		// Another mode's address request is never kept: it would announce a second address.
		_, ours := want.Annotations[k]
		_, vip := kubeVIPAnnotations("")[k]
		_, lb := loadBalancerAnnotations("")[k]
		if ours || vip || lb {
			continue
		}
		if strings.HasPrefix(k, kubeVIPPrefix) || strings.HasPrefix(k, metalLBGroup+"/") || strings.HasPrefix(k, "lbipam."+ciliumGroup+"/") {
			want.Annotations[k] = v
		}
	}
}

func (d deployStep) Inspect(ctx context.Context) (bool, error) {
	why, err := d.check(ctx)
	return why == "", err
}

// Apply writes the policy, claims and service, then the Deployment carrying the input hash, so
// a run that dies early redeploys next time.
func (d deployStep) Apply(ctx context.Context) error {
	if err := d.a.replicaChange(ctx); err != nil {
		return err
	}
	if err := d.a.ensureNamespace(ctx, false); err != nil {
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
			if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
				svc.Spec.HealthCheckNodePort = have.Spec.HealthCheckNodePort
				keepKubeVIPState(svc, have)
			}
		})
		if err != nil {
			return err
		}
	}
	if o.DaemonSet != nil {
		if err := d.a.Client.ensureEdge(ctx, d.a.ReleaseSet, o.Config); err != nil {
			return err
		}
		o.DaemonSet.Annotations = map[string]string{hashAnnotation: d.hash}
		if err := upsert(ctx, cs.AppsV1().DaemonSets(ns), "daemonset", o.DaemonSet, nil); err != nil {
			return err
		}
		return d.a.ensureNamespace(ctx, true)
	}
	if err := d.stopOldReplicas(ctx, o); err != nil {
		return err
	}
	if o.DatabasePolicy != nil {
		if err := upsert(ctx, cs.NetworkingV1().NetworkPolicies(ns), "network policy", o.DatabasePolicy, nil); err != nil {
			return err
		}
	}
	if o.Cluster != nil {
		if err := d.a.Client.applyCluster(ctx, d.a, o.Cluster, d.hash); err != nil {
			return err
		}
		// Its instances start one after another, each copying the one before: the app's
		// pods, which would only fail until then, start once it serves.
		if err := d.a.Client.waitCluster(ctx, d.a.Name); err != nil {
			return err
		}
	}
	// The database before the pods that wait for it, and "app" before the replicas: it is
	// the one that migrates.
	for _, dep := range []*appsv1.Deployment{o.Database, o.Deployment, o.Replica} {
		if dep == nil {
			continue
		}
		dep.Annotations = map[string]string{hashAnnotation: d.hash}
		if err := upsert(ctx, cs.AppsV1().Deployments(ns), "deployment", dep, nil); err != nil {
			return err
		}
	}
	return d.a.ensureNamespace(ctx, true)
}

// stopOldReplicas stops the replicas before another release's image starts: "app" migrates the
// database as it starts, and no release promises to serve a schema newer than itself. An
// upgrade is therefore an outage as long as a single instance's, not a rolling one.
func (d deployStep) stopOldReplicas(ctx context.Context, o objects) error {
	if o.Replica == nil {
		return nil
	}
	api := d.a.Client.cs.AppsV1().Deployments(namespaceOf(d.a.Name))
	have, found, err := getOwned(ctx, api, "deployment", replicaName)
	if err != nil || !found || have.Spec.Template.Spec.Containers[0].Image == o.Replica.Spec.Template.Spec.Containers[0].Image {
		return err
	}
	have.Spec.Replicas = ptr.To(int32(0))
	if _, err := api.Update(ctx, have, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("deployment %s: stop before the upgrade: %w", replicaName, err)
	}
	return waitGone(ctx, d.a, func(ctx context.Context) error {
		left, err := d.a.Client.podsLeft(ctx, d.a.Name, have.Spec.Selector)
		if err == nil && left {
			err = errors.New("a replica of the previous release is still running")
		}
		return err
	})
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
