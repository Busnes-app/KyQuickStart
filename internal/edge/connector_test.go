package edge

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

func TestConnectorCredentialReplacementAndSecretFreeConfig(t *testing.T) {
	e := testEdge()
	e.Connector = &stack.Connector{Network: "npm-net", NPMHost: "npm", FRPServer: "vps.example.com", FRPPort: 7000}
	root, err := filepath.EvalSymlinks(t.TempDir()) // macOS /var is a symlink
	if err != nil {
		t.Fatal(err)
	}
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

// The tunnel never carries an internal route's name, on frp or Cloudflare.
func TestConnectorLeavesOutInternalRoutes(t *testing.T) {
	for _, provider := range []string{"frp", "cloudflare"} {
		e := testEdge()
		e.Provider = provider
		e.Connector = &stack.Connector{Network: "npm-net", NPMHost: "npm", FRPServer: "frp.example.com", FRPPort: 7000, TunnelID: "12345678-1234-1234-1234-123456789012"}
		e.Routes = append(e.Routes, stack.Route{App: "kypulse", Hostname: "pulse.lan.example.com", Exposure: "internal"})
		b, _, _ := connectorConfig(e)
		if !strings.Contains(string(b), "hello.example.com") || strings.Contains(string(b), "pulse.lan.example.com") {
			t.Errorf("%s config:\n%s", provider, b)
		}
	}
}

func directEdge() stack.Edge {
	return stack.Edge{Target: "nas", Provider: "cloudflare-direct", ConnectorCIDR: "172.30.0.3/32",
		Connector: &stack.Connector{Network: "edge-net", TunnelID: "12345678-1234-1234-1234-123456789012"},
		Routes: []stack.Route{
			{App: "kyidentity", Hostname: "id.example.com", ForwardHost: "127.0.0.1", ForwardPort: 15867, Origin: "http://kyq-kyidentity:5867"},
			{App: "kypost", Hostname: "mail.example.com", ForwardHost: "127.0.0.1", ForwardPort: 15866, Origin: "http://kyq-kypost:5866"},
		}}
}

// A direct edge routes each hostname to its own app, then 404; NPM appears nowhere.
func TestCloudflareDirectIngress(t *testing.T) {
	b, image, args := connectorConfig(directEdge())
	var conf struct {
		Tunnel      string `yaml:"tunnel"`
		Credentials string `yaml:"credentials-file"`
		Ingress     []map[string]any
	}
	if err := yaml.Unmarshal(b, &conf); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"hostname": "id.example.com", "service": "http://kyq-kyidentity:5867"},
		{"hostname": "mail.example.com", "service": "http://kyq-kypost:5866"},
		{"service": "http_status:404"},
	}
	if !reflect.DeepEqual(conf.Ingress, want) || conf.Tunnel != "12345678-1234-1234-1234-123456789012" || conf.Credentials != "/run/secrets/credential" {
		t.Fatalf("ingress = %s", b)
	}
	if strings.Contains(string(b), "noTLSVerify") || strings.Contains(string(b), "npm") || image != cloudflaredImage || args[len(args)-1] != "run" {
		t.Fatalf("unexpected connector: %s %s %v", b, image, args)
	}
	// With its last route removed (uninstall), only the 404 rule is left.
	e := directEdge()
	e.Routes = nil
	if b, _, _ := connectorConfig(e); !strings.Contains(string(b), "http_status:404") || strings.Contains(string(b), "hostname") {
		t.Fatalf("empty ingress = %s", b)
	}
}

// A direct edge reads the same target-held tunnel credential as cloudflare, never into its config.
func TestCloudflareDirectCredential(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "edge", "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	e := directEdge()
	source := filepath.Join(dir, "cloudflare_tunnel_credentials")
	if err := os.WriteFile(source, []byte(`{"AccountTag":"a","TunnelSecret":"tunnel-secret-value","TunnelID":"00000000-0000-0000-0000-000000000000"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	steps, err := DockerConnector(context.Background(), remote.Local{}, root, "test", e, &engine.Redactor{})
	if err != nil {
		t.Fatal(err)
	}
	if err := steps[0].Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "planned TunnelID") {
		t.Fatalf("credential for another tunnel: %v", err)
	}
	if err := os.WriteFile(source, []byte(`{"AccountTag":"a","TunnelSecret":"tunnel-secret-value","TunnelID":"`+e.Connector.TunnelID+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := steps[0].Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := steps[0].Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	conf, err := os.ReadFile(filepath.Join(root, connectorName(e.Target), "connector.conf"))
	if err != nil || strings.Contains(string(conf), "tunnel-secret-value") || !strings.Contains(string(conf), "kyq-kyidentity:5867") {
		t.Fatalf("config %v: %s", err, conf)
	}
}

// networkRunner runs shell commands for real and answers docker network inspect with body.
type networkRunner string

func (n networkRunner) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	if strings.HasPrefix(cmd, "docker network inspect") {
		return []byte(n), nil
	}
	return remote.Local{}.Run(ctx, cmd, stdin)
}

// An IPv6-enabled network could show the apps cloudflared at an address they do not trust.
func TestCloudflareDirectRefusesIPv6Network(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := directEdge()
	os.MkdirAll(filepath.Join(root, "edge", "secrets"), 0o700)
	if err := os.WriteFile(filepath.Join(root, "edge", "secrets", "cloudflare_tunnel_credentials"), []byte(`{"AccountTag":"a","TunnelSecret":"s","TunnelID":"`+e.Connector.TunnelID+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	network := `[{"EnableIPv6":%s,"IPAM":{"Config":[{"Subnet":"172.30.0.0/24","IPRange":"172.30.0.128/25"}]},"Containers":{}}]`
	if err := CheckConnector(context.Background(), networkRunner(fmt.Sprintf(network, "false")), root, e); err != nil {
		t.Fatal(err)
	}
	if err := CheckConnector(context.Background(), networkRunner(fmt.Sprintf(network, "true")), root, e); err == nil || !strings.Contains(err.Error(), "IPv6") {
		t.Fatalf("IPv6 network: %v", err)
	}
}
