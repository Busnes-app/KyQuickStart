package cli

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	messageSetupID = "6d7e8f90-1a2b-4c3d-9e4f-5a6b7c8d9e0f"
	messageBundle  = "/app/data/installer/" + messageSetupID + "/setup.json"
	messageInstall = "1f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"
)

// Canaries: every secret value the KyMessages steps carry, searched for everywhere else.
var (
	consoleCanary   = base64.RawURLEncoding.EncodeToString([]byte("CanaryConsoleSecretCanaryConsole"))
	directoryCanary = base64.RawURLEncoding.EncodeToString([]byte("CanaryDirectorySecretCanaryDirec"))
	chatCanary      = base64.RawURLEncoding.EncodeToString([]byte("CanaryChatClientSecretCanaryChat"))
	smtpCanary      = "canary-smtp-password-for-the-relay"
)

var messageRoutes = []struct{ role, host, port string }{
	{"", "message.example.com", "18080"}, {"server_name", "example.com", "18080"}, {"matrix", "matrix.example.com", "18008"},
	{"auth", "auth.example.com", "18081"}, {"chat", "chat.example.com", "18082"}, {"rtc", "rtc.example.com", "17880"},
}

// messageStackPlan installs NPM and KyIdentity on k1 and KyMessages on Docker host nas, routed
// through k1's edge; edit changes the YAML before it is written.
func messageStackPlan(t *testing.T, f *fixture, edit func(string) string) {
	t.Helper()
	catalogIdentityPlan(t, f, "kyidentity", "  - product: kymessage\n    target: nas\n    bundle: "+messageBundle+"\n    identity_client: kymessage\n    chat_client: kymessage-chat\n")
	p := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(p)
	routes := ""
	for _, r := range messageRoutes {
		role := ""
		if r.role != "" {
			role = "        role: " + r.role + "\n"
		}
		routes += "      - app: kymessage\n" + role + "        hostname: " + r.host + "\n        forward_host: 192.0.2.1\n        forward_port: " + r.port + "\n        certificate_id: 1\n"
	}
	y := strings.Replace(string(b), "    target: k1\nedges:", "    target: k1\n  - name: kymessage\n    target: nas\n    media_address: 192.0.2.1\nedges:", 1)
	y = strings.Replace(y, "    connector_cidr: 192.0.2.9/32\n", "    connector_cidr: 192.0.2.9/32\n    egress_address: 192.0.2.50\n", 1)
	y = strings.Replace(y, "integrations:", routes+"integrations:", 1) + "installation_id: " + messageInstall + "\n"
	if edit != nil {
		y = edit(y)
	}
	os.WriteFile(p, []byte(y), 0o600)
}

func messageExports(setupID string) map[string]string {
	head := `{"schema":"kyidentity.client-credentials/1","setupId":"` + setupID + `","issuerUrl":"https://identity.example.com",`
	admin := `"administrator":{"subject":"` + adminSubject + `","username":"owner-admin","role":"%s"}}`
	return map[string]string{
		"kymessage": head + `"clientId":"kymessage","redirectUris":["https://message.example.com/api/sso/kyidentity/callback"],"directoryUrl":"https://message.example.com/api/sso/kyidentity/sync",` +
			`"clientSecret":"` + consoleCanary + `","directorySecret":"` + directoryCanary + `",` + strings.Replace(admin, "%s", "kymessage.admin", 1),
		"kymessage-chat": head + `"clientId":"kymessage-chat","redirectUris":["https://auth.example.com/upstream/callback/` + providerID(messageInstall, "kymessage") + `"],` +
			`"clientSecret":"` + chatCanary + `",` + strings.Replace(admin, "%s", "", 1),
	}
}

// messageHost is the Docker target nas: real files under its root, docker faked. matrix-init
// writes its outputs (MAS's only once KyIdentity's client secret is there); the console's
// container shell works on the same files through its /app/data and /matrix binds.
type messageHost struct {
	*dockerHost
	t          *testing.T
	inits      []bool // per matrix-init run: did MAS's client secret exist
	overwrite  bool   // matrix-init replaces the seed
	addresses  string // ip -o -4 addr show's output
	setupCalls []string
	cmds       []string // every command, docker or not
	applied    bool
	bundle     string
}

func (h *messageHost) dir() string { return filepath.Join(h.root, "kymessage") }

func (h *messageHost) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	h.cmds = append(h.cmds, cmd)
	switch {
	case strings.HasPrefix(cmd, "{ ip -o -4 addr show"):
		h.docker = append(h.docker, cmd)
		return remote.Local{}.Run(ctx, "printf '%s' "+remote.Quote(h.addresses)+" | "+strings.SplitN(cmd, "} | ", 2)[1], nil)
	case strings.Contains(cmd, " run --rm -T "):
		h.docker = append(h.docker, cmd)
		return nil, h.matrixInit()
	case strings.Contains(cmd, "docker exec "):
		h.docker = append(h.docker, cmd)
		return h.console(ctx, cmd, stdin)
	}
	return h.dockerHost.Run(ctx, cmd, stdin)
}

func (h *messageHost) matrixInit() error {
	m := filepath.Join(h.dir(), "matrix")
	_, err := os.Stat(filepath.Join(m, "secrets/kyidentity_client_secret"))
	h.inits = append(h.inits, err == nil)
	files := map[string]string{"synapse/homeserver.yaml": "server_name: \"example.com\"\n", "synapse/signing.key": "k", "element/config.json": "{}",
		"federation/federation.yaml": "{}", "secrets/mas_admin_client_id": "01J0000000000000000000000A", "secrets/postgres_password": "p"}
	if err == nil {
		files["mas/config.yaml"] = "upstream: x\n"
	}
	if h.overwrite {
		files["secrets/upstream_provider_id"] = "01JOTHERPROVIDER0000000000"
		os.Remove(filepath.Join(m, "secrets/upstream_provider_id"))
	}
	for rel, body := range files {
		p := filepath.Join(m, rel)
		os.MkdirAll(filepath.Dir(p), 0o700)
		if _, err := os.Stat(p); err == nil && !strings.HasPrefix(rel, "synapse/homeserver") {
			continue // matrix-init keeps secrets and keys
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// console answers docker exec into the console: placeScript on its binds, and its apply-setup.
func (h *messageHost) console(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	rest, ok := strings.CutPrefix(cmd, dockerAppContainer("kymessage"))
	if !ok {
		h.t.Fatalf("exec without finding the console's container: %s", cmd)
	}
	rest = strings.TrimPrefix(strings.TrimPrefix(rest, "docker exec -i "), "docker exec ")
	rest = strings.TrimPrefix(rest, `"$c" `)
	out, _ := remote.Local{}.Run(ctx, `printf '%s\0' `+rest, nil)
	args := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if args[0] == "sh" {
		p := strings.Replace(args[5], "/app/data/", filepath.Join(h.dir(), "data")+"/", 1)
		_, err := remote.Local{}.Run(ctx, "sh -c "+remote.Quote(placeScript)+" sh "+remote.Quote(args[4])+" "+remote.Quote(p), stdin)
		return nil, err
	}
	if args[0] != "/app/kymessages" {
		return nil, errors.New("unexpected binary " + args[0])
	}
	h.setupCalls = append(h.setupCalls, strings.Join(args[1:], " "))
	switch {
	case slices.Contains(args, "--capabilities"):
		return []byte(`{"schema":"ky.setup-capabilities/1","service":"kymessage","bundleSchema":"ky.product-setup/1","features":["kyidentity-oidc","directory-webhook","dedicated-admin","check","restart"]}`), nil
	case slices.Contains(args, "--restart"):
		return nil, nil
	}
	check := slices.Contains(args, "--check")
	b, err := os.ReadFile(strings.Replace(args[slices.Index(args, "--file")+1], "/app/data/", filepath.Join(h.dir(), "data")+"/", 1))
	if err != nil || check && !h.applied {
		return nil, errors.New("not applied")
	}
	h.bundle = string(b)
	created := `[]`
	if !check && !h.applied {
		h.applied, created = true, `[{"kind":"configuration","name":"sso"}]`
	}
	return []byte(`{"schema":"ky.product-setup-report/1","service":"kymessage","setupId":"` + messageSetupID + `","created":` + created + `,"present":[],"pending":[]}`), nil
}

// messageSession loads the plan with KyIdentity's ready pod answered by identity and nas by h.
func messageSession(t *testing.T, f *fixture, identity *fakeIdentity) (*session, *messageHost) {
	t.Helper()
	ctx := context.Background()
	h := &messageHost{dockerHost: f.h, t: t, addresses: "2: eth0    inet 192.0.2.1/24 brd 192.0.2.255 scope global eth0\n"}
	saved := dialTarget
	t.Cleanup(func() { dialTarget = saved })
	dialTarget = func(_ context.Context, t stack.Target, _ remote.SSHConfig, needsStorage bool) (driver, error) {
		if t.Kubernetes() {
			return kubeDriver{c: kube.Fake(f.cs, "https://k1.test", identity.stream), needsStorage: needsStorage}, nil
		}
		return sshDriver{t: t, c: h}, nil
	}
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	f.cs.CoreV1().Pods("kyq-kyidentity").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Labels: map[string]string{"app.kubernetes.io/name": "kyidentity", managed.Label: managed.Value}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}, metav1.CreateOptions{})
	s.drivers["k1"] = kubeDriver{c: kube.Fake(f.cs, "https://k1.test", identity.stream)}
	s.drivers["nas"] = sshDriver{t: s.placed["kymessage"], c: h}
	return s, h
}

func newMessageFixture(t *testing.T) *fixture {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	return f
}

var ulidRE = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

// AC9: one apply installing KyIdentity and KyMessages puts both clients in the first setup.
func TestKyMessageClientsFromCatalog(t *testing.T) {
	f := newMessageFixture(t)
	messageStackPlan(t, f, nil)
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	id := providerID(messageInstall, "kymessage")
	b, _ := json.Marshal(s.integrations[0].Identity.Clients)
	want := `[{"id":"kymessage","name":"KyMessages","redirectUris":["https://message.example.com/api/sso/kyidentity/callback"],"postLogoutUris":null,"administratorRole":"kymessage.admin","everydayRole":"kymessage.user","suiteWebhook":{"url":"https://message.example.com/api/sso/kyidentity/sync"}},` +
		`{"id":"kymessage-chat","name":"KyMessages chat","redirectUris":["https://auth.example.com/upstream/callback/` + id + `"],"postLogoutUris":null,"everydayRole":"kymessage.chat"}]`
	if string(b) != want || len(s.amendments) != 0 || !slices.Equal(s.derived, []string{"kymessage", "kymessage-chat"}) {
		t.Fatalf("clients = %s\namendments %d, derived %v", b, len(s.amendments), s.derived)
	}
	if !ulidRE.MatchString(id) || providerID(messageInstall, "kymessage") != id || providerID("2a2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b", "kymessage") == id {
		t.Fatalf("provider ID %s is not a stable ULID of the installation", id)
	}
	// The saved plan keeps only what the operator wrote.
	if len(f.stack(t).Integrations[0].Identity.Clients) != 0 {
		t.Fatal("derived clients reached the plan")
	}
	messageStackPlan(t, f, func(y string) string {
		return strings.Replace(y, "      clients: []\n", "      clients:\n        - {id: kymessage-chat, name: KyMessages chat, redirect_uris: [https://auth.example.com/upstream/callback/x], everyday_role: kymessage.chat}\n", 1)
	})
	if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), "differs from the catalog's KyMessages chat client") {
		t.Fatalf("different hand-written chat client: %v", err)
	}
}

// AC7: plans refused before any connection, naming the field.
func TestKyMessagePlanRefusals(t *testing.T) {
	pw := filepath.Join(t.TempDir(), "smtp.password")
	os.WriteFile(pw, []byte("x\n"), 0o600)
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(pw, link)
	loose := filepath.Join(t.TempDir(), "loose")
	os.WriteFile(loose, []byte("x\n"), 0o644)
	mail := func(security, file string) func(string) string {
		return func(y string) string {
			return strings.Replace(y, "      clients: []\n", "      mail: {host: smtp.example.test, port: 587, security: "+security+", from: identity@example.test, username: kyidentity, password_file: "+file+"}\n      clients: []\n", 1)
		}
	}
	route := func(from, to string) func(string) string {
		return func(y string) string { return strings.Replace(y, from, to, 1) }
	}
	all := func(from, to string) func(string) string {
		return func(y string) string { return strings.ReplaceAll(y, from, to) }
	}
	for name, c := range map[string]struct {
		edit func(string) string
		want string
	}{
		"on kubernetes": {func(y string) string {
			y = strings.Replace(y, "  - name: kymessage\n    target: nas\n    media_address: 192.0.2.1\n", "  - name: kymessage\n    target: k1\n    media_address: 192.0.2.1\n", 1)
			return strings.Replace(y, "    target: nas\n    bundle: /app/data", "    target: k1\n    bundle: /app/data", 1)
		}, "place it on a Docker host"},
		"missing role":          {route("      - app: kymessage\n        role: rtc\n", "      - app: kymessage\n        role: chat\n"), "role \"chat\" must be one of its roles, once"},
		"unknown role":          {route("        role: rtc\n", "        role: turn\n"), "role \"turn\" must be one of its roles"},
		"primary named":         {route("      - app: kymessage\n        hostname: message.example.com\n", "      - app: kymessage\n        role: console\n        hostname: message.example.com\n"), `route message.example.com has role "console"; remove the role`},
		"no primary route":      {route("      - app: kymessage\n        hostname: message.example.com\n        forward_host: 192.0.2.1\n        forward_port: 18080\n        certificate_id: 1\n", ""), "needs a route with no role: its console"},
		"wildcard forward_host": {all("forward_host: 192.0.2.1", "forward_host: 0.0.0.0"), "forward_host 0.0.0.0 must be one LAN IPv4 address of nas"},
		"loopback forward_host": {all("forward_host: 192.0.2.1", "forward_host: 127.0.0.1"), "forward_host 127.0.0.1 must be one LAN IPv4 address of nas"},
		"link-local forward":    {all("forward_host: 192.0.2.1", "forward_host: 169.254.1.1"), "forward_host 169.254.1.1 must be one LAN IPv4 address of nas"},
		"named forward_host":    {all("forward_host: 192.0.2.1", "forward_host: nas.example.test"), "forward_host nas.example.test must be one LAN IPv4 address of nas"},
		"server_name own port":  {route("hostname: example.com\n        forward_host: 192.0.2.1\n        forward_port: 18080", "hostname: example.com\n        forward_host: 192.0.2.1\n        forward_port: 18090"), "routes message.example.com and example.com are the same service port (app:8080) and need one forward_port: set both to 18080"},
		"two forward hosts":     {route("forward_host: 192.0.2.1\n        forward_port: 18008", "forward_host: 192.0.2.2\n        forward_port: 18008"), "one forward_host"},
		"shared forward port":   {route("forward_port: 18008", "forward_port: 18081"), "forward_port 18081 is given to two services"},
		"no media_address":      {route("    media_address: 192.0.2.1\n", ""), "set media_address"},
		"wildcard media":        {route("media_address: 192.0.2.1", "media_address: 0.0.0.0"), "media_address"},
		"loopback media":        {route("media_address: 192.0.2.1", "media_address: 127.0.0.1"), "media_address"},
		"link-local media":      {route("media_address: 192.0.2.1", "media_address: 169.254.1.1"), "media_address"},
		"multicast media":       {route("media_address: 192.0.2.1", "media_address: 224.0.0.9"), "media_address"},
		"ipv6 media":            {route("media_address: 192.0.2.1", "media_address: 'fd00::1'"), "media_address"},
		"name media":            {route("media_address: 192.0.2.1", "media_address: nas.example.test"), "media_address"},
		"no installation_id":    {route("installation_id: "+messageInstall+"\n", ""), "installation_id"},
		"no chat_client":        {route("    chat_client: kymessage-chat\n", ""), "chat_client must be kymessage-chat"},
		"other chat_client":     {route("chat_client: kymessage-chat", "chat_client: chat"), "chat_client must be kymessage-chat"},
		"owner admin username":  {route("username: owner-admin", "username: admin"), "identity.admin.username"},
		"bad everyday username": {route("everyday: {username: owner,", "everyday: {username: o,"), "identity.everyday.username"},
		"plaintext smtp":        {mail("none", pw), "identity.mail.security"},
		"relative password":     {mail("starttls", "smtp.password"), "identity.mail.password_file"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMessageFixture(t)
			messageStackPlan(t, f, c.edit)
			dialed := false
			saved := dialTarget
			defer func() { dialTarget = saved }()
			dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
				dialed = true
				return nil, errors.New("no connection expected")
			}
			for _, args := range [][]string{{"review"}, {"plan", "--noninteractive"}, {"apply"}} {
				if err := f.run(args...); err == nil || !strings.Contains(err.Error(), c.want) || dialed {
					t.Fatalf("%v: err = %v (dialed %v), want %q", args, err, dialed, c.want)
				}
			}
		})
	}
	// A valid plan, with mail, is accepted.
	f := newMessageFixture(t)
	messageStackPlan(t, f, mail("starttls", pw))
	if err := f.run("review"); err != nil {
		t.Fatal(err)
	}
	// review is offline and reads no password file; preflight and apply refuse an unsafe or
	// missing one before connecting.
	for name, file := range map[string]string{"symlinked": link, "loose": loose, "missing": filepath.Join(t.TempDir(), "absent")} {
		t.Run(name+" password", func(t *testing.T) {
			f := newMessageFixture(t)
			messageStackPlan(t, f, mail("starttls", file))
			dialed := false
			saved := dialTarget
			defer func() { dialTarget = saved }()
			dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
				dialed = true
				return nil, errors.New("no connection expected")
			}
			for _, args := range [][]string{{"review"}, {"plan", "--noninteractive"}} {
				if err := f.run(args...); err != nil {
					t.Fatalf("%v: %v", args, err)
				}
			}
			for _, args := range [][]string{{"preflight"}, {"apply"}} {
				if err := f.run(args...); err == nil || !strings.Contains(err.Error(), "identity.mail.password_file") || dialed {
					t.Fatalf("%v: err = %v (dialed %v)", args, err, dialed)
				}
			}
		})
	}
}

// Each unsafe SMTP password file is refused with its own reason, checked on the opened file.
func TestReadMailPassword(t *testing.T) {
	dir := t.TempDir()
	file := func(name, body string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(body), 0o600)
		os.Chmod(p, mode)
		return p
	}
	good := file("good", "s3cret\n", 0o600)
	link := filepath.Join(dir, "link")
	os.Symlink(good, link)
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{
		filepath.Join(dir, "missing"):    "does not exist",
		link:                             "is a symlink",
		fifo:                             "is not a regular file",
		dir:                              "is not a regular file",
		file("loose", "s3cret\n", 0o644): "has mode 0644, not 0600 (chmod 600 " + dir + "/loose)",
		file("empty", "", 0o600):         "is empty",
		file("long", strings.Repeat("x", 1025), 0o600): "is longer than 1024 bytes",
		file("lines", "one\ntwo\n", 0o600):             "holds more than one line (or a CR or NUL byte)",
		file("nul", "one\x00\n", 0o600):                "holds more than one line (or a CR or NUL byte)",
	} {
		if b, err := readMailPassword(p); err == nil || !strings.Contains(err.Error(), p+" "+want+". It must be a regular file of yours with mode 0600") {
			t.Errorf("%s: %q, %v; want %q", p, b, err, want)
		}
	}
	for body, want := range map[string]string{"s3cret\n": "s3cret", "s3cret": "s3cret", strings.Repeat("x", 1024) + "\n": strings.Repeat("x", 1024)} {
		p := file("ok", body, 0o600)
		if b, err := readMailPassword(p); err != nil || string(b) != want {
			t.Errorf("%q: %q, %v", body, b, err)
		}
	}
}

// AC7: at apply's preflight, a media_address that is not the Docker target's is refused.
func TestKyMessageMediaAddressPreflight(t *testing.T) {
	f := newMessageFixture(t)
	messageStackPlan(t, f, nil)
	s, h := messageSession(t, f, &fakeIdentity{})
	h.addresses = "2: eth0    inet 192.0.2.7/24 brd 192.0.2.255 scope global eth0\n"
	f.out.Reset()
	s.targets = []stack.Target{s.placed["kymessage"]}
	s.edges = nil
	if err := s.preflight(context.Background()); err == nil || !strings.Contains(f.out.String(), "media_address 192.0.2.1 is not an address of this host; set media_address to one of its LAN IPv4 addresses (ip -4 addr)") {
		t.Fatalf("err = %v\n%s", err, f.out.String())
	}
	h.addresses = "2: eth0    inet 192.0.2.1/24 brd 192.0.2.255 scope global eth0\n"
	f.out.Reset()
	s.preflight(context.Background())
	if !strings.Contains(f.out.String(), "ok    nas          media address    kymessage: 192.0.2.1 is this host's") {
		t.Fatalf("preflight:\n%s", f.out.String())
	}
}

// AC10, AC11: matrix-init's first pass, then the secrets from KyIdentity's export, then its
// second pass, then the console's setup; secrets only on stdin, never replaced.
func TestKyMessageSteps(t *testing.T) {
	ctx := context.Background()
	f := newMessageFixture(t)
	messageStackPlan(t, f, nil)
	identity := &fakeIdentity{staged: true, applied: true, exports: messageExports("271333e2-c221-4601-b249-563b3d828530"), checkReport: firstSetupCheck(ownerReused)}
	s, h := messageSession(t, f, identity)
	list, err := s.deploySteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(slices.DeleteFunc(ids(list), func(id string) bool { return strings.HasPrefix(id, "npm.") || strings.HasPrefix(id, "kyidentity.") }), " ")
	edges := strings.Join(ids(list)[len(ids(list))-8:len(ids(list))-1], " ")
	if !strings.HasPrefix(got, "kymessage.secrets kymessage.matrix-init kymessage.credentials kymessage.deploy kymessage.health kymessage.setup k1.edge.removed k1.edge.") || strings.Count(edges, "k1.edge.") != 7 ||
		!strings.HasSuffix(got, " kymessage.directory") || strings.Contains(got, "scim") {
		t.Fatalf("steps = %s", got)
	}
	steps := map[string]engine.Step{}
	for _, st := range list {
		steps[st.ID()] = st
	}
	// The seed: refused when it differs, accepted when it is the same.
	seed := filepath.Join(h.dir(), "matrix/secrets/upstream_provider_id")
	os.MkdirAll(filepath.Dir(seed), 0o700)
	os.WriteFile(seed, []byte("01JOTHERPROVIDER0000000000"), 0o600)
	if err := steps["kymessage.matrix-init"].Apply(ctx); code(err) != suite.ProviderIDMismatch {
		t.Fatalf("different seed: %v", err)
	}
	os.WriteFile(seed, []byte(providerID(messageInstall, "kymessage")), 0o600)
	for _, id := range []string{"kymessage.matrix-init", "kymessage.credentials"} {
		if err := steps[id].Apply(ctx); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if err := steps[id].Verify(ctx); err != nil {
			t.Fatalf("%s verify: %v", id, err)
		}
	}
	if !slices.Equal(h.inits, []bool{false, true}) {
		t.Fatalf("matrix-init passes saw MAS's secret: %v, want first without, second with", h.inits)
	}
	for _, c := range h.docker {
		if strings.Contains(c, " run --rm -T ") && (!strings.Contains(c, "-e 'KY_KYIDENTITY_ISSUER=https://identity.example.com' -e 'KY_MATRIX_MAS_CLIENT_ID=kymessage-chat' 'matrix-init'") || !strings.Contains(c, "-p 'kyq-kymessage'")) {
			t.Errorf("matrix-init run: %s", c)
		}
	}
	read := func(rel string) string { b, _ := os.ReadFile(filepath.Join(h.dir(), rel)); return string(b) }
	dir := "data/installer/" + messageSetupID
	for rel, want := range map[string]string{dir + "/client.secret": consoleCanary, dir + "/directory.secret": directoryCanary, "data/installer/directory_secret": directoryCanary, "matrix/secrets/kyidentity_client_secret": chatCanary} {
		fi, err := os.Stat(filepath.Join(h.dir(), rel))
		if read(rel) != want || err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s = %q (%v)", rel, read(rel), err)
		}
	}
	// Never replaced: a changed secret at KyIdentity is a refusal, and the file stays.
	identity.exports["kymessage-chat"] = strings.Replace(identity.exports["kymessage-chat"], chatCanary, base64.RawURLEncoding.EncodeToString([]byte("AnotherChatSecretAnotherChatSecr")), 1)
	if err := steps["kymessage.credentials"].Apply(ctx); err == nil || !strings.Contains(err.Error(), "never replaced") || read("matrix/secrets/kyidentity_client_secret") != chatCanary {
		t.Fatalf("changed chat secret: %v", err)
	}
	identity.exports = messageExports("271333e2-c221-4601-b249-563b3d828530")
	if err := steps["kymessage.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	// Verify only reads: check placements, --check and --capabilities, read-only exports.
	h.cmds, identity.calls, h.setupCalls = nil, nil, nil
	for _, id := range []string{"kymessage.matrix-init", "kymessage.credentials", "kymessage.setup"} {
		if err := steps[id].Verify(ctx); err != nil {
			t.Fatalf("%s verify: %v", id, err)
		}
	}
	for _, c := range append(append(h.cmds, identity.calls...), h.setupCalls...) {
		if strings.Contains(c, " run --rm") || strings.Contains(c, "sh -c") && !strings.Contains(c, "'check'") && !strings.Contains(c, " check ") ||
			strings.Contains(c, "apply-setup") && !strings.Contains(c, "--check") && !strings.Contains(c, "--capabilities") && !strings.Contains(c, "--export-client") ||
			strings.Contains(c, "--restart") {
			t.Errorf("Verify wrote: %s", c)
		}
	}
	var b struct {
		SSO      map[string]string
		Admins   []map[string]string
		Everyday []map[string]string
	}
	json.Unmarshal([]byte(h.bundle), &b)
	if b.SSO["clientId"] != "kymessage" || b.SSO["directorySecretFile"] != "/app/"+dir+"/directory.secret" || len(b.Admins) != 1 || b.Admins[0]["username"] != "owner-admin" {
		t.Fatalf("console bundle = %s", h.bundle)
	}
	// AC11: no secret in any argv, output, the plan or the state directory.
	var seen []string
	seen = append(seen, h.docker...)
	for _, c := range h.cmds {
		seen = append(seen, c) // argv of every host command
	}
	seen = append(seen, identity.calls...)
	seen = append(seen, f.out.String())
	filepath.WalkDir(f.state, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			seen = append(seen, string(b))
		}
		return nil
	})
	for _, s := range seen {
		for _, c := range []string{consoleCanary, directoryCanary, chatCanary} {
			if strings.Contains(s, c) {
				t.Fatalf("secret %s leaked into %q", c[:8], s)
			}
		}
	}
	// A matrix-init that replaces the seed is refused.
	h.overwrite = true
	if err := steps["kymessage.matrix-init"].Apply(ctx); code(err) != suite.ProviderIDMismatch {
		t.Fatalf("overwritten seed: %v", err)
	}
}

// AC8: the server name never changes after install; the auth host follows the moved-client rule.
func TestKyMessageServerNameChanged(t *testing.T) {
	ctx := context.Background()
	f := newMessageFixture(t)
	messageStackPlan(t, f, func(y string) string {
		return strings.Replace(y, "hostname: example.com\n", "hostname: chat-example.com\n", 1)
	})
	s, h := messageSession(t, f, &fakeIdentity{})
	p := filepath.Join(h.dir(), "matrix/synapse")
	os.MkdirAll(p, 0o700)
	os.WriteFile(filepath.Join(p, "homeserver.yaml"), []byte("server_name: \"example.com\"\n"), 0o600)
	_, err := s.deploySteps(ctx)
	if code(err) != suite.ServerNameChanged || !strings.Contains(err.Error(), "every Matrix ID") || !strings.Contains(err.Error(), "from example.com to chat-example.com") {
		t.Fatalf("err = %v", err)
	}
	// The auth host is in the chat client's registered redirect: moving it is issuer_changed.
	messageStackPlan(t, f, func(y string) string {
		return strings.Replace(y, "hostname: auth.example.com\n", "hostname: login.example.com\n", 1)
	})
	identity := &fakeIdentity{staged: true, applied: true, exports: messageExports("271333e2-c221-4601-b249-563b3d828530")}
	s, _ = messageSession(t, f, identity)
	f.cs.AppsV1().Deployments("kyq-kyidentity").Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Labels: map[string]string{"app.kubernetes.io/name": "kyidentity", managed.Label: managed.Value}},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app", Env: []corev1.EnvVar{{Name: "KYIDENTITY_ISSUER_URL", Value: "https://identity.example.com"}},
		}}}}},
	}, metav1.CreateOptions{})
	if _, err := s.deploySteps(ctx); code(err) != suite.IssuerChanged || !strings.Contains(err.Error(), "KyMessages chat") {
		t.Fatalf("moved auth host: %v", err)
	}
}

// AC10: KyMessages joining after KyIdentity's first setup brings both clients in one amendment.
func TestKyMessageAddedByAmendment(t *testing.T) {
	ctx := context.Background()
	f := newMessageFixture(t)
	identity := setUpIdentity(t, f, amendmentFeatures)
	messageStackPlan(t, f, nil)
	id := amendmentID(parentSetup, "kymessage")
	bundle := "/data/installer/" + id + "/setup.json"
	amendment := &fakeIdentity{parent: identity, exports: messageExports(id)}
	identity.setups[bundle] = amendment
	s, h := messageSession(t, f, identity)
	f.cs.AppsV1().Deployments("kyq-kyidentity").Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Labels: map[string]string{"app.kubernetes.io/name": "kyidentity", managed.Label: managed.Value}},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app", Env: []corev1.EnvVar{{Name: "KYIDENTITY_ISSUER_URL", Value: "https://identity.example.com"}},
		}}}}},
	}, metav1.CreateOptions{})
	s.changesPlan = false
	publish, err := s.amendLate(ctx, f.state, func() error { return nil })
	if err != nil || publish == nil || len(s.amendments) != 1 || len(s.derived) != 0 {
		t.Fatalf("amendment: %v, %d amendments, derived %v", err, len(s.amendments), s.derived)
	}
	var a struct {
		Extends string
		Clients []stack.IdentityClient
	}
	json.Unmarshal(s.amendments[0].bundle, &a)
	if a.Extends != parentSetup || len(a.Clients) != 2 || a.Clients[0].ID != "kymessage" || a.Clients[1].ID != "kymessage-chat" || a.Clients[1].AdministratorRole != "" {
		t.Fatalf("amendment bundle = %s", s.amendments[0].bundle)
	}
	s.edges = nil
	list, err := s.deploySteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(ids(list), " ")
	if !strings.Contains(got, "kyidentity.amend.kymessage kyidentity.retire-bootstrap") || strings.Contains(got, "kyidentity.amend.kymessage-chat") ||
		!strings.Contains(got, "kymessage.matrix-init kymessage.credentials kymessage.deploy kymessage.health kymessage.setup") || strings.Contains(got, "kyidentity.scim") {
		t.Fatalf("steps = %s", got)
	}
	// Its credentials step exports both clients from the amendment.
	for _, st := range list {
		if st.ID() == "kyidentity.amend.kymessage" {
			if err := st.Apply(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	os.MkdirAll(filepath.Join(h.dir(), "matrix/secrets"), 0o700)
	for _, st := range list {
		if st.ID() == "kymessage.matrix-init" || st.ID() == "kymessage.credentials" {
			if err := st.Apply(ctx); err != nil {
				t.Fatalf("%s: %v", st.ID(), err)
			}
		}
	}
	if b, _ := os.ReadFile(filepath.Join(h.dir(), "matrix/secrets/kyidentity_client_secret")); string(b) != chatCanary {
		t.Fatalf("MAS secret = %q", b)
	}
	if !slices.ContainsFunc(amendment.calls, func(c string) bool { return strings.HasSuffix(c, "--file "+bundle+" --export-client kymessage-chat") }) {
		t.Fatalf("amendment calls = %v", amendment.calls)
	}
}

// AC13: the relay's password goes to KyIdentity's private copy before staging, the bundle names
// only that copy, and a changed relay refuses, pointing at KyIdentity's admin UI.
func TestKyIdentityMailBundle(t *testing.T) {
	ctx := context.Background()
	f := newMessageFixture(t)
	pw := filepath.Join(t.TempDir(), "smtp.password")
	os.WriteFile(pw, []byte(smtpCanary+"\n"), 0o600)
	withMail := func(host string) func(string) string {
		return func(y string) string {
			return strings.Replace(y, "      clients: []\n", "      mail: {host: "+host+", port: 587, security: starttls, from: identity@example.test, username: kyidentity, password_file: "+pw+"}\n      clients: []\n", 1)
		}
	}
	messageStackPlan(t, f, withMail("smtp.example.test"))
	identity := &fakeIdentity{features: []string{"mail-relay"}}
	files := map[string]string{}
	var placed []string
	stream := identity.stream
	s, _ := messageSession(t, f, identity)
	s.drivers["k1"] = kubeDriver{c: kube.Fake(f.cs, "https://k1.test", func(ctx context.Context, ns, pod, c string, args []string, o remotecommand.StreamOptions) error {
		if args[0] == "sh" {
			b := readAll(o.Stdin)
			placed = append(placed, args[4]+" "+args[5])
			return fakePlace(files, args[4], args[5], b)
		}
		return stream(ctx, ns, pod, c, args, o)
	})}
	s.edges = nil
	list, err := s.deploySteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var setup engine.Step
	for _, st := range list {
		if st.ID() == "kyidentity.setup" {
			setup = st
		}
	}
	if err := setup.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if files["/data/installer/smtp.password"] != smtpCanary || placed[0] != "write /data/installer/smtp.password" {
		t.Fatalf("relay password placed %v: %q", placed, files["/data/installer/smtp.password"])
	}
	var intent struct {
		Mail map[string]any
	}
	json.Unmarshal([]byte(identity.intent), &intent)
	if intent.Mail["host"] != "smtp.example.test" || intent.Mail["security"] != "starttls" || intent.Mail["passwordFile"] != "/data/installer/smtp.password" || intent.Mail["from"] != "identity@example.test" || strings.Contains(identity.intent, smtpCanary) || strings.Contains(identity.intent, pw) {
		t.Fatalf("staged intent = %s", identity.intent)
	}
	if err := setup.Verify(ctx); err != nil {
		t.Fatalf("rerun with the same relay: %v", err)
	}
	// A changed host is a changed setup intent: refused, naming the admin UI.
	messageStackPlan(t, f, withMail("relay.example.test"))
	s, _ = messageSession(t, f, identity)
	s.drivers["k1"] = kubeDriver{c: kube.Fake(f.cs, "https://k1.test", func(ctx context.Context, ns, pod, c string, args []string, o remotecommand.StreamOptions) error {
		if args[0] == "sh" {
			return fakePlace(files, args[4], args[5], readAll(o.Stdin))
		}
		return stream(ctx, ns, pod, c, args, o)
	})}
	s.edges = nil
	list, _ = s.deploySteps(ctx)
	if err := list[slices.IndexFunc(list, func(st engine.Step) bool { return st.ID() == "kyidentity.setup" })].Apply(ctx); err == nil || !strings.Contains(err.Error(), "KyIdentity's admin UI (Settings > Mail)") {
		t.Fatalf("changed relay: %v", err)
	}
	for _, c := range identity.calls {
		if strings.Contains(c, smtpCanary) {
			t.Fatal("relay password in argv")
		}
	}
	// Without identity.mail the intent is byte for byte master's: TestStepInputsGolden.
}

func bufioReader(s string) *bufio.Reader { return bufio.NewReader(strings.NewReader(s)) }

func strconvItoa(n int) string { return strconv.Itoa(n) }

func readAll(r io.Reader) []byte {
	b, _ := io.ReadAll(r)
	return b
}

// The guided plan asks KyMessages' six hostnames under one domain, the server name being the
// domain itself, with one forward port per published service.
func TestGuidedKyMessageRoutes(t *testing.T) {
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	st := &stack.Stack{Version: 1, Targets: []stack.Target{{Name: "nas", Host: "192.0.2.1", User: "kyq", Port: 22, Root: "/opt/kyq"}},
		Apps: []stack.App{{Name: "kymessage", Target: "nas", MediaAddress: "192.0.2.1"}}}
	// The address: a wildcard and loopback are refused, then Enter takes the media address.
	answers := []string{"lan", "https://npm.example.com", "192.168.1.0/24", "", "yes", "example.com", "0.0.0.0", "127.0.0.1", "", "3", "", "", "", "", "", "", "", "", "", "", ""}
	var out strings.Builder
	p := &prompts{ctx: context.Background(), in: bufioReader(strings.Join(answers, "\n") + "\n"), out: &out, answers: map[string]string{}, draft: filepath.Join(t.TempDir(), "answers.json")}
	if err := guidedEdges(p, st, cat); err != nil {
		t.Fatal(err, out.String())
	}
	var got []string
	for _, r := range st.Edges[0].Routes {
		got = append(got, r.Role+"="+r.Hostname+":"+strconvItoa(r.ForwardPort)+"@"+r.ForwardHost)
	}
	want := []string{"=message.example.com:8080@192.0.2.1", "server_name=example.com:8080@192.0.2.1", "matrix=matrix.example.com:8008@192.0.2.1", "auth=auth.example.com:8081@192.0.2.1", "chat=chat.example.com:8082@192.0.2.1", "rtc=rtc.example.com:7880@192.0.2.1"}
	if !slices.Equal(got, want) {
		t.Fatalf("routes = %v\n%s", got, out.String())
	}
	for _, line := range []string{"kymessage's domain. It becomes the permanent Matrix server name (accounts are @name:<domain>)",
		"hostnames (each can be changed next): message.example.com (console), example.com (server_name), matrix.example.com (matrix), auth.example.com (auth), chat.example.com (chat), rtc.example.com (rtc). Each needs a DNS record",
		"address on nas that NPM reaches: one LAN IPv4 address, where every routed port is published [192.0.2.1]",
		"covering example.com and *.example.com (a wildcard alone misses the apex)", "server_name hostname is permanent"} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("prompts lack %q:\n%s", line, out.String())
		}
	}
	if n := strings.Count(out.String(), "Please enter a valid value"); n != 2 {
		t.Errorf("%d refusals of the wildcard and loopback addresses, want 2", n)
	}
	if _, err := withRoute(st, "kymessage", st.Targets[0], cat["kymessage"]); err != nil {
		t.Fatalf("guided routes refused: %v", err)
	}
}

// plan writes the chat client's one possible name for an installed KyMessages with a console
// client, and nothing for one without.
func TestWithChatClients(t *testing.T) {
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	st := &stack.Stack{Apps: []stack.App{{Name: "kymessage", Target: "nas"}}, Integrations: []stack.Integration{
		{Product: "kymessage", Target: "nas", IdentityClient: "kymessage"}, {Product: "kymessage", Target: "other", IdentityClient: "kymessage"},
		{Product: "kynotes", Target: "nas", IdentityClient: "kynotes"}}}
	withChatClients(st, cat)
	if got := []string{st.Integrations[0].ChatClient, st.Integrations[1].ChatClient, st.Integrations[2].ChatClient}; !slices.Equal(got, []string{"kymessage-chat", "", ""}) {
		t.Fatalf("chat clients %q", got)
	}
	st.Integrations[0].IdentityClient, st.Integrations[0].ChatClient = "", ""
	if withChatClients(st, cat); st.Integrations[0].ChatClient != "" {
		t.Fatal("a chat client without the console's")
	}
}

// kymessage.directory: apply goes on only once KyIdentity's connector delivered the console's
// directory, so an offboarding right after install reaches the chat sweep.
func TestKyMessageDirectoryStep(t *testing.T) {
	ctx := context.Background()
	detail := "pending, 2 pending, 0 failed"
	s := directoryStep{app: "kymessage", sync: func(context.Context) (string, string) { return "kymessage", detail }}
	if err := s.Apply(ctx); err == nil || err.Error() != "KyIdentity's kymessage connector has not delivered its directory to kymessage (pending, 2 pending, 0 failed); until it does, offboarding someone may not reach chat. Fix the connector, then rerun apply" {
		t.Fatalf("undelivered: %v", err)
	}
	detail = ""
	if err := s.Apply(ctx); err != nil || s.Verify(ctx) != nil || s.ID() != "kymessage.directory" {
		t.Fatalf("delivered: %v", err)
	}
	// A refused delivery is resynced until it is delivered.
	retry := directoryRetry
	directoryRetry = 0
	t.Cleanup(func() { directoryRetry = retry })
	details := []string{"failed, 3 pending, 1 failed", "failed, 1 pending, 1 failed", ""}
	s.sync = func(context.Context) (string, string) { d := details[0]; details = details[1:]; return "kymessage", d }
	if err := s.Apply(ctx); err != nil || len(details) != 0 {
		t.Fatalf("failed, then delivered: %v, %d left", err, len(details))
	}
}

// AC24: the handover names what a human must know and do.
func TestKyMessageHandover(t *testing.T) {
	f := newMessageFixture(t)
	messageStackPlan(t, f, nil)
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"chat at https://chat.example.com/ (Element)", "console at https://message.example.com/", "Server name example.com", "@<the local part of a verified email>:example.com",
		"everyday accounts only (owner)", "owner-admin signs in to the console alone and has no chat", `both "KyMessages" and "KyMessages chat"`, "email is verified",
		"192.0.2.1, TCP 7881 and UDP 7882, which are open to your LAN; no\n  TURN", "Federation is closed", "sign-out does not end chat sessions", "Offboarding", "ends their open chat sessions\n  within seconds", `"KyMessages chat" alone is locked out`,
		"docker logs kyq-kymessage-app-1 on nas", "cd '" + f.h.root + "/kymessage' && docker compose -p kyq-kymessage -f compose.yaml -f compose.kyq.yaml exec -T app /bin/sh -c 'eval \"$KYQ_SECRETS\" && exec /app/kymessages init-admin'",
		"No catalog product can pair with KyRecovery in this release", `replace the console's local "admin" break-glass password`, "the first lines of docker logs",
		"No mail relay is set, so only the owner's accounts are verified and nobody else\n  can get a chat account",
		`Removing someone from "KyMessages chat" alone stops only their new chat sign-ins`, "remove them from \"KyMessages\" too, or\n  offboard them"} {
		if !strings.Contains(s.handover, want) {
			t.Errorf("handover lacks %q:\n%s", want, s.handover)
		}
	}
	// With a relay, the handover names it instead.
	pw := filepath.Join(t.TempDir(), "smtp.password")
	os.WriteFile(pw, []byte("x\n"), 0o600)
	messageStackPlan(t, f, func(y string) string {
		return strings.Replace(y, "      clients: []\n", "      mail: {host: smtp.example.test, port: 587, security: starttls, from: identity@example.test, username: kyidentity, password_file: "+pw+"}\n      clients: []\n", 1)
	})
	if s, err = load(f.state, nil, f.o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.handover, "KyIdentity sends that mail through smtp.example.test:587.") || strings.Contains(s.handover, "No mail relay") {
		t.Errorf("handover with a relay:\n%s", s.handover)
	}
}
