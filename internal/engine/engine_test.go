package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

type fakeStep struct {
	id, hash          string
	inspect           func() (bool, error)
	apply, verify     func() error
	inspects, applies int
}

func (f *fakeStep) ID() string        { return f.id }
func (f *fakeStep) InputHash() string { return f.hash }
func (f *fakeStep) Inspect(context.Context) (bool, error) {
	f.inspects++
	if f.inspect == nil {
		return false, nil
	}
	return f.inspect()
}
func (f *fakeStep) Apply(context.Context) error {
	f.applies++
	if f.apply == nil {
		return nil
	}
	return f.apply()
}
func (f *fakeStep) Verify(context.Context) error {
	if f.verify == nil {
		return nil
	}
	return f.verify()
}

func newEngine(t *testing.T) (*Engine, *bytes.Buffer, *[]time.Duration) {
	var out bytes.Buffer
	var slept []time.Duration
	e := &Engine{
		Dir:    filepath.Join(t.TempDir(), "results"),
		Out:    &out,
		Redact: &Redactor{},
		Sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	}
	return e, &out, &slept
}

func readResult(t *testing.T, e *Engine, id string) Result {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.Dir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunThenSkip(t *testing.T) {
	e, out, _ := newEngine(t)
	s := &fakeStep{id: "a.one", hash: "h1"}
	if err := e.Run(context.Background(), []Step{s}); err != nil {
		t.Fatal(err)
	}
	if s.applies != 1 || readResult(t, e, "a.one").Status != "ok" {
		t.Fatalf("applies %d, result %+v", s.applies, readResult(t, e, "a.one"))
	}
	out.Reset()
	if err := e.Run(context.Background(), []Step{s}); err != nil {
		t.Fatal(err)
	}
	if s.applies != 1 || s.inspects != 1 || !strings.Contains(out.String(), "skipped") {
		t.Errorf("second run: applies %d, inspects %d, out %q", s.applies, s.inspects, out)
	}
}

func TestChangedHashRerunsStep(t *testing.T) {
	e, _, _ := newEngine(t)
	s := &fakeStep{id: "a.one", hash: "h1"}
	e.Run(context.Background(), []Step{s})
	s.hash = "h2"
	if err := e.Run(context.Background(), []Step{s}); err != nil {
		t.Fatal(err)
	}
	if s.applies != 2 || readResult(t, e, "a.one").InputHash != "h2" {
		t.Errorf("applies %d, result %+v", s.applies, readResult(t, e, "a.one"))
	}
}

// An operator stopped the containers: the skip check's Verify fails, so the step runs again.
func TestFailedVerifyRerunsStep(t *testing.T) {
	e, _, _ := newEngine(t)
	s := &fakeStep{id: "a.deploy", hash: "h"}
	e.Run(context.Background(), []Step{s})
	stopped := true
	s.verify = func() error {
		if stopped {
			return errors.New("service web is not running")
		}
		return nil
	}
	s.apply = func() error { stopped = false; return nil }
	if err := e.Run(context.Background(), []Step{s}); err != nil {
		t.Fatal(err)
	}
	if s.applies != 2 {
		t.Errorf("applies = %d, want a redeploy", s.applies)
	}
}

func TestInspectDoneSkipsApply(t *testing.T) {
	e, _, _ := newEngine(t)
	s := &fakeStep{id: "a.one", hash: "h", inspect: func() (bool, error) { return true, nil }}
	if err := e.Run(context.Background(), []Step{s}); err != nil {
		t.Fatal(err)
	}
	if s.applies != 0 {
		t.Errorf("applies = %d", s.applies)
	}
}

// A transient Apply error is followed by a fresh Inspect: the work may have landed.
func TestTransientInspectsBeforeRetry(t *testing.T) {
	e, _, slept := newEngine(t)
	landed := false
	s := &fakeStep{id: "a.pull", hash: "h",
		inspect: func() (bool, error) { return landed, nil },
		apply: func() error {
			landed = true
			return fmt.Errorf("pull: %w", ErrTransient)
		},
	}
	if err := e.Run(context.Background(), []Step{s}); err != nil {
		t.Fatal(err)
	}
	if s.applies != 1 || s.inspects != 2 || len(*slept) != 1 {
		t.Errorf("applies %d, inspects %d, sleeps %v", s.applies, s.inspects, *slept)
	}
}

func TestTransientGivesUp(t *testing.T) {
	e, _, slept := newEngine(t)
	s := &fakeStep{id: "a.pull", hash: "h", apply: func() error { return ErrTransient }}
	if err := e.Run(context.Background(), []Step{s}); err == nil {
		t.Fatal("no error")
	}
	if s.applies != 3 || len(*slept) != 2 {
		t.Errorf("applies %d, sleeps %v", s.applies, *slept)
	}
}

func TestStopsAtFirstFailure(t *testing.T) {
	e, _, _ := newEngine(t)
	bad := &fakeStep{id: "a.one", hash: "h", apply: func() error { return errors.New("boom") }}
	next := &fakeStep{id: "a.two", hash: "h"}
	err := e.Run(context.Background(), []Step{bad, next})
	if err == nil || !strings.Contains(err.Error(), "a.one") {
		t.Fatalf("err = %v", err)
	}
	if bad.applies != 1 || next.inspects != 0 {
		t.Errorf("bad applies %d, next inspects %d", bad.applies, next.inspects)
	}
	if r := readResult(t, e, "a.one"); r.Status != "failed" || !strings.Contains(r.Diagnostics, "boom") {
		t.Errorf("result %+v", r)
	}
}

func TestFailureRedactsSecrets(t *testing.T) {
	e, out, _ := newEngine(t)
	secret := "s3cr3t-value-that-leaked"
	e.Redact.Add(secret)
	s := &fakeStep{id: "a.up", hash: "h", apply: func() error {
		return errors.New("docker compose up: exit status 1: DB_PASSWORD=" + secret)
	}}
	err := e.Run(context.Background(), []Step{s})
	if err == nil {
		t.Fatal("no error")
	}
	raw, _ := os.ReadFile(filepath.Join(e.Dir, "a.up.json"))
	for where, text := range map[string]string{"error": err.Error(), "output": out.String(), "result": string(raw)} {
		if strings.Contains(text, secret) {
			t.Errorf("secret in %s: %s", where, text)
		}
		if !strings.Contains(text, "[redacted]") {
			t.Errorf("%s lacks the redaction marker: %s", where, text)
		}
	}
}

func TestDuplicateStepIDs(t *testing.T) {
	e, _, _ := newEngine(t)
	a, b := &fakeStep{id: "a.one"}, &fakeStep{id: "a.one"}
	if err := e.Run(context.Background(), []Step{a, b}); err == nil || a.inspects != 0 {
		t.Fatalf("err %v, inspects %d", err, a.inspects)
	}
}

func TestRedactorIgnoresShortValues(t *testing.T) {
	r := &Redactor{}
	r.Add("abc")
	if got := r.Redact("abc"); got != "abc" {
		t.Errorf("short value redacted: %q", got)
	}
}

func TestLongFailureRedactsBeforeBounding(t *testing.T) {
	e, out, _ := newEngine(t)
	secret := "Zq7Kp2VwN9bRt4LmQ8sHc3JdF6gA1eUoT5iYk0nBvXa" // 43 bytes
	e.Redact.Add(secret)
	// The old 2048-byte cut of stderr fell 20 bytes into the secret.
	stderr := strings.Repeat("x", 3000) + secret + strings.Repeat("y", 2048-20)
	s := &fakeStep{id: "a.up", hash: "h", apply: func() error {
		return &remote.ExitError{Code: 1, Stderr: []byte(stderr)}
	}}
	err := e.Run(context.Background(), []Step{s})
	if err == nil {
		t.Fatal("no error")
	}
	raw, _ := os.ReadFile(filepath.Join(e.Dir, "a.up.json"))
	for where, text := range map[string]string{"error": err.Error(), "output": out.String(), "result": string(raw)} {
		for i := 0; i+8 <= len(secret); i++ {
			if strings.Contains(text, secret[i:i+8]) {
				t.Fatalf("secret fragment %q in %s", secret[i:i+8], where)
			}
		}
	}
	if d := readResult(t, e, "a.up").Diagnostics; len(d) > maxDiagnostics {
		t.Errorf("diagnostics are %d bytes", len(d))
	}
}

func TestBoundedFailureIsValidUTF8(t *testing.T) {
	e, out, _ := newEngine(t)
	// "apply: " is 7 bytes, so the 2048-byte cut lands inside a 3-byte rune.
	s := &fakeStep{id: "a.up", hash: "h", apply: func() error {
		return errors.New(strings.Repeat("€", 1500))
	}}
	err := e.Run(context.Background(), []Step{s})
	if err == nil {
		t.Fatal("no error")
	}
	d := readResult(t, e, "a.up").Diagnostics
	for where, text := range map[string]string{"error": err.Error(), "output": out.String(), "diagnostics": d} {
		if !utf8.ValidString(text) || strings.ContainsRune(text, utf8.RuneError) {
			t.Errorf("%s is not clean UTF-8", where)
		}
	}
	if len(d) > maxDiagnostics || len(d) < maxDiagnostics-2 {
		t.Errorf("diagnostics are %d bytes", len(d))
	}
}
