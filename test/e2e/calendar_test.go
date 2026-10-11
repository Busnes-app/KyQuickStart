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

func testCalendarIntegration(t *testing.T, ctx context.Context, state, hostFP string) {
	binary := os.Getenv("KYQ_TEST_CALENDAR_BINARY")
	if binary == "" {
		return
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("KYQ_TEST_CALENDAR_BINARY must be absolute")
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
	bundle := `{"schema":"ky.product-setup/1","service":"kycalendar","setupId":"271333e2-c221-4601-b249-563b3d828530","sso":{"issuerUrl":"https://identity.example.test","clientId":"calendar","clientSecretFile":"/data/installer/client.secret","redirectUri":"https://calendar.example.test/api/sso/kysignon/callback","directorySecretFile":"/data/installer/directory.secret"},"admins":[{"issuer":"https://identity.example.test","subject":"owner-subject","username":"owner-admin"}],"everyday":[{"issuer":"https://identity.example.test","subject":"daily-subject","username":"daily"}]}`
	if err := os.WriteFile(filepath.Join(dir, "bundle.json"), []byte(bundle), 0600); err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	name := "kyq-calendar-" + hex.EncodeToString(random)
	docker(t, "run", "-d", "--name", name, "--restart", "always", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "-v", binary+":/app/kycalendar:ro", "-v", data+":/data", "-e", "KY_DATA_DIR=/data", "-e", "KY_APP_URL=https://calendar.example.test", "-e", "KY_SSO_AUTO_PROVISION=false", "-e", "KY_SCIM_ENABLED=false", "-e", "KY_ADMIN_PASSWORD=disposable-bootstrap-password", "--entrypoint", "/app/kycalendar", "kyq-e2e-sshd")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := exec.Command("docker", "exec", name, "wget", "-q", "-O-", "http://127.0.0.1:8080/api/installer/network").Output()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Fatalf("disposable KyCalendar did not become ready: %s", logs)
		}
		time.Sleep(250 * time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		args := []string{"integrate-product", "--product", "kycalendar", "--state", state, "--target", "e2e", "--container", name, "--bundle", "/data/installer/bundle.json", "--trust-host-key", "e2e=" + hostFP}
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
		if i == 0 && len(report.Created) != 3 || i > 0 && (len(report.Created) != 0 || len(report.Present) != 3) {
			t.Fatal("setup was not idempotent", out.String())
		}
		for _, secret := range []string{"disposable-oidc-secret", "disposable-directory-secret", "disposable-bootstrap-password"} {
			if strings.Contains(out.String(), secret) {
				t.Fatal("setup exposed credentials")
			}
		}
	}
	if count := docker(t, "inspect", "--format", "{{.RestartCount}}", name); count != "1" {
		t.Fatal("configuration did not activate exactly once", count)
	}
	t.Log("real KyCalendar setup through SSH: create, graceful activation, live check and resume passed")
}
