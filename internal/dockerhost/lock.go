package dockerhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// LockedError is a target held by another run. The lock is never broken automatically.
type LockedError struct {
	Root    string
	Holder  string
	Started time.Time
}

func (e *LockedError) Error() string {
	lock := path.Join(e.Root, ".lock")
	if e.Holder == "" {
		return fmt.Sprintf("target is locked by an unknown holder; remove %s by hand once you are sure no run is active", lock)
	}
	return fmt.Sprintf("target is locked by %s since %s (%s ago); remove %s by hand once you are sure that run is gone",
		e.Holder, e.Started.Format(time.RFC3339), time.Since(e.Started).Round(time.Second), lock)
}

type holderRecord struct {
	Holder  string    `json:"holder"`
	Run     string    `json:"run"`
	Started time.Time `json:"started"`
}

const lockedExit = 75

// Acquire takes <root>/.lock for one run. release removes it only if this run still holds it.
func Acquire(ctx context.Context, r remote.Runner, root, holder string) (release func(context.Context) error, err error) {
	run := make([]byte, 16)
	rand.Read(run)
	body, err := json.Marshal(holderRecord{Holder: holder, Run: hex.EncodeToString(run), Started: time.Now().UTC()})
	if err != nil {
		return nil, err
	}
	if err := trustedDir(ctx, r, root); err != nil {
		return nil, fmt.Errorf("installer root: %w", err)
	}
	lock := remote.Quote(path.Join(root, ".lock"))
	// mkdir is the atomic test-and-set; the final mkdir only repeats to report its error.
	cmd := fmt.Sprintf("umask 077; mkdir -p %s && if mkdir %s 2>/dev/null; then cat > %s/holder; elif test -d %s; then cat %s/holder 2>/dev/null; exit %d; else mkdir %s; fi",
		remote.Quote(root), lock, lock, lock, lock, lockedExit, lock)
	held := fmt.Sprintf("cmp -s - %s/holder", lock)
	unlock := fmt.Sprintf("rm %s/holder && rmdir %s", lock, lock)
	out, err := r.Run(ctx, cmd, body)
	var ee *remote.ExitError
	if errors.As(err, &ee) && ee.Code == lockedExit {
		le := &LockedError{Root: root}
		var h holderRecord
		if json.Unmarshal(out, &h) == nil {
			le.Holder, le.Started = h.Holder, h.Started
		}
		return nil, le
	}
	if err != nil {
		// A cancelled Run can return after the remote side took the lock; remove it only if
		// it is ours. Not holding it exits 0.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		err = fmt.Errorf("acquire lock: %w", err)
		if _, rerr := r.Run(cctx, "if "+held+"; then "+unlock+"; fi", body); rerr != nil {
			err = errors.Join(err, fmt.Errorf("release lock %s: %w", path.Join(root, ".lock"), rerr))
		}
		return nil, err
	}
	return func(ctx context.Context) error {
		if _, err := r.Run(ctx, held+" && "+unlock, body); err != nil {
			return fmt.Errorf("release lock %s: not held by this run or not removable: %w", path.Join(root, ".lock"), err)
		}
		return nil
	}, nil
}
