package librechat

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestNativeLifecyclePrerequisites runs unchanged pinned upstream modules with
// substituted dependency boundaries. It does not qualify MongoDB or HTTP access.
func TestNativeLifecyclePrerequisites(t *testing.T) {
	root := os.Getenv("KYQ_TEST_LIBRECHAT_SOURCE")
	if root == "" {
		t.Skip("set KYQ_TEST_LIBRECHAT_SOURCE to the pinned v0.8.8 checkout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "--max-old-space-size=256", "native_qualification.cjs", root).CombinedOutput()
	if err != nil {
		t.Fatalf("native prerequisite qualification: %v\n%s", err, output)
	}
	t.Log(string(output))
}
