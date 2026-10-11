package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// KyNotes retains its version-1 native bundle and live SSO/recovery service.
// The adapter reads only known report fields and requires observed present state
// on inspection, rather than re-applying the bundle as a substitute for checking.
func notesSetup(ctx context.Context, exec identityExec, bundle string, check bool) ([]byte, error) {
	if err := notesCapabilities(ctx, exec, false); err != nil {
		return nil, err
	}
	args := []string{"apply-setup", "--file", bundle}
	if check {
		args = append(args, "--check")
	}
	b, err := exec(ctx, args)
	if err != nil {
		return nil, err
	}
	return notesReport(b, check)
}

func notesCapabilities(ctx context.Context, exec identityExec, prepare bool, extra ...string) error {
	b, err := exec(ctx, []string{"apply-setup", "--capabilities"})
	if err != nil {
		return err
	}
	var caps struct {
		Schema, Service, BundleSchema string
		Features                      []string
	}
	if len(b) > 128<<10 || json.Unmarshal(b, &caps) != nil || caps.Schema != "ky.setup-capabilities/1" || caps.Service != "kynotes" || caps.BundleSchema != "kynotes.setup/1" {
		return errors.New("KyNotes does not advertise the required setup contract")
	}
	features := []string{"sso", "issuer-bound-admin", "dedicated-admin", "protected-setup-files", "backup", "check"}
	features = append(features, extra...)
	if prepare {
		features = append(features, "bundle-preparation")
	}
	for _, required := range features {
		found := false
		for _, f := range caps.Features {
			found = found || f == required
		}
		if !found {
			return fmt.Errorf("KyNotes lacks setup capability %s; upgrade the product first", required)
		}
	}
	return nil
}

func stageNotes(ctx context.Context, exec identityExec, inputExec func(context.Context, []string, []byte) ([]byte, error), n *stack.NotesPreparation, bundle string, check bool) error {
	if err := notesCapabilities(ctx, exec, true); err != nil {
		return err
	}
	raw, err := json.Marshal(n)
	if err != nil || len(raw) > 64<<10 {
		return errors.New("KyNotes preparation exceeds native input limit")
	}
	return stageNativeBundle(ctx, inputExec, raw, bundle, "kynotes.setup-preparation/1", check)
}

func notesReport(b []byte, check bool) ([]byte, error) {
	var report struct {
		Version int `json:"version"`
		Results []struct {
			Section string `json:"section"`
			Status  string `json:"status"`
		} `json:"results"`
		Handover struct {
			URL                    string   `json:"url"`
			AdminUsernames         []string `json:"adminUsernames"`
			RecoveryKeyFingerprint string   `json:"recoveryKeyFingerprint"`
			BackupDir              string   `json:"backupDir"`
			Version                string   `json:"version"`
		} `json:"handover"`
	}
	if len(b) > 128<<10 || json.Unmarshal(b, &report) != nil || report.Version != 1 || report.Results == nil || len(report.Results) > 128 || len(report.Handover.AdminUsernames) > 16 {
		return nil, errors.New("invalid KyNotes setup report")
	}
	for _, result := range report.Results {
		if (result.Section != "sso" && !strings.HasPrefix(result.Section, "admin:") && !strings.HasPrefix(result.Section, "backup.")) || !reportText(result.Section, true) || result.Status != "present" && (check || result.Status != "created") {
			return nil, errors.New("KyNotes setup is incomplete or inspection reported a mutation")
		}
	}
	for _, field := range append([]string{report.Handover.URL, report.Handover.RecoveryKeyFingerprint, report.Handover.BackupDir, report.Handover.Version}, report.Handover.AdminUsernames...) {
		if !reportText(field, false) {
			return nil, errors.New("invalid KyNotes handover field")
		}
	}
	return json.Marshal(report)
}
