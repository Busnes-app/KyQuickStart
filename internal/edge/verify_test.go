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
	_, err = VerifyRoutes(context.Background(), e, netip.MustParseAddr("192.168.1.50"))
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
	_, err = VerifyRoutes(context.Background(), e, netip.MustParseAddr("127.0.0.1"))
	l.Close() // ends an Accept that never came
	if err == nil || !<-dialled {
		t.Fatalf("allowed client did not probe client_port: %v", err)
	}
}

// One run reaches one side of a mixed edge: internal routes from inside allow_cidrs, external
// routes from outside.
func TestMixedEdgeVerifiesOneSidePerClient(t *testing.T) {
	internal := stack.Route{Hostname: "pulse.lan.example.com", Exposure: "internal"}
	external := stack.Route{Hostname: "notes.example.com", Exposure: "external"}
	e := stack.Edge{Provider: "cloudflare", AllowCIDRs: []string{"192.168.1.0/24"}, ClientPort: 443}
	e.Routes = []stack.Route{external}
	if _, err := VerifyRoutes(context.Background(), e, netip.MustParseAddr("192.168.1.50")); err == nil || !strings.Contains(err.Error(), "inside allow_cidrs") {
		t.Fatalf("LAN client on an edge with no internal routes: %v", err)
	}
	e.Routes = []stack.Route{internal}
	if _, err := VerifyRoutes(context.Background(), e, netip.MustParseAddr("203.0.113.9")); err == nil || !strings.Contains(err.Error(), "outside allow_cidrs") {
		t.Fatalf("outside client on an edge with only internal routes: %v", err)
	}
}
