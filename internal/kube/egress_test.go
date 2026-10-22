package kube

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// egressClient answers the egress check's probe with each of answers in turn.
func egressClient(t *testing.T, answers ...string) (*Client, *fake.Clientset, *int) {
	t.Helper()
	cs := fake.NewClientset()
	cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pod := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
		pod.Name, pod.UID = pod.GenerateName+"fixture", types.UID("fixture-check")
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		return true, pod, cs.Tracker().Create(corev1.SchemeGroupVersion.WithResource("pods"), pod, pod.Namespace)
	})
	c := New(cs)
	calls := 0
	c.execCommand = func(_ context.Context, namespace, pod, container string, args []string) ([]byte, error) {
		if namespace != "kyq-euro-office" || pod != "kyq-egress-check-fixture" || container != "check" || args[2] != egressProbe {
			t.Fatalf("exec %s/%s/%s %v", namespace, pod, container, args)
		}
		a := answers[min(calls, len(answers)-1)]
		calls++
		return []byte(a + "\n"), nil
	}
	return c, cs, &calls
}

// The check passes only when the probe is blocked, retries an open result while the policy
// may still be applying, refuses DNS failure and open egress with egress_unenforced, and
// always removes its restricted, unprivileged pod.
func TestProveEgress(t *testing.T) {
	ctx := context.Background()
	egressRetry = 0
	t.Cleanup(func() { egressRetry = 2 * time.Second })
	for _, c := range []struct {
		answers []string
		want    string
		calls   int
	}{
		{[]string{"blocked"}, "", 1},
		{[]string{"open", "open", "blocked"}, "", 3},
		{[]string{"open"}, "reached the API server", 5},
		{[]string{"dns"}, "could not resolve cluster DNS", 5},
	} {
		client, cs, calls := egressClient(t, c.answers...)
		var created *corev1.Pod
		cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
			created = action.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
			return false, nil, nil
		})
		err := client.proveEgress(ctx, "kyq-euro-office", "rs-1")
		if c.want == "" && err != nil || c.want != "" && (!errors.Is(err, ErrEgressUnenforced) || !strings.Contains(err.Error(), c.want)) || *calls != c.calls {
			t.Fatalf("%v: err = %v after %d probes", c.answers, err, *calls)
		}
		sc, ct := created.Spec.SecurityContext, created.Spec.Containers[0]
		if !*sc.RunAsNonRoot || *created.Spec.AutomountServiceAccountToken || ct.Image != CredentialGeneratorImage || *ct.SecurityContext.AllowPrivilegeEscalation || ct.SecurityContext.Capabilities.Drop[0] != "ALL" || !owned(created) {
			t.Fatalf("check pod = %+v", created)
		}
		if pods, _ := cs.CoreV1().Pods("kyq-euro-office").List(ctx, metav1.ListOptions{}); len(pods.Items) != 0 {
			t.Fatalf("check pod left behind: %v", pods.Items)
		}
	}
}
