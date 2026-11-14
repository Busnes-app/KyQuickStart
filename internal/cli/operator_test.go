package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/suite"
)

// The catalog's cloudnative-pg in a plan: installed before the apps that need it, with
// cluster-admin asked of the workstation alone and nothing of it left to the suite runner.
func TestOperatorPlan(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	k1 := "  - name: k1\n    kubeconfig: " + kc + "\n"
	apps := "apps:\n  - name: npm\n    target: k1\n  - name: cloudnative-pg\n    target: k1\n"
	f.plan(t, k1+apps)
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.order, []string{"cloudnative-pg", "npm"}) {
		t.Fatalf("order = %v", s.order)
	}
	// Even an app that sorts before it waits for it.
	if got := operatorsFirst([]string{"aaa", "cloudnative-pg", "zzz"}, s.cat); !slices.Equal(got, []string{"cloudnative-pg", "aaa", "zzz"}) {
		t.Fatalf("operatorsFirst = %v", got)
	}
	if n := s.needs("k1"); n != (kube.Needs{Operator: true}) {
		t.Fatalf("needs = %+v", n)
	}
	if n := s.needs("nas"); n != (kube.Needs{}) {
		t.Fatalf("needs on another target = %+v", n)
	}
	if !slices.Equal(s.edgeApps("k1"), []string{"npm"}) {
		t.Fatalf("edge apps = %v", s.edgeApps("k1"))
	}

	// The runner holds the install permissions, never cluster-admin.
	runner := *s
	runner.o.RunnerID = runnerID
	if n := runner.needs("k1"); n.Operator {
		t.Fatal("the runner's preflight asks for cluster-admin")
	}
	if err := runner.refuseOwnPod([]string{"cloudnative-pg"}); code(err) != suite.RunnerHost {
		t.Fatalf("a runner request naming the operator: %v", err)
	}
	if err := s.refuseOwnPod([]string{"cloudnative-pg"}); err != nil {
		t.Fatalf("the workstation is refused: %v", err)
	}

	for plan, want := range map[string]string{
		"apps:\n  - name: cloudnative-pg\n    target: nas\n": "place it on a Kubernetes target",
		k1 + apps + "edges:\n  - target: k1\n    provider: lan\n    allow_cidrs: [192.168.1.0/24]\n    npm_url: kube://kyq-npm/app:81\n    routes:\n      - app: cloudnative-pg\n        hostname: pg.example.com\n        forward_host: app.kyq-cloudnative-pg.svc\n        forward_port: 9443\n        certificate_id: 1\n": "serves nothing to route",
	} {
		f.plan(t, plan)
		if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", plan, err, want)
		}
	}

	// The wizard never asks for it, so it does not silently drop it either.
	f.plan(t, k1+apps)
	f.o.In = strings.NewReader("\n")
	if err := f.run("plan"); err == nil || !strings.Contains(err.Error(), "installs cloudnative-pg, which plan cannot ask for") {
		t.Fatalf("plan over a saved operator: %v", err)
	}
}
