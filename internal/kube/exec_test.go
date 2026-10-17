package kube

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/managed"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestExecOutputIsBounded(t *testing.T) {
	b := boundedExecOutput{limit: 128 * 1024}
	if _, err := b.Write(bytes.Repeat([]byte("a"), 128*1024)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("extra")); err == nil || b.Len() != 128*1024 {
		t.Fatal("unbounded exec stdout")
	}
	if _, err := New(fake.NewClientset()).Exec(context.Background(), "namespace", "pod", "container", []string{"command"}); err == nil {
		t.Fatal("fake client executed")
	}
}

func TestExecInputIsBoundedBeforeConnection(t *testing.T) {
	c := New(fake.NewClientset())
	if _, err := c.ExecInput(context.Background(), "namespace", "pod", "container", []string{"command"}, bytes.Repeat([]byte("a"), (128<<10)+1)); err == nil || err.Error() != "product command input exceeds limit" {
		t.Fatal("unbounded input admitted", err)
	}
	if _, err := c.ExecInput(context.Background(), "namespace", "pod", "container", []string{"command"}, []byte("bounded")); err == nil {
		t.Fatal("fake client executed stdin")
	}
}

func TestAppPodPicksTheOneReadyManagedPod(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	c := New(cs)
	pod := func(name string, managedBy string, ready corev1.ConditionStatus) {
		labels := map[string]string{nameLabel: "kyidentity"}
		if managedBy != "" {
			labels[managed.Label] = managedBy
		}
		cs.CoreV1().Pods("kyq-kyidentity").Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: ready}}}}, metav1.CreateOptions{})
	}
	if _, _, _, err := c.AppPod(ctx, "kyidentity"); err == nil {
		t.Fatal("no pod accepted")
	}
	pod("old", managed.Value, corev1.ConditionFalse)
	pod("foreign", "", corev1.ConditionTrue)
	pod("new", managed.Value, corev1.ConditionTrue)
	ns, name, container, err := c.AppPod(ctx, "kyidentity")
	if err != nil || ns != "kyq-kyidentity" || name != "new" || container != appName {
		t.Fatalf("AppPod = %s/%s/%s, %v", ns, name, container, err)
	}
	pod("second", managed.Value, corev1.ConditionTrue)
	if _, _, _, err := c.AppPod(ctx, "kyidentity"); err == nil || !strings.Contains(err.Error(), "2 ready pods") {
		t.Fatalf("two ready pods: %v", err)
	}
}
