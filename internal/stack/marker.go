package stack

import (
	"fmt"
	"regexp"
)

// Marker records, on each target and under its lock, the plan revision that last changed it,
// that plan's installation_id and who owns that plan.
type Marker struct {
	Revision       int    `json:"revision"`
	Owner          string `json:"owner"` // cli or runner
	RunnerID       string `json:"runner_id"`
	InstallationID string `json:"installation_id,omitempty"` // empty on markers from older builds
}

var runnerIDRE = regexp.MustCompile(`^[0-9a-f-]{0,36}$`)

func (m Marker) Validate() error {
	if m.Revision < 0 || m.Owner != "cli" && m.Owner != "runner" || !runnerIDRE.MatchString(m.RunnerID) ||
		m.InstallationID != "" && !validInstallationID(m.InstallationID) {
		return fmt.Errorf("plan marker %+v is malformed", m)
	}
	return nil
}
