package immich

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/cli"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func qualifyNativeInstaller(t *testing.T, ctx context.Context, container, origin, secret string, subjects map[string]string) {
	t.Helper()
	image := os.Getenv("KYQ_TEST_IMMICH_SSH_IMAGE")
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(image) {
		t.Fatal("installer fixture needs an immutable local SSH fixture image")
	}
	directory, err := os.MkdirTemp(os.TempDir(), "kyq-immich-ssh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if os.RemoveAll(directory) != nil {
			t.Error("private installer fixture cleanup failed")
		}
	})
	keys, state := filepath.Join(directory, "keys"), filepath.Join(directory, "state")
	for _, path := range []string{keys, state} {
		if os.Mkdir(path, 0700) != nil {
			t.Fatal("private installer directories unavailable")
		}
	}
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	hostBlock, err := ssh.MarshalPrivateKey(hostKey, "")
	if err != nil {
		t.Fatal(err)
	}
	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"host_ed25519": pem.EncodeToMemory(hostBlock), "authorized_keys": ssh.MarshalAuthorizedKey(clientSigner.PublicKey())} {
		if os.WriteFile(filepath.Join(keys, name), data, 0600) != nil {
			t.Fatal("private SSH keys unavailable")
		}
	}
	keyring := agent.NewKeyring()
	if keyring.Add(agent.AddedKey{PrivateKey: clientKey}) != nil {
		t.Fatal("private SSH agent key unavailable")
	}
	socket := filepath.Join(directory, "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal("private SSH agent listener failed")
	}
	t.Setenv("SSH_AUTH_SOCK", socket)
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	var workers sync.WaitGroup
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections[conn] = true
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				agent.ServeAgent(keyring, conn)
				mu.Lock()
				delete(connections, conn)
				mu.Unlock()
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-accepted
		mu.Lock()
		for conn := range connections {
			conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	name := container + "-ssh"
	owner := strings.TrimSuffix(container, "-server")
	command := exec.CommandContext(ctx, "docker", "run", "-d", "--name", name,
		"--label", "kyq.fixture=immich", "--label", "kyq.fixture-owner="+owner,
		"--memory", "256m", "--memory-swap", "256m", "--cpus", "1", "-p", "127.0.0.1::22",
		"-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", keys+":/keys:ro", image)
	if command.Run() != nil {
		t.Fatal("owned SSH fixture start failed")
	}
	t.Cleanup(func() {
		if exec.Command("docker", "rm", "-fv", name).Run() != nil {
			t.Error("owned SSH fixture cleanup failed")
		}
	})
	portRaw, err := exec.CommandContext(ctx, "docker", "port", name, "22").Output()
	host, portText, splitErr := net.SplitHostPort(strings.TrimSpace(string(portRaw)))
	port, numberErr := strconv.Atoi(portText)
	if err != nil || splitErr != nil || numberErr != nil || host != "127.0.0.1" || port < 1 || port > 65535 {
		t.Fatal("owned SSH fixture is not loopback")
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, portText), time.Second)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned SSH fixture did not become ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
	plan := "version: 1\ntargets:\n- name: fixture\n  host: 127.0.0.1\n  user: root\n  port: " + strconv.Itoa(port) + "\n  root: /opt/kyquickstart\nexisting:\n- product: immich\n  target: fixture\n  url: " + origin + "\nintegrations:\n- product: immich\n  target: fixture\n  container: " + container + "\n  bundle: /usr/src/app/kyquickstart/setup.json\n"
	if os.WriteFile(filepath.Join(state, "stack.yaml"), []byte(plan), 0600) != nil {
		t.Fatal("saved installer intent unavailable")
	}
	first := []string{"integrate-stack", "--state", state, "--trust-host-key", "fixture=" + ssh.FingerprintSHA256(hostSigner.PublicKey())}
	var rejected bytes.Buffer
	if err := cli.Run(ctx, append(append([]string(nil), first...), "--check"), cli.Options{Catalog: catalog.Embedded(), Out: &rejected}); err == nil || rejected.Len() != 0 {
		t.Fatal("unconfigured native setup inspection reported ready")
	}
	// The target lock must refuse the mutation before native resources are created.
	locked := exec.CommandContext(ctx, "docker", "exec", name, "sh", "-c", `umask 077; mkdir -p /opt/kyquickstart/.lock; printf '%s' '{"holder":"owned competing fixture","run":"fixture","started":"2026-10-10T00:00:00Z"}' > /opt/kyquickstart/.lock/holder`)
	if locked.Run() != nil {
		t.Fatal("owned contended-lock fixture failed")
	}
	rejected.Reset()
	if err := cli.Run(ctx, first, cli.Options{Catalog: catalog.Embedded(), Out: &rejected}); err == nil || !strings.Contains(err.Error(), "locked") || rejected.Len() != 0 {
		t.Fatal("held target lock did not refuse native setup")
	}
	if exec.CommandContext(ctx, "docker", "exec", name, "sh", "-c", "rm /opt/kyquickstart/.lock/holder && rmdir /opt/kyquickstart/.lock").Run() != nil {
		t.Fatal("owned contended-lock cleanup failed")
	}
	for run := 0; run < 3; run++ {
		args := []string{"integrate-stack", "--state", state}
		if run == 0 {
			args = append(args, "--trust-host-key", "fixture="+ssh.FingerprintSHA256(hostSigner.PublicKey()))
		}
		if run == 1 {
			args = append(args, "--check")
		}
		var output bytes.Buffer
		if err := cli.Run(ctx, args, cli.Options{Catalog: catalog.Embedded(), Out: &output}); err != nil {
			t.Fatal("native installer SSH setup failed", err)
		}
		var report struct {
			Product, Target string
			Report          struct {
				Schema, Service, SetupID  string
				Created, Present, Pending []struct{ Kind, Name, ID string }
			}
		}
		if strings.Contains(output.String(), secret) || strings.Contains(output.String(), "owned-owner") ||
			json.Unmarshal(output.Bytes(), &report) != nil || report.Product != "immich" || report.Target != "fixture" ||
			report.Report.Schema != "ky.product-setup-report/1" || report.Report.Service != "immich" ||
			report.Report.SetupID != "271333e2-c221-4601-b249-563b3d828530" || len(report.Report.Pending) != 0 {
			t.Fatal("native installer emitted an unsafe or incomplete report")
		}
		for _, subject := range subjects {
			if strings.Contains(output.String(), subject) {
				t.Fatal("native installer rendered an identity-provider subject")
			}
		}
		if run == 0 && len(report.Report.Created) != 3 || run != 0 && (len(report.Report.Created) != 0 || len(report.Report.Present) != 3) {
			t.Fatal("native installer creation/check/resume changed resource counts")
		}
		if exec.CommandContext(ctx, "docker", "exec", name, "test", "!", "-e", "/opt/kyquickstart/.lock").Run() != nil {
			t.Fatal("native installer left its target lock")
		}
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(state, entry.Name()))
		if err != nil || strings.Contains(string(data), secret) || strings.Contains(string(data), "clientSecret") {
			t.Fatal("workstation state contains native credentials")
		}
		for _, subject := range subjects {
			if strings.Contains(string(data), subject) {
				t.Fatal("workstation state contains an identity-provider subject")
			}
		}
	}
	t.Log("actual integrate-stack SSH/Docker creation, pinned host key, read-only verification, target lock release and resume verified; workstation state has no native credentials")
}
