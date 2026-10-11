package cli

import (
	"context"
	"strings"
	"testing"
)

func TestPostSetupPreservesPendingAndRejectsCheckMutation(t *testing.T) {
	caps := []byte(`{"schema":"ky.setup-capabilities/1","service":"kypost","bundleSchema":"kypost.setup/1","features":["sso","retained-pairing-key","domains","recovery-pairing","secret-files","check"]}`)
	report := []byte(`{"version":1,"applied":[],"unchanged":["sso"],"dns":[],"pending":["verify actual mail receipt"],"configurationApplied":true,"secret":"must-not-render"}`)
	exec := func(_ context.Context, args []string) ([]byte, error) {
		if args[1] == "--capabilities" {
			return caps, nil
		}
		return report, nil
	}
	out, err := postSetup(context.Background(), exec, "/run/private/bundle", true)
	if err != nil || strings.Contains(string(out), "must-not-render") || !strings.Contains(string(out), "verify actual mail receipt") {
		t.Fatal(string(out), err)
	}
	report = []byte(`{"version":1,"applied":["sso"],"unchanged":[],"dns":[],"pending":[],"configurationApplied":true}`)
	if _, err = postSetup(context.Background(), exec, "/run/private/bundle", true); err == nil {
		t.Fatal("accepted mutated check")
	}
}
