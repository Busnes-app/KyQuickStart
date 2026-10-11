package offboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrDirectoryUncertain = errors.New("directory mutation is held for outcome reconciliation")
var ErrRevoked = errors.New("directory mutation conflicts with irreversible revocation")

type DirectoryProfile struct {
	Name          string `json:"name"`
	Email         string `json:"email"`
	Administrator bool   `json:"administrator"`
}

func validDirectoryRoles(admin, everyday string) bool {
	return (admin == "" && everyday == "") || (appName.MatchString(admin) && appName.MatchString(everyday) && admin != everyday)
}

func validDirectoryProfile(p DirectoryProfile) bool {
	address, err := mail.ParseAddress(p.Email)
	return utf8.ValidString(p.Name) && len(p.Name) <= 100 && strings.IndexFunc(p.Name, unicode.IsControl) < 0 &&
		err == nil && address.Name == "" && address.Address == p.Email && len(p.Email) <= 254
}

func directoryDigest(p DirectoryProfile) string {
	data, _ := json.Marshal(p)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type directoryJob struct {
	Schema  string           `json:"schema"`
	Binding string           `json:"binding"`
	Attempt uint32           `json:"attempt"`
	Status  string           `json:"status"`
	Digest  string           `json:"digest"`
	Profile DirectoryProfile `json:"profile"`
	Time    time.Time        `json:"time"`
}

type directoryAudit struct {
	Schema  string    `json:"schema"`
	Binding string    `json:"binding"`
	Attempt uint32    `json:"attempt"`
	Digest  string    `json:"digest"`
	Outcome string    `json:"outcome"`
	Time    time.Time `json:"time"`
}

func directoryAuditName(id string, attempt uint32, phase string) string {
	return fmt.Sprintf("%s.directory-audit-%010d-%s", id, attempt, phase)
}

func (s *Store) directoryJob(id string) (directoryJob, error) {
	var j directoryJob
	if err := s.read(id+".directory", &j); err != nil {
		return j, err
	}
	if j.Schema != "ky.directory-job/1" || j.Binding != id || j.Attempt == 0 || j.Time.IsZero() ||
		!validDirectoryProfile(j.Profile) || !recordID.MatchString(j.Digest) || j.Digest != directoryDigest(j.Profile) ||
		(j.Status != "attempting" && j.Status != "complete" && j.Status != "refused" && j.Status != "uncertain") {
		return j, ErrState
	}
	if j.Status == "attempting" {
		// The previous process may have posted a native write before stopping.
		return j, nil
	}
	for phase, outcome := range map[string]string{"started": "started", "result": map[string]string{"complete": "success", "refused": "failed", "uncertain": "uncertain"}[j.Status]} {
		var a directoryAudit
		if s.read(directoryAuditName(id, j.Attempt, phase), &a) != nil || a.Schema != "ky.directory-audit/1" || a.Binding != id ||
			a.Attempt != j.Attempt || a.Digest != j.Digest || a.Outcome != outcome || a.Time.IsZero() ||
			(phase == "started" && !a.Time.Equal(j.Time)) || (phase == "result" && a.Time.Before(j.Time)) {
			return j, ErrState
		}
	}
	return j, nil
}

// ApplyDirectory owns one target-local directory job and immutable audit files
// per account. Its delivery lock is shared with revocation because the native
// account is one mutation target; their state files and audit names are separate.
func (s *Store) ApplyDirectory(ctx context.Context, target *Target, id string, p DirectoryProfile, now time.Time) error {
	b, err := s.Binding(id)
	if err != nil || !target.matches(b) || target.directory == nil || !validDirectoryProfile(p) || now.IsZero() {
		return ErrState
	}
	locks := s.account(id)
	if !locks.delivery.TryLock() {
		return ErrPending
	}
	defer locks.delivery.Unlock()
	var job directoryJob
	var audit directoryAudit
	err = func() error {
		unlock := s.lock(id)
		defer unlock()
		// Intent is irreversible even while native revocation is pending.
		if _, err := s.job(id); err == nil {
			return ErrRevoked
		} else if !errors.Is(err, os.ErrNotExist) {
			return ErrState
		}
		var err error
		job, err = s.directoryJob(id)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return ErrState
		}
		if err == nil && (job.Status == "attempting" || job.Status == "uncertain") {
			return ErrDirectoryUncertain
		}
		if job.Attempt == ^uint32(0) {
			return ErrState
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		job = directoryJob{Schema: "ky.directory-job/1", Binding: id, Attempt: job.Attempt + 1,
			Status: "attempting", Digest: directoryDigest(p), Profile: p, Time: now.UTC()}
		if s.write(id+".directory", job) != nil {
			return ErrState
		}
		audit = directoryAudit{Schema: "ky.directory-audit/1", Binding: id, Attempt: job.Attempt,
			Digest: job.Digest, Outcome: "started", Time: now.UTC()}
		return s.publish(directoryAuditName(id, job.Attempt, "started"), audit)
	}()
	if err != nil {
		return err
	}
	started := time.Now()
	err = target.directory(ctx, b, p)
	job.Status, audit.Outcome = "complete", "success"
	if errors.Is(err, ErrDirectoryUncertain) {
		job.Status, audit.Outcome = "uncertain", "uncertain"
	} else if err != nil {
		job.Status, audit.Outcome = "refused", "failed"
	}
	audit.Time = now.UTC().Add(time.Since(started))
	unlock := s.lock(id)
	defer unlock()
	if s.publish(directoryAuditName(id, job.Attempt, "result"), audit) != nil || s.write(id+".directory", job) != nil {
		return ErrState
	}
	return err
}
