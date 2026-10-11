package offboard

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

// ServeWorker resumes due target-local intents independently of SCIM requests.
// Cancel and join it before closing Store. One worker owns the streaming directory
// cursor; individual deliveries retain the store's per-account exclusion.
func (s *Store) ServeWorker(ctx context.Context, targets []*Target) error {
	if len(targets) == 0 || len(targets) > 32 {
		return ErrState
	}
	byApp := make(map[string]*Target, len(targets))
	for _, target := range targets {
		if target == nil || target.revoke == nil || !appName.MatchString(target.spec.App) || byApp[target.spec.App] != nil {
			return ErrState
		}
		byApp[target.spec.App] = target
	}
	if !s.worker.TryLock() {
		return ErrState
	}
	defer s.worker.Unlock()
	if err := s.privateRoot(); err != nil {
		return ErrState
	}
	directory, err := s.root.Open(".")
	if err != nil {
		return ErrState
	}
	runCtx, cancel := context.WithCancel(ctx)
	var joined sync.WaitGroup
	defer func() { cancel(); joined.Wait(); directory.Close() }()
	slots := make(chan struct{}, 4)
	failures := make(chan error, 4)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	refresh := false
	for {
		if refresh {
			fresh, err := s.root.Open(".")
			if err != nil {
				return ErrState
			}
			directory.Close()
			directory = fresh
			refresh = false
		}
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		if s.privateRoot() != nil {
			return ErrState
		}
		// ponytail: bounded streaming scans of the existing flat state layout.
		// Separate a jobs directory if audit growth makes full-cycle latency too high.
		entries, err := directory.ReadDir(1024)
		if err != nil && !errors.Is(err, io.EOF) {
			return ErrState
		}
		for _, entry := range entries {
			if runCtx.Err() != nil {
				return runCtx.Err()
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".job") {
				continue
			}
			id := strings.TrimSuffix(name, ".job")
			binding, err := s.Binding(id)
			if err != nil {
				return ErrState
			}
			target := byApp[binding.App]
			if !target.matches(binding) {
				return ErrState
			}
			job, present, err := s.Inspect(id)
			if err != nil || !present {
				return ErrState
			}
			if job.Status == "complete" || time.Now().Before(job.Next) {
				continue
			}
			select {
			case slots <- struct{}{}:
				joined.Add(1)
				go func(id string, target *Target) {
					defer joined.Done()
					defer func() { <-slots }()
					err := s.Deliver(runCtx, target, id, time.Now())
					if err != nil && !errors.Is(err, ErrPending) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
						select {
						case failures <- ErrState:
						default:
						}
					}
				}(id, target)
			default:
				// Keep the durable job untouched; a later scan revisits it.
			}
		}
		if errors.Is(err, io.EOF) {
			// Reopen on the next scan so Btrfs sees jobs published after startup.
			refresh = true
		}
		select {
		case <-runCtx.Done():
			return runCtx.Err()
		case err := <-failures:
			return err
		case <-tick.C:
		}
	}
}
