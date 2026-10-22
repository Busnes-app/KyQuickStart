//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const officeHost = "office.example.test"

// TestKubernetesEuroOffice installs KyDrive's editor: NPM, KyIdentity and the catalog
// euro-office in a kind cluster of its own, KyDrive on an sshd Docker target behind its lan
// edge. It proves the baseline pod with exactly its measured capabilities and seeded claims,
// KyDrive's editor_secret copied into the editor's Secret and nowhere else, /healthcheck through
// the edge, KyDrive accepting a callback signed with that secret (and refusing another), the
// editor reaching KyDrive through the edge and nothing else, a skipped rerun and a restart.
// The editor needs about 3 GiB and its image is 7 GB, so it runs only with KYQ_TEST_EURO_OFFICE=1.
func TestKubernetesEuroOffice(t *testing.T) {
	if os.Getenv("KYQ_TEST_EURO_OFFICE") == "" {
		t.Skip("set KYQ_TEST_EURO_OFFICE=1: needs a 5 GiB kind node and the 7 GB Euro-Office image")
	}
	ctx := context.Background()
	work := t.TempDir()
	state := filepath.Join(work, "state")
	os.MkdirAll(state, 0o700)
	name, kc := sizedKindCluster(t, work, "5g")
	node := name + "-control-plane"
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	cs := kubernetes.NewForConfigOrDie(cfg)
	catalogDir := filepath.Join(work, "catalog")
	for _, app := range []string{"npm", "kyidentity", "euro-office"} {
		if err := os.CopyFS(filepath.Join(catalogDir, app), os.DirFS("../../internal/catalog/apps/"+app)); err != nil {
			t.Fatal(err)
		}
	}
	plan := filepath.Join(state, "stack.yaml")
	os.WriteFile(plan, []byte("version: 1\ntargets:\n  - name: k8s\n    kubeconfig: "+kc+"\n    storage_class: standard\napps:\n  - name: npm\n    target: k8s\n"), 0o600)
	var trust []string
	var outputs []string
	apply := func(want string, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		err := cli.Run(ctx, append(append([]string{args[0], "--state", state}, args[1:]...), trust...), cli.Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "e2e", Out: &out})
		t.Logf("%s:\n%s", want, out.String())
		if err != nil {
			t.Fatalf("%s: %v", want, err)
		}
		outputs = append(outputs, out.String())
		return out.String()
	}
	apply("npm", "apply")

	connected, err := kube.Connect(kc, "")
	if err != nil {
		t.Fatal(err)
	}
	creds, err := cs.CoreV1().Secrets("kyq-edge").Get(ctx, "kyq-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	certID, certPEM := uploadCertificate(t, ctx, connected.ServiceDialer("kyq-npm", "app", 81), string(creds.Data["npm_token"]))
	ip := func(container string) string {
		return docker(t, "inspect", container, "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
	}
	nodeIP := ip(node)
	client := name + "-client"
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", client).Run() })
	docker(t, "run", "-d", "--name", client, "--network", "kind", "--entrypoint", "sleep", kindNode, "3600")

	loadCatalogApp(t, name, cs, "kyidentity")
	loadCatalogApp(t, name, cs, "euro-office")
	drive := startDriveTarget(t, work, catalogDir, nodeIP, certPEM)
	st, err := stack.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	// Pods reach routed hosts through the edge, so the pod network is a lan client too.
	st.Edges = []stack.Edge{{Target: "k8s", Provider: "lan", NPMURL: "kube://kyq-npm/app:81", ClientPort: 30443,
		AllowCIDRs: []string{ip(client) + "/32", mustNode(t, ctx, cs, node).Spec.PodCIDR}}}
	addCatalogIdentity(st, certID)
	addCatalogDrive(st, certID, drive)
	st.Apps = append(st.Apps, stack.App{Name: "euro-office", Target: "k8s"})
	st.Edges[0].Routes = append(st.Edges[0].Routes, stack.Route{App: "euro-office", Hostname: officeHost,
		ForwardHost: "app.kyq-euro-office.svc.cluster.local", ForwardPort: 80, CertificateID: certID})
	b, _ := yaml.Marshal(st)
	os.WriteFile(plan, b, 0o600)
	testClusterDNS(t, ctx, cs, nodeIP, nodeIP+" "+officeHost)
	trust = []string{"--trust-host-key", "nas=" + drive.fp}
	out := apply("editor", "apply")
	for _, id := range []string{"kydrive.secrets", "kydrive.deploy", "euro-office.secrets", "euro-office.deploy", "euro-office.health"} {
		if status(out, id) != "ok" {
			t.Fatalf("%s = %q", id, status(out, id))
		}
	}
	if strings.Index(out, "kydrive.setup") > strings.Index(out, "euro-office.secrets") {
		t.Fatal("euro-office ran before KyDrive")
	}

	// Baseline, root with exactly the measured capabilities, both claims seeded.
	ns, err := cs.CoreV1().Namespaces().Get(ctx, "kyq-euro-office", metav1.GetOptions{})
	if err != nil || ns.Labels["pod-security.kubernetes.io/enforce"] != "baseline" {
		t.Fatalf("namespace = %v, %v", ns.Labels, err)
	}
	podNS, pod, container, err := connected.AppPod(ctx, "euro-office")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := cs.CoreV1().Pods(podNS).Get(ctx, pod, metav1.GetOptions{})
	if sc := p.Spec.Containers[0].SecurityContext; *sc.RunAsUser != 0 || fmt.Sprint(sc.Capabilities.Add) != "[CHOWN DAC_OVERRIDE FOWNER SETGID SETUID KILL]" || *sc.AllowPrivilegeEscalation {
		t.Fatalf("security context = %+v", sc)
	}
	inPod := func(script string) string {
		t.Helper()
		out, err := connected.Exec(ctx, podNS, pod, container, []string{"/bin/sh", "-c", script})
		if err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := inPod("stat -c '%U %a' /var/lib/postgresql/16/main /var/lib/euro-office/documentserver; ls -A /var/lib/postgresql/.kyq-seeded /var/lib/euro-office/.kyq-seeded"); got != "postgres 700\nds 755\n/var/lib/euro-office/.kyq-seeded\n/var/lib/postgresql/.kyq-seeded" {
		t.Fatalf("seeded claims:\n%s", got)
	}

	// One secret on both sides, copied over SSH and the API only.
	sec, err := cs.CoreV1().Secrets("kyq-euro-office").Get(ctx, "kyq-secrets", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	jwt := string(sec.Data["jwt_secret"])
	if onDrive := docker(t, "exec", drive.name, "cat", drive.root+"/kydrive/secrets/editor_secret"); len(jwt) != 43 || onDrive != jwt || string(sec.Data["secure_link_secret"]) == jwt {
		t.Fatal("the editor's jwt_secret is not KyDrive's editor_secret")
	}
	if inPod("cat /var/www/euro-office/Data/.private/jwt_secret") != jwt {
		t.Fatal("the editor does not read the shared secret")
	}

	// The editor answers through the edge, and KyDrive takes a callback signed with the
	// shared secret and refuses one signed with any other.
	viaEdge := func(args ...string) string {
		t.Helper()
		return docker(t, append([]string{"exec", client, "curl", "-sk", "-m", "20", "--resolve", officeHost + ":30443:" + nodeIP, "--resolve", catalogDriveHost + ":30443:" + nodeIP}, args...)...)
	}
	if got := viaEdge("https://" + officeHost + ":30443/healthcheck"); got != "true" {
		t.Fatalf("/healthcheck through the edge = %q", got)
	}
	// Non-editor surfaces answer 404 at the edge; the editor's loader still comes through.
	for _, path := range []string{"/info/info.json", "/INFO/info.json", "/internal/cluster/inactive", "/9.3.4-x1/info/info.json", "/ai-proxy", "/admin", "/example/", "/welcome/", "/"} {
		if got := viaEdge("-o", "/dev/null", "-w", "%{http_code}", "https://"+officeHost+":30443"+path); got != "404" && !(path == "/" && got == "302") {
			t.Errorf("%s through the edge: HTTP %s, want 404", path, got)
		}
	}
	if got := viaEdge("-o", "/dev/null", "-w", "%{http_code}", "https://"+officeHost+":30443/web-apps/apps/api/documents/api.js"); got != "200" {
		t.Fatalf("the editor's api.js through the edge: HTTP %s", got)
	}
	callback := func(secret string) string {
		return viaEdge("-o", "/dev/null", "-w", "%{http_code}", "-X", "POST", "-H", "Content-Type: application/json",
			"-H", "Authorization: Bearer "+signHS256(secret, fmt.Sprintf(`{"key":"kyq-e2e-probe","status":1,"exp":%d}`, time.Now().Add(5*time.Minute).Unix())),
			"-d", "{}", "https://"+catalogDriveURLHost+"/api/editor/callback")
	}
	if got := callback(jwt); got != "200" {
		t.Fatalf("KyDrive refused a callback signed with the shared secret: HTTP %s", got)
	}
	if got := callback(strings.Repeat("x", 43)); got != "401" {
		t.Fatalf("KyDrive took a callback signed with another secret: HTTP %s", got)
	}

	// The deploy step's egress check ran and left no pod behind.
	if pods, _ := cs.CoreV1().Pods("kyq-euro-office").List(ctx, metav1.ListOptions{}); len(pods.Items) != 1 {
		t.Fatalf("pods in kyq-euro-office: %d, want the editor's alone", len(pods.Items))
	}
	// Egress: KyDrive through the edge's HTTPS port; not its plain HTTP port, not the API server.
	if got := inPod("curl -s -m 5 -o /dev/null -w '%{http_code}' http://" + catalogDriveHost + ":30080/ || true"); got != "000" {
		t.Fatalf("the editor reached the edge's HTTP port: HTTP %s", got)
	}
	if got := inPod("curl -sk -m 15 -o /dev/null -w '%{http_code}' https://" + catalogDriveURLHost + "/healthz || true"); got != "200" {
		t.Fatalf("the editor cannot reach KyDrive through the edge: HTTP %s", got)
	}
	if got := inPod("curl -sk -m 5 -o /dev/null -w '%{http_code}' https://kubernetes.default.svc/version || true"); got != "000" {
		t.Fatalf("the editor reached the API server: HTTP %s", got)
	}

	// Rerun: nothing changes. Restart: the new pod starts on the seeded claims, nothing refused.
	for _, line := range strings.Split(strings.TrimSpace(apply("rerun", "apply")), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && strings.Contains(f[0], ".") && f[1] != "skipped" {
			t.Errorf("rerun step not skipped: %s", line)
		}
	}
	apply("restart", "restart", "euro-office")
	if got := viaEdge("https://" + officeHost + ":30443/healthcheck"); got != "true" {
		t.Fatalf("/healthcheck after restart = %q", got)
	}
	if logs, err := connected.Logs(ctx, "euro-office", "", 2000); err != nil || bytes.Contains(logs, []byte("Operation not permitted")) {
		t.Fatalf("editor logs (%v) name a refused operation:\n%s", err, logs)
	}

	// Uninstall: KyDrive is redeployed without the editor first, then the editor goes.
	apply("uninstall", "uninstall", "euro-office")
	if env := docker(t, "inspect", "kyq-kydrive-app-1", "--format", "{{json .Config.Env}}"); strings.Contains(env, "KYDRIVE_EDITOR") {
		t.Fatalf("KyDrive still names the editor: %s", env)
	}
	if got := callback(jwt); got != "503" {
		t.Fatalf("KyDrive's editor callback after uninstall: HTTP %s, want 503 (no editor)", got)
	}
	if _, err := cs.AppsV1().Deployments("kyq-euro-office").Get(ctx, "app", metav1.GetOptions{}); err == nil {
		t.Fatal("the editor's Deployment survived uninstall")
	}

	// The shared secret never reached the workstation's output or state.
	filepath.WalkDir(state, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(path); bytes.Contains(b, []byte(jwt)) {
				t.Errorf("%s holds the shared secret", path)
			}
		}
		return nil
	})
	for _, o := range outputs {
		if strings.Contains(o, jwt) {
			t.Fatal("output holds the shared secret")
		}
	}
}

// signHS256 is a compact JWT over payload, signed as Euro-Office signs KyDrive's callbacks.
func signHS256(secret, payload string) string {
	enc := base64.RawURLEncoding.EncodeToString
	signing := enc([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + enc([]byte(payload))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signing))
	return signing + "." + enc(mac.Sum(nil))
}

// sizedKindCluster is kindCluster with a node of the given memory: the editor does not fit
// the shared 2 GiB node.
func sizedKindCluster(t *testing.T, work, memory string) (string, string) {
	t.Helper()
	kc := filepath.Join(work, "kubeconfig")
	id := make([]byte, 4)
	rand.Read(id)
	name := "kyq-e2e-" + hex.EncodeToString(id)
	dockerBinary, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	commands := filepath.Join(work, "commands")
	if err := os.Mkdir(commands, 0700); err != nil {
		t.Fatal(err)
	}
	wrapper := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = run ]; then\n  shift\n  exec %s run --memory %s --memory-swap %s --cpus 2 --pids-limit 2048 \"$@\"\nfi\nexec %s \"$@\"\n",
		remote.Quote(dockerBinary), memory, memory, remote.Quote(dockerBinary))
	if err := os.WriteFile(filepath.Join(commands, "docker"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", commands+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		exec.Command("go", "run", "sigs.k8s.io/kind@v0.33.0", "delete", "cluster", "--name", name).Run()
	})
	kind(t, "create", "cluster", "--name", name, "--image", kindNode, "--kubeconfig", kc, "--wait", "180s")
	return name, kc
}
