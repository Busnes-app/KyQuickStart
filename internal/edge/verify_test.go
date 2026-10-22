package edge

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

func TestProductVerificationRejectsMissingAndSpoofedReports(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		good       bool
	}{
		{"correct", `{"clientIp":"192.0.2.20","proxyHeadersTrusted":true,"scheme":"https"}`, true},
		{"missing scheme", `{"clientIp":"192.0.2.20","proxyHeadersTrusted":true}`, false},
		{"untrusted", `{"clientIp":"192.0.2.20","proxyHeadersTrusted":false,"scheme":"https"}`, false},
		{"spoofed", `{"clientIp":"198.51.100.66","proxyHeadersTrusted":true,"scheme":"https"}`, false},
		{"insecure scheme", `{"clientIp":"192.0.2.20","proxyHeadersTrusted":true,"scheme":"http"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Forwarded") == "" || r.Header.Get("X-Forwarded-Proto") != "http" || r.Header.Get("CF-Connecting-IP") == "" {
					t.Error("probe did not challenge header handling")
				}
				fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			err := verifyRoute(context.Background(), s.Client(), s.URL, netip.MustParseAddr("192.0.2.20"))
			if (err == nil) != tc.good {
				t.Fatalf("verification: %v", err)
			}
		})
	}
}

// A lan edge is verified from an allowed client, on the port clients use.
func TestLANVerificationNeedsAnAllowedClient(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	e := stack.Edge{Provider: "lan", AllowCIDRs: []string{"127.0.0.0/8"}, ClientPort: l.Addr().(*net.TCPAddr).Port,
		Routes: []stack.Route{{Hostname: "127.0.0.1", StatusPath: "/api/status"}}}
	err = VerifyRoutes(context.Background(), e, netip.MustParseAddr("192.168.1.50"))
	if err == nil || !strings.Contains(err.Error(), "outside allow_cidrs") {
		t.Fatalf("outside client: %v", err)
	}
	dialled := make(chan bool, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			c.Close()
		}
		dialled <- err == nil
	}()
	err = VerifyRoutes(context.Background(), e, netip.MustParseAddr("127.0.0.1"))
	l.Close() // ends an Accept that never came
	if err == nil || !<-dialled {
		t.Fatalf("allowed client did not probe client_port: %v", err)
	}
}
