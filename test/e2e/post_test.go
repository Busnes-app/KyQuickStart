//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/cli"
)

func testPostIntegration(t *testing.T, ctx context.Context, state, hostFP string) {
	binary, image := os.Getenv("KYQ_TEST_POST_BINARY"), os.Getenv("KYQ_TEST_POST_IMAGE")
	if binary == "" {
		return
	}
	if !filepath.IsAbs(binary) || image == "" {
		t.Fatal("Post fixture needs an absolute binary path and explicit local fixture image")
	}
	data := t.TempDir()
	for _, dir := range []string{"config", "state", "private", "logs"} {
		if err := os.Mkdir(filepath.Join(data, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(data, "private", "client.secret"), []byte("disposable-post-oidc-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	bundle := `{"version":1,"sso":{"enabled":true,"issuerUrl":"https://identity.example.test","clientId":"post","autoProvision":false},"clientSecretFile":"/fixture/private/client.secret"}`
	if err := os.WriteFile(filepath.Join(data, "private", "bundle.json"), []byte(bundle), 0600); err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	name := "kyq-post-" + hex.EncodeToString(random)
	// The image has a root entrypoint but its protected data and server belong to kypost.
	// This disposable fixture starts only the real API, leaving mail engines unexposed.
	docker(t, "run", "-d", "--name", name, "--user", "kypost", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "-v", binary+":/usr/local/bin/kypost-server:ro", "-v", data+":/fixture", "-e", "CONFIG_DIR=/fixture/config", "-e", "STATE_DIR=/fixture/state", "-e", "SECRET_DIR=/fixture/private", "-e", "LOG_DIR=/fixture/logs", "-e", "SERVER_BASE_URL=https://post.example.test", "-e", "BOOTSTRAP_ADMIN_PASS=disposable-post-bootstrap-password", "--entrypoint", "/bin/sh", image, "-c", "/usr/local/bin/kypost-server --mode bootstrap-admin >/dev/null && exec /usr/local/bin/kypost-server --mode server")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		if exec.Command("docker", "exec", name, "python3", "-c", `import urllib.request; urllib.request.urlopen('http://127.0.0.1:5866/api/installer/network').read()`).Run() == nil {
			break
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Fatalf("disposable KyPost API did not become ready: %s", logs)
		}
		time.Sleep(250 * time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		args := []string{"integrate-product", "--product", "kypost", "--state", state, "--target", "e2e", "--container", name, "--bundle", "/fixture/private/bundle.json", "--trust-host-key", "e2e=" + hostFP}
		if i == 1 {
			args = append(args, "--check")
		}
		var out bytes.Buffer
		if err := cli.Run(ctx, args, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "e2e", Out: &out}); err != nil {
			t.Fatal(err)
		}
		var report struct {
			Version                     int
			Applied, Unchanged, Pending []string
			ConfigurationApplied        bool
		}
		if json.Unmarshal(out.Bytes(), &report) != nil || report.Version != 1 || !report.ConfigurationApplied || len(report.Pending) < 5 {
			t.Fatal("lost external acceptance gates", out.String())
		}
		if i == 0 && len(report.Applied) != 1 || i > 0 && len(report.Applied) != 0 {
			t.Fatal("non-idempotent Post setup", out.String())
		}
		if bytes.Contains(out.Bytes(), []byte("disposable-post-oidc-secret")) {
			t.Fatal("fixture secret in output")
		}
	}
	t.Log("real KyPost setup as kypost through SSH: create, read-only check, resume and retained external gates passed")
}
