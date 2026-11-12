//go:build e2e

package edge

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// dirCluster is an nginx edge's cluster as one container: its ConfigMap and TLS Secret are
// host directories mounted where the pods mount them, and its one pod is that container.
type dirCluster struct {
	*fakeCluster
	t         *testing.T
	conf      string
	container string
}

func (d *dirCluster) SetEdgeFiles(ctx context.Context, files map[string]string, version string) error {
	if err := d.fakeCluster.SetEdgeFiles(ctx, files, version); err != nil {
		return err
	}
	have, _ := os.ReadDir(d.conf)
	for _, f := range have {
		if _, keep := files[f.Name()]; !keep {
			os.Remove(filepath.Join(d.conf, f.Name()))
		}
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(d.conf, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (d *dirCluster) EdgeLoaded(ctx context.Context, _ string) (map[string]string, error) {
	out, _ := exec.CommandContext(ctx, "docker", append([]string{"exec", d.container}, NginxLoaded...)...).Output()
	return map[string]string{d.container: string(out)}, nil
}

// TestNginxContainer runs the catalog's pinned nginx image as the edge's pods run it
// (unprivileged, read-only, no capabilities) with the configuration the steps render: routes
// load by reload, headers a client sends are never trusted, allow-lists hold, a rejected
// configuration keeps the last good one serving, and a renewed certificate is picked up.
func TestNginxContainer(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	app := cat[stack.NginxApp]
	image := app.Images[app.Kubernetes.Image]
	name := fmt.Sprintf("kyq-nginx-%d", time.Now().UnixNano())
	network := name + "-net"
	createReservedNetwork(t, network)
	t.Cleanup(func() { exec.Command("docker", "network", "rm", network).Run() })
	var networks []struct {
		IPAM struct{ Config []struct{ Gateway string } }
	}
	if err := json.Unmarshal([]byte(docker("network", "inspect", network)), &networks); err != nil || len(networks) == 0 || len(networks[0].IPAM.Config) == 0 {
		t.Fatalf("network inspect: %v", err)
	}
	gateway := networks[0].IPAM.Config[0].Gateway
	backend := name + "-backend"
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", backend, name).Run() })
	docker("run", "--rm", "-d", "--name", backend, "--network", network, backendImage, "--port=8080")

	// World-readable test material: the container's uid 101 is not this user.
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	conf, tlsDir := filepath.Join(dir, "conf"), filepath.Join(dir, "tls")
	for _, d := range []string{conf, tlsDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeCert := func() []byte {
		certPEM, keyPEM := testCertificate(t, time.Now().Add(24*time.Hour), "*.example.test")
		for file, b := range map[string][]byte{"tls.crt": certPEM, "tls.key": keyPEM} {
			if err := os.WriteFile(filepath.Join(tlsDir, file), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return certPEM
	}
	certPEM := writeCert()
	c := &dirCluster{fakeCluster: newFakeCluster(), t: t, conf: conf, container: name}
	c.tls["wild"] = certPEM
	if err := os.WriteFile(filepath.Join(conf, NginxMainFile), []byte(NginxMain), 0o644); err != nil {
		t.Fatal(err)
	}
	k := app.Kubernetes
	docker(append([]string{"run", "-d", "--name", name, "--network", network,
		"--user", fmt.Sprintf("%d:%d", k.RunAs, k.RunAs), "--read-only", "--tmpfs", "/tmp", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--sysctl", "net.ipv4.ip_unprivileged_port_start=0", "--memory", "256m", "--cpus", "1", "--pids-limit", "128",
		"-v", conf + ":" + NginxConfDir + ":ro", "-v", tlsDir + ":" + NginxTLSDir + "/wild:ro",
		"-p", "127.0.0.1::443", "-p", "127.0.0.1::80", "--entrypoint", NginxCommand[0], image}, NginxCommand[1:]...)...)
	https, plain := docker("port", name, "443/tcp"), docker("port", name, "80/tcp")

	// request asks host through the published TLS port; "" is a refused handshake.
	request := func(host, path string, headers ...string) (int, string) {
		t.Helper()
		client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: host}, // a throwaway certificate
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, https)
			},
		}}
		defer client.CloseIdleConnections()
		req, _ := http.NewRequestWithContext(ctx, "GET", "https://"+host+path, nil)
		for _, h := range headers {
			k, v, _ := strings.Cut(h, ": ")
			req.Header.Set(k, v)
		}
		res, err := client.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		return res.StatusCode, string(body)
	}
	header := func(page, name string) string {
		for _, line := range strings.Split(page, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), name+": "); ok {
				return v
			}
		}
		return ""
	}
	n, err := NewNginx(c, testID, &engine.Redactor{})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	state := filepath.Join(dir, "state")
	run := func(e stack.Edge) (string, error) {
		var out bytes.Buffer
		err := (&engine.Engine{Dir: state, Out: &out, Redact: &engine.Redactor{}}).Run(ctx, n.Steps(e))
		return out.String(), err
	}
	master := func() string { return docker("exec", name, "cat", nginxRunDir+"/nginx.pid") }

	// lan: this host reaches the proxy from the network's gateway, the one allowed client.
	e := stack.Edge{Target: "k1", Provider: "lan", Proxy: "nginx", AllowCIDRs: []string{gateway + "/32"}, ClientPort: 8443, Routes: []stack.Route{
		{App: "hello", Hostname: "one.example.test", ForwardHost: backend, ForwardPort: 8080, ForwardScheme: "http", CertificateSecret: "wild"},
		{App: "hello2", Hostname: "two.example.test", ForwardHost: backend, ForwardPort: 8080, ForwardScheme: "http", CertificateSecret: "wild"},
	}}
	if out, err := run(e); err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, docker("logs", name))
	}
	pid := master()
	code, page := request("one.example.test", "/x?y=1", "X-Forwarded-For: 198.51.100.9", "X-Real-IP: 198.51.100.10", "Forwarded: for=198.51.100.11", "CF-Connecting-IP: 198.51.100.12", "True-Client-IP: 198.51.100.13", "X-Forwarded-Proto: http")
	if code != 200 || header(page, "X-Forwarded-For") != gateway || header(page, "X-Real-Ip") != gateway || header(page, "X-Forwarded-Proto") != "https" ||
		header(page, "X-Forwarded-Port") != "8443" || header(page, "X-Forwarded-Host") != "one.example.test:8443" || strings.Contains(page, "198.51.100") || !strings.Contains(page, "GET /x?y=1 ") {
		t.Fatalf("lan request: %d\n%s", code, page)
	}
	// An unknown name gets no handshake, and a routed name asked for under another is refused.
	if code, why := request("unknown.example.test", "/"); code != 0 {
		t.Fatalf("unknown TLS name answered %d: %s", code, why)
	}
	// Port 80 serves nothing: it redirects, and answers the readiness probe.
	for path, want := range map[string]int{"/": 301, NginxReadyPath: 204} {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+plain+path, nil)
		req.Host = "one.example.test"
		res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil || res.StatusCode != want || want == 301 && res.Header.Get("Location") != "https://one.example.test/" {
			t.Fatalf("port 80 %s: %v %v", path, res, err)
		}
		res.Body.Close()
	}
	// Rerun: every step is skipped, and nginx was reloaded, never restarted.
	out, err := run(e)
	if err != nil || strings.Contains(out, " ok") {
		t.Fatalf("rerun: %v\n%s", err, out)
	}

	// Outside allow_cidrs, with and without a forged allowed address: this host is no longer allowed.
	e.AllowCIDRs = []string{"192.0.2.0/24"}
	if out, err := run(e); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, forged := range []string{"X-Test: none", "X-Real-IP: 192.0.2.7", "X-Forwarded-For: 192.0.2.7", "CF-Connecting-IP: 192.0.2.7"} {
		if code, page := request("one.example.test", "/", forged); code != 403 {
			t.Fatalf("served a client outside allow_cidrs (%s): %d\n%s", forged, code, page)
		}
	}

	// cloudflare: only the connector (this host) is believed, and only through its header.
	tunnel := stack.Edge{Target: "k1", Provider: "cloudflare", Proxy: "nginx", ConnectorCIDR: gateway + "/32", Routes: e.Routes[:1]}
	if out, err := run(tunnel); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	code, page = request("one.example.test", "/", "CF-Connecting-IP: 203.0.113.7", "X-Forwarded-For: 198.51.100.9", "X-Real-IP: 198.51.100.10")
	if code != 200 || header(page, "X-Forwarded-For") != "203.0.113.7" || header(page, "X-Real-Ip") != "203.0.113.7" || strings.Contains(page, "198.51.100") || header(page, "Cf-Connecting-Ip") != "" {
		t.Fatalf("connector request: %d\n%s", code, page)
	}
	tunnel.ConnectorCIDR = "192.0.2.7/32" // this host is no longer the connector
	if out, err := run(tunnel); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if code, page := request("one.example.test", "/", "CF-Connecting-IP: 192.0.2.7"); code != 403 {
		t.Fatalf("a source other than connector_cidr was served: %d\n%s", code, page)
	}
	// The removed route is disabled: its file stays, its name no longer answers.
	gone := routeKey("two.example.test")
	if _, err := os.Stat(filepath.Join(conf, gone+disabledExt)); err != nil {
		t.Fatalf("removed route's file: %v", err)
	}
	if code, _ := request("two.example.test", "/"); code != 0 {
		t.Fatalf("disabled route answered %d", code)
	}
	if master() != pid {
		t.Fatal("nginx was restarted, not reloaded")
	}

	// Catalog paths: only the listed path is proxied, a denied path answers 404.
	paths := tunnel
	paths.ConnectorCIDR = gateway + "/32"
	paths.Routes = []stack.Route{{App: "hello", Hostname: "one.example.test", ForwardHost: backend, ForwardPort: 8080, ForwardScheme: "http", CertificateSecret: "wild", DenyPaths: []string{"^/admin"}}}
	if out, err := run(paths); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if code, _ := request("one.example.test", "/admin/users"); code != 404 {
		t.Fatalf("denied path answered %d", code)
	}
	if code, _ := request("one.example.test", "/ok"); code != 200 {
		t.Fatalf("allowed path answered %d", code)
	}

	// A file nginx rejects is never loaded: the last good configuration keeps serving, the
	// route's step fails with nginx's reason and disables the route again.
	good := maps.Clone(c.files)
	bad := paths
	bad.Routes = append([]stack.Route{}, paths.Routes...)
	bad.Routes = append(bad.Routes, stack.Route{App: "hello3", Hostname: "three.example.test", ForwardHost: backend, ForwardPort: 8080, ForwardScheme: "http", CertificateSecret: "absent-secret"})
	c.tls["absent-secret"] = certPEM // the Secret passes the check; no pod mounts it
	nginxWait = 20 * time.Second
	defer func() { nginxWait = 3 * time.Minute }()
	out, err = run(bad)
	if err == nil || !strings.Contains(err.Error(), "rejected the configuration") || !strings.Contains(err.Error(), "absent-secret/tls.crt") {
		t.Fatalf("rejected configuration: %v\n%s", err, out)
	}
	if !maps.Equal(maps.Collect(enabled(c.files)), maps.Collect(enabled(good))) {
		t.Fatalf("rejected route left enabled: %v", c.files)
	}
	if code, _ := request("one.example.test", "/ok"); code != 200 || master() != pid {
		t.Fatalf("the good route stopped serving: %d", code)
	}
	if out, err := run(paths); err != nil {
		t.Fatalf("recovery: %v\n%s", err, out)
	}

	// A renewed certificate is served without a restart.
	serial := func() string {
		conn, err := tls.Dial("tcp", https, &tls.Config{InsecureSkipVerify: true, ServerName: "one.example.test"})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0].SerialNumber.String()
	}
	before := serial()
	writeCert()
	for deadline := time.Now().Add(30 * time.Second); serial() == before; time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatalf("renewed certificate not served\n%s", docker("logs", name))
		}
	}
	if master() != pid {
		t.Fatal("certificate renewal restarted nginx")
	}

	// Stopping lets open requests finish: the container exits cleanly within its grace period.
	docker("stop", "-t", "20", name)
	if code := docker("inspect", "--format", "{{.State.ExitCode}}", name); code != "0" {
		t.Fatalf("nginx exited %s on stop\n%s", code, docker("logs", name))
	}
}

// enabled yields the files nginx includes.
func enabled(files map[string]string) func(func(string, string) bool) {
	return func(yield func(string, string) bool) {
		for k, v := range files {
			if strings.HasSuffix(k, enabledExt) && !yield(k, v) {
				return
			}
		}
	}
}
