package offboard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func groupFixture() (*Target, GroupBinding) {
	b := directoryBinding()
	g := GroupBinding{SetupID: b.SetupID, App: b.App, Origin: b.Origin, Issuer: b.Issuer, ExternalID: "group-subject", SourceID: b.SourceID, OrganizationID: 12, Organization: "owned-org", TeamName: "owned-team", Permission: "read", Permissions: map[string]string{"repo.code": "read"}}
	t := directoryTarget(b, nil)
	t.groupInspect = func(context.Context, GroupBinding) error { return nil }
	return t, g
}

func TestGroupCreationProofsAndKnownRefusalRetry(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	target, scope := groupFixture()
	calls := 0
	id := groupID(scope)
	target.groupCreate = func(_ context.Context, b GroupBinding) (GroupBinding, error) {
		calls++
		j, _, err := s.groupCreation(id)
		var started groupCreationAudit
		if err != nil || j.Status != "attempting" || s.read(groupAuditName(id, j.Attempt, "started"), &started) != nil {
			t.Error("native group creation precedes durable intent/start")
		}
		if calls == 1 {
			return GroupBinding{}, errors.New("native known refusal")
		}
		b.TeamID = 44
		return b, nil
	}
	if _, err := s.CreateGroup(context.Background(), target, scope, time.Now()); err == nil {
		t.Fatal("known refusal became success")
	}
	b, err := s.CreateGroup(context.Background(), target, scope, time.Now())
	if err != nil || b.TeamID != 44 || calls != 2 {
		t.Fatal("verified group not published", err)
	}
	b.Permissions["repo.code"] = "write"
	retained, err := s.GroupBinding(id)
	if err != nil || retained.Permissions["repo.code"] != "read" {
		t.Fatal("returned mutable map changed stored authority", err)
	}
	if _, err := s.CreateGroup(context.Background(), target, scope, time.Now()); err != nil || calls != 2 {
		t.Fatal("group repeat performed another create", err)
	}
	j, _, err := s.groupCreation(id)
	if err != nil || j.Status != "complete" || j.Attempt != 2 {
		t.Fatal("group completion proof missing", err)
	}
	if s.root.Remove(groupAuditName(id, j.Attempt, "result")) != nil {
		t.Fatal("could not remove disposable proof")
	}
	if _, err := s.GroupBinding(id); err == nil {
		t.Fatal("missing native receipt accepted")
	}
	if _, err := s.CreateGroup(context.Background(), target, scope, time.Now()); err == nil || calls != 2 {
		t.Fatal("missing receipt caused native recreation", err)
	}
}

func TestGroupUnknownCreationHoldsChangedScopesAcrossRestart(t *testing.T) {
	for _, mode := range []string{"uncertain", "interrupted", "foreign-receipt"} {
		t.Run(mode, func(t *testing.T) {
			path := stateDir(t)
			s := openStore(t, path)
			defer func() { s.Close() }()
			target, scope := groupFixture()
			calls := 0
			target.groupCreate = func(_ context.Context, b GroupBinding) (GroupBinding, error) {
				calls++
				if mode == "foreign-receipt" {
					b.TeamID = 44
					b.OrganizationID++
					return b, nil
				}
				return GroupBinding{}, ErrGroupUncertain
			}
			if _, err := s.CreateGroup(context.Background(), target, scope, time.Now()); !errors.Is(err, ErrGroupUncertain) {
				t.Fatal("unknown creation was released", err)
			}
			if mode == "interrupted" {
				j, _, _ := s.groupCreation(groupID(scope))
				j.Status = "attempting"
				if s.write(groupID(scope)+".group-creation", j) != nil {
					t.Fatal("could not stage interrupted fixture")
				}
			}
			s.Close()
			s = openStore(t, path)
			for _, name := range []string{scope.TeamName, "changed-name"} {
				scope.TeamName = name
				if _, err := s.CreateGroup(context.Background(), target, scope, time.Now()); !errors.Is(err, ErrGroupUncertain) {
					t.Fatal("restart/changed scope released native hold", err)
				}
			}
			if calls != 1 {
				t.Fatal("unknown native create replayed")
			}
			if _, err := s.GroupBinding(groupID(scope)); err == nil {
				t.Fatal("unknown group adopted")
			}
		})
	}
}

func TestGroupReceiptRepairsPublicationWithoutNativeCreate(t *testing.T) {
	path := stateDir(t)
	s := openStore(t, path)
	defer func() { s.Close() }()
	target, scope := groupFixture()
	calls := 0
	id := groupID(scope)
	target.groupCreate = func(_ context.Context, b GroupBinding) (GroupBinding, error) {
		calls++
		b.TeamID = 44
		// Unsafe binding publication must stop, while retaining native receipt.
		if os.WriteFile(filepath.Join(path, id+".group-binding"), []byte(`{}`), 0644) != nil {
			t.Fatal("fixture write failed")
		}
		return b, nil
	}
	if _, err := s.CreateGroup(context.Background(), target, scope, time.Now()); !errors.Is(err, ErrState) {
		t.Fatal("unsafe publication accepted", err)
	}
	j, receipt, err := s.groupCreation(id)
	if err != nil || j.Status != "created" || receipt.TeamID != 44 {
		t.Fatal("publication failure lost verified receipt", err)
	}
	if os.Remove(filepath.Join(path, id+".group-binding")) != nil {
		t.Fatal("fixture cleanup failed")
	}
	s.Close()
	s = openStore(t, path)
	if _, err := s.CreateGroup(context.Background(), target, scope, time.Now()); err != nil || calls != 1 {
		t.Fatal("repair created native team again", err)
	}
}

func TestGroupScopeAndAuthorityConflictNeverReachesNative(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	target, scope := groupFixture()
	calls := 0
	target.groupCreate = func(_ context.Context, b GroupBinding) (GroupBinding, error) { calls++; b.TeamID = 44; return b, nil }
	foreign := scope
	foreign.SetupID = "11111111-1111-1111-1111-111111111111"
	if _, err := s.CreateGroup(context.Background(), target, foreign, time.Now()); err == nil || calls != 0 {
		t.Fatal("foreign setup reached native API")
	}
	if _, err := s.CreateGroup(context.Background(), target, scope, time.Now()); err != nil {
		t.Fatal(err)
	}
	scope.TeamName = "different-team"
	if _, err := s.CreateGroup(context.Background(), target, scope, time.Now()); !errors.Is(err, ErrGroupConflict) || calls != 1 {
		t.Fatal("retained group scope was replaced", err)
	}
}

func TestGroupCreationSerializesOnlyTheSameOwnedGroup(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	target, scope := groupFixture()
	entered, release := make(chan struct{}), make(chan struct{})
	target.groupCreate = func(_ context.Context, b GroupBinding) (GroupBinding, error) {
		if b.ExternalID == scope.ExternalID {
			close(entered)
			<-release
		}
		b.TeamID = 44
		return b, nil
	}
	completed := make(chan error, 1)
	go func() { _, err := s.CreateGroup(context.Background(), target, scope, time.Now()); completed <- err }()
	// Release and join the native callback even if a later assertion fails.
	defer func() {
		close(release)
		if err := <-completed; err != nil {
			t.Error("serialized group did not complete", err)
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("group create never started")
	}
	if _, err := s.CreateGroup(context.Background(), target, scope, time.Now()); !errors.Is(err, ErrPending) {
		t.Fatal("same group received a concurrent create", err)
	}
	other := cloneGroup(scope)
	other.ExternalID = "independent-group"
	other.TeamName = "independent-team"
	if _, err := s.CreateGroup(context.Background(), target, other, time.Now()); err != nil {
		t.Fatal("independent group was blocked by shared state", err)
	}
}
