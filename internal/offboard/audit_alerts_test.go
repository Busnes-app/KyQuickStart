package offboard

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestAuditRequiredAlertCapability(t *testing.T) {
	for _, mode := range []string{"missing", "duplicate", "correct"} {
		t.Run(mode, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts.Add(1)
				}
				w.Header().Set("Ky-Log-Receipt", "ky.log-ingest/1")
				w.Header().Set("Ky-Log-Source", pulseFixtureSource)
				if mode != "missing" {
					w.Header().Add("Ky-Log-Alerts", "ky.offboarding-alerts/1")
				}
				if mode == "duplicate" {
					w.Header().Add("Ky-Log-Alerts", "ky.offboarding-alerts/1")
				}
				w.WriteHeader(204)
			}))
			defer server.Close()
			d, err := NewAuditDestination(server.URL, pulseFixtureToken)
			if err != nil {
				t.Fatal(err)
			}
			d.client = server.Client()
			d.requireAlerts = true
			_, err = d.request(context.Background(), "OPTIONS", "", 0, "")
			if mode == "correct" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrState) {
				t.Fatal("unverified alert capability accepted", err)
			}
			if mode != "correct" {
				s, target, _ := auditFixture(t)
				defer s.Close()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := s.ServeAudit(ctx, d, []*Target{target}); !errors.Is(err, ErrState) {
					t.Fatal("unsupported alert source reached relay", err)
				}
				if _, err := s.root.Stat(auditHeadFile); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("capability refusal wrote relay state", err)
				}
			}
			if posts.Load() != 0 {
				t.Fatal("capability mutated source")
			}
		})
	}
}

// Invoked by the native kyPulse API harness; errors remain fixture-private.
func TestNativePulseFailureRelay(t *testing.T) {
	origin := os.Getenv("KYQ_TEST_PULSE_FAILURE_RELAY_URL")
	if origin == "" {
		t.Skip("explicit native kyPulse failure fixture")
	}
	var material struct {
		Token       string
		Certificate []byte
	}
	if json.Unmarshal([]byte(os.Getenv("KYQ_TEST_PULSE_RELAY_MATERIAL")), &material) != nil {
		t.Fatal("invalid native fixture")
	}
	leaf, err := x509.ParseCertificate(material.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	prior := http.DefaultTransport
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = prior; transport.CloseIdleConnections() }()
	s := openStore(t, stateDir(t))
	defer s.Close()
	b := fixtureBinding()
	id, err := s.Bind(b)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := s.Request(id, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(context.Background(), id, now, func(context.Context, Binding) error { return errors.New("private native fixture failure") }); !errors.Is(err, ErrPending) {
		t.Fatal("failure audit was not retained", err)
	}
	d, err := NewAuditDestination(origin, material.Token)
	if err != nil {
		t.Fatal(err)
	}
	d.requireAlerts = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.ServeAudit(ctx, d, []*Target{auditTarget(b)}) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var head auditHead
		if s.read(auditHeadFile, &head) == nil && head.Sequence == 2 && head.Pending == "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("native failure relay did not receive both acknowledgments")
}
