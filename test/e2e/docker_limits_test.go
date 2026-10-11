//go:build e2e

package e2e

import (
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/kube"
)

// Inspect actual Docker configuration: host defaults must not leave old optional
// native product fixtures unbounded or override a deliberately selected limit.
func TestDockerFixtureLimits(t *testing.T) {
	// Pull first: an uncached image's pull progress would corrupt `run -d`'s ID output.
	docker(t, "pull", "-q", kube.CredentialGeneratorImage)
	for _, tc := range []struct {
		name         string
		flags        []string
		memory, swap int64
		cpus, pids   int64
	}{
		{name: "defaults", memory: 512 << 20, swap: 512 << 20, cpus: 1e9, pids: 128},
		{name: "selected-memory", flags: []string{"--memory", "64m"}, memory: 64 << 20, swap: 64 << 20, cpus: 1e9, pids: 128},
		{name: "explicit", flags: []string{"--memory=64m", "--memory-swap=96m", "--cpus=0.5", "--pids-limit=32"}, memory: 64 << 20, swap: 96 << 20, cpus: 5e8, pids: 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"run", "-d", "--network", "none", "--read-only", "--cap-drop", "ALL"}, tc.flags...)
			args = append(args, kube.CredentialGeneratorImage, "sleep", "60")
			id := docker(t, args...)
			t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
			var actual struct {
				Memory     int64
				MemorySwap int64
				NanoCpus   int64
				PidsLimit  int64
			}
			if err := json.Unmarshal([]byte(docker(t, "inspect", "--format", "{{json .HostConfig}}", id)), &actual); err != nil {
				t.Fatal(err)
			}
			if actual.Memory != tc.memory || actual.MemorySwap != tc.swap || actual.NanoCpus != tc.cpus || actual.PidsLimit != tc.pids {
				t.Fatalf("actual fixture limits: %+v", actual)
			}
		})
	}
}
