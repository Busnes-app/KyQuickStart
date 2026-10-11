//go:build e2e

package edge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

func TestFRPConnectorContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		b, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s failed: %v", args[0], err)
		}
		return strings.TrimSpace(string(b))
	}
	name := fmt.Sprintf("kyq-frpc-%d", time.Now().UnixNano())
	network := name + "-net"
	createReservedNetwork(t, network)
	t.Cleanup(func() { exec.Command("docker", "network", "rm", network).Run() })
	var networks []struct {
		IPAM struct{ Config []struct{ Subnet string } }
	}
	if err := json.Unmarshal([]byte(docker("network", "inspect", network)), &networks); err != nil || len(networks) != 1 || len(networks[0].IPAM.Config) == 0 {
		t.Fatal("invalid network response")
	}
	prefix, err := netip.ParsePrefix(networks[0].IPAM.Config[0].Subnet)
	if err != nil {
		t.Fatal(err)
	}
	ip := prefix.Masked().Addr()
	for i := 0; i < 10; i++ {
		ip = ip.Next()
	}
	root := t.TempDir()
	secrets := filepath.Join(root, "edge", "secrets")
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	token := "disposable-" + name
	write := func(path string, b []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, b, mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(secrets, "frp_token"), []byte(token), 0o600)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	write(filepath.Join(secrets, "frp_ca"), certPEM, 0o600)
	server := t.TempDir()
	write(filepath.Join(server, "cert.pem"), certPEM, 0o644)
	write(filepath.Join(server, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600)
	write(filepath.Join(server, "frps.toml"), []byte("bindPort = 7000\nvhostHTTPSPort = 9443\nauth.method = \"token\"\nauth.token = \""+token+"\"\ntransport.tls.force = true\ntransport.tls.certFile = \"/test/cert.pem\"\ntransport.tls.keyFile = \"/test/key.pem\"\n"), 0o600)
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	docker("run", "-d", "--name", name, "--network", network, "-v", server+":/test:ro", "ghcr.io/fatedier/frps:v0.71.0@sha256:a650c1fde734b0213ee5f0c78a2be568e582f098bb737b03aae26e8f9035eb8d", "-c", "/test/frps.toml")
	e := testEdge()
	e.Target = name
	e.ConnectorCIDR = ip.String() + "/32"
	// Without a reserved range Docker could give the trusted address to another container.
	plain := name + "-plain"
	docker("network", "create", plain)
	t.Cleanup(func() { exec.Command("docker", "network", "rm", plain).Run() })
	var plainNet []struct {
		IPAM struct{ Config []struct{ Subnet string } }
	}
	if err := json.Unmarshal([]byte(docker("network", "inspect", plain)), &plainNet); err != nil || len(plainNet) != 1 || len(plainNet[0].IPAM.Config) == 0 {
		t.Fatal("invalid network response")
	}
	plainPrefix, err := netip.ParsePrefix(plainNet[0].IPAM.Config[0].Subnet)
	if err != nil {
		t.Fatal(err)
	}
	e.ConnectorCIDR = plainPrefix.Masked().Addr().Next().Next().String() + "/32"
	e.Connector = &stack.Connector{Network: plain, NPMHost: "npm", FRPServer: name, FRPPort: 7000}
	if err := CheckConnector(ctx, remote.Local{}, root, e); err == nil || !strings.Contains(err.Error(), "ip-range") {
		t.Fatal("connector address accepted without a reserved range", err)
	}
	e.ConnectorCIDR = ip.String() + "/32"
	e.Connector.Network = network
	if err := CheckConnector(ctx, remote.Local{}, root, e); err != nil {
		t.Fatal(err)
	}
	steps, err := DockerConnector(ctx, remote.Local{}, root, "e2e", e, &engine.Redactor{})
	if err != nil {
		t.Fatal(err)
	}
	connectorDir := filepath.Join(root, connectorName(e.Target))
	t.Cleanup(func() {
		exec.Command("docker", "compose", "-p", "kyq-"+connectorName(e.Target), "-f", filepath.Join(connectorDir, "compose.yaml"), "-f", filepath.Join(connectorDir, "compose.kyq.yaml"), "down").Run()
	})
	state := filepath.Join(t.TempDir(), "state")
	var out bytes.Buffer
	en := &engine.Engine{Dir: state, Out: &out, Redact: &engine.Redactor{}}
	if err := en.Run(ctx, steps); err != nil {
		t.Fatal(err)
	}
	id := docker("compose", "-p", "kyq-"+connectorName(e.Target), "-f", filepath.Join(connectorDir, "compose.yaml"), "-f", filepath.Join(connectorDir, "compose.kyq.yaml"), "ps", "-q")
	deadline := time.Now().Add(10 * time.Second)
	for {
		logs := docker("logs", id)
		if strings.Contains(logs, token) {
			t.Fatal("frpc logged its token")
		}
		if strings.Contains(logs, "login to server success") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("generated frpc configuration did not establish verified TLS and authenticate")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := CheckConnector(ctx, remote.Local{}, root, e); err != nil {
		t.Fatalf("own connector failed ownership check: %v", err)
	}
	out.Reset()
	if err := en.Run(ctx, steps); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "skipped") != len(steps) {
		t.Fatalf("second connector apply wasn't a no-op: %s", out.String())
	}
	b, err := os.ReadFile(filepath.Join(state, steps[0].ID()+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(token)) {
		t.Fatal("token in state")
	}
}

func TestCloudflaredIngressContainer(t *testing.T) {
	e := testEdge()
	e.Provider = "cloudflare"
	e.ProxyPort = 0
	e.Connector = &stack.Connector{Network: "npm-net", NPMHost: "npm", TunnelID: "12345678-1234-1234-1234-123456789012"}
	b, image, args := connectorConfig(e)
	file := filepath.Join(t.TempDir(), "connector.conf")
	if err := os.WriteFile(file, b, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := []string{"run", "--rm", "--network", "none", "--user", "65532:65532", "-v", file + ":/etc/kyq/connector.conf:ro", image}
	run = append(run, args[:len(args)-1]...)
	run = append(run, "ingress", "validate")
	if out, err := exec.CommandContext(ctx, "docker", run...).CombinedOutput(); err != nil {
		t.Fatalf("cloudflared rejected generated ingress: %v\n%s", err, out)
	}
}
