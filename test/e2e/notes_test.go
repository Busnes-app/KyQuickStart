//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

func testNotesIntegration(t *testing.T, ctx context.Context, state, hostFP string) {
	binary := os.Getenv("KYQ_TEST_NOTES_BINARY")
	if binary == "" {
		return
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("KYQ_TEST_NOTES_BINARY must be absolute")
	}
	data := t.TempDir()
	if err := os.Mkdir(filepath.Join(data, "backups"), 0700); err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	settings := "data_dir: /data\nserver:\n  bind: 0.0.0.0:8080\n  behind_proxy: true\n  trusted_proxies: [127.0.0.1/32]\nsecrets:\n  pairing_secret: " + hex.EncodeToString(secret) + "\n  server_salt_key: " + hex.EncodeToString(secret) + "\n"
	if err := os.WriteFile(filepath.Join(data, "kynotes.yaml"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "bundle.json"), []byte(`{"version":1,"backup":{"dir":"/data/backups","keep":7,"depositInterval":"24h"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "shared.json"), []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(data, "shared.json"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("bundle.json", filepath.Join(data, "linked.json")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(data, "fifo.json"), 0600); err != nil {
		t.Fatal(err)
	}
	name := "kyq-notes-" + hex.EncodeToString(secret[:4])
	docker(t, "run", "-d", "--name", name, "--memory", "768m", "--memory-swap", "768m", "--cpus", "1", "--pids-limit", "128", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "-v", binary+":/kynotes-server:ro", "-v", data+":/data", "-e", "KYNOTES_BACKUP_DIR=/data/backups", "-e", "GOMAXPROCS=2", "-e", "GOMEMLIMIT=512MiB", "-e", "KYNOTES_BACKUP_KEEP=7", "--entrypoint", "/kynotes-server", "kyq-e2e-sshd", "--config", "/data/kynotes.yaml")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		if exec.Command("docker", "exec", name, "/kynotes-server", "healthcheck", "--config", "/data/kynotes.yaml").Run() == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disposable KyNotes did not become ready")
		}
		time.Sleep(250 * time.Millisecond)
	}
	for _, file := range []string{"shared.json", "linked.json", "fifo.json"} {
		var out bytes.Buffer
		attemptCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := cli.Run(attemptCtx, []string{"integrate-product", "--product", "kynotes", "--state", state, "--target", "e2e", "--container", name, "--bundle", "/data/" + file, "--trust-host-key", "e2e=" + hostFP}, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "e2e", Out: &out})
		cancel()
		if err == nil || attemptCtx.Err() == context.DeadlineExceeded || out.Len() != 0 {
			t.Fatalf("unsafe setup input %s was accepted, blocked, or emitted a report: %v", file, err)
		}
	}
	for i := 0; i < 3; i++ {
		args := []string{"integrate-product", "--product", "kynotes", "--state", state, "--target", "e2e", "--container", name, "--bundle", "/data/bundle.json", "--trust-host-key", "e2e=" + hostFP}
		if i == 1 {
			args = append(args, "--check")
		}
		var out bytes.Buffer
		if err := cli.Run(ctx, args, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "e2e", Out: &out}); err != nil {
			t.Fatal(err)
		}
		var report struct {
			Version int
			Results []struct{ Section, Status string }
		}
		if json.Unmarshal(out.Bytes(), &report) != nil || report.Version != 1 || len(report.Results) != 3 {
			t.Fatal("invalid native report", out.String())
		}
		created := 0
		for _, r := range report.Results {
			if r.Status == "created" {
				created++
			} else if r.Status != "present" {
				t.Fatal("incomplete setup", out.String())
			}
		}
		if i == 0 && created != 1 || i > 0 && created != 0 {
			t.Fatal("setup did not resume cleanly", out.String())
		}
		if bytes.Contains(out.Bytes(), []byte(hex.EncodeToString(secret))) {
			t.Fatal("setup exposed fixture secret")
		}
	}
	t.Log("real KyNotes backup setup through SSH: create, read-only inspection and resume passed")
	st, err := stack.Load(filepath.Join(state, "stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	st.Apps, st.Edges = nil, nil
	st.Existing = []stack.Existing{{Product: "kynotes", Target: "e2e", URL: "https://notes.example.test"}}
	st.Integrations = []stack.Integration{{Product: "kynotes", Target: "e2e", Container: name, Bundle: "/data/installer/setup.json", Notes: &stack.NotesPreparation{Version: 1, Backup: &stack.NotesBackup{Dir: "/data/backups", Keep: 7, DepositInterval: "24h"}}}}
	runPreparedNotesStack(t, ctx, st, []string{"--trust-host-key", "e2e=" + hostFP})
}

func runPreparedNotesStack(t *testing.T, ctx context.Context, st *stack.Stack, trust []string) {
	t.Helper()
	state := t.TempDir()
	raw, err := yaml.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "stack.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	args := append([]string{"integrate-stack", "--state", state}, trust...)
	if err := cli.Run(ctx, append(append([]string{}, args...), "--check"), cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "e2e", Out: &out}); err == nil || out.Len() != 0 {
		t.Fatal("missing prepared bundle was inspected as ready", err)
	}
	for _, check := range []bool{false, true, false} {
		out.Reset()
		current := append([]string{}, args...)
		if check {
			current = append(current, "--check")
		}
		if err := cli.Run(ctx, current, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "e2e", Out: &out}); err != nil {
			t.Fatal("prepared stack", err)
		}
		if !bytes.Contains(out.Bytes(), []byte(`"product":"kynotes"`)) || !bytes.Contains(out.Bytes(), []byte(`"section":"backup.interval"`)) {
			t.Fatal("native prepared stack was not reported", out.String())
		}
	}
	// Changed saved intent must fail read-only comparison and publication, retaining
	// both the existing bundle and the already-applied product configuration.
	st.Integrations[0].Notes.Backup.Keep = 8
	raw, _ = yaml.Marshal(st)
	if err := os.WriteFile(filepath.Join(state, "stack.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, check := range []bool{true, false} {
		out.Reset()
		current := append([]string{}, args...)
		if check {
			current = append(current, "--check")
		}
		if err := cli.Run(ctx, current, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "e2e", Out: &out}); err == nil || out.Len() != 0 {
			t.Fatal("changed preparation accepted or reported ready", err)
		}
	}
	st.Integrations[0].Notes.Backup.Keep = 7
	raw, _ = yaml.Marshal(st)
	if err := os.WriteFile(filepath.Join(state, "stack.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := cli.Run(ctx, append(append([]string{}, args...), "--check"), cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "e2e", Out: &out}); err != nil {
		t.Fatal("changed intent damaged retained configuration", err)
	}
	t.Log("real native bundle preparation: missing check, create/check/resume, changed-intent refusal and retained configuration passed")
}
