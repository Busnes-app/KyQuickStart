package offboard

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const pulseFixtureSource = "ab1333e2-c221-4601-b249-563b3d828530"
const pulseFixtureToken = "audit-fixture-token"

func auditTarget(b Binding) *Target {
	return &Target{spec: TargetSpec{SetupID: b.SetupID, App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer}, revoke: func(context.Context, Binding) error { return nil }}
}

func auditFixture(t *testing.T) (*Store, *Target, string) {
	t.Helper()
	s := openStore(t, stateDir(t))
	b := fixtureBinding()
	id, err := s.Bind(b)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := s.Request(id, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(context.Background(), id, now, func(context.Context, Binding) error { return nil }); err != nil {
		t.Fatal(err)
	}
	return s, auditTarget(b), id
}

func TestAuditLostReplyRetainsExactBodyAcrossRestart(t *testing.T) {
	s, target, id := auditFixture(t)
	var posts atomic.Int32
	var original string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+pulseFixtureToken {
			t.Error("wrong credential")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Ky-Log-Receipt", "ky.log-ingest/1")
		if r.Method == "OPTIONS" {
			w.Header().Set("Ky-Log-Source", pulseFixtureSource)
			w.WriteHeader(204)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if posts.Add(1) == 1 {
			original = string(body)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			connection.Close()
			return
		}
		if string(body) != original || r.Header.Get("Ky-Log-Sequence") != "1" {
			t.Error("lost-response retry changed body or sequence")
		}
		w.Header().Set("Ky-Log-Sequence", "1")
		w.Header().Set("Ky-Log-Digest", bodyDigest(string(body)))
		w.WriteHeader(204)
	}))
	defer server.Close()
	trustFixture(t, server)
	destination, err := NewAuditDestination(server.URL, pulseFixtureToken)
	if err != nil {
		t.Fatal(err)
	}
	source, err := destination.request(context.Background(), http.MethodOptions, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]*Target{target.spec.App: target}
	head, err := s.loadAuditHead(destination, source, target.spec.SetupID, targets)
	if err != nil {
		t.Fatal(err)
	}
	head.Sequence = 1
	head.Pending = auditName(id, 1, "started")
	head.Body, err = s.auditBody(head.Pending, targets)
	if err != nil {
		t.Fatal(err)
	}
	head.Digest = bodyDigest(head.Body)
	if err := s.write(auditHeadFile, head); err != nil {
		t.Fatal(err)
	}
	if err := s.deliverAudit(context.Background(), destination, head, targets, time.Now()); !errors.Is(err, ErrAuditPending) {
		t.Fatal("lost reply treated as success", err)
	}
	path := s.root.Name()
	s.Close()
	s = openStore(t, path)
	defer s.Close()
	head, err = s.loadAuditHead(destination, source, target.spec.SetupID, targets)
	if err != nil || head.Sequence != 1 || head.Attempts != 1 || time.Until(head.Next) < 25*time.Second {
		t.Fatal("restart lost retry intent", head, err)
	}
	if err := s.deliverAudit(context.Background(), destination, head, targets, time.Now()); !errors.Is(err, ErrAuditPending) || posts.Load() != 1 {
		t.Fatal("persisted backoff ignored", err)
	}
	if err := s.deliverAudit(context.Background(), destination, head, targets, head.Next); err != nil {
		t.Fatal(err)
	}
	completed, err := s.loadAuditHead(destination, source, target.spec.SetupID, targets)
	if err != nil || completed.Pending != "" || completed.Sent != head.Pending || completed.Sequence != 1 {
		t.Fatal("receipt not committed", completed, err)
	}
	// Crash after durable receipt but before the head advances requires no network retry.
	if err := s.write(auditHeadFile, head); err != nil {
		t.Fatal(err)
	}
	if err := s.deliverAudit(context.Background(), destination, head, targets, head.Next); err != nil || posts.Load() != 2 {
		t.Fatal("durable receipt replayed native I/O", err)
	}
	if err := os.Remove(filepath.Join(path, head.Pending+".sent")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.loadAuditHead(destination, source, target.spec.SetupID, targets); !errors.Is(err, ErrState) {
		t.Fatal("missing latest acknowledgment accepted", err)
	}
	if strings.Contains(fmt.Sprintf("%v %#v %v %#v", destination, destination, *destination, *destination), pulseFixtureToken) {
		t.Fatal("diagnostic credential leak")
	}
}

func TestAuditServiceOrderingAndMissingHistoricalReceiptRefusal(t *testing.T) {
	s, target, id := auditFixture(t)
	defer s.Close()
	late := fixtureBinding()
	late.Subject += "-late"
	lateID := bindingID(late)
	var posts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Ky-Log-Receipt", "ky.log-ingest/1")
		if r.Method == "OPTIONS" {
			w.Header().Set("Ky-Log-Source", pulseFixtureSource)
			w.WriteHeader(204)
			return
		}
		body, _ := io.ReadAll(r.Body)
		n := posts.Add(1)
		if r.Header.Get("Ky-Log-Sequence") != strconv.Itoa(int(n)) {
			t.Error("out of order sequence")
		}
		var row struct{ Line string }
		json.Unmarshal(body, &row)
		var event struct {
			Outcome, Resource, Hash string
			Seq                     int64
		}
		json.Unmarshal([]byte(row.Line), &event)
		want := "started"
		if n%2 == 0 {
			want = "success"
		}
		resource, sequence := id, int64(n)
		if n > 2 {
			resource, sequence = lateID, int64(n-2)
		}
		if event.Outcome != want || event.Seq != sequence || event.Resource != "wiki:"+resource || len(event.Hash) != 64 || strings.Contains(row.Line, fixtureBinding().Subject) {
			t.Error("invalid safe audit projection")
		}
		w.Header().Set("Ky-Log-Sequence", strconv.Itoa(int(n)))
		w.Header().Set("Ky-Log-Digest", bodyDigest(string(body)))
		w.WriteHeader(204)
	}))
	defer server.Close()
	trustFixture(t, server)
	destination, err := NewAuditDestination(server.URL, pulseFixtureToken)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.ServeAudit(ctx, destination, []*Target{target}) }()
	waitSequence := func(sequence int64) {
		deadline := time.Now().Add(8 * time.Second)
		for {
			select {
			case err := <-done:
				t.Fatalf("audit service stopped before completion: %v", err)
			default:
			}
			var head auditHead
			if s.read(auditHeadFile, &head) == nil && head.Sequence == sequence && head.Pending == "" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("audit service did not complete")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitSequence(2)
	// Add work after the reader has opened/scanned the directory. It must
	// discover both new audit files and receipts without restarting the relay.
	if got, err := s.Bind(late); err != nil || got != lateID {
		t.Fatal("late binding failed", err)
	}
	now := time.Now().UTC()
	if _, err := s.Request(lateID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(context.Background(), lateID, now, func(context.Context, Binding) error { return nil }); err != nil {
		t.Fatal(err)
	}
	waitSequence(4)
	if err := s.ServeAudit(ctx, destination, []*Target{target}); !errors.Is(err, ErrState) {
		t.Fatal("two relay writers accepted")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.root.Name(), auditName(id, 1, "started")+".sent")); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel2()
	if err := s.ServeAudit(ctx2, destination, []*Target{target}); !errors.Is(err, ErrState) || posts.Load() != 4 {
		t.Fatal("missing historical receipt resent an old audit", err)
	}
}

func TestAuditRetryAfterKeepsPendingAndSafeDiagnostics(t *testing.T) {
	s, target, id := auditFixture(t)
	defer s.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(429)
		io.WriteString(w, "PRIVATE-SERVER-ERROR-TOKEN")
	}))
	defer server.Close()
	trustFixture(t, server)
	destination, err := NewAuditDestination(server.URL, pulseFixtureToken)
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]*Target{target.spec.App: target}
	head, err := s.loadAuditHead(destination, pulseFixtureSource, target.spec.SetupID, targets)
	if err != nil {
		t.Fatal(err)
	}
	head.Sequence = 1
	head.Pending = auditName(id, 1, "started")
	head.Body, err = s.auditBody(head.Pending, targets)
	if err != nil {
		t.Fatal(err)
	}
	head.Digest = bodyDigest(head.Body)
	if err := s.write(auditHeadFile, head); err != nil {
		t.Fatal(err)
	}
	err = s.deliverAudit(context.Background(), destination, head, targets, time.Now())
	if !errors.Is(err, ErrAuditPending) || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("unsafe retry diagnostic", err)
	}
	head, err = s.loadAuditHead(destination, pulseFixtureSource, target.spec.SetupID, targets)
	if err != nil || head.Pending == "" || head.Attempts != 1 || time.Until(head.Next) < 115*time.Second {
		t.Fatal("server retry hint was not persisted", err)
	}
	raw, err := os.ReadFile(filepath.Join(s.root.Name(), auditHeadFile))
	if err != nil || strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), pulseFixtureToken) {
		t.Fatal("native diagnostic/credential entered state")
	}
}

func TestAuditCapabilityAndReceiptRefusals(t *testing.T) {
	for _, mode := range []string{"old server", "redirect", "bad digest", "duplicate sequence", "source changed"} {
		t.Run(mode, func(t *testing.T) {
			s, target, id := auditFixture(t)
			defer s.Close()
			var posts atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					w.Header().Set("Location", "https://foreign.invalid/")
					w.WriteHeader(307)
					return
				}
				if mode != "old server" {
					w.Header().Set("Ky-Log-Receipt", "ky.log-ingest/1")
				}
				if r.Method == "OPTIONS" {
					w.Header().Set("Ky-Log-Source", pulseFixtureSource)
					w.WriteHeader(204)
					return
				}
				posts.Add(1)
				body, _ := io.ReadAll(r.Body)
				w.Header().Set("Ky-Log-Sequence", "1")
				w.Header().Set("Ky-Log-Digest", bodyDigest(string(body)))
				if mode == "bad digest" {
					w.Header().Set("Ky-Log-Digest", strings.Repeat("00", 32))
				}
				if mode == "duplicate sequence" {
					w.Header().Add("Ky-Log-Sequence", "1")
				}
				w.WriteHeader(204)
			}))
			defer server.Close()
			trustFixture(t, server)
			destination, err := NewAuditDestination(server.URL, pulseFixtureToken)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "source changed" {
				if err := s.write(auditHeadFile, auditHead{Schema: "ky.offboarding-relay/1", Origin: server.URL, Source: "foreign", SetupID: target.spec.SetupID}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.ServeAudit(ctx, destination, []*Target{target}); !errors.Is(err, ErrState) {
				t.Fatal("unsafe response accepted", err)
			}
			var ack auditAcknowledgment
			if s.read(auditName(id, 1, "started")+".sent", &ack) == nil {
				t.Fatal("invalid receipt marked durable")
			}
			if (mode == "old server" || mode == "redirect" || mode == "source changed") && posts.Load() != 0 {
				t.Fatal("capability/source refusal followed mutation")
			}
		})
	}
}

// Launched only by the native kyPulse test harness, using its real API and store.
func TestNativePulseAuditRelay(t *testing.T) {
	origin := os.Getenv("KYQ_TEST_PULSE_RELAY_URL")
	if origin == "" {
		t.Skip("native kyPulse fixture")
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
	transport := http.DefaultTransport.(*http.Transport).Clone()
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	transport.Proxy = nil
	prior := http.DefaultTransport
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = prior; transport.CloseIdleConnections() }()
	s, target, id := auditFixture(t)
	path := s.root.Name()
	destination, err := NewAuditDestination(origin, material.Token)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.ServeAudit(ctx, destination, []*Target{target}) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var head auditHead
		if s.read(auditHeadFile, &head) == nil && head.Attempts == 1 && time.Until(head.Next) < 35*time.Second {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			s.Close()
			t.Fatal("native lost reply was not persisted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	s.Close()
	directory := stateDir(t)
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	cert, key, _ := runtimeCertificate(t)
	for name, data := range map[string][]byte{"server.pem": cert, "server-key.pem": key, "product-token": []byte(strings.Repeat("ab", 32)), "scim-token": []byte(strings.Repeat("cd", 32)), "pulse-token": []byte(material.Token), "pulse-ca.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: material.Certificate})} {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	b := fixtureBinding()
	cfg := runtimeFile{Schema: "ky.offboarding-runtime/1", SetupID: b.SetupID, Listen: address, StateDir: path, CertFile: "server.pem", KeyFile: "server-key.pem",
		Apps: []runtimeAppFile{{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, TokenFile: "product-token", SCIMFile: "scim-token"}}, Audit: &runtimeAuditFile{Origin: origin, TokenFile: "pulse-token"}}
	file := filepath.Join(directory, "config.json")
	writeRuntime(t, file, cfg)
	ctx, cancel = context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	binary := filepath.Join(directory, "kyoffboard")
	if err := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/kyoffboard").Run(); err != nil {
		t.Fatal("native relay binary build failed")
	}
	command := exec.CommandContext(ctx, binary, "--config", file)
	command.Env = append(os.Environ(), "SSL_CERT_FILE="+filepath.Join(directory, "pulse-ca.pem"))
	if err := command.Start(); err != nil {
		t.Fatal("native relay binary start failed")
	}
	done = make(chan error, 1)
	go func() { done <- command.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			command.Process.Kill()
			<-done
		}
	}()
	root, err := openPrivateRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	observer := &Store{root: root}
	for {
		var head auditHead
		if observer.read(auditHeadFile, &head) == nil && head.Sequence == 2 && head.Pending == "" {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("native retained retry did not complete")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatal("native relay SIGTERM failed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("native relay binary did not join")
	}
	s = openStore(t, path)
	defer s.Close()
	for _, phase := range []string{"started", "result"} {
		var ack auditAcknowledgment
		if s.read(auditName(id, 1, phase)+".sent", &ack) != nil || ack.Source == "" {
			t.Fatal("native acknowledgment missing")
		}
	}
}
