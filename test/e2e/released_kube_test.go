//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// TestKubernetesNPMEdgeReleased and TestKubernetesNginxEdgeReleased route a Docker app through
// the cluster's lan edge, take it out of the catalog, unmanage it and apply the rest of the
// plan: the released route still reaches the app through the edge, whose egress policy keeps
// exactly its upstream, and the app's container is never recreated.
func TestKubernetesNPMEdgeReleased(t *testing.T) { testKubernetesEdgeReleased(t, "npm") }

func TestKubernetesNginxEdgeReleased(t *testing.T) { testKubernetesEdgeReleased(t, stack.NginxApp) }

func testKubernetesEdgeReleased(t *testing.T, proxy string) {
	ctx := context.Background()
	work := t.TempDir()
	state := filepath.Join(work, "state")
	os.MkdirAll(state, 0o700)
	name, kc := kindCluster(t, work)
	node := name + "-control-plane"
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	cs := kubernetes.NewForConfigOrDie(cfg)
	catalogDir := filepath.Join(work, "catalog")
	for _, src := range []string{"../../internal/catalog/testdata/apps/hello", "../../internal/catalog/apps/" + proxy} {
		if err := os.CopyFS(filepath.Join(catalogDir, filepath.Base(src)), os.DirFS(src)); err != nil {
			t.Fatal(err)
		}
	}
	// parked is the app the next release's catalog lacks: hello's whoami on the Docker target,
	// published on its route's forward address.
	parked := filepath.Join(catalogDir, "parked")
	if err := os.CopyFS(parked, os.DirFS("../../internal/catalog/testdata/apps/hello")); err != nil {
		t.Fatal(err)
	}
	for file, r := range map[string][2]string{
		"manifest.yaml":     {"name: hello\n", "name: parked\n"},
		"compose.yaml.tmpl": {"    image: {{ .Images.whoami }}\n", "    image: {{ .Images.whoami }}\n    command: [\"--port\", \"8080\"]\n    ports: [\"{{forward}}:8080\"]\n"},
	} {
		b, _ := os.ReadFile(filepath.Join(parked, file))
		if !strings.Contains(string(b), r[0]) {
			t.Fatalf("hello's %s changed; test plumbing for %q", file, r[0])
		}
		os.WriteFile(filepath.Join(parked, file), []byte(strings.Replace(string(b), r[0], r[1], 1)), 0o600)
	}
	plan := filepath.Join(state, "stack.yaml")
	os.WriteFile(plan, []byte("version: 1\ntargets:\n  - name: k8s\n    kubeconfig: "+kc+"\n    storage_class: standard\napps:\n  - name: "+proxy+"\n    target: k8s\n  - name: hello\n    target: k8s\n"), 0o600)
	var trust []string
	run := func(label string, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		err := cli.Run(ctx, append(append(args, "--state", state), trust...), cli.Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "e2e", Out: &out})
		t.Logf("%s:\n%s", label, out.String())
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		return out.String()
	}
	out := run("install", "apply")
	var k kindEdge
	if proxy == stack.NginxApp {
		k = checkNginxInstall(t, ctx, cs, out)
	} else {
		connected, err := kube.Connect(kc, "")
		if err != nil {
			t.Fatal(err)
		}
		k, _ = checkNPMInstall(t, ctx, cs, connected, out)
	}
	ip := func(container string) string {
		return docker(t, "inspect", container, "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
	}
	nodeIP := ip(node)
	client := name + "-client"
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", client).Run() })
	docker(t, "run", "-d", "--name", client, "--network", "kind", "--entrypoint", "sleep", kindNode, "900")

	nas := startDriveTarget(t, work, catalogDir, nodeIP, k.certPEM)
	t.Cleanup(func() { exec.Command("docker", "compose", "-p", "kyq-parked", "down").Run() })
	port := freePort(t, nas.forward)
	upstream := net.JoinHostPort(nas.forward, strconv.Itoa(port))
	allow := []string{ip(client) + "/32", mustNode(t, ctx, cs, node).Spec.PodCIDR}
	for _, gw := range strings.Fields(docker(t, "network", "inspect", "kind", "--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}")) {
		if strings.Contains(gw, ".") {
			allow = append(allow, gw+"/32")
		}
	}
	st, err := stack.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	sshPort, _ := strconv.Atoi(nas.port)
	st.Targets = append(st.Targets, stack.Target{Name: "nas", Host: "127.0.0.1", Port: sshPort, User: "root", Root: nas.root})
	st.Apps = append(st.Apps, stack.App{Name: "parked", Target: "nas"})
	st.Edges = []stack.Edge{{Target: "k8s", Provider: "lan", NPMURL: "kube://kyq-npm/app:81", AllowCIDRs: allow, ClientPort: 30443, Routes: []stack.Route{
		{App: "hello", Hostname: "hello.example.test", ForwardHost: "app.kyq-hello.svc.cluster.local", ForwardPort: 8080, CertificateID: k.certID},
		{App: "parked", Hostname: "parked.example.test", ForwardHost: nas.forward, ForwardPort: port, CertificateID: k.certID},
	}}}
	k.certify(st)
	b, _ := yaml.Marshal(st)
	os.WriteFile(plan, b, 0o600)
	trust = []string{"--trust-host-key", "nas=" + nas.fp}
	run("route parked", "apply")

	// get is what the LAN client gets for parked's route through the edge: whoami names the
	// container that answered.
	get := func() string {
		b, _ := exec.Command("docker", "exec", client, "curl", "-sk", "-m", "10", "--resolve", "parked.example.test:30443:"+nodeIP, "https://parked.example.test:30443/").CombinedOutput()
		return string(b)
	}
	container := docker(t, "ps", "-q", "--no-trunc", "--filter", "label=com.docker.compose.project=kyq-parked")
	if page := get(); container == "" || !strings.Contains(page, "Hostname: "+container[:12]) {
		t.Fatalf("parked through the edge (container %q):\n%s", container, page)
	}
	egress := func() string {
		p, err := cs.NetworkingV1().NetworkPolicies("kyq-"+proxy).Get(ctx, "default-deny-ingress", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var cidrs []string
		for _, r := range p.Spec.Egress {
			for _, to := range r.To {
				if to.IPBlock != nil {
					cidrs = append(cidrs, to.IPBlock.CIDR+" "+r.Ports[0].Port.String())
				}
			}
		}
		return strings.Join(cidrs, ",")
	}
	want := nas.forward + "/32 " + strconv.Itoa(port)
	if got := egress(); got != want {
		t.Fatalf("edge egress before the release = %q, want %q", got, want)
	}

	// The next release lacks parked: unmanage forgets it, and the rest of the plan applies.
	if err := os.RemoveAll(parked); err != nil {
		t.Fatal(err)
	}
	run("unmanage parked", "unmanage", "parked")
	run("remaining plan", "apply")
	if page := get(); !strings.Contains(page, "Hostname: "+container[:12]) {
		t.Fatalf("the released route no longer reaches parked through the edge:\n%s", page)
	}
	if got := egress(); got != want {
		t.Errorf("edge egress after the remaining plan = %q, want %q", got, want)
	}
	if now := docker(t, "ps", "-q", "--no-trunc", "--filter", "label=com.docker.compose.project=kyq-parked"); now != container {
		t.Errorf("parked's container changed: %q, was %q", now, container)
	}
	// Where the allowance lives now: the edge's released entry, which the operator deletes.
	var after struct {
		Edges []struct {
			Routes   []struct{ App string }
			Released []map[string]string
		}
	}
	b, _ = os.ReadFile(plan)
	if err := yaml.Unmarshal(b, &after); err != nil || len(after.Edges) != 1 || len(after.Edges[0].Routes) != 1 || len(after.Edges[0].Released) != 1 ||
		after.Edges[0].Released[0]["hostname"] != "parked.example.test" || after.Edges[0].Released[0]["upstream"] != upstream {
		t.Errorf("plan after unmanage (%v):\n%s", err, b)
	}
}
