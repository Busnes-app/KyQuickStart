package edge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
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
				if r.Header.Get("Forwarded") == "" || r.Header.Get("X-Forwarded-Proto") != "http" {
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
