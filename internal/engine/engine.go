// Package engine runs installer steps: Inspect, Apply if needed, then Verify.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"
)

type Step interface {
	ID() string        // "<app>.<step>", unique; names the result file
	InputHash() string // changes force Apply even if a prior run succeeded
	Inspect(ctx context.Context) (done bool, err error)
	Apply(ctx context.Context) error
	Verify(ctx context.Context) error
}

// ErrTransient marks an Apply error worth a bounded retry (image pulls, 429, 503).
var ErrTransient = errors.New("transient")

// Result is one step's record on the workstation. It never holds a secret.
type Result struct {
	Status      string    `json:"status"` // "ok" or "failed"
	InputHash   string    `json:"input_hash"`
	Time        time.Time `json:"time"`
	Diagnostics string    `json:"diagnostics,omitempty"`
}

type Engine struct {
	Dir    string // one <step-id>.json per step
	Out    io.Writer
	Redact *Redactor
	Sleep  func(context.Context, time.Duration) error // nil sleeps for real
}

const attempts = 3

// maxDiagnostics bounds a failure message on screen and in the result file.
const maxDiagnostics = 2048

// Run runs steps in order and stops at the first failure. A step whose last result is ok
// with the same input hash, and whose Verify still passes, is skipped.
func (e *Engine) Run(ctx context.Context, steps []Step) error {
	seen := map[string]bool{}
	for _, s := range steps {
		if seen[s.ID()] {
			return fmt.Errorf("duplicate step %s", s.ID())
		}
		seen[s.ID()] = true
	}
	if err := os.MkdirAll(e.Dir, 0o700); err != nil {
		return err
	}
	for _, s := range steps {
		status, err := e.runStep(ctx, s)
		if err != nil {
			msg := bound(e.Redact.Redact(err.Error()))
			fmt.Fprintf(e.Out, "%-20s failed: %s\n", s.ID(), msg)
			res := Result{Status: "failed", InputHash: s.InputHash(), Time: time.Now().UTC(), Diagnostics: msg}
			return errors.Join(fmt.Errorf("%s: %s", s.ID(), msg), e.write(s.ID(), res))
		}
		if status == "ok" {
			if err := e.write(s.ID(), Result{Status: "ok", InputHash: s.InputHash(), Time: time.Now().UTC()}); err != nil {
				return err
			}
		}
		fmt.Fprintf(e.Out, "%-20s %s\n", s.ID(), status)
	}
	return nil
}

func (e *Engine) runStep(ctx context.Context, s Step) (string, error) {
	if prior, ok := e.read(s.ID()); ok && prior.Status == "ok" && prior.InputHash == s.InputHash() && s.Verify(ctx) == nil {
		return "skipped", nil
	}
	for attempt := 1; ; attempt++ {
		done, err := s.Inspect(ctx)
		if err != nil {
			return "", fmt.Errorf("inspect: %w", err)
		}
		if done {
			break
		}
		err = s.Apply(ctx)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrTransient) || attempt == attempts {
			return "", fmt.Errorf("apply: %w", err)
		}
		if err := e.sleep(ctx, time.Duration(attempt)*2*time.Second); err != nil {
			return "", err
		}
	}
	if err := s.Verify(ctx); err != nil {
		return "", fmt.Errorf("verify: %w", err)
	}
	return "ok", nil
}

// bound keeps the last maxDiagnostics bytes of an already redacted message, on a rune
// boundary. Cutting before redacting could leave a secret's tail.
func bound(msg string) string {
	if len(msg) <= maxDiagnostics {
		return msg
	}
	i := len(msg) - maxDiagnostics
	for i < len(msg) && !utf8.RuneStart(msg[i]) {
		i++
	}
	return msg[i:]
}

func (e *Engine) sleep(ctx context.Context, d time.Duration) error {
	if e.Sleep != nil {
		return e.Sleep(ctx, d)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func (e *Engine) read(id string) (Result, bool) {
	b, err := os.ReadFile(filepath.Join(e.Dir, id+".json"))
	if err != nil {
		return Result{}, false
	}
	var r Result
	return r, json.Unmarshal(b, &r) == nil
}

func (e *Engine) write(id string, r Result) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(e.Dir, id+".json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(e.Dir, id+".json"))
}
