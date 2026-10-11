package kube

import (
	"context"
	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EdgeToken reads an existing installer-owned credential; neither it nor its namespace
// is created by this read. Secret values never appear in returned errors.
func (c *Client) EdgeToken(ctx context.Context) ([]byte, error) {
	ns, err := c.cs.CoreV1().Namespaces().Get(ctx, "kyq-edge", metav1.GetOptions{})
	if err != nil || !owned(ns) {
		return nil, errors.New("edge credential namespace is absent or not installer-owned")
	}
	s, err := c.cs.CoreV1().Secrets("kyq-edge").Get(ctx, "kyq-credentials", metav1.GetOptions{})
	if err != nil || !owned(s) {
		return nil, errors.New("edge credential Secret is absent or not installer-owned")
	}
	token := s.Data["npm_token"]
	if len(token) == 0 || len(token) > 16384 {
		return nil, errors.New("edge npm_token is absent, empty or oversized")
	}
	return token, nil
}
