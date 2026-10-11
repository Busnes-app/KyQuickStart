package offboard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureBinding() Binding {
	return Binding{SetupID: "271333e2-c221-4601-b249-563b3d828530", App: "wiki", Kind: "bookstack", Origin: "https://wiki.example.test", Issuer: "https://identity.example.test", Subject: "exact-native-subject", NativeID: 3}
}
func stateDir(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func openStore(t *testing.T, path string) *Store {
	t.Helper()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestPrivateStoreOwnershipAndPermanentBinding(t *testing.T) {
	dir := stateDir(t)
	s := openStore(t, dir)
	defer s.Close()
	if duplicate, err := Open(dir); err == nil {
		duplicate.Close()
		t.Fatal("two resident writers accepted")
	}
	b := fixtureBinding()
	id, err := s.Bind(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Binding){func(b *Binding) { b.NativeID++ }, func(b *Binding) { b.SetupID = "371333e2-c221-4601-b249-563b3d828530" }, func(b *Binding) { b.Origin = "https://foreign.test" }} {
		changed := b
		change(&changed)
		if _, err := s.Bind(changed); !errors.Is(err, ErrState) {
			t.Fatal("conflicting account adopted")
		}
	}
	got, err := s.Binding(id)
	if err != nil || got != b {
		t.Fatal("retained native binding changed", err)
	}
	if _, _, err := s.Inspect("../../foreign"); err == nil {
		t.Fatal("path boundary accepted")
	}
	if err := os.Chmod(filepath.Join(dir, id+".binding"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Binding(id); err == nil {
		t.Fatal("shared binding accepted")
	}
	if err := os.Chmod(filepath.Join(dir, id+".binding"), 0600); err != nil {
		t.Fatal(err)
	}
	other := fixtureBinding()
	other.Subject = "another-subject"
	otherID := bindingID(other)
	if err := os.Symlink(filepath.Join(dir, id+".binding"), filepath.Join(dir, otherID+".binding")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bind(other); err == nil {
		t.Fatal("symlink binding adopted")
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Binding(id); err == nil {
		t.Fatal("permission drift accepted by active store")
	}
	if shared, err := Open(dir); err == nil {
		shared.Close()
		t.Fatal("shared directory accepted")
	}
}

func TestRevocationSurvivesFailureRestartAndConcurrentRequests(t *testing.T) {
	dir := stateDir(t)
	s := openStore(t, dir)
	b := fixtureBinding()
	id, err := s.Bind(b)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if _, err := s.Request(id, now); err != nil {
		t.Fatal(err)
	}
	err = s.Run(context.Background(), id, now, func(ctx context.Context, got Binding) error {
		if got != b {
			t.Fatal("wrong native authority selected")
		}
		var a Audit
		if s.read(auditName(id, 1, "started"), &a) != nil || a.Outcome != "started" {
			t.Fatal("mutation preceded durable audit")
		}
		return errors.New("private-token-and-server-body")
	})
	if !errors.Is(err, ErrPending) {
		t.Fatal("failed mutation marked complete", err)
	}
	j, present, err := s.Inspect(id)
	if err != nil || !present || j.Status != "pending" || j.Attempts != 1 || j.Next.Sub(now) < 30*time.Second {
		t.Fatal("failed state/backoff", j, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, dir)
	defer s.Close()
	var calls atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Request(id, j.Next); err != nil {
				t.Error(err)
			}
			if err := s.Run(context.Background(), id, j.Next, func(context.Context, Binding) error { calls.Add(1); return nil }); err != nil {
				if !errors.Is(err, ErrPending) {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("duplicate native delivery: %d", calls.Load())
	}
	completed, present, err := s.Inspect(id)
	if err != nil || !present || completed.Status != "complete" || completed.Attempts != 2 || !completed.Requested.Equal(now) {
		t.Fatal("resume lost intent", completed, err)
	}
	if _, err := s.Bind(b); err != nil {
		t.Fatal("active setup changed retained binding", err)
	}
	if err := s.Run(context.Background(), id, j.Next.Add(time.Hour), func(context.Context, Binding) error { t.Fatal("replayed completed revocation"); return nil }); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "private-token-and-server-body") || strings.HasPrefix(entry.Name(), "tmp-") {
			t.Fatal("leaked native error or staging file")
		}
	}
	// A job marker alone cannot prove completion after a damaged/partial restore.
	if err := os.Remove(filepath.Join(dir, auditName(id, 2, "result"))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Inspect(id); err == nil {
		t.Fatal("missing completion proof accepted")
	}
}

func TestInterruptedAttemptKeepsIntentAndBackoff(t *testing.T) {
	dir := stateDir(t)
	s := openStore(t, dir)
	id, err := s.Bind(fixtureBinding())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	j, err := s.Request(id, now)
	if err != nil {
		t.Fatal(err)
	}
	j.Status = "attempting"
	j.Attempts = 1
	j.Next = now.Add(90 * time.Second)
	if s.write(id+".job", j) != nil {
		t.Fatal("cannot simulate interrupted worker")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, dir)
	defer s.Close()
	if err := s.Run(context.Background(), id, now, func(context.Context, Binding) error { t.Fatal("retry ignored persisted backoff"); return nil }); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if err := s.Run(context.Background(), id, j.Next, func(context.Context, Binding) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if backoff(32) != time.Hour {
		t.Fatal("unbounded retry delay")
	}
}

func TestInspectionAndOtherAccountsRemainAvailableDuringNativeCall(t *testing.T) {
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
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	go func() {
		done <- s.Run(context.Background(), id, now, func(ctx context.Context, got Binding) error {
			close(started)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	<-started
	observed := make(chan error, 1)
	go func() {
		j, present, err := s.Inspect(id)
		if err == nil && (!present || j.Status != "attempting") {
			err = ErrState
		}
		observed <- err
	}()
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("native call blocked state inspection")
	}
	if _, err := s.Request(id, now); err != nil {
		t.Fatal("request blocked or reset active attempt", err)
	}
	if err := s.Run(context.Background(), id, now, func(context.Context, Binding) error { t.Fatal("duplicate native call"); return nil }); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	other := b
	other.App = "other-wiki"
	otherID, err := s.Bind(other)
	if err != nil || otherID == id {
		t.Fatal("app ownership not separate", err)
	}
	if _, err := s.Request(otherID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(context.Background(), otherID, now, func(context.Context, Binding) error { return nil }); err != nil {
		t.Fatal("other app blocked", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
