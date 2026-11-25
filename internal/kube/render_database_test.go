package kube

import (
	"encoding/json"
	"os"
	"testing"
)

// TestWriteDatabasePod is scripts/test-postgres-17-to-18-pod.sh's renderer, skipped unless
// KYQ_RENDER_POD names a file: it writes the pod spec of the replicated KyIdentity's database
// Deployment as apply renders it.
func TestWriteDatabasePod(t *testing.T) {
	to := os.Getenv("KYQ_RENDER_POD")
	if to == "" {
		t.Skip("KYQ_RENDER_POD is not set")
	}
	a, _, _ := replicatedApp(t, 2, "")
	b, err := json.Marshal(render(a).Database.Spec.Template.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
