//go:build e2e

package e2e

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// cluster17 is the CloudNativePG operand image earlier releases ran.
const cluster17 = "ghcr.io/cloudnative-pg/postgresql:17.12-minimal-trixie@sha256:4ae89311e5031155a139a5c6d31a80f6deb135443711e61ff7df41cb549fdcaa"

// TestKubernetesIdentityDatabaseClusterUpgrade installs KyIdentity on a Cluster of three
// PostgreSQL 17 instances as an earlier release did, seeds it, and applies this release: the
// Cluster is copied and upgraded to 18 in place with its rows, roles and passwords. Then it
// goes back to 17 from the primary's copy, step by step as docs/guided-setup.md says.
func TestKubernetesIdentityDatabaseClusterUpgrade(t *testing.T) {
	ctx := context.Background()
	cat, err := catalog.Load(os.DirFS(relaysOffCatalog(t)))
	if err != nil {
		t.Fatal(err)
	}
	app := cat["kyidentity"]
	image := os.Getenv("KYQ_TEST_IDENTITY_REPLICA_IMAGE")
	if image == "" {
		image = app.Images[app.Kubernetes.Image]
	}
	cluster, kc := kindClusterNodes(t, t.TempDir(), 3)
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	cs := kubernetes.NewForConfigOrDie(cfg)
	connected, err := kube.Connect(kc, "")
	if err != nil {
		t.Fatal(err)
	}
	app.Kubernetes = app.Kubernetes.WithHost(catalogIdentityHost)
	cluster18 := app.Images[app.Kubernetes.Replicas.ClusterImage]
	app.Images = map[string]string{"kyidentity": image, "postgres_cluster": cluster18}
	kindLoad(t, cluster, image)
	r := &replicaCluster{t: t, ctx: ctx, cs: cs, c: connected, app: app}
	kubectl := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("docker", append([]string{"exec", "-i", cluster + "-control-plane", "kubectl", "--kubeconfig", "/etc/kubernetes/admin.conf"}, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("kubectl %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("logs:\n%s", r.logs())
			t.Logf("cluster:\n%s", kubectl("", "-n", replicaNS, "get", "cluster,pods,pvc,jobs", "-o", "wide"))
			t.Logf("describe:\n%s", kubectl("", "-n", replicaNS, "describe", "cluster", "database"))
			t.Logf("operator:\n%s", kubectl("", "-n", operatorNS, "logs", "deploy/cnpg-controller-manager", "--tail", "80"))
		}
	})
	op := kube.App{Name: "cloudnative-pg", Client: connected, Catalog: cat["cloudnative-pg"], ReleaseSet: "e2e", Redact: &engine.Redactor{}}
	if applied, err := r.run(kube.Steps(op)); err != nil {
		t.Fatalf("operator: %v after %v", err, applied)
	}
	plan := func(clusterImage string) kube.App {
		a := r.plan(3, "")
		k := *a.Catalog.Kubernetes
		k.DatabaseInstances = 3
		a.Catalog.Kubernetes = &k
		a.Catalog.Images = maps.Clone(a.Catalog.Images)
		a.Catalog.Images["postgres_cluster"] = clusterImage
		return a
	}
	version := func() string { return r.sql("show server_version_num")[:2] }
	// login connects as role over the network to the primary's Service, with password.
	login := func(role, password string) (string, error) {
		out, err := r.c.Exec(ctx, replicaNS, r.primary(), "postgres", []string{"env", "PGPASSWORD=" + password, "PGCONNECT_TIMEOUT=10", "psql",
			"host=database-rw." + replicaNS + ".svc user=" + role + " dbname=kyidentity sslmode=require", "-Atc", "select count(*) from upgrade_check"})
		return strings.TrimSpace(string(out)), err
	}
	checkData := func(when, rows string) {
		t.Helper()
		if got := r.sql("select count(*) from upgrade_check"); got != rows {
			t.Fatalf("%s: %s rows", when, got)
		}
		if got, err := login("reader", "reader-pw"); err != nil || got != rows {
			t.Fatalf("%s: reader's password: %q %v", when, got, err)
		}
		if _, err := login("reader", "wrong-pw"); err == nil {
			t.Fatalf("%s: a wrong password is accepted: the check above proves nothing", when)
		}
	}

	// The earlier release's install, on 17, with data and a role of its own.
	started := time.Now()
	if applied, err := r.run(kube.Steps(plan(cluster17))); err != nil {
		t.Fatalf("install on 17: %v after %v", err, applied)
	}
	t.Logf("install on 17 took %s", time.Since(started).Round(time.Second))
	if v := version(); v != "17" {
		t.Fatalf("installed on PostgreSQL %s", v)
	}
	r.sql("create table upgrade_check(n int primary key, s text collate \"C\"); insert into upgrade_check select g, md5(g::text) from generate_series(1, 1000) g; create role reader login password 'reader-pw'; grant select on upgrade_check to reader")
	pods := r.pods()
	password := string(r.secret("kyq-replica-keys")["bootstrap-admin-pass"])
	session := r.signIn(pods[0], password)
	checkData("on 17", "1000")
	primary := r.primary()

	// This release: copied, then upgraded in place.
	a := plan(cluster18)
	started = time.Now()
	applied, err := r.run(kube.Steps(a))
	if err != nil {
		t.Fatalf("upgrade: %v after %v", err, applied)
	}
	t.Logf("the upgrade took %s, steps %v", time.Since(started).Round(time.Second), applied)
	for _, s := range kube.Steps(a) {
		if n, ok := s.(engine.Noter); ok && s.ID() == "kyidentity.database-upgrade" {
			t.Logf("note: %s", n.Note())
		}
	}
	if v := version(); v != "18" {
		t.Fatalf("after the upgrade: PostgreSQL %s", v)
	}
	checkData("on 18", "1000")
	for _, pod := range r.pods() {
		if !r.signedIn(pod, session) {
			t.Fatalf("%s refuses a session made on 17", pod)
		}
	}
	copies, err := cs.CoreV1().PersistentVolumeClaims(replicaNS).List(ctx, metav1.ListOptions{LabelSelector: "kyquickstart/postgres=17"})
	if err != nil || len(copies.Items) != 3 {
		t.Fatalf("copies: %v %v", copies, err)
	}
	var primaryCopy string
	for _, c := range copies.Items {
		if c.Annotations["kyquickstart/pre-upgrade"] != "verified" || c.Annotations["kyquickstart/copied-at"] == "" {
			t.Fatalf("copy %s: %v", c.Name, c.Annotations)
		}
		if c.Labels["kyquickstart/instance-role"] == "primary" {
			primaryCopy = c.Name
		}
	}
	if primaryCopy != "pre-upgrade-pg17-"+primary {
		t.Fatalf("the primary's copy is %q, the primary was %s", primaryCopy, primary)
	}
	if applied, err := r.run(kube.Steps(a)); err != nil || len(applied) != 0 {
		t.Fatalf("second apply: %v, applied %v", err, applied)
	}
	r.sql("insert into upgrade_check values (1001, 'written on 18')")

	// Back to 17 from the primary's copy, as docs/guided-setup.md "Going back to PostgreSQL 17" says.
	pv := kubectl("", "-n", replicaNS, "get", "pvc", primaryCopy, "-o", "jsonpath={.spec.volumeName}")
	class := kubectl("", "-n", replicaNS, "get", "pvc", primaryCopy, "-o", "jsonpath={.spec.storageClassName}")
	size := kubectl("", "-n", replicaNS, "get", "pvc", primaryCopy, "-o", "jsonpath={.spec.resources.requests.storage}")
	kubectl("", "patch", "pv", pv, "-p", `{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}`)
	kubectl("", "-n", replicaNS, "delete", "cluster", "database", "--wait")
	r.eventually("the 18 Cluster's claims are gone", func() bool {
		left, err := cs.CoreV1().PersistentVolumeClaims(replicaNS).List(ctx, metav1.ListOptions{LabelSelector: "cnpg.io/cluster=database"})
		return err == nil && len(left.Items) == 0 && len(r.instances()) == 0
	})
	kubectl("", "-n", replicaNS, "delete", "pvc", primaryCopy, "--wait")
	kubectl("", "patch", "pv", pv, "--type", "json", "-p", `[{"op":"remove","path":"/spec/claimRef"}]`)
	kubectl(`apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: database-1
  namespace: `+replicaNS+`
  labels:
    cnpg.io/cluster: database
    cnpg.io/instanceName: database-1
    cnpg.io/instanceRole: primary
    role: primary
    cnpg.io/pvcRole: PG_DATA
  annotations:
    cnpg.io/nodeSerial: "1"
    cnpg.io/pvcStatus: ready
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: `+class+`
  volumeName: `+pv+`
  resources:
    requests:
      storage: `+size+`
`, "apply", "-f", "-")
	// The previous release's apply makes the Cluster on 17, which adopts the claim.
	started = time.Now()
	if applied, err := r.run(kube.Steps(plan(cluster17))); err != nil {
		t.Fatalf("back on 17: %v after %v", err, applied)
	}
	t.Logf("the rollback took %s", time.Since(started).Round(time.Second))
	if v := version(); v != "17" {
		t.Fatalf("after the rollback: PostgreSQL %s", v)
	}
	checkData("back on 17", "1000")
	r.eventually("KyIdentity accepts the session made on 17 again", func() bool {
		pods := r.pods()
		return len(pods) == 3 && r.signedIn(pods[0], session)
	})
	if n := len(r.instances()); n != 3 {
		t.Fatalf("%d instances after the rollback", n)
	}
}
