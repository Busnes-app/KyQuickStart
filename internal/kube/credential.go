package kube

import (
	"context"
	"fmt"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

const ledgerKey = "kydns_records"

// credentials returns the installer's existing kyq-edge/kyq-credentials.
func (c *Client) credentials(ctx context.Context) (*corev1.Secret, error) {
	s, found, err := getOwned(ctx, c.cs.CoreV1().Secrets(edgeNamespace), "secret", edgeSecret)
	if err == nil && !found {
		err = fmt.Errorf("secret %s/%s is absent; apply the npm app first", edgeNamespace, edgeSecret)
	}
	return s, err
}

// LANDNSLedger reads the LAN DNS ledger from kyq-edge/kyq-credentials; empty when none yet.
func (c *Client) LANDNSLedger(ctx context.Context) ([]byte, error) {
	s, err := c.credentials(ctx)
	if err != nil {
		return nil, err
	}
	return s.Data[ledgerKey], nil
}

// SetLANDNSLedger replaces the ledger, keeping the Secret's other keys.
func (c *Client) SetLANDNSLedger(ctx context.Context, ledger []byte) error {
	s, err := c.credentials(ctx)
	if err != nil {
		return err
	}
	if s.Data == nil {
		s.Data = map[string][]byte{}
	}
	s.Data[ledgerKey] = ledger
	_, err = c.cs.CoreV1().Secrets(edgeNamespace).Update(ctx, s, metav1.UpdateOptions{})
	switch {
	case apierrors.IsConflict(err):
		return fmt.Errorf("secret %s/%s: %w", edgeNamespace, edgeSecret, engine.ErrTransient)
	case err != nil:
		return fmt.Errorf("secret %s/%s: cluster refused the LAN DNS ledger update", edgeNamespace, edgeSecret)
	}
	return nil
}
