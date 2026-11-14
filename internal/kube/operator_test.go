package kube

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/managed"
)

const operatorNS = "kyq-cloudnative-pg"

// operatorApp is the catalog's cloudnative-pg on a fake cluster. The fake has no apps
// controller and no API extensions server: bring makes the Deployment ready and the
// definitions established, as they do.
func operatorApp(t *testing.T) (App, *dynamicfake.FakeDynamicClient, func()) {
	t.Helper()
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	lists := map[schema.GroupVersionResource]string{clusterResource: "ClusterList"}
	for kind, k := range operatorKinds {
		lists[k.gvr] = kind + "List"
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists)
	cs := fake.NewClientset()
	// One cluster behind both clients: the dynamic client's Deployments are the typed one's.
	deployments := operatorKinds["Deployment"].gvr
	dyn.PrependReactor("*", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		var err error
		switch act := action.(type) {
		case k8stesting.CreateAction: // and update, which embeds it
			d := &appsv1.Deployment{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(act.GetObject().(*unstructured.Unstructured).Object, d); err != nil {
				t.Fatal(err)
			}
			if action.GetVerb() == "create" {
				err = cs.Tracker().Create(deployments, d, d.Namespace)
			} else {
				err = cs.Tracker().Update(deployments, d, d.Namespace)
			}
			return true, act.GetObject(), err
		case k8stesting.GetAction:
			o, err := cs.Tracker().Get(deployments, act.GetNamespace(), act.GetName())
			if err != nil {
				return true, nil, err
			}
			m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
			return true, &unstructured.Unstructured{Object: m}, err
		}
		t.Fatalf("unexpected %s of deployments through the dynamic client", action.GetVerb())
		return true, nil, nil
	})
	c := New(cs)
	c.dyn = dyn
	a := App{Name: "cloudnative-pg", Client: c, Catalog: cat["cloudnative-pg"], ReleaseSet: "rs-1"}
	bring := func() {
		t.Helper()
		ctx := context.Background()
		d, err := cs.AppsV1().Deployments(operatorNS).Get(ctx, appName, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
		if _, err := cs.AppsV1().Deployments(operatorNS).UpdateStatus(ctx, d, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		crds := dyn.Resource(operatorKinds["CustomResourceDefinition"].gvr)
		list, err := crds.List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range list.Items {
			unstructured.SetNestedSlice(o.Object, []any{map[string]any{"type": "Established", "status": "True"}}, "status", "conditions")
			if _, err := crds.Update(ctx, &o, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return a, dyn, bring
}

// The manifest is its project's, byte for byte (the catalog loader checks the pinned
// SHA-256); this is everything the installer changes in it.
func TestOperatorObjects(t *testing.T) {
	a, _, _ := operatorApp(t)
	set, err := operatorObjects(a)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	var deployment *unstructured.Unstructured
	for _, o := range set.objects {
		kinds[o.GetKind()]++
		if !owned(o) || o.GetAnnotations()[hashAnnotation] != set.hash {
			t.Errorf("%s %s lacks the managed label or the hash", o.GetKind(), o.GetName())
		}
		if operatorKinds[o.GetKind()].namespaced != (o.GetNamespace() == operatorNS) {
			t.Errorf("%s %s is in namespace %q", o.GetKind(), o.GetName(), o.GetNamespace())
		}
		if o.GetKind() == "Deployment" {
			deployment = o
		}
	}
	want := map[string]int{"CustomResourceDefinition": 11, "ClusterRole": 7, "ClusterRoleBinding": 1, "ServiceAccount": 1,
		"ConfigMap": 1, "Service": 1, "Deployment": 1, "MutatingWebhookConfiguration": 1, "ValidatingWebhookConfiguration": 1}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for k, n := range want {
		if kinds[k] != n {
			t.Errorf("%d %s, want %d", kinds[k], k, n)
		}
	}
	b, _ := json.Marshal(set.objects)
	if strings.Contains(string(b), "cnpg-system") {
		t.Error("the manifest's own namespace is still named")
	}
	// The binding and every webhook follow the ServiceAccount and Service to the app's namespace.
	if n := strings.Count(string(b), `"namespace":"`+operatorNS+`"`); n != 4+1+9 {
		t.Errorf("%d references to %s, want 4 objects, 1 subject and 9 webhooks", n, operatorNS)
	}
	d := &appsv1.Deployment{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(deployment.Object, d); err != nil {
		t.Fatal(err)
	}
	image := a.Catalog.Images["operator"]
	c := d.Spec.Template.Spec.Containers[0]
	if d.Name != appName || c.Name != appName || c.Image != image || !catalog.Pinned(c.Image) || c.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("deployment %s runs container %s from %s (%s)", d.Name, c.Name, c.Image, c.ImagePullPolicy)
	}
	// The image it copies into every database pod is pinned with it.
	var env string
	for _, e := range c.Env {
		if e.Name == "OPERATOR_IMAGE_NAME" {
			env = e.Value
		}
	}
	if env != image {
		t.Errorf("OPERATOR_IMAGE_NAME = %q, want %s", env, image)
	}
	if d.Spec.Template.Labels[managed.Label] != managed.Value || d.Spec.Template.Labels[nameLabel] != a.Name {
		t.Errorf("pod labels = %v", d.Spec.Template.Labels)
	}
	if len(d.Spec.Template.Spec.Tolerations) != 2 || *d.Spec.Template.Spec.Tolerations[0].TolerationSeconds != 30 {
		t.Errorf("tolerations = %v", d.Spec.Template.Spec.Tolerations)
	}
	// Restricted, as its namespace enforces.
	sc := c.SecurityContext
	if !*d.Spec.Template.Spec.SecurityContext.RunAsNonRoot || *sc.AllowPrivilegeEscalation || len(sc.Capabilities.Drop) != 1 || !*sc.ReadOnlyRootFilesystem {
		t.Errorf("security context = %+v", sc)
	}
	p := set.policy.Spec
	if len(p.Ingress) != 1 || len(p.Ingress[0].From) != 0 || len(p.Ingress[0].Ports) != 1 || p.Ingress[0].Ports[0].Port.IntValue() != 9443 {
		t.Errorf("policy admits %+v, want the webhook port alone", p.Ingress)
	}
	if a.namespaceLabels()[podSecurity] != "restricted" {
		t.Error("the operator's namespace is not restricted")
	}
}

func TestOperatorManifestRefused(t *testing.T) {
	a, _, _ := operatorApp(t)
	for name, change := range map[string]func(string) string{
		"another kind": func(s string) string {
			return s + "\n---\napiVersion: v1\nkind: Secret\nmetadata: {name: x, namespace: cnpg-system}\n"
		},
		"another namespace": func(s string) string {
			return s + "\n---\napiVersion: v1\nkind: ConfigMap\nmetadata: {name: x, namespace: kube-system}\n"
		},
		"another image": func(s string) string { return strings.ReplaceAll(s, "cloudnative-pg:1.30.1", "cloudnative-pg:1.30.2") },
		"a second deployment": func(s string) string {
			return s + "\n---\napiVersion: apps/v1\nkind: Deployment\nmetadata: {name: cnpg-controller-manager, namespace: cnpg-system}\n"
		},
		"no namespace":           func(s string) string { return strings.Replace(s, "kind: Namespace", "kind: ConfigMap", 1) },
		"an unmoved reference":   func(s string) string { return strings.Replace(s, "kind: ClusterRoleBinding", "kind: ClusterRole", 1) },
		"another api version":    func(s string) string { return strings.Replace(s, "apiVersion: apps/v1", "apiVersion: apps/v1beta1", 1) },
		"a named service target": func(s string) string { return strings.Replace(s, "targetPort: 9443", "targetPort: webhook-server", 1) },
	} {
		b := a
		b.Catalog.Operator = []byte(change(string(a.Catalog.Operator)))
		if _, err := operatorObjects(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestOperatorInstall(t *testing.T) {
	ctx := context.Background()
	a, dyn, bring := operatorApp(t)
	steps := Steps(a)
	if len(steps) != 2 || steps[0].ID() != "cloudnative-pg.deploy" || steps[1].ID() != "cloudnative-pg.health" {
		t.Fatalf("steps = %v", steps)
	}
	deploy, health := steps[0], steps[1]
	if done, err := deploy.Inspect(ctx); err != nil || done {
		t.Fatalf("before install: %v %v", done, err)
	}
	if err := deploy.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := deploy.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	ns, err := a.Client.cs.CoreV1().Namespaces().Get(ctx, operatorNS, metav1.GetOptions{})
	if err != nil || !owned(ns) || ns.Labels[podSecurity] != "restricted" || ns.Labels[managed.VersionLabel] != "1.30.1" {
		t.Fatalf("namespace = %v, %v", ns, err)
	}
	if _, err := a.Client.cs.NetworkingV1().NetworkPolicies(operatorNS).Get(ctx, policyName, metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
	// Not healthy until the Deployment is ready and every definition is served.
	if done, err := health.Inspect(ctx); err != nil || done {
		t.Fatalf("health before the pod is ready: %v %v", done, err)
	}
	bring()
	if err := health.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if state, err := a.Client.State(ctx, a.Name); err != nil || state != "running" {
		t.Fatalf("state = %s, %v", state, err)
	}

	// A rerun writes nothing.
	dyn.ClearActions()
	if done, err := deploy.Inspect(ctx); err != nil || !done {
		t.Fatalf("rerun: %v %v", done, err)
	}
	if err := deploy.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	for _, act := range dyn.Actions() {
		if act.GetVerb() != "get" {
			t.Errorf("an idle rerun did %s %s", act.GetVerb(), act.GetResource().Resource)
		}
	}

	// A new release rewrites every object, and keeps what the running operator wrote into
	// its webhooks: without it the API server refuses every database write.
	hooks := dyn.Resource(operatorKinds["ValidatingWebhookConfiguration"].gvr)
	h, err := hooks.Get(ctx, "cnpg-validating-webhook-configuration", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	list, _, _ := unstructured.NestedSlice(h.Object, "webhooks")
	for _, w := range list {
		unstructured.SetNestedField(w.(map[string]any), "Q0E=", "clientConfig", "caBundle")
	}
	h.Object["webhooks"] = list
	if _, err := hooks.Update(ctx, h, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	next := a
	next.ReleaseSet = "rs-2"
	if done, err := Steps(next)[0].Inspect(ctx); err != nil || done {
		t.Fatalf("a new release is already applied: %v %v", done, err)
	}
	if err := Steps(next)[0].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Steps(next)[0].Verify(ctx); err != nil {
		t.Fatal(err)
	}
	h, _ = hooks.Get(ctx, "cnpg-validating-webhook-configuration", metav1.GetOptions{})
	list, _, _ = unstructured.NestedSlice(h.Object, "webhooks")
	for _, w := range list {
		if ca, _, _ := unstructured.NestedString(w.(map[string]any), "clientConfig", "caBundle"); ca != "Q0E=" {
			t.Fatalf("webhook %v lost the operator's certificate authority", w.(map[string]any)["name"])
		}
	}
	if h.GetLabels()[managed.ReleaseSetLabel] != "rs-2" {
		t.Fatal("the webhook configuration was not rewritten")
	}
}

// An operator installed another way is never adopted, and nothing is written beside it.
func TestOperatorForeign(t *testing.T) {
	ctx := context.Background()
	a, dyn, _ := operatorApp(t)
	crds := operatorKinds["CustomResourceDefinition"].gvr
	foreign := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata": map[string]any{"name": "clusters.postgresql.cnpg.io"}}}
	if _, err := dyn.Resource(crds).Create(ctx, foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if f := a.Client.OperatorCheck(ctx); f.OK || !strings.Contains(f.Detail, "installed another way") {
		t.Fatalf("preflight = %+v", f)
	}
	deploy := Steps(a)[0]
	if _, err := deploy.Inspect(ctx); err == nil || !strings.Contains(err.Error(), "installed another way") {
		t.Fatalf("inspect = %v", err)
	}
	dyn.ClearActions()
	if err := deploy.Apply(ctx); err == nil || !strings.Contains(err.Error(), "installed another way") {
		t.Fatalf("apply = %v", err)
	}
	for _, act := range dyn.Actions() {
		if act.GetVerb() != "get" {
			t.Errorf("apply did %s %s beside a foreign operator", act.GetVerb(), act.GetResource().Resource)
		}
	}
	if _, err := a.Client.cs.CoreV1().Namespaces().Get(ctx, operatorNS, metav1.GetOptions{}); err == nil {
		t.Error("apply made the namespace beside a foreign operator")
	}
}

func TestOperatorLifecycle(t *testing.T) {
	ctx := context.Background()
	a, dyn, bring := operatorApp(t)
	pollInterval = time.Millisecond
	if err := Steps(a)[0].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	bring()
	database := func(hibernated string) {
		t.Helper()
		o := &unstructured.Unstructured{Object: map[string]any{"apiVersion": clusterGroup + "/v1", "kind": "Cluster"}}
		o.SetName(databaseName)
		o.SetNamespace("kyq-kyidentity")
		o.SetAnnotations(map[string]string{hibernation: hibernated})
		api := dyn.Resource(clusterResource).Namespace("kyq-kyidentity")
		if _, err := api.Update(ctx, o, metav1.UpdateOptions{}); err != nil {
			if _, err := api.Create(ctx, o, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	database("off")

	// Under a database that runs, the operator is neither stopped nor removed.
	for name, step := range map[string]interface {
		Apply(context.Context) error
	}{"stop": StopStep(a), "uninstall": RemoveSteps(a)[0]} {
		if err := step.Apply(ctx); err == nil || !strings.Contains(err.Error(), "kyq-kyidentity/database") {
			t.Fatalf("%s under a running database: %v", name, err)
		}
	}
	if state, _ := a.Client.State(ctx, a.Name); state != "running" {
		t.Fatalf("state after the refusals = %s", state)
	}

	database("on")
	if err := RemoveSteps(a)[0].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSteps(a)[0].Verify(ctx); err != nil {
		t.Fatal(err)
	}
	// Only the Deployment goes: the definitions are what every database is made from.
	if _, err := a.Client.cs.AppsV1().Deployments(operatorNS).Get(ctx, appName, metav1.GetOptions{}); err == nil {
		t.Fatal("uninstall kept the Deployment")
	}
	set, _ := operatorObjects(a)
	for _, o := range set.objects {
		if o.GetKind() == "Deployment" {
			continue
		}
		if have, err := a.Client.operatorObject(ctx, a.Name, o); err != nil || have == nil {
			t.Errorf("uninstall removed %s %s (%v)", o.GetKind(), o.GetName(), err)
		}
	}
	if _, err := dyn.Resource(clusterResource).Namespace("kyq-kyidentity").Get(ctx, databaseName, metav1.GetOptions{}); err != nil {
		t.Fatal("uninstall touched the database:", err)
	}

	// Reinstall, then unmanage: every label goes, nothing else.
	if err := Steps(a)[0].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	un := UnmanageSteps(a)[0]
	if done, err := un.Inspect(ctx); err != nil || done {
		t.Fatalf("unmanage before: %v %v", done, err)
	}
	if err := un.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := un.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	for _, o := range set.objects {
		if o.GetKind() == "Deployment" { // read below as unmanage wrote it, through the typed client
			continue
		}
		have, err := a.Client.operatorAPI(o).Get(ctx, o.GetName(), metav1.GetOptions{})
		if err != nil || owned(have) {
			t.Errorf("after unmanage, %s %s: owned or gone (%v)", o.GetKind(), o.GetName(), err)
		}
	}
	d, err := a.Client.cs.AppsV1().Deployments(operatorNS).Get(ctx, appName, metav1.GetOptions{})
	if err != nil || owned(d) || d.Spec.Template.Labels[managed.Label] != "" {
		t.Errorf("after unmanage, the Deployment: owned or gone (%v)", err)
	}
	// Handed over, it is an operator installed another way.
	if err := Steps(a)[0].Apply(ctx); err == nil || !strings.Contains(err.Error(), "installed another way") {
		t.Fatalf("the installer would install over an unmanaged operator: %v", err)
	}
}

func TestOperatorPreflight(t *testing.T) {
	ctx := context.Background()
	_, dyn, _ := operatorApp(t)
	admin := cluster("37", "")
	c := New(admin)
	c.dyn = dyn
	out := Preflight(ctx, c, false, "", nil, Needs{Operator: true, DatabaseCluster: true})
	if f := find(out, "cluster-admin"); !f.OK {
		t.Fatalf("cluster-admin = %+v", f)
	}
	if f := find(out, "operator"); !f.OK {
		t.Fatalf("operator = %+v", f)
	}
	// The plan installs it: its absence is not a failure.
	if f := find(out, "database operator"); f.Check != "absent" {
		t.Fatalf("a plan that installs the operator checks for it: %+v", f)
	}
	limited := New(cluster("37", "* *"))
	limited.dyn = dyn
	if f := find(Preflight(ctx, limited, false, "", nil, Needs{Operator: true}), "cluster-admin"); f.OK || !strings.Contains(f.Detail, "cluster-admin kubeconfig") {
		t.Fatalf("without cluster-admin: %+v", f)
	}
	// Asked only of a plan with the app.
	if f := find(Preflight(ctx, limited, false, "", nil, Needs{}), "cluster-admin"); f.Check != "absent" {
		t.Fatalf("a plain plan asks for cluster-admin: %+v", f)
	}
}
