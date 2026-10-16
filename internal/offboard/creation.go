package offboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

var ErrCreationUncertain = errors.New("account creation is held for outcome reconciliation")
var ErrAccountConflict = errors.New("account creation conflicts with retained ownership")

type creationJob struct {
	Schema           string           `json:"schema"`
	Scope            Binding          `json:"scope"`
	Profile          DirectoryProfile `json:"profile"`
	Digest           string           `json:"digest"`
	Attempt          uint32           `json:"attempt"`
	Status           string           `json:"status"`
	DirectoryAttempt uint32           `json:"directory_attempt,omitempty"`
	Time             time.Time        `json:"time"`
}
type creationAudit struct {
	Schema, ID, Digest, Outcome string
	Attempt                     uint32
	Time                        time.Time
	Binding                     *Binding `json:"binding,omitempty"`
}

func creationDigest(b Binding, p DirectoryProfile) string {
	raw, _ := json.Marshal(struct {
		Scope   Binding
		Profile DirectoryProfile
	}{b, p})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func creationAuditName(id string, attempt uint32, phase string) string {
	return fmt.Sprintf("%s.creation-audit-%010d-%s", id, attempt, phase)
}
func validCreationScope(b Binding) bool {
	if b.NativeID != 0 || b.NativeUUID != "" {
		return false
	}
	return validate(creationProbe(b)) == nil
}

// Supply an identity only for ordinary binding validation. The probe is never
// persisted or sent to a product; native creation must return the real receipt.
func creationProbe(b Binding) Binding {
	switch b.Kind {
	case "forgejo", "bookstack":
		b.NativeID = 1
	case "immich":
		b.NativeUUID = "00000000-0000-4000-8000-000000000000"
	}
	return b
}

func creationScope(b Binding) Binding {
	b.NativeID = 0
	b.NativeUUID = ""
	return b
}
func (s *Store) creationJob(id string) (creationJob, error) {
	var j creationJob
	if err := s.read(id+".creation", &j); err != nil {
		return j, err
	}
	if j.Schema != "ky.creation-job/1" || !validCreationScope(j.Scope) || bindingID(j.Scope) != id || j.Attempt == 0 || j.Time.IsZero() || !validDirectoryProfile(j.Profile) || j.Digest != creationDigest(j.Scope, j.Profile) ||
		(j.Status != "attempting" && j.Status != "created" && j.Status != "complete" && j.Status != "refused" && j.Status != "uncertain") || (j.Status != "complete" && j.DirectoryAttempt != 0) {
		return j, ErrState
	}
	if j.Status == "attempting" {
		return j, nil
	}
	if _, err := s.creationReceipt(id, j); err != nil {
		return j, err
	}
	if j.Status == "complete" {
		if j.DirectoryAttempt == 0 {
			return j, ErrState
		}
		var started time.Time
		for _, phase := range []string{"started", "result"} {
			var audit directoryAudit
			outcome := map[string]string{"started": "started", "result": "success"}[phase]
			if s.read(directoryAuditName(id, j.DirectoryAttempt, phase), &audit) != nil || audit.Schema != "ky.directory-audit/1" || audit.Binding != id || audit.Attempt != j.DirectoryAttempt || audit.Digest != directoryDigest(j.Profile) || audit.Outcome != outcome || audit.Time.IsZero() {
				return j, ErrState
			}
			if phase == "started" {
				started = audit.Time
			} else if audit.Time.Before(started) {
				return j, ErrState
			}
		}
	}
	return j, nil
}
func (s *Store) creationReceipt(id string, j creationJob) (Binding, error) {
	var result Binding
	var started time.Time
	for _, phase := range []string{"started", "result"} {
		var a creationAudit
		outcome := "started"
		if phase == "result" {
			outcome = map[string]string{"created": "success", "complete": "success", "refused": "failed", "uncertain": "uncertain"}[j.Status]
		}
		if s.read(creationAuditName(id, j.Attempt, phase), &a) != nil || a.Schema != "ky.creation-audit/1" || a.ID != id || a.Digest != j.Digest || a.Attempt != j.Attempt || a.Outcome != outcome || a.Time.IsZero() {
			return Binding{}, ErrState
		}
		if phase == "started" {
			if a.Binding != nil || !a.Time.Equal(j.Time) {
				return Binding{}, ErrState
			}
			started = a.Time
		} else {
			if a.Time.Before(started) {
				return Binding{}, ErrState
			}
			if outcome == "success" {
				if a.Binding == nil || validate(*a.Binding) != nil {
					return Binding{}, ErrState
				}
				result = *a.Binding
				if creationScope(result) != j.Scope {
					return Binding{}, ErrState
				}
			} else if a.Binding != nil {
				return Binding{}, ErrState
			}
		}
	}
	return result, nil
}

// CreateAccount persists intent before native creation and a verified immutable
// receipt before publishing a binding. It never adopts, repeats an unknown create
// or grants administrator authority before the separately audited directory lane.
func (s *Store) CreateAccount(ctx context.Context, t *Target, scope Binding, p DirectoryProfile, now time.Time) (Binding, error) {
	checked := creationProbe(scope)
	if !validCreationScope(scope) || !t.matches(checked) || t.create == nil || !validDirectoryProfile(p) || now.IsZero() {
		return Binding{}, ErrState
	}
	id := bindingID(scope)
	locks := s.account(id)
	if !locks.delivery.TryLock() {
		return Binding{}, ErrPending
	}
	defer locks.delivery.Unlock()
	if _, err := s.job(id); err == nil {
		return Binding{}, ErrRevoked
	} else if !errors.Is(err, os.ErrNotExist) {
		return Binding{}, ErrState
	}
	j, err := s.creationJob(id)
	if err == nil {
		if j.Status == "attempting" || j.Status == "uncertain" {
			return Binding{}, ErrCreationUncertain
		}
		// A known refusal may retry the same scope with a corrected profile.
		if j.Scope != scope || (j.Status != "refused" && j.Digest != creationDigest(scope, p)) {
			return Binding{}, ErrAccountConflict
		}
		switch j.Status {
		case "created", "complete":
			b, err := s.creationReceipt(id, j)
			if err != nil || t.inspect(ctx, b) != nil {
				return Binding{}, ErrState
			}
			if _, err := s.bind(b, true); err != nil {
				return Binding{}, err
			}
			return b, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Binding{}, ErrState
	}
	var existing bindingRecord
	if err := s.read(id+".binding", &existing); err == nil {
		return Binding{}, ErrAccountConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return Binding{}, ErrState
	}
	if j.Attempt == ^uint32(0) {
		return Binding{}, ErrState
	}
	if err := ctx.Err(); err != nil {
		return Binding{}, err
	}
	j = creationJob{Schema: "ky.creation-job/1", Scope: scope, Profile: p, Digest: creationDigest(scope, p), Attempt: j.Attempt + 1, Status: "attempting", Time: now.UTC()}
	a := creationAudit{Schema: "ky.creation-audit/1", ID: id, Digest: j.Digest, Attempt: j.Attempt, Outcome: "started", Time: j.Time}
	if s.write(id+".creation", j) != nil || s.publish(creationAuditName(id, j.Attempt, "started"), a) != nil {
		return Binding{}, ErrState
	}
	started := time.Now()
	b, err := t.create(ctx, scope, p)
	j.Status, a.Outcome = "created", "success"
	if errors.Is(err, ErrCreationUncertain) {
		j.Status, a.Outcome = "uncertain", "uncertain"
	} else if err != nil {
		j.Status, a.Outcome = "refused", "failed"
	} else {
		if validate(b) != nil || creationScope(b) != scope {
			err = ErrCreationUncertain
			j.Status, a.Outcome = "uncertain", "uncertain"
		} else {
			a.Binding = &b
		}
	}
	a.Time = now.UTC().Add(time.Since(started))
	if s.publish(creationAuditName(id, j.Attempt, "result"), a) != nil || s.write(id+".creation", j) != nil {
		return Binding{}, ErrState
	}
	if err != nil {
		return Binding{}, err
	}
	if _, err := s.bind(b, true); err != nil {
		return Binding{}, err
	}
	return b, nil
}

func (s *Store) completeCreation(id string) error {
	locks := s.account(id)
	if !locks.delivery.TryLock() {
		return ErrPending
	}
	defer locks.delivery.Unlock()
	j, err := s.creationJob(id)
	if err != nil {
		return ErrState
	}
	if j.Status == "complete" {
		return nil
	}
	if j.Status != "created" {
		return ErrCreationUncertain
	}
	directory, err := s.directoryJob(id)
	if err != nil || directory.Status != "complete" || directory.Profile != j.Profile {
		return ErrState
	}
	j.Status, j.DirectoryAttempt = "complete", directory.Attempt
	return s.write(id+".creation", j)
}
