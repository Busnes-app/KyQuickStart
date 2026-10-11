package offboard

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func membershipFixture(t *testing.T, s *Store) (*Target, string, string) {
	t.Helper()
	target, scope := groupFixture()
	target.directory = func(context.Context, Binding, DirectoryProfile) error { return nil }
	target.groupCreate = func(_ context.Context, b GroupBinding) (GroupBinding, error) { b.TeamID = 44; return b, nil }
	if _, err := s.CreateGroup(context.Background(), target, scope, time.Now()); err != nil {
		t.Fatal(err)
	}
	b := directoryBinding()
	id, err := s.Bind(b)
	if err != nil {
		t.Fatal(err)
	}
	return target, groupID(scope), id
}

func TestMembershipIntentProofAndMissingResultRefusal(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	target, id, account := membershipFixture(t, s)
	calls := 0
	target.groupReplace = func(_ context.Context, g GroupBinding, owned, desired []Binding) error {
		calls++
		j, err := s.membershipJob(id)
		var a groupMembershipAudit
		if err != nil || j.Status != "attempting" || s.read(membershipAuditName(id, j.Attempt, "started"), &a) != nil {
			t.Error("native membership preceded intent/start")
		}
		if g.TeamID != 44 || len(owned) != 1 || len(desired) != 1 || owned[0] != directoryBinding() || desired[0] != owned[0] {
			t.Error("unverified native membership binding")
		}
		return nil
	}
	p := GroupMembership{Owned: []string{account}, Members: []string{account}}
	if err := s.ApplyGroupMembership(context.Background(), target, id, p, time.Now()); err != nil {
		t.Fatal(err)
	}
	j, err := s.membershipJob(id)
	if err != nil || j.Status != "complete" || j.Attempt != 1 {
		t.Fatal("membership completion not proved", err)
	}
	if s.root.Remove(membershipAuditName(id, j.Attempt, "result")) != nil {
		t.Fatal("fixture result removal failed")
	}
	if err := s.ApplyGroupMembership(context.Background(), target, id, p, time.Now()); err == nil || calls != 1 {
		t.Fatal("missing proof permitted another native write", err)
	}
}

func TestMembershipUnknownHoldsAcrossRestartButAccountRevocationRuns(t *testing.T) {
	for _, mode := range []string{"uncertain", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			path := stateDir(t)
			s := openStore(t, path)
			defer func() { s.Close() }()
			target, id, account := membershipFixture(t, s)
			calls := 0
			target.groupReplace = func(context.Context, GroupBinding, []Binding, []Binding) error {
				calls++
				return ErrGroupMembershipUncertain
			}
			p := GroupMembership{Owned: []string{account}, Members: []string{account}}
			if err := s.ApplyGroupMembership(context.Background(), target, id, p, time.Now()); !errors.Is(err, ErrGroupMembershipUncertain) {
				t.Fatal("unknown membership acknowledged", err)
			}
			if mode == "interrupted" {
				j, _ := s.membershipJob(id)
				j.Status = "attempting"
				if s.write(id+".group-membership", j) != nil {
					t.Fatal("fixture state write failed")
				}
			}
			s.Close()
			s = openStore(t, path)
			for _, next := range []GroupMembership{p, {Owned: []string{account}, Members: []string{}}} {
				if err := s.ApplyGroupMembership(context.Background(), target, id, next, time.Now()); !errors.Is(err, ErrGroupMembershipUncertain) {
					t.Fatal("restart/changed membership released hold", err)
				}
			}
			if calls != 1 {
				t.Fatal("unknown membership replayed")
			}
			if _, err := s.Request(account, time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := s.Deliver(context.Background(), target, account, time.Now()); err != nil {
				t.Fatal("held group blocked native account revocation", err)
			}
		})
	}
}

func TestMembershipRejectsUnownedRevokedAndUnsettledAuthority(t *testing.T) {
	for _, mode := range []string{"foreign", "revoked", "directory-held", "administrator", "demotion-refused", "not-in-owned", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			s := openStore(t, stateDir(t))
			defer s.Close()
			target, id, account := membershipFixture(t, s)
			calls := 0
			target.groupReplace = func(context.Context, GroupBinding, []Binding, []Binding) error { calls++; return nil }
			p := GroupMembership{Owned: []string{account}, Members: []string{account}}
			switch mode {
			case "foreign":
				p.Owned = []string{strings.Repeat("f", 64)}
				p.Members = p.Owned
			case "revoked":
				if _, err := s.Request(account, time.Now()); err != nil {
					t.Fatal(err)
				}
			case "directory-held", "administrator":
				target.directory = func(context.Context, Binding, DirectoryProfile) error {
					if mode == "directory-held" {
						return ErrDirectoryUncertain
					}
					return nil
				}
				_ = s.ApplyDirectory(context.Background(), target, account, DirectoryProfile{Name: "day", Email: "day@example.test", Administrator: true}, time.Now())
			case "demotion-refused":
				admin := DirectoryProfile{Name: "day", Email: "day@example.test", Administrator: true}
				if err := s.ApplyDirectory(context.Background(), target, account, admin, time.Now()); err != nil {
					t.Fatal(err)
				}
				target.directory = func(context.Context, Binding, DirectoryProfile) error { return errors.New("refused") }
				admin.Administrator = false
				if err := s.ApplyDirectory(context.Background(), target, account, admin, time.Now()); err == nil {
					t.Fatal("refused demotion acknowledged")
				}
			case "not-in-owned":
				p.Owned = nil
			case "duplicate":
				p.Members = append(p.Members, account)
			}
			if err := s.ApplyGroupMembership(context.Background(), target, id, p, time.Now()); err == nil || calls != 0 {
				t.Fatal("unproved member authority reached native mutation", err)
			}
			if _, err := s.membershipJob(id); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid member published mutation intent", err)
			}
		})
	}
}

func TestMembershipNativeCallSerializesWithRolesAndObservesNewRevocation(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	target, id, account := membershipFixture(t, s)
	entered, release := make(chan struct{}), make(chan struct{})
	target.groupReplace = func(context.Context, GroupBinding, []Binding, []Binding) error { close(entered); <-release; return nil }
	done := make(chan error, 1)
	go func() {
		done <- s.ApplyGroupMembership(context.Background(), target, id, GroupMembership{Owned: []string{account}, Members: []string{account}}, time.Now())
	}()
	defer func() {
		close(release)
		if err := <-done; !errors.Is(err, ErrGroupMembershipUncertain) {
			t.Error("concurrent revocation not retained as group uncertainty", err)
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("native membership never started")
	}
	if err := s.ApplyDirectory(context.Background(), target, account, DirectoryProfile{Name: "day", Email: "day@example.test"}, time.Now()); !errors.Is(err, ErrPending) {
		t.Fatal("role update raced native membership", err)
	}
	other := directoryBinding()
	other.Subject, other.Username, other.NativeID = "other-subject", "other", 88
	otherID, err := s.Bind(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyDirectory(context.Background(), target, otherID, DirectoryProfile{Name: "Other", Email: "other@example.test"}, time.Now()); err != nil {
		t.Fatal("unrelated account lost independent progress", err)
	}
	if _, err := s.Request(account, time.Now()); err != nil {
		t.Fatal("membership delayed irrevocable revocation intent", err)
	}
	if err := s.Deliver(context.Background(), target, account, time.Now()); !errors.Is(err, ErrPending) {
		t.Fatal("native disable raced membership", err)
	}
}

func TestDirectoryMembershipDerivesWithdrawalsFromTheLatestProof(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	target, id, oldID := membershipFixture(t, s)
	other := directoryBinding()
	other.Subject, other.Username, other.NativeID = "next-subject", "next", 88
	nextID, err := s.Bind(other)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	target.groupReplace = func(_ context.Context, _ GroupBinding, owned, desired []Binding) error {
		calls++
		if calls == 2 && (len(owned) != 2 || len(desired) != 1 || desired[0] != other) {
			t.Error("replacement lost previously verified membership")
		}
		return nil
	}
	for _, member := range []string{oldID, nextID} {
		if err := s.applyGroupMembership(context.Background(), target, id, GroupMembership{Owned: []string{member}, Members: []string{member}}, time.Now(), true); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("native replacements missing")
	}
}

func TestRetiredGroupSurvivesRefusedRepeatDelete(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	target, id, account := membershipFixture(t, s)
	calls := 0
	target.groupReplace = func(context.Context, GroupBinding, []Binding, []Binding) error { calls++; return nil }
	p := GroupMembership{Owned: []string{account}, Members: []string{}, Deleted: true}
	if err := s.ApplyGroupMembership(context.Background(), target, id, p, time.Now()); err != nil {
		t.Fatal(err)
	}
	target.groupReplace = func(context.Context, GroupBinding, []Binding, []Binding) error { calls++; return errors.New("refused") }
	if err := s.ApplyGroupMembership(context.Background(), target, id, p, time.Now()); err != nil || calls != 1 {
		t.Fatal("repeat delete of a retired group reached native I/O", err)
	}
	revive := GroupMembership{Owned: []string{account}, Members: []string{account}}
	if err := s.ApplyGroupMembership(context.Background(), target, id, revive, time.Now()); !errors.Is(err, ErrGroupDeleted) || calls != 1 {
		t.Fatal("retired group reactivated", err)
	}
}
