package kube

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kt "k8s.io/client-go/testing"
)

func TestClusterGeneratorSecurityAndCleanup(t *testing.T) {
	a, cs := testApp(t, "token")
	var seen *corev1.Pod
	cs.PrependReactor("create", "pods", func(action kt.Action) (bool, runtime.Object, error) {
		seen = action.(kt.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
		return false, nil, nil
	})
	if err := (secretsStep{a}).Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen == nil || !owned(seen) || seen.Name != "" || seen.GenerateName == "" || *seen.Spec.AutomountServiceAccountToken || *seen.Spec.ActiveDeadlineSeconds != 180 || seen.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatal("unbounded, shared or credentialed generator pod")
	}
	c := seen.Spec.Containers[0]
	if c.Image != CredentialGeneratorImage || !strings.Contains(c.Image, "@sha256:") || *c.SecurityContext.AllowPrivilegeEscalation || !*c.SecurityContext.ReadOnlyRootFilesystem || len(c.SecurityContext.Capabilities.Drop) != 1 || c.SecurityContext.Capabilities.Drop[0] != "ALL" || c.Resources.Limits.Memory().String() != "32Mi" || *seen.Spec.SecurityContext.RunAsUser == 0 {
		t.Fatal("generator confinement not enforced")
	}
	pods, err := cs.CoreV1().Pods("kyq-hello").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(pods.Items) != 0 {
		t.Fatal("generator pod left after success", err)
	}
	for _, action := range cs.Actions() {
		if action.GetVerb() == "delete" && action.GetResource().Resource == "pods" {
			opts := action.(kt.DeleteAction).GetDeleteOptions()
			if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != "fixture-generator" {
				t.Fatal("cleanup can delete a replacement pod")
			}
		}
	}
}

func TestClusterGenerationFailureIsNonmutatingAndPrivate(t *testing.T) {
	for _, mode := range []string{"exec-failure", "malformed", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			a, cs := testApp(t, "token")
			marker := strings.Repeat("X", 43)
			a.Client.execCommand = func(context.Context, string, string, string, []string) ([]byte, error) {
				if mode == "exec-failure" {
					return []byte(marker), errors.New(marker)
				}
				if mode == "malformed" {
					return []byte(strings.Repeat("!", 43)), nil
				}
				return []byte(strings.Repeat("A", 43)), nil
			}
			if mode == "cleanup" {
				cs.PrependReactor("delete", "pods", func(kt.Action) (bool, runtime.Object, error) { return true, nil, errors.New(marker) })
			}
			err := (secretsStep{a}).Apply(context.Background())
			if err == nil || strings.Contains(err.Error(), marker) {
				t.Fatal("failed generation accepted or exposed output")
			}
			if _, err := cs.CoreV1().Secrets("kyq-hello").Get(context.Background(), secretName, metav1.GetOptions{}); err == nil {
				t.Fatal("failed generation published credentials")
			}
			if mode != "cleanup" {
				pods, err := cs.CoreV1().Pods("kyq-hello").List(context.Background(), metav1.ListOptions{})
				if err != nil || len(pods.Items) != 0 {
					t.Fatal("failed generation did not clean up", err)
				}
			}
		})
	}
}

func TestClusterRetainsEmptyExistingCredentialForInspection(t *testing.T) {
	a, cs := testApp(t, "token")
	if err := a.ensureNamespace(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := cs.CoreV1().Secrets("kyq-hello").Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "kyq-hello", Labels: workloadLabels("rs")}, Data: map[string][]byte{"token": {}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a.Client.execCommand = func(context.Context, string, string, string, []string) ([]byte, error) {
		t.Fatal("regenerated an existing empty credential")
		return nil, nil
	}
	if err := (secretsStep{a}).Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := (secretsStep{a}).Verify(context.Background()); err == nil {
		t.Fatal("empty credential accepted")
	}
}

func TestClusterGeneratorCancellationStillCleansUp(t *testing.T) {
	a, cs := testApp(t, "token")
	ctx, cancel := context.WithCancel(context.Background())
	a.Client.execCommand = func(context.Context, string, string, string, []string) ([]byte, error) {
		cancel()
		return nil, context.Canceled
	}
	if err := (secretsStep{a}).Apply(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not preserved", err)
	}
	pods, err := cs.CoreV1().Pods("kyq-hello").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(pods.Items) != 0 {
		t.Fatal("cancelled generator retained pod", err)
	}
}
