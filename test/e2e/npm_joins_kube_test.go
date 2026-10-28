//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// TestKubernetesNPMEdgeJoins is TestKubernetesNPMEdge's later half, split off to run in its own
// CI shard. On the smallest stack the joins need, KyIdentity and KyRecovery behind the lan edge
// with the edge already admitting this host, the cluster's pods and the Docker target's relay,
// KyNotes, KyCalendar and KyPost and KyDNS (on the Docker target) join by setup amendment.
func TestKubernetesNPMEdgeJoins(t *testing.T) {
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
	for _, src := range []string{"../../internal/catalog/testdata/apps/hello", "../../internal/catalog/apps/npm", "../../internal/catalog/apps/kyidentity", "../../internal/catalog/apps/kyrecovery", "../../internal/catalog/apps/kynotes", "../../internal/catalog/apps/kycalendar"} {
		if err := os.CopyFS(filepath.Join(catalogDir, filepath.Base(src)), os.DirFS(src)); err != nil {
			t.Fatal(err)
		}
		relaysOff(t, filepath.Join(catalogDir, filepath.Base(src)))
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
	if out := apply("install"); status(out, "npm.bootstrap") != "ok" {
		t.Fatal("npm was not bootstrapped")
	}
	connected, err := kube.Connect(kc, "")
	if err != nil {
		t.Fatal(err)
	}
	secrets, _ := cs.CoreV1().Secrets("kyq-npm").Get(ctx, "kyq-secrets", metav1.GetOptions{})
	password := string(secrets.Data["admin_password"])
	stored := func() string {
		sec, err := cs.CoreV1().Secrets("kyq-edge").Get(ctx, "kyq-credentials", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("edge credential: %v", err)
		}
		return string(sec.Data["npm_token"])
	}
	token := stored()
	certID, certPEM := uploadCertificate(t, ctx, connected.ServiceDialer("kyq-npm", "app", 81), token)
	ip := func(container string) string {
		return docker(t, "inspect", container, "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
	}
	nodeIP := ip(node)
	client := name + "-client"
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", client).Run() })
	docker(t, "run", "-d", "--name", client, "--network", "kind", "--entrypoint", "sleep", kindNode, "900")

	// The lan edge as TestKubernetesNPMEdge leaves it before KyNotes joins: client_port 30443
	// and allow_cidrs for the LAN client, the cluster's pods, this host and the target's relay.
	loadCatalogApp(t, name, cs, "kyidentity")
	loadCatalogApp(t, name, cs, "kyrecovery")
	drive := startDriveTarget(t, work, catalogDir, nodeIP, certPEM)
	testClusterDNS(t, ctx, cs, nodeIP)
	allow := []string{ip(client) + "/32", mustNode(t, ctx, cs, node).Spec.PodCIDR}
	for _, gw := range strings.Fields(docker(t, "network", "inspect", "kind", "--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}")) {
		if strings.Contains(gw, ".") {
			allow = append(allow, gw+"/32")
		}
	}
	allow = append(allow, drive.relayIP(t)+"/32")
	st, err := stack.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	st.Edges = []stack.Edge{{Target: "k8s", Provider: "lan", NPMURL: "kube://kyq-npm/app:81", AllowCIDRs: allow, ClientPort: 30443, Routes: []stack.Route{{
		App: "hello", Hostname: "hello.example.test", ForwardHost: "app.kyq-hello.svc.cluster.local", ForwardPort: 8080, CertificateID: certID,
	}}}}
	addCatalogIdentity(st, certID)
	addCatalogRecovery(st, certID)
	b, _ := yaml.Marshal(st)
	os.WriteFile(plan, b, 0o600)
	out := apply("identity")
	viaEdge := func(host, path string) string {
		return docker(t, "exec", client, "curl", "-sk", "-m", "10", "--resolve", host+":30443:"+nodeIP, "https://"+host+":30443"+path)
	}
	recoverySecrets := checkCatalogRecovery(t, ctx, cs, kc, out, viaEdge)
	trustTestCA(t, ctx, cs, connected, "kyidentity", certPEM)

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
	calendarSecrets := checkCatalogCalendar(t, ctx, cs, connected, kc, out, code)
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

	// KyPost joins the Docker target beside KyDrive after KyIdentity's first setup, by setup
	// amendment; its setup runs before the edges. The handover's commands run verbatim there.
	postPort := preparePost(t, work, catalogDir, drive)
	st, _ = stack.Load(plan)
	addNASTarget(st, drive)
	trust = []string{"--trust-host-key", "nas=" + drive.fp}
	addCatalogPost(st, certID, drive, postPort)
	b, _ = yaml.Marshal(st)
	os.WriteFile(plan, b, 0o600)
	out = apply("post")
	postSecrets := checkCatalogPost(t, out, drive, postPort, viaEdge, code)
	postAdmin := postSignOn(t, ctx, connected, certPEM, nodeIP)
	postSecrets = append(postSecrets, checkPostHandover(t, ctx, out, drive, postAdmin, code))
	out = apply("post rerun")
	for _, id := range []string{"kyidentity.amend.kypost", "kypost.deploy", "kypost.health", "kypost.setup", postRouteStep} {
		if status(out, id) != "skipped" {
			t.Errorf("rerun: %s = %q", id, status(out, id))
		}
	}

	// KyDNS joins on the Docker target by setup amendment too, its admin UI behind the edge and
	// DNS on the target's forward address. Its edge's egress_address is the kind node's address,
	// where pods leave kind. kydns.gate proves this host, not the edge, gets 403 with a forged
	// X-Forwarded-Proto; kydns.edge proves NPM's pod gets KyDNS's trust, or fails apply.
	dnsPort := freePort(t, drive.forward)
	copyCatalogDNS(t, catalogDir, filepath.Join(work, "drive-ca.pem"), drive)
	st, _ = stack.Load(plan)
	addCatalogDNS(st, certID, drive, dnsPort)
	st.Edges[0].EgressAddress = nodeIP
	b, _ = yaml.Marshal(st)
	os.WriteFile(plan, b, 0o600)
	try := func(label string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		err := cli.Run(ctx, append([]string{"apply", "--state", state}, trust...), cli.Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "e2e", Out: &out})
		t.Logf("apply (%s): %v\n%s", label, err, out.String())
		outputs = append(outputs, out.String())
		return out.String(), err
	}
	out, err = try("dns")
	dnsSecrets := checkCatalogDNS(t, out, drive, dnsPort, err == nil)
	checkDNSGate(t, ctx, drive, dnsPort)
	_, newClient := edgeClient(t, certPEM, nodeIP)
	trusted := err == nil
	if trusted {
		if status(out, "kydns.edge") != "ok" {
			t.Fatalf("kydns.edge = %q", status(out, "kydns.edge"))
		}
		checkDNSEdge(t, ctx, newClient)
		dnsSignOn(t, ctx, connected, newClient)
	} else {
		// This Docker host hides client addresses (the userland proxy hands KyDNS every
		// connection from its network's gateway), so KyDNS refuses the edge and kydns.edge fails
		// apply after every other step passed: fail closed. The edge-trusted path is unproven
		// here. Trusting that gateway would open the gate to every peer, and kydns.gate refuses it.
		if status(out, "kydns.edge") != "failed" || !strings.Contains(err.Error(), "KyDNS does not trust edge k8s at egress_address "+nodeIP) {
			t.Fatalf("dns apply: %v", err)
		}
		checkDNSFailClosed(t, ctx, newClient)
		gw := strings.Fields(docker(t, "network", "inspect", "kyq-kydns_default", "--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}"))[0]
		t.Logf("UNPROVEN on this host: KyDNS sees the edge as %s, not %s; kydns.edge fails apply, edge-trusted checks and sign-in skipped", gw, nodeIP)
		st, _ = stack.Load(plan)
		st.Edges[0].EgressAddress = gw
		b, _ = yaml.Marshal(st)
		os.WriteFile(plan, b, 0o600)
		refused, err := try("dns gateway egress_address")
		if err == nil || status(refused, "kydns.gate") != "failed" || !strings.Contains(err.Error(), "egress_address "+gw+" on edge k8s is not the edge's alone") || status(refused, "kydns.edge") != "" {
			t.Fatalf("gateway egress_address: %v", err)
		}
		st, _ = stack.Load(plan)
		st.Edges[0].EgressAddress = nodeIP
		b, _ = yaml.Marshal(st)
		os.WriteFile(plan, b, 0o600)
		if out, err := try("dns egress_address restored"); err == nil || status(out, "kydns.deploy") != "ok" || status(out, "kydns.gate") != "ok" || status(out, "kydns.edge") != "failed" {
			t.Fatalf("restored egress_address: %v", err)
		}
		checkDNSGate(t, ctx, drive, dnsPort)
	}
	out, err = try("dns rerun")
	for _, id := range []string{"kyidentity.amend.kydns", "kydns.deploy", "kydns.health", "kydns.setup", "kydns.gate", dnsRouteStep} {
		if status(out, id) != "skipped" {
			t.Errorf("rerun: %s = %q", id, status(out, id))
		}
	}
	if want := map[bool]string{true: "skipped", false: "failed"}[trusted]; status(out, "kydns.edge") != want || (err == nil) != trusted {
		t.Errorf("rerun: kydns.edge = %q, %v", status(out, "kydns.edge"), err)
	}

	noCredentialLeak(t, state, outputs, slices.Concat([]string{password, token, stored()}, recoverySecrets, notesSecrets, calendarSecrets, postSecrets, dnsSecrets))
}
