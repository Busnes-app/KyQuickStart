package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"go.yaml.in/yaml/v3"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	"golang.org/x/crypto/ssh"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

const runnerID = "0b6c1f3e-2a1d-4f6e-9c3b-5d7e8f9a0b1c"

// fakeRunner answers serve-keygen and serve-export, and keeps what serve-import received.
type fakeRunner struct {
	key      string
	imported map[string]string
	export   string
	ran      []string
}

func newFakeRunner(t *testing.T, f *fixture) *fakeRunner {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sp, _ := ssh.NewPublicKey(pub)
	r := &fakeRunner{key: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))}
	f.h.exec = func(cmd string, stdin []byte) ([]byte, error) {
		serve, ok := strings.CutPrefix(cmd, "docker exec -i 'kyquickstart-runner' kyquickstart ")
		serve, ok2 := strings.CutSuffix(serve, " --state /state")
		if !ok || !ok2 {
			t.Errorf("unexpected exec %q", cmd)
		}
		return r.answer(strings.Trim(serve, "'"), stdin), nil
	}
	return r
}

func (r *fakeRunner) answer(serve string, stdin []byte) []byte {
	r.ran = append(r.ran, serve)
	switch serve {
	case "serve-keygen":
		return []byte(r.key + " kyquickstart-runner " + runnerID + "\n")
	case "serve-export":
		return []byte(r.export)
	}
	r.imported = map[string]string{}
	tr := tar.NewReader(bytes.NewReader(stdin))
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		r.imported[h.Name] = string(b)
	}
	return nil
}

// runnerPod puts the fake runner in pod kyq-runner/runner-0 on Kubernetes target k1, whose
// API server is at https://k1.lan:6443. allowExec decides pods/exec; everything else is allowed.
func runnerPod(t *testing.T, f *fixture, r *fakeRunner, allowExec bool) {
	t.Helper()
	ctx := context.Background()
	f.cs.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		review := a.(ktesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		review.Status.Allowed = allowExec || review.Spec.ResourceAttributes.Subresource != "exec"
		return true, review, nil
	})
	f.cs.PrependReactor("create", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
		if s := a.(ktesting.CreateAction).GetObject().(*corev1.Secret); s.Type == corev1.SecretTypeServiceAccountToken {
			s.Data = map[string][]byte{"token": []byte("runner-token-" + s.Annotations["kyquickstart/runner-id"]), "ca.crt": []byte("CA")}
		}
		return false, nil, nil
	})
	f.cs.CoreV1().Pods("kyq-runner").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "runner-0", Namespace: "kyq-runner", Labels: map[string]string{"app": "runner"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "runner"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{})
	c := kube.Fake(f.cs, "https://k1.lan:6443", func(_ context.Context, ns, pod, container string, args []string, o remotecommand.StreamOptions) error {
		if ns+"/"+pod+"/"+container != "kyq-runner/runner-0/runner" || len(args) != 5 || args[0] != "kyquickstart" || strings.Join(args[2:], " ") != "--state /state/runner --pod-volumes" {
			t.Errorf("exec %s/%s/%s %q", ns, pod, container, args)
		}
		var in []byte
		if o.Stdin != nil {
			in, _ = io.ReadAll(o.Stdin)
		}
		_, err := o.Stdout.Write(r.answer(args[1], in))
		return err
	})
	saved := dialTarget
	dialTarget = func(ctx context.Context, tg stack.Target, sc remote.SSHConfig, needsStorage bool) (driver, error) {
		if tg.Kubernetes() {
			return kubeDriver{c: c}, nil
		}
		return saved(ctx, tg, sc, needsStorage)
	}
}

// kubeFixture is suiteFixture's Docker target nas plus Kubernetes target k1.
func kubeFixture(t *testing.T) *fixture {
	t.Helper()
	f, _ := suiteFixture(t)
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	f.plan(t, "  - name: k1\n    kubeconfig: "+kc+"\napps: []\n")
	t.Setenv("HOME", t.TempDir())
	return f
}

func server(t *testing.T, kubeconfig string) string {
	t.Helper()
	cfg, err := clientcmd.Load([]byte(kubeconfig))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Clusters[cfg.Contexts[cfg.CurrentContext].Cluster].Server
}

func TestHandoffToARunnerPod(t *testing.T) {
	f := kubeFixture(t)
	r := newFakeRunner(t, f)
	runnerPod(t, f, r, true)

	// A runner on nas reaches k1 at the workstation's address.
	if err := f.run("handoff", "--kyyard-target", "nas"); err != nil {
		t.Fatal(err, f.out.String())
	}
	if got := server(t, r.imported["kubeconfigs/k1.yaml"]); got != "https://k1.lan:6443" {
		t.Errorf("runner on nas reaches k1 at %s", got)
	}
	if err := f.run("handoff", "--revoke"); err != nil {
		t.Fatal(err)
	}

	// A runner in k1 reaches it in-cluster.
	r.ran = nil
	if err := f.run("handoff", "--kyyard-target", "k1", "--runner-pod", "kyq-runner/app=runner"); err != nil {
		t.Fatal(err, f.out.String())
	}
	if got := server(t, r.imported["kubeconfigs/k1.yaml"]); got != "https://kubernetes.default.svc" {
		t.Errorf("runner in k1 reaches it at %s", got)
	}
	if strings.Join(r.ran, " ") != "serve-keygen serve-import" || !strings.Contains(f.out.String(), "pod kyq-runner/app=runner on k1") {
		t.Errorf("ran %v\n%s", r.ran, f.out.String())
	}
	rec, _ := os.ReadFile(filepath.Join(f.state, handoffFile))
	if string(rec) != `{"kyyard_target":"k1","runner_namespace":"kyq-runner","runner_selector":"app=runner"}` {
		t.Errorf("handoff.json = %s", rec)
	}
	if m, _ := kube.New(f.cs).Marker(context.Background()); m.Owner != "runner" || m.RunnerID != runnerID || f.marker(t) == "" {
		t.Errorf("k1 marker %+v", m)
	}

	// pull-plan and --revoke find the runner in handoff.json.
	r.export = strings.Replace(r.imported["stack.yaml"], "revision: 3", "revision: 4", 1)
	if err := f.run("pull-plan"); err != nil || f.stack(t).Revision != 4 || r.ran[len(r.ran)-1] != "serve-export" {
		t.Fatalf("pull-plan: %v, revision %d", err, f.stack(t).Revision)
	}
	if rec2, _ := os.ReadFile(filepath.Join(f.state, handoffFile)); string(rec2) != string(rec) {
		t.Errorf("pull-plan rewrote handoff.json as %s", rec2)
	}
	if err := f.run("handoff", "--revoke"); err != nil || !strings.Contains(f.out.String(), "runner in pod kyq-runner/app=runner on k1 keeps") {
		t.Fatalf("revoke: %v\n%s", err, f.out.String())
	}
}

func TestHandoffRefusesARunnerPodItCannotExec(t *testing.T) {
	f := kubeFixture(t)
	r := newFakeRunner(t, f)
	runnerPod(t, f, r, false)
	err := f.run("handoff", "--kyyard-target", "k1")
	if err == nil || !strings.Contains(err.Error(), "denied create pods/exec") {
		t.Fatalf("handoff without pods/exec: %v", err)
	}
	if len(r.ran) != 0 || f.stack(t).Revision != 0 {
		t.Errorf("ran %v, revision %d", r.ran, f.stack(t).Revision)
	}
	if _, err := os.Stat(filepath.Join(f.state, handoffFile)); err == nil {
		t.Error("handoff.json written")
	}
	if _, err := f.cs.CoreV1().ServiceAccounts("kyquickstart").Get(context.Background(), "kyquickstart-runner", metav1.GetOptions{}); err == nil {
		t.Error("runner access granted")
	}
	if keys, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".ssh", "authorized_keys")); len(keys) != 0 {
		t.Errorf("authorized_keys = %q", keys)
	}
}

func TestHandoffRunnerFlags(t *testing.T) {
	f := kubeFixture(t)
	for _, c := range []struct {
		args []string
		want suite.Code
	}{
		{[]string{"handoff", "--kyyard-target", "k1", "--runner-container", "x"}, suite.InvalidRequest},
		{[]string{"handoff", "--kyyard-target", "nas", "--runner-pod", "kyq-runner/app=runner"}, suite.InvalidRequest},
		{[]string{"handoff", "--kyyard-target", "k1", "--runner-pod", "kyq-runner"}, suite.InvalidRequest},
		{[]string{"handoff", "--kyyard-target", "k1", "--runner-pod", "kyq-runner/app=("}, suite.InvalidRequest},
		{[]string{"handoff", "--kyyard-target", "nas", "--runner-container", "-x"}, suite.InvalidRequest},
		{[]string{"handoff", "--kyyard-target", "k9"}, suite.UnknownTarget},
		{[]string{"pull-plan"}, suite.InvalidRequest}, // nothing was handed off
	} {
		if err := f.run(c.args...); code(err) != c.want {
			t.Errorf("%v: %v", c.args, err)
		}
	}
	for _, args := range [][]string{
		{"handoff", "--kyyard-target", "nas", "--runner-container", "x", "--runner-pod", "a/b=c"},
		{"pull-plan", "--runner-pod", "a/b=c"},
		{"handoff"},
	} {
		if err := f.run(args...); err != ErrUsage {
			t.Errorf("%v: %v", args, err)
		}
	}
	// A hand-edited record is checked like the flags.
	os.WriteFile(filepath.Join(f.state, handoffFile), []byte(`{"kyyard_target":"k1","runner_container":"kyquickstart-runner"}`), 0o600)
	if err := f.run("pull-plan"); code(err) != suite.InvalidRequest {
		t.Errorf("pull-plan from a Docker record on k1: %v", err)
	}
}

func TestHandoffOwnershipAndRevoke(t *testing.T) {
	f, cat := suiteFixture(t)
	writeApp(t, cat, "extra", "version: 1.0.0\n")
	home := t.TempDir()
	t.Setenv("HOME", home)
	r := newFakeRunner(t, f)
	if err := f.run("install", "web", "--target", "nas"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(f.state, "known_hosts"), []byte("[192.0.2.1]:22 ssh-ed25519 AAAA\n"), 0o600)

	for range 2 { // a rerun reconciles
		if err := f.run("handoff", "--kyyard-target", "nas"); err != nil {
			t.Fatal(err, f.out.String())
		}
	}
	if f.stack(t).Revision != 3 || f.marker(t) != `{"revision":3,"owner":"runner","runner_id":"`+runnerID+`","installation_id":"`+f.stack(t).InstallationID+`"}` {
		t.Fatalf("revision %d, marker %s", f.stack(t).Revision, f.marker(t))
	}
	keys, _ := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys"))
	if string(keys) != "restrict "+r.key+" kyquickstart-runner "+runnerID+"\n" {
		t.Errorf("authorized_keys = %q", keys)
	}
	if st, err := stack.Parse([]byte(r.imported["stack.yaml"])); err != nil || st.Revision != 3 || st.InstallationID != f.stack(t).InstallationID {
		t.Errorf("imported plan (it carries the installation_id): %v", err)
	}
	if r.imported["known_hosts"] == "" || r.imported["results/web.deploy.json"] == "" {
		t.Errorf("imported %v", r.imported)
	}

	// The CLI may not change a runner-owned plan, locally or on its targets.
	f.h.docker = nil
	for _, args := range [][]string{{"install", "extra", "--target", "nas"}, {"uninstall", "web"}, {"unmanage", "web"}} {
		if err := f.run(args...); code(err) != suite.OwnedByRunner || !strings.Contains(err.Error(), "Suite page") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if f.ran(" down") || f.ran(" up ") || f.stack(t).Revision != 3 {
		t.Error("a refused command changed a target or the plan")
	}
	f.o.In = strings.NewReader("")
	if err := f.run("plan"); code(err) != suite.OwnedByRunner {
		t.Errorf("plan: %v", err)
	}
	f.o.In = nil
	// Plan-preserving commands run at the runner's revision.
	if err := f.run("upgrade"); err != nil {
		t.Errorf("upgrade at the runner's revision: %v", err)
	}
	if err := f.run("status"); err != nil {
		t.Errorf("status: %v", err)
	}

	// The runner moved on: the CLI must pull first.
	os.WriteFile(filepath.Join(f.h.root, ".plan"), []byte(`{"revision":5,"owner":"runner","runner_id":"`+runnerID+`"}`), 0o600)
	if err := f.run("apply"); code(err) != suite.PlanStale {
		t.Fatalf("apply behind the runner: %v", err)
	}
	if err := f.run("status"); code(err) != suite.PlanStale {
		t.Fatalf("status behind the runner: %v", err)
	}
	r.export = strings.Replace(r.imported["stack.yaml"], "revision: 3", "revision: 5", 1)
	if err := f.run("pull-plan", "--kyyard-target", "nas"); err != nil {
		t.Fatal(err)
	}
	if bak, _ := stack.Load(filepath.Join(f.state, "stack.yaml.bak")); f.stack(t).Revision != 5 || bak == nil || bak.Revision != 3 {
		t.Fatalf("pulled revision %d", f.stack(t).Revision)
	}
	if err := f.run("apply"); err != nil {
		t.Fatalf("apply after pull-plan: %v", err)
	}
	r.export = strings.Replace(r.export, "revision: 5", "revision: 4", 1)
	if err := f.run("pull-plan", "--kyyard-target", "nas"); code(err) != suite.PlanStale || f.stack(t).Revision != 5 {
		t.Fatalf("pulled an older plan: %v", err)
	}

	if err := f.run("handoff", "--revoke"); err != nil {
		t.Fatal(err, f.out.String())
	}
	if f.marker(t) != `{"revision":6,"owner":"cli","runner_id":"","installation_id":"`+f.stack(t).InstallationID+`"}` {
		t.Errorf("marker after revoke = %s", f.marker(t))
	}
	if keys, _ := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys")); len(keys) != 0 {
		t.Errorf("runner key left: %q", keys)
	}
	if err := f.run("install", "extra", "--target", "nas"); err != nil {
		t.Errorf("install after revoke: %v", err)
	}
	if err := f.run("uninstall", "web"); err != nil {
		t.Errorf("uninstall after revoke: %v", err)
	}
}

func TestRunnerActsOnlyOnItsOwnTargets(t *testing.T) {
	f, _ := suiteFixture(t)
	f.o.RunnerID = runnerID
	if err := f.run("install", "db", "--target", "nas"); code(err) != suite.OwnedByCLI {
		t.Fatalf("no marker: %v", err)
	}
	os.WriteFile(filepath.Join(f.h.root, ".plan"), []byte(`{"revision":0,"owner":"runner","runner_id":"9b6c1f3e-2a1d-4f6e-9c3b-5d7e8f9a0b1c"}`), 0o600)
	if err := f.run("install", "db", "--target", "nas"); code(err) != suite.OwnedByRunner {
		t.Fatalf("another runner's target: %v", err)
	}
	os.WriteFile(filepath.Join(f.h.root, ".plan"), []byte(`{"revision":0,"owner":"runner","runner_id":"`+runnerID+`","installation_id":"`+otherPlan+`"}`), 0o600)
	if err := f.run("install", "db", "--target", "nas"); code(err) != suite.OwnedByOtherPlan {
		t.Fatalf("another plan's target: %v", err)
	}
	os.WriteFile(filepath.Join(f.h.root, ".plan"), []byte(`{"revision":0,"owner":"runner","runner_id":"`+runnerID+`"}`), 0o600)
	if err := f.run("install", "db", "--target", "nas"); err != nil {
		t.Fatal(err)
	}
	if f.marker(t) != `{"revision":1,"owner":"runner","runner_id":"`+runnerID+`","installation_id":"`+f.stack(t).InstallationID+`"}` {
		t.Errorf("marker = %s", f.marker(t))
	}
	if _, err := Status(context.Background(), f.state, f.o); err != nil {
		t.Errorf("status: %v", err)
	}
}

func TestHandoffRefusesBadKeygenOutput(t *testing.T) {
	for _, out := range []string{
		"",
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC kyquickstart-runner " + runnerID,
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA operator@laptop",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA kyquickstart-runner " + runnerID + "\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA x",
	} {
		if _, _, err := runnerKey([]byte(out)); err == nil {
			t.Errorf("accepted %q", out)
		}
	}
}

// A stopped app would strand the runner, which cannot start it; the hand-off waits for start.
func TestHandoffRefusesAStoppedApp(t *testing.T) {
	f, _ := suiteFixture(t)
	t.Setenv("HOME", t.TempDir())
	newFakeRunner(t, f)
	if err := f.run("install", "web", "--target", "nas"); err != nil {
		t.Fatal(err)
	}
	st := f.stack(t)
	st.Apps[slices.IndexFunc(st.Apps, func(a stack.App) bool { return a.Name == "web" })].Stopped = true
	b, _ := yaml.Marshal(st)
	os.WriteFile(filepath.Join(f.state, "stack.yaml"), b, 0o600)
	rev := f.stack(t).Revision
	if err := f.run("handoff", "--kyyard-target", "nas"); code(err) != suite.AppStopped || !strings.Contains(err.Error(), "kyquickstart start web") {
		t.Fatalf("handoff of a stopped app: %v", err)
	}
	if f.stack(t).Revision != rev {
		t.Fatal("a refused hand-off changed the plan")
	}
}
