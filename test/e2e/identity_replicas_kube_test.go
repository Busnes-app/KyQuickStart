//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
)

const replicaNS = "kyq-kyidentity"

// replicaCluster is one kind cluster and the catalog KyIdentity placed on it as replicas.
type replicaCluster struct {
	t   *testing.T
	ctx context.Context
	cs  kubernetes.Interface
	c   *kube.Client
	app catalog.App
}

// plan is the app as a plan with count replicas and that database_secret places it.
func (r *replicaCluster) plan(count int, databaseSecret string) kube.App {
	app := r.app
	k := *app.Kubernetes
	k.ReplicaCount, k.DatabaseSecret = count, databaseSecret
	app.Kubernetes = &k
	return kube.App{Name: "kyidentity", Client: r.c, Catalog: app, ReleaseSet: "e2e", Redact: &engine.Redactor{}}
}

// run takes steps as apply does, and returns those that had work to do.
func (r *replicaCluster) run(steps []engine.Step) (applied []string, err error) {
	for _, s := range steps {
		done, err := s.Inspect(r.ctx)
		if err != nil {
			return applied, err
		}
		if !done {
			applied = append(applied, s.ID())
			if err := s.Apply(r.ctx); err != nil {
				return applied, err
			}
		}
		if err := s.Verify(r.ctx); err != nil {
			return applied, err
		}
	}
	return applied, nil
}

// pods are the running KyIdentity pods, "app"'s first.
func (r *replicaCluster) pods() []string {
	r.t.Helper()
	list, err := r.cs.CoreV1().Pods(replicaNS).List(r.ctx, metav1.ListOptions{LabelSelector: "ky.serves=kyidentity"})
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, p := range list.Items {
		if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
			continue
		}
		if p.Labels["app.kubernetes.io/name"] == "kyidentity" {
			out = append([]string{p.Name}, out...)
		} else {
			out = append(out, p.Name)
		}
	}
	return out
}

// http makes one request inside pod, from its own loopback, and returns status line, headers
// and body. BusyBox wget is what the image has.
func (r *replicaCluster) http(pod, path string, headers map[string]string, body string) string {
	r.t.Helper()
	args := []string{"wget", "-S", "-q", "-O", "-", "-T", "10"}
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range headers {
			if !yield(k) {
				return
			}
		}
	}) {
		args = append(args, "--header", k+": "+headers[k])
	}
	if body != "" {
		args = append(args, "--post-data", body)
	}
	args = append(args, "http://127.0.0.1:5867"+path)
	// wget fails on a 4xx, and exec reports only that a command failed: keep its output.
	out, err := r.c.Exec(r.ctx, replicaNS, pod, "app", append([]string{"/bin/sh", "-c", `"$@" 2>&1 || true`, "wget"}, args...))
	if err != nil {
		r.t.Fatalf("%s %s: %v", pod, path, err)
	}
	return string(out)
}

var setCookie = regexp.MustCompile(`(?m)^\s*Set-Cookie: (kyidentity_[a-z]+)=([^;\s]+)`)

func cookies(reply string) map[string]string {
	out := map[string]string{}
	for _, m := range setCookie.FindAllStringSubmatch(reply, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// signIn signs the bootstrap administrator in on pod and returns the session's Cookie header.
func (r *replicaCluster) signIn(pod, password string) string {
	r.t.Helper()
	csrf := cookies(r.http(pod, "/api/auth/csrf", nil, ""))["kyidentity_csrf"]
	if csrf == "" {
		r.t.Fatalf("%s gave no CSRF cookie", pod)
	}
	login, _ := json.Marshal(map[string]string{"username": "kyq-bootstrap", "password": password})
	reply := r.http(pod, "/api/auth/login", map[string]string{"Content-Type": "application/json", "Cookie": "kyidentity_csrf=" + csrf, "X-CSRF-Token": csrf}, string(login))
	session := cookies(reply)["kyidentity_session"]
	if session == "" {
		r.t.Fatalf("sign-in on %s: %s", pod, strings.ReplaceAll(reply, password, "[password]"))
	}
	return "kyidentity_session=" + session
}

// signedIn reports whether pod accepts the session.
func (r *replicaCluster) signedIn(pod, session string) bool {
	r.t.Helper()
	return strings.Contains(r.http(pod, "/api/auth/me", map[string]string{"Cookie": session}, ""), "HTTP/1.1 200")
}

func (r *replicaCluster) jwks(pod string) string {
	r.t.Helper()
	reply := r.http(pod, "/.well-known/jwks.json", nil, "")
	_, body, _ := strings.Cut(reply, "{")
	var set struct {
		Keys []struct{ Kid, N string }
	}
	if json.Unmarshal([]byte("{"+body), &set) != nil || len(set.Keys) == 0 || set.Keys[0].N == "" {
		r.t.Fatalf("%s JWKS: %s", pod, reply)
	}
	return set.Keys[0].Kid + " " + set.Keys[0].N
}

func (r *replicaCluster) secret(name string) map[string][]byte {
	r.t.Helper()
	s, err := r.cs.CoreV1().Secrets(replicaNS).Get(r.ctx, name, metav1.GetOptions{})
	if err != nil {
		r.t.Fatal(err)
	}
	return s.Data
}

func (r *replicaCluster) deployments() map[string]*appsv1.Deployment {
	r.t.Helper()
	list, err := r.cs.AppsV1().Deployments(replicaNS).List(r.ctx, metav1.ListOptions{})
	if err != nil {
		r.t.Fatal(err)
	}
	out := map[string]*appsv1.Deployment{}
	for i, d := range list.Items {
		out[d.Name] = &list.Items[i]
	}
	return out
}

// eventually polls until ok, for as long as a pod may take to be replaced.
func (r *replicaCluster) eventually(what string, ok func() bool) {
	r.t.Helper()
	for deadline := time.Now().Add(4 * time.Minute); !ok(); time.Sleep(2 * time.Second) {
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting until %s", what)
		}
	}
}

// probe runs BusyBox nc from a pod with those labels in the app's namespace and reports
// whether it reached host:port.
func (r *replicaCluster) probe(name string, labels map[string]string, host string) bool {
	r.t.Helper()
	labels[managed.Label] = managed.Value
	security := &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
	limits := corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi"), corev1.ResourceCPU: resource.MustParse("100m")}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Spec: corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false), TerminationGracePeriodSeconds: ptr.To(int64(0)),
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(65532)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Containers: []corev1.Container{{Name: "probe", Image: kube.CredentialGeneratorImage, SecurityContext: security, Resources: corev1.ResourceRequirements{Limits: limits},
			Command: []string{"/bin/sh", "-c", "for i in 1 2 3 4 5; do nc -z -w 3 " + host + " 5432 && exit 0; sleep 1; done; exit 1"}}},
	}}
	pods := r.cs.CoreV1().Pods(replicaNS)
	if _, err := pods.Create(r.ctx, pod, metav1.CreateOptions{}); err != nil {
		r.t.Fatal(err)
	}
	defer pods.Delete(context.Background(), name, metav1.DeleteOptions{})
	var phase corev1.PodPhase
	r.eventually(name+" finishes", func() bool {
		p, err := pods.Get(r.ctx, name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		phase = p.Status.Phase
		return phase == corev1.PodSucceeded || phase == corev1.PodFailed
	})
	return phase == corev1.PodSucceeded
}

// logs are every container's log in the app's namespace.
func (r *replicaCluster) logs() string {
	r.t.Helper()
	list, err := r.cs.CoreV1().Pods(replicaNS).List(r.ctx, metav1.ListOptions{})
	if err != nil {
		r.t.Fatal(err)
	}
	var out strings.Builder
	for _, p := range list.Items {
		for _, c := range append(p.Spec.InitContainers, p.Spec.Containers...) {
			b, _ := r.cs.CoreV1().Pods(replicaNS).GetLogs(p.Name, &corev1.PodLogOptions{Container: c.Name}).DoRaw(r.ctx)
			out.WriteString("==> " + p.Name + "/" + c.Name + "\n")
			out.Write(b)
		}
	}
	return out.String()
}

// TestKubernetesIdentityReplicas installs KyIdentity as replicas on a three-node kind cluster,
// first on an operator's Postgres and then on the installer's. It needs a KyIdentity image
// with replica support (KyIdentity-server #219-#228) already in the local Docker:
// KYQ_TEST_IDENTITY_REPLICA_IMAGE names it.
func TestKubernetesIdentityReplicas(t *testing.T) {
	image := os.Getenv("KYQ_TEST_IDENTITY_REPLICA_IMAGE")
	if image == "" {
		t.Skip("KYQ_TEST_IDENTITY_REPLICA_IMAGE names no KyIdentity image with replica support")
	}
	ctx := context.Background()
	work := t.TempDir()
	cluster, kc := kindClusterNodes(t, work, 2)
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
	postgres := app.Images[app.Kubernetes.Replicas.DatabaseImage]
	app.Images = map[string]string{"kyidentity": image, "postgres": postgres}
	kind(t, "load", "docker-image", image, "--name", cluster)
	r := &replicaCluster{t: t, ctx: ctx, cs: cs, c: connected, app: app}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("logs:\n%s", r.logs())
			events, _ := cs.CoreV1().Events(replicaNS).List(context.Background(), metav1.ListOptions{})
			for _, e := range events.Items {
				t.Logf("event: %s %s: %s", e.InvolvedObject.Name, e.Reason, e.Message)
			}
		}
	})

	// An operator's own Postgres, in a namespace the installer knows nothing of.
	const theirs = "their-postgres"
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: theirs}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	const theirPassword = "their-password-0123456789"
	if _, err := cs.CoreV1().Pods(theirs).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "postgres", Labels: map[string]string{"app": "postgres"}}, Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: "postgres", Image: postgres, Env: []corev1.EnvVar{
			{Name: "POSTGRES_PASSWORD", Value: theirPassword}, {Name: "POSTGRES_DB", Value: "identity"}, {Name: "POSTGRES_INITDB_ARGS", Value: "--encoding=UTF8 --locale=C"},
		}}},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Services(theirs).Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "postgres"}, Spec: corev1.ServiceSpec{
		Selector: map[string]string{"app": "postgres"}, Ports: []corev1.ServicePort{{Port: 5432}},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	theirURL := "postgres://postgres:" + theirPassword + "@postgres." + theirs + ".svc:5432/identity?sslmode=disable"

	t.Run("operator database", func(t *testing.T) {
		r.t = t
		a := r.plan(2, "my-postgres")
		// The first apply stops at the Secret only the operator can create.
		applied, err := r.run(kube.Steps(a))
		if err == nil || !strings.Contains(err.Error(), "create secret generic my-postgres") || strings.Contains(err.Error(), theirPassword) {
			t.Fatalf("apply without the Secret: %v after %v", err, applied)
		}
		if len(r.deployments()) != 0 {
			t.Fatal("something was deployed without a database")
		}
		if _, err := cs.CoreV1().Secrets(replicaNS).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "my-postgres", Labels: map[string]string{managed.Label: managed.Value}},
			Data:       map[string][]byte{"url": []byte(theirURL + "\n")},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.run(kube.Steps(a)); err != nil {
			t.Fatal(err)
		}
		d := r.deployments()
		if len(d) != 2 || d["app"].Status.ReadyReplicas != 1 || d["app-replica"].Status.ReadyReplicas != 1 {
			t.Fatalf("deployments = %v", d)
		}
		pods := r.pods()
		if len(pods) != 2 || r.jwks(pods[0]) != r.jwks(pods[1]) {
			t.Fatalf("pods %v do not present one signing key", pods)
		}
		session := r.signIn(pods[1], string(r.secret("kyq-replica-keys")["bootstrap-admin-pass"]))
		if !r.signedIn(pods[0], session) {
			t.Fatal("a session made on the replica is refused by app")
		}
		if logs := r.logs(); strings.Contains(logs, theirPassword) {
			t.Fatal("the operator's database password is in a pod log")
		}
		if _, err := r.run(kube.RemoveSteps(a)); err != nil {
			t.Fatal(err)
		}
	})

	// A new installation: nothing of the first may remain.
	if err := cs.CoreV1().Namespaces().Delete(ctx, replicaNS, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	r.t = t
	r.eventually("the namespace is gone", func() bool {
		_, err := cs.CoreV1().Namespaces().Get(ctx, replicaNS, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	})

	a := r.plan(3, "")
	applied, err := r.run(kube.Steps(a))
	if err != nil {
		t.Fatalf("apply: %v after %v", err, applied)
	}
	d := r.deployments()
	if len(d) != 3 || d["app"].Status.ReadyReplicas != 1 || d["app-replica"].Status.ReadyReplicas != 2 || d["database"].Status.ReadyReplicas != 1 {
		t.Fatalf("deployments = %v", d)
	}
	pods := r.pods()
	if len(pods) != 3 {
		t.Fatalf("pods = %v", pods)
	}
	// Setup, which runs in one pod, still finds exactly one: "app"'s.
	if _, pod, _, err := connected.AppPod(ctx, "kyidentity"); err != nil || pod != pods[0] {
		t.Fatalf("setup would run in %q: %v", pod, err)
	}

	// One identity: one signing key, one administrator, and a session any pod accepts.
	key := r.jwks(pods[0])
	keys, db := r.secret("kyq-replica-keys"), r.secret("kyq-database")
	password := string(keys["bootstrap-admin-pass"])
	sessions := map[string]string{}
	for i, pod := range pods {
		if r.jwks(pod) != key {
			t.Fatalf("%s signs with another key", pod)
		}
		sessions[pod] = r.signIn(pod, password)
		for _, other := range pods {
			if !r.signedIn(other, sessions[pod]) {
				t.Fatalf("a session made on pod %d (%s) is refused by %s", i, pod, other)
			}
		}
	}
	if r.signedIn(pods[1], "kyidentity_session=not-a-session") {
		t.Fatal("any cookie is accepted")
	}

	// The database: the app's role owns its database and is no superuser; UTF8 and C.
	dbPods, err := cs.CoreV1().Pods(replicaNS).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=kyidentity-database"})
	if err != nil || len(dbPods.Items) != 1 {
		t.Fatalf("database pods: %v", err)
	}
	if got := r.query("select rolsuper::text || ' ' || rolcreaterole::text || ' ' || rolcreatedb::text from pg_roles where rolname = 'kyidentity'"); got != "false false false" {
		t.Fatalf("the app's role: %s", got)
	}
	if got := r.query("select pg_get_userbyid(datdba) || ' ' || datcollate || ' ' || pg_encoding_to_char(encoding) from pg_database where datname = 'kyidentity'"); got != "kyidentity C UTF8" {
		t.Fatalf("the app's database: %s", got)
	}
	if got := r.query("select count(*) from users"); got != "1" {
		t.Fatalf("three pods starting at once created %s administrators", got)
	}

	// Only the app's pods reach Postgres.
	if r.probe("probe-stranger", map[string]string{}, "database") {
		t.Fatal("a pod that is not the app's reached the database")
	}
	if !r.probe("probe-app", map[string]string{"ky.serves": "kyidentity"}, "database") {
		t.Fatal("the probe cannot reach the database even as the app: the check above proves nothing")
	}

	// No secret in a log, and none in an object but the two Secrets.
	secrets := []string{password, string(db["password"]), string(db["superuser-password"]), string(db["url"]), string(keys["jwt_rs256.key"])}
	logs := r.logs()
	for _, d := range r.deployments() {
		b, _ := json.Marshal(d)
		logs += string(b)
	}
	for i, s := range secrets {
		if len(s) < 40 || strings.Contains(logs, s) {
			t.Fatalf("secret %d (%d bytes) is in a pod log or a Deployment", i, len(s))
		}
	}

	// A second apply has nothing to do and replaces no pod.
	if applied, err := r.run(kube.Steps(a)); err != nil || len(applied) != 0 {
		t.Fatalf("second apply: %v, applied %v", err, applied)
	}
	if again := r.pods(); !slices.Equal(again, pods) {
		t.Fatalf("pods changed: %v -> %v", pods, again)
	}

	// "app"'s pod dies: the replicas keep serving every session, and the new pod joins them.
	if err := cs.CoreV1().Pods(replicaNS).Delete(ctx, pods[0], metav1.DeleteOptions{GracePeriodSeconds: ptr.To(int64(0))}); err != nil {
		t.Fatal(err)
	}
	for _, pod := range pods[1:] {
		if !r.signedIn(pod, sessions[pods[0]]) {
			t.Fatalf("%s lost the session when app's pod died", pod)
		}
	}
	r.eventually("app's pod is replaced", func() bool {
		_, err := r.run(kube.Steps(a)[3:])
		return err == nil && len(r.pods()) == 3
	})
	pods = r.pods()
	if r.jwks(pods[0]) != key || !r.signedIn(pods[0], sessions[pods[1]]) {
		t.Fatal("app's new pod is another identity")
	}

	// The database pod dies: its claim keeps the data, and the pods recover without a restart
	// of anything by the installer.
	if err := cs.CoreV1().Pods(replicaNS).Delete(ctx, dbPods.Items[0].Name, metav1.DeleteOptions{GracePeriodSeconds: ptr.To(int64(0))}); err != nil {
		t.Fatal(err)
	}
	r.eventually("a new database pod is ready", func() bool {
		now, err := cs.CoreV1().Pods(replicaNS).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=kyidentity-database"})
		return err == nil && len(now.Items) == 1 && now.Items[0].Name != dbPods.Items[0].Name && slices.ContainsFunc(now.Items[0].Status.Conditions, func(c corev1.PodCondition) bool {
			return c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue
		})
	})
	// The same pods, never restarted by the installer, find the database again by themselves,
	// and the session is still in it.
	r.eventually("every pod accepts the earlier session again", func() bool {
		return kube.Steps(a)[3].Verify(ctx) == nil && slices.Equal(r.pods(), pods) &&
			!slices.ContainsFunc(pods, func(pod string) bool { return !r.signedIn(pod, sessions[pods[1]]) })
	})
	if got := r.query("select count(*) from users"); got != "1" {
		t.Fatalf("the database came back with %s administrators", got)
	}

	// Stop and start: nothing of the app runs in between, and it comes back as it was.
	if _, err := r.run([]engine.Step{kube.StopStep(a)}); err != nil {
		t.Fatal(err)
	}
	if left := r.pods(); len(left) != 0 {
		t.Fatalf("pods after stop: %v", left)
	}
	if state, err := connected.State(ctx, "kyidentity"); err != nil || state != "stopped" {
		t.Fatalf("state after stop: %s %v", state, err)
	}
	if _, err := r.run([]engine.Step{kube.StartStep(a)}); err != nil {
		t.Fatal(err)
	}
	pods = r.pods()
	if state, _ := connected.State(ctx, "kyidentity"); state != "running" || len(pods) != 3 {
		t.Fatalf("state after start: %s, pods %v", state, pods)
	}
	session := r.signIn(pods[2], password)

	// Uninstall keeps the claims and the Secrets; a reinstall is the same identity.
	if _, err := r.run(kube.RemoveSteps(a)); err != nil {
		t.Fatal(err)
	}
	if left := r.deployments(); len(left) != 0 {
		t.Fatalf("deployments after uninstall: %v", left)
	}
	if _, err := r.run(kube.Steps(a)); err != nil {
		t.Fatal(err)
	}
	pods = r.pods()
	if len(pods) != 3 || r.jwks(pods[1]) != key || !r.signedIn(pods[0], session) {
		t.Fatal("the reinstall is another identity, or lost its data")
	}

	// The plan may not quietly go back to one instance on an empty SQLite file.
	if _, err := r.run(kube.Steps(r.plan(0, ""))); err == nil || !strings.Contains(err.Error(), "installed as replicas on Postgres") {
		t.Fatalf("back to a single instance: %v", err)
	}
	if len(r.pods()) != 3 {
		t.Fatal("the refusal changed the install")
	}
}

// query runs sql in the installer's Postgres pod as it is now.
func (r *replicaCluster) query(sql string) string {
	r.t.Helper()
	pods, err := r.cs.CoreV1().Pods(replicaNS).List(r.ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=kyidentity-database"})
	if err != nil || len(pods.Items) != 1 {
		r.t.Fatalf("database pods: %v", err)
	}
	out, err := r.c.Exec(r.ctx, replicaNS, pods.Items[0].Name, "database", []string{"psql", "-U", "postgres", "-d", "kyidentity", "-Atc", sql})
	if err != nil {
		r.t.Fatalf("%s: %v", sql, err)
	}
	return strings.TrimSpace(string(out))
}
