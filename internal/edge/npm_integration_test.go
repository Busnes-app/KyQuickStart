//go:build e2e

package edge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/netip"
	"net/url"
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

const npmImage = "jc21/nginx-proxy-manager:2.16.0@sha256:57d85bffc776c01909c7b24e127a69a72b7d8c75d387e6cb26c762978fc43e4c"
const backendImage = "traefik/whoami:v1.11.0@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab"

// TestNPMContainer exercises the actual upstream API and generated nginx configuration.
// Every published port is loopback-only, and every resource has a per-test name.
func TestNPMContainer(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s failed: %v", args[0], err)
		}
		return strings.TrimSpace(string(out))
	}
	name := fmt.Sprintf("kyq-edge-%d", time.Now().UnixNano())
	network := name + "-net"
	createReservedNetwork(t, network)
	t.Cleanup(func() { exec.Command("docker", "network", "rm", network).Run() })
	var networks []struct {
		IPAM struct {
			Config []struct{ Gateway, Subnet string }
		}
	}
	if err := json.Unmarshal([]byte(docker("network", "inspect", network)), &networks); err != nil || len(networks) == 0 || len(networks[0].IPAM.Config) == 0 {
		t.Fatalf("network inspect: %v", err)
	}
	gateway := networks[0].IPAM.Config[0].Gateway
	if gateway == "" {
		t.Fatal("network inspect reported no gateway")
	}
	backend := name + "-backend"
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", backend).Run() })
	docker("run", "--rm", "-d", "--name", backend, "--network", network, backendImage, "--port=8080")
	dir := t.TempDir()
	password := "test-only-" + name
	env := filepath.Join(dir, "npm.env")
	if err := os.WriteFile(env, []byte("INITIAL_ADMIN_EMAIL=admin@example.test\nINITIAL_ADMIN_PASSWORD="+password+"\nDISABLE_IPV6=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dataVolume, certVolume := name+"-data", name+"-certs"
	t.Cleanup(func() { exec.Command("docker", "volume", "rm", dataVolume, certVolume).Run() })
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	docker("run", "-d", "--name", name, "--network", network, "--env-file", env,
		"--mount", "type=volume,source="+dataVolume+",target=/data",
		"--mount", "type=volume,source="+certVolume+",target=/etc/letsencrypt",
		"-p", "127.0.0.1::81", "-p", "127.0.0.1::8443", "-p", "127.0.0.1::443", npmImage)
	address := func(port string) string { return docker("port", name, port+"/tcp") }
	apiURL := "http://" + address("81")
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		if docker("inspect", "--format", "{{.State.Running}}", name) != "true" {
			t.Fatal("NPM exited before its API became ready")
		}
		res, err := client.Get(apiURL + "/api/")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("NPM startup timed out")
		case <-time.After(time.Second):
		}
	}
	token := ""
	request := func(method, path string, body io.Reader, contentType string, result any) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, apiURL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", contentType)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("NPM API request: %v", err)
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			t.Fatalf("NPM API %s %s: HTTP %d", method, path, res.StatusCode)
		}
		if result != nil {
			if err := json.NewDecoder(res.Body).Decode(result); err != nil {
				t.Fatal("invalid NPM response")
			}
		}
	}
	login, _ := json.Marshal(map[string]string{"identity": "admin@example.test", "secret": password})
	var auth struct {
		Token string `json:"token"`
	}
	request("POST", "/api/tokens", bytes.NewReader(login), "application/json", &auth)
	token = auth.Token
	if token == "" {
		t.Fatal("NPM returned no token")
	}
	// A throwaway certificate is uploaded through the API, never by writing its database.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "*.example.test"}, DNSNames: []string{"*.example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	var certificate struct {
		ID int `json:"id"`
	}
	request("POST", "/api/nginx/certificates", strings.NewReader(`{"provider":"other","nice_name":"disposable edge test"}`), "application/json", &certificate)
	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	for field, data := range map[string][]byte{"certificate": certPEM, "certificate_key": keyPEM} {
		part, err := form.CreateFormFile(field, field+".pem")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	request("POST", fmt.Sprintf("/api/nginx/certificates/%d/upload", certificate.ID), &upload, form.FormDataContentType(), nil)
	// HTTPS shim exists only for the test's loopback API. Production still requires verified LAN HTTPS.
	upstream, err := url.Parse(apiURL)
	if err != nil {
		t.Fatal(err)
	}
	shim := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(upstream))
	defer shim.Close()
	n, err := NewNPM(shim.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	n.http.Transport.(*http.Transport).TLSClientConfig = shim.Client().Transport.(*http.Transport).TLSClientConfig
	defer n.Close()
	e := stack.Edge{Target: name, Provider: "frp", NPMURL: shim.URL, ConnectorCIDR: gateway + "/32", ProxyPort: 8443, Routes: []stack.Route{
		{App: "hello", Hostname: "one.example.test", ForwardHost: backend, ForwardPort: 8080, ForwardScheme: "http", CertificateID: certificate.ID},
		{App: "hello", Hostname: "two.example.test", ForwardHost: backend, ForwardPort: 8080, ForwardScheme: "http", CertificateID: certificate.ID},
	}}
	if err := n.Preflight(ctx, e); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	var out bytes.Buffer
	en := &engine.Engine{Dir: state, Out: &out, Redact: &engine.Redactor{}}
	if err := en.Run(ctx, n.Steps(e)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := en.Run(ctx, n.Steps(e)); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "skipped") != 3 {
		t.Fatal("second apply was not a no-op")
	}
	probe := func(address, sni, host string, proxy bool) (int, string) {
		t.Helper()
		var conn *tls.Conn
		deadline := time.Now().Add(10 * time.Second)
		for {
			raw, err := net.DialTimeout("tcp", address, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := raw.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if proxy {
				// PROXY v2, IPv4 TCP, source 198.51.100.23. nginx learns it only from the pinned peer.
				preamble := append([]byte("\r\n\r\n\x00\r\nQUIT\n"), 0x21, 0x11, 0, 12)
				preamble = append(preamble, net.ParseIP("198.51.100.23").To4()...)
				preamble = append(preamble, net.ParseIP("192.0.2.1").To4()...)
				preamble = binary.BigEndian.AppendUint16(preamble, 45678)
				preamble = binary.BigEndian.AppendUint16(preamble, 443)
				if _, err := raw.Write(preamble); err != nil {
					t.Fatal(err)
				}
			}
			conn = tls.Client(raw, &tls.Config{ServerName: sni, InsecureSkipVerify: true}) // disposable self-signed fixture only
			err = conn.Handshake()
			if err == nil {
				break
			}
			raw.Close()
			if time.Now().After(deadline) {
				t.Fatal(err)
			}
			// nginx -s reload returns before the new workers accept connections.
			time.Sleep(100 * time.Millisecond)
		}
		defer conn.Close()
		if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nX-Forwarded-For: 203.0.113.99\r\nX-Real-IP: 203.0.113.99\r\nX-Forwarded-Proto: http\r\nCF-Connecting-IP: 198.51.100.23\r\nForwarded: for=203.0.113.99\r\nConnection: close\r\n\r\n", host); err != nil {
			t.Fatal(err)
		}
		res, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, string(b)
	}
	expectedClientIP := "198.51.100.23"
	checkRoute := func(endpoint, hostname string, proxy bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			code, body := probe(endpoint, hostname, hostname, proxy)
			if code == 200 {
				if !strings.Contains(body, "X-Forwarded-For: "+expectedClientIP) || !strings.Contains(body, "X-Forwarded-Proto: https") || strings.Contains(body, "203.0.113.99") || strings.Contains(body, "\nForwarded:") {
					t.Fatalf("%s forwarding-header proof failed", hostname)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s route failed after nginx reload: HTTP %d", hostname, code)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	for _, route := range e.Routes {
		checkRoute(address("8443"), route.Hostname, true)
	}
	if code, _ := probe(address("8443"), "unknown.example.test", e.Routes[0].Hostname, true); code != 421 {
		t.Fatalf("unknown SNI accepted: %d", code)
	}
	// A route removed from stack.yaml is disabled, not deleted, and stops serving.
	removed := e.Routes[1]
	e.Routes = e.Routes[:1]
	if err := en.Run(ctx, n.Steps(e)); err != nil {
		t.Fatal(err)
	}
	removedID := 0
	var hosts []host
	request("GET", hostPath, nil, "application/json", &hosts)
	for _, h := range hosts {
		if len(h.Domains) == 1 && h.Domains[0] == removed.Hostname {
			if h.Enabled {
				t.Fatal("removed route is still enabled")
			}
			removedID = h.ID
		}
	}
	if removedID == 0 {
		t.Fatal("removed route was deleted")
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		code, _ := probe(address("8443"), removed.Hostname, removed.Hostname, true)
		if code == 421 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("removed route still served: HTTP %d", code)
		}
	}
	// verify-edge fails while an owned, unconfigured host is enabled; apply disables it again.
	request("POST", fmt.Sprintf("%s/%d/enable", hostPath, removedID), nil, "application/json", nil)
	if err := n.Steps(e)[0].Verify(ctx); err == nil {
		t.Fatal("verify accepted an enabled removed route")
	}
	if err := en.Run(ctx, n.Steps(e)); err != nil {
		t.Fatal(err)
	}
	if err := n.Steps(e)[0].Verify(ctx); err != nil {
		t.Fatal(err)
	}
	// Restoring the route re-enables the same host.
	e.Routes = append(e.Routes, removed)
	if err := en.Run(ctx, n.Steps(e)); err != nil {
		t.Fatal(err)
	}
	checkRoute(address("8443"), removed.Hostname, true)
	// Repeat with actual frps and the installer's generated frpc deployment. The
	// workstation sends ordinary TLS; frpc must supply PROXY v2 to the real NPM.
	frps := name + "-frps"
	serverDir := t.TempDir()
	frpToken := "disposable-" + frps
	for filename, data := range map[string][]byte{
		"cert.pem": certPEM, "key.pem": keyPEM,
		"frps.toml": []byte("bindPort = 7000\nvhostHTTPSPort = 9443\nauth.method = \"token\"\nauth.token = \"" + frpToken + "\"\ntransport.tls.force = true\ntransport.tls.certFile = \"/test/cert.pem\"\ntransport.tls.keyFile = \"/test/key.pem\"\n"),
	} {
		if err := os.WriteFile(filepath.Join(serverDir, filename), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", frps).Run() })
	docker("run", "-d", "--name", frps, "--network", network, "--network-alias", "frp.example.test",
		"-p", "127.0.0.1::9443", "-v", serverDir+":/test:ro",
		"ghcr.io/fatedier/frps:v0.71.0@sha256:a650c1fde734b0213ee5f0c78a2be568e582f098bb737b03aae26e8f9035eb8d", "-c", "/test/frps.toml")
	prefix, err := netip.ParsePrefix(networks[0].IPAM.Config[0].Subnet)
	if err != nil {
		t.Fatal(err)
	}
	ip := prefix.Masked().Addr()
	for range 10 {
		ip = ip.Next()
	}
	root := t.TempDir()
	secrets := filepath.Join(root, "edge", "secrets")
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	for filename, data := range map[string][]byte{"frp_token": []byte(frpToken), "frp_ca": certPEM} {
		if err := os.WriteFile(filepath.Join(secrets, filename), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.ConnectorCIDR = ip.String() + "/32"
	e.Connector = &stack.Connector{Network: network, NPMHost: name, FRPServer: "frp.example.test", FRPPort: 7000}
	if err := en.Run(ctx, n.Steps(e)); err != nil {
		t.Fatal(err)
	}
	if err := CheckConnector(ctx, remote.Local{}, root, e); err != nil {
		t.Fatal(err)
	}
	connectorSteps, err := DockerConnector(ctx, remote.Local{}, root, "e2e", e, en.Redact)
	if err != nil {
		t.Fatal(err)
	}
	connectorDir := filepath.Join(root, connectorName(e.Target))
	t.Cleanup(func() {
		exec.Command("docker", "compose", "-p", "kyq-"+connectorName(e.Target), "-f", filepath.Join(connectorDir, "compose.yaml"), "-f", filepath.Join(connectorDir, "compose.kyq.yaml"), "down").Run()
	})
	if err := en.Run(ctx, connectorSteps); err != nil {
		t.Fatal(err)
	}
	expectedClientIP = gateway
	publicEndpoint := docker("port", frps, "9443/tcp")
	for _, route := range e.Routes {
		checkRoute(publicEndpoint, route.Hostname, false)
	}
	// Change only the owned hosts to Cloudflare and prove ordinary TLS plus sanitized headers.
	e.Provider, e.ProxyPort = "cloudflare", 0
	e.Connector, e.ConnectorCIDR = nil, gateway+"/32"
	expectedClientIP = "198.51.100.23"
	if err := en.Run(ctx, n.Steps(e)); err != nil {
		t.Fatal(err)
	}
	for _, route := range e.Routes {
		checkRoute(address("443"), route.Hostname, false)
	}
	if err := filepath.WalkDir(state, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(b, []byte(token)) || bytes.Contains(b, []byte(password)) || bytes.Contains(b, []byte(frpToken)) {
			t.Error("credential in state")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
