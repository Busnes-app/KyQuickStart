package dockerhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

func TestAcquireReportsHolderAndKeepsLock(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(realTemp(t), "it's root")
	release, err := Acquire(ctx, remote.Local{}, root, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	holder := filepath.Join(root, ".lock", "holder")
	before, _ := os.ReadFile(holder)

	_, err = Acquire(ctx, remote.Local{}, root, "run-b")
	var le *LockedError
	if !errors.As(err, &le) || le.Holder != "run-a" || time.Since(le.Started) > time.Minute {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(le.Error(), "run-a") || !strings.Contains(le.Error(), "by hand") {
		t.Errorf("message = %q", le.Error())
	}
	if after, _ := os.ReadFile(holder); string(after) != string(before) {
		t.Errorf("holder changed: %s", after)
	}

	if err := release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".lock")); !os.IsNotExist(err) {
		t.Fatalf("lock still present: %v", err)
	}
	release2, err := Acquire(ctx, remote.Local{}, root, "run-b")
	if err != nil {
		t.Fatal(err)
	}
	release2(ctx)
}

func TestReleaseLeavesSomeoneElsesLock(t *testing.T) {
	ctx := context.Background()
	root := realTemp(t)
	release, err := Acquire(ctx, remote.Local{}, root, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	holder := filepath.Join(root, ".lock", "holder")
	os.WriteFile(holder, []byte(`{"holder":"run-z"}`), 0o600)
	if err := release(ctx); err == nil {
		t.Fatal("released a lock it does not hold")
	}
	if b, _ := os.ReadFile(holder); string(b) != `{"holder":"run-z"}` {
		t.Errorf("holder = %s", b)
	}
}

func TestLockWithoutHolder(t *testing.T) {
	root := realTemp(t)
	os.Mkdir(filepath.Join(root, ".lock"), 0o700)
	_, err := Acquire(context.Background(), remote.Local{}, root, "run-a")
	var le *LockedError
	if !errors.As(err, &le) || le.Holder != "" || !strings.Contains(le.Error(), "unknown holder") {
		t.Fatalf("err = %v", err)
	}
}

func TestAcquireUnwritableRoot(t *testing.T) {
	file := filepath.Join(realTemp(t), "file")
	os.WriteFile(file, nil, 0o600)
	_, err := Acquire(context.Background(), remote.Local{}, filepath.Join(file, "root"), "run-a")
	var le *LockedError
	if err == nil || errors.As(err, &le) {
		t.Fatalf("err = %v, want a plain failure", err)
	}
}

// cancelFirst runs every command for real but reports the first as cancelled, like an
// SSH run interrupted after the remote side already finished.
type cancelFirst struct{ calls int }

func (c *cancelFirst) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	out, err := remote.Local{}.Run(ctx, cmd, stdin)
	if c.calls++; c.calls == 1 {
		return out, context.Canceled
	}
	return out, err
}

func TestCancelledAcquireRemovesItsLock(t *testing.T) {
	root := realTemp(t)
	_, err := Acquire(context.Background(), &cancelFirst{}, root, "run-a")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".lock")); !os.IsNotExist(err) {
		t.Fatalf("lock left behind: %v", err)
	}
}

func TestCancelledAcquireLeavesSomeoneElsesLock(t *testing.T) {
	root := realTemp(t)
	holder := filepath.Join(root, ".lock", "holder")
	os.Mkdir(filepath.Join(root, ".lock"), 0o700)
	os.WriteFile(holder, []byte(`{"holder":"run-z"}`), 0o600)
	_, err := Acquire(context.Background(), &cancelFirst{}, root, "run-a")
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "release") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(holder); string(b) != `{"holder":"run-z"}` {
		t.Errorf("holder = %s", b)
	}
}
