package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	"github.com/google/uuid"
)

// syncBuffer is a log the refresh goroutine and the test share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// The daily TLS refresh runs on its own after startup and holds the runner as an operation
// would: a request meanwhile is busy, and its failure is logged, redacted.
func TestTLSRefresh(t *testing.T) {
	var log syncBuffer
	s, err := newServer(filepath.Join(shortDir(t), "state"), false, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "0.5.0"}, &log)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(s.plan, "stack.yaml"), []byte(demoPlan), 0o600)
	s.o.Redact.Add("secret-value")
	called, release := make(chan string, 1), make(chan struct{})
	s.refreshAfter = time.Millisecond
	s.refreshTLS = func(ctx context.Context, plan, holder string, o cli.Options) error {
		called <- holder
		<-release
		return errors.New("NPM refused secret-value")
	}
	h := serveHarness(t, s)
	select {
	case holder := <-called:
		if holder != "kyquickstart runner "+s.id+" TLS refresh" {
			t.Fatalf("holder %q", holder)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no refresh after startup")
	}
	code, b := h.do(t, "POST", "/v1/operations", request(uuid.NewString(), "upgrade"))
	refused(t, code, b, 409, suite.Busy)
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(log.String(), "TLS certificate refresh: NPM refused [redacted]") {
		if time.Now().After(deadline) {
			t.Fatalf("log = %s", log.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(log.String(), "secret-value") {
		t.Fatal("secret logged")
	}
	s.mu.Lock()
	busy := s.refreshing
	s.mu.Unlock()
	if busy {
		t.Fatal("still refreshing")
	}
}

// While an operation runs the refresh waits (refreshRetry) and touches nothing.
func TestTLSRefreshWaitsForOperation(t *testing.T) {
	s, err := newServer(filepath.Join(shortDir(t), "state"), false, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "0.5.0"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(s.plan, "stack.yaml"), []byte(demoPlan), 0o600)
	calls := 0
	s.refreshTLS = func(context.Context, string, string, cli.Options) error { calls++; return nil }
	s.running = &record{}
	if s.refresh(context.Background()) || calls != 0 {
		t.Fatalf("refreshed during an operation (%d calls)", calls)
	}
	s.running = nil
	if !s.refresh(context.Background()) || calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
}
