package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/edge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// ServiceDialer reaches port of the installer's Service namespace/name through the API
// server's pods/portforward: TLS to the API server, then the kubelet into the pod. No local
// port is opened. Each dial picks a ready pod again, so a restarted pod is found.
func (c *Client) ServiceDialer(namespace, name string, port int) edge.Dial {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		pod, target, err := c.backend(ctx, namespace, name, port)
		if err != nil {
			return nil, err
		}
		if c.portForward != nil {
			return c.portForward(ctx, namespace, pod, target)
		}
		return c.forward(ctx, namespace, pod, target)
	}
}

// backend returns a ready pod behind the Service and the container port behind port.
func (c *Client) backend(ctx context.Context, namespace, name string, port int) (string, int, error) {
	svc, found, err := getOwned(ctx, c.cs.CoreV1().Services(namespace), "service", name)
	if err != nil {
		return "", 0, err
	}
	if !found {
		return "", 0, fmt.Errorf("service %s/%s is absent", namespace, name)
	}
	i := slices.IndexFunc(svc.Spec.Ports, func(p corev1.ServicePort) bool { return int(p.Port) == port })
	if i < 0 || svc.Spec.Ports[i].TargetPort.IntValue() == 0 || len(svc.Spec.Selector) == 0 {
		return "", 0, fmt.Errorf("service %s/%s has no port %d with a numeric target", namespace, name, port)
	}
	pods, err := c.cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(svc.Spec.Selector).String()})
	if err != nil {
		return "", 0, fmt.Errorf("pods behind %s/%s: %w", namespace, name, err)
	}
	for _, p := range pods.Items {
		if owned(&p) && p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning && slices.ContainsFunc(p.Status.Conditions, func(c corev1.PodCondition) bool {
			return c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue
		}) {
			return p.Name, svc.Spec.Ports[i].TargetPort.IntValue(), nil
		}
	}
	return "", 0, fmt.Errorf("no ready pod behind service %s/%s", namespace, name)
}

// forward opens one SPDY port-forward connection carrying one TCP stream to the pod's port.
func (c *Client) forward(ctx context.Context, namespace, pod string, port int) (net.Conn, error) {
	if c.config == nil {
		return nil, errors.New("port-forward requires a connected Kubernetes client")
	}
	rt, upgrader, err := spdy.RoundTripperFor(c.config)
	if err != nil {
		return nil, errors.New("cannot prepare a Kubernetes port-forward")
	}
	u := c.cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(pod).SubResource("portforward").URL()
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: rt}, http.MethodPost, u)
	type result struct {
		conn httpstream.Connection
		err  error
	}
	done := make(chan result, 1)
	go func() {
		conn, _, err := dialer.Dial(portforward.PortForwardProtocolV1Name)
		done <- result{conn, err}
	}()
	var conn httpstream.Connection
	select {
	case <-ctx.Done():
		go func() {
			if r := <-done; r.conn != nil {
				r.conn.Close()
			}
		}()
		return nil, ctx.Err()
	case r := <-done:
		if r.err != nil {
			return nil, fmt.Errorf("Kubernetes port-forward to %s/%s failed", namespace, pod)
		}
		conn = r.conn
	}
	headers := http.Header{}
	headers.Set(corev1.StreamType, corev1.StreamTypeError)
	headers.Set(corev1.PortHeader, strconv.Itoa(port))
	headers.Set(corev1.PortForwardRequestIDHeader, "0")
	errStream, err := conn.CreateStream(headers)
	if err == nil {
		errStream.Close() // read-only for us
		headers.Set(corev1.StreamType, corev1.StreamTypeData)
		var data httpstream.Stream
		if data, err = conn.CreateStream(headers); err == nil {
			go func() {
				// The kubelet reports a failed connection to the pod's port here.
				if msg, _ := io.ReadAll(io.LimitReader(errStream, 4096)); len(msg) > 0 {
					conn.Close()
				}
			}()
			return &tunnelConn{Stream: data, conn: conn}, nil
		}
	}
	conn.Close()
	return nil, fmt.Errorf("Kubernetes port-forward to %s/%s failed", namespace, pod)
}

// tunnelConn is one port-forward stream as a net.Conn; closing it ends its SPDY connection.
type tunnelConn struct {
	httpstream.Stream
	conn httpstream.Connection
}

func (t *tunnelConn) Close() error                     { return t.conn.Close() }
func (t *tunnelConn) LocalAddr() net.Addr              { return tunnelAddr{} }
func (t *tunnelConn) RemoteAddr() net.Addr             { return tunnelAddr{} }
func (t *tunnelConn) SetDeadline(time.Time) error      { return nil }
func (t *tunnelConn) SetReadDeadline(time.Time) error  { return nil }
func (t *tunnelConn) SetWriteDeadline(time.Time) error { return nil }

type tunnelAddr struct{}

func (tunnelAddr) Network() string { return "kube-portforward" }
func (tunnelAddr) String() string  { return "kube-portforward" }
