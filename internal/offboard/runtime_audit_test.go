package offboard

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeAuditConfigurationAndJoinedDelivery(t *testing.T) {
	for _, cancelInFlight := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelInFlight), func(t *testing.T) {
			file, cfg, _, _ := runtimeFixture(t)
			var posts atomic.Int32
			started := make(chan struct{}, 1)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+pulseFixtureToken {
					t.Error("wrong audit credential")
					w.WriteHeader(401)
					return
				}
				w.Header().Set("Ky-Log-Receipt", "ky.log-ingest/1")
				if r.Method == "OPTIONS" {
					w.Header().Set("Ky-Log-Source", pulseFixtureSource)
					w.Header().Set("Ky-Log-Alerts", "ky.offboarding-alerts/1")
					w.WriteHeader(204)
					return
				}
				posts.Add(1)
				body, _ := io.ReadAll(r.Body)
				if cancelInFlight {
					started <- struct{}{}
					<-r.Context().Done()
					return
				}
				w.Header().Set("Ky-Log-Sequence", r.Header.Get("Ky-Log-Sequence"))
				w.Header().Set("Ky-Log-Digest", bodyDigest(string(body)))
				w.WriteHeader(204)
			}))
			defer server.Close()
			prior := http.DefaultTransport
			transport := prior.(*http.Transport).Clone()
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
			transport.TLSClientConfig.RootCAs = transport.TLSClientConfig.RootCAs.Clone()
			transport.TLSClientConfig.RootCAs.AddCert(server.Certificate())
			transport.TLSClientConfig.MinVersion = tls.VersionTLS12
			http.DefaultTransport = transport
			defer func() { http.DefaultTransport = prior; transport.CloseIdleConnections() }()
			cfg.Audit = &runtimeAuditFile{Origin: server.URL, TokenFile: "pulse-token", RequireAlerts: true}
			if err := os.WriteFile(filepath.Join(filepath.Dir(file), "pulse-token"), []byte(pulseFixtureToken), 0600); err != nil {
				t.Fatal(err)
			}
			writeRuntime(t, file, cfg)
			loaded, err := LoadRuntime(file)
			if err != nil || loaded.audit == nil || !loaded.audit.requireAlerts {
				t.Fatal("private audit configuration rejected", err)
			}
			s := openStore(t, cfg.StateDir)
			b := fixtureBinding()
			b.Origin = cfg.Apps[0].Origin
			id, err := s.Register(context.Background(), loaded.apps[0].target, b)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Request(id, time.Now()); err != nil {
				t.Fatal(err)
			}
			s.Close()
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- loaded.Serve(ctx) }()
			stopped := false
			defer func() {
				if !stopped {
					cancel()
					<-done
				}
			}()
			if cancelInFlight {
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("runtime did not dispatch audit")
				}
			} else {
				deadline := time.Now().Add(8 * time.Second)
				for {
					select {
					case err := <-done:
						stopped = true
						t.Fatalf("runtime stopped before audit acknowledgment: %v", err)
					default:
					}
					var head auditHead
					root, err := openPrivateRoot(cfg.StateDir)
					if err != nil {
						t.Fatal(err)
					}
					observer := &Store{root: root}
					err = observer.read(auditHeadFile, &head)
					root.Close()
					if err == nil && head.Sequence == 2 && head.Pending == "" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("runtime did not acknowledge audit phases")
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
			cancel()
			select {
			case err := <-done:
				stopped = true
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("runtime audit shutdown did not join")
			}
			s = openStore(t, cfg.StateDir)
			defer s.Close()
			var head auditHead
			if s.read(auditHeadFile, &head) != nil {
				t.Fatal("runtime audit state absent")
			}
			if cancelInFlight {
				if head.Pending == "" || head.Attempts != 1 {
					t.Fatal("cancelled audit lost retry intent")
				}
			} else if head.Sequence != 2 || head.Pending != "" || posts.Load() != 2 {
				t.Fatal("runtime audit not completed once")
			}
			// Audit credentials cannot reuse product or SCIM authority.
			if err := os.WriteFile(filepath.Join(filepath.Dir(file), "pulse-token"), []byte(strings.Repeat("cd", 32)), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadRuntime(file); !errors.Is(err, ErrRuntime) {
				t.Fatal("reused audit/SCIM credential accepted")
			}
		})
	}
}
