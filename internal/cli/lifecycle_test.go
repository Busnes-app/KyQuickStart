package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
)

// stoppableHost is dockerHost with compose stop, restart and logs: a stopped project's
// container reports exited until up -d or restart.
type stoppableHost struct {
	*dockerHost
	stopped map[string]bool
	logs    string
}

func (h *stoppableHost) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	m := projectRE.FindStringSubmatch(cmd)
	switch {
	case m == nil || !strings.Contains(cmd, "docker compose"):
	case strings.HasSuffix(cmd, " stop"):
		h.docker = append(h.docker, cmd)
		h.stopped[m[1]] = true
		return nil, nil
	case strings.HasSuffix(cmd, " restart"), strings.Contains(cmd, " up -d"):
		h.stopped[m[1]] = false
	case strings.Contains(cmd, " logs "):
		h.docker = append(h.docker, cmd)
		return []byte(h.logs), nil
	case strings.Contains(cmd, " ps --all --format json") && h.stopped[m[1]]:
		h.docker = append(h.docker, cmd)
		return []byte(`{"Service":"whoami","State":"exited","Health":""}` + "\n"), nil
	}
	return h.dockerHost.Run(ctx, cmd, stdin)
}

func stoppableFixture(t *testing.T) (*fixture, *stoppableHost) {
	t.Helper()
	f := newFixture(t, "../catalog/testdata/apps")
	h := &stoppableHost{dockerHost: f.h, stopped: map[string]bool{}}
	dialTarget = func(_ context.Context, t stack.Target, _ remote.SSHConfig, _ bool) (driver, error) {
		return sshDriver{t: t, c: h}, nil
	}
	f.plan(t, "apps:\n  - name: hello\n    target: nas\n")
	if err := f.run("apply"); err != nil {
		t.Fatal(err, f.out.String())
	}
	return f, h
}

func (f *fixture) status(t *testing.T) suite.App {
	t.Helper()
	if err := f.run("status", "--json"); err != nil {
		t.Fatal(err)
	}
	var st suite.Status
	if err := json.Unmarshal(f.out.Bytes(), &st); err != nil || len(st.Apps) != 1 {
		t.Fatalf("status %v: %s", err, f.out.String())
	}
	return st.Apps[0]
}

func TestStopStartRestartOnDocker(t *testing.T) {
	f, h := stoppableFixture(t)
	if a := f.status(t); a.Workload != "running" || a.Stopped {
		t.Fatalf("status %+v", a)
	}
	rev := f.stack(t).Revision
	if err := f.run("stop", "hello"); err != nil || f.step("hello.stop") != "ok" {
		t.Fatal(err, f.out.String())
	}
	if st := f.stack(t); !st.Apps[0].Stopped || st.Revision != rev+1 {
		t.Fatalf("plan %+v", st)
	}
	// A rerun changes nothing: no plan write, no docker compose stop.
	h.docker = nil
	if err := f.run("stop", "hello"); err != nil || f.step("hello.stop") != "skipped" || f.ran(" stop") || f.stack(t).Revision != rev+1 {
		t.Fatalf("second stop: %v %q\n%s", err, h.docker, f.out.String())
	}
	if a := f.status(t); a.Workload != "stopped" || !a.Stopped {
		t.Fatalf("status %+v", a)
	}
	f.run("status")
	if !strings.Contains(f.out.String(), "installed  stopped (kyquickstart stop)") {
		t.Errorf("status:\n%s", f.out.String())
	}

	// Nothing that would start it runs.
	h.docker = nil
	for _, args := range [][]string{{"apply"}, {"upgrade"}, {"upgrade", "hello"}, {"restart", "hello"}} {
		if err := f.run(args...); code(err) != suite.AppStopped || !strings.Contains(err.Error(), "kyquickstart start hello") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if f.ran(" up ") || f.ran(" restart") {
		t.Errorf("started a stopped app: %q", h.docker)
	}

	if err := f.run("start", "hello"); err != nil || f.step("hello.start") != "ok" || !f.ran(" up -d") {
		t.Fatal(err, f.out.String())
	}
	if st := f.stack(t); st.Apps[0].Stopped || st.Revision != rev+2 {
		t.Fatalf("plan %+v", st)
	}
	h.docker = nil
	if err := f.run("start", "hello"); err != nil || f.step("hello.start") != "skipped" || f.ran(" up ") {
		t.Fatalf("second start: %v %q", err, h.docker)
	}
	for range 2 { // every restart restarts
		h.docker = nil
		if err := f.run("restart", "hello"); err != nil || f.step("hello.restart") != "ok" || !f.ran(" restart") {
			t.Fatal(err, f.out.String())
		}
	}
	if f.stack(t).Revision != rev+2 {
		t.Error("restart changed the plan")
	}
	if a := f.status(t); a.Workload != "running" || a.Stopped {
		t.Fatalf("status %+v", a)
	}
	if err := f.run("apply"); err != nil {
		t.Fatal(err, f.out.String())
	}
}

func TestLogsRedactsGeneratedSecrets(t *testing.T) {
	f, h := stoppableFixture(t)
	secret, err := os.ReadFile(filepath.Join(f.h.root, "hello", "secrets", "token"))
	if err != nil || len(secret) < 8 {
		t.Fatal(err)
	}
	h.logs = "whoami-1  | token " + string(secret) + "\n"
	h.docker = nil
	if err := f.run("logs", "hello", "--tail", "5"); err != nil {
		t.Fatal(err)
	}
	if out := f.out.String(); out != "whoami-1  | token [redacted]\n" || !f.ran(" logs --no-color --tail 5") {
		t.Fatalf("logs %q, ran %q", out, h.docker)
	}
	for _, args := range [][]string{{"logs", "hello", "--tail", "0"}, {"logs", "hello", "--service", "nope"}, {"logs"}, {"logs", "hello", "web"}} {
		if err := f.run(args...); code(err) != suite.InvalidRequest {
			t.Errorf("%v: %v", args, err)
		}
	}
	if err := f.run("logs", "hello", "--service", "whoami"); err != nil || !f.ran(" logs --no-color --tail 200 'whoami'") {
		t.Errorf("service logs: %v %q", err, h.docker)
	}
}

func TestLifecycleRefusesUnmanagedAndRunnerOwned(t *testing.T) {
	f, h := stoppableFixture(t)
	override := filepath.Join(f.h.root, "hello", "compose.kyq.yaml")
	b, _ := os.ReadFile(override)
	os.WriteFile(override, bytes.ReplaceAll(b, []byte(managed.Label), []byte("x.other")), 0o600)
	h.docker = nil
	for _, args := range [][]string{{"stop", "hello"}, {"start", "hello"}, {"restart", "hello"}, {"logs", "hello"}} {
		if err := f.run(args...); code(err) != suite.NotInstalled {
			t.Errorf("%v: %v", args, err)
		}
	}
	if f.ran(" stop") || f.ran(" restart") || f.ran(" logs") || f.stack(t).Apps[0].Stopped {
		t.Errorf("acted on an unmanaged app: %q", h.docker)
	}
	os.WriteFile(override, b, 0o600)

	st := f.stack(t)
	os.WriteFile(filepath.Join(f.h.root, ".plan"), []byte(`{"revision":`+jsonInt(st.Revision)+`,"owner":"runner","runner_id":"ab-12","installation_id":"`+st.InstallationID+`"}`), 0o600)
	for _, cmd := range []string{"stop", "start"} {
		if err := f.run(cmd, "hello"); code(err) != suite.OwnedByRunner {
			t.Errorf("%s under the runner: %v", cmd, err)
		}
	}
	if f.ran(" stop") || f.stack(t).Apps[0].Stopped {
		t.Errorf("stopped a runner-owned app: %q", h.docker)
	}
	for _, cmd := range []string{"stop", "start", "restart", "logs"} {
		if err := f.run(cmd, "nope"); code(err) != suite.UnknownApp {
			t.Errorf("%s nope: %v", cmd, err)
		}
	}
}

func jsonInt(n int) string { b, _ := json.Marshal(n); return string(b) }

// TestLifecycleOnKubernetes drives a managed Deployment on k1; a reactor plays the controller.
func TestLifecycleOnKubernetes(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	f.plan(t, "  - name: k1\n    kubeconfig: "+kc+"\napps:\n  - name: hello\n    target: k1\n")
	labels := map[string]string{managed.Label: managed.Value, managed.ReleaseSetLabel: "test", managed.VersionLabel: "1.11.0"}
	sel := map[string]string{"app.kubernetes.io/name": "hello"}
	f.cs.Tracker().Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kyq-hello", Labels: labels}})
	f.cs.Tracker().Add(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kyq-secrets", Namespace: "kyq-hello", Labels: labels}, Data: map[string][]byte{"token": []byte("kube-generated-token")}})
	f.cs.Tracker().Add(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "kyq-hello", Labels: labels},
		Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: sel}}})
	f.cs.Tracker().Add(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "kyq-hello", Labels: map[string]string{managed.Label: managed.Value, "app.kubernetes.io/name": "hello"}}})
	f.cs.PrependReactor("get", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
		g := a.(ktesting.GetAction)
		o, err := f.cs.Tracker().Get(appsv1.SchemeGroupVersion.WithResource("deployments"), g.GetNamespace(), g.GetName())
		if err != nil {
			return true, nil, err
		}
		d := o.(*appsv1.Deployment).DeepCopy()
		n := *d.Spec.Replicas
		d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: n, UpdatedReplicas: n, ReadyReplicas: n, AvailableReplicas: n}
		return true, d, nil
	})
	f.cs.PrependReactor("get", "pods/log", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &runtime.Unknown{Raw: []byte("token kube-generated-token\n")}, nil
	})

	if a := f.status(t); a.Workload != "running" {
		t.Fatalf("status %+v", a)
	}
	if err := f.run("stop", "hello"); err != nil || !f.stack(t).Apps[0].Stopped {
		t.Fatal(err, f.out.String())
	}
	if a := f.status(t); a.Workload != "stopped" || !a.Stopped {
		t.Fatalf("status %+v", a)
	}
	if err := f.run("apply"); code(err) != suite.AppStopped {
		t.Fatalf("apply of a stopped app: %v", err)
	}
	if err := f.run("start", "hello"); err != nil || f.stack(t).Apps[0].Stopped {
		t.Fatal(err, f.out.String())
	}
	if err := f.run("restart", "hello"); err != nil {
		t.Fatal(err, f.out.String())
	}
	d, _ := f.cs.Tracker().Get(appsv1.SchemeGroupVersion.WithResource("deployments"), "kyq-hello", "app")
	if dep := d.(*appsv1.Deployment); *dep.Spec.Replicas != 1 || dep.Spec.Template.Annotations["kyquickstart/restarted-at"] == "" {
		t.Errorf("deployment %+v", dep.Spec)
	}
	if err := f.run("logs", "hello"); err != nil || f.out.String() != "==> pod app-1 <==\ntoken [redacted]\n" {
		t.Fatalf("logs %v %q", err, f.out.String())
	}
}
