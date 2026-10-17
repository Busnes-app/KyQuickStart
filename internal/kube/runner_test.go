package kube

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
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

	kc, err := c.GrantRunner(ctx, runnerA)
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
	if _, err := c.GrantRunner(ctx, runnerA); err != nil || *issued != 1 {
		t.Fatalf("rerun: %v, %d tokens", err, *issued)
	}
	kc, err = c.GrantRunner(ctx, runnerB)
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
	if _, err := c.GrantRunner(ctx, runnerA); err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("foreign service account: %v", err)
	}
	if err := c.RevokeRunner(ctx); err == nil {
		t.Fatal("revoke removed a foreign service account")
	}
	c.config.Insecure = true
	if _, err := c.GrantRunner(ctx, runnerA); err == nil {
		t.Fatal("granted over an unverified connection")
	}
}
