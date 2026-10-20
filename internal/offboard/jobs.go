package offboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// Job is one epoch of an account's access state. Epoch 1 keeps the original
// revocation file names and schema; later epochs alternate reactivation (even)
// and revocation (odd) in their own files. A completed epoch is never rewritten.
type Job struct {
	Schema    string            `json:"schema"`
	Binding   string            `json:"binding"`
	Epoch     uint32            `json:"epoch,omitempty"`
	Status    string            `json:"status"`
	Requested time.Time         `json:"requested"`
	Attempts  uint32            `json:"attempts"`
	Next      time.Time         `json:"next"`
	Profile   *DirectoryProfile `json:"profile,omitempty"`
}
type Audit struct {
	Schema  string    `json:"schema"`
	Binding string    `json:"binding"`
	Epoch   uint32    `json:"epoch,omitempty"`
	Attempt uint32    `json:"attempt"`
	Time    time.Time `json:"time"`
	Outcome string    `json:"outcome"`
}

// maxEpochs is odd so a revocation epoch always fits after the last reactivation.
const maxEpochs = 1001

func reactivating(epoch uint32) bool { return epoch%2 == 0 }

func epochName(id string, epoch uint32) string {
	if epoch == 1 {
		return id + ".job"
	}
	return fmt.Sprintf("%s.epoch-%010d", id, epoch)
}
func auditName(id string, attempt uint32, phase string) string {
	return fmt.Sprintf("%s.audit-%010d-%s", id, attempt, phase)
}

// Later epoch audits avoid ".audit-": the kyPulse relay owns those names.
func epochAuditName(id string, epoch, attempt uint32, phase string) string {
	if epoch == 1 {
		return auditName(id, attempt, phase)
	}
	return fmt.Sprintf("%s.epoch-%010d-audit-%010d-%s", id, epoch, attempt, phase)
}

// schemas returns the job/audit schemas and the stored epoch field (0 for epoch 1).
func schemas(epoch uint32) (string, string, uint32) {
	if epoch == 1 {
		return "ky.offboarding-job/1", "ky.offboarding-audit/1", 0
	}
	return "ky.offboarding-epoch/1", "ky.offboarding-epoch-audit/1", epoch
}

var refusals = map[string]error{"reactivation_unsupported": ErrReactivationUnsupported, "administrator_refused": ErrReactivationRefused, "native_refused": ErrNativeRefused}

func (s *Store) epochAudit(id string, epoch, attempt uint32, phase string) (Audit, error) {
	var a Audit
	if err := s.read(epochAuditName(id, epoch, attempt, phase), &a); err != nil {
		return a, err
	}
	_, schema, stamp := schemas(epoch)
	_, refusal := refusals[a.Outcome]
	outcome := a.Outcome == "success" || a.Outcome == "failed" || (reactivating(epoch) && refusal)
	if a.Schema != schema || a.Epoch != stamp || a.Binding != id || a.Attempt != attempt || a.Time.IsZero() ||
		(phase == "started" && a.Outcome != "started") || (phase == "result" && !outcome) {
		return a, ErrState
	}
	return a, nil
}

func (s *Store) epochJob(id string, epoch uint32) (Job, error) {
	var j Job
	if err := s.read(epochName(id, epoch), &j); err != nil {
		return j, err
	}
	schema, _, stamp := schemas(epoch)
	if j.Schema != schema || j.Epoch != stamp || j.Binding != id || j.Requested.IsZero() || j.Next.IsZero() ||
		(j.Status != "pending" && j.Status != "attempting" && j.Status != "complete" && (j.Status != "refused" || !reactivating(epoch))) ||
		(j.Status != "pending" && j.Attempts == 0) || (j.Profile != nil && (!reactivating(epoch) || !validDirectoryProfile(*j.Profile))) {
		return j, ErrState
	}
	if j.Status == "complete" || j.Status == "refused" {
		result, err := s.epochAudit(id, epoch, j.Attempts, "result")
		_, refused := refusals[result.Outcome]
		if _, startErr := s.epochAudit(id, epoch, j.Attempts, "started"); startErr != nil || err != nil ||
			(j.Status == "complete") != (result.Outcome == "success") || (j.Status == "refused") != refused {
			return j, ErrState
		}
	}
	return j, nil
}

func (s *Store) job(id string) (Job, error) { return s.epochJob(id, 1) }

// epochs returns the latest epoch and whether the latest completed epoch left the
// account active. A revocation must complete before a reactivation follows it;
// an unfinished reactivation may be superseded by the next revocation.
func (s *Store) epochs(id string) (Job, uint32, bool, error) {
	latest, err := s.epochJob(id, 1)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := s.epochJob(id, 2); !errors.Is(err, os.ErrNotExist) {
			return Job{}, 0, false, ErrState
		}
		return Job{}, 0, true, os.ErrNotExist
	}
	active := true
	for epoch := uint32(1); ; epoch++ {
		if err != nil {
			return Job{}, 0, false, ErrState
		}
		if latest.Status == "complete" {
			active = reactivating(epoch)
		}
		next, nextErr := s.epochJob(id, epoch+1)
		if errors.Is(nextErr, os.ErrNotExist) {
			return latest, epoch, active, nil
		}
		if epoch >= maxEpochs || (!reactivating(epoch) && latest.Status != "complete") {
			return Job{}, 0, false, ErrState
		}
		latest, err = next, nextErr
	}
}

// latest is the locked view used by reads and the worker.
func (s *Store) latest(id string) (Job, uint32, bool, error) {
	if _, err := s.Binding(id); err != nil {
		return Job{}, 0, false, err
	}
	unlock := s.lock(id)
	defer unlock()
	return s.epochs(id)
}

// Request publishes revocation intent before any product call. Concurrent
// delivery of the same SCIM event neither resets attempts nor clears completion.
// After a reactivation, in any state, it starts the next revocation epoch.
func (s *Store) Request(id string, now time.Time) (Job, error) {
	if _, err := s.Binding(id); err != nil || now.IsZero() {
		return Job{}, ErrState
	}
	unlock := s.lock(id)
	defer unlock()
	j, epoch, _, err := s.epochs(id)
	if err == nil && !reactivating(epoch) {
		return j, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Job{}, ErrState
	}
	return s.startEpoch(id, epoch+1, now, nil)
}

func (s *Store) startEpoch(id string, epoch uint32, now time.Time, p *DirectoryProfile) (Job, error) {
	schema, _, stamp := schemas(epoch)
	j := Job{Schema: schema, Binding: id, Epoch: stamp, Status: "pending", Requested: now.UTC(), Next: now.UTC(), Profile: p}
	return j, s.publish(epochName(id, epoch), j)
}

// Inspect reports the latest epoch. It distinguishes never-requested from
// unreadable/corrupt state and never infers revocation from absence of a job.
func (s *Store) Inspect(id string) (Job, bool, error) {
	j, _, _, err := s.latest(id)
	if errors.Is(err, os.ErrNotExist) {
		return Job{}, false, nil
	}
	return j, err == nil, err
}
func backoff(attempt uint32) time.Duration {
	return min(30*time.Second*time.Duration(uint64(1)<<min(attempt-1, 7)), time.Hour)
}

// Run invokes the native adapter's inspect/revoke/verify operation for the latest
// revocation epoch. Failures and interrupted attempts remain due after backoff,
// with errors omitted from state. Each attempt owns its durable audit files.
func (s *Store) Run(ctx context.Context, id string, now time.Time, revoke func(context.Context, Binding) error) error {
	if revoke == nil {
		return ErrState
	}
	return s.run(ctx, id, now, 0, revoke, nil)
}

// run drives the latest epoch; want, when nonzero, must still be the latest.
func (s *Store) run(ctx context.Context, id string, now time.Time, want uint32, revoke func(context.Context, Binding) error, reactivate func(context.Context, Binding, uint32, *DirectoryProfile) error) error {
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
	var epoch uint32
	var a Audit
	err = func() error {
		unlock := s.lock(id)
		defer unlock()
		var err error
		j, epoch, _, err = s.epochs(id)
		if err != nil {
			return ErrState
		}
		if want != 0 && epoch != want {
			return ErrPending
		}
		if reactivating(epoch) && reactivate == nil {
			return ErrState
		}
		if j.Status == "complete" {
			return nil
		}
		if j.Status == "attempting" {
			// A published result settles the interrupted attempt without another native call.
			result, err := s.epochAudit(id, epoch, j.Attempts, "result")
			if err == nil {
				return s.settle(id, epoch, &j, result)
			}
			if !errors.Is(err, os.ErrNotExist) {
				return ErrState
			}
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
		if s.write(epochName(id, epoch), j) != nil {
			return ErrState
		}
		_, schema, stamp := schemas(epoch)
		a = Audit{Schema: schema, Binding: id, Epoch: stamp, Attempt: j.Attempts, Time: now.UTC(), Outcome: "started"}
		return s.publish(epochAuditName(id, epoch, j.Attempts, "started"), a)
	}()
	if err != nil || j.Status != "attempting" {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	started := time.Now()
	if reactivating(epoch) {
		err = reactivate(callCtx, b, epoch, j.Profile)
	} else {
		err = revoke(callCtx, b)
	}
	cancel()
	a.Time = now.UTC().Add(time.Since(started))
	a.Outcome = "failed"
	switch {
	case err == nil:
		a.Outcome = "success"
	case reactivating(epoch) && errors.Is(err, ErrReactivationUnsupported):
		a.Outcome = "reactivation_unsupported"
	case reactivating(epoch) && errors.Is(err, ErrReactivationRefused):
		a.Outcome = "administrator_refused"
	case reactivating(epoch) && errors.Is(err, ErrNativeRefused):
		a.Outcome = "native_refused"
	}
	unlock := s.lock(id)
	defer unlock()
	if s.publish(epochAuditName(id, epoch, j.Attempts, "result"), a) != nil {
		return ErrState
	}
	return s.settle(id, epoch, &j, a)
}

// settle records an attempt's published result in its epoch job. A refusal is
// final for that attempt; only another PUT tries again.
func (s *Store) settle(id string, epoch uint32, j *Job, a Audit) error {
	j.Next = a.Time.Add(backoff(j.Attempts))
	result := refusals[a.Outcome]
	switch {
	case a.Outcome == "success":
		j.Status = "complete"
	case a.Outcome == "failed":
		j.Status, result = "pending", ErrPending
	case result != nil:
		j.Status, j.Next = "refused", a.Time
	default:
		return ErrState
	}
	if s.write(epochName(id, epoch), *j) != nil {
		return ErrState
	}
	return result
}
