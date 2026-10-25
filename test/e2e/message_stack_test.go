//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"golang.org/x/crypto/ssh"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// The stack's hostnames, all under the edge certificate's *.example.test; the server name is
// home.example.test.
var messageHosts = map[string]string{"": "message.example.test", "server_name": "home.example.test", "matrix": "matrix.example.test",
	"auth": "auth.example.test", "chat": "chat.example.test", "rtc": "rtc.example.test"}

const messageSetupDir = "/app/data/installer/5b8c9d0e-1f2a-4b3c-8d4e-5f6a7b8c9d0e"

// messageTarget is the Docker target KyMessages runs on: an sshd container in the host's
// network namespace (so the addresses it sees are the Docker host's, which media_address must
// be one of), on the host's Docker socket, whose root the host shares.
type messageTarget struct {
	name, port, root, fp string
	forward              string // the kind network's IPv4 gateway: this host, as kind reaches it
	ports                map[string]int
	relay                string
}

// startMessageTarget starts the target and copies the catalog KyMessages into catalogDir with
// test-only plumbing: identity.example.test through a relay to the lan edge, and the edge's
// throwaway certificate trusted by the console and MAS, which both call KyIdentity.
func startMessageTarget(t *testing.T, work, catalogDir, nodeIP string, certPEM []byte) messageTarget {
	t.Helper()
	keys := filepath.Join(work, "message-keys")
	os.Mkdir(keys, 0o700)
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(hostPriv, "")
	os.WriteFile(filepath.Join(keys, "host_ed25519"), pem.EncodeToMemory(block), 0o600)
	hostSigner, _ := ssh.NewSignerFromKey(hostPriv)
	_, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	clientSigner, _ := ssh.NewSignerFromKey(clientPriv)
	os.WriteFile(filepath.Join(keys, "authorized_keys"), ssh.MarshalAuthorizedKey(clientSigner.PublicKey()), 0o600)
	serveAgent(t, work, clientPriv)

	id := make([]byte, 4)
	rand.Read(id)
	name := "kyq-e2e-message-" + hex.EncodeToString(id)
	docker(t, "build", "-q", "-t", "kyq-e2e-sshd", "sshd")
	base := "/tmp/" + name
	docker(t, "run", "--rm", "--memory", "64m", "--memory-swap", "64m", "--cpus", "1", "--pids-limit", "32", "-v", "/tmp:/tmp", "--entrypoint", "mkdir", "kyq-e2e-sshd", "-m", "755", base)
	t.Cleanup(func() {
		exec.Command("docker", "run", "--rm", "--memory", "64m", "--memory-swap", "64m", "--cpus", "1", "--pids-limit", "32", "-v", "/tmp:/tmp", "--entrypoint", "rm", "kyq-e2e-sshd", "-rf", base).Run()
	})
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	docker(t, "run", "-d", "--name", name, "--network", "host", "--memory", "256m", "--memory-swap", "256m", "--cpus", "1", "--pids-limit", "128",
		"-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", keys+":/keys:ro", "-v", base+":"+base, "-v", work+":"+work, "kyq-e2e-sshd",
		"-o", "Port="+port, "-o", "ListenAddress=127.0.0.1")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	// The SSH user is kyq, not root: matrix-init refuses root, and every service runs as the
	// SSH user. It reaches the host's Docker socket through its group.
	docker(t, "exec", name, "sh", "-c", `set -e
g=$(stat -c %g /var/run/docker.sock)
getent group "$g" >/dev/null || addgroup -g "$g" hostdocker
adduser -D -u 4711 -G "$(getent group "$g" | cut -d: -f1)" kyq
sed -i 's/^kyq:!/kyq:*/' /etc/shadow
install -d -o kyq -m 700 /home/kyq/.ssh
install -o kyq -m 600 /keys/authorized_keys /home/kyq/.ssh/authorized_keys
chown kyq `+base)
	t.Cleanup(func() {
		if t.Failed() {
			for _, svc := range []string{"app", "mas", "synapse", "postgres", "element", "livekit"} {
				logs, _ := exec.Command("docker", "logs", "--tail", "60", "kyq-kymessage-"+svc+"-1").CombinedOutput()
				t.Logf("kymessage %s logs:\n%s", svc, logs)
			}
		}
		exec.Command("docker", "compose", "-p", "kyq-kymessage", "down").Run()
	})
	fp := ssh.FingerprintSHA256(hostSigner.PublicKey())
	waitForSSH(t, port, fp)

	var gateway string
	for _, gw := range strings.Fields(docker(t, "network", "inspect", "kind", "--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}")) {
		if strings.Contains(gw, ".") {
			gateway = gw
		}
	}
	ports := map[string]int{}
	for _, role := range []string{"", "matrix", "auth", "chat", "rtc"} {
		l, err := net.Listen("tcp", net.JoinHostPort(gateway, "0"))
		if err != nil {
			t.Fatal(err)
		}
		ports[role] = l.Addr().(*net.TCPAddr).Port
		l.Close()
	}
	ports["server_name"] = ports[""]
	conf := filepath.Join(work, "message-relay.conf")
	os.WriteFile(conf, []byte("events {}\nstream {\n  server {\n    listen 30443;\n    proxy_pass "+nodeIP+":30443;\n  }\n}\n"), 0o644)
	relay := name + "-relay"
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", relay).Run() })
	docker(t, "run", "-d", "--name", relay, "--network", "kind", "--memory", "64m", "--memory-swap", "64m", "-p", gateway+":30443:30443", "-v", conf+":/etc/nginx/nginx.conf:ro", relayImage)

	ca := filepath.Join(work, "message-ca.pem")
	os.WriteFile(ca, certPEM, 0o644)
	dir := filepath.Join(catalogDir, "kymessage")
	if err := os.CopyFS(dir, os.DirFS("../../internal/catalog/apps/kymessage")); err != nil {
		t.Fatal(err)
	}
	extra := "    extra_hosts: [\"" + catalogIdentityHost + ":" + gateway + "\"]\n"
	patch(t, filepath.Join(dir, "compose.yaml.tmpl"), [][2]string{
		{`    ports: ["{{forward}}:8080"]` + "\n", `    ports: ["{{forward}}:8080"]` + "\n" + extra},
		{"      GOMEMLIMIT: 384MiB\n", "      GOMEMLIMIT: 384MiB\n      SSL_CERT_FILE: /test-ca/ca.pem\n"},
		{"      - ./media:/matrix-media:ro\n", "      - ./media:/matrix-media:ro\n      - " + ca + ":/test-ca/ca.pem:ro\n"},
		{`    ports: ["{{forward "auth"}}:8080"]` + "\n", `    ports: ["{{forward "auth"}}:8080"]` + "\n" + extra + "    environment: {SSL_CERT_FILE: /test-ca/ca.pem}\n"},
		{"    volumes: [./matrix/mas/config.yaml:/config/config.yaml:ro]\n", "    volumes: [./matrix/mas/config.yaml:/config/config.yaml:ro, \"" + ca + ":/test-ca/ca.pem:ro\"]\n"},
	})
	return messageTarget{name: name, port: port, root: base + "/root", fp: fp, forward: gateway, ports: ports, relay: relay}
}

// wildcardListeners is the target's TCP and UDP sockets listening on every address, from
// ss -ltnu on the target (the host's network namespace): "<netid> <address:port>".
func wildcardListeners(t *testing.T, sshd string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, l := range strings.Split(docker(t, "exec", sshd, "ss", "-Hltnu"), "\n") {
		f := strings.Fields(l)
		if len(f) >= 5 && (strings.HasPrefix(f[4], "0.0.0.0:") || strings.HasPrefix(f[4], "*:") || strings.HasPrefix(f[4], "[::]:")) {
			out[f[0]+" "+f[4]] = true
		}
	}
	return out
}

func patch(t *testing.T, file string, edits [][2]string) {
	t.Helper()
	b, _ := os.ReadFile(file)
	s := string(b)
	for _, e := range edits {
		if strings.Count(s, e[0]) != 1 {
			t.Fatalf("%s changed; test plumbing for %q", file, e[0])
		}
		s = strings.Replace(s, e[0], e[1], 1)
	}
	os.WriteFile(file, []byte(s), 0o600)
}

func addCatalogMessage(st *stack.Stack, certID int, d messageTarget) {
	port, _ := strconv.Atoi(d.port)
	st.Targets = append(st.Targets, stack.Target{Name: "nas", Host: "127.0.0.1", Port: port, User: "kyq", Root: d.root})
	st.Apps = append(st.Apps, stack.App{Name: "kymessage", Target: "nas", MediaAddress: d.forward})
	for _, role := range []string{"", "server_name", "matrix", "auth", "chat", "rtc"} {
		st.Edges[0].Routes = append(st.Edges[0].Routes, stack.Route{App: "kymessage", Role: role, Hostname: messageHosts[role],
			ForwardHost: d.forward, ForwardPort: d.ports[role], CertificateID: certID, StatusPath: "/api/installer/network"})
	}
	st.Integrations = append(st.Integrations, stack.Integration{Product: "kymessage", Target: "nas",
		Bundle: messageSetupDir + "/setup.json", IdentityClient: "kymessage", ChatClient: "kymessage-chat"})
}

// messageTransport dials the stack's hostnames and KyIdentity at the lan edge's NodePort.
func messageTransport(t *testing.T, certPEM []byte, nodeIP string) *http.Transport {
	t.Helper()
	transport := driveTransport(t, certPEM, nodeIP)
	allowed := []string{catalogIdentityURLHost}
	for _, h := range messageHosts {
		allowed = append(allowed, h+":30443", h+":443")
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if !slices.Contains(allowed, address) {
			return nil, errors.New("unselected destination " + address)
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(nodeIP, "30443"))
	}
	return transport
}

// TestKubernetesKyMessage installs the whole KyMessages stack on a Docker target behind the
// kind cluster's lan edge, after KyIdentity's first setup (a setup amendment), and proves its
// routes and closed surfaces, its hardening on the target, console sign-on, a skipped rerun and
// the handover's init-admin line run as printed.
func TestKubernetesKyMessage(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
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
	mail := startMailpit(t, work)
	catalogDir := filepath.Join(work, "catalog")
	for _, app := range []string{"npm", "kyidentity"} {
		if err := os.CopyFS(filepath.Join(catalogDir, app), os.DirFS("../../internal/catalog/apps/"+app)); err != nil {
			t.Fatal(err)
		}
	}
	loadCatalogApp(t, name, cs, "kyidentity")
	logAppOnFailure(t, cs, "kyidentity")
	plan := filepath.Join(state, "stack.yaml")
	os.WriteFile(plan, []byte("version: 1\ntargets:\n  - name: k8s\n    kubeconfig: "+kc+"\n    storage_class: standard\napps:\n  - name: npm\n    target: k8s\n"), 0o600)
	var outputs, trust []string
	apply := func(label string) string {
		t.Helper()
		out, err := kyq(t, ctx, state, catalogDir, trust, "", "apply")
		outputs = append(outputs, out)
		if err != nil {
			t.Fatalf("apply (%s): %v\n%s", label, err, out)
		}
		return out
	}
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
	msg := startMessageTarget(t, work, catalogDir, nodeIP, certPEM)
	st, err := stack.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	st.Edges = []stack.Edge{{Target: "k8s", Provider: "lan", NPMURL: "kube://kyq-npm/app:81", ClientPort: 30443, EgressAddress: nodeIP,
		AllowCIDRs: []string{mustNode(t, ctx, cs, node).Spec.PodCIDR, msg.forward + "/32", docker(t, "inspect", msg.relay, "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}") + "/32"}}}
	addCatalogIdentity(st, certID)
	// KyIdentity's mail relay is set by its first setup only: Mailpit, STARTTLS and AUTH.
	st.Integrations[len(st.Integrations)-1].Identity.Mail = mail.relay(t, work)
	savePlan(t, plan, st)
	testClusterDNS(t, ctx, cs, nodeIP, mail.ip+" "+mailHost)
	apply("identity")
	trustTestCA(t, ctx, cs, connected, "kyidentity", append(slices.Clone(certPEM), mail.caPEM...))
	mail.refusesInsecure(t)

	// AC14: KyMessages joins later, by amendment.
	st, _ = stack.Load(plan)
	addCatalogMessage(st, certID, msg)
	savePlan(t, plan, st)
	trust = []string{"--trust-host-key", "nas=" + msg.fp}
	listening := wildcardListeners(t, msg.name)
	stackStart := time.Now()
	out := apply("kymessage")
	t.Logf("KyMessages apply took %s", time.Since(stackStart).Round(time.Second))
	if !strings.Contains(out, "setup amendment") {
		t.Fatalf("no amendment line:\n%s", out)
	}
	ids := []string{"kyidentity.amend.kymessage", "kymessage.secrets", "kymessage.matrix-init", "kymessage.credentials", "kymessage.deploy", "kymessage.health", "kymessage.setup", "kymessage.directory"}
	for _, id := range ids {
		if status(out, id) != "ok" {
			t.Fatalf("%s = %q\n%s", id, status(out, id), out)
		}
	}
	if strings.Count(out, "k8s.edge.") < 7 || strings.Contains(out, "kyidentity.scim.kymessage") {
		t.Fatalf("route steps:\n%s", out)
	}

	transport := messageTransport(t, certPEM, nodeIP)
	c := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(role, path string) (int, http.Header, []byte) {
		t.Helper()
		r, err := c.Get("https://" + messageHosts[role] + ":30443" + path)
		if err != nil {
			t.Fatalf("GET %s%s: %v", messageHosts[role], path, err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		return r.StatusCode, r.Header, b
	}
	// AC15: what answers through the edge, and what the edge closes.
	// This host's Docker NAT shows the console its network's gateway, not the edge, so it
	// trusts no forwarded header here (as KyPost's and KyDNS's e2e record): unproven on this host.
	if code, _, b := get("", "/api/installer/network"); code != 200 {
		t.Fatalf("console network: %d %s", code, b)
	} else {
		t.Logf("console network through the edge: %s", b)
	}
	code, h, b := get("server_name", "/.well-known/matrix/client")
	var wk map[string]any
	if code != 200 || json.Unmarshal(b, &wk) != nil || h.Get("Access-Control-Allow-Origin") != "*" || !bytes.Contains(b, []byte("https://"+messageHosts["matrix"])) ||
		!bytes.Contains(b, []byte("https://"+messageHosts[""]+":30443/api/matrix/rtc")) && !bytes.Contains(b, []byte("https://"+messageHosts[""]+"/api/matrix/rtc")) {
		t.Fatalf(".well-known/matrix/client: %d %v %s", code, h, b)
	}
	t.Logf(".well-known/matrix/client: %s", b)
	for _, c := range []struct {
		role, path string
		want       int
	}{
		{"matrix", "/_matrix/client/versions", 200}, {"auth", "/.well-known/openid-configuration", 200}, {"chat", "/config.json", 200}, {"rtc", "/", 200},
		{"server_name", "/", 404}, {"server_name", "/api/installer/network", 404}, {"server_name", "/.well-known/matrix/server", 404},
		{"matrix", "/_synapse/admin/v1/server_version", 404}, {"matrix", "/_synapse/mas/is_localpart_available", 404}, {"rtc", "/twirp/livekit.RoomService/ListRooms", 404},
	} {
		if code, _, b := get(c.role, c.path); code != c.want {
			t.Errorf("%s%s = %d, want %d: %.200s", messageHosts[c.role], c.path, code, c.want, b)
		}
	}
	if code, _, _ := get("matrix", "/_matrix/federation/v1/version"); code == 200 {
		t.Error("federation answers")
	}
	if _, _, b := get("auth", "/.well-known/openid-configuration"); !bytes.Contains(b, []byte(`"issuer":"https://`+messageHosts["auth"])) {
		t.Errorf("MAS issuer: %.300s", b)
	}
	if _, _, b := get("chat", "/config.json"); !bytes.Contains(b, []byte(messageHosts["server_name"])) || !bytes.Contains(b, []byte("https://"+messageHosts["matrix"])) {
		t.Errorf("Element config: %.400s", b)
	}
	// Federation's closed level: no federation listener and an allow-list of none.
	fed := docker(t, "exec", msg.name, "cat", msg.root+"/kymessage/matrix/federation/federation.yaml")
	var level struct {
		Listeners []struct{ Resources []struct{ Names []string } }
		Allow     []string `json:"federation_domain_whitelist"`
	}
	if _, body, _ := strings.Cut(fed, "\n"); json.Unmarshal([]byte(body), &level) != nil || len(level.Listeners) != 1 || len(level.Listeners[0].Resources) != 1 ||
		!slices.Equal(level.Listeners[0].Resources[0].Names, []string{"client", "openid"}) || level.Allow == nil || len(level.Allow) != 0 {
		t.Errorf("federation.yaml is not at the closed level:\n%s", fed)
	}

	// AC16 and AC2: on the target.
	root := msg.root + "/kymessage"
	secrets := map[string]string{}
	for _, f := range []string{"secrets/session_secret", "data/installer/directory_secret", "data/installer/5b8c9d0e-1f2a-4b3c-8d4e-5f6a7b8c9d0e/client.secret"} {
		secrets[f] = strings.TrimSpace(docker(t, "exec", msg.name, "cat", root+"/"+f))
	}
	for _, f := range strings.Fields(docker(t, "exec", msg.name, "ls", root+"/matrix/secrets")) {
		secrets["matrix/secrets/"+f] = strings.TrimSpace(docker(t, "exec", msg.name, "cat", root+"/matrix/secrets/"+f))
	}
	if bad := docker(t, "exec", msg.name, "find", root, "!", "-user", "kyq"); bad != "" {
		t.Errorf("files not the SSH user's: %s", bad)
	}
	modes := docker(t, "exec", msg.name, "sh", "-c", "stat -c '%a %n' "+root+"/data/installer/*/ "+root+"/data/installer/*/* "+root+"/data/installer/directory_secret "+root+"/matrix/secrets/*")
	for _, l := range strings.Split(modes, "\n") {
		if f := strings.Fields(l); len(f) == 2 && !(f[0] == "600" || f[0] == "700" && strings.HasSuffix(f[1], "/")) {
			t.Errorf("mode %s", l)
		}
	}
	manifest, _ := os.ReadFile(filepath.Join(catalogDir, "kymessage/manifest.yaml"))
	for _, svc := range []string{"app", "postgres", "synapse", "mas", "element", "livekit"} {
		c := "kyq-kymessage-" + svc + "-1"
		var in []struct {
			Config struct {
				Image                string
				Env, Cmd, Entrypoint []string
			}
			Image      string
			HostConfig struct {
				Memory, MemorySwap int64
				Privileged         bool
				PortBindings       map[string][]struct{ HostIp, HostPort string }
			}
		}
		if err := json.Unmarshal([]byte(docker(t, "inspect", c)), &in); err != nil || len(in) != 1 {
			t.Fatalf("inspect %s: %v", c, err)
		}
		i := in[0]
		if !strings.Contains(i.Config.Image, "@sha256:") || !bytes.Contains(manifest, []byte(i.Config.Image)) {
			t.Errorf("%s image %s is not the manifest's pin", svc, i.Config.Image)
		}
		if i.HostConfig.Memory <= 0 || i.HostConfig.Memory != i.HostConfig.MemorySwap || i.HostConfig.Privileged {
			t.Errorf("%s memory %d swap %d privileged %v", svc, i.HostConfig.Memory, i.HostConfig.MemorySwap, i.HostConfig.Privileged)
		}
		for p, bs := range i.HostConfig.PortBindings {
			for _, b := range bs {
				if b.HostIp != msg.forward || svc == "postgres" || svc == "mas" && p != "8080/tcp" {
					t.Errorf("%s publishes %s on %q", svc, p, b.HostIp)
				}
			}
		}
		if svc == "livekit" && (len(i.HostConfig.PortBindings) != 3 || i.HostConfig.PortBindings["7882/udp"] == nil || i.HostConfig.PortBindings["7881/tcp"] == nil) {
			t.Errorf("livekit publishes %v", i.HostConfig.PortBindings)
		}
		for _, v := range secrets {
			if len(v) >= 16 && strings.Contains(strings.Join(slices.Concat(i.Config.Env, i.Config.Cmd, i.Config.Entrypoint), "\n"), v) {
				t.Errorf("%s's environment or command holds a secret", svc)
			}
		}
	}
	// Nothing new listens on every address of the target: each publication names its address.
	stackPorts := []string{"5432", "7880", "7881", "7882", "8008", "8080", "8081"}
	for _, p := range msg.ports {
		stackPorts = append(stackPorts, strconv.Itoa(p))
	}
	for l := range wildcardListeners(t, msg.name) {
		if !listening[l] {
			_, port, _ := net.SplitHostPort(strings.Fields(l)[1])
			if slices.Contains(stackPorts, port) {
				t.Errorf("new wildcard listener on the target: %s", l)
			} else {
				t.Logf("new wildcard listener on this shared host, not a KyMessages port: %s", l)
			}
		}
	}
	checks := func() {
		t.Helper()
		check := docker(t, "exec", "kyq-kymessage-app-1", "/bin/sh", "-c", `eval "$KYQ_SECRETS" && exec /app/kymessages apply-setup --file `+messageSetupDir+`/setup.json --check`)
		var report struct{ Created, Pending, Present []map[string]string }
		if json.Unmarshal([]byte(check), &report) != nil || len(report.Created) != 0 || len(report.Pending) != 0 ||
			len(slices.DeleteFunc(slices.Clone(report.Present), func(p map[string]string) bool { return p["kind"] != "identity" })) != 2 {
			t.Errorf("console --check: %s", check)
		}
		t.Logf("console --check: %s", check)
		ns, pod, container, err := connected.AppPod(ctx, "kyidentity")
		if err != nil {
			t.Fatal(err)
		}
		b, err := connected.Exec(ctx, ns, pod, container, []string{"/usr/local/bin/kyidentity", "apply-setup", "--file", "/data/installer/" + identityFixturePreparation().SetupID + "/setup.json", "--check"})
		var identity struct{ Created, Reused []map[string]string }
		if err != nil || json.Unmarshal(b, &identity) != nil || identity.Created == nil || len(identity.Created) != 0 {
			t.Errorf("KyIdentity --check: %v %s", err, b)
		}
		t.Logf("KyIdentity --check: %s", b)
	}
	checks()

	// AC17: console sign-on for both owner accounts.
	console := messageLogin(t, ctx, transport, "prepared-admin", ownerPassword(t, ctx, connected, "admin.password"), true)
	messageLogin(t, ctx, transport, "prepared-user", ownerPassword(t, ctx, connected, "everyday.password"), false)

	// AC23: a rerun skips every KyMessages step and the routes, and touches no file.
	before := docker(t, "exec", msg.name, "sh", "-c", "find "+root+"/matrix "+root+"/data/installer -exec stat -c '%Y %n' {} +")
	out = apply("rerun")
	for _, id := range append(ids, "k8s.edge.") {
		if s := status(out, id); id != "k8s.edge." && s != "skipped" {
			t.Errorf("rerun %s = %q", id, s)
		}
	}
	if after := docker(t, "exec", msg.name, "sh", "-c", "find "+root+"/matrix "+root+"/data/installer -exec stat -c '%Y %n' {} +"); after != before {
		t.Error("rerun changed files under matrix or data/installer")
	}

	// AC24: the handover, and its init-admin line run as printed on the target.
	for _, want := range []string{"chat at https://" + messageHosts["chat"] + ":30443/ (Element)", "Server name " + messageHosts["server_name"], "TCP 7881 and UDP 7882",
		`both "KyMessages" and "KyMessages chat"`, "sign-out does not end chat sessions", "No catalog product can pair with KyRecovery in this release",
		"KyIdentity sends that mail through " + mailHost + ":1025.", `Removing someone from "KyMessages chat" alone stops only their new chat sign-ins`} {
		if !strings.Contains(out, want) {
			t.Errorf("handover lacks %q", want)
		}
	}
	k := strings.Index(out, "cd '"+root+"' && docker compose")
	if k < 0 {
		t.Fatalf("no init-admin line:\n%s", out)
	}
	line := strings.TrimSpace(strings.SplitN(out[k:], "\n", 2)[0])
	pw := hex.EncodeToString(id16())
	cmd := exec.Command("ssh", "-p", msg.port, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "kyq@127.0.0.1", line)
	cmd.Stdin = strings.NewReader(pw + "\n")
	res, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(res, []byte("password successfully reset")) {
		t.Fatalf("init-admin as printed: %v\n%s", err, res)
	}
	secrets["break-glass"] = pw

	// AC18-AC22, AC25, AC26 and the chat-only check: chat, offboarding, backup and restore, the
	// memory sampled throughout.
	chat := &messageChat{t: t, ctx: ctx, transport: transport, connected: connected, msg: msg, mail: mail, console: console, secrets: secrets,
		mas: newMAS(t, ctx, transport), admin: newIdentityAdmin(t, ctx, transport, "prepared-admin", ownerPassword(t, ctx, connected, "admin.password"))}
	secrets["the SMTP password"] = mail.password
	chat.signIns()
	chat.chatOnlySignIn()
	sampler := sampleMemory()
	chatStart := time.Now()
	chat.encryptedRoom()
	chat.offboarding(chat.calls())
	chat.unassign()
	chat.earlyRemoval()
	skipped := func(label, out string) {
		t.Helper()
		for _, id := range ids {
			if s := status(out, id); s != "skipped" {
				t.Errorf("%s: %s = %q", label, id, s)
			}
		}
	}
	chat.backupRestore(messageOps{work: work, state: state, catalogDir: catalogDir, plan: plan, trust: trust, apply: apply, outputs: &outputs, checks: checks, skipped: skipped})
	peak, at := sampler.end()
	t.Logf("AC28: peak KyMessages memory %.0f MiB during AC20-AC26 (%s): %s", peak, time.Since(chatStart).Round(time.Second), at)
	if peak > 3*1024 {
		t.Errorf("KyMessages peaked at %.0f MiB, over 3 GiB", peak)
	}

	// AC27: no secret in any apply, backup or restore output, nor under the state directory.
	noSecrets(t, state, outputs, sweepSecrets(secrets))
	t.Logf("AC27: %d secrets swept over %d outputs and the state directory", len(sweepSecrets(secrets)), len(outputs))
	stats := docker(t, "stats", "--no-stream", "--format", "{{.Name}} {{.MemUsage}}")
	t.Logf("memory:\n%s", stats)
	t.Logf("TestKubernetesKyMessage took %s", time.Since(start).Round(time.Second))
}

func id16() []byte {
	b := make([]byte, 16)
	rand.Read(b)
	return b
}

// messageLogin signs username in to KyIdentity, then to the console as client kymessage, and
// checks its role and the admin-only audit.
func messageLogin(t *testing.T, ctx context.Context, transport *http.Transport, username string, password []byte, admin bool) *http.Client {
	t.Helper()
	identity, console := "https://"+catalogIdentityURLHost, "https://"+messageHosts[""]+":30443"
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: transport, Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(method, endpoint string, body []byte, csrf string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		r, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, endpoint, err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		return r, b
	}
	_, b := send("GET", identity+"/api/auth/csrf", nil, "")
	var csrf struct {
		CSRFToken string `json:"csrfToken"`
	}
	json.Unmarshal(b, &csrf)
	payload, _ := json.Marshal(map[string]string{"username": username, "password": strings.TrimSpace(string(password))})
	clear(password)
	if r, _ := send("POST", identity+"/api/auth/login", payload, csrf.CSRFToken); r.StatusCode != 200 {
		t.Fatalf("%s password sign-in: %d", username, r.StatusCode)
	}
	r, b := send("GET", console+"/api/sso/kyidentity/login", nil, "")
	authorize, err := r.Location()
	if err != nil || r.StatusCode/100 != 3 {
		t.Fatalf("console sign-in start: %d %s", r.StatusCode, b)
	}
	q := authorize.Query()
	if q.Get("client_id") != "kymessage" || q.Get("redirect_uri") != console+"/api/sso/kyidentity/callback" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("nonce") == "" {
		t.Fatalf("authorization request: %s", authorize)
	}
	r, _ = send("GET", authorize.String(), nil, "")
	callback, _ := r.Location()
	if r.StatusCode != 302 || callback == nil || callback.Query().Get("code") == "" {
		t.Fatalf("KyIdentity authorization of %s for kymessage: %d %v", username, r.StatusCode, callback)
	}
	if r, b = send("GET", callback.String(), nil, ""); r.StatusCode/100 != 3 {
		t.Fatalf("console callback: %d %s", r.StatusCode, b)
	}
	_, b = send("GET", console+"/api/auth/me", nil, "")
	t.Logf("%s on the console: %s", username, b)
	role := map[bool]string{true: `"admin"`, false: `"user"`}[admin]
	if !bytes.Contains(b, []byte(role)) {
		t.Fatalf("console role of %s: %s", username, b)
	}
	r, _ = send("GET", console+"/api/admin/audit", nil, "")
	if want := map[bool]int{true: 200, false: 403}[admin]; r.StatusCode != want {
		t.Fatalf("%s /api/admin/audit = %d, want %d", username, r.StatusCode, want)
	}
	return client
}
