package kube

import (
	"context"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func testApp(t *testing.T, secrets ...string) (App, *fake.Clientset) {
	t.Helper()
	cs := fake.NewClientset()
	c := fixture(t)
	c.Secrets = secrets
	return App{Name: "hello", Client: New(cs), Catalog: c, ReleaseSet: "rs-1", Redact: &engine.Redactor{}}, cs
}

func TestSecretsCreatedOnce(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token", "db_password")
	s := secretsStep{a}
	if done, err := s.Inspect(ctx); err != nil || done {
		t.Fatalf("Inspect before = %v %v", done, err)
	}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	ns, err := cs.CoreV1().Namespaces().Get(ctx, "kyq-hello", metav1.GetOptions{})
	if err != nil || ns.Labels[podSecurity] != "restricted" || !owned(ns) {
		t.Fatalf("namespace = %+v, %v", ns, err)
	}
	sec, _ := cs.CoreV1().Secrets("kyq-hello").Get(ctx, secretName, metav1.GetOptions{})
	first := string(sec.Data["token"])
	if len(first) != 43 || len(sec.Data["db_password"]) != 43 || !owned(sec) {
		t.Fatalf("secret = %+v", sec)
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Redact.Redact("x"+first) != "x[redacted]" {
		t.Error("secret not registered for redaction")
	}
	if done, _ := s.Inspect(ctx); !done {
		t.Fatal("Inspect after Apply is not done")
	}
}

func TestSecretsAddOnlyMissing(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token", "db_password")
	if err := a.ensureNamespace(ctx); err != nil {
		t.Fatal(err)
	}
	keep := strings.Repeat("k", 43)
	cs.CoreV1().Secrets("kyq-hello").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "kyq-hello", Labels: workloadLabels("rs-0")},
		Data:       map[string][]byte{"token": []byte(keep)},
	}, metav1.CreateOptions{})
	s := secretsStep{a}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	sec, _ := cs.CoreV1().Secrets("kyq-hello").Get(ctx, secretName, metav1.GetOptions{})
	if string(sec.Data["token"]) != keep || len(sec.Data["db_password"]) != 43 {
		t.Fatalf("data = %q", sec.Data)
	}
}

func TestSecretsVerifyRejectsWrongLength(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token")
	s := secretsStep{a}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	sec, _ := cs.CoreV1().Secrets("kyq-hello").Get(ctx, secretName, metav1.GetOptions{})
	sec.Data["token"] = []byte("short")
	cs.CoreV1().Secrets("kyq-hello").Update(ctx, sec, metav1.UpdateOptions{})
	err := s.Verify(ctx)
	if err == nil || strings.Contains(err.Error(), "short") {
		t.Fatalf("err = %v (must fail without printing the value)", err)
	}
}

func TestNamespaceNotOwned(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token")
	cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kyq-hello"}}, metav1.CreateOptions{})
	err := secretsStep{a}.Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "not managed by kyquickstart") {
		t.Fatalf("err = %v", err)
	}
	if _, err := cs.CoreV1().Secrets("kyq-hello").Get(ctx, secretName, metav1.GetOptions{}); err == nil {
		t.Fatal("wrote a secret into a namespace it does not own")
	}
}

func TestNoSecretsIsDone(t *testing.T) {
	a, _ := testApp(t)
	if done, err := (secretsStep{a}).Inspect(context.Background()); err != nil || !done {
		t.Fatalf("Inspect = %v %v", done, err)
	}
}
