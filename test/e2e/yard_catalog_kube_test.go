//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
)

const (
	catalogYardHost    = "yard.example.test"
	catalogYardURLHost = catalogYardHost + ":30443"
	yardSetupDir       = "/data/installer/4e7a1c2b-9d3f-4b6a-8e5c-1f2a3b4c5d6e"
	// Test plumbing: the kubectl a handover reader would use, for the printed break-glass line.
	kubectlImage  = "registry.k8s.io/kubectl:v1.37.0@sha256:5ed410ebac5dc976cc717098994dcdb29bbbd38f6bd65f582311f5be4ba719cf"
	yardRunnerPod = "kyq-kyyard/app.kubernetes.io/name=kyyard"
)

// yardRouteStep is the edge step that routes KyYard (the NPM host step's ID).
var yardRouteStep = func() string {
	sum := sha256.Sum256([]byte(catalogYardHost))
	return "k8s.edge." + hex.EncodeToString(sum[:8])
}()

// TestKubernetesKyYard installs the catalog KyYard with the suite runner in its pod and hands
// the plan to that runner. It has a cluster of its own: the runner works with its own build's
// catalog and from inside the pod, so the plan holds only catalog apps on this cluster (the
// shared test's hello and Docker target are beyond it).
func TestKubernetesKyYard(t *testing.T) {
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
	for _, app := range []string{"npm", "kyidentity", "kyyard"} {
		if err := os.CopyFS(filepath.Join(catalogDir, app), os.DirFS("../../internal/catalog/apps/"+app)); err != nil {
			t.Fatal(err)
		}
	}
	runnerImage := yardRunnerImage(t, name, node)
	plan := filepath.Join(state, "stack.yaml")
	os.WriteFile(plan, []byte("version: 1\ntargets:\n  - name: k8s\n    kubeconfig: "+kc+"\n    storage_class: standard\napps:\n  - name: npm\n    target: k8s\n"), 0o600)
	var outputs []string
	run := func(label string, args ...string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		err := cli.Run(ctx, append(args, "--state", state), cli.Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "e2e", Out: &out, RunnerImage: runnerImage})
		t.Logf("%s (%s): %v\n%s", args[0], label, err, out.String())
		outputs = append(outputs, out.String())
		return out.String(), err
	}
	apply := func(label string) string {
		t.Helper()
		out, err := run(label, "apply")
		if err != nil {
			t.Fatalf("apply (%s): %v", label, err)
		}
		return out
	}
	save := func(st *stack.Stack) {
		b, _ := yaml.Marshal(st)
		os.WriteFile(plan, b, 0o600)
	}

	// The edge: NPM with a throwaway certificate, a lan edge admitting this host and the pods.
	apply("npm")
	connected, err := kube.Connect(kc, "")
	if err != nil {
		t.Fatal(err)
	}
	sec, err := cs.CoreV1().Secrets("kyq-edge").Get(ctx, "kyq-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	certID, certPEM := uploadCertificate(t, ctx, connected.ServiceDialer("kyq-npm", "app", 81), string(sec.Data["npm_token"]))
	nodeIP := docker(t, "inspect", node, "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
	st, err := stack.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	allow := []string{mustNode(t, ctx, cs, node).Spec.PodCIDR}
	for _, gw := range strings.Fields(docker(t, "network", "inspect", "kind", "--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}")) {
		if strings.Contains(gw, ".") {
			allow = append(allow, gw+"/32")
		}
	}
	st.Edges = []stack.Edge{{Target: "k8s", Provider: "lan", NPMURL: "kube://kyq-npm/app:81", AllowCIDRs: allow, ClientPort: 30443}}
	loadCatalogApp(t, name, cs, "kyidentity")
	addCatalogIdentity(st, certID)
	save(st)
	// Test-only cluster DNS: KyYard reaches KyIdentity through the edge, as a browser does.
	testClusterDNS(t, ctx, cs, nodeIP)
	if out := apply("identity"); status(out, "kyidentity.setup") != "ok" {
		t.Fatal("KyIdentity's first setup did not run")
	}

	// KyYard joins after KyIdentity's first setup, by setup amendment.
	loadCatalogApp(t, name, cs, "kyyard")
	st, _ = stack.Load(plan)
	addCatalogYard(st, certID)
	save(st)
	out := apply("yard")
	client := yardClient(t, certPEM, nodeIP)
	secrets := checkCatalogYard(t, ctx, cs, connected, kc, state, out, client)
	// Test-only: KyYard's discovery and token exchange at sign-in go through the edge and its
	// throwaway certificate. The Deployment's input hash is unchanged, so reruns skip it.
	trustTestCA(t, ctx, cs, connected, "kyyard", certPEM)
	yardSignOn(t, ctx, connected, client, false)
	admin := yardSignOn(t, ctx, connected, client, true)

	// AC15: a rerun with no plan change skips every KyYard step.
	out = apply("yard rerun")
	for _, id := range []string{"kyidentity.amend.kyyard", "kyyard.deploy", "kyyard.health", "kyyard.setup", yardRouteStep} {
		if status(out, id) != "skipped" {
			t.Errorf("rerun: %s = %q", id, status(out, id))
		}
	}

	// AC14: the printed handoff line, run as printed with this checkout's kyquickstart, twice.
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "kyquickstart handoff ") {
			line = strings.TrimSpace(l)
		}
	}
	if want := "kyquickstart handoff --state '" + state + "' --kyyard-target k8s --runner-pod " + yardRunnerPod; line != want {
		t.Fatalf("handoff line %q, want %q", line, want)
	}
	bin := filepath.Join(work, "bin")
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "kyquickstart"), "../../cmd/kyquickstart")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build kyquickstart: %v\n%s", err, b)
	}
	ns, pod, _, err := connected.AppPod(ctx, "kyyard")
	if err != nil {
		t.Fatal(err)
	}
	before := yardPodState(t, ctx, cs, pod)
	var runnerIDs []string
	for i := range 2 {
		sh := exec.Command("sh", "-c", line)
		sh.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		b, err := sh.CombinedOutput()
		t.Logf("handoff %d: %v\n%s", i+1, err, b)
		outputs = append(outputs, string(b))
		if err != nil {
			t.Fatalf("handoff %d: %v", i+1, err)
		}
		m, err := connected.Marker(ctx)
		if err != nil || m.Owner != "runner" {
			t.Fatalf("marker after handoff %d: %+v %v", i+1, m, err)
		}
		runnerIDs = append(runnerIDs, m.RunnerID)
	}
	if runnerIDs[0] != runnerIDs[1] {
		t.Fatalf("rerun changed the runner: %v", runnerIDs)
	}
	if _, err := cs.CoreV1().ServiceAccounts("kyquickstart").Get(ctx, "kyquickstart-runner", metav1.GetOptions{}); err != nil {
		t.Fatalf("runner service account: %v", err)
	}
	revision := mustStack(t, plan).Revision

	// Through the shared socket, as KyYard would: a uid-10001 client mounting only the socket.
	ephemeral(t, ctx, cs, pod, corev1.EphemeralContainer{EphemeralContainerCommon: corev1.EphemeralContainerCommon{
		Name: "client", Image: curlImage, Command: []string{"sleep", "3600"},
		VolumeMounts: []corev1.VolumeMount{{Name: "runner-socket", MountPath: "/run/kyquickstart"}},
	}})
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		args := []string{"curl", "-sS", "--max-time", "15", "--unix-socket", "/run/kyquickstart/sock/runner.sock", "-X", method, "-w", "\n%{http_code}"}
		if body != nil {
			b, _ := json.Marshal(body)
			args = append(args, "-H", "Content-Type: application/json", "--data-binary", string(b))
		}
		out, err := connected.Exec(ctx, ns, pod, "client", append(args, "http://runner"+path))
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		i := bytes.LastIndexByte(out, '\n')
		code, _ := strconv.Atoi(string(out[i+1:]))
		var res map[string]any
		json.Unmarshal(out[:i], &res)
		return code, res
	}
	code, got := call("GET", "/v1/status", nil)
	if r, _ := got["runner"].(map[string]any); code != 200 || r["plan_owner"] != "runner" || r["plan_revision"] != float64(revision) || r["runner_id"] != runnerIDs[0] {
		t.Fatalf("status: %d %v", code, got)
	}
	// KyYard's own view of the runner, read with the admin's session.
	r, b := send(t, ctx, admin, "GET", "https://"+catalogYardURLHost+"/api/suite/status", nil, "")
	var suiteStatus struct {
		Configured bool `json:"configured"`
		Runner     struct {
			PlanOwner string `json:"plan_owner"`
			RunnerID  string `json:"runner_id"`
		} `json:"runner"`
	}
	if r.StatusCode != 200 || json.Unmarshal(b, &suiteStatus) != nil || !suiteStatus.Configured || suiteStatus.Runner.PlanOwner != "runner" || suiteStatus.Runner.RunnerID != runnerIDs[0] {
		t.Fatalf("KyYard's suite status: %d %s", r.StatusCode, b)
	}
	t.Logf("KyYard reads the runner through the socket: %s", b)
	operate := func(apps ...string) map[string]any {
		t.Helper()
		id := uuid.NewString()
		req := map[string]any{"id": id, "kind": "upgrade", "apps": append([]string{}, apps...), "acknowledge_no_backup": false, "actor": "e2e-admin"}
		if code, out := call("POST", "/v1/operations", req); code != 202 {
			t.Fatalf("upgrade %v: %d %v", apps, code, out)
		}
		for range 600 {
			if _, op := call("GET", "/v1/operations/"+id, nil); op != nil && op["state"] != "running" {
				t.Logf("upgrade %v: %v", apps, op)
				return op
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("upgrade %v never finished", apps)
		return nil
	}
	// Every app: KyYard's workload is one note step and its pod is left alone.
	op := operate()
	stepStatus := map[string]string{}
	steps, _ := op["steps"].([]any)
	for _, s := range steps {
		if m, ok := s.(map[string]any); ok {
			stepStatus[m["id"].(string)], _ = m["status"].(string)
		}
	}
	if op["state"] != "succeeded" || stepStatus["kyyard.runner"] == "" || stepStatus["kyidentity.deploy"] == "" || stepStatus["kyyard.deploy"] != "" || stepStatus["kyyard.setup"] != "" {
		t.Fatalf("upgrade through the runner: %v", op)
	}
	if op := operate("kyyard"); op["state"] != "failed" || op["error"].(map[string]any)["code"] != "runner_host" {
		t.Fatalf("upgrade kyyard through the runner: %v", op)
	}
	if after := yardPodState(t, ctx, cs, pod); after != before {
		t.Fatalf("KyYard's pod changed under its runner: %s, was %s", after, before)
	}
	runnerSecrets := runnerCredentials(t, ctx, cs, connected, ns, pod)

	// The workstation may not redeploy KyYard under its runner; revoke gives the plan back.
	if _, err := run("under the runner", "apply"); err == nil || !strings.Contains(err.Error(), "owned_by_runner") {
		t.Fatalf("workstation apply under the runner: %v", err)
	}
	if _, err := run("revoke", "handoff", "--revoke"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := cs.CoreV1().ServiceAccounts("kyquickstart").Get(ctx, "kyquickstart-runner", metav1.GetOptions{}); err == nil {
		t.Fatal("runner service account left after revoke")
	}
	if m, err := connected.Marker(ctx); err != nil || m.Owner != "cli" || m.RunnerID != "" {
		t.Fatalf("marker after revoke: %+v %v", m, err)
	}
	out = apply("after revoke")
	for _, id := range []string{"kyidentity.amend.kyyard", "kyyard.deploy", "kyyard.health", "kyyard.setup", yardRouteStep} {
		if status(out, id) != "skipped" {
			t.Errorf("after revoke: %s = %q", id, status(out, id))
		}
	}

	// AC16: no credential reached any output or the workstation state.
	all := append(secrets, runnerSecrets...)
	for _, o := range outputs {
		for _, s := range all {
			if strings.Contains(o, s) {
				t.Error("credential printed")
			}
		}
	}
	filepath.WalkDir(state, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			for _, s := range all {
				if bytes.Contains(b, []byte(s)) {
					t.Errorf("credential stored in %s", p)
				}
			}
		}
		return nil
	})
}

// yardRunnerImage builds the runner from this checkout, loads it into kind and names it by the
// digest kind's containerd recorded. Test plumbing: a real install pins an attested release;
// this proves a source build, by digest, through the same render and pull path.
func yardRunnerImage(t *testing.T, cluster, node string) string {
	t.Helper()
	docker(t, "build", "-q", "--build-arg", "VERSION=e2e", "-t", "kyq-e2e-runner", "../..")
	kind(t, "load", "docker-image", "kyq-e2e-runner", "--name", cluster)
	const tagged = "docker.io/library/kyq-e2e-runner:latest"
	lines := strings.Split(docker(t, "exec", node, "ctr", "-n", "k8s.io", "images", "ls", "name=="+tagged), "\n")
	if len(lines) != 2 || len(strings.Fields(lines[1])) < 3 || !strings.HasPrefix(strings.Fields(lines[1])[2], "sha256:") {
		t.Fatalf("loaded runner image: %q", lines)
	}
	ref := "docker.io/library/kyq-e2e-runner@" + strings.Fields(lines[1])[2]
	docker(t, "exec", node, "ctr", "-n", "k8s.io", "images", "tag", "--force", tagged, ref)
	docker(t, "exec", node, "crictl", "inspecti", ref) // kubelet resolves it without a registry
	t.Logf("test plumbing: runner image %s, built from this checkout", ref)
	return ref
}

// addCatalogYard installs the catalog KyYard behind the edge and selects its setup.
func addCatalogYard(st *stack.Stack, certID int) {
	st.Apps = append(st.Apps, stack.App{Name: "kyyard", Target: "k8s"})
	st.Edges[0].Routes = append(st.Edges[0].Routes, stack.Route{App: "kyyard", Hostname: catalogYardHost,
		ForwardHost: "app.kyq-kyyard.svc.cluster.local", ForwardPort: 9273, CertificateID: certID})
	st.Integrations = append(st.Integrations, stack.Integration{Product: "kyyard", Target: "k8s",
		Bundle: yardSetupDir + "/setup.json", IdentityClient: "kyyard"})
}

// yardClient returns HTTP clients, each with its own cookies, that reach KyIdentity and KyYard
// through the edge and trust its throwaway certificate.
func yardClient(t *testing.T, certPEM []byte, nodeIP string) func() *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("invalid edge certificate")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != catalogIdentityURLHost && address != catalogYardURLHost {
			return nil, errors.New("unselected destination " + address)
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(nodeIP, "30443"))
	}
	t.Cleanup(transport.CloseIdleConnections)
	return func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Transport: transport, Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
}

func send(t *testing.T, ctx context.Context, c *http.Client, method, endpoint string, body []byte, csrf string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	r, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, endpoint, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	return r, b
}

// checkCatalogYard proves KyYard after its first apply (AC9, AC10, AC11, AC13) and returns its
// client and directory secrets and break-glass password, read from its pod.
func checkCatalogYard(t *testing.T, ctx context.Context, cs kubernetes.Interface, c *kube.Client, kubeconfig, state, out string, client func() *http.Client) []string {
	t.Helper()
	if !strings.Contains(out, "ran without kyyard's client: adding it with setup amendment ") {
		t.Fatal("KyYard amendment not announced")
	}
	for _, id := range []string{"kyidentity.amend.kyyard", "kyyard.deploy", "kyyard.health", "kyyard.setup", yardRouteStep} {
		if status(out, id) != "ok" {
			t.Fatalf("%s = %q", id, status(out, id))
		}
	}
	kubectl := "kubectl --kubeconfig '" + kubeconfig + "' -n kyq-kyyard exec deployment/app -c app -- "
	for _, want := range []string{
		"KyYard at https://" + catalogYardURLHost + "/: sign in through KyIdentity as prepared-admin (administration only)",
		"accounts are not given KyYard access",
		kubectl + "cat /data/initial-admin-password",
		"change it. Only after that, delete the file:",
		"kyquickstart handoff --state '" + state + "' --kyyard-target k8s --runner-pod " + yardRunnerPod + "\n",
		"workstation apply is refused until kyquickstart handoff --revoke",
		"handoff --revoke, apply, then handoff again",
		"KyYard's Suite page shows the runner",
		"No catalog product can pair with KyRecovery in this release",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("KyYard handover lacks %q", want)
		}
	}
	ns, pod, container, err := c.AppPod(ctx, "kyyard")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); n.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Fatalf("namespace = %v", n.Labels)
	}
	p, err := cs.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil || len(p.Status.ContainerStatuses) != 2 {
		t.Fatalf("pod: %v", err)
	}
	for _, s := range p.Status.ContainerStatuses {
		if (s.Name != "app" && s.Name != "runner") || !s.Ready || s.RestartCount != 0 {
			t.Fatalf("container %s: ready %v, restarts %d", s.Name, s.Ready, s.RestartCount)
		}
	}
	for _, e := range p.Spec.Containers[0].Env {
		if e.Name == "KY_ADMIN_PASSWORD" || e.Name == "KY_AGENT_IMAGE" || e.Name == "KY_TRUSTED_PROXIES" && e.Value != "127.0.0.1" {
			t.Fatalf("KyYard runs with %s", e.Name)
		}
	}

	// Through the edge: readiness, and forwarded headers untrusted.
	r, _ := send(t, ctx, client(), "GET", "https://"+catalogYardURLHost+"/health/ready", nil, "")
	if r.StatusCode != 200 {
		t.Fatalf("KyYard readiness through the edge: %d", r.StatusCode)
	}
	r, b := send(t, ctx, client(), "GET", "https://"+catalogYardURLHost+"/api/installer/network", nil, "")
	var network struct {
		ProxyHeadersTrusted *bool `json:"proxyHeadersTrusted"`
	}
	if r.StatusCode != 200 || json.Unmarshal(b, &network) != nil || network.ProxyHeadersTrusted == nil || *network.ProxyHeadersTrusted {
		t.Fatalf("KyYard network report: %d %s", r.StatusCode, b)
	}

	// Private setup files, KyYard's own check, and the socket it shares with the runner.
	in := func(args ...string) string {
		t.Helper()
		b, err := c.Exec(ctx, ns, pod, container, args)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	d := yardSetupDir
	want := d + " 700 10001\n" + d + "/client.secret 600 10001\n" + d + "/directory.secret 600 10001\n" + d + "/setup.json 600 10001"
	if got := in("stat", "-c", "%n %a %u", d, d+"/client.secret", d+"/directory.secret", d+"/setup.json"); got != want {
		t.Fatalf("setup files:\n%s", got)
	}
	var report struct {
		Created, Pending []json.RawMessage
		Present          []struct{ Kind, Name string }
	}
	raw := in("/app/kyyard-server", "apply-setup", "--file", d+"/setup.json", "--check")
	var identities []string
	if json.Unmarshal([]byte(raw), &report) != nil || report.Created == nil || len(report.Created) != 0 || len(report.Pending) != 0 {
		t.Fatalf("KyYard check:\n%s", raw)
	}
	for _, o := range report.Present {
		if o.Kind == "identity" {
			identities = append(identities, o.Name)
		}
	}
	if len(identities) != 1 || identities[0] != "prepared-admin" {
		t.Fatalf("KyYard check names identities %v:\n%s", identities, raw)
	}
	// The directory may also carry setgid (2700), inherited from the fsGroup emptyDir's root:
	// access stays the owner's alone.
	want = "/run/kyquickstart/sock directory 700 10001\n/run/kyquickstart/sock/runner.sock socket 600 10001"
	if got := in("stat", "-c", "%n %F %a %u", "/run/kyquickstart/sock", "/run/kyquickstart/sock/runner.sock"); strings.Replace(got, " 2700 ", " 700 ", 1) != want {
		t.Fatalf("runner socket from KyYard's container:\n%s", got)
	}
	if got := in("printenv", "KY_SUITE_RUNNER_SOCKET"); got != "/run/kyquickstart/sock/runner.sock" {
		t.Fatalf("KY_SUITE_RUNNER_SOCKET = %q", got)
	}
	if got := in("sh", "-c", "test -e /state && echo present || echo absent"); got != "absent" {
		t.Fatal("KyYard's container sees /state")
	}
	// The runner's own view, from a test-only container in its process namespace.
	inspectRunner(t, ctx, cs, pod)
	if got, err := c.Exec(ctx, ns, pod, "inspect", []string{"sh", "-c", "test -d /proc/1/root/state/runner && test ! -e /proc/1/root/data && echo ok"}); err != nil || strings.TrimSpace(string(got)) != "ok" {
		t.Fatalf("runner container's /state/runner and /data: %v %s", err, got)
	}

	// AC13: the printed break-glass line, run unchanged with a pinned kubectl (test plumbing:
	// this host has no kubectl). The file is private to KyYard's user.
	argv := shellWords(t, kubectl+"cat /data/initial-admin-password")
	docker(t, "pull", "-q", kubectlImage)
	password := strings.TrimSpace(docker(t, append([]string{"run", "--rm", "--network", "host", "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		"-v", kubeconfig + ":" + kubeconfig + ":ro", "--entrypoint=", kubectlImage}, argv...)...))
	if len(password) < 12 {
		t.Fatal("break-glass password missing")
	}
	if got := in("stat", "-c", "%a %u", "/data/initial-admin-password"); got != "600 10001" {
		t.Fatalf("break-glass password file: %s", got)
	}
	secrets := []string{password}
	for _, f := range []string{d + "/client.secret", d + "/directory.secret"} {
		s := in("cat", f)
		if len(s) < 12 {
			t.Fatalf("%s is empty", f)
		}
		secrets = append(secrets, s)
	}
	return secrets
}

// shellWords splits a printed command line as sh would.
func shellWords(t *testing.T, line string) []string {
	t.Helper()
	b, err := exec.Command("sh", "-c", `eval "set -- $1"; for a; do printf '%s\0' "$a"; done`, "sh", line).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

// ephemeral adds a test-only container to KyYard's pod, restricted as its own, and waits for it.
func ephemeral(t *testing.T, ctx context.Context, cs kubernetes.Interface, pod string, e corev1.EphemeralContainer) {
	t.Helper()
	uid := int64(10001)
	e.SecurityContext = &corev1.SecurityContext{
		RunAsUser: &uid, RunAsGroup: &uid, RunAsNonRoot: ptr.To(true), AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
		Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	p, err := cs.CoreV1().Pods("kyq-kyyard").Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p.Spec.EphemeralContainers = append(p.Spec.EphemeralContainers, e)
	if _, err := cs.CoreV1().Pods("kyq-kyyard").UpdateEphemeralContainers(ctx, pod, p, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Second) {
		p, err := cs.CoreV1().Pods("kyq-kyyard").Get(ctx, pod, metav1.GetOptions{})
		if err == nil {
			for _, s := range p.Status.EphemeralContainerStatuses {
				if s.Name == e.Name && s.State.Running != nil {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("ephemeral container %s never ran", e.Name)
		}
	}
}

// inspectRunner adds the test-only container "inspect" in the runner's process namespace, so
// /proc/1/root is the runner container's filesystem.
func inspectRunner(t *testing.T, ctx context.Context, cs kubernetes.Interface, pod string) {
	t.Helper()
	ephemeral(t, ctx, cs, pod, corev1.EphemeralContainer{TargetContainerName: "runner", EphemeralContainerCommon: corev1.EphemeralContainerCommon{
		Name: "inspect", Image: curlImage, Command: []string{"sleep", "3600"},
	}})
}

// yardPodState is KyYard's pod's UID and its containers' restart counts.
func yardPodState(t *testing.T, ctx context.Context, cs kubernetes.Interface, pod string) string {
	t.Helper()
	p, err := cs.CoreV1().Pods("kyq-kyyard").Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := string(p.UID)
	for _, s := range p.Status.ContainerStatuses {
		out += " " + s.Name + "=" + strconv.Itoa(int(s.RestartCount))
	}
	return out
}

// runnerCredentials reads the runner's minted ServiceAccount token and SSH private key from its
// state volume, through the test-only container in its process namespace.
func runnerCredentials(t *testing.T, ctx context.Context, cs kubernetes.Interface, c *kube.Client, ns, pod string) []string {
	t.Helper()
	inspectRunner(t, ctx, cs, pod) // trustTestCA replaced the pod checkCatalogYard inspected
	read := func(f string) []byte {
		b, err := c.Exec(ctx, ns, pod, "inspect", []string{"cat", "/proc/1/root/state/runner/" + f})
		if err != nil || len(b) == 0 {
			t.Fatalf("runner %s: %v", f, err)
		}
		return b
	}
	cfg, err := clientcmd.Load(read("kubeconfigs/k8s.yaml"))
	if err != nil || len(cfg.AuthInfos) != 1 {
		t.Fatalf("runner kubeconfig: %v", err)
	}
	var out []string
	for _, a := range cfg.AuthInfos {
		if len(a.Token) < 32 {
			t.Fatal("runner kubeconfig has no token")
		}
		out = append(out, a.Token)
	}
	// The key's base64 body, line by line: any line of it is a leak.
	for _, l := range strings.Split(string(read("ssh/id_ed25519")), "\n") {
		if len(l) >= 32 && !strings.HasPrefix(l, "-----") {
			out = append(out, l)
		}
	}
	if len(out) < 2 {
		t.Fatal("runner SSH key unreadable")
	}
	return out
}

// yardSignOn signs one of the owner's accounts in to KyYard through KyIdentity behind the edge,
// as a browser would (AC12). The everyday account, not assigned to KyYard's client, is refused
// at KyIdentity's gate: no code, and no KyYard session or account. The admin becomes KyYard's
// administrator; its signed-in client is returned.
func yardSignOn(t *testing.T, ctx context.Context, c *kube.Client, client func() *http.Client, admin bool) *http.Client {
	t.Helper()
	identity, yard := "https://"+catalogIdentityURLHost, "https://"+catalogYardURLHost
	username, passwordFile := "prepared-user", "everyday.password"
	if admin {
		username, passwordFile = "prepared-admin", "admin.password"
	}
	ns, pod, container, err := c.AppPod(ctx, "kyidentity")
	if err != nil {
		t.Fatal(err)
	}
	password, err := c.Exec(ctx, ns, pod, container, []string{"cat", "/data/installer/" + identityFixturePreparation().SetupID + "/" + passwordFile})
	if err != nil || len(password) == 0 {
		t.Fatal("installer-created account credential missing", err)
	}
	hc := client()
	_, b := send(t, ctx, hc, "GET", identity+"/api/auth/csrf", nil, "")
	var csrf struct {
		CSRFToken string `json:"csrfToken"`
	}
	if json.Unmarshal(b, &csrf) != nil || csrf.CSRFToken == "" {
		t.Fatalf("KyIdentity CSRF: %s", b)
	}
	payload, _ := json.Marshal(map[string]string{"username": username, "password": strings.TrimSpace(string(password))})
	clear(password)
	r, b := send(t, ctx, hc, "POST", identity+"/api/auth/login", payload, csrf.CSRFToken)
	clear(payload)
	var login struct{ Success, MFARequired bool }
	if r.StatusCode != 200 || json.Unmarshal(b, &login) != nil || !login.Success || login.MFARequired {
		t.Fatalf("%s password sign-in: %d", username, r.StatusCode)
	}

	r, b = send(t, ctx, hc, "GET", yard+"/api/sso/kyidentity/login", nil, "")
	authorize, err := r.Location()
	if err != nil || r.StatusCode != 302 || authorize.Host != catalogIdentityURLHost {
		t.Fatalf("KyYard sign-in start: %d %s", r.StatusCode, b)
	}
	q := authorize.Query()
	if q.Get("client_id") != "kyyard" || q.Get("redirect_uri") != yard+"/api/sso/kyidentity/callback" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("authorization request: %s", authorize)
	}
	r, _ = send(t, ctx, hc, "GET", authorize.String(), nil, "")
	callback, _ := r.Location()
	if r.StatusCode != 302 || callback == nil || "https://"+callback.Host != yard || callback.Path != "/api/sso/kyidentity/callback" || callback.Query().Get("state") != q.Get("state") {
		t.Fatalf("KyIdentity authorization of %s for KyYard: %d %v", username, r.StatusCode, callback)
	}
	session := func() bool {
		u, _ := url.Parse(yard + "/")
		for _, ck := range hc.Jar.Cookies(u) {
			if ck.Name == "ky_session" && ck.Value != "" {
				return true
			}
		}
		return false
	}
	if !admin {
		// KyIdentity's per-app gate: no code, an access_denied error. KyYard answers the
		// callback before any code exchange or account lookup, so it creates nothing.
		if callback.Query().Get("error") != "access_denied" || callback.Query().Get("code") != "" {
			t.Fatalf("everyday account authorized for KyYard: %v", callback)
		}
		r, b = send(t, ctx, hc, "GET", callback.String(), nil, "")
		if r.StatusCode != 400 || session() {
			t.Fatalf("KyYard callback for the refused account: %d %s", r.StatusCode, b)
		}
		t.Logf("%s refused by KyIdentity for KyYard: callback error=access_denied, KyYard %d %s, no session", username, r.StatusCode, b)
		return nil
	}
	if callback.Query().Get("code") == "" {
		t.Fatalf("no code for %s: %v", username, callback)
	}
	r, b = send(t, ctx, hc, "GET", callback.String(), nil, "")
	if r.StatusCode != 302 || r.Header.Get("Location") != "/" || !session() {
		t.Fatalf("KyYard callback for %s: %d %s", username, r.StatusCode, b)
	}
	_, b = send(t, ctx, hc, "GET", yard+"/api/auth/me", nil, "")
	var me struct {
		Authenticated bool
		User          struct{ Username, Role string }
	}
	if json.Unmarshal(b, &me) != nil || !me.Authenticated || me.User.Username != username || me.User.Role != "admin" {
		t.Fatalf("KyYard /api/auth/me for %s: %s", username, b)
	}
	// Only the admin and the break-glass account exist: the everyday account got none.
	r, b = send(t, ctx, hc, "GET", yard+"/api/admin/users", nil, "")
	var users []struct{ Username string }
	if r.StatusCode != 200 || json.Unmarshal(b, &users) != nil || len(users) != 2 || users[0].Username != "admin" || users[1].Username != "prepared-admin" {
		t.Fatalf("KyYard users after sign-on: %d %s", r.StatusCode, b)
	}
	t.Logf("%s signed in to KyYard through KyIdentity as its administrator", username)
	return hc
}

func mustStack(t *testing.T, plan string) *stack.Stack {
	t.Helper()
	st, err := stack.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
