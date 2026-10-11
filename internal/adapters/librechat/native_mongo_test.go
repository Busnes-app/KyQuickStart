package librechat

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"
)

// Invoked only inside the disposable Python-owned fixture; uses native packages
// and real MongoDB, not the unit-test model substitute.
func TestNativeMongoInspectionClient(t *testing.T) {
	container := os.Getenv("KYQ_TEST_LIBRECHAT_CONTAINER")
	if container == "" {
		t.Skip("requires disposable native container fixture")
	}
	user := os.Getenv("KYQ_TEST_LIBRECHAT_USER")
	execute := func(ctx context.Context, argv []string, input []byte) ([]byte, error) {
		args := append([]string{"exec", "-i", "--user", user, container}, argv...)
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Stdin = bytes.NewReader(input)
		return cmd.Output()
	}
	c, err := New(execute, "/fixture", "/module/command.cjs")
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{ID: "0123456789abcdef01234567", Issuer: "https://identity.example.test", Subject: "subject-雪"}
	for range 2 {
		a, err := c.Inspect(context.Background(), b)
		if err != nil || a.Binding != b || a.Role != "USER" {
			t.Fatal("real native inspection", err)
		}
	}
	b.Subject = "foreign-subject"
	if _, err := c.Inspect(context.Background(), b); err == nil {
		t.Fatal("foreign subject admitted")
	}
}
