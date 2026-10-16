//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

func testPulseIntegration(t *testing.T, ctx context.Context, state, hostFP string) {
	binary := os.Getenv("KYQ_TEST_PULSE_BINARY")
	if binary == "" {
		return
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("KYQ_TEST_PULSE_BINARY must be absolute")
	}
	data := t.TempDir()
	dir := filepath.Join(data, "installer")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	sourceToken := strings.Repeat("ab", 32)
	for name, value := range map[string]string{"client.secret": "disposable-oidc-secret", "directory.secret": "disposable-directory-secret", "pulse-source.secret": sourceToken} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bundle := `{"schema":"ky.product-setup/1","service":"kypulse","setupId":"271333e2-c221-4601-b249-563b3d828530","sso":{"issuerUrl":"https://identity.example.test","clientId":"pulse","clientSecretFile":"/data/installer/client.secret","redirectUri":"https://pulse.example.test/api/sso/kysignon/callback","directorySecretFile":"/data/installer/directory.secret"},"admins":[{"issuer":"https://identity.example.test","subject":"owner-subject","username":"owner-admin"}],"everyday":[{"issuer":"https://identity.example.test","subject":"daily-subject","username":"daily"}],"monitoring":{"targets":[{"key":"identity","name":"KyIdentity","url":"https://identity.example.test/healthz"}],"sources":[{"key":"offboard","name":"offboard","target":"identity","tokenFile":"/data/installer/pulse-source.secret","offboardingAlerts":true}]}}`
	if err := os.WriteFile(filepath.Join(dir, "bundle.json"), []byte(bundle), 0600); err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	name := "kyq-pulse-" + hex.EncodeToString(random)
	// Saved intent drives the same native adapter; unrelated inventory is untouched.
	saved, err := stack.Load(filepath.Join(state, "stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	saved.Existing = append(saved.Existing, stack.Existing{Product: "kypulse", Target: "e2e", URL: "https://pulse.example.test"})
	saved.Integrations = []stack.Integration{{Product: "kypulse", Target: "e2e", Container: name, Bundle: "/data/installer/bundle.json"}}
	integrationState := t.TempDir()
	encoded, err := yaml.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(integrationState, "stack.yaml"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	docker(t, "run", "-d", "--name", name, "--restart", "always", "-p", "127.0.0.1::8080", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "-v", binary+":/app/kypulse:ro", "-v", data+":/data", "-e", "KYPULSE_ENV=production", "-e", "KYPULSE_HOST=0.0.0.0", "-e", "KYPULSE_DATA_DIR=/data", "-e", "KYPULSE_APP_URL=https://pulse.example.test", "-e", "KYPULSE_SSO_AUTO_PROVISION=false", "-e", "KYPULSE_ADMIN_PASSWORD=disposable-bootstrap-password", "--entrypoint", "/app/kypulse", "kyq-e2e-sshd")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := exec.Command("docker", "exec", name, "wget", "-q", "-O-", "http://127.0.0.1:8080/healthz").Output()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disposable kyPulse did not become ready")
		}
		time.Sleep(250 * time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		if i == 0 {
			failurePlan := *saved
			failurePlan.Existing = append(append([]stack.Existing(nil), saved.Existing...), stack.Existing{Product: "kydns", Target: "e2e", URL: "https://dns.example.test"})
			failurePlan.Integrations = append(append([]stack.Integration(nil), saved.Integrations...), stack.Integration{Product: "kydns", Target: "e2e", Container: name + "-absent", Bundle: "/data/installer/bundle.json"})
			failureBytes, err := yaml.Marshal(&failurePlan)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(integrationState, "stack.yaml"), failureBytes, 0600); err != nil {
				t.Fatal(err)
			}
		}
		args := []string{"integrate-stack", "--state", integrationState, "--trust-host-key", "e2e=" + hostFP}
		if i == 1 {
			args = append(args, "--check")
		}
		var out bytes.Buffer
		err := cli.Run(ctx, args, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "e2e", Out: &out})
		if i == 0 {
			if err == nil || !strings.Contains(err.Error(), "integration kydns failed") {
				t.Fatal("later failure was not reported", err)
			}
			// Removing failed fixture intent neither reverses nor resets successful setup.
			if err := os.WriteFile(filepath.Join(integrationState, "stack.yaml"), encoded, 0600); err != nil {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		var report struct {
			Schema                    string
			Created, Present, Pending []json.RawMessage
		}
		var envelope struct {
			Product, Target string
			Report          json.RawMessage
		}
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil || envelope.Product != "kypulse" || envelope.Target != "e2e" {
			t.Fatal("invalid saved integration receipt", out.String(), err)
		}
		if err := json.Unmarshal(envelope.Report, &report); err != nil || report.Schema != "ky.product-setup-report/1" || len(report.Pending) != 0 {
			t.Fatal("unverified setup", out.String(), err)
		}
		if i == 0 && len(report.Created) != 5 || i > 0 && (len(report.Created) != 0 || len(report.Present) != 5) {
			t.Fatal("setup was not idempotent", out.String())
		}
		for _, secret := range []string{"disposable-oidc-secret", "disposable-directory-secret", "disposable-bootstrap-password", sourceToken} {
			if strings.Contains(out.String(), secret) {
				t.Fatal("setup exposed credentials")
			}
		}
	}
	if count := docker(t, "inspect", "--format", "{{.RestartCount}}", name); count != "1" {
		t.Fatal("configuration did not activate exactly once", count)
	}
	address := docker(t, "port", name, "8080/tcp")
	client := &http.Client{Timeout: 5 * time.Second}
	request, err := http.NewRequestWithContext(ctx, "OPTIONS", "http://"+address+"/api/ingest/logs", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+sourceToken)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("native source capability unavailable", err)
	}
	response.Body.Close()
	if response.StatusCode != 204 || response.Header.Get("Ky-Log-Receipt") != "ky.log-ingest/1" || response.Header.Get("Ky-Log-Alerts") != "ky.offboarding-alerts/1" || response.Header.Get("Ky-Log-Source") == "" {
		t.Fatal("installer source did not retain native alert authority")
	}
	t.Log("saved integration plan through real SSH/kyPulse: accounts, monitoring enrollment, graceful activation, later-adapter failure, live check, resume and native alert capability passed")
}
