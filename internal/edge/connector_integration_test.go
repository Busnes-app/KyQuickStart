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

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
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

// TestCloudflaredDirectContainer checks a cloudflare-direct edge offline: the pinned
// cloudflared accepts the generated ingress and matches each hostname to its app's origin and
// anything else to 404; on a real reserved network the app, joined by the Compose override,
// sees exactly the connector's fixed address, and a container elsewhere on the network cannot.
// Reaching Cloudflare needs a live tunnel and is not tested.
func TestCloudflaredDirectContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		b, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	limits := []string{"--memory", "64m", "--memory-swap", "64m", "--cpus", "0.5", "--pids-limit", "64"}
	const busybox = "busybox:1.37.0-musl@sha256:dc88b80842580654294472735332ec7c7789a4010ec9a68b69f9b88c19abede6"

	name := fmt.Sprintf("direct%d", time.Now().UnixNano()%1e9)
	e := stack.Edge{Target: name, Provider: "cloudflare-direct",
		Connector: &stack.Connector{Network: "kyq-" + name + "-net", TunnelID: "12345678-1234-1234-1234-123456789012"},
		Routes: []stack.Route{
			{App: name, Hostname: "id.example.com", ForwardHost: "127.0.0.1", ForwardPort: 15867, Origin: "http://" + dockerhost.EdgeAlias(name) + ":8080"},
			{App: "kypost", Hostname: "mail.example.com", ForwardHost: "127.0.0.1", ForwardPort: 15866, Origin: "http://kyq-kypost:5866"},
		}}
	conf, image, args := connectorConfig(e)
	file := filepath.Join(t.TempDir(), "connector.conf")
	if err := os.WriteFile(file, conf, 0o644); err != nil {
		t.Fatal(err)
	}
	cloudflared := append(append([]string{"run", "--rm", "--network", "none", "--user", "65532:65532", "-v", file + ":/etc/kyq/connector.conf:ro"}, limits...), image)
	cloudflared = append(cloudflared, args[:len(args)-1]...)
	docker(append(cloudflared, "ingress", "validate")...)
	for url, service := range map[string]string{"https://id.example.com/": e.Routes[0].Origin, "https://mail.example.com/x": "http://kyq-kypost:5866", "https://other.example.com/": "http_status:404"} {
		if out := docker(append(cloudflared, "ingress", "rule", url)...); !strings.Contains(out, service) {
			t.Errorf("%s matched %s, want %s", url, out, service)
		}
	}

	createReservedNetwork(t, e.Connector.Network)
	t.Cleanup(func() { exec.Command("docker", "network", "rm", e.Connector.Network).Run() })
	var networks []struct {
		IPAM struct{ Config []struct{ Subnet string } }
	}
	if err := json.Unmarshal([]byte(docker("network", "inspect", e.Connector.Network)), &networks); err != nil || len(networks) != 1 {
		t.Fatal("invalid network response")
	}
	ip := netip.MustParsePrefix(networks[0].IPAM.Config[0].Subnet).Masked().Addr()
	for i := 0; i < 10; i++ {
		ip = ip.Next()
	}
	e.ConnectorCIDR = ip.String() + "/32"

	// A stand-in app: busybox httpd answering each request with the peer address it sees.
	compose, err := yaml.Marshal(map[string]any{"services": map[string]any{"app": map[string]any{
		"image": busybox, "read_only": true, "tmpfs": []string{"/tmp:exec"}, "cap_drop": []string{"ALL"},
		"mem_limit": "64m", "memswap_limit": "64m", "cpus": 0.5, "pids_limit": 64,
		"environment": map[string]string{"SCRIPT": "#!/bin/sh\necho Content-Type: text/plain\necho\necho $$REMOTE_ADDR\n"},
		"command":     []string{"sh", "-c", `mkdir -p /tmp/www/cgi-bin && printf %s "$$SCRIPT" > /tmp/www/cgi-bin/addr && chmod 755 /tmp/www/cgi-bin/addr && exec httpd -f -p 8080 -h /tmp/www`},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	app := dockerhost.App{Name: name, Root: root, Runner: remote.Local{}, ReleaseSet: "e2e", Redact: &engine.Redactor{}, EdgeNetwork: e.Connector.Network,
		Catalog: catalog.App{Manifest: catalog.Manifest{Name: name, Category: "ky", Images: map[string]string{"app": busybox}, Health: catalog.Health{TimeoutSeconds: 60}}, Compose: compose, Services: []string{"app"}}}
	dir := filepath.Join(root, name)
	t.Cleanup(func() {
		exec.Command("docker", "compose", "-p", "kyq-"+name, "-f", filepath.Join(dir, "compose.yaml"), "-f", filepath.Join(dir, "compose.kyq.yaml"), "down").Run()
	})
	var out bytes.Buffer
	en := &engine.Engine{Dir: filepath.Join(t.TempDir(), "state"), Out: &out, Redact: &engine.Redactor{}}
	if err := en.Run(ctx, dockerhost.Steps(app)); err != nil {
		t.Fatal(err)
	}
	// A second operation reads the override back: Deployed, then an unchanged rerun.
	if d, err := dockerhost.Deployed(ctx, app); err != nil || !d.Found || !d.Managed {
		t.Fatalf("deployed = %+v, %v", d, err)
	}
	out.Reset()
	if err := en.Run(ctx, dockerhost.Steps(app)); err != nil || strings.Count(out.String(), "skipped") != len(dockerhost.Steps(app)) {
		t.Fatalf("rerun was not a no-op: %v\n%s", err, out.String())
	}
	probe := func(args ...string) string {
		run := append(append([]string{"run", "--rm", "--network", e.Connector.Network}, limits...), args...)
		run = append(run, busybox, "wget", "-qO-", "-T", "5", "http://"+dockerhost.EdgeAlias(name)+":8080/cgi-bin/addr")
		var out string
		for i := 0; i < 50; i++ { // httpd may still be starting
			b, err := exec.CommandContext(ctx, "docker", run...).CombinedOutput()
			out = strings.TrimSpace(string(b))
			if a, perr := netip.ParseAddr(strings.Trim(out, "[]")); err == nil && perr == nil {
				return a.Unmap().String() // httpd reports [::ffff:a.b.c.d]
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("probe failed: %s", out)
		return ""
	}
	if seen := probe("--ip", ip.String()); seen != ip.String() {
		t.Fatalf("app saw %q from the connector address %s: a trusted /32 would not match", seen, ip)
	}
	if seen := probe(); seen == ip.String() || seen == "" {
		t.Fatalf("another container presented as %q", seen)
	}

	// The network must hold only installer containers.
	secrets := filepath.Join(root, "edge", "secrets")
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "cloudflare_tunnel_credentials"), []byte(`{"AccountTag":"a","TunnelSecret":"s","TunnelID":"`+e.Connector.TunnelID+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckConnector(ctx, remote.Local{}, root, e); err != nil {
		t.Fatalf("network with only the installer's app refused: %v", err)
	}
	foreign := name + "-foreign"
	docker(append(append([]string{"run", "-d", "--name", foreign, "--network", e.Connector.Network}, limits...), busybox, "sleep", "300")...)
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", foreign).Run() })
	if err := CheckConnector(ctx, remote.Local{}, root, e); err == nil || !strings.Contains(err.Error(), "only this installer's containers") {
		t.Fatalf("foreign container on the edge network: %v", err)
	}
	docker("rm", "-f", foreign)

	// unmanage keeps the network in the override and the container on it, unlabelled, so
	// preflight then refuses the edge until the operator disconnects it.
	if err := en.Run(ctx, dockerhost.UnmanageSteps(app)); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "compose.kyq.yaml")); err != nil || !strings.Contains(string(b), "name: "+e.Connector.Network) {
		t.Fatalf("unmanage dropped the edge network: %v\n%s", err, b)
	}
	if d, err := dockerhost.Deployed(ctx, app); err != nil || !d.Found || d.Managed {
		t.Fatalf("deployed after unmanage = %+v, %v", d, err)
	}
	if seen := probe("--ip", ip.String()); seen != ip.String() {
		t.Fatalf("unmanaged app saw %q", seen)
	}
	if err := CheckConnector(ctx, remote.Local{}, root, e); err == nil || !strings.Contains(err.Error(), "only this installer's containers") {
		t.Fatalf("unmanaged app on the edge network: %v", err)
	}
}
