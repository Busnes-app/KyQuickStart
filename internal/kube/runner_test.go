package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	runnerA = "0b6c1f3e-2a1d-4f6e-9c3b-5d7e8f9a0b1c"
	runnerB = "9b6c1f3e-2a1d-4f6e-9c3b-5d7e8f9a0b1c"
)

// tokenController fills every created token Secret, as the cluster's controller does.
func tokenController(cs *fake.Clientset) *int {
	issued := 0
	cs.PrependReactor("create", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		s := a.(k8stesting.CreateAction).GetObject().(*corev1.Secret)
		if s.Type == corev1.SecretTypeServiceAccountToken {
			issued++
			s.UID = types.UID(fmt.Sprint("uid-", issued))
			s.Data = map[string][]byte{"token": []byte(fmt.Sprint("token-", issued)), "ca.crt": []byte("CA")}
		}
		return false, nil, nil
	})
	return &issued
}

func TestRunnerRulesAreTheInstallersOwn(t *testing.T) {
	cluster, lock := RunnerRules()
	n := 0
	for _, r := range append(slices.Clone(cluster), lock...) {
		for _, v := range r.Verbs {
			n++
			if v == "*" || r.Resources[0] == "*" || r.APIGroups[0] == "*" {
				t.Errorf("wildcard rule %+v", r)
			}
		}
		if r.Resources[0] == "clusterroles" || r.Resources[0] == "rolebindings" || r.Resources[0] == "clusterrolebindings" {
			t.Errorf("the runner may grant access: %+v", r)
		}
	}
	if n != len(required) {
		t.Errorf("%d verbs granted, preflight requires %d", n, len(required))
	}
	if len(lock) != 1 || lock[0].Resources[0] != "leases" {
		t.Errorf("lock namespace rules = %+v", lock)
	}
}

func TestGrantAndRevokeRunner(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	issued := tokenController(cs)
	c := New(cs)
	c.config = &rest.Config{Host: "https://cluster.lan:6443"}

	kc, err := c.GrantRunner(ctx, runnerA, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := clientcmd.Load(kc)
	if err != nil {
		t.Fatal(err)
	}
	cur := cfg.Contexts[cfg.CurrentContext]
	if cfg.Clusters[cur.Cluster].Server != "https://cluster.lan:6443" || string(cfg.Clusters[cur.Cluster].CertificateAuthorityData) != "CA" || cfg.AuthInfos[cur.AuthInfo].Token != "token-1" {
		t.Fatalf("kubeconfig = %s", kc)
	}
	sa, err := cs.CoreV1().ServiceAccounts(lockNamespace).Get(ctx, runnerName, metav1.GetOptions{})
	if err != nil || *sa.AutomountServiceAccountToken {
		t.Fatalf("service account: %v %+v", err, sa)
	}
	crb, _ := cs.RbacV1().ClusterRoleBindings().Get(ctx, runnerName, metav1.GetOptions{})
	if crb.RoleRef.Name != runnerName || crb.Subjects[0].Name != runnerName || crb.Subjects[0].Namespace != lockNamespace {
		t.Errorf("binding = %+v", crb)
	}

	// A rerun keeps the token; another runner gets a new one.
	if _, err := c.GrantRunner(ctx, runnerA, false); err != nil || *issued != 1 {
		t.Fatalf("rerun: %v, %d tokens", err, *issued)
	}
	kc, err = c.GrantRunner(ctx, runnerB, false)
	if err != nil || *issued != 2 || !strings.Contains(string(kc), "token-2") {
		t.Fatalf("new runner: %v, %d tokens\n%s", err, *issued, kc)
	}

	for range 2 {
		if err := c.RevokeRunner(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cs.RbacV1().ClusterRoles().Get(ctx, runnerName, metav1.GetOptions{}); err == nil {
		t.Error("cluster role left after revoke")
	}
	if _, err := cs.CoreV1().Secrets(lockNamespace).Get(ctx, runnerToken, metav1.GetOptions{}); err == nil {
		t.Error("token left after revoke")
	}
}

func TestGrantRunnerRefusesForeignObjectsAndInsecureClusters(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: runnerName, Namespace: lockNamespace}})
	c := New(cs)
	c.config = &rest.Config{Host: "https://cluster.lan:6443"}
	if _, err := c.GrantRunner(ctx, runnerA, false); err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("foreign service account: %v", err)
	}
	if err := c.RevokeRunner(ctx); err == nil {
		t.Fatal("revoke removed a foreign service account")
	}
	c.config.Insecure = true
	if _, err := c.GrantRunner(ctx, runnerA, false); err == nil {
		t.Fatal("granted over an unverified connection")
	}
}

func TestGrantRunnerInCluster(t *testing.T) {
	cs := fake.NewClientset()
	tokenController(cs)
	c := New(cs)
	c.config = &rest.Config{Host: "https://cluster.lan:6443"}
	for inCluster, want := range map[bool]string{true: "https://kubernetes.default.svc", false: "https://cluster.lan:6443"} {
		kc, err := c.GrantRunner(context.Background(), runnerA, inCluster)
		if err != nil {
			t.Fatal(err)
		}
		cfg, _ := clientcmd.Load(kc)
		cl := cfg.Clusters[cfg.Contexts[cfg.CurrentContext].Cluster]
		if cl.Server != want || string(cl.CertificateAuthorityData) != "CA" || inCluster != (cl.TLSServerName == "kubernetes.default.svc") {
			t.Errorf("in cluster %v: %+v", inCluster, cl)
		}
	}
}

func TestParseRunnerPod(t *testing.T) {
	if ns, sel, err := ParseRunnerPod("kyq-runner/app=runner,tier in (a)"); err != nil || ns != "kyq-runner" || sel != "app=runner,tier in (a)" {
		t.Errorf("%q %q %v", ns, sel, err)
	}
	for _, bad := range []string{"", "kyq-runner", "kyq-runner/", "Bad_NS/app=runner", "/app=runner", "kyq-runner/app=(", "a/b/=c"} {
		if _, _, err := ParseRunnerPod(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// runnerCluster is a fake cluster with the given pods in kyq-runner, which allows exec only
// when allowed.
func runnerCluster(allowed bool, pods ...*corev1.Pod) *Client {
	var objs []runtime.Object
	for _, p := range pods {
		objs = append(objs, p)
	}
	cs := fake.NewClientset(objs...)
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		r := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		r.Status.Allowed = allowed || r.Spec.ResourceAttributes.Subresource == ""
		return true, r, nil
	})
	return New(cs)
}

func runnerPod(name string, phase corev1.PodPhase, container string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kyq-runner", Labels: map[string]string{"app": "runner"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "kyyard"}, {Name: container}}},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func TestExecRunner(t *testing.T) {
	ctx := context.Background()
	other := runnerPod("other", corev1.PodRunning, "runner")
	other.Labels["app"] = "kyyard"
	c := runnerCluster(true, runnerPod("runner-1", corev1.PodRunning, "runner"), runnerPod("runner-0", corev1.PodSucceeded, "runner"), other)
	var got []string
	c.execStream = func(_ context.Context, ns, pod, container string, args []string, o remotecommand.StreamOptions) error {
		in, _ := io.ReadAll(o.Stdin)
		got = append(got, ns, pod, container, strings.Join(args, " "), string(in))
		o.Stdout.Write([]byte("out"))
		if string(in) == "fail" {
			o.Stderr.Write([]byte("import: older plan\n"))
			return errors.New("command terminated with exit code 1")
		}
		return nil
	}
	out, err := c.ExecRunner(ctx, "kyq-runner", "app=runner", []string{"kyquickstart", "serve-import"}, strings.NewReader("tar"))
	if err != nil || string(out) != "out" || strings.Join(got, "|") != "kyq-runner|runner-1|runner|kyquickstart serve-import|tar" {
		t.Fatalf("%q %v %q", out, err, got)
	}
	if _, err := c.ExecRunner(ctx, "kyq-runner", "app=runner", nil, strings.NewReader("fail")); err == nil || !strings.Contains(err.Error(), "exit code 1: import: older plan") {
		t.Errorf("stderr not in the error: %v", err)
	}
	c.execStream = func(_ context.Context, _, _, _ string, _ []string, o remotecommand.StreamOptions) error {
		_, err := o.Stdout.Write(make([]byte, maxRunnerOutput+1))
		return err
	}
	if _, err := c.ExecRunner(ctx, "kyq-runner", "app=runner", nil, nil); err == nil {
		t.Error("unbounded stdout")
	}
}

func TestExecRunnerNeedsOneRunnerPodAndExec(t *testing.T) {
	ran := false
	for name, c := range map[string]*Client{
		"0 running pods":   runnerCluster(true, runnerPod("runner-0", corev1.PodPending, "runner")),
		"2 running pods":   runnerCluster(true, runnerPod("runner-0", corev1.PodRunning, "runner"), runnerPod("runner-1", corev1.PodRunning, "runner")),
		"no container":     runnerCluster(true, runnerPod("runner-0", corev1.PodRunning, "sidecar")),
		"denied pods/exec": runnerCluster(false, runnerPod("runner-0", corev1.PodRunning, "runner")),
	} {
		c.execStream = func(context.Context, string, string, string, []string, remotecommand.StreamOptions) error {
			ran = true
			return nil
		}
		_, err := c.ExecRunner(context.Background(), "kyq-runner", "app=runner", nil, nil)
		if err == nil || !strings.Contains(err.Error(), map[string]string{"0 running pods": "0 running pods", "2 running pods": "2 running pods",
			"no container": `no container "runner"`, "denied pods/exec": "denied create pods/exec"}[name]) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if ran {
		t.Error("executed in a refused pod")
	}
}
