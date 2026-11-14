//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
)

const operatorNS = "kyq-cloudnative-pg"

// instances are the Cluster's pods that are not being deleted.
func (r *replicaCluster) instances() []corev1.Pod {
	r.t.Helper()
	list, err := r.cs.CoreV1().Pods(replicaNS).List(r.ctx, metav1.ListOptions{LabelSelector: "cnpg.io/cluster=database,cnpg.io/podRole=instance"})
	if err != nil {
		r.t.Fatal(err)
	}
	return slices.DeleteFunc(list.Items, func(p corev1.Pod) bool { return p.DeletionTimestamp != nil })
}

// primary is the running instance the operator names as primary; "" during a failover.
func (r *replicaCluster) primary() string {
	r.t.Helper()
	for _, p := range r.instances() {
		if p.Labels["cnpg.io/instanceRole"] == "primary" && p.Status.Phase == corev1.PodRunning {
			return p.Name
		}
	}
	return ""
}

// sql runs a statement on the primary, as Postgres's own superuser over its local socket.
func (r *replicaCluster) sql(statement string) string {
	r.t.Helper()
	out, err := r.c.Exec(r.ctx, replicaNS, r.primary(), "postgres", []string{"psql", "-U", "postgres", "-d", "kyidentity", "-Atc", statement})
	if err != nil {
		r.t.Fatalf("%s: %v", statement, err)
	}
	return strings.TrimSpace(string(out))
}

// TestKubernetesIdentityDatabaseCluster installs KyIdentity as replicas on a highly available
// Postgres, on a kind cluster with three workers and the catalog's CloudNativePG operator,
// which it installs first, then powers off the node the primary runs on. It needs the image TestKubernetesIdentityReplicas needs.
func TestKubernetesIdentityDatabaseCluster(t *testing.T) {
	image := os.Getenv("KYQ_TEST_IDENTITY_REPLICA_IMAGE")
	if image == "" {
		t.Skip("KYQ_TEST_IDENTITY_REPLICA_IMAGE names no KyIdentity image with replica support")
	}
	ctx := context.Background()
	work := t.TempDir()
	cluster, kc := kindClusterNodes(t, work, 3)
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	cs := kubernetes.NewForConfigOrDie(cfg)
	connected, err := kube.Connect(kc, "")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	app := cat["kyidentity"]
	app.Kubernetes = app.Kubernetes.WithHost(catalogIdentityHost)
	app.Images = map[string]string{"kyidentity": image, "postgres_cluster": app.Images[app.Kubernetes.Replicas.ClusterImage]}
	kind(t, "load", "docker-image", image, "--name", cluster)
	r := &replicaCluster{t: t, ctx: ctx, cs: cs, c: connected, app: app}
	kubectl := func(stdin []byte, args ...string) string {
		t.Helper()
		cmd := exec.Command("docker", append([]string{"exec", "-i", cluster + "-control-plane", "kubectl", "--kubeconfig", "/etc/kubernetes/admin.conf"}, args...)...)
		cmd.Stdin = bytes.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("kubectl %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("logs:\n%s", r.logs())
			t.Logf("cluster:\n%s", kubectl(nil, "-n", replicaNS, "get", "cluster,pods,pvc", "-o", "wide"))
			t.Logf("operator:\n%s", kubectl(nil, "-n", operatorNS, "get", "pods,deployments", "-o", "wide"))
			events, _ := cs.CoreV1().Events(replicaNS).List(context.Background(), metav1.ListOptions{})
			for _, e := range events.Items {
				t.Logf("event: %s %s: %s", e.InvolvedObject.Name, e.Reason, e.Message)
			}
		}
	})
	plan := func(replicas, instances int) kube.App {
		a := r.plan(replicas, "")
		k := *a.Catalog.Kubernetes
		k.DatabaseInstances = instances
		a.Catalog.Kubernetes = &k
		return a
	}
	a := plan(3, 3)

	// Without the operator the plan is refused before anything of the database exists.
	if f := connected.DatabaseOperatorCheck(); f.OK || !strings.Contains(f.Detail, "CloudNativePG") {
		t.Fatalf("preflight without the operator: %+v", f)
	}
	if _, err := r.run(kube.Steps(a)); err == nil || !strings.Contains(err.Error(), "CloudNativePG") {
		t.Fatalf("apply without the operator: %v", err)
	}
	if len(r.deployments()) != 0 {
		t.Fatal("KyIdentity was deployed without a database")
	}

	// The catalog's operator, from its project's manifest: in the installer's own namespace,
	// under the restricted profile and a policy that admits its webhook port alone.
	op := kube.App{Name: "cloudnative-pg", Client: connected, Catalog: cat["cloudnative-pg"], ReleaseSet: "e2e", Redact: &engine.Redactor{}}
	if f := connected.OperatorCheck(ctx); !f.OK {
		t.Fatalf("preflight before the operator: %+v", f)
	}
	started := time.Now()
	if applied, err := r.run(kube.Steps(op)); err != nil {
		t.Fatalf("operator: %v after %v", err, applied)
	}
	t.Logf("installing the operator took %s", time.Since(started).Round(time.Second))
	if f := connected.DatabaseOperatorCheck(); !f.OK {
		t.Fatalf("preflight with the operator: %+v", f)
	}
	if f := connected.OperatorCheck(ctx); !f.OK {
		t.Fatalf("preflight over the installer's operator: %+v", f)
	}
	ns, err := cs.CoreV1().Namespaces().Get(ctx, operatorNS, metav1.GetOptions{})
	if err != nil || ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Fatalf("operator namespace: %v, %v", ns.Labels, err)
	}
	if _, err := cs.CoreV1().Namespaces().Get(ctx, "cnpg-system", metav1.GetOptions{}); err == nil {
		t.Fatal("the manifest's own namespace was created")
	}
	operators, err := cs.CoreV1().Pods(operatorNS).List(ctx, metav1.ListOptions{})
	if err != nil || len(operators.Items) != 1 || !strings.Contains(operators.Items[0].Spec.Containers[0].Image, "@sha256:") {
		t.Fatalf("operator pods: %v, %v", operators.Items, err)
	}
	if state, err := connected.State(ctx, op.Name); err != nil || state != "running" {
		t.Fatalf("operator state: %s %v", state, err)
	}
	if applied, err := r.run(kube.Steps(op)); err != nil || len(applied) != 0 {
		t.Fatalf("second apply of the operator: %v, applied %v", err, applied)
	}

	started = time.Now()
	applied, err := r.run(kube.Steps(a))
	if err != nil {
		t.Fatalf("apply: %v after %v", err, applied)
	}
	t.Logf("first apply took %s", time.Since(started).Round(time.Second))
	pods := r.pods()
	if d := r.deployments(); len(d) != 2 || len(pods) != 3 {
		t.Fatalf("deployments = %v, pods = %v", d, pods)
	}

	// Three instances on three nodes, and every commit waits for one of the standbys.
	nodes := map[string]bool{}
	for _, p := range r.instances() {
		nodes[p.Spec.NodeName] = true
	}
	if len(r.instances()) != 3 || len(nodes) != 3 {
		t.Fatalf("instances on nodes %v", nodes)
	}
	if got := r.sql("show synchronous_standby_names"); !strings.HasPrefix(got, "ANY 1 (") {
		t.Fatalf("synchronous_standby_names = %q", got)
	}
	if got := r.sql("select count(*) from pg_stat_replication where sync_state = 'quorum'"); got != "2" {
		t.Fatalf("%s standbys confirm commits", got)
	}

	// The same database as the single pod: a role that owns it and is no superuser, UTF8 and C.
	if got := r.sql("select rolsuper::text || ' ' || rolcreaterole::text || ' ' || rolcreatedb::text from pg_roles where rolname = 'kyidentity'"); got != "false false false" {
		t.Fatalf("the app's role: %s", got)
	}
	if got := r.sql("select pg_get_userbyid(datdba) || ' ' || datcollate || ' ' || pg_encoding_to_char(encoding) from pg_database where datname = 'kyidentity'"); got != "kyidentity C UTF8" {
		t.Fatalf("the app's database: %s", got)
	}
	if got := r.sql("select count(*) from users"); got != "1" {
		t.Fatalf("three pods starting at once created %s administrators", got)
	}
	// Every connection KyIdentity holds is encrypted.
	if got := r.sql("select count(*) filter (where s.ssl) || ' of ' || count(*) from pg_stat_activity a join pg_stat_ssl s using (pid) where a.usename = 'kyidentity'"); strings.HasPrefix(got, "0 ") || strings.Split(got, " of ")[0] != strings.Split(got, " of ")[1] {
		t.Fatalf("encrypted connections: %s", got)
	}

	// One identity, and a session any pod accepts.
	key := r.jwks(pods[0])
	keys, db, tlsSecret := r.secret("kyq-replica-keys"), r.secret("kyq-database"), r.secret("kyq-database-tls")
	password := string(keys["bootstrap-admin-pass"])
	sessions := map[string]string{}
	for _, pod := range pods {
		if r.jwks(pod) != key {
			t.Fatalf("%s signs with another key", pod)
		}
		sessions[pod] = r.signIn(pod, password)
	}
	for _, pod := range pods {
		for _, other := range pods {
			if !r.signedIn(other, sessions[pod]) {
				t.Fatalf("a session made on %s is refused by %s", pod, other)
			}
		}
	}

	// Only the app's pods reach Postgres.
	if r.probe("probe-stranger", map[string]string{}, "database-rw") {
		t.Fatal("a pod that is not the app's reached the database")
	}
	if !r.probe("probe-app", map[string]string{"ky.serves": "kyidentity"}, "database-rw") {
		t.Fatal("the probe cannot reach the database even as the app: the check above proves nothing")
	}

	// The operator's metrics port is closed to the cluster; its webhook port, which the API
	// server calls, is all its namespace admits.
	operatorIP := operators.Items[0].Status.PodIP
	if r.probePort("probe-metrics", map[string]string{}, operatorIP, 8080) {
		t.Fatal("a stranger pod reached the operator's metrics port")
	}
	if !r.probePort("probe-webhook", map[string]string{}, operatorIP, 9443) {
		t.Fatal("the probe cannot reach the webhook port either: the check above proves nothing")
	}

	// No secret in a log, nor in the Cluster or a Deployment.
	logs := r.logs() + kubectl(nil, "-n", replicaNS, "get", "cluster", "database", "-o", "json")
	for _, d := range r.deployments() {
		b, _ := json.Marshal(d)
		logs += string(b)
	}
	for i, s := range []string{password, string(db["password"]), string(db["url"]), string(keys["jwt_rs256.key"]), string(tlsSecret["tls.key"])} {
		if len(s) < 40 || strings.Contains(logs, s) {
			t.Fatalf("secret %d (%d bytes) is in a pod log, the Cluster or a Deployment", i, len(s))
		}
	}

	// A second apply has nothing to do and replaces no pod.
	if applied, err := r.run(kube.Steps(a)); err != nil || len(applied) != 0 {
		t.Fatalf("second apply: %v, applied %v", err, applied)
	}

	// Under a database that runs, the operator is neither stopped nor uninstalled.
	for name, steps := range map[string][]engine.Step{"stop": {kube.StopStep(op)}, "uninstall": kube.RemoveSteps(op)} {
		if _, err := r.run(steps); err == nil || !strings.Contains(err.Error(), replicaNS+"/database") {
			t.Fatalf("%s of the operator under a running database: %v", name, err)
		}
	}
	// Another installer release rewrites every object of the operator in place, definitions
	// included, and the API server still reaches its webhooks: the next write passes them.
	next := op
	next.ReleaseSet = "e2e-next"
	if applied, err := r.run(kube.Steps(next)); err != nil || len(applied) == 0 {
		t.Fatalf("operator under another release: %v, applied %v", err, applied)
	}
	for _, kind := range []string{"validatingwebhookconfiguration/cnpg-validating-webhook-configuration", "mutatingwebhookconfiguration/cnpg-mutating-webhook-configuration"} {
		for _, ca := range strings.Fields(kubectl(nil, "get", kind, "-o", `jsonpath={range .webhooks[*]}{.clientConfig.caBundle}{"\n"}{end}`)) {
			if len(ca) < 100 {
				t.Fatalf("%s lost the operator's certificate authority", kind)
			}
		}
		if n := len(strings.Fields(kubectl(nil, "get", kind, "-o", `jsonpath={range .webhooks[*]}{.clientConfig.caBundle}{"\n"}{end}`))); n < 4 {
			t.Fatalf("%s: %d webhooks carry a certificate authority", kind, n)
		}
	}
	kubectl(nil, "-n", replicaNS, "annotate", "cluster", "database", "kyquickstart.test/webhook=passed")
	if len(r.instances()) != 3 {
		t.Fatal("rewriting the operator disturbed the database")
	}
	op = next
	if again := r.pods(); !slices.Equal(again, pods) {
		t.Fatalf("pods changed: %v -> %v", pods, again)
	}

	// The primary's pod is lost. The operator promotes a standby or restarts it, the pods find
	// the primary again by themselves, and the Cluster is whole afterwards.
	first := r.primary()
	old := map[string]bool{}
	for _, p := range r.instances() {
		old[string(p.UID)] = p.Name == first
	}
	if err := cs.CoreV1().Pods(replicaNS).Delete(ctx, first, metav1.DeleteOptions{GracePeriodSeconds: ptr.To(int64(0))}); err != nil {
		t.Fatal(err)
	}
	// The Cluster's status trails its pods, so wait on the pods themselves: three ready, none
	// of them the one deleted.
	r.eventually("three instances are ready again and every pod accepts the earlier sessions", func() bool {
		ready := 0
		for _, p := range r.instances() {
			if !old[string(p.UID)] && slices.ContainsFunc(p.Status.Conditions, func(c corev1.PodCondition) bool {
				return c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue
			}) {
				ready++
			}
		}
		return ready == 3 && r.primary() != "" && kube.Steps(a)[3].Verify(ctx) == nil && slices.Equal(r.pods(), pods) &&
			!slices.ContainsFunc(pods, func(pod string) bool { return !r.signedIn(pod, sessions[pods[1]]) })
	})
	if got := r.sql("select count(*) from users"); got != "1" {
		t.Fatalf("after the primary's pod was lost there are %s administrators", got)
	}
	after := r.signIn(pods[0], password)

	// Stop and start: nothing runs in between, and the data is there afterwards.
	if _, err := r.run([]engine.Step{kube.StopStep(a)}); err != nil {
		t.Fatal(err)
	}
	if left, db := r.pods(), r.instances(); len(left) != 0 || len(db) != 0 {
		t.Fatalf("after stop: pods %v, %d instances", left, len(db))
	}
	if state, err := connected.State(ctx, "kyidentity"); err != nil || state != "stopped" {
		t.Fatalf("state after stop: %s %v", state, err)
	}
	// With every database hibernated the operator may go. Its definitions stay, and with
	// them the Cluster and its claims; a reinstall picks the database up again.
	if _, err := r.run(kube.RemoveSteps(op)); err != nil {
		t.Fatalf("uninstall the operator: %v", err)
	}
	if left, err := cs.CoreV1().Pods(operatorNS).List(ctx, metav1.ListOptions{}); err != nil || len(left.Items) != 0 {
		t.Fatalf("operator pods after uninstall: %v, %v", left.Items, err)
	}
	kubectl(nil, "-n", replicaNS, "get", "cluster", "database")
	if held, err := cs.CoreV1().PersistentVolumeClaims(replicaNS).List(ctx, metav1.ListOptions{LabelSelector: "cnpg.io/cluster=database"}); err != nil || len(held.Items) != 3 {
		t.Fatalf("uninstalling the operator left %d of the database's claims: %v", len(held.Items), err)
	}
	if _, err := r.run(kube.Steps(op)); err != nil {
		t.Fatalf("reinstall the operator: %v", err)
	}
	if _, err := r.run([]engine.Step{kube.StartStep(a)}); err != nil {
		t.Fatal(err)
	}
	pods = r.pods()
	if state, _ := connected.State(ctx, "kyidentity"); state != "running" || len(pods) != 3 || !r.signedIn(pods[0], after) {
		t.Fatalf("state after start: %s, pods %v", state, pods)
	}

	// Uninstall keeps the Cluster, its claims and the Secrets; a reinstall is the same identity.
	if _, err := r.run(kube.RemoveSteps(a)); err != nil {
		t.Fatal(err)
	}
	if left := r.deployments(); len(left) != 0 || len(r.instances()) != 0 {
		t.Fatalf("after uninstall: deployments %v", left)
	}
	claims, err := cs.CoreV1().PersistentVolumeClaims(replicaNS).List(ctx, metav1.ListOptions{LabelSelector: "cnpg.io/cluster=database"})
	if err != nil || len(claims.Items) != 3 {
		t.Fatalf("uninstall left %d of the database's claims: %v", len(claims.Items), err)
	}
	if _, err := r.run(kube.Steps(a)); err != nil {
		t.Fatal(err)
	}
	pods = r.pods()
	if len(pods) != 3 || r.jwks(pods[1]) != key || !r.signedIn(pods[0], after) {
		t.Fatal("the reinstall is another identity, or lost its data")
	}

	// The installer moves no database: not to the single pod, not to one instance on SQLite.
	if _, err := r.run(kube.Steps(plan(3, 0))); err == nil || !strings.Contains(err.Error(), "keeps its accounts in the installer's highly available Postgres") {
		t.Fatalf("onto the single pod: %v", err)
	}
	if _, err := r.run(kube.Steps(plan(0, 0))); err == nil || !strings.Contains(err.Error(), "installed as replicas on Postgres") {
		t.Fatalf("back to a single instance: %v", err)
	}
	if pods = r.pods(); len(pods) != 3 || len(r.instances()) != 3 || !r.signedIn(pods[2], after) {
		t.Fatal("the refusal changed the install")
	}

	// Last, the primary's node loses power and stays off. The KyIdentity pods on the other
	// nodes sign people in again without anything being restarted, on the two instances
	// left, and no session made before is lost. (A kind node does not rejoin after a power
	// cut, so the node's return is not part of this test.)
	primary := r.primary()
	var lost string
	for _, p := range r.instances() {
		if p.Name == primary {
			lost = p.Spec.NodeName
		}
	}
	var survivors []string
	list, err := cs.CoreV1().Pods(replicaNS).List(ctx, metav1.ListOptions{LabelSelector: "ky.serves=kyidentity"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list.Items {
		if p.Spec.NodeName != lost {
			survivors = append(survivors, p.Name)
		}
	}
	if lost == "" || len(survivors) == 0 {
		t.Fatalf("primary %s on node %q; KyIdentity pods elsewhere: %v", primary, lost, survivors)
	}
	// The worst case: the operator, which is what fails the database over, is on that node
	// too, and must first leave it.
	workers, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range workers.Items {
		if n.Name != lost {
			kubectl(nil, "cordon", n.Name)
		}
	}
	if err := cs.CoreV1().Pods(operatorNS).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	r.eventually("the operator runs on the primary's node", func() bool {
		on, err := cs.CoreV1().Pods(operatorNS).List(ctx, metav1.ListOptions{})
		state, _ := connected.State(ctx, op.Name)
		return err == nil && len(on.Items) == 1 && on.Items[0].Spec.NodeName == lost && state == "running"
	})
	for _, n := range workers.Items {
		if n.Name != lost {
			kubectl(nil, "uncordon", n.Name)
		}
	}
	docker(t, "kill", lost)
	powerOff := time.Now()
	// signIn fails the test on a refusal, so ask the pod whether it is ready first.
	r.eventually("a surviving pod is ready on a new primary", func() bool {
		now := r.primary()
		return now != "" && now != primary && strings.Contains(r.http(survivors[0], "/readyz", nil, ""), "HTTP/1.1 200")
	})
	t.Logf("sign-in was back %s after the primary's node lost power", time.Since(powerOff).Round(time.Second))
	fresh := r.signIn(survivors[0], password)
	for _, pod := range survivors {
		for _, session := range []string{after, fresh} {
			if !r.signedIn(pod, session) {
				t.Fatalf("%s refuses a session after the failover", pod)
			}
		}
	}
	if got := r.sql("select count(*) from users"); got != "1" {
		t.Fatalf("the new primary has %s administrators", got)
	}
}
