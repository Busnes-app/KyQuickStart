package kube

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/managed"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type probeConn struct {
	net.Conn
	alert bool
}

func (c *probeConn) Write(b []byte) (int, error) {
	if len(b) > 0 && b[0] == 21 { // TLS 1.2 alert record, including close_notify.
		c.alert = true
	}
	return c.Conn.Write(b)
}

// Real TLS handshakes prove SNI, exact-leaf matching and checking every ready
// managed pod. A mixed rollout must hold the old copy even if the first pod
// already loaded the replacement. Certificate validity backdating is irrelevant.
func TestTLSCertificateServed(t *testing.T) {
	const host = "drive.example.com"
	servers := make([]*httptest.Server, 2)
	var want []byte
	for i := range servers {
		_, cert, key, err := serverCertificate(host, time.Now().Add(-2*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		pair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			t.Fatal(err)
		}
		servers[i] = httptest.NewUnstartedServer(nil)
		servers[i].TLS = &tls.Config{MaxVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			if hello.ServerName != host {
				return nil, fmt.Errorf("wrong SNI: %s", hello.ServerName)
			}
			return nil, nil
		}}
		servers[i].StartTLS()
		t.Cleanup(servers[i].Close)
		if i == 0 {
			block, _ := pem.Decode(cert)
			want = block.Bytes
		}
	}
	for _, app := range []string{"nginx", "npm"} {
		t.Run(app, func(t *testing.T) {
			ctx := context.Background()
			cs := fake.NewClientset()
			for _, name := range []string{"a", "b", "unready", "foreign"} {
				p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespaceOf(app), Labels: selector(app)},
					Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
				if name != "foreign" {
					p.Labels[managed.Label] = managed.Value
				}
				if name == "unready" {
					p.Status.Conditions = nil
				}
				if _, err := cs.CoreV1().Pods(p.Namespace).Create(ctx, p, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			c := New(cs)
			mixed, unreachable := true, false
			var asked []string
			var connections []*probeConn
			c.portForward = func(ctx context.Context, ns, pod string, port int) (net.Conn, error) {
				if ns != namespaceOf(app) || port != 443 || (pod != "a" && pod != "b") {
					t.Fatalf("unexpected target %s/%s:%d", ns, pod, port)
				}
				asked = append(asked, pod)
				if unreachable {
					return nil, io.ErrUnexpectedEOF
				}
				i := 0
				if mixed && pod == "b" {
					i = 1
				}
				conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", servers[i].Listener.Addr().String())
				if err != nil {
					return nil, err
				}
				watched := &probeConn{Conn: conn}
				connections = append(connections, watched)
				return watched, nil
			}
			if err := c.TLSCertificateServed(ctx, app, host, want); err == nil || !slices.Equal(asked, []string{"a", "b"}) {
				t.Fatalf("mixed rollout accepted or pods missed: %v, %v", err, asked)
			}
			mixed, asked = false, nil
			connections = nil
			if err := c.TLSCertificateServed(ctx, app, host, want); err != nil || !slices.Equal(asked, []string{"a", "b"}) {
				t.Fatalf("loaded rollout refused: %v, %v", err, asked)
			}
			for _, conn := range connections {
				if conn.alert {
					t.Fatal("successful probe wrote TLS close_notify; tunnel write deadlines are ineffective")
				}
			}
			if err := c.TLSCertificateServed(ctx, app, "wrong.example.com", want); err == nil {
				t.Fatal("wrong SNI accepted")
			}
			unreachable = true
			if err := c.TLSCertificateServed(ctx, app, host, want); err == nil {
				t.Fatal("unreachable pod accepted")
			}
			if err := New(fake.NewClientset()).TLSCertificateServed(ctx, app, host, want); err == nil {
				t.Fatal("empty pod set accepted")
			}
			// A tunnel that accepts no TLS bytes must end on caller cancellation.
			left, right := net.Pipe()
			defer right.Close()
			c.portForward = func(context.Context, string, string, int) (net.Conn, error) { return left, nil }
			short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
			if err := c.TLSCertificateServed(short, app, host, want); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("stalled handshake did not honor cancellation: %v", err)
			}
		})
	}
}
