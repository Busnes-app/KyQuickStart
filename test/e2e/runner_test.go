//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/google/uuid"
)

// testRunner hands a demo plan to a runner container built from this checkout, installs and
// uninstalls hello through its socket, pulls the plan back, checks that the CLI may not
// change it, and revokes the runner's access.
func testRunner(t *testing.T, ctx context.Context, demoState, port, base, sshd string) {
	exec.Command("docker", "compose", "-p", "kyq-hello", "down").Run() // testSuiteCommands' unmanaged app
	docker(t, "build", "-q", "--build-arg", "VERSION=e2e", "-t", "kyq-e2e-runner", "../..")

	short := func() string {
		d, err := os.MkdirTemp("", "kyq")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(d) })
		return d
	}
	sockDir, runnerState, state := short(), short(), short()
	id := make([]byte, 4)
	rand.Read(id)
	runner := "kyq-e2e-runner-" + hex.EncodeToString(id)
	// The runner reaches the sshd fixture where the workstation does, on the host's loopback.
	docker(t, "run", "-d", "--name", runner, "--network", "host", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"-v", runnerState+":/state", "-v", sockDir+":/run/kyquickstart", "kyq-e2e-runner")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", runner).CombinedOutput()
			t.Logf("runner logs:\n%s", out)
		}
		exec.Command("docker", "rm", "-f", runner).Run()
		exec.Command("docker", "compose", "-p", "kyq-hello", "down").Run()
	})
	socket := filepath.Join(sockDir, "runner.sock")
	for i := 0; ; i++ {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if i == 100 {
			t.Fatal("runner socket never appeared")
		}
		time.Sleep(100 * time.Millisecond)
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, "http://runner"+path, r)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	operate := func(kind string, apps ...string) map[string]any {
		t.Helper()
		opID := uuid.NewString()
		req := map[string]any{"id": opID, "kind": kind, "apps": apps, "acknowledge_no_backup": false, "actor": "e2e-admin"}
		if kind == "install" {
			req["target"] = "e2e"
		}
		if code, out := call("POST", "/v1/operations", req); code != 202 {
			t.Fatalf("%s: %d %v", kind, code, out)
		}
		for range 600 {
			_, op := call("GET", "/v1/operations/"+opID, nil)
			if op["state"] != "running" {
				t.Logf("%s: %v", kind, op)
				return op
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("%s never finished", kind)
		return nil
	}
	run := func(args ...string) error {
		var out bytes.Buffer
		err := cli.Run(ctx, append(args, "--state", state), cli.Options{Catalog: os.DirFS("../../internal/catalog/testdata/apps"), ReleaseSet: "e2e", Out: &out})
		t.Logf("%s:\n%s", strings.Join(args, " "), out.String())
		return err
	}
	running := func() string {
		return docker(t, "ps", "--filter", "label=com.docker.compose.project=kyq-hello", "--filter", "label=ky.managed-by=kyquickstart", "--format", "{{.Names}}")
	}

	kh, _ := os.ReadFile(filepath.Join(demoState, "known_hosts"))
	os.WriteFile(filepath.Join(state, "known_hosts"), kh, 0o600)
	plan := fmt.Sprintf("version: 1\ndemo: true\nrevision: 1\ntargets:\n  - name: e2e\n    host: 127.0.0.1\n    port: %s\n    user: root\n    root: %s\napps: []\n", port, base+"/runner")
	os.WriteFile(filepath.Join(state, "stack.yaml"), []byte(plan), 0o600)

	if code, out := call("GET", "/v1/status", nil); code != 409 || fmt.Sprint(out["error"]) == "" {
		t.Fatalf("status before handoff: %d %v", code, out)
	}
	for range 2 { // a rerun reconciles
		if err := run("handoff", "--kyyard-target", "e2e", "--runner-container", runner); err != nil {
			t.Fatalf("handoff: %v", err)
		}
	}
	keys := docker(t, "exec", sshd, "cat", "/root/.ssh/authorized_keys")
	if strings.Count(keys, "kyquickstart-runner ") != 1 || !strings.HasPrefix(keys, "restrict ssh-ed25519 ") {
		t.Fatalf("authorized_keys:\n%s", keys)
	}

	if op := operate("install", "hello"); op["state"] != "succeeded" || running() == "" {
		t.Fatalf("install through the runner: %v", op)
	}
	code, st := call("GET", "/v1/status", nil)
	if r, _ := st["runner"].(map[string]any); code != 200 || r["plan_owner"] != "runner" || r["plan_revision"] != float64(4) {
		t.Fatalf("status: %d %v", code, st)
	}
	if op := operate("uninstall", "hello"); op["state"] != "succeeded" || running() != "" {
		t.Fatalf("uninstall through the runner: %v", op)
	}

	if err := run("pull-plan", "--kyyard-target", "e2e", "--runner-container", runner); err != nil {
		t.Fatalf("pull-plan: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "stack.yaml")); !strings.Contains(string(b), "revision: 5") {
		t.Fatalf("pulled plan:\n%s", b)
	}
	if err := run("install", "hello", "--target", "e2e"); err == nil || !strings.Contains(err.Error(), "owned_by_runner") {
		t.Fatalf("CLI install of a runner-owned plan: %v", err)
	}

	if err := run("handoff", "--revoke"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if keys := docker(t, "exec", sshd, "cat", "/root/.ssh/authorized_keys"); strings.Contains(keys, "kyquickstart-runner") {
		t.Fatalf("runner key left after revoke:\n%s", keys)
	}
	if marker := docker(t, "exec", sshd, "cat", base+"/runner/.plan"); !strings.Contains(marker, `"owner":"cli"`) {
		t.Fatalf("marker after revoke: %s", marker)
	}
	if op := operate("install", "hello"); op["state"] != "failed" || running() != "" {
		t.Fatalf("runner acted after revoke: %v", op)
	}
}
