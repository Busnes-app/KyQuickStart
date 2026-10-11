package offboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func workerTarget(b Binding, revoke func(context.Context, Binding) error) *Target {
	return &Target{spec: TargetSpec{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, SourceID: b.SourceID}, revoke: revoke}
}
func startWorker(t *testing.T, s *Store, targets ...*Target) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.ServeWorker(ctx, targets) }()
	return cancel, done
}
func stopWorker(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("worker failed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not join")
	}
}
func waitJob(t *testing.T, s *Store, id, status string) Job {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		job, present, err := s.Inspect(id)
		if err != nil {
			t.Fatal(err)
		}
		if present && job.Status == status {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatal("worker state did not progress", job)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestWorkerResumesBackoffAndCompletedProofWithoutRequests(t *testing.T) {
	dir := stateDir(t)
	s := openStore(t, dir)
	defer func() { s.Close() }()
	b := fixtureBinding()
	id, err := s.Bind(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Request(id, time.Now()); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	target := workerTarget(b, func(_ context.Context, retained Binding) error {
		if retained != b {
			t.Error("retained binding changed")
		}
		if calls.Add(1) == 1 {
			return errors.New("PRIVATE-NATIVE-CREDENTIAL")
		}
		return nil
	})
	cancel, done := startWorker(t, s, target)
	deadline := time.Now().Add(4 * time.Second)
	var job Job
	for {
		job, _, err = s.Inspect(id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == "pending" && job.Attempts == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first attempt did not fail durably")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopWorker(t, cancel, done)
	s.Close()
	s = openStore(t, dir)
	cancel, done = startWorker(t, s, target)
	time.Sleep(600 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatal("restart discarded backoff")
	}
	stopWorker(t, cancel, done)
	job.Next = time.Now().Add(-time.Second)
	if err := s.write(id+".job", job); err != nil {
		t.Fatal(err)
	}
	cancel, done = startWorker(t, s, target)
	completed := waitJob(t, s, id, "complete")
	stopWorker(t, cancel, done)
	if completed.Attempts != 2 || calls.Load() != 2 {
		t.Fatal("native completion not proved")
	}
	cancel, done = startWorker(t, s, target)
	time.Sleep(600 * time.Millisecond)
	stopWorker(t, cancel, done)
	if calls.Load() != 2 {
		t.Fatal("completed native operation replayed")
	}
	for _, phase := range []string{"started", "result"} {
		if _, err := os.Stat(filepath.Join(dir, auditName(id, 2, phase))); err != nil {
			t.Fatal("audit proof lost")
		}
	}
}

func TestWorkerBoundsCallsAndJoinsCanceledAttempts(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	b := fixtureBinding()
	ids := make([]string, 7)
	for i := range ids {
		account := b
		account.Subject = "worker-subject-" + string(rune('a'+i))
		account.NativeID += int64(i)
		var err error
		ids[i], err = s.Bind(account)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Request(ids[i], time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	started := make(chan struct{}, 7)
	var active, peak atomic.Int32
	target := workerTarget(b, func(ctx context.Context, _ Binding) error {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if current <= old || peak.CompareAndSwap(old, current) {
				break
			}
		}
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	cancel, done := startWorker(t, s, target)
	for range 4 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("bounded work did not start")
		}
	}
	if err := s.ServeWorker(context.Background(), []*Target{target}); !errors.Is(err, ErrState) {
		t.Fatal("second local worker accepted")
	}
	time.Sleep(600 * time.Millisecond)
	if active.Load() != 4 || peak.Load() != 4 {
		t.Fatal("worker concurrency exceeded bound")
	}
	stopWorker(t, cancel, done)
	if active.Load() != 0 {
		t.Fatal("worker returned before native calls joined")
	}
	var attempted, unattempted int
	for _, id := range ids {
		job, present, err := s.Inspect(id)
		if err != nil || !present || job.Status != "pending" {
			t.Fatal("cancellation lost intent", job, err)
		}
		if job.Attempts == 1 {
			attempted++
		} else if job.Attempts == 0 {
			unattempted++
		} else {
			t.Fatal("duplicate native delivery")
		}
	}
	if attempted != 4 || unattempted != 3 {
		t.Fatal("unattempted backlog changed", attempted, unattempted)
	}
}

func TestWorkerFailsClosedOnConfigurationAndState(t *testing.T) {
	for _, mode := range []string{"missing target", "wrong target", "bad job", "missing proof", "unsafe directory"} {
		t.Run(mode, func(t *testing.T) {
			dir := stateDir(t)
			s := openStore(t, dir)
			defer s.Close()
			b := fixtureBinding()
			id, err := s.Bind(b)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Request(id, time.Now()); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			target := workerTarget(b, func(context.Context, Binding) error { calls.Add(1); return nil })
			switch mode {
			case "missing target":
				target.spec.App = "other"
			case "wrong target":
				target.spec.Origin = "https://foreign.test"
			case "bad job":
				if err := os.Chmod(filepath.Join(dir, id+".job"), 0644); err != nil {
					t.Fatal(err)
				}
			case "missing proof":
				if err := s.Deliver(context.Background(), target, id, time.Now()); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(dir, auditName(id, 1, "result"))); err != nil {
					t.Fatal(err)
				}
				calls.Store(0)
			case "unsafe directory":
				if err := os.Chmod(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.ServeWorker(ctx, []*Target{target}); !errors.Is(err, ErrState) || calls.Load() != 0 {
				t.Fatal("unsafe worker state delivered", err)
			}
		})
	}
	s := openStore(t, stateDir(t))
	defer s.Close()
	target := workerTarget(fixtureBinding(), func(context.Context, Binding) error { return nil })
	for _, targets := range [][]*Target{nil, {nil}, {target, target}} {
		if err := s.ServeWorker(context.Background(), targets); !errors.Is(err, ErrState) {
			t.Fatal("invalid worker config accepted")
		}
	}
}

func TestWorkerStreamsLargeDirectoryAndFindsNewIntent(t *testing.T) {
	dir := stateDir(t)
	s := openStore(t, dir)
	defer s.Close()
	for i := 0; i < 1100; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("padding-%04d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	b := fixtureBinding()
	target := workerTarget(b, func(context.Context, Binding) error { return nil })
	cancel, done := startWorker(t, s, target)
	defer stopWorker(t, cancel, done)
	time.Sleep(50 * time.Millisecond)
	id, err := s.Bind(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Request(id, time.Now()); err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, s, id, "complete")
	if job.Attempts != 1 {
		t.Fatal("new intent delivered twice")
	}
}

// Actual-product harness: the worker, not another SCIM mutation, finishes the
// retained intent after real backoff. The receiver only observes completion.
func resumeThroughWorker(t *testing.T, s *Store, target *Target, id string, next time.Time) {
	t.Helper()
	token := strings.Repeat("cd", 32)
	receiver, err := NewReceiver(s, target, token)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(receiver)
	defer server.Close()
	read := func() bool {
		t.Helper()
		req, _ := http.NewRequest("GET", server.URL+receiver.base+"/Users/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal("native observer request failed")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		var resource struct {
			ID     string
			Active *bool
		}
		if response.StatusCode != 200 || json.Unmarshal(data, &resource) != nil || resource.ID != id || resource.Active == nil {
			t.Fatal("native observation invalid")
		}
		return *resource.Active
	}
	if !read() {
		t.Fatal("uncompleted native work reported inactive")
	}
	if delay := time.Until(next); delay < 0 || delay > 35*time.Second {
		t.Fatal("unexpected native backoff")
	}
	cancel, done := startWorker(t, s, target)
	defer stopWorker(t, cancel, done)
	deadline := next.Add(5 * time.Second)
	for {
		job, present, err := s.Inspect(id)
		if err != nil || !present {
			t.Fatal("worker lost native intent", err)
		}
		if job.Status == "complete" {
			if job.Attempts != 2 {
				t.Fatal("native work replayed")
			}
			break
		}
		if time.Now().Before(next) && job.Attempts != 1 {
			t.Fatal("worker discarded actual backoff")
		}
		if time.Now().After(deadline) {
			t.Fatal("resident worker did not finish native work")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if read() {
		t.Fatal("native completion not reflected through SCIM")
	}
}
