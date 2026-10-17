//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func docker(t *testing.T, args ...string) string {
	t.Helper()
	if len(args) > 0 && args[0] == "run" {
		value := func(flag string) string {
			for i, arg := range args {
				if arg == flag && i+1 < len(args) {
					return args[i+1]
				}
				if strings.HasPrefix(arg, flag+"=") {
					return strings.TrimPrefix(arg, flag+"=")
				}
			}
			return ""
		}
		memory := value("--memory")
		if memory == "" {
			memory = "512m"
		}
		limits := []string{"run"}
		for _, limit := range [][2]string{{"--memory", memory}, {"--memory-swap", memory}, {"--cpus", "1"}, {"--pids-limit", "128"}} {
			if value(limit[0]) == "" {
				limits = append(limits, limit[0], limit[1])
			}
		}
		args = append(limits, args[1:]...)
	}
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// serveAgent runs an in-process SSH agent holding key and points SSH_AUTH_SOCK at it.
func serveAgent(t *testing.T, dir string, key ed25519.PrivateKey) {
	kr := agent.NewKeyring()
	if err := kr.Add(agent.AddedKey{PrivateKey: key}); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "agent.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go agent.ServeAgent(kr, c)
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
}

func TestApply(t *testing.T) {
	ctx := context.Background()
	work := t.TempDir()
	keys, state, foreignState := filepath.Join(work, "keys"), filepath.Join(work, "state"), filepath.Join(work, "foreign")
	for _, d := range []string{keys, state, foreignState} {
		os.MkdirAll(d, 0o700)
	}

	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(hostPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(keys, "host_ed25519"), pem.EncodeToMemory(block), 0o600)
	hostSigner, _ := ssh.NewSignerFromKey(hostPriv)
	hostFP := ssh.FingerprintSHA256(hostSigner.PublicKey())

	_, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	clientSigner, _ := ssh.NewSignerFromKey(clientPriv)
	os.WriteFile(filepath.Join(keys, "authorized_keys"), ssh.MarshalAuthorizedKey(clientSigner.PublicKey()), 0o600)
	serveAgent(t, work, clientPriv)

	id := make([]byte, 4)
	rand.Read(id)
	name := "kyq-e2e-" + hex.EncodeToString(id)
	docker(t, "build", "-q", "-t", "kyq-e2e-sshd", "sshd")

	// The installer runs as uid 0 in the container and requires root-owned ancestors, so the
	// root lives in a root-owned directory under /tmp, created and removed through Docker.
	// The root path is shared with the host so Compose bind mounts resolve there.
	base := "/tmp/" + name
	root := filepath.Join(base, "root")
	docker(t, "run", "--rm", "--memory", "64m", "--memory-swap", "64m", "--cpus", "1", "--pids-limit", "32", "-v", "/tmp:/tmp", "--entrypoint", "mkdir", "kyq-e2e-sshd", "-m", "755", base)
	t.Cleanup(func() {
		exec.Command("docker", "run", "--rm", "--memory", "64m", "--memory-swap", "64m", "--cpus", "1", "--pids-limit", "32", "-v", "/tmp:/tmp", "--entrypoint", "rm", "kyq-e2e-sshd", "-rf", base).Run()
	})
	docker(t, "run", "-d", "--name", name, "--memory", "256m", "--memory-swap", "256m", "--cpus", "1", "--pids-limit", "128", "-p", "127.0.0.1::22",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", keys+":/keys:ro", "-v", base+":"+base, "-v", work+":"+work, "kyq-e2e-sshd")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	t.Cleanup(func() {
		exec.Command("docker", "compose", "-p", "kyq-hello", "down").Run()
		exec.Command("docker", "exec", name, "rm", "-rf", filepath.Join(work, "root")).Run()
	})
	hostPort := docker(t, "port", name, "22")
	_, port, _ := net.SplitHostPort(hostPort)
	waitForSSH(t, port, hostFP)

	stackYAML := "version: 1\ntargets:\n  - name: e2e\n    host: 127.0.0.1\n    port: %s\n    user: root\n    root: %s\napps:\n  - name: hello\n    target: e2e\n"
	// Exercise the operator path: guided choices produce the plan used for actual SSH deployment.
	var guidedOut bytes.Buffer
	answers := strings.Join([]string{"yes", "1", "e2e", "docker", "127.0.0.1", "root", port, root, "install", "e2e", "0", "none", "yes", "finish", ""}, "\n")
	if err := cli.Run(ctx, []string{"plan", "--demo", "--state", state}, cli.Options{Catalog: os.DirFS("../../internal/catalog/apps"), In: strings.NewReader(answers), Out: &guidedOut}); err != nil {
		t.Fatalf("guided setup: %v\n%s", err, guidedOut.String())
	}
	os.WriteFile(filepath.Join(foreignState, "stack.yaml"), []byte(fmt.Sprintf(stackYAML, port, filepath.Join(work, "root"))), 0o600)

	applyIn := func(dir string) (string, error) {
		var out bytes.Buffer
		o := cli.Options{Catalog: os.DirFS("../../internal/catalog/testdata/apps"), ReleaseSet: "e2e", Out: &out}
		err := cli.Run(ctx, []string{"apply", "--state", dir, "--trust-host-key", "e2e=" + hostFP}, o)
		t.Logf("apply:\n%s", out.String())
		return out.String(), err
	}
	apply := func() (string, error) { return applyIn(state) }

	// A root under a directory another user owns (the test user, not the container's uid 0)
	// is refused before anything is written there.
	if _, err := applyIn(foreignState); err == nil || !strings.Contains(err.Error(), "owned by another user") {
		t.Fatalf("foreign-owned ancestor: err = %v", err)
	}
	if got := docker(t, "exec", name, "sh", "-c", "ls -A "+filepath.Join(work, "root")+" 2>/dev/null"); got != "" {
		t.Fatalf("installer wrote under a foreign-owned ancestor: %s", got)
	}

	// First apply installs.
	out, err := apply()
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	res, err := client.Get("http://127.0.0.1:18080")
	if err != nil {
		t.Fatalf("guided demo endpoint: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("guided demo endpoint: HTTP %d", res.StatusCode)
	}
	secretFile := filepath.Join(root, "hello", "secrets", "token")
	if mode := docker(t, "exec", name, "stat", "-c", "%a", secretFile); mode != "600" {
		t.Errorf("secret mode %s", mode)
	}
	secret := docker(t, "exec", name, "cat", secretFile)
	if strings.Contains(out, secret) {
		t.Error("secret printed")
	}
	filepath.WalkDir(state, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(secret)) {
				t.Errorf("secret stored in %s", p)
			}
		}
		return nil
	})
	labelled := docker(t, "ps", "--filter", "label=com.docker.compose.project=kyq-hello",
		"--filter", "label=ky.managed-by=kyquickstart", "--filter", "label=ky.release-set=e2e", "--format", "{{.Names}}")
	if labelled == "" {
		t.Error("no container carries the managed labels")
	}

	testIdentityIntegration(t, ctx, state, hostFP)
	testDriveIntegration(t, ctx, state, hostFP)
	testNotesIntegration(t, ctx, state, hostFP)
	testCredentialTransfer(t, ctx, state, hostFP)
	testPostIntegration(t, ctx, state, hostFP)
	testPulseIntegration(t, ctx, state, hostFP)
	testDNSIntegration(t, ctx, state, hostFP)
	testRecoveryIntegration(t, ctx, state, hostFP)
	testMessageIntegration(t, ctx, state, hostFP)
	testCalendarIntegration(t, ctx, state, hostFP)
	testYardIntegration(t, ctx, state, hostFP)

	// Second apply changes nothing.
	out, err = apply()
	if err != nil || status(out, "hello.secrets") != "skipped" || status(out, "hello.deploy") != "skipped" || status(out, "hello.health") != "skipped" {
		t.Fatalf("second apply: %v\n%s", err, out)
	}

	// Containers stopped by hand are redeployed. Health is skipped: its Verify passes after
	// the redeploy.
	docker(t, "compose", "-p", "kyq-hello", "stop")
	out, err = apply()
	if err != nil || status(out, "hello.deploy") != "ok" || status(out, "hello.secrets") != "skipped" {
		t.Fatalf("apply after stop: %v\n%s", err, out)
	}
	if running := docker(t, "ps", "--filter", "label=com.docker.compose.project=kyq-hello", "--filter", "status=running", "--format", "{{.Names}}"); running == "" {
		t.Error("not running after redeploy")
	}

	// A lock left by another run stops apply and survives.
	stale := `{"holder":"other run","run":"x","started":"2026-01-01T00:00:00Z"}`
	docker(t, "exec", name, "sh", "-c", "mkdir "+root+"/.lock && printf %s '"+stale+"' > "+root+"/.lock/holder")
	_, err = apply()
	var le *dockerhost.LockedError
	if !errors.As(err, &le) || le.Holder != "other run" {
		t.Fatalf("err = %v, want LockedError", err)
	}
	if got := docker(t, "exec", name, "cat", root+"/.lock/holder"); got != stale {
		t.Errorf("lock holder changed: %s", got)
	}
	docker(t, "exec", name, "rm", "-r", root+"/.lock")
	testSuiteCommands(t, ctx, state, hostFP, port, root, name)
	testRunner(t, ctx, state, port, base, name)
}

// testSuiteCommands drives install, upgrade, uninstall and unmanage over SSH, from a
// non-demo plan whose catalog version can be raised.
func testSuiteCommands(t *testing.T, ctx context.Context, demoState, hostFP, port, root, sshd string) {
	work := t.TempDir()
	state, cat := filepath.Join(work, "state"), filepath.Join(work, "apps", "hello")
	os.MkdirAll(state, 0o700)
	os.MkdirAll(cat, 0o700)
	kh, _ := os.ReadFile(filepath.Join(demoState, "known_hosts"))
	os.WriteFile(filepath.Join(state, "known_hosts"), kh, 0o600)
	os.WriteFile(filepath.Join(state, "stack.yaml"), []byte(fmt.Sprintf("version: 1\ntargets:\n  - name: e2e\n    host: 127.0.0.1\n    port: %s\n    user: root\n    root: %s\nrevision: 1\napps: []\n", port, root)), 0o600)
	manifest, _ := os.ReadFile("../../internal/catalog/testdata/apps/hello/manifest.yaml")
	compose, _ := os.ReadFile("../../internal/catalog/testdata/apps/hello/compose.yaml.tmpl")
	os.WriteFile(filepath.Join(cat, "compose.yaml.tmpl"), compose, 0o600)
	version := func(v string) {
		os.WriteFile(filepath.Join(cat, "manifest.yaml"), bytes.Replace(manifest, []byte("version: 1.11.0"), []byte("version: "+v), 1), 0o600)
	}
	run := func(args ...string) error {
		var out bytes.Buffer
		err := cli.Run(ctx, append(args, "--state", state), cli.Options{Catalog: os.DirFS(filepath.Dir(cat)), ReleaseSet: "e2e", Out: &out})
		t.Logf("%s:\n%s", strings.Join(args, " "), out.String())
		return err
	}
	labelled := func(label string) string {
		return docker(t, "ps", "--filter", "label=com.docker.compose.project=kyq-hello", "--filter", "label="+label, "--format", "{{.Names}}")
	}

	version("1.11.0")
	if err := run("install", "hello", "--target", "e2e"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if labelled("kyquickstart/version=1.11.0") == "" || labelled("ky.managed-by=kyquickstart") == "" {
		t.Error("installed container lacks its version or managed label")
	}
	secret := docker(t, "exec", sshd, "cat", root+"/hello/secrets/token")

	version("1.12.0")
	if err := run("upgrade", "hello"); err == nil || !strings.Contains(err.Error(), "ack_required") {
		t.Fatalf("upgrade without acknowledgement: %v", err)
	}
	if err := run("upgrade", "hello", "--acknowledge-no-backup"); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if labelled("kyquickstart/version=1.12.0") == "" {
		t.Error("upgraded container lacks its new version label")
	}

	if err := run("uninstall", "hello"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if got := docker(t, "ps", "-a", "--filter", "label=com.docker.compose.project=kyq-hello", "--format", "{{.Names}}"); got != "" {
		t.Errorf("containers left after uninstall: %s", got)
	}
	if got := docker(t, "exec", sshd, "cat", root+"/hello/secrets/token"); got != secret {
		t.Error("uninstall did not keep the app's secret")
	}
	if err := run("install", "hello", "--target", "e2e"); err != nil {
		t.Fatalf("reinstall: %v", err)
	}

	if err := run("unmanage", "hello"); err != nil {
		t.Fatalf("unmanage: %v", err)
	}
	if labelled("ky.managed-by") != "" || labelled("kyquickstart/version=1.12.0") == "" {
		t.Error("unmanaged container still managed, or lost its version")
	}
	if running := docker(t, "ps", "--filter", "label=com.docker.compose.project=kyq-hello", "--filter", "status=running", "--format", "{{.Names}}"); running == "" {
		t.Error("unmanage stopped the app")
	}
	if b, err := os.ReadFile(filepath.Join(state, "handover.md")); err != nil || !strings.Contains(string(b), "hello 1.12.0 on e2e is no longer managed") {
		t.Errorf("handover: %v\n%s", err, b)
	}
	if err := run("install", "hello", "--target", "e2e"); err == nil || !strings.Contains(err.Error(), "already_installed") {
		t.Errorf("install over an unmanaged app: %v", err)
	}
}

// status returns the engine's status word for step id in apply output.
func status(out, id string) string {
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == id {
			return strings.TrimSuffix(f[1], ":")
		}
	}
	return ""
}

func waitForSSH(t *testing.T, port, fp string) {
	t.Helper()
	kh := filepath.Join(t.TempDir(), "known_hosts")
	var p int
	fmt.Sscan(port, &p)
	var err error
	for range 50 {
		var c *remote.SSH
		c, err = remote.Dial(context.Background(), remote.SSHConfig{Name: "e2e", Host: "127.0.0.1", Port: p, User: "root", KnownHosts: kh, Trust: fp})
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("sshd not ready: %v", err)
}
