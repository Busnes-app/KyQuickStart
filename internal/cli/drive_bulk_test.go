package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

type bulkCompose struct {
	Services struct {
		App struct {
			Entrypoint  []string          `yaml:"entrypoint"`
			Environment map[string]string `yaml:"environment"`
			Secrets     []string          `yaml:"secrets"`
		} `yaml:"app"`
	} `yaml:"services"`
	Secrets map[string]struct {
		File string `yaml:"file"`
	} `yaml:"secrets"`
}

var bulkDest = stack.BackupDestination{Repository: "rest:https://restic.example.com:8000/nas", CredentialFile: "/home/kyq/.config/kyquickstart/rest-password"}

// With a backup destination, KyDrive's documents go to <repository>/kydrive-bulk, as the
// destination's user, and the password reaches only a 0600 file in KyDrive's private /tmp.
func TestWithBulkRepository(t *testing.T) {
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	plain := cat["kydrive"].WithRoute(catalog.Route{Host: "drive.example.com", Forward: "192.0.2.1:18443", ForwardAddr: "192.0.2.1"}).WithPrivateRecovery(false)
	edited, err := withEditor(plain, "office.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for name, drive := range map[string]catalog.App{"without editor": plain, "with editor": edited} {
		got, err := withBulkRepository(drive, bulkDest)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var c bulkCompose
		if err := yaml.Unmarshal(got.Compose, &c); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, got.Compose)
		}
		app := c.Services.App
		if app.Environment["KYDRIVE_BULK_BACKUP_REPOSITORY"] != "rest:https://restic.example.com:8000/nas/kydrive-bulk" || app.Environment["KYDRIVE_BULK_BACKUP_CREDENTIALS_FILE"] != bulkCredentials {
			t.Fatalf("%s: environment = %v", name, app.Environment)
		}
		if !slices.Contains(app.Secrets, bulkPassword) || c.Secrets[bulkPassword].File != bulkDest.CredentialFile {
			t.Fatalf("%s: secrets = %v, files = %v", name, app.Secrets, c.Secrets)
		}
		if slices.Contains(got.Secrets, bulkPassword) {
			t.Fatalf("%s: the rest-server password is the operator's file, never a generated secret", name)
		}
		if name == "with editor" && !slices.Contains(app.Secrets, editorSecret) {
			t.Fatalf("with editor: lost editor_secret: %v", app.Secrets)
		}
		runEntrypoint(t, name, app.Entrypoint)
	}
}

// runEntrypoint runs KyDrive's entrypoint script with its paths moved into a temporary
// directory and the server replaced by a no-op, then checks the credentials file it writes.
func runEntrypoint(t *testing.T, name string, entrypoint []string) {
	t.Helper()
	if len(entrypoint) < 3 || entrypoint[0] != "/bin/sh" || entrypoint[1] != "-c" {
		t.Fatalf("%s: entrypoint = %q", name, entrypoint)
	}
	dir := t.TempDir()
	for _, f := range []string{"session_secret", editorSecret, bulkPassword} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("pw-"+f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	creds := filepath.Join(dir, "creds")
	script := strings.NewReplacer("$$", "$", "/run/secrets/", dir+"/", bulkCredentials, creds, "exec /app/kydrive-server", "exit 0").Replace(entrypoint[2])
	if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("%s: entrypoint failed: %v\n%s", name, err, out)
	}
	raw, err := os.ReadFile(creds)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "RESTIC_REST_USERNAME=nas\nRESTIC_REST_PASSWORD=pw-"+bulkPassword+"\n" {
		t.Fatalf("%s: credentials file = %q", name, raw)
	}
	if fi, _ := os.Stat(creds); fi.Mode().Perm() != 0o600 {
		t.Fatalf("%s: credentials file mode = %v, want 0600", name, fi.Mode().Perm())
	}
}

// KyDrive's restic trusts only the system's CAs, so a destination with its own CA is refused
// rather than failing at the first document backup; without a destination nothing changes.
func TestBulkPlan(t *testing.T) {
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	withCA := bulkDest
	withCA.CAFile = "/home/kyq/.config/kyquickstart/rest-ca.pem"
	if _, err := withBulkRepository(cat["kydrive"], withCA); err == nil || !strings.Contains(err.Error(), "ca_file") {
		t.Fatalf("a destination with ca_file was accepted: %v", err)
	}
	st := &stack.Stack{Targets: []stack.Target{{Name: "nas"}}, Apps: []stack.App{{Name: "kydrive", Target: "nas"}}}
	if _, ok, err := bulkPlan(st, cat); ok || err != nil {
		t.Fatalf("bulkPlan without a destination = %v, %v", ok, err)
	}
	st.Targets[0].Backup = &withCA
	if _, ok, err := bulkPlan(st, cat); ok || err != nil {
		t.Fatalf("bulkPlan with a private CA = %v, %v; want KyDrive left as before", ok, err)
	}
	st.Targets[0].Backup = &bulkDest
	if drive, ok, err := bulkPlan(st, cat); !ok || err != nil || !strings.Contains(string(drive.Compose), "/nas/kydrive-bulk") {
		t.Fatalf("bulkPlan with a destination = %v, %v", ok, err)
	}
}

type bulkInitRunner struct{ ran []string }

func (r *bulkInitRunner) Run(_ context.Context, cmd string, _ []byte) ([]byte, error) {
	r.ran = append(r.ran, cmd)
	return nil, nil
}
func (r *bulkInitRunner) Close() error { return nil }

// bulk-init loads KyDrive's whole configuration, which requires KY_SESSION_SECRET; docker
// compose exec skips the entrypoint that sets it, so the step reads it from the secret itself.
func TestBulkInitSetsSessionSecret(t *testing.T) {
	r := &bulkInitRunner{}
	target := stack.Target{Name: "nas", Root: "/srv/kyq", Backup: &bulkDest}
	step := bulkInit(sshDriver{t: target, c: r}, target)
	if step == nil {
		t.Fatal("no bulk-init step with a backup destination")
	}
	if err := step.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.ran) != 1 {
		t.Fatalf("ran %q", r.ran)
	}
	want := "cd '/srv/kyq/kydrive' && docker compose -p kyq-kydrive -f compose.yaml -f compose.kyq.yaml exec -T app /bin/sh -c '" + bulkInitScript + "'"
	if r.ran[0] != want {
		t.Fatalf("ran %q, want %q", r.ran[0], want)
	}
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	if prefix, _, _ := strings.Cut(bulkInitScript, "exec "); !strings.Contains(string(cat["kydrive"].Compose), strings.ReplaceAll(prefix, "$", "$$")) {
		t.Fatalf("bulk-init no longer reads the session secret as KyDrive's entrypoint does")
	}
	if out, err := exec.Command("sh", "-n", "-c", bulkInitScript).CombinedOutput(); err != nil {
		t.Fatalf("bulk-init script: %v: %s", err, out)
	}
}
