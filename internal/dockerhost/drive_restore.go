package dockerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// driveRestoreRun uses the same pinned binary and uid as the deployed Drive, without
// the app's network, published ports, startup wrapper or target-held credentials.
func (s Store) driveRestoreRun(data string, args ...string) (string, error) {
	image := s.App.Catalog.Images["kydrive"]
	if s.App.Name != "kydrive" || !catalog.Pinned(image) {
		return "", errors.New("Drive restore requires its digest-pinned catalog image")
	}
	run := `docker run --rm --platform linux/amd64 --user "$(id -u):$(id -g)" --read-only --cap-drop ALL --security-opt no-new-privileges --network none --tmpfs /tmp:size=128m --memory 1g --memory-swap 1g --cpus 1 --pids-limit 128 --entrypoint /app/kydrive-server`
	if data != "" {
		run += " -v " + remote.Quote(data+":/restore/data:rw")
	}
	return run + " " + remote.Quote(image) + " " + quoteAll(args), nil
}

// CheckDriveRestoreReset never mounts data: older binaries route unknown commands to
// server startup, so restore-reset is invoked only after the known capabilities command.
func (s Store) CheckDriveRestoreReset(ctx context.Context) error {
	run, err := s.driveRestoreRun("", "apply-setup", "--capabilities")
	if err != nil {
		return err
	}
	out, err := s.App.Runner.Run(ctx, run, nil)
	var c struct {
		Schema, Service, BundleSchema string
		Features                      []string
	}
	if err != nil || len(out) > 128<<10 || json.Unmarshal(out, &c) != nil || c.Schema != "ky.setup-capabilities/1" || c.Service != "kydrive" || c.BundleSchema != "ky.product-setup/1" || !slices.Contains(c.Features, "restore-reset") {
		return errors.New("KyDrive's pinned image does not advertise restore-reset; restore refused: use a released catalog image with offline access-grant reset")
	}
	return nil
}

// ResetDriveRestore stops a possibly running resumed restore before resetting its grants.
// Only the restored data directory is mounted; secrets are loaded by the product itself.
func (s Store) ResetDriveRestore(ctx context.Context) error {
	running, err := s.Running(ctx)
	if err != nil {
		return err
	}
	if running {
		if err := s.Stop(ctx); err != nil {
			return err
		}
	}
	if err := s.CheckDriveRestoreReset(ctx); err != nil {
		return err
	}
	data := path.Join(s.App.dir(), "data")
	if _, err := s.App.Runner.Run(ctx, "test -d "+remote.Quote(data), nil); err != nil {
		return errors.New("restored Drive data directory is missing")
	}
	if err := trustedDir(ctx, s.App.Runner, data); err != nil {
		return err
	}
	run, err := s.driveRestoreRun(data, "restore-reset", "-to", "/restore")
	if err != nil {
		return err
	}
	if _, err := s.App.Runner.Run(ctx, run, nil); err != nil {
		return fmt.Errorf("Drive access-grant reset failed; restored app remains stopped: %w", err)
	}
	return nil
}
