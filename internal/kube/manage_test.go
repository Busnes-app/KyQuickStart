package kube

import (
	"context"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/managed"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// deployed installs the fixture app through its secrets and deploy steps.
func deployed(t *testing.T) (App, context.Context) {
	t.Helper()
	ctx := context.Background()
	a, _ := testApp(t, "token")
	for _, s := range Steps(a)[:2] {
		if err := s.Apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return a, ctx
}

func TestDeployedRecordsVersion(t *testing.T) {
	a, ctx := deployed(t)
	d, err := a.Client.Deployed(ctx, "hello")
	if err != nil || d != (managed.Deployed{Found: true, Managed: true, ReleaseSet: "rs-1", Version: "1.11.0"}) {
		t.Fatalf("deployed = %+v, %v", d, err)
	}
	if d, err := a.Client.Deployed(ctx, "other"); err != nil || d.Found {
		t.Fatalf("absent app = %+v, %v", d, err)
	}
}

func TestRemoveKeepsData(t *testing.T) {
	a, ctx := deployed(t)
	cs := a.Client.cs
	s := RemoveSteps(a)[0]
	if done, err := s.Inspect(ctx); err != nil || done {
		t.Fatalf("Inspect before = %v %v", done, err)
	}
	for range 2 { // a rerun after a partial removal finishes it
		if err := s.Apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	for kind, err := range map[string]error{
		"deployment": get(cs.AppsV1().Deployments("kyq-hello").Get(ctx, appName, metav1.GetOptions{})),
		"service":    get(cs.CoreV1().Services("kyq-hello").Get(ctx, appName, metav1.GetOptions{})),
	} {
		if err == nil {
			t.Errorf("%s kept", kind)
		}
	}
	for kind, err := range map[string]error{
		"claim":     get(cs.CoreV1().PersistentVolumeClaims("kyq-hello").Get(ctx, "vol-data", metav1.GetOptions{})),
		"secret":    get(cs.CoreV1().Secrets("kyq-hello").Get(ctx, secretName, metav1.GetOptions{})),
		"policy":    get(cs.NetworkingV1().NetworkPolicies("kyq-hello").Get(ctx, policyName, metav1.GetOptions{})),
		"namespace": get(cs.CoreV1().Namespaces().Get(ctx, "kyq-hello", metav1.GetOptions{})),
	} {
		if err != nil {
			t.Errorf("%s removed: %v", kind, err)
		}
	}
}

func TestUnmanageRemovesLabelsEverywhere(t *testing.T) {
	a, ctx := deployed(t)
	cs := a.Client.cs
	s := UnmanageSteps(a)[0]
	if done, err := s.Inspect(ctx); err != nil || done {
		t.Fatalf("Inspect before = %v %v", done, err)
	}
	for range 2 {
		if err := s.Apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	dep, _ := cs.AppsV1().Deployments("kyq-hello").Get(ctx, appName, metav1.GetOptions{})
	svc, _ := cs.CoreV1().Services("kyq-hello").Get(ctx, appName, metav1.GetOptions{})
	pvc, _ := cs.CoreV1().PersistentVolumeClaims("kyq-hello").Get(ctx, "vol-data", metav1.GetOptions{})
	sec, _ := cs.CoreV1().Secrets("kyq-hello").Get(ctx, secretName, metav1.GetOptions{})
	ns, _ := cs.CoreV1().Namespaces().Get(ctx, "kyq-hello", metav1.GetOptions{})
	for what, l := range map[string]map[string]string{"deployment": dep.Labels, "template": dep.Spec.Template.Labels, "service": svc.Labels, "claim": pvc.Labels, "secret": sec.Labels, "namespace": ns.Labels} {
		if _, ok := l[managed.Label]; ok {
			t.Errorf("%s still labelled: %v", what, l)
		}
		if _, ok := l[managed.ReleaseSetLabel]; ok {
			t.Errorf("%s keeps its release set: %v", what, l)
		}
	}
	if dep.Spec.Template.Labels[nameLabel] != "hello" {
		t.Error("selector label removed from the pod template")
	}
	if d, _ := a.Client.Deployed(ctx, "hello"); !d.Found || d.Managed || d.Version != "1.11.0" {
		t.Errorf("deployed after unmanage = %+v", d)
	}
	// The installer no longer adopts what it handed over.
	if err := deployOf(a).Apply(ctx); err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Errorf("deploy after unmanage: %v", err)
	}
}
