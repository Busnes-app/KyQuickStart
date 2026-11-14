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
)

// The CloudNativePG release the test installs, as a cluster's owner would.
const (
	databaseOperator       = "https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v1.30.1/cnpg-1.30.1.yaml"
	databaseOperatorSHA256 = "37237f145d8138256ea25ae830f87759255665ff08f8d552fdd8224a5ec032fb"
)

// instances are the Cluster's pods that are not being deleted.
func (r *replicaCluster) instances() []corev1.Pod {
	r.t.Helper()
	list, err := r.cs.CoreV1().Pods(replicaNS).List(r.ctx, metav1.ListOptions{LabelSelector: "cnpg.io/cluster=database,cnpg.io/podRole=instance"})
	if err != nil {
		r.t.Fatal(err)
	}
	return slices.DeleteFunc(list.Items, func(p corev1.Pod) bool { return p.DeletionTimestamp != nil })
}

// primary is the instance the operator names as primary.
func (r *replicaCluster) primary() corev1.Pod {
	r.t.Helper()
	for _, p := range r.instances() {
		if p.Labels["cnpg.io/instanceRole"] == "primary" {
			return p
		}
	}
	r.t.Fatal("no primary instance")
	return corev1.Pod{}
}

// sql runs a statement on the primary, as Postgres's own superuser over its local socket.
func (r *replicaCluster) sql(statement string) string {
	r.t.Helper()
	out, err := r.c.Exec(r.ctx, replicaNS, r.primary().Name, "postgres", []string{"psql", "-U", "postgres", "-d", "kyidentity", "-Atc", statement})
	if err != nil {
		r.t.Fatalf("%s: %v", statement, err)
	}
	return strings.TrimSpace(string(out))
}

// TestKubernetesIdentityDatabaseCluster installs KyIdentity as replicas on a highly available
// Postgres, on a kind cluster with three workers and the CloudNativePG operator, then powers
// off the node the primary runs on. It needs the image TestKubernetesIdentityReplicas needs.
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

	kubectl(fetchPinned(t, databaseOperator, databaseOperatorSHA256), "apply", "--server-side", "-f", "-")
	kubectl(nil, "-n", "cnpg-system", "rollout", "status", "deployment/cnpg-controller-manager", "--timeout=300s")
	if f := connected.DatabaseOperatorCheck(); !f.OK {
		t.Fatalf("preflight with the operator: %+v", f)
	}

	started := time.Now()
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
	if again := r.pods(); !slices.Equal(again, pods) {
		t.Fatalf("pods changed: %v -> %v", pods, again)
	}

	// The primary's node loses power. The KyIdentity pods on the other nodes sign people in
	// again without anything being restarted, and no session made before is lost.
	primary := r.primary()
	lost := primary.Spec.NodeName
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
	if len(survivors) < 2 {
		t.Fatalf("KyIdentity pods off node %s: %v", lost, survivors)
	}
	docker(t, "kill", lost)
	powerOff := time.Now()
	t.Cleanup(func() { exec.Command("docker", "start", lost).Run() })
	// signIn fails the test on a refusal, so ask each pod whether it is ready first.
	r.eventually("a surviving pod is ready on the new primary", func() bool {
		return strings.Contains(r.http(survivors[0], "/readyz", nil, ""), "HTTP/1.1 200") && r.primary().Name != primary.Name
	})
	t.Logf("sign-in was back %s after the primary's node lost power", time.Since(powerOff).Round(time.Second))
	after := r.signIn(survivors[0], password)
	for _, pod := range survivors {
		for _, session := range append(slices.Collect(func(yield func(string) bool) {
			for _, s := range sessions {
				if !yield(s) {
					return
				}
			}
		}), after) {
			if !r.signedIn(pod, session) {
				t.Fatalf("%s refuses a session after the failover", pod)
			}
		}
	}
	if got := r.sql("select count(*) from users"); got != "1" {
		t.Fatalf("the new primary has %s administrators", got)
	}

	// Power returns: the old primary rejoins as a standby, and the Cluster is whole again.
	docker(t, "start", lost)
	r.eventually("three instances are ready again", func() bool {
		_, err := r.run(kube.Steps(a)[3:])
		return err == nil && len(r.pods()) == 3
	})
	if r.primary().Name == primary.Name {
		t.Fatal("the failed primary came back as the primary")
	}
	pods = r.pods()

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
}
