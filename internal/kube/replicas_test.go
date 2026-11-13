package kube

import (
	"context"
	"encoding/json"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// replicatedApp is the catalog KyIdentity as a plan with replicas places it. generated records
// the key generator's pods.
func replicatedApp(t *testing.T, count int, databaseSecret string) (App, *fake.Clientset, *[]*corev1.Pod) {
	t.Helper()
	c := kyidentityApp(t)
	k := *c.Kubernetes
	k.ReplicaCount, k.DatabaseSecret = count, databaseSecret
	c.Kubernetes = &k
	cs := fake.NewClientset()
	client := New(cs)
	var pods []*corev1.Pod
	cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pod := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
		pod.Name = pod.GenerateName + "fixture"
		pod.UID = types.UID("fixture-" + pod.GenerateName)
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		pods = append(pods, pod)
		return true, pod, cs.Tracker().Create(corev1.SchemeGroupVersion.WithResource("pods"), pod, pod.Namespace)
	})
	client.execCommand = func(ctx context.Context, namespace, pod, container string, args []string) ([]byte, error) {
		if strings.HasPrefix(pod, "kyq-keys-") && args[0] == "cat" {
			return []byte("key of " + path.Base(args[1])), nil
		}
		return (remote.Local{}).Run(ctx, args[2], nil)
	}
	return App{Name: "kyidentity", Client: client, Catalog: c, ReleaseSet: "rs-1", Redact: &engine.Redactor{}}, cs, &pods
}

func envOf(c corev1.Container) map[string]corev1.EnvVar {
	out := map[string]corev1.EnvVar{}
	for _, e := range c.Env {
		out[e.Name] = e
	}
	return out
}

func TestSingleInstanceRendersAsBefore(t *testing.T) {
	c := kyidentityApp(t)
	with := render(App{Name: "kyidentity", Catalog: c, ReleaseSet: "rs-1"})
	k := *c.Kubernetes
	k.Replicas = nil
	c.Kubernetes = &k
	if without := render(App{Name: "kyidentity", Catalog: c, ReleaseSet: "rs-1"}); with.hash() != without.hash() || with.Replica != nil || with.Database != nil {
		t.Fatal("the replicas block changes a single instance")
	}
}

func TestRenderReplicas(t *testing.T) {
	a, _, _ := replicatedApp(t, 3, "")
	a.Catalog.Kubernetes.RecoveryKeyID = strings.Repeat("ab", 32)
	o := render(a)
	if *o.Deployment.Spec.Replicas != 1 || o.Deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("app = %+v", o.Deployment.Spec)
	}
	r := o.Replica
	if *r.Spec.Replicas != 2 || r.Spec.Strategy.RollingUpdate.MaxUnavailable.IntValue() != 0 || r.Name != replicaName {
		t.Fatalf("replicas = %+v", r.Spec)
	}
	for _, d := range []*appsv1.Deployment{o.Deployment, r} {
		pod := d.Spec.Template
		if pod.Labels[servesLabel] != "kyidentity" || !owned(&pod) || pod.Labels[nameLabel] != d.Spec.Selector.MatchLabels[nameLabel] {
			t.Fatalf("%s labels = %v", d.Name, pod.Labels)
		}
		c := pod.Spec.Containers[0]
		env := envOf(c)
		for name, want := range map[string]string{
			"KYIDENTITY_REPLICATED":          "true",
			"KYIDENTITY_DATABASE_URL_FILE":   "/run/kyq-database/url",
			"KYIDENTITY_SECRET_KEY_FILE":     "/run/kyq-keys/secret.key",
			"KYIDENTITY_ENCRYPTION_KEY_FILE": "/run/kyq-keys/encryption.key",
			"KYIDENTITY_RSA_KEY_PATH":        "/run/kyq-keys/jwt_rs256.key",
			"KYIDENTITY_VAPID_KEY_PATH":      "/run/kyq-keys/push-vapid-private.key",
			"KYIDENTITY_RECOVERY_KEY_ID":     strings.Repeat("ab", 32),
		} {
			if env[name].Value != want {
				t.Errorf("%s: %s = %q", d.Name, name, env[name].Value)
			}
		}
		// The password is a reference, never a value in the object or its hash.
		if p := env["BOOTSTRAP_ADMIN_PASS"]; p.Value != "" || p.ValueFrom.SecretKeyRef.Name != keysSecret || p.ValueFrom.SecretKeyRef.Key != bootstrapKey {
			t.Errorf("%s: bootstrap password = %+v", d.Name, p)
		}
		if !slices.IsSortedFunc(c.Env, func(x, y corev1.EnvVar) int { return strings.Compare(x.Name, y.Name) }) {
			t.Errorf("%s: env is not sorted, so the hash is not stable", d.Name)
		}
		for _, v := range pod.Spec.Volumes {
			if v.Secret == nil {
				continue
			}
			if *v.Secret.DefaultMode != 0o440 {
				t.Errorf("%s: secret volume %s mode %o", d.Name, v.Name, *v.Secret.DefaultMode)
			}
			// The keys volume projects the four key files only, not the bootstrap password.
			if v.Name == "replica-keys" && (len(v.Secret.Items) != 4 || slices.ContainsFunc(v.Secret.Items, func(i corev1.KeyToPath) bool { return i.Key == bootstrapKey })) {
				t.Errorf("%s: keys volume projects %v", d.Name, v.Secret.Items)
			}
		}
	}
	claims := func(d *appsv1.Deployment) (n int) {
		for _, v := range d.Spec.Template.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				n++
			}
		}
		return n
	}
	if claims(o.Deployment) != 1 || claims(r) != 0 {
		t.Fatalf("claims: app %d, replicas %d", claims(o.Deployment), claims(r))
	}
	if o.Service.Spec.Selector[servesLabel] != "kyidentity" || len(o.Service.Spec.Selector) != 1 {
		t.Fatalf("service selects %v", o.Service.Spec.Selector)
	}

	db := o.Database.Spec.Template.Spec
	c := db.Containers[0]
	sc := c.SecurityContext
	if *o.Database.Spec.Replicas != 1 || !*sc.RunAsNonRoot || !*sc.ReadOnlyRootFilesystem || *sc.AllowPrivilegeEscalation || sc.Capabilities.Drop[0] != "ALL" ||
		*db.AutomountServiceAccountToken || c.Image != a.Catalog.Images["postgres"] {
		t.Fatalf("database pod = %+v", c)
	}
	script := c.Command[2]
	for _, want := range []string{"CREATE ROLE kyidentity LOGIN PASSWORD :'pw';", "ENCODING 'UTF8' LOCALE 'C' TEMPLATE template0", "SET log_min_error_statement = panic;", "\\set pw `cat /run/secrets/password`"} {
		if !strings.Contains(script, want) {
			t.Errorf("database command lacks %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "SUPERUSER") || envOf(c)["POSTGRES_USER"].Value != "" {
		t.Error("the app's role must not be the superuser")
	}
	p := o.DatabasePolicy.Spec
	if len(p.PolicyTypes) != 2 || len(p.Egress) != 0 || len(p.Ingress) != 1 || p.Ingress[0].From[0].PodSelector.MatchLabels[servesLabel] != "kyidentity" ||
		p.Ingress[0].From[0].NamespaceSelector != nil || p.Ingress[0].Ports[0].Port.IntValue() != 5432 || p.PodSelector.MatchLabels[nameLabel] != "kyidentity-database" {
		t.Fatalf("database policy = %+v", p)
	}
	if got := len(o.Claims); got != 2 || o.Claims[1].Name != "vol-database" {
		t.Fatalf("claims = %d", got)
	}
	if b, _ := json.Marshal(o); strings.Contains(string(b), "postgres://") {
		t.Fatal("the rendered objects carry a database URL")
	}

	theirs, _, _ := replicatedApp(t, 2, "my-postgres")
	o = render(theirs)
	if o.Database != nil || o.DatabaseService != nil || o.DatabasePolicy != nil || len(o.Claims) != 1 || *o.Replica.Spec.Replicas != 1 {
		t.Fatal("an operator's database still deploys the installer's")
	}
	var named bool
	for _, v := range o.Replica.Spec.Template.Spec.Volumes {
		named = named || v.Secret != nil && v.Secret.SecretName == "my-postgres" && v.Secret.Items[0].Key == "url"
	}
	if _, set := envOf(o.Replica.Spec.Template.Spec.Containers[0])["KYIDENTITY_RECOVERY_KEY_ID"]; !named || set {
		t.Fatalf("operator's secret mounted: %v; recovery key set without one confirmed: %v", named, set)
	}
}

func secretOf(t *testing.T, cs *fake.Clientset, name string) map[string][]byte {
	t.Helper()
	s, err := cs.CoreV1().Secrets("kyq-kyidentity").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil || !owned(s) {
		t.Fatalf("secret %s: %v", name, err)
	}
	return s.Data
}

func TestReplicaSecretsCreatedOnce(t *testing.T) {
	ctx := context.Background()
	a, cs, pods := replicatedApp(t, 2, "")
	s := replicaSecretsStep{a}
	if done, err := s.Inspect(ctx); err != nil || done {
		t.Fatalf("Inspect before = %v %v", done, err)
	}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	keys, db := secretOf(t, cs, keysSecret), secretOf(t, cs, databaseSecret)
	for _, k := range replicaKeys {
		if string(keys[k.file]) != "key of "+k.file {
			t.Errorf("%s = %q", k.file, keys[k.file])
		}
	}
	if len(keys[bootstrapKey]) != 43 || len(db[passwordKey]) != 43 || len(db[superuserKey]) != 43 || string(db[passwordKey]) == string(db[superuserKey]) {
		t.Fatalf("generated values: %d %d %d", len(keys[bootstrapKey]), len(db[passwordKey]), len(db[superuserKey]))
	}
	want := "postgres://kyidentity:" + string(db[passwordKey]) + "@database.kyq-kyidentity.svc:5432/kyidentity?sslmode=disable"
	if string(db["url"]) != want {
		t.Fatalf("url = %q", db["url"])
	}
	for _, secret := range []string{want, string(db[passwordKey]), string(keys[bootstrapKey]), string(keys["jwt_rs256.key"])} {
		if out := a.Redact.Redact("x " + secret); strings.Contains(out, secret) {
			t.Errorf("a secret is not registered for redaction: %s", out)
		}
	}

	// The generator is a single instance of the app's image on memory, with nothing of the
	// replicated install mounted.
	var gen *corev1.Pod
	for _, p := range *pods {
		if strings.HasPrefix(p.Name, "kyq-keys-") {
			gen = p
		}
	}
	c := gen.Spec.Containers[0]
	env := envOf(c)
	if c.Image != a.Catalog.Images["kyidentity"] || env["KYIDENTITY_DATA_DIR"].Value != keyScratch || env["KYIDENTITY_REPLICATED"].Name != "" || env["KYIDENTITY_DATABASE_URL_FILE"].Name != "" ||
		!*c.SecurityContext.ReadOnlyRootFilesystem || !*c.SecurityContext.RunAsNonRoot || *gen.Spec.AutomountServiceAccountToken || c.ReadinessProbe == nil || !owned(gen) {
		t.Fatalf("generator = %+v", c)
	}
	for _, v := range gen.Spec.Volumes {
		if v.EmptyDir == nil || v.EmptyDir.Medium != corev1.StorageMediumMemory {
			t.Fatalf("generator volume %s is not memory", v.Name)
		}
	}
	if _, err := cs.CoreV1().Pods("kyq-kyidentity").Get(ctx, gen.Name, metav1.GetOptions{}); err == nil {
		t.Fatal("the generator outlived its session")
	}

	// A rerun generates nothing and replaces nothing.
	before := len(*pods)
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if len(*pods) != before || string(secretOf(t, cs, databaseSecret)[passwordKey]) != string(db[passwordKey]) || string(secretOf(t, cs, keysSecret)["secret.key"]) != string(keys["secret.key"]) {
		t.Fatal("a rerun regenerated a value")
	}
	// A Secret that lost a key gets that key back; the others are kept.
	sec, _ := cs.CoreV1().Secrets("kyq-kyidentity").Get(ctx, databaseSecret, metav1.GetOptions{})
	delete(sec.Data, "url")
	cs.CoreV1().Secrets("kyq-kyidentity").Update(ctx, sec, metav1.UpdateOptions{})
	if done, _ := s.Inspect(ctx); done {
		t.Fatal("Inspect passes without the URL")
	}
	if err := s.Apply(ctx); err != nil || string(secretOf(t, cs, databaseSecret)["url"]) != want {
		t.Fatalf("repair: %v", err)
	}
}

func TestOperatorDatabaseSecret(t *testing.T) {
	ctx := context.Background()
	a, cs, _ := replicatedApp(t, 2, "my-postgres")
	s := replicaSecretsStep{a}
	if done, err := s.Inspect(ctx); err != nil || done {
		t.Fatalf("Inspect before = %v %v", done, err)
	}
	// The first apply makes the namespace and the keys, then says what the operator creates.
	err := s.Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "kubectl -n kyq-kyidentity create secret generic my-postgres --from-file=url=") || !strings.Contains(err.Error(), "label secret my-postgres ky.managed-by=kyquickstart") {
		t.Fatalf("apply without the Secret = %v", err)
	}
	if _, err := cs.CoreV1().Namespaces().Get(ctx, "kyq-kyidentity", metav1.GetOptions{}); err != nil {
		t.Fatal("no namespace to put the Secret in")
	}
	if _, err := cs.CoreV1().Secrets("kyq-kyidentity").Get(ctx, databaseSecret, metav1.GetOptions{}); err == nil {
		t.Fatal("the installer's database Secret exists beside the operator's")
	}
	const url = "postgres://ky:hunter2-hunter2@db1.example.com:5432,db2.example.com:5432/kyidentity"
	put := func(labels map[string]string, value string) {
		api := cs.CoreV1().Secrets("kyq-kyidentity")
		api.Delete(ctx, "my-postgres", metav1.DeleteOptions{})
		if _, err := api.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "my-postgres", Labels: labels}, Data: map[string][]byte{"url": []byte(value)}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	put(nil, url)
	if err := s.Apply(ctx); err == nil || !strings.Contains(err.Error(), "not labelled") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("unlabelled Secret = %v", err)
	}
	ours := map[string]string{managed.Label: managed.Value}
	put(ours, "mysql://ky:hunter2-hunter2@db1.example.com/kyidentity")
	if err := s.Apply(ctx); err == nil || !strings.Contains(err.Error(), "postgres:// URL") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("wrong URL = %v", err)
	}
	put(ours, url+"\n")
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if out := a.Redact.Redact("failed: " + url); strings.Contains(out, "hunter2") {
		t.Fatalf("the operator's URL is not redacted: %s", out)
	}
}

// markReady reports every Deployment in the namespace as rolled out.
func markReady(t *testing.T, cs *fake.Clientset) {
	t.Helper()
	ctx := context.Background()
	list, err := cs.AppsV1().Deployments("kyq-kyidentity").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range list.Items {
		n := *d.Spec.Replicas
		d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: n, UpdatedReplicas: n, ReadyReplicas: n, AvailableReplicas: n}
		if _, err := cs.AppsV1().Deployments(d.Namespace).UpdateStatus(ctx, &d, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
}

func replicaCounts(t *testing.T, cs *fake.Clientset) map[string]int32 {
	t.Helper()
	list, err := cs.AppsV1().Deployments("kyq-kyidentity").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int32{}
	for _, d := range list.Items {
		out[d.Name] = *d.Spec.Replicas
	}
	return out
}

func TestReplicatedAppLifecycle(t *testing.T) {
	pollInterval = time.Millisecond
	ctx := context.Background()
	a, cs, _ := replicatedApp(t, 3, "")
	steps := Steps(a)
	if len(steps) != 4 || steps[1].ID() != "kyidentity.replica-secrets" {
		t.Fatalf("steps = %d", len(steps))
	}
	for _, s := range steps[:3] {
		if err := s.Apply(ctx); err != nil {
			t.Fatalf("%s: %v", s.ID(), err)
		}
		if err := s.Verify(ctx); err != nil {
			t.Fatalf("%s: %v", s.ID(), err)
		}
	}
	if got := replicaCounts(t, cs); got["app"] != 1 || got[replicaName] != 2 || got[databaseName] != 1 {
		t.Fatalf("deployed = %v", got)
	}
	for _, name := range []string{appName, databaseName} {
		if _, err := cs.CoreV1().Services("kyq-kyidentity").Get(ctx, name, metav1.GetOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cs.NetworkingV1().NetworkPolicies("kyq-kyidentity").Get(ctx, databaseName, metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}

	// Health waits for the replicas and the database, not only for "app".
	health := steps[3]
	app, _ := cs.AppsV1().Deployments("kyq-kyidentity").Get(ctx, appName, metav1.GetOptions{})
	app.Status = appsv1.DeploymentStatus{ObservedGeneration: app.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
	cs.AppsV1().Deployments("kyq-kyidentity").UpdateStatus(ctx, app, metav1.UpdateOptions{})
	if err := health.Verify(ctx); err == nil || !strings.Contains(err.Error(), "0 of ") {
		t.Fatalf("health with only app ready = %v", err)
	}
	if s, _ := a.Client.State(ctx, a.Name); s != "unknown" {
		t.Fatalf("state with only app ready = %s", s)
	}
	markReady(t, cs)
	if err := health.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := a.Client.State(ctx, a.Name); s != "running" {
		t.Fatalf("state = %s", s)
	}

	// Drift in a replica count is repaired by deploy.
	deploy := steps[2]
	r, _ := cs.AppsV1().Deployments("kyq-kyidentity").Get(ctx, replicaName, metav1.GetOptions{})
	r.Spec.Replicas = new(int32)
	cs.AppsV1().Deployments("kyq-kyidentity").Update(ctx, r, metav1.UpdateOptions{})
	if done, _ := deploy.Inspect(ctx); done {
		t.Fatal("deploy accepts replicas scaled away")
	}
	if err := deploy.Apply(ctx); err != nil || replicaCounts(t, cs)[replicaName] != 2 {
		t.Fatalf("repair: %v", err)
	}

	if err := StopStep(a).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if got := replicaCounts(t, cs); got["app"] != 0 || got[replicaName] != 0 || got[databaseName] != 0 {
		t.Fatalf("stopped = %v", got)
	}
	markReady(t, cs)
	if s, _ := a.Client.State(ctx, a.Name); s != "stopped" {
		t.Fatalf("state after stop = %s", s)
	}
	go func() { // the rollout the start waits for
		for i := 0; i < 200 && ctx.Err() == nil; i++ {
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
	if got := replicaCounts(t, cs); got["app"] != 1 || got[replicaName] != 2 || got[databaseName] != 1 {
		t.Fatalf("started = %v", got)
	}

	if err := RestartStep(a, "run-1").Apply(ctx); err != nil {
		t.Fatal(err)
	}
	list, _ := cs.AppsV1().Deployments("kyq-kyidentity").List(ctx, metav1.ListOptions{})
	for _, d := range list.Items {
		if got := d.Spec.Template.Annotations[restartedAnnotation] == "run-1"; got != (d.Name != databaseName) {
			t.Errorf("restart of %s = %v", d.Name, got)
		}
	}

	// Uninstall removes all three workloads and keeps the claims and Secrets.
	rm := RemoveSteps(a)[0]
	if err := rm.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if got := replicaCounts(t, cs); len(got) != 0 {
		t.Fatalf("left after uninstall: %v", got)
	}
	if _, err := cs.CoreV1().Services("kyq-kyidentity").Get(ctx, databaseName, metav1.GetOptions{}); err == nil {
		t.Fatal("database service left")
	}
	secretOf(t, cs, keysSecret)
	secretOf(t, cs, databaseSecret)
	for _, claim := range []string{"vol-data", "vol-database"} {
		if _, err := cs.CoreV1().PersistentVolumeClaims("kyq-kyidentity").Get(ctx, claim, metav1.GetOptions{}); err != nil {
			t.Fatalf("claim %s: %v", claim, err)
		}
	}
	// A reinstall as replicas finds its keys and carries on.
	if err := deploy.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := UnmanageSteps(a)[0].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{keysSecret, databaseSecret} {
		if s, _ := cs.CoreV1().Secrets("kyq-kyidentity").Get(ctx, name, metav1.GetOptions{}); owned(s) {
			t.Errorf("secret %s still managed", name)
		}
	}
	list, _ = cs.AppsV1().Deployments("kyq-kyidentity").List(ctx, metav1.ListOptions{})
	for _, d := range list.Items {
		if owned(&d) || owned(&d.Spec.Template) {
			t.Errorf("deployment %s still managed", d.Name)
		}
	}
}

// An upgrade stops the replicas of the old release before any pod of the new one starts.
func TestUpgradeStopsOldReplicasFirst(t *testing.T) {
	pollInterval = time.Millisecond
	ctx := context.Background()
	a, cs, _ := replicatedApp(t, 2, "")
	for _, s := range Steps(a)[:3] {
		if err := s.Apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	next := a
	next.Catalog.Images = map[string]string{"kyidentity": "ghcr.io/busnes-app/kyidentity-server:9.9.9@sha256:" + strings.Repeat("0", 64), "postgres": a.Catalog.Images["postgres"]}
	var order []string
	cs.PrependReactor("update", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		d := action.(k8stesting.UpdateAction).GetObject().(*appsv1.Deployment)
		order = append(order, d.Name+" "+d.Spec.Template.Spec.Containers[0].Image[:44]+" x"+string(rune('0'+*d.Spec.Replicas)))
		return false, nil, nil
	})
	if err := Steps(next)[2].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	old, new := a.Catalog.Images["kyidentity"][:44], next.Catalog.Images["kyidentity"][:44]
	want := []string{replicaName + " " + old + " x0", "database " + a.Catalog.Images["postgres"][:44] + " x1", "app " + new + " x1", replicaName + " " + new + " x1"}
	if !slices.Equal(order, want) {
		t.Fatalf("updates = %q\nwant      %q", order, want)
	}
}

func TestReplicaChangeRefused(t *testing.T) {
	ctx := context.Background()
	// A single instance, installed: the plan gains replicas.
	single, cs, _ := replicatedApp(t, 0, "")
	for _, s := range Steps(single) {
		if s.ID() != "kyidentity.health" {
			if err := s.Apply(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	replicated := single
	k := *single.Catalog.Kubernetes
	k.ReplicaCount = 2
	replicated.Catalog.Kubernetes = &k
	for _, s := range Steps(replicated)[1:3] {
		if _, err := s.Inspect(ctx); err == nil || !strings.Contains(err.Error(), "does not yet move it into Postgres") {
			t.Fatalf("%s Inspect = %v", s.ID(), err)
		}
		if err := s.Apply(ctx); err == nil || !strings.Contains(err.Error(), "does not yet move it into Postgres") {
			t.Fatalf("%s Apply = %v", s.ID(), err)
		}
	}
	if _, err := cs.CoreV1().Secrets("kyq-kyidentity").Get(ctx, keysSecret, metav1.GetOptions{}); err == nil {
		t.Fatal("keys were generated beside a single instance's data")
	}
	if got := replicaCounts(t, cs); len(got) != 1 {
		t.Fatalf("deployments = %v", got)
	}

	// Replicas, installed: the plan loses them.
	a, cs, _ := replicatedApp(t, 2, "")
	for _, s := range Steps(a)[:3] {
		if err := s.Apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	back := a
	k = *a.Catalog.Kubernetes
	k.ReplicaCount = 0
	back.Catalog.Kubernetes = &k
	d := Steps(back)[1]
	if err := d.Apply(ctx); err == nil || !strings.Contains(err.Error(), "installed as replicas on Postgres") {
		t.Fatalf("Apply = %v", err)
	}
	if got := replicaCounts(t, cs); got[replicaName] != 1 || got[databaseName] != 1 {
		t.Fatalf("deployments after the refusal = %v", got)
	}
	// Its keys Secret lost while its database remains: never a fresh set of keys.
	if err := cs.CoreV1().Secrets("kyq-kyidentity").Delete(ctx, keysSecret, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := Steps(a)[1].Apply(ctx); err == nil || !strings.Contains(err.Error(), "restore Secret "+keysSecret) {
		t.Fatalf("Apply without the keys = %v", err)
	}
	if _, err := cs.CoreV1().Secrets("kyq-kyidentity").Get(ctx, keysSecret, metav1.GetOptions{}); err == nil {
		t.Fatal("new keys were generated over an existing database")
	}
	if err := PreUpgradeRefusal(a); err == nil || PreUpgradeRefusal(back) != nil {
		t.Fatalf("pre-upgrade copy of replicas = %v", err)
	}
}

func TestReplicasCatalogValidation(t *testing.T) {
	c := kyidentityApp(t)
	if r := c.Kubernetes.Replicas; r == nil || c.Images[r.DatabaseImage] == "" || !catalog.Pinned(c.Images[r.DatabaseImage]) {
		t.Fatalf("replicas = %+v", r)
	}
}
