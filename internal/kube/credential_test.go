package kube

import (
	"context"
	"strings"
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

func TestEdgeSecretNeedsTheManagedLabel(t *testing.T) {
	owned := map[string]string{managed.Label: managed.Value}
	cs := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kyq-edge", Labels: owned}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cloudflare-dns", Namespace: "kyq-edge", Labels: owned}, Data: map[string][]byte{"api_token": []byte("cf-value")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unlabelled", Namespace: "kyq-edge"}, Data: map[string][]byte{"api_token": []byte("cf-value")}},
	)
	c := New(cs)
	if b, err := c.EdgeSecret(context.Background(), "cloudflare-dns", "api_token"); err != nil || string(b) != "cf-value" {
		t.Fatalf("read %q %v", b, err)
	}
	for _, ref := range [][2]string{{"unlabelled", "api_token"}, {"cloudflare-dns", "other"}, {"absent", "api_token"}} {
		if _, err := c.EdgeSecret(context.Background(), ref[0], ref[1]); err == nil || strings.Contains(err.Error(), "cf-value") {
			t.Fatalf("%v: %v", ref, err)
		}
	}
	for _, action := range cs.Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("credential read mutated the cluster: %s", action.GetVerb())
		}
	}
}

func TestLANDNSLedgerKeepsOtherKeysAndNeedsOwnedSecret(t *testing.T) {
	ctx := context.Background()
	owned := map[string]string{managed.Label: managed.Value}
	cs := fake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kyq-credentials", Namespace: "kyq-edge", Labels: owned}, Data: map[string][]byte{"npm_token": []byte("npm-value")}})
	c := New(cs)
	if b, err := c.LANDNSLedger(ctx); err != nil || len(b) != 0 {
		t.Fatalf("empty ledger: %q %v", b, err)
	}
	if err := c.SetLANDNSLedger(ctx, []byte(`[{"id":1}]`)); err != nil {
		t.Fatal(err)
	}
	s, _ := cs.CoreV1().Secrets("kyq-edge").Get(ctx, "kyq-credentials", metav1.GetOptions{})
	if string(s.Data["npm_token"]) != "npm-value" || string(s.Data["kydns_records"]) != `[{"id":1}]` {
		t.Fatalf("secret data %v", s.Data)
	}
	if b, _ := c.LANDNSLedger(ctx); string(b) != `[{"id":1}]` {
		t.Fatalf("ledger %q", b)
	}

	foreign := New(fake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kyq-credentials", Namespace: "kyq-edge"}}))
	if err := foreign.SetLANDNSLedger(ctx, []byte(`[]`)); err == nil {
		t.Fatal("wrote an unlabelled Secret")
	}
	if err := New(fake.NewClientset()).SetLANDNSLedger(ctx, []byte(`[]`)); err == nil {
		t.Fatal("created the Secret")
	}
}
