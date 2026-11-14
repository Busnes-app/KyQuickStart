package kube

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// clusteredApp is replicatedApp with database_instances, on a cluster that serves
// CloudNativePG's Clusters. While healthy is set, the operator reports every instance ready.
func clusteredApp(t *testing.T, instances int) (App, *fake.Clientset, *dynamicfake.FakeDynamicClient, *atomic.Bool) {
	t.Helper()
	a, cs, _ := replicatedApp(t, 3, "")
	k := *a.Catalog.Kubernetes
	k.DatabaseInstances = instances
	a.Catalog.Kubernetes = &k
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{clusterResource: "ClusterList"})
	healthy := &atomic.Bool{}
	healthy.Store(true)
	dyn.PrependReactor("get", "clusters", func(action k8stesting.Action) (bool, runtime.Object, error) {
		get := action.(k8stesting.GetAction)
		o, err := dyn.Tracker().Get(clusterResource, get.GetNamespace(), get.GetName())
		if err != nil {
			return true, nil, err
		}
		u := o.(*unstructured.Unstructured).DeepCopy()
		if healthy.Load() {
			want, _, _ := unstructured.NestedInt64(u.Object, "spec", "instances")
			u.Object["status"] = map[string]any{"phase": clusterHealthy, "readyInstances": want}
		}
		return true, u, nil
	})
	a.Client.dyn = dyn
	return a, cs, dyn, healthy
}

func clusterOf(t *testing.T, dyn *dynamicfake.FakeDynamicClient) *unstructured.Unstructured {
	t.Helper()
	o, err := dyn.Tracker().Get(clusterResource, "kyq-kyidentity", databaseName)
	if err != nil {
		t.Fatal(err)
	}
	return o.(*unstructured.Unstructured)
}

func TestRenderDatabaseCluster(t *testing.T) {
	a, _, _, _ := clusteredApp(t, 3)
	a.StorageClass = "longhorn"
	o := render(a)
	if o.Database != nil || o.DatabaseService != nil || len(o.extras()) != 1 {
		t.Fatal("a clustered app still renders the single Postgres pod")
	}
	for _, c := range o.Claims {
		if c.Name == "vol-"+databaseName {
			t.Fatal("a clustered app still claims the single pod's volume")
		}
	}
	spec := o.Cluster
	str := func(path ...string) string {
		v, _, _ := unstructured.NestedString(spec, path...)
		return v
	}
	if n, _, _ := unstructured.NestedInt64(spec, "instances"); n != 3 {
		t.Errorf("instances = %d", n)
	}
	if n, _, _ := unstructured.NestedInt64(spec, "postgresql", "synchronous", "number"); n != 1 || str("postgresql", "synchronous", "method") != "any" {
		t.Error("a commit does not wait for a standby")
	}
	if str("affinity", "podAntiAffinityType") != "required" || str("affinity", "topologyKey") != "kubernetes.io/hostname" {
		t.Error("two instances may share a node")
	}
	if got := str("imageName"); got != a.Catalog.Images["postgres_cluster"] || !strings.Contains(got, "@sha256:") {
		t.Errorf("image = %q", got)
	}
	for path, want := range map[string]string{
		"database": databaseUser, "owner": databaseUser, "encoding": "UTF8", "localeCollate": "C", "localeCType": "C",
	} {
		if got := str("bootstrap", "initdb", path); got != want {
			t.Errorf("initdb %s = %q", path, got)
		}
	}
	if str("bootstrap", "initdb", "secret", "name") != databaseSecret || str("storage", "storageClass") != "longhorn" || str("storage", "size") != "2Gi" {
		t.Errorf("spec = %v", spec)
	}
	if str("certificates", "serverTLSSecret") != clusterTLSSecret || str("certificates", "serverCASecret") != clusterTLSSecret {
		t.Error("the Cluster does not serve the installer's certificate")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(spec, "enableSuperuserAccess"); found {
		t.Error("superuser access is asked for")
	}

	// Postgres from KyIdentity's pods; replication and the operator; nothing else.
	p := o.DatabasePolicy.Spec
	if p.PodSelector.MatchLabels[clusterLabel] != databaseName || len(p.Ingress) != 2 {
		t.Fatalf("policy = %+v", p)
	}
	if from := p.Ingress[0].From; len(from) != 1 || from[0].NamespaceSelector != nil || from[0].PodSelector.MatchLabels[servesLabel] != "kyidentity" ||
		len(p.Ingress[0].Ports) != 1 || p.Ingress[0].Ports[0].Port.IntValue() != databasePort {
		t.Errorf("app rule = %+v", p.Ingress[0])
	}
	if from := p.Ingress[1].From; len(from) != 2 || from[0].PodSelector.MatchLabels[clusterLabel] != databaseName || from[1].PodSelector.MatchLabels[nameLabel] != "cloudnative-pg" {
		t.Errorf("operator rule = %+v", p.Ingress[1])
	}
	for _, rule := range p.Ingress {
		for _, peer := range rule.From {
			if peer.PodSelector == nil || len(peer.PodSelector.MatchLabels) == 0 {
				t.Errorf("a rule admits every pod: %+v", rule)
			}
		}
	}
	if len(p.PolicyTypes) != 1 || p.PolicyTypes[0] != networkingv1.PolicyTypeIngress {
		t.Errorf("policy types = %v", p.PolicyTypes)
	}

	// Every KyIdentity pod trusts the authority, and holds neither key.
	for _, d := range []*corev1.PodSpec{&o.Deployment.Spec.Template.Spec, &o.Replica.Spec.Template.Spec} {
		var ca *corev1.SecretVolumeSource
		for _, v := range d.Volumes {
			if v.Name == "database-ca" {
				ca = v.Secret
			}
		}
		if ca == nil || ca.SecretName != clusterTLSSecret || len(ca.Items) != 1 || ca.Items[0].Key != clusterCAKey {
			t.Fatalf("authority volume = %+v", ca)
		}
	}
	u, err := url.Parse(string(a.databaseURL([]byte("pw"))))
	if err != nil || u.Host != "database-rw.kyq-kyidentity.svc:5432" || u.Query().Get("sslmode") != "verify-full" || u.Query().Get("sslrootcert") != clusterCADir+"/"+clusterCAKey {
		t.Errorf("url host %q query %q", u.Host, u.RawQuery)
	}

	// Without database_instances nothing of it is rendered, so existing installs keep their hash.
	single, _, _ := replicatedApp(t, 3, "")
	if o := render(single); o.Cluster != nil || o.Database == nil || !strings.Contains(string(single.databaseURL([]byte("pw"))), "sslmode=disable") {
		t.Error("the single pod changed")
	}
}

func TestServerCertificate(t *testing.T) {
	now := time.Now()
	host := clusterHost("kyidentity")
	ca, cert, key, err := serverCertificate(host, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tls.X509KeyPair(cert, key); err != nil {
		t.Fatalf("key does not match: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("authority is not a certificate")
	}
	block, _ := pem.Decode(cert)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{host, host + ".cluster.local"} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name, CurrentTime: now.AddDate(9, 11, 0)}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "database-rw.kyq-other.svc"}); err == nil {
		t.Error("valid for another namespace")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host, CurrentTime: now.AddDate(10, 0, 2)}); err == nil {
		t.Error("never expires")
	}
	if leaf.IsCA {
		t.Error("the server certificate can sign")
	}
	// A second install is its own authority.
	if other, _, _, _ := serverCertificate(host, now); string(other) == string(ca) {
		t.Error("two installs share an authority")
	}
}

func TestDatabaseClusterLifecycle(t *testing.T) {
	pollInterval = time.Millisecond
	ctx := context.Background()
	a, cs, dyn, healthy := clusteredApp(t, 3)
	steps := Steps(a)
	for _, s := range steps[:3] {
		if err := s.Apply(ctx); err != nil {
			t.Fatalf("%s: %v", s.ID(), err)
		}
		if err := s.Verify(ctx); err != nil {
			t.Fatalf("%s: %v", s.ID(), err)
		}
	}
	if got := replicaCounts(t, cs); len(got) != 2 || got["app"] != 1 || got[replicaName] != 2 {
		t.Fatalf("deployed = %v", got)
	}
	c := clusterOf(t, dyn)
	if !owned(c) || c.GetAnnotations()[hashAnnotation] == "" {
		t.Fatalf("cluster metadata = %v %v", c.GetLabels(), c.GetAnnotations())
	}

	// The owner's Secret is what the operator reads; no superuser password exists.
	db, err := cs.CoreV1().Secrets("kyq-kyidentity").Get(ctx, databaseSecret, metav1.GetOptions{})
	if err != nil || db.Type != corev1.SecretTypeBasicAuth || string(db.Data[usernameKey]) != databaseUser || len(db.Data[passwordKey]) == 0 || len(db.Data[superuserKey]) != 0 {
		t.Fatalf("database secret = %v %v", db.Type, err)
	}
	if string(db.Data["url"]) != string(a.databaseURL(db.Data[passwordKey])) {
		t.Fatal("url does not carry the password")
	}
	tlsSecret, err := cs.CoreV1().Secrets("kyq-kyidentity").Get(ctx, clusterTLSSecret, metav1.GetOptions{})
	if err != nil || tlsSecret.Type != corev1.SecretTypeTLS || len(tlsSecret.Data) != 3 {
		t.Fatalf("tls secret = %v", err)
	}
	if _, err := tls.X509KeyPair(tlsSecret.Data[corev1.TLSCertKey], tlsSecret.Data[corev1.TLSPrivateKeyKey]); err != nil {
		t.Fatal(err)
	}
	// A second apply replaces neither.
	if err := steps[1].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if again := secretOf(t, cs, clusterTLSSecret); string(again[clusterCAKey]) != string(tlsSecret.Data[clusterCAKey]) || string(secretOf(t, cs, databaseSecret)[passwordKey]) != string(db.Data[passwordKey]) {
		t.Fatal("a second apply replaced the certificate or the password")
	}

	// Health and deploy wait for the instances, not only for the pods.
	markReady(t, cs)
	health := steps[3]
	if err := health.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	healthy.Store(false)
	if err := health.Verify(ctx); err == nil || !strings.Contains(err.Error(), "0 of 3 instances ready") {
		t.Fatalf("health with no instance ready = %v", err)
	}
	clusterTimeout = 20 * time.Millisecond
	if err := steps[2].Apply(ctx); err == nil || !strings.Contains(err.Error(), "describe cluster") {
		t.Fatalf("deploy with no instance ready = %v", err)
	}
	clusterTimeout = time.Minute
	healthy.Store(true)

	// A changed plan reaches the Cluster.
	more := a
	k := *a.Catalog.Kubernetes
	k.DatabaseInstances = 5
	more.Catalog.Kubernetes = &k
	grow := Steps(more)[2]
	if done, _ := grow.Inspect(ctx); done {
		t.Fatal("deploy accepts three instances for five")
	}
	if err := grow.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := unstructured.NestedInt64(clusterOf(t, dyn).Object, "spec", "instances"); n != 5 {
		t.Fatalf("instances = %d", n)
	}
	if err := steps[2].Apply(ctx); err != nil {
		t.Fatal(err)
	}

	// Stop hibernates it; while one of its pods is left the app is not yet stopped.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "database-1", Namespace: "kyq-kyidentity", Labels: map[string]string{clusterLabel: databaseName}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if err := cs.Tracker().Add(pod); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cs.CoreV1().Pods(pod.Namespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{})
	}()
	if err := StopStep(a).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{}); err == nil {
		t.Fatal("stop returned while an instance was running")
	}
	if clusterOf(t, dyn).GetAnnotations()[hibernation] != "on" {
		t.Fatal("stop left the database running")
	}
	markReady(t, cs)
	if s, _ := a.Client.State(ctx, a.Name); s != "stopped" {
		t.Fatalf("state after stop = %s", s)
	}
	if done, _ := steps[2].Inspect(ctx); done {
		t.Fatal("deploy accepts a hibernated database")
	}
	go func() {
		for i := 0; i < 400; i++ {
			time.Sleep(5 * time.Millisecond)
			if replicaCounts(t, cs)["app"] == 1 {
				markReady(t, cs)
				return
			}
		}
	}()
	if err := StartStep(a).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if clusterOf(t, dyn).GetAnnotations()[hibernation] == "on" {
		t.Fatal("start left the database hibernated")
	}

	// Uninstall stops the instances and keeps the Cluster, which owns their claims.
	if err := RemoveSteps(a)[0].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if got := replicaCounts(t, cs); len(got) != 0 {
		t.Fatalf("left after uninstall: %v", got)
	}
	if clusterOf(t, dyn).GetAnnotations()[hibernation] != "on" {
		t.Fatal("uninstall left the database running, or deleted it")
	}
	// A reinstall wakes it.
	if err := steps[2].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if clusterOf(t, dyn).GetAnnotations()[hibernation] == "on" {
		t.Fatal("reinstall left the database hibernated")
	}

	if err := UnmanageSteps(a)[0].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if owned(clusterOf(t, dyn)) {
		t.Error("cluster still managed")
	}
	if s, _ := cs.CoreV1().Secrets("kyq-kyidentity").Get(ctx, clusterTLSSecret, metav1.GetOptions{}); owned(s) {
		t.Error("tls secret still managed")
	}
	if err := UnmanageSteps(a)[0].Verify(ctx); err != nil {
		t.Fatal(err)
	}
}

// The installer never moves a database: between the single pod and the Cluster is a move.
func TestDatabaseClusterChangeRefused(t *testing.T) {
	pollInterval = time.Millisecond
	ctx := context.Background()
	a, _, dyn, _ := clusteredApp(t, 3)
	single := a
	k := *a.Catalog.Kubernetes
	k.DatabaseInstances = 0
	single.Catalog.Kubernetes = &k
	for _, s := range Steps(single)[:3] {
		if err := s.Apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range Steps(a)[1:3] {
		if err := s.Apply(ctx); err == nil || !strings.Contains(err.Error(), "the installer's Postgres (Deployment database)") || !strings.Contains(err.Error(), "highly available") {
			t.Fatalf("%s = %v", s.ID(), err)
		}
	}
	if list, _ := dyn.Resource(clusterResource).Namespace("kyq-kyidentity").List(ctx, metav1.ListOptions{}); len(list.Items) != 0 {
		t.Fatal("a Cluster was created beside the single pod's data")
	}

	// A Cluster with no keys beside it is old data: no new keys are made.
	b, cs2, _, _ := clusteredApp(t, 3)
	for _, s := range Steps(b)[:3] {
		if err := s.Apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := cs2.CoreV1().Secrets("kyq-kyidentity").Delete(ctx, keysSecret, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := cs2.CoreV1().PersistentVolumeClaims("kyq-kyidentity").Delete(ctx, "vol-data", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := Steps(b)[1].Apply(ctx); err == nil || !strings.Contains(err.Error(), "already has a database") {
		t.Fatalf("keys regenerated beside a Cluster: %v", err)
	}
}

func TestDatabaseOperatorCheck(t *testing.T) {
	ctx := context.Background()
	cs := cluster("37", "")
	if f := New(cs).DatabaseOperatorCheck(); f.OK || !strings.Contains(f.Detail, "CloudNativePG") {
		t.Fatalf("no operator: %+v", f)
	}
	if f := find(Preflight(ctx, New(cs), false, "", nil, Needs{}), "database operator"); f.Check != "absent" {
		t.Fatalf("a plan without database_instances checks for the operator: %+v", f)
	}
	if f := find(Preflight(ctx, New(cs), false, "", nil, Needs{DatabaseCluster: true}), "database operator"); f.OK || f.Check == "absent" {
		t.Fatalf("preflight = %+v", f)
	}
	disco := cs.Discovery().(*fakediscovery.FakeDiscovery)
	disco.Resources = []*metav1.APIResourceList{{GroupVersion: clusterGroup + "/v1", APIResources: []metav1.APIResource{{Name: "clusters"}}}}
	if f := New(cs).DatabaseOperatorCheck(); !f.OK {
		t.Fatalf("operator served: %+v", f)
	}
	// Its permission is asked only of a plan that uses it.
	denied := cluster("37", "create clusters")
	if f := find(Preflight(ctx, New(denied), false, "", nil, Needs{}), "permissions"); !f.OK {
		t.Fatalf("plain plan: %+v", f)
	}
	if f := find(Preflight(ctx, New(denied), false, "", nil, Needs{DatabaseCluster: true}), "permissions"); f.OK || !strings.Contains(f.Detail, "clusters") {
		t.Fatalf("clustered plan: %+v", f)
	}
}
