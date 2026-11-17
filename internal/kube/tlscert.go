package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// TLSCertificateServed confirms that every ready managed edge pod serves leaf for
// host. The authenticated Kubernetes port-forward reaches each pod directly, so
// DNS, public routing and a load balancer cannot hide a pod that has not reloaded.
func (c *Client) TLSCertificateServed(ctx context.Context, app, host string, leaf []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ns := namespaceOf(app)
	pods, err := c.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(selector(app)).String()})
	if err != nil {
		return fmt.Errorf("list %s pods: %w", app, err)
	}
	checked := 0
	for _, p := range pods.Items {
		if !owned(&p) || !podReady(&p) {
			continue
		}
		dial := c.forward
		if c.portForward != nil {
			dial = c.portForward
		}
		conn, err := dial(ctx, ns, p.Name, edgeHTTPS)
		if err != nil {
			return fmt.Errorf("TLS probe of %s/%s: %w", ns, p.Name, err)
		}
		client := tls.Client(conn, &tls.Config{
			ServerName: host,
			// The exact leaf from the authenticated cluster API is the trust
			// anchor. The caller already checked its hostname and validity.
			InsecureSkipVerify: true,
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 || !bytes.Equal(cs.PeerCertificates[0].Raw, leaf) {
					return errors.New("edge has not loaded the replacement certificate")
				}
				return nil
			},
		})
		err = client.HandshakeContext(ctx)
		// No application data was sent. Close the tunnel directly: tls.Close
		// writes close_notify, while a Kubernetes tunnel ignores write deadlines.
		conn.Close()
		if err != nil {
			return fmt.Errorf("TLS probe of %s/%s for %s: %w", ns, p.Name, host, err)
		}
		checked++
	}
	if checked == 0 {
		return fmt.Errorf("no ready managed pod of %s to verify", app)
	}
	return nil
}
