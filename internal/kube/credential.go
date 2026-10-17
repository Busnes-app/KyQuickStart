package kube

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EdgeToken reads the installer's NPM token from kyq-edge/kyq-credentials.
func (c *Client) EdgeToken(ctx context.Context) ([]byte, error) {
	return c.EdgeSecret(ctx, edgeSecret, edgeTokenKey)
}

// EdgeSecret reads key from an existing installer-labelled Secret in kyq-edge; neither it nor
// its namespace is created by this read. Secret values never appear in returned errors.
func (c *Client) EdgeSecret(ctx context.Context, name, key string) ([]byte, error) {
	ns, err := c.cs.CoreV1().Namespaces().Get(ctx, edgeNamespace, metav1.GetOptions{})
	if err != nil || !owned(ns) {
		return nil, fmt.Errorf("edge credential namespace %s is absent or not installer-owned", edgeNamespace)
	}
	s, err := c.cs.CoreV1().Secrets(edgeNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil || !owned(s) {
		return nil, fmt.Errorf("secret %s/%s is absent or lacks the ky.managed-by=kyquickstart label", edgeNamespace, name)
	}
	v := s.Data[key]
	if len(v) == 0 || len(v) > 16384 {
		return nil, fmt.Errorf("secret %s/%s key %s is absent, empty or oversized", edgeNamespace, name, key)
	}
	return v, nil
}
