package offboard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func directoryBinding() Binding {
	b := fixtureBinding()
	b.Kind, b.SourceID, b.Username = "forgejo", 7, "day"
	return b
}

func directoryTarget(b Binding, apply func(context.Context, Binding, DirectoryProfile) error) *Target {
	return &Target{spec: TargetSpec{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, SetupID: b.SetupID,
		SourceID: b.SourceID, AdministratorRole: "admin", EverydayRole: "user"}, directory: apply,
		revoke: func(context.Context, Binding) error { return nil }}
}

func TestDirectoryUnknownOutcomeIsHeldAcrossRestartAndRevocationStillRuns(t *testing.T) {
	dir := stateDir(t)
	s := openStore(t, dir)
	defer func() { s.Close() }()
	b := directoryBinding()
	id, err := s.Bind(b)
	if err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int32
	target := directoryTarget(b, func(_ context.Context, retained Binding, p DirectoryProfile) error {
		if retained != b {
			t.Error("directory request changed native binding")
		}
		writes.Add(1)
		return ErrDirectoryUncertain
	})
	profile := DirectoryProfile{Name: "New", Email: "day@example.test", Administrator: true}
	if err := s.ApplyDirectory(context.Background(), target, id, profile, time.Now()); !errors.Is(err, ErrDirectoryUncertain) {
		t.Fatal("unknown native outcome was acknowledged", err)
	}
	job, err := s.directoryJob(id)
	if err != nil || job.Status != "uncertain" || job.Attempt != 1 {
		t.Fatal("unknown outcome lost", err)
	}
	s.Close()
	s = openStore(t, dir)
	for _, next := range []DirectoryProfile{profile, {Name: "Later", Email: "day@example.test"}} {
		if err := s.ApplyDirectory(context.Background(), target, id, next, time.Now()); !errors.Is(err, ErrDirectoryUncertain) {
			t.Fatal("restart released an unresolved native write", err)
		}
	}
	if writes.Load() != 1 {
		t.Fatal("unknown write was replayed or superseded")
	}
	if _, err := s.Request(id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Deliver(context.Background(), target, id, time.Now()); err != nil {
		t.Fatal("directory hold blocked revocation", err)
	}
	if err := s.ApplyDirectory(context.Background(), target, id, profile, time.Now()); !errors.Is(err, ErrRevoked) {
		t.Fatal("active update bypassed irreversible revocation", err)
	}
}

func TestDirectoryIntentAndAuditProofPrecedeSuccess(t *testing.T) {
	for _, failure := range []string{"none", "started", "result", "lost-process"} {
		t.Run(failure, func(t *testing.T) {
			dir := stateDir(t)
			s := openStore(t, dir)
			defer s.Close()
			b := directoryBinding()
			id, err := s.Bind(b)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			profile := DirectoryProfile{Name: "Updated", Email: "day@example.test"}
			target := directoryTarget(b, func(_ context.Context, retained Binding, p DirectoryProfile) error {
				calls++
				j, err := s.directoryJob(id)
				if err != nil || j.Status != "attempting" || retained != b || p != profile {
					t.Error("native call preceded durable narrowed intent")
				}
				var audit directoryAudit
				if s.read(directoryAuditName(id, 1, "started"), &audit) != nil || audit.Outcome != "started" {
					t.Error("native call preceded audit")
				}
				return nil
			})
			if failure == "started" || failure == "result" {
				if err := os.Mkdir(filepath.Join(dir, directoryAuditName(id, 1, failure)), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "lost-process" {
				j := directoryJob{Schema: "ky.directory-job/1", Binding: id, Attempt: 1, Status: "attempting", Digest: directoryDigest(profile), Profile: profile, Time: time.Now()}
				if err := s.write(id+".directory", j); err != nil {
					t.Fatal(err)
				}
			}
			err = s.ApplyDirectory(context.Background(), target, id, profile, time.Now())
			if failure == "none" {
				j, readErr := s.directoryJob(id)
				if err != nil || readErr != nil || j.Status != "complete" || calls != 1 {
					t.Fatal("completion lacks proof", err, readErr)
				}
				if err := os.Remove(filepath.Join(dir, directoryAuditName(id, 1, "result"))); err != nil {
					t.Fatal(err)
				}
				if err := s.ApplyDirectory(context.Background(), target, id, profile, time.Now()); !errors.Is(err, ErrState) || calls != 1 {
					t.Fatal("missing receipt authorized another write", err)
				}
			} else if err == nil || (failure != "result" && calls != 0) || (failure == "result" && calls != 1) {
				t.Fatal("partial publication was acknowledged or retried", err)
			}
		})
	}
}

func TestDirectoryAndRevocationSerializeNativeMutation(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	b := directoryBinding()
	id, err := s.Bind(b)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	target := directoryTarget(b, func(context.Context, Binding, DirectoryProfile) error {
		close(entered)
		<-release
		return nil
	})
	finished := make(chan error, 1)
	go func() {
		finished <- s.ApplyDirectory(context.Background(), target, id, DirectoryProfile{Email: "day@example.test"}, time.Now())
	}()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatal("directory call failed before native mutation", err)
	case <-time.After(2 * time.Second):
		t.Fatal("directory call never reached native mutation")
	}
	if _, err := s.Request(id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Deliver(context.Background(), target, id, time.Now()); !errors.Is(err, ErrPending) {
		t.Error("native writes overlapped", err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := s.Deliver(context.Background(), target, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyDirectory(context.Background(), target, id, DirectoryProfile{Email: "day@example.test"}, time.Now()); !errors.Is(err, ErrRevoked) {
		t.Fatal("revocation was reset", err)
	}
}

func TestDirectoryKnownRefusalCanRetryWithoutRetainingNativeDiagnostics(t *testing.T) {
	dir := stateDir(t)
	s := openStore(t, dir)
	defer s.Close()
	b := directoryBinding()
	id, _ := s.Bind(b)
	calls := 0
	target := directoryTarget(b, func(context.Context, Binding, DirectoryProfile) error {
		calls++
		if calls == 1 {
			return errors.New("PRIVATE-NATIVE-CREDENTIAL")
		}
		return nil
	})
	profile := DirectoryProfile{Email: "day@example.test"}
	if err := s.ApplyDirectory(context.Background(), target, id, profile, time.Now()); err == nil {
		t.Fatal("refusal acknowledged")
	}
	if err := s.ApplyDirectory(context.Background(), target, id, profile, time.Now()); err != nil {
		t.Fatal(err)
	}
	j, err := s.directoryJob(id)
	if err != nil || j.Attempt != 2 || j.Status != "complete" {
		t.Fatal("known refusal blocked retry", err)
	}
	files, _ := os.ReadDir(dir)
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(dir, file.Name()))
		var decoded any
		if json.Unmarshal(data, &decoded) != nil {
			t.Fatal("invalid durable state")
		}
		if strings.Contains(string(data), "PRIVATE-NATIVE-CREDENTIAL") {
			t.Fatal("native diagnostic persisted")
		}
	}
}
