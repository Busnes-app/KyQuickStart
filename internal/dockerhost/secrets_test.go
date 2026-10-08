package dockerhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
)

func localApp(t *testing.T, secrets ...string) App {
	return App{
		Name:    "hello",
		Root:    filepath.Join(t.TempDir(), "it's root"),
		Runner:  remote.Local{},
		Catalog: catalog.App{Manifest: catalog.Manifest{Name: "hello", Secrets: secrets}},
		Redact:  &engine.Redactor{},
	}
}

func TestSecretsCreatedOnceAndPrivate(t *testing.T) {
	ctx := context.Background()
	a := localApp(t, "db_password", "token")
	s := secretsStep{a}
	if done, err := s.Inspect(ctx); err != nil || done {
		t.Fatalf("Inspect before = %v, %v", done, err)
	}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(a.Root, "hello", "secrets", "token")
	first, _ := os.ReadFile(f)
	if len(first) != 43 {
		t.Fatalf("secret is %d bytes, want 43 (32 bytes base64url)", len(first))
	}
	if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(f)); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", fi.Mode().Perm())
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if got := a.Redact.Redact("x" + string(first)); got != "x[redacted]" {
		t.Errorf("secret not registered for redaction: %q", got)
	}
	if done, err := s.Inspect(ctx); err != nil || !done {
		t.Fatalf("Inspect after = %v, %v", done, err)
	}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(f); string(again) != string(first) {
		t.Error("secret overwritten")
	}
}

func TestSecretsVerifyRejectsOpenMode(t *testing.T) {
	ctx := context.Background()
	a := localApp(t, "token")
	s := secretsStep{a}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	os.Chmod(filepath.Join(a.Root, "hello", "secrets", "token"), 0o644)
	if err := s.Verify(ctx); err == nil {
		t.Fatal("Verify accepted mode 644")
	}
}

// Apply never produces an empty secret, so one found on disk fails Verify instead of being skipped.
func TestEmptySecretFailsVerify(t *testing.T) {
	ctx := context.Background()
	a := localApp(t, "token")
	dir := filepath.Join(a.Root, "hello", "secrets")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "token"), nil, 0o600)
	if err := (secretsStep{a}).Verify(ctx); err == nil {
		t.Fatal("Verify accepted an empty secret")
	}
}

func TestApplyLeavesOnlySecretFiles(t *testing.T) {
	ctx := context.Background()
	a := localApp(t, "db_password", "token")
	if err := (secretsStep{a}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(a.Root, "hello", "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 || names[0] != "db_password" || names[1] != "token" {
		t.Errorf("secrets dir holds %v", names)
	}
}

func TestApplyFailsWhenDirCannotBeCreated(t *testing.T) {
	a := localApp(t, "token")
	os.WriteFile(a.Root, nil, 0o600)
	if err := (secretsStep{a}).Apply(context.Background()); err == nil {
		t.Fatal("Apply succeeded without a secrets directory")
	}
}

func TestVerifyRejectsWrongLengthWithoutValue(t *testing.T) {
	ctx := context.Background()
	a := localApp(t, "token")
	dir := filepath.Join(a.Root, "hello", "secrets")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "token"), []byte("0123456789"), 0o600)
	err := (secretsStep{a}).Verify(ctx)
	if err == nil {
		t.Fatal("Verify accepted a 10-byte secret")
	}
	if strings.Contains(err.Error(), "0123456789") || !strings.Contains(err.Error(), "token") {
		t.Errorf("error %q must name the secret and not show its value", err)
	}
}
