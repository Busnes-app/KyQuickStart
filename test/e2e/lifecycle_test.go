//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	"go.yaml.in/yaml/v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

const foreignImage = "traefik/whoami:v1.11.0@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab"

// kyq runs one kyquickstart command against state with the test catalog at catalogDir.
func kyqRun(t *testing.T, ctx context.Context, state, catalogDir string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := cli.Run(ctx, append(args, "--state", state), cli.Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "e2e", Out: &out})
	t.Logf("%s: %v\n%s", strings.Join(args, " "), err, out.String())
	return out.String(), err
}

// workloadOf is app's live workload and stop intent in status --json.
func workloadOf(t *testing.T, ctx context.Context, state, catalogDir, app string) (string, bool) {
	t.Helper()
	out, err := kyqRun(t, ctx, state, catalogDir, "status", "--json")
	var st suite.Status
	if err != nil || json.Unmarshal([]byte(out), &st) != nil {
		t.Fatalf("status: %v", err)
	}
	for _, a := range st.Apps {
		if a.Name == app {
			return a.Workload, a.Stopped
		}
	}
	t.Fatalf("status lists no %s", app)
	return "", false
}

// lifecycleChecks drives stop, start, restart and logs on app and checks each through status;
// secret is a value the installer generated for app, which logs must not print.
func lifecycleChecks(t *testing.T, ctx context.Context, state, catalogDir, app, secret string) {
	t.Helper()
	run := func(args ...string) string {
		t.Helper()
		out, err := kyqRun(t, ctx, state, catalogDir, args...)
		if err != nil {
			t.Fatalf("%s: %v", strings.Join(args, " "), err)
		}
		return out
	}
	if w, stopped := workloadOf(t, ctx, state, catalogDir, app); w != "running" || stopped {
		t.Fatalf("before stop: %s %v", w, stopped)
	}
	run("stop", app)
	if out := run("stop", app); status(out, app+".stop") != "skipped" {
		t.Errorf("second stop:\n%s", out)
	}
	if w, stopped := workloadOf(t, ctx, state, catalogDir, app); w != "stopped" || !stopped {
		t.Fatalf("after stop: %s %v", w, stopped)
	}
	if _, err := kyqRun(t, ctx, state, catalogDir, "apply"); err == nil || !strings.Contains(err.Error(), "app_stopped") {
		t.Fatalf("apply of a stopped app: %v", err)
	}
	if w, _ := workloadOf(t, ctx, state, catalogDir, app); w != "stopped" {
		t.Fatalf("refused apply started the app: %s", w)
	}
	run("start", app)
	run("restart", app)
	if w, stopped := workloadOf(t, ctx, state, catalogDir, app); w != "running" || stopped {
		t.Fatalf("after start and restart: %s %v", w, stopped)
	}
	logs := run("logs", app, "--tail", "50")
	if strings.TrimSpace(logs) == "" || strings.Contains(logs, secret) {
		t.Errorf("logs empty or holding the generated secret:\n%s", logs)
	}
	if out := run("apply"); status(out, app+".deploy") != "skipped" {
		t.Errorf("apply after start redeployed:\n%s", out)
	}
}

// foreignContainer runs a container that only looks like the installer's (its labels, another
// project) and returns a check that it is still running, untouched.
func foreignContainer(t *testing.T) func() {
	id := make([]byte, 4)
	rand.Read(id)
	name := "kyq-e2e-foreign-" + hex.EncodeToString(id)
	docker(t, "run", "-d", "--name", name, "--memory", "16m", "--pids-limit", "8",
		"--label", "com.docker.compose.project=kyq-foreign", "--label", managed.Label+"="+managed.Value,
		"--entrypoint", "sleep", "kyq-e2e-sshd", "3600")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	started := docker(t, "inspect", "-f", "{{.State.StartedAt}}", name)
	return func() {
		t.Helper()
		if got := docker(t, "inspect", "-f", "{{.State.Running}} {{.State.StartedAt}}", name); got != "true "+started {
			t.Errorf("foreign container %s: %s, started %s", name, got, started)
		}
	}
}

// testLifecycle installs a copy of hello as "lifecycle" in a root of its own on the SSH
// target, drives every lifecycle verb, then uninstalls it.
func testLifecycle(t *testing.T, ctx context.Context, demoState, port, base, sshd string) {
	work := t.TempDir()
	state, cat := filepath.Join(work, "state"), filepath.Join(work, "apps")
	os.MkdirAll(state, 0o700)
	if err := os.CopyFS(filepath.Join(cat, "lifecycle"), os.DirFS("../../internal/catalog/testdata/apps/hello")); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(cat, "lifecycle", "manifest.yaml")
	b, _ := os.ReadFile(manifest)
	os.WriteFile(manifest, bytes.Replace(b, []byte("name: hello"), []byte("name: lifecycle"), 1), 0o600)
	kh, _ := os.ReadFile(filepath.Join(demoState, "known_hosts"))
	os.WriteFile(filepath.Join(state, "known_hosts"), kh, 0o600)
	root := base + "/lifecycle-root"
	t.Cleanup(func() { exec.Command("docker", "compose", "-p", "kyq-lifecycle", "down").Run() })
	st := stack.Stack{Version: 1, Targets: []stack.Target{{Name: "e2e", Host: "127.0.0.1", User: "root", Root: root}}, Apps: []stack.App{{Name: "lifecycle", Target: "e2e"}}}
	st.Targets[0].Port, _ = strconv.Atoi(port)
	raw, _ := yaml.Marshal(st)
	os.WriteFile(filepath.Join(state, "stack.yaml"), raw, 0o600)
	if _, err := kyqRun(t, ctx, state, cat, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	secret := docker(t, "exec", sshd, "cat", root+"/lifecycle/secrets/token")
	lifecycleChecks(t, ctx, state, cat, "lifecycle", secret)
	if _, err := kyqRun(t, ctx, state, cat, "uninstall", "lifecycle"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
}

// foreignDeployment creates a Deployment in a namespace the installer does not manage and
// returns a check that nothing changed it.
func foreignDeployment(t *testing.T, ctx context.Context, cs kubernetes.Interface) func() {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "e2e-foreign"}}
	if _, err := cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"app": "foreign"}
	limits := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("32Mi")}
	dep, err := cs.AppsV1().Deployments("e2e-foreign").Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Labels: labels},
		Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: "app", Image: foreignImage, Args: []string{"--port", "8080"}, Resources: corev1.ResourceRequirements{Requests: limits, Limits: limits},
			}}}}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		got, err := cs.AppsV1().Deployments("e2e-foreign").Get(ctx, "app", metav1.GetOptions{})
		if err != nil || got.UID != dep.UID || got.Generation != dep.Generation || *got.Spec.Replicas != 1 || got.Labels[managed.Label] != "" {
			t.Errorf("foreign deployment changed: %v %+v", err, got)
		}
	}
}

// testKubernetesLifecycle picks up the cluster's plan revision after the runner test (as
// pull-plan would), reinstalls hello and drives every lifecycle verb.
func testKubernetesLifecycle(t *testing.T, ctx context.Context, cs kubernetes.Interface, connected *kube.Client, state string) {
	const cat = "../../internal/catalog/testdata/apps"
	m, err := connected.Marker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(state, "stack.yaml")
	st, err := stack.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	st.Revision = m.Revision
	raw, _ := yaml.Marshal(st)
	os.WriteFile(plan, raw, 0o600)
	if _, err := kyqRun(t, ctx, state, cat, "apply"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	sec, err := cs.CoreV1().Secrets("kyq-hello").Get(ctx, "kyq-secrets", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lifecycleChecks(t, ctx, state, cat, "hello", string(sec.Data["token"]))
	if dep, err := cs.AppsV1().Deployments("kyq-hello").Get(ctx, "app", metav1.GetOptions{}); err != nil || dep.Spec.Template.Annotations["kyquickstart/restarted-at"] == "" {
		t.Errorf("restart left no rollout annotation: %v", err)
	}
	if _, err := kyqRun(t, ctx, state, cat, "uninstall", "hello"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := cs.CoreV1().Secrets("kyq-hello").Get(ctx, "kyq-secrets", metav1.GetOptions{}); err != nil {
		t.Errorf("uninstall removed hello's secrets: %v", err)
	}
}
