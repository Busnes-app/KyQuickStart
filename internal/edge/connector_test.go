package edge

import (
	"context"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

func TestConnectorCredentialReplacementAndSecretFreeConfig(t *testing.T) {
	e := testEdge()
	e.Connector = &stack.Connector{Network: "npm-net", NPMHost: "npm", FRPServer: "vps.example.com", FRPPort: 7000}
	root := t.TempDir()
	dir := filepath.Join(root, "edge", "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "frp_token")
	if err := os.WriteFile(source, []byte("first-existing-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	if err := os.WriteFile(filepath.Join(dir, "frp_ca"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	steps, err := DockerConnector(context.Background(), remote.Local{}, root, "test", e, &engine.Redactor{})
	if err != nil {
		t.Fatal(err)
	}
	files := steps[0]
	if ok, err := files.Inspect(context.Background()); ok || err != nil {
		t.Fatalf("initial inspect %v %v", ok, err)
	}
	if err := files.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := files.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	conf, err := os.ReadFile(filepath.Join(root, connectorName(e.Target), "connector.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(conf), "first-existing-secret") || !strings.Contains(string(conf), "auth.tokenSource.type = \"file\"") || !strings.Contains(string(conf), "proxyProtocolVersion = \"v2\"") {
		t.Fatal("secret or incorrect auth/transport in frpc config")
	}
	hash := files.InputHash()
	marker := filepath.Join(root, connectorName(e.Target), ".input-hash")
	if err := os.WriteFile(marker, []byte("old-deployment"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("replaced-existing-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := files.Verify(context.Background()); err == nil {
		t.Fatal("missed changed credential")
	}
	if err := files.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := files.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("deployment wasn't invalidated for credential recreation")
	}
	if files.InputHash() != hash {
		t.Fatal("credential changed the workstation input hash")
	}
}

func TestCloudflareLocalIngressHasVerifiedTLSAndCatchAll(t *testing.T) {
	e := testEdge()
	e.Provider = "cloudflare"
	e.ProxyPort = 0
	e.Connector = &stack.Connector{Network: "npm-net", NPMHost: "npm", TunnelID: "12345678-1234-1234-1234-123456789012"}
	b, image, args := connectorConfig(e)
	for _, s := range []string{"https://npm:443", "originServerName: hello.example.com", "httpHostHeader: hello.example.com", "http_status:404", "credentials-file: /run/secrets/credential"} {
		if !strings.Contains(string(b), s) {
			t.Errorf("missing %s", s)
		}
	}
	if strings.Contains(string(b), "noTLSVerify") || !strings.Contains(image, "@sha256:") || len(args) == 0 {
		t.Fatal("unverified TLS or unpinned connector")
	}
}
