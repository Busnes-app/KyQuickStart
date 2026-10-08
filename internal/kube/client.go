// Package kube installs Ky products and edge components on a Kubernetes cluster.
package kube

import (
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type Client struct{ cs kubernetes.Interface }

// New wraps a clientset; tests pass a fake one.
func New(cs kubernetes.Interface) *Client { return &Client{cs: cs} }

// Connect loads a kubeconfig the way kubectl does, exec plugins included. An empty context uses
// the file's current context.
func Connect(kubeconfig, context string) (*Client, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: context},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig %s: %w", kubeconfig, err)
	}
	cfg.Timeout = 30 * time.Second
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig %s: %w", kubeconfig, err)
	}
	return New(cs), nil
}
