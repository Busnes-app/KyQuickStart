package kube

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func nginxApp(t *testing.T) catalog.App {
	t.Helper()
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	return cat["nginx"]
}

var managedLabels = map[string]string{managed.Label: managed.Value}

// The nginx edge is a restricted, unprivileged pod on every node, with no volume, no admin
// port and nothing but 80 and 443 published.
func TestRenderNginxEdge(t *testing.T) {
	c := nginxApp(t)
	if c.PodSecurity != "restricted" || len(c.Secrets) != 0 || len(c.Kubernetes.Volumes) != 0 || !strings.Contains(c.Images["nginx"], "@sha256:") {
		t.Fatalf("catalog entry = %+v", c.Manifest)
	}
	o := render(App{Name: "nginx", Catalog: c, ReleaseSet: "rs-1", TLSSecrets: []string{"kyq-tls-wild", "operator.tls"}, Upstreams: nil})
	if o.Deployment != nil || o.Service != nil || len(o.Claims) != 0 {
		t.Fatalf("the nginx edge has a Deployment, an internal Service or a claim: %+v", o)
	}
	ds := o.DaemonSet
	u := ds.Spec.UpdateStrategy.RollingUpdate
	if ds.Namespace != "kyq-nginx" || ds.Name != appName || u.MaxSurge.IntValue() != 1 || u.MaxUnavailable.IntValue() != 0 {
		t.Fatalf("daemonset = %+v", ds.Spec.UpdateStrategy)
	}
	pod := ds.Spec.Template.Spec
	ctr := pod.Containers[0]
	sc := ctr.SecurityContext
	if pod.Affinity != nil || pod.Tolerations != nil || *pod.AutomountServiceAccountToken || len(pod.InitContainers) != 0 ||
		!*sc.RunAsNonRoot || *sc.RunAsUser != 101 || !*sc.ReadOnlyRootFilesystem || *sc.AllowPrivilegeEscalation || len(sc.Capabilities.Add) != 0 || sc.Capabilities.Drop[0] != "ALL" ||
		pod.SecurityContext.Sysctls[0].Name != "net.ipv4.ip_unprivileged_port_start" {
		t.Fatalf("pod is not restricted: %+v %+v", pod.SecurityContext, sc)
	}
	if !slices.Equal(ctr.Command, edge.NginxCommand) || ctr.ReadinessProbe.HTTPGet.Path != edge.NginxReadyPath || ctr.ReadinessProbe.HTTPGet.Port.IntValue() != 80 {
		t.Fatalf("container = %v %+v", ctr.Command, ctr.ReadinessProbe)
	}
	mounts := map[string]string{}
	for _, m := range ctr.VolumeMounts {
		mounts[m.MountPath] = m.Name
	}
	if len(mounts) != 4 || mounts["/tmp"] == "" || mounts[edge.NginxConfDir] != "config" || mounts[edge.NginxTLSDir+"/kyq-tls-wild"] == "" || mounts[edge.NginxTLSDir+"/operator.tls"] == "" {
		t.Fatalf("mounts = %v", mounts)
	}
	for _, v := range pod.Volumes {
		if s := v.Secret; s != nil && (!*s.Optional || *s.DefaultMode != 0o440) {
			t.Fatalf("TLS volume %s must be optional and group-readable only", v.Name)
		}
	}
	if o.Config.Name != edgeConfig || o.Config.Data[edge.NginxMainFile] != edge.NginxMain || len(o.Config.Data) != 1 {
		t.Fatalf("config = %v", o.Config.Data)
	}
	// The Service is NPM's: same name, ports, Local policy.
	npm := render(App{Name: "npm", Catalog: npmApp(t), ReleaseSet: "rs-1"}).Public
	if o.Public.Spec.Type != npm.Spec.Type || o.Public.Spec.ExternalTrafficPolicy != corev1.ServiceExternalTrafficPolicyLocal || len(o.Public.Spec.Ports) != 2 ||
		o.Public.Spec.Ports[0] != npm.Spec.Ports[0] || o.Public.Spec.Ports[1] != npm.Spec.Ports[1] {
		t.Fatalf("public service = %+v", o.Public.Spec)
	}
	// A certificate added later changes the pod template, and so the hash.
	if render(App{Name: "nginx", Catalog: c, ReleaseSet: "rs-1"}).hash() == o.hash() {
		t.Fatal("TLS Secrets are not part of the deploy hash")
	}
}

// The edge's policy is NPM's without HTTP(S) to anywhere, which only NPM's ACME client needs.
func TestNginxEdgePolicy(t *testing.T) {
	for mode, set := range map[string]func(*catalog.Kubernetes){
		"nodeport": func(*catalog.Kubernetes) {},
		"kube-vip": func(k *catalog.Kubernetes) { k.KubeVIPAddress = "192.168.1.50" },
		"lb":       func(k *catalog.Kubernetes) { k.LoadBalancerAddress = "192.168.1.51" },
	} {
		c, n := nginxApp(t), npmApp(t)
		k, nk := *c.Kubernetes, *n.Kubernetes
		set(&k)
		set(&nk)
		c.Kubernetes, n.Kubernetes = &k, &nk
		got := render(App{Name: "nginx", Catalog: c, ReleaseSet: "rs-1"})
		want := render(App{Name: "npm", Catalog: n, ReleaseSet: "rs-1"})
		if got.Public.Spec.Type != want.Public.Spec.Type || !slices.Equal(slices.Sorted(mapKeys(got.Public.Annotations)), slices.Sorted(mapKeys(want.Public.Annotations))) {
			t.Fatalf("%s: service %v %v, want %v %v", mode, got.Public.Spec.Type, got.Public.Annotations, want.Public.Spec.Type, want.Public.Annotations)
		}
	}
	p := render(App{Name: "nginx", Catalog: nginxApp(t), ReleaseSet: "rs-1"}).Policy.Spec
	npm := render(App{Name: "npm", Catalog: npmApp(t), ReleaseSet: "rs-1"}).Policy.Spec
	if len(p.Ingress) != 1 || len(p.Ingress[0].Ports) != 2 || len(p.Ingress[0].From) != 0 || p.Ingress[0].Ports[0].Port.IntValue() != 80 || p.Ingress[0].Ports[1].Port.IntValue() != 443 {
		t.Fatalf("ingress = %+v", p.Ingress)
	}
	if len(p.Egress) != 2 || len(npm.Egress) != 3 || len(p.Egress[0].To) == 0 || p.Egress[1].To[0].NamespaceSelector.MatchLabels[managed.Label] != managed.Value {
		t.Fatalf("egress = %+v", p.Egress)
	}
	// An app admits both edges while one replaces the other.
	hello := render(App{Name: "hello", Catalog: fixture(t), ReleaseSet: "rs-1", Edges: []string{"nginx", "npm"}}).Policy.Spec
	if from := hello.Ingress[0].From; len(from) != 2 || from[0].NamespaceSelector.MatchLabels[nsNameLabel] != "kyq-nginx" || from[1].NamespaceSelector.MatchLabels[nsNameLabel] != "kyq-npm" {
		t.Fatalf("app ingress = %+v", hello.Ingress)
	}
}

func mapKeys(m map[string]string) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// nginxCluster is a cluster whose DaemonSet controller the test plays: nodes, and the pods
// and status the test sets.
func nginxCluster(t *testing.T) (App, *fake.Clientset) {
	t.Helper()
	cs := fake.NewClientset()
	a := App{Name: "nginx", Client: New(cs), Catalog: nginxApp(t), ReleaseSet: "rs-1", TLSSecrets: []string{"wild"}}
	return a, cs
}

func TestDeployNginxEdge(t *testing.T) {
	ctx := context.Background()
	a, cs := nginxCluster(t)
	steps := Steps(a)
	if len(steps) != 3 {
		t.Fatalf("steps = %d: the nginx edge has no bootstrap", len(steps))
	}
	d := deployOf(a)
	if done, err := d.Inspect(ctx); done || err != nil {
		t.Fatalf("inspect before deploy = %v %v", done, err)
	}
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if done, err := d.Inspect(ctx); !done || err != nil {
		t.Fatalf("inspect after deploy = %v %v", done, err)
	}
	ns, _ := cs.CoreV1().Namespaces().Get(ctx, "kyq-nginx", metav1.GetOptions{})
	if ns.Labels[podSecurity] != "restricted" {
		t.Fatalf("namespace = %v", ns.Labels)
	}
	if _, err := cs.AppsV1().Deployments("kyq-nginx").Get(ctx, appName, metav1.GetOptions{}); err == nil {
		t.Fatal("a Deployment was created")
	}
	// The edge credential namespace and Secret exist for the LAN DNS ledger and DNS tokens.
	if _, err := a.Client.LANDNSLedger(ctx); err != nil {
		t.Fatal(err)
	}
	// Route files survive a redeploy that changes the main file.
	files, version, err := a.Client.EdgeFiles(ctx)
	if err != nil || files[edge.NginxMainFile] != edge.NginxMain {
		t.Fatalf("files = %v %v", files, err)
	}
	files["abc.server"], files[edge.NginxMainFile] = "server {}\n", "an older release's"
	if err := a.Client.SetEdgeFiles(ctx, files, version); err != nil {
		t.Fatal(err)
	}
	if done, _ := d.Inspect(ctx); done {
		t.Fatal("a stale main file counted as deployed")
	}
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if files, _, _ = a.Client.EdgeFiles(ctx); files["abc.server"] != "server {}\n" || files[edge.NginxMainFile] != edge.NginxMain {
		t.Fatalf("redeploy lost a route or kept the old main file: %v", files)
	}

	// Unmanage drops every label; uninstall removes the DaemonSet and Service and keeps the
	// configuration, as it keeps an app's data.
	if state, _ := a.Client.State(ctx, "nginx"); state != "unknown" { // no pod yet
		t.Fatalf("state = %s", state)
	}
	if err := StopStep(a).Apply(ctx); err == nil || !strings.Contains(err.Error(), "no stopped state") {
		t.Fatalf("stop = %v", err)
	}
	if err := RemoveSteps(a)[0].(removeStep).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.AppsV1().DaemonSets("kyq-nginx").Get(ctx, appName, metav1.GetOptions{}); err == nil {
		t.Fatal("uninstall left the DaemonSet")
	}
	if files, _, err := a.Client.EdgeFiles(ctx); err != nil || files["abc.server"] == "" {
		t.Fatalf("uninstall removed the routes: %v %v", files, err)
	}
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := UnmanageSteps(a)[0].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	ds, _ := cs.AppsV1().DaemonSets("kyq-nginx").Get(ctx, appName, metav1.GetOptions{})
	cm, _ := cs.CoreV1().ConfigMaps("kyq-nginx").Get(ctx, edgeConfig, metav1.GetOptions{})
	if owned(ds) || owned(cm) || ds.Spec.Template.Labels[managed.Label] != "" {
		t.Fatal("unmanage left a managed label")
	}
}

// Health waits for every node that answers, and not for one that does not: surviving a
// node's loss is what the DaemonSet is for.
func TestNginxEdgeHealth(t *testing.T) {
	ctx := context.Background()
	a, cs := nginxCluster(t)
	if err := deployOf(a).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	node := func(name string, ready corev1.ConditionStatus) {
		n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}}}}
		if _, err := cs.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{}); err != nil {
			cs.CoreV1().Nodes().Create(ctx, n, metav1.CreateOptions{})
		}
	}
	ds, _ := cs.AppsV1().DaemonSets("kyq-nginx").Get(ctx, appName, metav1.GetOptions{})
	// status plays the DaemonSet controller; pod sets one node's pod ready or not.
	status := func(desired, updated int32) {
		ds, _ = cs.AppsV1().DaemonSets("kyq-nginx").Get(ctx, appName, metav1.GetOptions{})
		ds.Status = appsv1.DaemonSetStatus{DesiredNumberScheduled: desired, CurrentNumberScheduled: desired, UpdatedNumberScheduled: updated}
		cs.AppsV1().DaemonSets("kyq-nginx").Update(ctx, ds, metav1.UpdateOptions{})
	}
	pod := func(n string, ready bool) {
		p := readyPod("app-"+n, "")
		p.Spec.NodeName = n
		p.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(ds, appsv1.SchemeGroupVersion.WithKind("DaemonSet"))}
		if !ready {
			p.Status.Conditions[0].Status = corev1.ConditionFalse
		}
		if _, err := cs.CoreV1().Pods("kyq-nginx").Update(ctx, p, metav1.UpdateOptions{}); err != nil {
			cs.CoreV1().Pods("kyq-nginx").Create(ctx, p, metav1.CreateOptions{})
		}
	}
	for _, n := range []string{"n1", "n2", "n3"} {
		node(n, corev1.ConditionTrue)
		pod(n, true)
	}
	h := healthStep{a: a}
	state := func() string {
		why, _, err := h.state(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return why
	}
	status(3, 3)
	pod("n3", false)
	if why := state(); why != "2 of 3 pods ready" {
		t.Fatalf("one pod unready: %q", why)
	}
	pod("n3", true)
	status(3, 2)
	if why := state(); why != "2 of 3 nodes run the current pod" {
		t.Fatalf("rollout in progress: %q", why)
	}
	status(3, 3)
	if why := state(); why != "" {
		t.Fatalf("all ready: %q", why)
	}
	if s, _ := a.Client.State(ctx, "nginx"); s != "running" {
		t.Fatalf("state = %s", s)
	}
	// n3 stops answering: its pod is not waited for, whether the controller still counts the
	// node (as the fake's status does here) or has dropped it (as kind's does).
	node("n3", corev1.ConditionUnknown)
	pod("n3", false)
	for _, desired := range []int32{3, 2} {
		status(desired, 2)
		if why := state(); why != "" {
			t.Fatalf("a dead node blocks health (desired %d): %q", desired, why)
		}
	}
	pod("n2", false)
	if why := state(); why != "1 of 2 pods ready" {
		t.Fatalf("an unready pod on a live node: %q", why)
	}
	// No pod ready anywhere is never healthy.
	node("n1", corev1.ConditionUnknown)
	node("n2", corev1.ConditionUnknown)
	if why := state(); why != "no pod is ready on a node that answers" {
		t.Fatalf("no node answers: %q", why)
	}
}

func readyPod(name, ip string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kyq-nginx", Labels: podLabels("nginx", "rs-1")},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
}

func TestEdgeLoadedAndCurl(t *testing.T) {
	ctx := context.Background()
	unready := readyPod("app-c", "10.0.0.3")
	unready.Status.Conditions[0].Status = corev1.ConditionFalse
	foreign := readyPod("stranger", "10.0.0.4")
	foreign.Labels = selector("nginx") // not installer-labelled
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: publicName, Namespace: "kyq-nginx", Labels: managedLabels, Annotations: map[string]string{}}}
	cs := fake.NewClientset(readyPod("app-a", "10.0.0.1"), readyPod("app-b", "10.0.0.2"), unready, foreign, svc)
	c := New(cs)
	var asked []string
	c.execCommand = func(_ context.Context, ns, pod, container string, args []string) ([]byte, error) {
		asked = append(asked, pod+" "+args[0])
		if ns != "kyq-nginx" || container != appName {
			t.Fatalf("exec in %s/%s", ns, container)
		}
		if pod == "app-b" {
			return nil, errors.New("exec failed")
		}
		return []byte("gen-1\n"), nil
	}
	loaded, err := c.EdgeLoaded(ctx, "gen-1")
	if err != nil || len(loaded) != 2 || loaded["app-a"] != "gen-1\n" || loaded["app-b"] != "" {
		t.Fatalf("loaded = %q %v", loaded, err)
	}
	// Hastened once per generation: the annotation is what makes the kubelet look again.
	for _, name := range []string{"app-a", "app-b"} {
		p, _ := cs.CoreV1().Pods("kyq-nginx").Get(ctx, name, metav1.GetOptions{})
		if p.Annotations[hastenAnnotation] != "gen-1" {
			t.Fatalf("%s not hastened: %v", name, p.Annotations)
		}
	}
	patches := func() (n int) {
		for _, a := range cs.Actions() {
			if a.GetVerb() == "patch" {
				n++
			}
		}
		return n
	}
	before := patches()
	c.EdgeLoaded(ctx, "gen-1")
	c.EdgeLoaded(ctx, "")
	if patches() != before {
		t.Fatal("hastened again for the same generation")
	}

	// Without kube-vip every ready pod is asked; with its egress, the one pod it rewrites.
	asked = nil
	out, err := c.EdgeCurl(ctx, []string{"-s", "http://192.0.2.1/"})
	if err != nil || len(out) != 2 || !slices.Equal(asked, []string{"app-a curl", "app-b curl"}) {
		t.Fatalf("curl = %q %v %v", out, asked, err)
	}
	svc.Annotations = kubeVIPAnnotations("192.168.1.50")
	cs.CoreV1().Services("kyq-nginx").Update(ctx, svc, metav1.UpdateOptions{})
	if _, err := c.EdgeCurl(ctx, nil); err == nil || !strings.Contains(err.Error(), "active-endpoint") {
		t.Fatalf("kube-vip without an active endpoint: %v", err)
	}
	svc.Annotations[kubeVIPPrefix+"active-endpoint"] = "10.0.0.1"
	cs.CoreV1().Services("kyq-nginx").Update(ctx, svc, metav1.UpdateOptions{})
	asked = nil
	if out, err := c.EdgeCurl(ctx, nil); err != nil || len(out) != 1 || !slices.Equal(asked, []string{"app-a curl"}) {
		t.Fatalf("kube-vip curl = %q %v %v", out, asked, err)
	}
	svc.Annotations[kubeVIPPrefix+"active-endpoint"] = "10.0.0.9"
	cs.CoreV1().Services("kyq-nginx").Update(ctx, svc, metav1.UpdateOptions{})
	if _, err := c.EdgeCurl(ctx, nil); err == nil {
		t.Fatal("kube-vip names no ready pod, yet a pod was asked")
	}
}

func TestEdgeFilesConflict(t *testing.T) {
	ctx := context.Background()
	c := New(fake.NewClientset())
	if files, version, err := c.EdgeFiles(ctx); err != nil || version != "" || len(files) != 0 {
		t.Fatalf("before the first deploy: %v %q %v", files, version, err)
	}
	if err := c.SetEdgeFiles(ctx, map[string]string{"a.server": "x"}, ""); err == nil || !strings.Contains(err.Error(), "apply the nginx app first") {
		t.Fatalf("write before the first deploy: %v", err)
	}
	cs := fake.NewClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: edgeConfig, Namespace: "kyq-nginx"}})
	if _, _, err := New(cs).EdgeFiles(ctx); !errors.Is(err, errNotOwned) {
		t.Fatalf("a foreign config map: %v", err)
	}
	cs = fake.NewClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: edgeConfig, Namespace: "kyq-nginx", Labels: managedLabels}})
	cs.PrependReactor("update", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, edgeConfig, errors.New("changed"))
	})
	if err := New(cs).SetEdgeFiles(ctx, map[string]string{}, "1"); !errors.Is(err, engine.ErrTransient) {
		t.Fatalf("a lost race: %v", err)
	}
}

func TestTLSCertificate(t *testing.T) {
	ctx := context.Background()
	secret := func(name string, labels map[string]string, typ corev1.SecretType) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kyq-nginx", Labels: labels}, Type: typ,
			Data: map[string][]byte{"tls.crt": []byte("CERT"), "tls.key": []byte("KEY")}}
	}
	c := New(fake.NewClientset(secret("good", managedLabels, corev1.SecretTypeTLS), secret("unlabelled", nil, corev1.SecretTypeTLS), secret("opaque", managedLabels, corev1.SecretTypeOpaque)))
	if b, err := c.TLSCertificate(ctx, "good"); err != nil || string(b) != "CERT" {
		t.Fatalf("good = %q %v", b, err)
	}
	if b, err := c.TLSCertificate(ctx, "absent"); err != nil || b != nil {
		t.Fatalf("absent = %q %v", b, err)
	}
	for _, name := range []string{"unlabelled", "opaque"} {
		if _, err := c.TLSCertificate(ctx, name); err == nil || strings.Contains(err.Error(), "KEY") {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

// certManagerCluster serves cert-manager's v1 API through discovery and a fake dynamic client.
func certManagerCluster(t *testing.T, resources ...string) *Client {
	t.Helper()
	cs := cluster("37", "")
	l := &metav1.APIResourceList{GroupVersion: certManagerGroup + "/v1"}
	for _, r := range resources {
		l.APIResources = append(l.APIResources, metav1.APIResource{Name: r})
	}
	cs.Discovery().(*fakediscovery.FakeDiscovery).Resources = []*metav1.APIResourceList{l}
	c := New(cs)
	c.dyn = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		certManagerResource("Issuer"): "IssuerList", certManagerResource("Certificate"): "CertificateList"})
	return c
}

func TestCertManager(t *testing.T) {
	ctx := context.Background()
	if f := New(cluster("37", "")).CertManagerCheck(); f.OK || !strings.Contains(f.Detail, "cert-manager") || !strings.Contains(f.Detail, "certificate_secret") {
		t.Fatalf("no cert-manager: %+v", f)
	}
	if f := certManagerCluster(t, "issuers").CertManagerCheck(); f.OK {
		t.Fatalf("no certificates resource: %+v", f)
	}
	c := certManagerCluster(t, "issuers", "certificates")
	if f := c.CertManagerCheck(); !f.OK {
		t.Fatalf("cert-manager present: %+v", f)
	}
	if hash, _, _, err := c.CertManager(ctx, "Certificate", "kyq-wild"); hash != "" || err != nil {
		t.Fatalf("absent certificate: %q %v", hash, err)
	}
	spec := map[string]any{"secretName": "kyq-tls-wild", "dnsNames": []any{"example.test"}}
	if err := c.ApplyCertManager(ctx, "Certificate", "kyq-wild", "h1", spec); err != nil {
		t.Fatal(err)
	}
	api := c.dyn.Resource(certManagerResource("Certificate")).Namespace("kyq-nginx")
	o, err := api.Get(ctx, "kyq-wild", metav1.GetOptions{})
	if err != nil || o.GetLabels()[managed.Label] != managed.Value || o.GetAPIVersion() != "cert-manager.io/v1" || o.GetKind() != "Certificate" {
		t.Fatalf("created = %v %v", o, err)
	}
	if hash, ready, msg, _ := c.CertManager(ctx, "Certificate", "kyq-wild"); hash != "h1" || ready || msg == "" {
		t.Fatalf("new certificate: %q %v %q", hash, ready, msg)
	}
	unstructured.SetNestedSlice(o.Object, []any{map[string]any{"type": "Ready", "status": "True", "reason": "Ready", "message": "Certificate is up to date"}}, "status", "conditions")
	api.Update(ctx, o, metav1.UpdateOptions{})
	if hash, ready, _, _ := c.CertManager(ctx, "Certificate", "kyq-wild"); hash != "h1" || !ready {
		t.Fatalf("ready certificate: %q %v", hash, ready)
	}
	spec["dnsNames"] = []any{"example.test", "*.example.test"}
	if err := c.ApplyCertManager(ctx, "Certificate", "kyq-wild", "h2", spec); err != nil {
		t.Fatal(err)
	}
	if hash, _, _, _ := c.CertManager(ctx, "Certificate", "kyq-wild"); hash != "h2" {
		t.Fatalf("updated certificate hash = %q", hash)
	}
	// Another owner's object of that name is never replaced.
	foreign := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cert-manager.io/v1", "kind": "Issuer", "spec": map[string]any{}}}
	foreign.SetName("kyq-wild")
	foreign.SetNamespace("kyq-nginx")
	c.dyn.Resource(certManagerResource("Issuer")).Namespace("kyq-nginx").Create(ctx, foreign, metav1.CreateOptions{})
	if err := c.ApplyCertManager(ctx, "Issuer", "kyq-wild", "h", map[string]any{}); !errors.Is(err, errNotOwned) {
		t.Fatalf("foreign issuer: %v", err)
	}

	// The Issuer's token copy: created, compared, replaced; its value never in an error.
	if b, err := c.ACMEToken(ctx, "kyq-wild-dns"); b != nil || err != nil {
		t.Fatalf("absent token: %q %v", b, err)
	}
	for _, token := range []string{"first-token", "second-token"} {
		if err := c.SetACMEToken(ctx, "kyq-wild-dns", []byte(token)); err != nil {
			t.Fatal(err)
		}
		if b, _ := c.ACMEToken(ctx, "kyq-wild-dns"); string(b) != token {
			t.Fatalf("token copy = %q", b)
		}
	}
	c.cs.(*fake.Clientset).PrependReactor("update", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("refused: third-token")
	})
	if err := c.SetACMEToken(ctx, "kyq-wild-dns", []byte("third-token")); err == nil || strings.Contains(err.Error(), "third-token") {
		t.Fatalf("refused write leaked or passed: %v", err)
	}
}

// The nginx edge's permissions are asked of, and granted to a runner on, only a cluster whose
// plan has it; every other plan's list is unchanged.
func TestNeeds(t *testing.T) {
	ctx := context.Background()
	if len(Needs{}.access()) != len(required) {
		t.Fatal("a plan without the nginx edge needs more than required")
	}
	for deny, needs := range map[string]Needs{
		"create daemonsets": {EdgeProxy: true}, "update configmaps": {EdgeProxy: true}, "patch pods": {EdgeProxy: true}, "list nodes": {EdgeProxy: true},
		"create issuers": {EdgeProxy: true, Certificates: true}, "update certificates": {EdgeProxy: true, Certificates: true},
	} {
		if f := find(Preflight(ctx, New(cluster("37", deny)), false, "", nil, Needs{}), "permissions"); !f.OK {
			t.Fatalf("%s is required of a plan without the nginx edge: %+v", deny, f)
		}
		if f := find(Preflight(ctx, New(cluster("37", deny)), false, "", nil, needs), "permissions"); f.OK || !strings.Contains(f.Detail, deny) {
			t.Fatalf("%s not required of a plan with it: %+v", deny, f)
		}
	}
	if f := find(Preflight(ctx, New(cluster("37", "")), false, "", nil, Needs{EdgeProxy: true, Certificates: true}), "cert-manager"); f.OK {
		t.Fatal("managed certificates passed preflight without cert-manager")
	}
	if f := find(Preflight(ctx, New(cluster("37", "")), false, "", nil, Needs{EdgeProxy: true}), "cert-manager"); f.Check != "absent" {
		t.Fatal("cert-manager checked for a plan without managed certificates")
	}
	rule := func(rules []struct{ group, resource string }, group, resource string) bool {
		return slices.Contains(rules, struct{ group, resource string }{group, resource})
	}
	names := func(n Needs) []struct{ group, resource string } {
		cluster, _ := RunnerRules(n)
		var out []struct{ group, resource string }
		for _, r := range cluster {
			out = append(out, struct{ group, resource string }{r.APIGroups[0], r.Resources[0]})
		}
		return out
	}
	base, withEdge := names(Needs{}), names(Needs{EdgeProxy: true, Certificates: true})
	if rule(base, "apps", "daemonsets") || rule(base, "", "configmaps") || rule(base, certManagerGroup, "issuers") {
		t.Fatalf("base runner rules = %v", base)
	}
	if !rule(withEdge, "apps", "daemonsets") || !rule(withEdge, "", "configmaps") || !rule(withEdge, "", "nodes") || !rule(withEdge, certManagerGroup, "certificates") {
		t.Fatalf("runner rules with the edge = %v", withEdge)
	}
}

// A plan without the nginx edge holds no DaemonSet or ConfigMap permission, so no step of
// another app may ask for either: uninstall, unmanage, state and logs read Deployments only.
func TestOtherAppsNeverReadDaemonSets(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token")
	if err := deployOf(a).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := UnmanageSteps(a)[0].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	a2, cs2 := testApp(t, "token")
	if err := deployOf(a2).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSteps(a2)[0].(removeStep).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a2.Client.State(ctx, a2.Name); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*fake.Clientset{cs, cs2} {
		for _, act := range c.Actions() {
			if r := act.GetResource().Resource; r == "daemonsets" || r == "configmaps" || r == "nodes" {
				t.Fatalf("an app that is not the nginx edge asked for %s %s", act.GetVerb(), r)
			}
		}
	}
}
