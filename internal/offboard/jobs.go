package offboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

type Job struct {
	Schema    string    `json:"schema"`
	Binding   string    `json:"binding"`
	Status    string    `json:"status"`
	Requested time.Time `json:"requested"`
	Attempts  uint32    `json:"attempts"`
	Next      time.Time `json:"next"`
}
type Audit struct {
	Schema  string    `json:"schema"`
	Binding string    `json:"binding"`
	Attempt uint32    `json:"attempt"`
	Time    time.Time `json:"time"`
	Outcome string    `json:"outcome"`
}

func (s *Store) job(id string) (Job, error) {
	var j Job
	err := s.read(id+".job", &j)
	if err != nil {
		return j, err
	}
	if j.Schema != "ky.offboarding-job/1" || j.Binding != id || j.Requested.IsZero() || j.Next.IsZero() || (j.Status != "pending" && j.Status != "attempting" && j.Status != "complete") ||
		(j.Status != "pending" && j.Attempts == 0) {
		return j, ErrState
	}
	if j.Status == "complete" {
		for phase, outcome := range map[string]string{"started": "started", "result": "success"} {
			var a Audit
			if s.read(auditName(id, j.Attempts, phase), &a) != nil || a.Schema != "ky.offboarding-audit/1" || a.Binding != id || a.Attempt != j.Attempts || a.Outcome != outcome || a.Time.IsZero() {
				return j, ErrState
			}
		}
	}
	return j, nil
}

// Request publishes irreversible intent before any product call. Concurrent
// delivery of the same SCIM event neither resets attempts nor clears completion.
func (s *Store) Request(id string, now time.Time) (Job, error) {
	if _, err := s.Binding(id); err != nil || now.IsZero() {
		return Job{}, ErrState
	}
	unlock := s.lock(id)
	defer unlock()
	j, err := s.job(id)
	if err == nil {
		return j, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Job{}, ErrState
	}
	j = Job{Schema: "ky.offboarding-job/1", Binding: id, Status: "pending", Requested: now.UTC(), Next: now.UTC()}
	return j, s.write(id+".job", j)
}

// Inspect distinguishes never-requested from unreadable/corrupt state. It never
// infers completed revocation from absence of a job.
func (s *Store) Inspect(id string) (Job, bool, error) {
	if _, err := s.Binding(id); err != nil {
		return Job{}, false, err
	}
	unlock := s.lock(id)
	defer unlock()
	j, err := s.job(id)
	if errors.Is(err, os.ErrNotExist) {
		return Job{}, false, nil
	}
	return j, err == nil, err
}
func backoff(attempt uint32) time.Duration {
	return min(30*time.Second*time.Duration(uint64(1)<<min(attempt-1, 7)), time.Hour)
}
func auditName(id string, attempt uint32, phase string) string {
	return fmt.Sprintf("%s.audit-%010d-%s", id, attempt, phase)
}

// Run invokes the native adapter's inspect/revoke/verify operation. Failures and
// interrupted attempts remain due after backoff, with errors omitted from state.
// Each attempt owns its durable audit outbox file; kyPulse delivery is separate.
func (s *Store) Run(ctx context.Context, id string, now time.Time, revoke func(context.Context, Binding) error) error {
	b, err := s.Binding(id)
	if err != nil || revoke == nil || now.IsZero() {
		return ErrState
	}
	locks := s.account(id)
	if !locks.delivery.TryLock() {
		return ErrPending
	}
	defer locks.delivery.Unlock()
	var j Job
	var a Audit
	err = func() error {
		unlock := s.lock(id)
		defer unlock()
		var err error
		j, err = s.job(id)
		if err != nil {
			return ErrState
		}
		if j.Status == "complete" {
			return nil
		}
		if now.Before(j.Next) {
			return ErrPending
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if j.Attempts == ^uint32(0) {
			return ErrState
		}
		j.Attempts++
		j.Status = "attempting"
		j.Next = now.UTC().Add(time.Minute + backoff(j.Attempts))
		if s.write(id+".job", j) != nil {
			return ErrState
		}
		a = Audit{Schema: "ky.offboarding-audit/1", Binding: id, Attempt: j.Attempts, Time: now.UTC(), Outcome: "started"}
		return s.publish(auditName(id, j.Attempts, "started"), a)
	}()
	if err != nil || j.Status == "complete" {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	started := time.Now()
	err = revoke(callCtx, b)
	cancel()
	a.Time = now.UTC().Add(time.Since(started))
	j.Next = a.Time.Add(backoff(j.Attempts))
	a.Outcome = "success"
	j.Status = "complete"
	if err != nil {
		a.Outcome = "failed"
		j.Status = "pending"
	}
	unlock := s.lock(id)
	defer unlock()
	if s.publish(auditName(id, j.Attempts, "result"), a) != nil || s.write(id+".job", j) != nil {
		return ErrState
	}
	if err != nil {
		return ErrPending
	}
	return nil
}
