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
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/cli"
)

func testRecoveryIntegration(t *testing.T, ctx context.Context, state, hostFP string) {
	binary := os.Getenv("KYQ_TEST_RECOVERY_BINARY")
	if binary == "" {
		return
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("KYQ_TEST_RECOVERY_BINARY must be absolute")
	}
	data := t.TempDir()
	dir := filepath.Join(data, "installer")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"client.secret": "disposable-oidc-secret", "directory.secret": "disposable-directory-secret"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bundle := `{"schema":"ky.product-setup/1","service":"kyrecovery","setupId":"271333e2-c221-4601-b249-563b3d828530","sso":{"issuerUrl":"https://identity.example.test","clientId":"recovery","clientSecretFile":"/data/installer/client.secret","redirectUri":"https://recovery.example.test/api/auth/callback","directorySecretFile":"/data/installer/directory.secret"},"admins":[{"issuer":"https://identity.example.test","subject":"owner-subject","username":"owner-admin"}]}`
	if err := os.WriteFile(filepath.Join(dir, "bundle.json"), []byte(bundle), 0600); err != nil {
		t.Fatal(err)
	}

	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	name := "kyq-recovery-" + hex.EncodeToString(random)
	image := name + "-image"
	build := exec.Command("docker", "build", "-q", "-t", image, "-")
	build.Stdin = strings.NewReader(fmt.Sprintf("FROM kyq-e2e-sshd\nRUN addgroup -g %d -S kyrecovery && adduser -u %d -S -G kyrecovery kyrecovery\n", os.Getgid(), os.Getuid()))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("runtime-user fixture build: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command("docker", "image", "rm", image).Run() })
	docker(t, "run", "-d", "--name", name, "--restart", "always", "--user", "kyrecovery", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "-v", binary+":/app/kyrecovery:ro", "-v", data+":/data", "-e", "KYRECOVERY_DATA_DIR=/data", "-e", "KY_ADMIN_INITIAL_PASSWORD=disposable-bootstrap-password", "--entrypoint", "/app/kyrecovery", image, "serve")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := exec.Command("docker", "exec", name, "wget", "-q", "-O-", "http://127.0.0.1:8095/api/installer/network").Output()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disposable KyRecovery did not become ready")
		}
		time.Sleep(250 * time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		args := []string{"integrate-product", "--product", "kyrecovery", "--state", state, "--target", "e2e", "--container", name, "--bundle", "/data/installer/bundle.json", "--trust-host-key", "e2e=" + hostFP}
		if i == 1 {
			args = append(args, "--check")
		}
		var out bytes.Buffer
		if err := cli.Run(ctx, args, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "e2e", Out: &out}); err != nil {
			t.Fatal(err)
		}
		var report struct {
			Schema                    string
			Created, Present, Pending []json.RawMessage
		}
		if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Schema != "ky.product-setup-report/1" || len(report.Pending) != 0 {
			t.Fatal("unverified setup", out.String(), err)
		}
		if i == 0 && len(report.Created) != 2 || i > 0 && (len(report.Created) != 0 || len(report.Present) != 2) {
			t.Fatal("setup was not idempotent", out.String())
		}
		for _, secret := range []string{"disposable-oidc-secret", "disposable-directory-secret", "disposable-bootstrap-password"} {
			if strings.Contains(out.String(), secret) {
				t.Fatal("setup exposed credentials")
			}
		}
	}
	if count := docker(t, "inspect", "--format", "{{.RestartCount}}", name); count != "0" {
		t.Fatal("live configuration unnecessarily restarted", count)
	}
	t.Log("real KyRecovery setup through SSH: create, live check and resume passed")
}
