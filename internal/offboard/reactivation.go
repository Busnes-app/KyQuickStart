package offboard

import (
	"context"
	"errors"
	"os"
	"time"
)

var ErrReactivationUnsupported = errors.New("this product has no native reactivation")
var ErrReactivationRefused = errors.New("reactivation of this account is refused")

// errActive means no revocation applies: the PUT is an ordinary directory update.
var errActive = errors.New("account is active")

// Reactivate answers KyIdentity's re-assignment PUT. After a completed revocation
// it starts a reactivation epoch, then delivers it through the durable job path.
// p is the PUT's directory projection, nil for revocation-only targets.
func (s *Store) Reactivate(ctx context.Context, t *Target, id string, p *DirectoryProfile, now time.Time) error {
	b, err := s.Binding(id)
	if err != nil || !t.matches(b) || now.IsZero() || (t.directory != nil) != (p != nil) || (p != nil && !validDirectoryProfile(*p)) {
		return ErrState
	}
	var epoch uint32
	err = func() error {
		unlock := s.lock(id)
		defer unlock()
		j, latest, _, err := s.epochs(id)
		if errors.Is(err, os.ErrNotExist) {
			return errActive
		}
		if err != nil {
			return ErrState
		}
		epoch = latest
		if reactivating(epoch) && j.Status == "complete" {
			return errActive
		}
		if !reactivating(epoch) && j.Status != "complete" {
			return ErrPending // Reactivation waits for the revocation to finish.
		}
		if p != nil {
			// The projection writes the same native fields; never past a held directory write.
			if d, err := s.directoryJob(id); err == nil && (d.Status == "attempting" || d.Status == "uncertain") {
				return ErrDirectoryUncertain
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return ErrState
			}
		}
		if !reactivating(epoch) {
			if epoch+1 >= maxEpochs {
				return ErrReactivationRefused
			}
			epoch++
			_, err := s.startEpoch(id, epoch, now, p)
			return err
		}
		// The newest PUT is the desired state of an unfinished reactivation.
		same := (j.Profile == nil) == (p == nil) && (p == nil || *j.Profile == *p)
		if j.Status != "attempting" && !same {
			j.Profile = p
			return s.write(epochName(id, epoch), j)
		}
		return nil
	}()
	if err != nil {
		return err
	}
	return s.deliver(ctx, t, id, now, epoch)
}
