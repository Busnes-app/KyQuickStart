//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"go.yaml.in/yaml/v3"
)

// trustNet is a test network whose fixed addresses Docker never assigns (outside --ip-range):
// edgeIP stands for the edge, otherIP for any other peer.
const (
	trustNet = "kyq-e2e-trust"
	edgeIP   = "172.31.254.2"
	otherIP  = "172.31.254.3"
)

// Real pinned images, private owned binds, actual Compose and native capabilities.
// Runs serially and refuses an existing project instead of adopting an installation.
func TestDockerKyCatalog(t *testing.T) {
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	if docker(t, "network", "ls", "-q", "--filter", "name=^"+trustNet+"$") != "" {
		t.Fatal("fixture network already exists; refusing to touch it")
	}
	docker(t, "network", "create", "--subnet", "172.31.254.0/24", "--ip-range", "172.31.254.128/25", trustNet)
	t.Cleanup(func() { exec.Command("docker", "network", "rm", trustNet).Run() })
	for _, name := range []string{"kyidentity", "kynotes", "kycalendar", "kyrecovery", "kypulse", "kyyard"} {
		t.Run(name, func(t *testing.T) {
			if existing := docker(t, "ps", "-aq", "--filter", "label=com.docker.compose.project=kyq-"+name); existing != "" {
				t.Fatal("fixture project already exists; refusing to touch it")
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			app := cat[name].WithRoute(catalog.Route{Host: name + ".example.test", Forward: fmt.Sprintf("127.0.0.1:%d", port), ForwardAddr: "127.0.0.1", EdgeAddress: edgeIP}).WithPrivateRecovery(false)
			root := t.TempDir()
			runner := remote.Local{}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			dir := filepath.Join(root, name)
			t.Cleanup(func() {
				_, err := runner.Run(context.Background(), "cd "+remote.Quote(dir)+" && KYQ_UID=$(id -u) KYQ_GID=$(id -g) docker compose -p "+remote.Quote("kyq-"+name)+" -f compose.yaml -f compose.kyq.yaml down --volumes --remove-orphans", nil)
				if err != nil {
					t.Error("fixture cleanup failed", err)
				}
			})
			for _, step := range dockerhost.Steps(dockerhost.App{Name: name, Root: root, Runner: runner, Catalog: app, ReleaseSet: "fixture", Redact: &engine.Redactor{}}) {
				if err := step.Apply(ctx); err != nil {
					t.Fatalf("%s: %v", step.ID(), err)
				}
				if err := step.Verify(ctx); err != nil {
					t.Fatal(err)
				}
			}
			client := &http.Client{Timeout: 10 * time.Second}
			res, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, app.Kubernetes.ReadinessPath))
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatalf("readiness: %d", res.StatusCode)
			}
			id := docker(t, "ps", "-q", "--filter", "label=com.docker.compose.project=kyq-"+name, "--filter", "label=com.docker.compose.service=app")
			if id == "" || strings.Contains(id, "\n") {
				t.Fatal("expected one fixture container")
			}
			binary := map[string]string{"kyidentity": "/usr/local/bin/kyidentity", "kynotes": "/kynotes-server", "kycalendar": "/app/kycalendar", "kyrecovery": "/app/kyrecovery", "kypulse": "/app/kypulse", "kyyard": "/app/kyyard-server"}[name]
			checkEdgeTrust(t, name, id, app.Kubernetes.Ports[0])
			var caps struct{ Schema, Service string }
			if err = json.Unmarshal([]byte(docker(t, "exec", id, binary, "apply-setup", "--capabilities")), &caps); err != nil || caps.Service != name {
				t.Fatal("native setup unavailable", err)
			}
			// Restart retains the private target-generated keys and database.
			docker(t, "restart", "--timeout", "30", id)
			for _, step := range dockerhost.Steps(dockerhost.App{Name: name, Root: root, Runner: runner, Catalog: app, ReleaseSet: "fixture", Redact: &engine.Redactor{}}) {
				if strings.HasSuffix(step.ID(), ".health") {
					if err := step.Apply(ctx); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

// checkEdgeTrust proves issue #95 on the pinned image: the product believes forwarded headers
// from the edge's exact address and from no other peer beside it.
func checkEdgeTrust(t *testing.T, name, id string, port int) {
	t.Helper()
	// Pull the pinned probe image first so docker run's pull progress (CombinedOutput)
	// cannot contaminate the JSON body this helper parses below.
	docker(t, "pull", "-q", kube.CredentialGeneratorImage)
	docker(t, "network", "connect", trustNet, id)
	app := docker(t, "inspect", id, "--format", "{{(index .NetworkSettings.Networks \""+trustNet+"\").IPAddress}}")
	path := "/api/installer/network"
	if name == "kynotes" {
		path = "/api/v1/installer/network"
	}
	for peer, trusted := range map[string]bool{edgeIP: true, otherIP: false} {
		out := docker(t, "run", "--rm", "--network", trustNet, "--ip", peer, kube.CredentialGeneratorImage,
			"wget", "-qO-", "--header", "X-Forwarded-For: 203.0.113.7", "--header", "X-Forwarded-Proto: https", fmt.Sprintf("http://%s:%d%s", app, port, path))
		var n struct {
			ClientIP string `json:"clientIp"`
			Trusted  bool   `json:"proxyHeadersTrusted"`
		}
		want := map[bool]string{true: "203.0.113.7", false: peer}[trusted]
		if err := json.Unmarshal([]byte(out), &n); err != nil || n.Trusted != trusted || n.ClientIP != want {
			t.Fatalf("%s from %s: %s, want trusted %v and client %s", name, peer, out, trusted, want)
		}
	}
}

func TestDockerKyYardRunner(t *testing.T) {
	if docker(t, "ps", "-aq", "--filter", "label=com.docker.compose.project=kyq-kyyard") != "" {
		t.Fatal("fixture project already exists")
	}
	work := t.TempDir()
	// Test plumbing: a static binary from this checkout in the same pinned runtime
	// base. Production still requires the release's verified image digest.
	build := exec.Command("go", "build", "-o", filepath.Join(work, "kyquickstart"), "../../cmd/kyquickstart")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build runner: %v %s", err, out)
	}
	dockerfile := `FROM gcr.io/distroless/static-debian12@sha256:6447365a6337c3732f412d1b74357b30a633831955b2bc45552b0086be907687
COPY kyquickstart /usr/local/bin/kyquickstart
USER 10001:10001
`
	if err := os.WriteFile(filepath.Join(work, "Dockerfile"), []byte(dockerfile), 0600); err != nil {
		t.Fatal(err)
	}
	image := "kyq-docker-runner-fixture:local"
	docker(t, "build", "-q", "-t", image, work)
	t.Cleanup(func() { exec.Command("docker", "image", "rm", image).Run() })
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	app := cat["kyyard"].WithRoute(catalog.Route{Host: "yard.example.test", Forward: "127.0.0.1:19273", ForwardAddr: "127.0.0.1", EdgeAddress: "192.0.2.2"}).WithPrivateRecovery(false)
	app, err = app.WithDockerRunner("fixture/runner@sha256:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	var composition map[string]any
	if err = yaml.Unmarshal(app.Compose, &composition); err != nil {
		t.Fatal(err)
	}
	services := composition["services"].(map[string]any)
	services["runner"].(map[string]any)["image"] = image
	// No public publish is needed to prove the private runner topology.
	delete(services["app"].(map[string]any), "ports")
	for _, bind := range app.Binds {
		if err = os.Mkdir(filepath.Join(work, bind), 0700); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := yaml.Marshal(composition)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(work, "compose.yaml")
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"compose", "-p", "kyq-kyyard", "-f", file}
	command := func(args ...string) string { return docker(t, append(base, args...)...) }
	t.Setenv("KYQ_UID", fmt.Sprint(os.Getuid()))
	t.Setenv("KYQ_GID", fmt.Sprint(os.Getgid()))
	t.Cleanup(func() { exec.Command("docker", append(base, "down", "--volumes", "--remove-orphans")...).Run() })
	command("up", "-d", "--pull", "never")
	socket := filepath.Join(work, "runner-socket", "runner.sock")
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(time.Minute)
	for {
		res, err := client.Get("http://runner/v1/status")
		if err == nil {
			res.Body.Close()
			if res.StatusCode != http.StatusConflict {
				t.Fatalf("unassigned runner should refuse without a handed-off plan: %d", res.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runner socket unavailable", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	yard := strings.TrimSpace(command("ps", "-q", "app"))
	docker(t, "exec", yard, "sh", "-c", "test -S /run/kyquickstart/runner.sock && test ! -e /var/run/docker.sock && test ! -e /state")
	command("up", "-d", "--force-recreate", "--pull", "never", "app")
	yard = strings.TrimSpace(command("ps", "-q", "app"))
	docker(t, "exec", yard, "sh", "-c", "test -S /run/kyquickstart/runner.sock")
	if _, err = os.Stat(filepath.Join(work, "runner-state", "runner.json")); err != nil {
		t.Fatal("runner identity not persisted", err)
	}
}
