package offboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrGroupMembershipUncertain = errors.New("group membership is held for outcome reconciliation")
var ErrGroupDeleted = errors.New("group membership is permanently retired")

// GroupMembership uses verified bridge account IDs, never native IDs from an event.
// Owned is the complete permitted native membership scope, including withdrawals.
type GroupMembership struct {
	Label   string   `json:"label,omitempty"`
	Deleted bool     `json:"deleted,omitempty"`
	Owned   []string `json:"owned"`
	Members []string `json:"members"`
}

func canonicalMembership(p GroupMembership) (GroupMembership, error) {
	p = GroupMembership{Label: p.Label, Deleted: p.Deleted, Owned: append([]string{}, p.Owned...), Members: append([]string{}, p.Members...)}
	slices.Sort(p.Owned)
	slices.Sort(p.Members)
	if !validMembership(p) {
		return GroupMembership{}, ErrState
	}
	return p, nil
}
func validMembership(p GroupMembership) bool {
	if len(p.Owned) > 100 || len(p.Members) > 100 || !utf8.ValidString(p.Label) || len(p.Label) > 100 || strings.IndexFunc(p.Label, unicode.IsControl) >= 0 || (p.Deleted && len(p.Members) != 0) {
		return false
	}
	for _, ids := range [][]string{p.Owned, p.Members} {
		for i, id := range ids {
			if !recordID.MatchString(id) || (i > 0 && ids[i-1] >= id) {
				return false
			}
		}
	}
	for _, id := range p.Members {
		if _, ok := slices.BinarySearch(p.Owned, id); !ok {
			return false
		}
	}
	return true
}
func membershipDigest(binding string, p GroupMembership) string {
	raw, _ := json.Marshal(struct {
		Binding string
		Scope   GroupMembership
	}{binding, p})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type groupMembershipJob struct {
	Schema        string          `json:"schema"`
	Group         string          `json:"group"`
	BindingDigest string          `json:"binding_digest"`
	Scope         GroupMembership `json:"scope"`
	Digest        string          `json:"digest"`
	Attempt       uint32          `json:"attempt"`
	Status        string          `json:"status"`
	Time          time.Time       `json:"time"`
}
type groupMembershipAudit struct {
	Schema  string    `json:"schema"`
	Group   string    `json:"group"`
	Digest  string    `json:"digest"`
	Attempt uint32    `json:"attempt"`
	Outcome string    `json:"outcome"`
	Time    time.Time `json:"time"`
}

func membershipAuditName(id string, attempt uint32, phase string) string {
	return fmt.Sprintf("%s.group-membership-audit-%010d-%s", id, attempt, phase)
}
func (s *Store) membershipJob(id string) (groupMembershipJob, error) {
	var j groupMembershipJob
	if err := s.read(id+".group-membership", &j); err != nil {
		return j, err
	}
	if j.Schema != "ky.group-membership/1" || j.Group != id || !recordID.MatchString(id) || !recordID.MatchString(j.BindingDigest) || !validMembership(j.Scope) || j.Digest != membershipDigest(j.BindingDigest, j.Scope) || j.Attempt == 0 || j.Time.IsZero() || (j.Status != "attempting" && j.Status != "complete" && j.Status != "refused" && j.Status != "uncertain") {
		return j, ErrState
	}
	if j.Status == "attempting" {
		return j, nil
	}
	for _, phase := range []string{"started", "result"} {
		var a groupMembershipAudit
		outcome := "started"
		if phase == "result" {
			outcome = map[string]string{"complete": "success", "refused": "failed", "uncertain": "uncertain"}[j.Status]
		}
		if s.read(membershipAuditName(id, j.Attempt, phase), &a) != nil || a.Schema != "ky.group-membership-audit/1" || a.Group != id || a.Digest != j.Digest || a.Attempt != j.Attempt || a.Outcome != outcome || a.Time.IsZero() || a.Time.Before(j.Time) || (phase == "started" && !a.Time.Equal(j.Time)) {
			return j, ErrState
		}
	}
	return j, nil
}

// membershipReady checks durable account authority while its delivery lock is
// held. Request may publish revocation intent independently; check again after I/O.
func (s *Store) membershipReady(id string) error {
	if _, err := s.job(id); err == nil {
		return ErrRevoked
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	if j, err := s.creationJob(id); err == nil {
		if j.Status != "complete" {
			return ErrCreationUncertain
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	if j, err := s.directoryJob(id); err == nil {
		// A refused job records the requested profile, not the native one.
		if j.Status != "complete" {
			return ErrDirectoryUncertain
		}
		if j.Profile.Administrator {
			return ErrGroupConflict
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	return nil
}

// ApplyGroupMembership owns a group's intent/audits, while sharing account delivery
// locks only for the native authority each membership uses. Unknown outcomes hold
// later memberships across restart; account revocation remains independently usable.
func (s *Store) ApplyGroupMembership(ctx context.Context, t *Target, id string, p GroupMembership, now time.Time) error {
	return s.applyGroupMembership(ctx, t, id, p, now, false)
}

// Directory events derive withdrawal scope from the last verified membership under
// the same group lock, so competing replacements cannot lose each other's members.
func (s *Store) applyGroupMembership(ctx context.Context, t *Target, id string, p GroupMembership, now time.Time, derive bool) error {
	g, err := s.GroupBinding(id)
	if err != nil || !t.matchesGroup(g) || t.groupReplace == nil || now.IsZero() {
		return ErrState
	}
	p, err = canonicalMembership(p)
	if err != nil {
		return err
	}
	groupLock := s.account("group:" + id)
	if !groupLock.delivery.TryLock() {
		return ErrPending
	}
	defer groupLock.delivery.Unlock()
	j, err := s.membershipJob(id)
	if err == nil {
		if j.Status == "attempting" || j.Status == "uncertain" {
			return ErrGroupMembershipUncertain
		}
		if j.BindingDigest != groupDigest(g) {
			return ErrState
		}
		// Retirement is final. A repeat delete re-verifies native emptiness read-only, so
		// drift or a failed read never overwrites the proof.
		if j.Status == "complete" && j.Scope.Deleted {
			if !p.Deleted {
				return ErrGroupDeleted
			}
			return s.verifyRetired(ctx, t, g, j.Scope.Owned)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	if derive {
		p.Owned = append([]string{}, p.Members...)
		if j.Status == "complete" {
			p.Owned = append(p.Owned, j.Scope.Members...)
		} else {
			p.Owned = append(p.Owned, j.Scope.Owned...)
		}
		if p.Deleted {
			p.Label = j.Scope.Label
		}
		slices.Sort(p.Owned)
		p.Owned = slices.Compact(p.Owned)
		if !validMembership(p) {
			return ErrState
		}
	}
	if j.Attempt == ^uint32(0) {
		return ErrState
	}
	locked := make([]*sync.Mutex, 0, len(p.Owned))
	defer func() {
		for i := len(locked) - 1; i >= 0; i-- {
			locked[i].Unlock()
		}
	}()
	for _, account := range p.Owned {
		lock := &s.account(account).delivery
		if !lock.TryLock() {
			return ErrPending
		}
		locked = append(locked, lock)
	}
	owned := make([]Binding, 0, len(p.Owned))
	desired := make([]Binding, 0, len(p.Members))
	for _, account := range p.Owned {
		b, err := s.Binding(account)
		if err != nil || !t.matches(b) || b.SetupID != g.SetupID {
			return ErrState
		}
		owned = append(owned, b)
		if _, wanted := slices.BinarySearch(p.Members, account); wanted {
			if err := s.membershipReady(account); err != nil {
				return err
			}
			desired = append(desired, b)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	j = groupMembershipJob{Schema: "ky.group-membership/1", Group: id, BindingDigest: groupDigest(g), Scope: p, Attempt: j.Attempt + 1, Status: "attempting", Time: now.UTC()}
	j.Digest = membershipDigest(j.BindingDigest, p)
	a := groupMembershipAudit{Schema: "ky.group-membership-audit/1", Group: id, Digest: j.Digest, Attempt: j.Attempt, Outcome: "started", Time: j.Time}
	if s.write(id+".group-membership", j) != nil || s.publish(membershipAuditName(id, j.Attempt, "started"), a) != nil {
		return ErrState
	}
	started := time.Now()
	err = t.groupReplace(ctx, g, owned, desired)
	if err == nil && p.Label != "" {
		if t.groupLabel == nil || t.groupLabel(ctx, g, p.Label) != nil {
			err = ErrGroupMembershipUncertain
		}
	}
	if err == nil {
		for _, account := range p.Members {
			if s.membershipReady(account) != nil {
				err = ErrGroupMembershipUncertain
				break
			}
		}
	}
	j.Status, a.Outcome = "complete", "success"
	if errors.Is(err, ErrGroupMembershipUncertain) {
		j.Status, a.Outcome = "uncertain", "uncertain"
	} else if err != nil {
		j.Status, a.Outcome = "refused", "failed"
	}
	a.Time = now.UTC().Add(time.Since(started))
	if s.publish(membershipAuditName(id, j.Attempt, "result"), a) != nil || s.write(id+".group-membership", j) != nil {
		return ErrState
	}
	return err
}

// verifyRetired runs under the group lock. Native reads refuse foreign members.
func (s *Store) verifyRetired(ctx context.Context, t *Target, g GroupBinding, ids []string) error {
	if t.groupRead == nil {
		return ErrState
	}
	owned := make([]Binding, 0, len(ids))
	for _, id := range ids {
		b, err := s.Binding(id)
		if err != nil || !t.matches(b) || b.SetupID != g.SetupID {
			return ErrState
		}
		owned = append(owned, b)
	}
	if _, members, err := t.groupRead(ctx, g, owned); err != nil || len(members) != 0 {
		return ErrGroupMembershipUncertain
	}
	return nil
}
