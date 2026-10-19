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

	"github.com/Busnes-app/kyquickstart/internal/catalog"
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
	docker []string                                       // docker commands, in order
	up     map[string]string                              // project -> compose.kyq.yaml when its container was created
	fail   string                                         // docker commands containing this fail
	exec   func(cmd string, stdin []byte) ([]byte, error) // answers docker exec
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
	if h.exec != nil && strings.Contains(cmd, "docker exec ") {
		return h.exec(cmd, stdin)
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
	case strings.Contains(cmd, " up -d") || strings.Contains(cmd, " up --no-start"):
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
	dialTarget = func(_ context.Context, t stack.Target, _ remote.SSHConfig, needsStorage bool) (driver, error) {
		if t.Kubernetes() {
			return kubeDriver{c: kube.New(f.cs), needsStorage: needsStorage, storageClass: t.StorageClass}, nil
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
	// The plan had no installation_id: apply published one, at revision 3.
	b, _ := os.ReadFile(filepath.Join(f.h.root, ".plan"))
	if string(b) != `{"revision":3,"owner":"cli","runner_id":"","installation_id":"`+f.stack(t).InstallationID+`"}` {
		t.Fatalf("marker = %s", b)
	}

	os.WriteFile(filepath.Join(f.h.root, ".plan"), []byte(`{"revision":4,"owner":"runner","runner_id":"ab-12"}`), 0o600)
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
	if b, _ := os.ReadFile(filepath.Join(f.h.root, ".plan")); !strings.Contains(string(b), `"revision":4`) {
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

func TestKubernetesStorageClassReachesDriver(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	f.plan(t, "  - name: k1\n    kubeconfig: "+kc+"\n    storage_class: local-path\napps:\n  - name: hello\n    target: k1\n")
	f.cs.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{Major: "1", Minor: "37"}
	f.cs.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		r := a.(ktesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		r.Status.Allowed = true
		return true, r, nil
	})
	// No default class: only the named one.
	f.cs.StorageV1().StorageClasses().Create(context.Background(), &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "local-path"}}, metav1.CreateOptions{})
	if err := f.run("preflight"); err != nil || !strings.Contains(f.out.String(), "StorageClass local-path") {
		t.Fatalf("preflight: %v\n%s", err, f.out.String())
	}
	cat, err := catalog.Load(os.DirFS("../catalog/testdata/apps"))
	if err != nil {
		t.Fatal(err)
	}
	hash := func(class string) string {
		return kubeDriver{c: kube.New(f.cs), storageClass: class}.steps("hello", cat["hello"], "rs", nil)[1].InputHash()
	}
	if hash("local-path") == hash("") {
		t.Fatal("storage class does not reach the deploy step")
	}
}

func TestDialTargetKeepsStorageClass(t *testing.T) {
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	cfg := "apiVersion: v1\nkind: Config\nclusters:\n- name: c\n  cluster:\n    server: https://192.0.2.1:6443\ncontexts:\n- name: c\n  context:\n    cluster: c\n    user: u\nusers:\n- name: u\n  user:\n    token: t\ncurrent-context: c\n"
	if err := os.WriteFile(kc, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := dialTarget(context.Background(), stack.Target{Name: "k1", Kubeconfig: kc, StorageClass: "local-path"}, remote.SSHConfig{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := d.(kubeDriver); !ok || k.storageClass != "local-path" {
		t.Fatalf("driver = %+v", d)
	}
}

const (
	thisPlan  = "271333e2-c221-4601-b249-563b3d828530"
	otherPlan = "2c676879-9c71-4913-b8a7-995521d9ed20"
)

// Revisions compare only within one installation; a marker without an id predates them.
func TestGuardInstallation(t *testing.T) {
	cli := &session{revision: 5, installation: thisPlan}
	runner := &session{revision: 5, installation: thisPlan, o: Options{RunnerID: runnerID}}
	for _, c := range []struct {
		s    *session
		m    stack.Marker
		want suite.Code
	}{
		{cli, stack.Marker{Revision: 4, Owner: "cli", InstallationID: thisPlan}, ""},
		{cli, stack.Marker{Revision: 6, Owner: "cli", InstallationID: thisPlan}, suite.PlanStale},
		{cli, stack.Marker{Revision: 1, Owner: "cli", InstallationID: otherPlan}, suite.OwnedByOtherPlan},
		{cli, stack.Marker{Revision: 9, Owner: "cli", InstallationID: otherPlan}, suite.OwnedByOtherPlan},
		{cli, stack.Marker{Revision: 4, Owner: "cli"}, ""},
		{cli, stack.Marker{Revision: 6, Owner: "cli"}, suite.PlanStale},
		{runner, stack.Marker{Revision: 4, Owner: "runner", RunnerID: runnerID, InstallationID: thisPlan}, ""},
		{runner, stack.Marker{Revision: 4, Owner: "runner", RunnerID: runnerID, InstallationID: otherPlan}, suite.OwnedByOtherPlan},
		{runner, stack.Marker{Revision: 4, Owner: "runner", RunnerID: runnerID}, ""},
	} {
		err := c.s.guard("nas", c.m)
		if code(err) != c.want {
			t.Errorf("%+v (runner %q): %v, want %q", c.m, c.s.o.RunnerID, err, c.want)
		}
		if c.want == suite.OwnedByOtherPlan && !strings.Contains(err.Error(), "installation "+otherPlan+"); run apply --take-over-target nas") {
			t.Errorf("message: %v", err)
		}
	}
}

func TestApplyClaimsAndTakesOverTargets(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.plan(t, "revision: 2\ninstallation_id: "+thisPlan+"\napps:\n  - name: hello\n    target: nas\n")
	marker := filepath.Join(f.h.root, ".plan")
	ours := `{"revision":2,"owner":"cli","runner_id":"","installation_id":"` + thisPlan + `"}`

	// A marker from an older build is claimed.
	os.WriteFile(marker, []byte(`{"revision":1,"owner":"cli","runner_id":""}`), 0o600)
	if err := f.run("apply"); err != nil || f.marker(t) != ours {
		t.Fatalf("legacy marker: %v, marker %s", err, f.marker(t))
	}

	// Another plan's target is refused before anything changes, whatever its revision.
	foreign := `{"revision":9,"owner":"cli","runner_id":"","installation_id":"` + otherPlan + `"}`
	os.WriteFile(marker, []byte(foreign), 0o600)
	f.h.docker = nil
	if err := f.run("apply"); code(err) != suite.OwnedByOtherPlan || f.ran(" up ") || f.marker(t) != foreign {
		t.Fatalf("other plan: %v, marker %s", err, f.marker(t))
	}
	if err := f.run("apply", "--take-over-target", "nas"); err != nil {
		t.Fatal(err, f.out.String())
	}
	if f.marker(t) != ours || !strings.Contains(f.out.String(), "warning: taking nas over from installation "+otherPlan) {
		t.Fatalf("take-over: marker %s, out %s", f.marker(t), f.out.String())
	}

	// The runner's target must be revoked by its own plan first.
	runners := `{"revision":1,"owner":"runner","runner_id":"` + runnerID + `","installation_id":"` + otherPlan + `"}`
	os.WriteFile(marker, []byte(runners), 0o600)
	if err := f.run("apply", "--take-over-target", "nas"); code(err) != suite.OwnedByRunner || !strings.Contains(err.Error(), "--revoke") || f.marker(t) != runners {
		t.Fatalf("runner-owned take-over: %v, marker %s", err, f.marker(t))
	}

	if err := f.run("apply", "--take-over-target", "nope"); code(err) != suite.UnknownTarget {
		t.Errorf("unknown target: %v", err)
	}
	if err := f.run("preflight", "--take-over-target", "nas"); err == nil || !strings.Contains(err.Error(), "only for apply") {
		t.Errorf("preflight: %v", err)
	}
}

// A failed apply's new id is not published, so it must not reach the marker either: the
// rerun generates another and would otherwise be refused as another plan.
func TestFailedApplyKeepsTheIDOffMarkers(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.plan(t, "apps:\n  - name: hello\n    target: nas\n")
	f.h.fail = " up -d"
	if err := f.run("apply"); err == nil {
		t.Fatal("apply passed")
	}
	if m := f.marker(t); m == "" || strings.Contains(m, "installation_id") || f.stack(t).InstallationID != "" {
		t.Fatalf("unpublished id leaked: marker %s", f.marker(t))
	}
	f.h.fail = ""
	if err := f.run("apply"); err != nil {
		t.Fatal(err, f.out.String())
	}
	if id := f.stack(t).InstallationID; id == "" || !strings.Contains(f.marker(t), id) {
		t.Fatalf("rerun: plan id %q, marker %s", id, f.marker(t))
	}
}
