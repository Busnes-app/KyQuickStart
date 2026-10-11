package offboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"time"
)

// GroupBinding is protected provisioning state. Neither names nor SCIM events
// supply native organization/team authority or authorize adoption.
type GroupBinding struct {
	SetupID        string            `json:"setup_id"`
	App            string            `json:"app"`
	Origin         string            `json:"origin"`
	Issuer         string            `json:"issuer"`
	ExternalID     string            `json:"external_id"`
	SourceID       int64             `json:"source_id"`
	OrganizationID int64             `json:"organization_id"`
	Organization   string            `json:"organization"`
	TeamID         int64             `json:"team_id"`
	TeamName       string            `json:"team_name"`
	Permission     string            `json:"permission"`
	Permissions    map[string]string `json:"permissions"`
}

// GroupConfig is protected target policy; events supply none of these values.
type GroupConfig struct {
	OrganizationID int64             `json:"organization_id"`
	Organization   string            `json:"organization"`
	Permission     string            `json:"permission"`
	Permissions    map[string]string `json:"permissions"`
}

func validGroupConfig(g *GroupConfig) bool {
	if g == nil || g.OrganizationID <= 0 || !nativeName.MatchString(g.Organization) || (g.Permission != "read" && g.Permission != "write") || len(g.Permissions) == 0 || len(g.Permissions) > 10 {
		return false
	}
	for unit, p := range g.Permissions {
		switch unit {
		case "repo.actions", "repo.code", "repo.issues", "repo.ext_issues", "repo.wiki", "repo.ext_wiki", "repo.pulls", "repo.releases", "repo.projects", "repo.packages":
		default:
			return false
		}
		if p != "none" && p != "read" && p != "write" {
			return false
		}
	}
	return true
}
func (t *Target) groupScope(external string) GroupBinding {
	g := t.spec.Groups
	b := GroupBinding{SetupID: t.spec.SetupID, App: t.spec.App, Origin: t.spec.Origin, Issuer: t.spec.Issuer, ExternalID: external, SourceID: t.spec.SourceID, OrganizationID: g.OrganizationID, Organization: g.Organization, Permission: g.Permission, Permissions: maps.Clone(g.Permissions)}
	b.TeamName = "kyq-" + groupID(b)[:32]
	return b
}

var ErrGroupUncertain = errors.New("group creation is held for outcome reconciliation")
var ErrGroupConflict = errors.New("group creation conflicts with retained ownership")

func validGroup(b GroupBinding) bool {
	if !setupID.MatchString(b.SetupID) || !appName.MatchString(b.App) || !https(b.Origin, true) || !https(b.Issuer, false) || !validSubject(b.ExternalID) || b.SourceID <= 0 || b.TeamID <= 0 || !nativeName.MatchString(b.TeamName) {
		return false
	}
	return validGroupConfig(&GroupConfig{OrganizationID: b.OrganizationID, Organization: b.Organization, Permission: b.Permission, Permissions: b.Permissions})
}
func validGroupScope(b GroupBinding) bool {
	if b.TeamID != 0 {
		return false
	}
	b.TeamID = 1
	return validGroup(b)
}
func groupID(b GroupBinding) string {
	sum := sha256.Sum256([]byte("group\x00" + b.App + "\x00" + b.Issuer + "\x00" + b.ExternalID))
	return hex.EncodeToString(sum[:])
}
func groupDigest(b GroupBinding) string {
	raw, _ := json.Marshal(b)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func sameGroup(a, b GroupBinding) bool       { return groupDigest(a) == groupDigest(b) }
func cloneGroup(b GroupBinding) GroupBinding { b.Permissions = maps.Clone(b.Permissions); return b }

type groupCreationJob struct {
	Schema  string       `json:"schema"`
	Scope   GroupBinding `json:"scope"`
	Digest  string       `json:"digest"`
	Attempt uint32       `json:"attempt"`
	Status  string       `json:"status"`
	Time    time.Time    `json:"time"`
}
type groupCreationAudit struct {
	Schema  string        `json:"schema"`
	ID      string        `json:"id"`
	Digest  string        `json:"digest"`
	Attempt uint32        `json:"attempt"`
	Outcome string        `json:"outcome"`
	Time    time.Time     `json:"time"`
	Binding *GroupBinding `json:"binding,omitempty"`
}

func groupAuditName(id string, attempt uint32, phase string) string {
	return fmt.Sprintf("%s.group-creation-audit-%010d-%s", id, attempt, phase)
}
func (s *Store) groupCreation(id string) (groupCreationJob, GroupBinding, error) {
	var j groupCreationJob
	if err := s.read(id+".group-creation", &j); err != nil {
		return j, GroupBinding{}, err
	}
	if !recordID.MatchString(id) || j.Schema != "ky.group-creation/1" || !validGroupScope(j.Scope) || groupID(j.Scope) != id || j.Digest != groupDigest(j.Scope) || j.Attempt == 0 || j.Time.IsZero() || (j.Status != "attempting" && j.Status != "created" && j.Status != "complete" && j.Status != "refused" && j.Status != "uncertain") {
		return j, GroupBinding{}, ErrState
	}
	if j.Status == "attempting" {
		return j, GroupBinding{}, nil
	}
	var receipt GroupBinding
	for _, phase := range []string{"started", "result"} {
		var a groupCreationAudit
		outcome := "started"
		if phase == "result" {
			outcome = map[string]string{"created": "success", "complete": "success", "refused": "failed", "uncertain": "uncertain"}[j.Status]
		}
		if s.read(groupAuditName(id, j.Attempt, phase), &a) != nil || a.Schema != "ky.group-creation-audit/1" || a.ID != id || a.Digest != j.Digest || a.Attempt != j.Attempt || a.Outcome != outcome || a.Time.IsZero() || (phase == "started" && !a.Time.Equal(j.Time)) || a.Time.Before(j.Time) {
			return j, GroupBinding{}, ErrState
		}
		if phase == "result" && outcome == "success" {
			if a.Binding == nil || !validGroup(*a.Binding) {
				return j, GroupBinding{}, ErrState
			}
			scope := *a.Binding
			scope.TeamID = 0
			if !sameGroup(scope, j.Scope) {
				return j, GroupBinding{}, ErrState
			}
			receipt = cloneGroup(*a.Binding)
		} else if a.Binding != nil {
			return j, GroupBinding{}, ErrState
		}
	}
	return j, receipt, nil
}

// GroupBinding requires the owned creation intent and immutable native receipt;
// removing either proof cannot turn a discovered native team into an owned one.
func (s *Store) GroupBinding(id string) (GroupBinding, error) {
	var record struct {
		Schema  string       `json:"schema"`
		Binding GroupBinding `json:"binding"`
	}
	if !recordID.MatchString(id) || s.read(id+".group-binding", &record) != nil || record.Schema != "ky.group-binding/1" || !validGroup(record.Binding) || groupID(record.Binding) != id {
		return GroupBinding{}, ErrState
	}
	j, receipt, err := s.groupCreation(id)
	if err != nil || (j.Status != "created" && j.Status != "complete") || !sameGroup(receipt, record.Binding) {
		return GroupBinding{}, ErrState
	}
	return cloneGroup(record.Binding), nil
}
func (s *Store) publishGroup(id string, b GroupBinding) error {
	var record struct {
		Schema  string       `json:"schema"`
		Binding GroupBinding `json:"binding"`
	}
	err := s.read(id+".group-binding", &record)
	if err == nil {
		if record.Schema != "ky.group-binding/1" || !sameGroup(record.Binding, b) {
			return ErrState
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	record.Schema, record.Binding = "ky.group-binding/1", cloneGroup(b)
	return s.publish(id+".group-binding", record)
}

// CreateGroup is a protected setup operation, not a SCIM endpoint. Persist intent
// and audit before native creation, then its verified receipt before publication.
// Unknown/interrupted creation holds all later scopes without native replay.
func (s *Store) CreateGroup(ctx context.Context, t *Target, scope GroupBinding, now time.Time) (GroupBinding, error) {
	if !validGroupScope(scope) || !t.matchesGroup(scope) || t.groupCreate == nil || t.groupInspect == nil || now.IsZero() {
		return GroupBinding{}, ErrState
	}
	scope = cloneGroup(scope)
	id := groupID(scope)
	locks := s.account("group:" + id)
	if !locks.delivery.TryLock() {
		return GroupBinding{}, ErrPending
	}
	defer locks.delivery.Unlock()
	j, receipt, err := s.groupCreation(id)
	if err == nil {
		if j.Status == "attempting" || j.Status == "uncertain" {
			return GroupBinding{}, ErrGroupUncertain
		}
		if !sameGroup(j.Scope, scope) {
			return GroupBinding{}, ErrGroupConflict
		}
		if j.Status == "created" || j.Status == "complete" {
			if t.groupInspect(ctx, receipt) != nil || s.publishGroup(id, receipt) != nil {
				return GroupBinding{}, ErrState
			}
			j.Status = "complete"
			if s.write(id+".group-creation", j) != nil {
				return GroupBinding{}, ErrState
			}
			return receipt, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return GroupBinding{}, ErrState
	}
	var existing json.RawMessage
	if err := s.read(id+".group-binding", &existing); err == nil {
		return GroupBinding{}, ErrGroupConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return GroupBinding{}, ErrState
	}
	if j.Attempt == ^uint32(0) {
		return GroupBinding{}, ErrState
	}
	if err := ctx.Err(); err != nil {
		return GroupBinding{}, err
	}
	j = groupCreationJob{Schema: "ky.group-creation/1", Scope: scope, Digest: groupDigest(scope), Attempt: j.Attempt + 1, Status: "attempting", Time: now.UTC()}
	a := groupCreationAudit{Schema: "ky.group-creation-audit/1", ID: id, Digest: j.Digest, Attempt: j.Attempt, Outcome: "started", Time: j.Time}
	if s.write(id+".group-creation", j) != nil || s.publish(groupAuditName(id, j.Attempt, "started"), a) != nil {
		return GroupBinding{}, ErrState
	}
	started := time.Now()
	b, err := t.groupCreate(ctx, scope)
	j.Status, a.Outcome = "created", "success"
	if err == nil {
		retained := b
		retained.TeamID = 0
		if !validGroup(b) || !sameGroup(retained, scope) {
			err = ErrGroupUncertain
		} else {
			b = cloneGroup(b)
			a.Binding = &b
		}
	}
	if errors.Is(err, ErrGroupUncertain) {
		j.Status, a.Outcome = "uncertain", "uncertain"
	} else if err != nil {
		j.Status, a.Outcome = "refused", "failed"
	}
	a.Time = now.UTC().Add(time.Since(started))
	if s.publish(groupAuditName(id, j.Attempt, "result"), a) != nil || s.write(id+".group-creation", j) != nil {
		return GroupBinding{}, ErrState
	}
	if err != nil {
		return GroupBinding{}, err
	}
	if s.publishGroup(id, b) != nil {
		return GroupBinding{}, ErrState
	}
	j.Status = "complete"
	if s.write(id+".group-creation", j) != nil {
		return GroupBinding{}, ErrState
	}
	return b, nil
}
