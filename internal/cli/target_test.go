package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	authorizationv1 "k8s.io/api/authorization/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// dockerHost is a Docker target on the local filesystem: shell commands run for real, docker is
// faked with one "whoami" container per Compose project.
type dockerHost struct {
	root   string
	docker []string          // docker commands, in order
	up     map[string]string // project -> compose.kyq.yaml when its container was created
	fail   string            // docker commands containing this fail
}

var (
	projectRE = regexp.MustCompile(`(?:-p '|compose\.project=)(kyq-[a-z0-9-]+)`)
	dirRE     = regexp.MustCompile(`^cd '([^']+)'`)
)

func (h *dockerHost) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	if !strings.Contains(cmd, "docker ") {
		return remote.Local{}.Run(ctx, cmd, stdin)
	}
	h.docker = append(h.docker, cmd)
	if h.fail != "" && strings.Contains(cmd, h.fail) {
		return nil, &remote.ExitError{Code: 1, Stderr: []byte("injected failure")}
	}
	var project string
	if m := projectRE.FindStringSubmatch(cmd); m != nil {
		project = m[1]
	}
	override, created := h.up[project]
	switch {
	case strings.Contains(cmd, "docker version"):
		return []byte("27.0.0\n"), nil
	case strings.Contains(cmd, "compose version"):
		return []byte("2.29.0\n"), nil
	case strings.Contains(cmd, " up -d"):
		b, err := os.ReadFile(filepath.Join(dirRE.FindStringSubmatch(cmd)[1], "compose.kyq.yaml"))
		h.up[project] = string(b)
		return nil, err
	case strings.Contains(cmd, " down"):
		delete(h.up, project)
	case strings.Contains(cmd, " ps --all --format json") && created:
		return []byte(`{"Service":"whoami","State":"running","Health":""}` + "\n"), nil
	case strings.Contains(cmd, "docker ps -aq") && created:
		if !strings.Contains(cmd, "label="+managed.Label) || strings.Contains(override, managed.Label) {
			return []byte("c0ffee\n"), nil
		}
	}
	return nil, nil
}

func (h *dockerHost) Close() error { return nil }

// fixture is a state directory whose targets connect to fakes: Docker target "nas" at
// h.root and, when the plan names one, Kubernetes target "k1" on cs.
type fixture struct {
	state string
	h     *dockerHost
	cs    *fake.Clientset
	out   bytes.Buffer
	o     Options
}

func newFixture(t *testing.T, catalogDir string) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{state: t.TempDir(), h: &dockerHost{root: root, up: map[string]string{}}, cs: fake.NewClientset()}
	f.o = Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "test", Out: &f.out}
	saved := dialTarget
	t.Cleanup(func() { dialTarget = saved })
	dialTarget = func(_ context.Context, t stack.Target, _, _ string, _ func(string, string) bool, needsStorage bool) (driver, error) {
		if t.Kubernetes() {
			return kubeDriver{c: kube.New(f.cs), needsStorage: needsStorage}, nil
		}
		return sshDriver{t: t, c: f.h}, nil
	}
	return f
}

// plan writes stack.yaml with a Docker target "nas" at the fixture root, plus extra YAML.
func (f *fixture) plan(t *testing.T, extra string) {
	t.Helper()
	y := "version: 1\ntargets:\n  - name: nas\n    host: 192.0.2.1\n    user: kyq\n    root: " + f.h.root + "\n" + extra
	if err := os.WriteFile(filepath.Join(f.state, "stack.yaml"), []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) run(args ...string) error {
	f.out.Reset()
	return Run(context.Background(), append(args, "--state", f.state), f.o)
}

func (f *fixture) stack(t *testing.T) *stack.Stack {
	t.Helper()
	st, err := stack.Load(filepath.Join(f.state, "stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func code(err error) suite.Code {
	var se *suite.Error
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

func TestApplyWritesMarkerAndRefusesNewerPlan(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.plan(t, "revision: 2\napps:\n  - name: hello\n    target: nas\n")
	if err := f.run("apply"); err != nil {
		t.Fatal(err, f.out.String())
	}
	b, _ := os.ReadFile(filepath.Join(f.h.root, ".plan"))
	if string(b) != `{"revision":2,"owner":"cli","runner_id":""}` {
		t.Fatalf("marker = %s", b)
	}

	os.WriteFile(filepath.Join(f.h.root, ".plan"), []byte(`{"revision":3,"owner":"runner","runner_id":"ab-12"}`), 0o600)
	f.h.docker = nil
	err := f.run("apply")
	if code(err) != suite.PlanStale || !strings.Contains(err.Error(), "pull-plan") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range f.h.docker {
		if strings.Contains(c, "compose") && !strings.Contains(c, "version") {
			t.Errorf("mutated after a stale-plan refusal: %s", c)
		}
	}
	if _, err := os.Stat(filepath.Join(f.h.root, ".lock")); !os.IsNotExist(err) {
		t.Error("lock kept after refusal")
	}
	if b, _ := os.ReadFile(filepath.Join(f.h.root, ".plan")); !strings.Contains(string(b), `"revision":3`) {
		t.Errorf("refusal rewrote the marker: %s", b)
	}
}

func TestKubernetesMarkerRefusesNewerPlan(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	f.plan(t, "  - name: k1\n    kubeconfig: "+kc+"\napps:\n  - name: hello\n    target: k1\n")
	f.cs.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{Major: "1", Minor: "37"}
	f.cs.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		r := a.(ktesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		r.Status.Allowed = true
		return true, r, nil
	})
	f.cs.StorageV1().StorageClasses().Create(context.Background(), &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{
		Name: "standard", Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"},
	}}, metav1.CreateOptions{})
	c := kube.New(f.cs)
	release, err := kube.Acquire(context.Background(), c, "setup")
	if err != nil {
		t.Fatal(err)
	}
	c.WriteMarker(context.Background(), stack.Marker{Revision: 1, Owner: "runner", RunnerID: "ab"})
	release(context.Background())
	if err := f.run("apply"); code(err) != suite.PlanStale {
		t.Fatalf("err = %v", err)
	}
	if _, err := kube.Acquire(context.Background(), c, "after"); err != nil {
		t.Fatalf("lease kept after refusal: %v", err)
	}
}
