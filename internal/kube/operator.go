package kube

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/utils/ptr"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
)

// A cluster operator (category operator: CloudNativePG) is its project's release manifest,
// applied whole: definitions, roles and admission webhooks for the whole cluster, and one
// Deployment. Its namespace becomes the app's own, its Deployment "app", so every other
// command finds it as it finds any app. Writing roles the installer does not itself hold
// takes cluster-admin, asked only of a plan with the app (Needs.Operator).

// operatorKind is a kind an operator's manifest may hold; any other is refused, not guessed.
type operatorKind struct {
	gvr        schema.GroupVersionResource
	namespaced bool
}

var operatorKinds = map[string]operatorKind{
	"CustomResourceDefinition":       {schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}, false},
	"ClusterRole":                    {schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}, false},
	"ClusterRoleBinding":             {schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}, false},
	"MutatingWebhookConfiguration":   {schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}, false},
	"ValidatingWebhookConfiguration": {schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}, false},
	"ServiceAccount":                 {schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}, true},
	"ConfigMap":                      {schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, true},
	"Service":                        {schema.GroupVersionResource{Version: "v1", Resource: "services"}, true},
	"Deployment":                     {schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, true},
}

// adminCheck asks for cluster-admin, which installing an operator takes: its manifest holds
// roles, and the API server lets nobody grant what they do not hold. Any narrower list that
// could write those roles (escalate, bind) would be cluster-admin under another name.
func (c *Client) adminCheck(ctx context.Context) Finding {
	const check = "cluster-admin"
	denied, err := c.denied(ctx, []authorizationv1.ResourceAttributes{{Group: "*", Resource: "*", Verb: "*"}})
	switch {
	case err != nil:
		return Finding{check, false, err.Error()}
	case len(denied) > 0:
		return Finding{check, false, "installing an operator (definitions, roles and admission webhooks for the whole cluster) needs a cluster-admin kubeconfig, which this is not. Have the cluster's owner apply this plan, or install the operator and remove it from the plan's apps"}
	}
	return Finding{check, true, "this kubeconfig may install an operator"}
}

func (a App) operator() bool { return a.Catalog.Category == "operator" }

// operatorSet is an operator's manifest as the installer applies it.
type operatorSet struct {
	objects []*unstructured.Unstructured
	hash    string
	policy  *networkingv1.NetworkPolicy
}

// operatorObjects reads the app's manifest into the objects to apply, in the manifest's
// order: its Namespace dropped for the app's own, every reference to it moved there, the
// Deployment renamed "app" and run from the catalog's digest-pinned image.
func operatorObjects(a App) (operatorSet, error) {
	o, ns, image := a.Catalog.Kubernetes.Operator, namespaceOf(a.Name), a.Catalog.Images[a.Catalog.Kubernetes.Image]
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(a.Catalog.Operator), 4096)
	var set operatorSet
	var from string // the manifest's own namespace
	var ports []networkingv1.NetworkPolicyPort
	deployments := 0
	for {
		u := &unstructured.Unstructured{}
		if err := dec.Decode(u); err == io.EOF {
			break
		} else if err != nil {
			return set, fmt.Errorf("%s: %w", o.Manifest, err)
		}
		if len(u.Object) == 0 {
			continue
		}
		what := u.GetKind() + " " + u.GetName()
		if u.GetKind() == "Namespace" && u.GetAPIVersion() == "v1" && from == "" {
			from = u.GetName()
			continue
		}
		kind, ok := operatorKinds[u.GetKind()]
		if !ok || u.GetAPIVersion() != kind.gvr.GroupVersion().String() {
			return set, fmt.Errorf("%s: %s (%s) is not a kind the installer applies", o.Manifest, what, u.GetAPIVersion())
		}
		if kind.namespaced {
			if u.GetNamespace() != from {
				return set, fmt.Errorf("%s: %s is outside the manifest's namespace", o.Manifest, what)
			}
			u.SetNamespace(ns)
		}
		switch u.GetKind() {
		case "ClusterRoleBinding":
			subjects, _, _ := unstructured.NestedSlice(u.Object, "subjects")
			for _, s := range subjects {
				if m, ok := s.(map[string]any); ok && m["namespace"] == from {
					m["namespace"] = ns
				}
			}
			u.Object["subjects"] = subjects
		case "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration":
			hooks, _, _ := unstructured.NestedSlice(u.Object, "webhooks")
			for _, h := range hooks {
				if svc, ok, _ := unstructured.NestedMap(h.(map[string]any), "clientConfig", "service"); ok && svc["namespace"] == from {
					unstructured.SetNestedField(h.(map[string]any), ns, "clientConfig", "service", "namespace")
				}
			}
			u.Object["webhooks"] = hooks
		case "Service":
			// What the API server calls: the only ports the namespace's policy admits.
			list, _, _ := unstructured.NestedSlice(u.Object, "spec", "ports")
			for _, p := range list {
				target, ok := p.(map[string]any)["targetPort"].(int64)
				if !ok {
					return set, fmt.Errorf("%s: %s needs numeric target ports", o.Manifest, what)
				}
				ports = append(ports, networkingv1.NetworkPolicyPort{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(int32(target)))})
			}
		case "Deployment":
			if deployments++; u.GetName() != o.Deployment || deployments > 1 {
				return set, fmt.Errorf("%s: %s is not the one Deployment %s", o.Manifest, what, o.Deployment)
			}
			if err := operatorDeployment(u, image, a.ReleaseSet); err != nil {
				return set, fmt.Errorf("%s: %s: %w", o.Manifest, what, err)
			}
		}
		labels := u.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		for k, v := range workloadLabels(a.ReleaseSet) {
			labels[k] = v
		}
		u.SetLabels(labels)
		set.objects = append(set.objects, u)
	}
	if from == "" || deployments != 1 || len(ports) == 0 {
		return set, fmt.Errorf("%s: needs a Namespace, the Deployment %s and a Service", o.Manifest, o.Deployment)
	}
	b, err := json.Marshal(set.objects)
	if err != nil {
		return set, err
	}
	// Nothing may still point at the namespace that is not created.
	if bytes.Contains(b, []byte(`"namespace":"`+from+`"`)) {
		return set, fmt.Errorf("%s: a reference to namespace %s is left that the installer does not move", o.Manifest, from)
	}
	sum := sha256.Sum256(b)
	set.hash = hex.EncodeToString(sum[:])
	for _, u := range set.objects {
		ann := u.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		ann[hashAnnotation] = set.hash
		u.SetAnnotations(ann)
	}
	// Admission requests come from the API server, which no pod selector names: the webhook
	// port is open to any source, and nothing else (its metrics port) is.
	set.policy = &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: policyName, Namespace: ns, Labels: workloadLabels(a.ReleaseSet)},
		Spec: networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{Ports: ports}},
		},
	}
	return set, nil
}

// operatorDeployment makes the manifest's Deployment the app's: named "app" like every
// other, its one container run from image (the manifest's tag, pinned by digest), and its
// pod leaving a dead node as fast as the databases that wait for it to fail over.
func operatorDeployment(u *unstructured.Unstructured, image, releaseSet string) error {
	u.SetName(appName)
	containers, _, _ := unstructured.NestedSlice(u.Object, "spec", "template", "spec", "containers")
	if len(containers) != 1 {
		return errors.New("needs exactly one container")
	}
	c := containers[0].(map[string]any)
	tagged, _ := c["image"].(string)
	if tagged == "" || !strings.HasPrefix(image, tagged+"@") {
		return fmt.Errorf("runs %s, which is not the catalog's %s", tagged, image)
	}
	c["name"], c["image"], c["imagePullPolicy"] = appName, image, string(corev1.PullIfNotPresent)
	// The image the operator copies its own binary from into every database pod.
	env, _ := c["env"].([]any)
	for _, e := range env {
		if m, ok := e.(map[string]any); ok && m["value"] == tagged {
			m["value"] = image
		}
	}
	if err := unstructured.SetNestedSlice(u.Object, containers, "spec", "template", "spec", "containers"); err != nil {
		return err
	}
	labels, _, _ := unstructured.NestedStringMap(u.Object, "spec", "template", "metadata", "labels")
	if labels == nil {
		labels = map[string]string{}
	}
	for k, v := range workloadLabels(releaseSet) {
		labels[k] = v
	}
	if err := unstructured.SetNestedStringMap(u.Object, labels, "spec", "template", "metadata", "labels"); err != nil {
		return err
	}
	var tolerations []any
	for _, t := range leaveDeadNode {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&t)
		if err != nil {
			return err
		}
		tolerations = append(tolerations, m)
	}
	return unstructured.SetNestedSlice(u.Object, tolerations, "spec", "template", "spec", "tolerations")
}

// api is where o is read and written.
func (c *Client) operatorAPI(o *unstructured.Unstructured) dynamic.ResourceInterface {
	kind := operatorKinds[o.GetKind()]
	if kind.namespaced {
		return c.dyn.Resource(kind.gvr).Namespace(o.GetNamespace())
	}
	return c.dyn.Resource(kind.gvr)
}

// operatorObject reads the cluster's copy of want, nil when absent. One the installer did
// not create is the same operator installed some other way: never adopted.
func (c *Client) operatorObject(ctx context.Context, app string, want *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	what := strings.ToLower(want.GetKind()) + " " + want.GetName()
	have, err := c.operatorAPI(want).Get(ctx, want.GetName(), metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("%s: %w", what, err)
	case !owned(have):
		return nil, fmt.Errorf("%s exists and is %w: this operator is already on the cluster, installed another way. Remove %s from the plan's apps; apps here use the one that is installed", what, errNotOwned, app)
	}
	return have, nil
}

type operatorStep struct{ a App }

func (s operatorStep) ID() string { return s.a.Name + ".deploy" }

func (s operatorStep) InputHash() string {
	set, err := operatorObjects(s.a)
	if err != nil {
		return hashOf("unreadable", err.Error())
	}
	return set.hash
}

// check returns why the operator is not as this release applies it, or "" if it is.
func (s operatorStep) check(ctx context.Context) (string, error) {
	set, err := operatorObjects(s.a)
	if err != nil {
		return "", err
	}
	cs, ns := s.a.Client.cs, namespaceOf(s.a.Name)
	n, err := cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		// Before anything is written: a foreign operator is refused, not "missing".
		for _, o := range set.objects {
			if _, err := s.a.Client.operatorObject(ctx, s.a.Name, o); err != nil {
				return "", err
			}
		}
		return "namespace missing", nil
	case err != nil:
		return "", fmt.Errorf("namespace %s: %w", ns, err)
	}
	for k, v := range s.a.namespaceLabels() {
		if n.Labels[k] != v {
			return "namespace label " + k + " differs", nil
		}
	}
	pol, found, err := getOwned(ctx, cs.NetworkingV1().NetworkPolicies(ns), "network policy", policyName)
	if err != nil || !found || !equality.Semantic.DeepEqual(pol.Spec, set.policy.Spec) {
		return "network policy missing or differs", err
	}
	for _, o := range set.objects {
		have, err := s.a.Client.operatorObject(ctx, s.a.Name, o)
		switch {
		case err != nil:
			return "", err
		case have == nil:
			return strings.ToLower(o.GetKind()) + " " + o.GetName() + " missing", nil
		case have.GetAnnotations()[hashAnnotation] != set.hash:
			return strings.ToLower(o.GetKind()) + " " + o.GetName() + " differs from this release", nil
		}
	}
	return "", nil
}

func (s operatorStep) Inspect(ctx context.Context) (bool, error) {
	why, err := s.check(ctx)
	return why == "", err
}

// Apply writes the namespace and its policy, then every object in the manifest's order. One
// already at this release is left alone, so a rerun writes nothing.
func (s operatorStep) Apply(ctx context.Context) error {
	set, err := operatorObjects(s.a)
	if err != nil {
		return err
	}
	// All of them first: nothing is written beside an operator installed another way.
	have := make([]*unstructured.Unstructured, len(set.objects))
	for i, o := range set.objects {
		if have[i], err = s.a.Client.operatorObject(ctx, s.a.Name, o); err != nil {
			return err
		}
	}
	if err := s.a.ensureNamespace(ctx, false); err != nil {
		return err
	}
	if err := upsert(ctx, s.a.Client.cs.NetworkingV1().NetworkPolicies(namespaceOf(s.a.Name)), "network policy", set.policy, nil); err != nil {
		return err
	}
	for i, o := range set.objects {
		what := strings.ToLower(o.GetKind()) + " " + o.GetName()
		api := s.a.Client.operatorAPI(o)
		switch {
		case have[i] == nil:
			_, err = api.Create(ctx, o, metav1.CreateOptions{})
		case have[i].GetAnnotations()[hashAnnotation] == set.hash:
			continue
		default:
			keepOperatorState(o, have[i])
			_, err = api.Update(ctx, o, metav1.UpdateOptions{})
		}
		if err := edgeWrite(what, err); err != nil {
			return err
		}
	}
	return s.a.ensureNamespace(ctx, true)
}

// keepOperatorState copies into want what the cluster and the running operator own in have:
// a Service's addresses, and the certificate authority the operator writes into its webhooks,
// without which the API server refuses every database write until it writes it again.
func keepOperatorState(want, have *unstructured.Unstructured) {
	want.SetResourceVersion(have.GetResourceVersion())
	switch want.GetKind() {
	case "Service":
		for _, f := range []string{"clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy"} {
			if v, ok, _ := unstructured.NestedFieldCopy(have.Object, "spec", f); ok {
				unstructured.SetNestedField(want.Object, v, "spec", f)
			}
		}
	case "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration":
		old, _, _ := unstructured.NestedSlice(have.Object, "webhooks")
		hooks, _, _ := unstructured.NestedSlice(want.Object, "webhooks")
		for _, h := range hooks {
			i := slices.IndexFunc(old, func(o any) bool { return o.(map[string]any)["name"] == h.(map[string]any)["name"] })
			if i < 0 {
				continue
			}
			if ca, ok, _ := unstructured.NestedString(old[i].(map[string]any), "clientConfig", "caBundle"); ok {
				unstructured.SetNestedField(h.(map[string]any), ca, "clientConfig", "caBundle")
			}
		}
		want.Object["webhooks"] = hooks
	}
}

func (s operatorStep) Verify(ctx context.Context) error {
	why, err := s.check(ctx)
	if err == nil && why != "" {
		err = errors.New(why)
	}
	return err
}

// operatorServing returns why the operator's definitions are not served yet, "" when every
// one is established: the apps after it create objects of them at once.
func (c *Client) operatorServing(ctx context.Context, a App) (string, error) {
	set, err := operatorObjects(a)
	if err != nil {
		return "", err
	}
	for _, o := range set.objects {
		if o.GetKind() != "CustomResourceDefinition" {
			continue
		}
		have, err := c.operatorAPI(o).Get(ctx, o.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return "definition " + o.GetName() + " missing", nil
		}
		if err != nil {
			return "", fmt.Errorf("definition %s: %w", o.GetName(), err)
		}
		conditions, _, _ := unstructured.NestedSlice(have.Object, "status", "conditions")
		if !slices.ContainsFunc(conditions, func(c any) bool {
			m, _ := c.(map[string]any)
			return m["type"] == "Established" && m["status"] == "True"
		}) {
			return "definition " + o.GetName() + " is not established yet", nil
		}
	}
	return "", nil
}

// operatorIdle refuses to stop or remove the operator under a database that runs: without
// it nothing fails a lost primary over, and nobody is told.
func (c *Client) operatorIdle(ctx context.Context, app string) error {
	list, err := c.dyn.Resource(clusterResource).List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) { // no such resource: no database either
		return nil
	}
	if err != nil {
		return fmt.Errorf("list database clusters: %w", err)
	}
	var awake []string
	for _, o := range list.Items {
		if o.GetAnnotations()[hibernation] != "on" {
			awake = append(awake, o.GetNamespace()+"/"+o.GetName())
		}
	}
	if len(awake) > 0 {
		return fmt.Errorf("%s runs the database cluster(s) %s, which would no longer fail over or repair: stop or uninstall the apps that use them first (that hibernates their databases)", app, strings.Join(awake, ", "))
	}
	return nil
}

// unmanageOperator reports the first object of the manifest still carrying a managed label
// ("" when none does), and with fix removes them. The Deployment is unmanage's own.
func (c *Client) unmanageOperator(ctx context.Context, a App, fix bool) (string, error) {
	set, err := operatorObjects(a)
	if err != nil {
		return "", err
	}
	for _, o := range set.objects {
		if o.GetKind() == "Deployment" {
			continue
		}
		what := strings.ToLower(o.GetKind()) + " " + o.GetName()
		api := c.operatorAPI(o)
		have, err := api.Get(ctx, o.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return what, fmt.Errorf("%s: %w", what, err)
		}
		_, x := have.GetLabels()[managed.Label]
		_, y := have.GetLabels()[managed.ReleaseSetLabel]
		if !x && !y {
			continue
		}
		if !fix {
			return what, nil
		}
		if _, err := api.Patch(ctx, o.GetName(), types.MergePatchType, unlabel, metav1.PatchOptions{}); err != nil {
			return what, fmt.Errorf("%s: %w", what, err)
		}
	}
	return "", nil
}

// OperatorCheck reports, for a plan that installs the database operator, whether the cluster
// already has one that is not the installer's, which apply would refuse.
func (c *Client) OperatorCheck(ctx context.Context) Finding {
	const check = "operator"
	crd := clusterResource.Resource + "." + clusterGroup
	have, err := c.dyn.Resource(operatorKinds["CustomResourceDefinition"].gvr).Get(ctx, crd, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return Finding{check, true, "CloudNativePG is not on this cluster; apply installs it"}
	case err != nil:
		return Finding{check, false, err.Error()}
	case !owned(have):
		return Finding{check, false, "CloudNativePG is already on this cluster, installed another way (" + crd + " is not kyquickstart's). Remove cloudnative-pg from the plan's apps; database_instances uses the one that is installed"}
	}
	return Finding{check, true, "CloudNativePG here is kyquickstart's"}
}

var _ engine.Step = operatorStep{}
