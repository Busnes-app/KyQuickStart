//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
)

// TestKubernetesNPMEdge deploys NPM from the catalog into kind, bootstraps it, routes the
// demo app through it as a cloudflare and then a lan edge, and reruns apply until nothing
// changes. KyIdentity, KyRecovery and kyPulse join behind the lan edge, so their URLs carry
// its client_port, which may not change once KyIdentity's setup ran. KyDrive joins from a
// Docker target, routed through the same edge, with its client in the same first setup.
func TestKubernetesNPMEdge(t *testing.T) {
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
	for _, src := range []string{"../../internal/catalog/testdata/apps/hello", "../../internal/catalog/apps/npm", "../../internal/catalog/apps/kyidentity", "../../internal/catalog/apps/kyrecovery", "../../internal/catalog/apps/kypulse", "../../internal/catalog/apps/kynotes", "../../internal/catalog/apps/kycalendar"} {
		if err := os.CopyFS(filepath.Join(catalogDir, filepath.Base(src)), os.DirFS(src)); err != nil {
			t.Fatal(err)
		}
	}
	plan := filepath.Join(state, "stack.yaml")
	os.WriteFile(plan, []byte("version: 1\ntargets:\n  - name: k8s\n    kubeconfig: "+kc+"\n    storage_class: standard\napps:\n  - name: npm\n    target: k8s\n  - name: hello\n    target: k8s\n"), 0o600)
	var outputs, trust []string
	apply := func(want string) string {
		t.Helper()
		var out bytes.Buffer
		err := cli.Run(ctx, append([]string{"apply", "--state", state}, trust...), cli.Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "e2e", Out: &out})
		t.Logf("apply:\n%s", out.String())
		if err != nil {
			t.Fatalf("apply (%s): %v", want, err)
		}
		outputs = append(outputs, out.String())
		return out.String()
	}

	out := apply("install")
	if status(out, "npm.bootstrap") != "ok" {
		t.Fatal("npm was not bootstrapped")
	}
	ns, err := cs.CoreV1().Namespaces().Get(ctx, "kyq-npm", metav1.GetOptions{})
	if err != nil || ns.Labels["pod-security.kubernetes.io/enforce"] != "baseline" || ns.Labels[managed.Label] != managed.Value {
		t.Fatalf("npm namespace = %v, %v", ns.Labels, err)
	}
	admin, _ := cs.CoreV1().Services("kyq-npm").Get(ctx, "app", metav1.GetOptions{})
	public, _ := cs.CoreV1().Services("kyq-npm").Get(ctx, "app-public", metav1.GetOptions{})
	if admin.Spec.Type != corev1.ServiceTypeClusterIP || len(admin.Spec.Ports) != 1 || admin.Spec.Ports[0].Port != 81 {
		t.Fatalf("admin service = %+v", admin.Spec)
	}
	if public.Spec.Type != corev1.ServiceTypeNodePort || len(public.Spec.Ports) != 2 || public.Spec.Ports[0].NodePort != 30080 || public.Spec.Ports[1].NodePort != 30443 {
		t.Fatalf("public service = %+v", public.Spec)
	}

	// Bootstrap: the stored token works, NPM is set up, the default login and none other works.
	connected, err := kube.Connect(kc, "")
	if err != nil {
		t.Fatal(err)
	}
	tunnel := connected.ServiceDialer("kyq-npm", "app", 81)
	npm, err := edge.NewAdmin("kube://kyq-npm/app:81", tunnel)
	if err != nil {
		t.Fatal(err)
	}
	defer npm.Close()
	secrets, _ := cs.CoreV1().Secrets("kyq-npm").Get(ctx, "kyq-secrets", metav1.GetOptions{})
	password := string(secrets.Data["admin_password"])
	stored := func() string {
		sec, err := cs.CoreV1().Secrets("kyq-edge").Get(ctx, "kyq-credentials", metav1.GetOptions{})
		if err != nil || sec.Labels[managed.Label] != managed.Value {
			t.Fatalf("edge credential: %v", err)
		}
		return string(sec.Data["npm_token"])
	}
	token := stored()
	if setup, err := npm.Setup(ctx); err != nil || !setup {
		t.Fatalf("NPM setup = %v, %v", setup, err)
	}
	if ok, err := npm.Verify(ctx, token); !ok || err != nil {
		t.Fatalf("stored token rejected: %v", err)
	}
	if def, err := npm.Login(ctx, edge.DefaultEmail, edge.DefaultPassword); def != "" || err != nil {
		t.Fatalf("default login accepted: %v", err)
	}
	if mine, err := npm.Login(ctx, "admin@kyquickstart.invalid", password); mine == "" || err != nil {
		t.Fatalf("installer login rejected: %v", err)
	}
	if err := npm.CreateAdmin(ctx, "intruder@example.test", "intruder-password"); err == nil {
		t.Fatal("NPM still accepts an unauthenticated administrator")
	}

	// The policy: ports 80/443 answer any pod, the admin API answers none.
	for url, want := range map[string]string{
		"http://app-public.kyq-npm.svc.cluster.local/": "200",
		"http://app.kyq-npm.svc.cluster.local:81/api/": "000",
	} {
		if got := probe(t, ctx, cs, url); got != want {
			t.Errorf("pod to %s: HTTP %s, want %s", url, got, want)
		}
	}

	// A throwaway certificate, uploaded through the tunnel.
	certID, certPEM := uploadCertificate(t, ctx, tunnel, token)
	ip := func(container string) string {
		return docker(t, "inspect", container, "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
	}
	nodeIP := ip(node)
	// A LAN client: a container on kind's network, from the node image already present.
	client := name + "-client"
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", client).Run() })
	docker(t, "run", "-d", "--name", client, "--network", "kind", "--entrypoint", "sleep", kindNode, "900")
	st, err := stack.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	// NodePort's Local policy keeps the client's address, which the route pins.
	st.Edges = []stack.Edge{{Target: "k8s", Provider: "cloudflare", NPMURL: "kube://kyq-npm/app:81", ConnectorCIDR: ip(client) + "/32", Routes: []stack.Route{{
		App: "hello", Hostname: "hello.example.test", ForwardHost: "app.kyq-hello.svc.cluster.local", ForwardPort: 8080, CertificateID: certID,
	}}}}
	b, _ := yaml.Marshal(st)
	os.WriteFile(plan, b, 0o600)
	apply("route")
	if st, err = stack.Load(plan); err != nil || st.InstallationID == "" || st.Revision != 1 {
		t.Fatalf("installation id not published: %+v %v", st, err)
	}
	get := func(from string) string {
		return docker(t, "exec", from, "curl", "-sk", "-m", "10", "--resolve", "hello.example.test:30443:"+nodeIP,
			"-H", "CF-Connecting-IP: 203.0.113.7", "-H", "X-Forwarded-For: 198.51.100.9", "https://hello.example.test:30443/")
	}
	page := get(client)
	if !strings.Contains(page, "X-Forwarded-For: 203.0.113.7") || !strings.Contains(page, "X-Forwarded-Proto: https") || strings.Contains(page, "198.51.100.9") {
		t.Fatalf("hello through the edge:\n%s", page)
	}
	if page := get(node); !strings.Contains(page, "403 Forbidden") {
		t.Fatalf("a source other than connector_cidr was served:\n%s", page)
	}
	if code := docker(t, "exec", node, "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "-m", "10", "http://"+nodeIP+":30080/"); code != "200" {
		t.Fatalf("NodePort 30080 answered %s", code)
	}

	// Rerun: nothing changes.
	rerun := func(name string) {
		t.Helper()
		out := apply(name)
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && strings.Contains(f[0], ".") && f[1] != "skipped" {
				t.Errorf("%s step not skipped: %s", name, line)
			}
		}
		if status(out, "npm.bootstrap") != "skipped" || status(out, "k8s.edge.removed") != "skipped" {
			t.Fatalf("%s did not report the bootstrap and edge steps", name)
		}
	}
	rerun("rerun")

	// lan: the client reaches NPM directly, only allow_cidrs is served, no forwarding header
	// is trusted. kind's network is inside the private ranges NPM's http block trusts.
	allowed := ip(client)
	st.Edges[0].Provider, st.Edges[0].ConnectorCIDR = "lan", ""
	st.Edges[0].AllowCIDRs, st.Edges[0].ClientPort = []string{allowed + "/32"}, 30443
	b, _ = yaml.Marshal(st)
	os.WriteFile(plan, b, 0o600)
	apply("lan")
	lan := func(from string, headers ...string) string {
		args := []string{"exec", from, "curl", "-sk", "-m", "10", "--resolve", "hello.example.test:30443:" + nodeIP}
		for _, h := range headers {
			args = append(args, "-H", h)
		}
		return docker(t, append(args, "https://hello.example.test:30443/")...)
	}
	header := func(page, name string) string {
		for _, line := range strings.Split(page, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), name+": "); ok {
				return v
			}
		}
		return ""
	}
	page = lan(client, "X-Forwarded-For: 198.51.100.9", "X-Real-IP: 198.51.100.10", "Forwarded: for=198.51.100.11")
	if header(page, "X-Forwarded-For") != allowed || header(page, "X-Real-Ip") != allowed || header(page, "X-Forwarded-Port") != "30443" || strings.Contains(page, "198.51.100") {
		t.Fatalf("lan client through the edge:\n%s", page)
	}
	// Outside allow_cidrs, with and without a forged allowed address.
	for _, forged := range []string{"X-Lan-Test: none", "X-Real-IP: " + allowed, "X-Forwarded-For: " + allowed} {
		if page := lan(node, forged); !strings.Contains(page, "403 Forbidden") {
			t.Fatalf("lan served a client outside allow_cidrs (%s):\n%s", forged, page)
		}
	}

	// A rejected token (expired or revoked): connect mints one in memory, bootstrap stores a fresh one.
	sec, _ := cs.CoreV1().Secrets("kyq-edge").Get(ctx, "kyq-credentials", metav1.GetOptions{})
	sec.Data["npm_token"] = []byte("rejected-npm-token")
	cs.CoreV1().Secrets("kyq-edge").Update(ctx, sec, metav1.UpdateOptions{})
	out = apply("refresh")
	if status(out, "npm.bootstrap") != "ok" || status(out, "hello.deploy") != "skipped" {
		t.Fatal("rejected token not refreshed by bootstrap alone")
	}
	if ok, err := npm.Verify(ctx, stored()); !ok || err != nil || stored() == token {
		t.Fatalf("refreshed token = %v %v", ok, err)
	}
	if status(apply("after refresh"), "npm.bootstrap") != "skipped" {
		t.Fatal("bootstrap not idempotent after refresh")
	}

	// The catalog KyIdentity takes its issuer from its route, client_port included, so it
	// joins behind the lan edge it keeps; KyRecovery's and kyPulse's clients join KyIdentity's
	// first setup, and kyPulse watches every other routed app.
	loadCatalogApp(t, name, cs, "kyidentity")
	loadCatalogApp(t, name, cs, "kyrecovery")
	loadCatalogApp(t, name, cs, "kypulse")
	drive := startDriveTarget(t, work, catalogDir, nodeIP, certPEM)
	addCatalogIdentity(st, certID)
	addCatalogRecovery(st, certID)
	addCatalogPulse(st, certID)
	addCatalogDrive(st, certID, drive)
	trust = []string{"--trust-host-key", "nas=" + drive.fp}
	// KyIdentity's SCIM connector reaches KyDrive through the edge from its pod, as on K80:
	// test-only cluster DNS names every routed host after the node, and the edge admits the
	// cluster's pods, whose requests to KyIdentity go through it like a browser's.
	testClusterDNS(t, ctx, cs, nodeIP)
	podCIDR := mustNode(t, ctx, cs, node).Spec.PodCIDR
	st.Edges[0].AllowCIDRs = append(st.Edges[0].AllowCIDRs, podCIDR)
	b, _ = yaml.Marshal(st)
	os.WriteFile(plan, b, 0o600)
	out = apply("identity")
	viaEdge := func(host, path string) string {
		return docker(t, "exec", client, "curl", "-sk", "-m", "10", "--resolve", host+":30443:"+nodeIP, "https://"+host+":30443"+path)
	}
	checkCatalogIdentity(t, ctx, cs, kc, out, viaEdge)
	recoverySecrets := checkCatalogRecovery(t, ctx, cs, kc, out, viaEdge)
	pulseSecrets := checkCatalogPulse(t, ctx, cs, kc, out, viaEdge)
	driveSecrets := checkCatalogDrive(t, out, drive, viaEdge)
	checkDriveConnector(t, ctx, connected, out, driveSecrets[1])
	// Test-only: KyIdentity trusts the edge's throwaway certificate. Its deliveries so far
	// failed and are retried with backoff.
	trustTestCA(t, ctx, cs, connected, "kyidentity", certPEM)
	rerun("identity rerun")

	// Changed setup intent: the product refuses it at kyidentity.setup. Restored, the
	// read-only check passes again, so the refusal changed nothing.
	saved, _ := os.ReadFile(plan)
	changed, _ := stack.Load(plan)
	changed.Integrations[0].Identity.Clients[0].Name = "Changed intent"
	b, _ = yaml.Marshal(changed)
	os.WriteFile(plan, b, 0o600)
	var refused bytes.Buffer
	err = cli.Run(ctx, []string{"apply", "--state", state}, cli.Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "e2e", Out: &refused})
	if err == nil || status(refused.String(), "kyidentity.setup") != "failed" {
		t.Fatalf("changed KyIdentity intent: %v\n%s", err, refused.String())
	}
	outputs = append(outputs, refused.String())
	os.WriteFile(plan, saved, 0o600)
	if out := apply("intent restored"); status(out, "kyidentity.setup") != "ok" || status(out, "kyidentity.deploy") != "skipped" {
		t.Fatal("restored KyIdentity intent did not check clean")
	}

	// A client_port change would move the issuer of a set-up KyIdentity: refused before
	// kyidentity.deploy, so its Deployment and pod keep the issuer every client knows.
	issuer := func() (string, string) {
		dep, err := cs.AppsV1().Deployments("kyq-kyidentity").Get(ctx, "app", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, pod, _, err := connected.AppPod(ctx, "kyidentity")
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
			if e.Name == "KYIDENTITY_ISSUER_URL" {
				return e.Value, pod
			}
		}
		return "", pod
	}
	issuerBefore, podBefore := issuer()
	moved, _ := stack.Load(plan)
	moved.Edges[0].ClientPort = 30444
	b, _ = yaml.Marshal(moved)
	os.WriteFile(plan, b, 0o600)
	refused.Reset()
	err = cli.Run(ctx, []string{"apply", "--state", state}, cli.Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "e2e", Out: &refused})
	var se *suite.Error
	if !errors.As(err, &se) || se.Code != suite.IssuerChanged || !strings.Contains(err.Error(), "from https://"+catalogIdentityURLHost+" to https://"+catalogIdentityHost+":30444") || status(refused.String(), "kyidentity.deploy") != "" {
		t.Fatalf("client_port change after setup: %v\n%s", err, refused.String())
	}
	outputs = append(outputs, refused.String())
	if after, pod := issuer(); after != issuerBefore || after != "https://"+catalogIdentityURLHost || pod != podBefore {
		t.Fatalf("refused client_port change still moved KyIdentity: issuer %s, pod %s (was %s)", after, pod, podBefore)
	}
	os.WriteFile(plan, saved, 0o600)

	// Sign-on: the lan edge also admits this host (the network's gateway).
	st, _ = stack.Load(plan)
	for _, gw := range strings.Fields(docker(t, "network", "inspect", "kind", "--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}")) {
		if strings.Contains(gw, ".") {
			st.Edges[0].AllowCIDRs = append(st.Edges[0].AllowCIDRs, gw+"/32")
		}
	}
	// KyDrive, on the Docker target, reaches KyIdentity through the edge from its relay.
	st.Edges[0].AllowCIDRs = append(st.Edges[0].AllowCIDRs, drive.relayIP(t)+"/32")
	b, _ = yaml.Marshal(st)
	os.WriteFile(plan, b, 0o600)
	if out := apply("sign-on edge"); status(out, "kyrecovery.setup") != "skipped" || status(out, "kypulse.setup") != "skipped" || status(out, "kydrive.setup") != "skipped" || status(out, "kyidentity.scim.kydrive") != "skipped" {
		t.Fatal("edge change re-ran an app's setup")
	}
	catalogSignOn(t, ctx, cs, kc, certPEM, nodeIP)
	driveSignOn(t, ctx, kc, certPEM, nodeIP, driveSecrets[1])

	// KyNotes joins after KyIdentity's first setup: its client comes in a setup amendment, and
	// its setup runs after KyIdentity's route and before its own.
	loadCatalogApp(t, name, cs, "kynotes")
	st, _ = stack.Load(plan)
	addCatalogNotes(st, certID)
	b, _ = yaml.Marshal(st)
	os.WriteFile(plan, b, 0o600)
	var first bytes.Buffer
	err = cli.Run(ctx, []string{"apply", "--state", state}, cli.Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "e2e", Out: &first})
	t.Logf("apply (notes):\n%s", first.String())
	outputs = append(outputs, first.String())
	out = first.String()
	if !strings.Contains(out, "ran without kynotes's client: adding it with setup amendment ") {
		t.Fatal("amendment not announced")
	}
	for _, id := range []string{"kyidentity.amend.kynotes", "kynotes.deploy", "kynotes.health"} {
		if status(out, id) != "ok" {
			t.Fatalf("%s = %q", id, status(out, id))
		}
	}
	// code is the HTTP status the LAN client gets through the edge; curl fails (000) when
	// NPM serves no host for the name.
	code := func(host, path string) string {
		b, _ := exec.Command("docker", "exec", client, "curl", "-sk", "-o", "/dev/null", "-w", "%{http_code}", "-m", "10", "--resolve", host+":30443:"+nodeIP, "https://"+host+":30443"+path).Output()
		return strings.TrimSpace(string(b))
	}
	if err != nil {
		// Test-only: KyNotes' issuer probe refuses the edge's throwaway certificate. Apply
		// stopped before KyNotes' route, so its open first-run setup was never served.
		if status(out, "kynotes.setup") != "failed" || status(out, notesRouteStep) != "" || code(catalogNotesHost, "/healthz") == "200" {
			t.Fatalf("failed KyNotes setup: %v", err)
		}
		trustTestCA(t, ctx, cs, connected, "kynotes", certPEM)
		out = apply("notes after test CA")
	}
	notesSecrets := checkCatalogNotes(t, ctx, cs, connected, out, code)
	if _, err := cs.CoreV1().ConfigMaps("kyq-kynotes").Get(ctx, "test-ca", metav1.GetOptions{}); err != nil {
		trustTestCA(t, ctx, cs, connected, "kynotes", certPEM) // its token exchange goes through the edge too
	}
	notesSignOn(t, ctx, connected, certPEM, nodeIP)
	out = apply("notes rerun")
	for _, id := range []string{"kyidentity.amend.kynotes", "kynotes.deploy", "kynotes.health", "kynotes.setup", notesRouteStep} {
		if status(out, id) != "skipped" {
			t.Errorf("rerun: %s = %q", id, status(out, id))
		}
	}

	// KyCalendar joins after KyIdentity's first setup too, by setup amendment. Its setup needs
	// no issuer probe, so it runs before the edges as kyPulse's does; its everyday account's
	// subject comes from KyIdentity's read-only check of its first setup.
	loadCatalogApp(t, name, cs, "kycalendar")
	st, _ = stack.Load(plan)
	addCatalogCalendar(st, certID)
	b, _ = yaml.Marshal(st)
	os.WriteFile(plan, b, 0o600)
	out = apply("calendar")
	calendarSecrets := checkCatalogCalendar(t, ctx, cs, connected, out, code)
	// Test-only: KyCalendar's discovery and token exchange at sign-in go through the edge
	// and its throwaway certificate.
	trustTestCA(t, ctx, cs, connected, "kycalendar", certPEM)
	calendarSignOn(t, ctx, connected, certPEM, nodeIP)
	out = apply("calendar rerun")
	for _, id := range []string{"kyidentity.amend.kycalendar", "kycalendar.deploy", "kycalendar.health", "kycalendar.setup", calendarRouteStep} {
		if status(out, id) != "skipped" {
			t.Errorf("rerun: %s = %q", id, status(out, id))
		}
	}

	// No credential reached apply output or workstation state.
	secretsSeen := append(append(append(append(append([]string{password, token, stored()}, recoverySecrets...), pulseSecrets...), driveSecrets...), notesSecrets...), calendarSecrets...)
	for _, o := range outputs {
		for _, s := range secretsSeen {
			if strings.Contains(o, s) {
				t.Error("credential printed")
			}
		}
	}
	filepath.WalkDir(state, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			for _, s := range secretsSeen {
				if bytes.Contains(b, []byte(s)) {
					t.Errorf("credential stored in %s", p)
				}
			}
		}
		return nil
	})
}

// probe returns the HTTP status a pod in an unmanaged namespace gets from url ("000": no answer).
func probe(t *testing.T, ctx context.Context, cs kubernetes.Interface, url string) string {
	t.Helper()
	ns := "e2e-probe"
	if _, err := cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); err != nil {
		cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
	}
	dep, err := cs.AppsV1().Deployments("kyq-npm").Get(ctx, "app", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	limits := corev1.ResourceList{corev1.ResourceCPU: dep.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU], corev1.ResourceMemory: dep.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory]}
	pod, err := cs.CoreV1().Pods(ns).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "probe-"},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false), Containers: []corev1.Container{{
			// The NPM image is already on the node and carries curl.
			Name: "probe", Image: dep.Spec.Template.Spec.Containers[0].Image, Command: []string{"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "-m", "5", url},
			Resources: corev1.ResourceRequirements{Limits: limits, Requests: limits},
		}}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		p, err := cs.CoreV1().Pods(ns).Get(ctx, pod.Name, metav1.GetOptions{})
		if err == nil && (p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe %s did not finish", url)
		}
		time.Sleep(2 * time.Second)
	}
	logs, err := cs.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{}).DoRaw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cs.CoreV1().Pods(ns).Delete(ctx, pod.Name, metav1.DeleteOptions{})
	return strings.TrimSpace(string(logs))
}

// uploadCertificate adds a self-signed *.example.test certificate through NPM's API and
// returns its ID and PEM.
func uploadCertificate(t *testing.T, ctx context.Context, tunnel edge.Dial, token string) (int, []byte) {
	t.Helper()
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: tunnel}}
	defer client.CloseIdleConnections()
	request := func(method, path, contentType string, body *bytes.Buffer, result any) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, "http://app.kyq-npm:81"+path, body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("NPM %s %s: %v", method, path, err)
		}
		defer res.Body.Close()
		if res.StatusCode/100 != 2 {
			t.Fatalf("NPM %s %s: HTTP %d", method, path, res.StatusCode)
		}
		if result != nil {
			json.NewDecoder(res.Body).Decode(result)
		}
	}
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "*.example.test"}, DNSNames: []string{"*.example.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		ID int `json:"id"`
	}
	request("POST", "/api/nginx/certificates", "application/json", bytes.NewBufferString(`{"provider":"other","nice_name":"disposable kind edge test"}`), &created)
	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	for field, data := range map[string][]byte{
		"certificate":     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		"certificate_key": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
	} {
		part, _ := form.CreateFormFile(field, field+".pem")
		part.Write(data)
	}
	form.Close()
	request("POST", fmt.Sprintf("/api/nginx/certificates/%d/upload", created.ID), form.FormDataContentType(), &upload, nil)
	return created.ID, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func mustNode(t *testing.T, ctx context.Context, cs kubernetes.Interface, name string) *corev1.Node {
	t.Helper()
	n, err := cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil || n.Spec.PodCIDR == "" {
		t.Fatalf("node %s: %v", name, err)
	}
	return n
}
