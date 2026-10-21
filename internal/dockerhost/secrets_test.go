package dockerhost

import (
	"context"
	"encoding/base64"
	"fmt"
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
		Root:    filepath.Join(realTemp(t), "it's root"),
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

// The target fixture controls random bytes. Workstation-generated credentials or
// secret values passed as stdin/argv cannot satisfy this proof.
type targetSecretRunner struct {
	t        *testing.T
	bin      string
	commands []string
}

func (r *targetSecretRunner) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	r.t.Helper()
	if len(stdin) != 0 {
		r.t.Fatal("credential generation sent workstation input")
	}
	r.commands = append(r.commands, cmd)
	return (remote.Local{}).Run(ctx, "PATH="+remote.Quote(r.bin)+":$PATH\n"+cmd, nil)
}

func TestSecretsUseTargetRandomAndReadExisting(t *testing.T) {
	a := localApp(t, "token")
	bin := realTemp(t)
	// A disposable target-side dd fixture, not a production random-source override.
	script := `#!/bin/sh
for arg; do
  case "$arg" in of=*) file=${arg#of=};; esac
done
printf 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' > "$file"
`
	if err := os.WriteFile(filepath.Join(bin, "dd"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	r := &targetSecretRunner{t: t, bin: bin}
	a.Runner = r
	s := secretsStep{a}
	if err := s.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(a.Root, "hello", "secrets", "token")
	value, err := os.ReadFile(file)
	want := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("A", 32)))
	if err != nil || string(value) != want {
		t.Fatal("secret did not originate from the target random source", err)
	}
	for _, cmd := range r.commands {
		if strings.Contains(cmd, want) {
			t.Fatal("credential entered command arguments")
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "dd"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(context.Background()); err != nil {
		t.Fatal("resume regenerated an existing credential", err)
	}
	if err := s.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.Redact.Redact(want) != "[redacted]" {
		t.Fatal("existing credential not registered for redaction")
	}
}

func TestFailedTargetRandomDoesNotPublishOrLeaveScratch(t *testing.T) {
	for _, length := range []int{0, 7} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			a := localApp(t, "token")
			bin := realTemp(t)
			script := fmt.Sprintf(`#!/bin/sh
for arg; do
  case "$arg" in of=*) file=${arg#of=};; esac
done
printf '%%s' %s > "$file"
`, remote.Quote(strings.Repeat("A", length)))
			if err := os.WriteFile(filepath.Join(bin, "dd"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			a.Runner = &targetSecretRunner{t: t, bin: bin}
			if err := (secretsStep{a}).Apply(context.Background()); err == nil {
				t.Fatal("short random read was published")
			}
			entries, err := os.ReadDir(filepath.Join(a.Root, "hello", "secrets"))
			if err != nil || len(entries) != 0 {
				t.Fatal("failed generation left published/scratch credentials", err)
			}
		})
	}
}

func TestVerifyRefusesCredentialSymlinksAndMalformedEncoding(t *testing.T) {
	for _, kind := range []string{"symlink", "invalid-encoding"} {
		t.Run(kind, func(t *testing.T) {
			a := localApp(t, "token")
			dir := filepath.Join(a.Root, "hello", "secrets")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "token")
			if kind == "symlink" {
				source := filepath.Join(realTemp(t), "source")
				if err := os.WriteFile(source, []byte(strings.Repeat("A", 43)), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(source, file); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(file, []byte(strings.Repeat("!", 43)), 0600); err != nil {
				t.Fatal(err)
			}
			if err := (secretsStep{a}).Verify(context.Background()); err == nil {
				t.Fatal("unsafe credential accepted")
			}
		})
	}
}

func TestFailedTargetEncoderDoesNotPublishCompleteOutput(t *testing.T) {
	a := localApp(t, "token")
	bin := realTemp(t)
	// Valid-looking output followed by failure must not survive a hidden pipeline exit.
	script := "#!/bin/sh\nprintf '%s' " + remote.Quote(strings.Repeat("A", 43)) + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "base64"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a.Runner = &targetSecretRunner{t: t, bin: bin}
	if err := (secretsStep{a}).Apply(context.Background()); err == nil {
		t.Fatal("failed encoder published a credential")
	}
	entries, err := os.ReadDir(filepath.Join(a.Root, "hello", "secrets"))
	if err != nil || len(entries) != 0 {
		t.Fatal("failed encoding left published/scratch credentials", err)
	}
}

func TestTargetSecretCreationFailureNeverExposesOutput(t *testing.T) {
	a := localApp(t, "token")
	marker := "sensitive-target-value"
	a.Runner = &fakeRunner{reply: func(cmd string) ([]byte, error) {
		if strings.Contains(cmd, "dd if=/dev/urandom") {
			return []byte(marker), &remote.ExitError{Code: 1, Stderr: []byte(marker)}
		}
		return nil, nil
	}}
	err := (secretsStep{a}).Apply(context.Background())
	if err == nil || strings.Contains(err.Error(), marker) {
		t.Fatal("target creation output escaped the credential boundary")
	}
}

// An app that declares no secrets gets no secrets step.
func TestStepsSecretsOnlyWhenDeclared(t *testing.T) {
	for secrets, want := range map[int]string{0: "hello.deploy hello.health", 1: "hello.secrets hello.deploy hello.health"} {
		var ids []string
		for _, s := range Steps(localApp(t, []string{"token"}[:secrets]...)) {
			ids = append(ids, s.ID())
		}
		if got := strings.Join(ids, " "); got != want {
			t.Errorf("%d secrets: steps %s", secrets, got)
		}
	}
}

// BookStack's APP_KEY: Laravel's "base64:" and 32 random bytes in standard base64, once.
func TestLaravelSecretFormat(t *testing.T) {
	ctx := context.Background()
	a := localApp(t, "app_key", "db_password")
	a.Catalog.SecretFormats = map[string]string{"app_key": "laravel"}
	s := secretsStep{a}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(a.Root, "hello", "secrets", "app_key")
	key, _ := os.ReadFile(f)
	raw, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(string(key), "base64:"))
	if !strings.HasPrefix(string(key), "base64:") || err != nil || len(raw) != 32 {
		t.Fatalf("app_key %q", key)
	}
	if other, _ := os.ReadFile(filepath.Join(a.Root, "hello", "secrets", "db_password")); len(other) != 43 {
		t.Fatalf("db_password is %d bytes", len(other))
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(f); string(again) != string(key) {
		t.Fatal("app_key was replaced")
	}
	// A base64url key, or one of another length, fails Verify.
	for _, bad := range []string{strings.NewReplacer("+", "-", "/", "_").Replace(string(key)) + "-_", "base64:" + base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		os.Chmod(f, 0o600)
		os.WriteFile(f, []byte(bad), 0o600)
		if s.Verify(ctx) == nil {
			t.Errorf("Verify accepted %q", bad)
		}
	}
}
