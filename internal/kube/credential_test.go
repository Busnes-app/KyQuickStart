package kube

import (
	"context"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/managed"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEdgeCredentialRequiresOwnedObjectsAndOnlyReads(t *testing.T) {
	for _, tc := range []struct {
		name              string
		namespace, secret bool
		token             string
		ok                bool
	}{
		{"owned", true, true, "private-token", true},
		{"foreign namespace", false, true, "private-token", false},
		{"foreign secret", true, false, "private-token", false},
		{"empty", true, true, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			labels := func(owned bool) map[string]string {
				if owned {
					return map[string]string{managed.Label: managed.Value}
				}
				return nil
			}
			cs := fake.NewClientset(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kyq-edge", Labels: labels(tc.namespace)}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kyq-credentials", Namespace: "kyq-edge", Labels: labels(tc.secret)}, Data: map[string][]byte{"npm_token": []byte(tc.token)}},
			)
			b, err := New(cs).EdgeToken(context.Background())
			if (err == nil) != tc.ok || tc.ok && string(b) != tc.token {
				t.Fatal("credential ownership or contents were not enforced")
			}
			for _, action := range cs.Actions() {
				if action.GetVerb() != "get" {
					t.Fatalf("credential read mutated the cluster: %s", action.GetVerb())
				}
			}
		})
	}
}
