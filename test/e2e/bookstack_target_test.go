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
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/stack"
	"golang.org/x/crypto/ssh"
)

// BookStack's two hostnames under the edge certificate's *.example.test.
var bookstackHosts = map[string]string{"": "wiki.example.test", "directory": "wiki-directory.example.test"}

const (
	bookstackSetupDir = "/data/installer/8a9b0c1d-2e3f-4a5b-8c6d-7e8f9a0b1c2d"
	bookstackOrigin   = "https://wiki.example.test:30443"
	// The disposable registry the receiver image is pushed to while no kyoffboard tag
	// publishes one: compose pull needs a registry digest.
	testRegistryImage = "registry:3@sha256:21078ecfcba8a0cd7e1a384f75b3509899661dd21823bf1da21b5a4674aaceb9"
)

// kyoffboardImage is the receiver image BookStack's manifest pins, or, while it is pending,
// this checkout's (KYQ_TEST_KYOFFBOARD_IMAGE, or built here) pushed to a disposable local
// registry: test plumbing, never written into the catalog. local reports the second.
func kyoffboardImage(t *testing.T, manifest string) (ref string, local bool) {
	t.Helper()
	if m := regexp.MustCompile(`(?m)^  kyoffboard: (\S+)$`).FindStringSubmatch(manifest); m != nil {
		return m[1], false
	}
	img := os.Getenv("KYQ_TEST_KYOFFBOARD_IMAGE")
	if img == "" {
		img = "kyq-e2e-kyoffboard-bookstack"
		start := time.Now()
		docker(t, "build", "-q", "--build-arg", "VERSION=e2e", "-f", "../../cmd/kyoffboard/Dockerfile", "-t", img, "../..")
		t.Logf("kyoffboard image build: %s", time.Since(start).Round(time.Second))
	}
	name := "kyq-e2e-registry-" + hex.EncodeToString(id16()[:4])
	t.Cleanup(func() { exec.Command("docker", "rm", "-fv", name).Run() })
	docker(t, "run", "-d", "--name", name, "--memory", "256m", "--memory-swap", "256m", "--cpus", "1", "--pids-limit", "64", "-p", "127.0.0.1::5000", testRegistryImage)
	_, port, _ := net.SplitHostPort(docker(t, "port", name, "5000"))
	reg := "127.0.0.1:" + port
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		r, err := http.Get("http://" + reg + "/v2/")
		if err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("registry at %s: %v", reg, err)
		}
	}
	tag := reg + "/kyoffboard:e2e"
	docker(t, "tag", img, tag)
	m := regexp.MustCompile(`digest: (sha256:[0-9a-f]{64})`).FindStringSubmatch(docker(t, "push", tag))
	if m == nil {
		t.Fatal("push of the receiver image named no digest")
	}
	ref = tag + "@" + m[1]
	t.Cleanup(func() { exec.Command("docker", "image", "rm", tag, reg+"/kyoffboard@"+m[1]).Run() })
	return ref, true
}

// bookstackTarget is the Docker target BookStack runs on: an sshd container in the host's
// network namespace on the host's Docker socket, signed in to as kyq (uid 4711), whose root
// the host shares, as KyMessages' is.
type bookstackTarget struct {
	name, port, root, fp string
	forward              string // the kind network's IPv4 gateway: this host, as kind reaches it
	ports                map[string]int
	relay                string
	receiver             string // the receiver image the catalog copy pins
	local                bool   // a local test build, not an attested release
}

// startBookStackTarget starts the target and copies the catalog BookStack into catalogDir
// with test-only plumbing: the receiver image pinned (kyoffboardImage), identity.example.test
// through a relay to the lan edge, and the edge's throwaway certificate as BookStack's CA
// bundle, which its OIDC discovery and token calls to KyIdentity use.
func startBookStackTarget(t *testing.T, work, catalogDir, nodeIP string, certPEM []byte) bookstackTarget {
	t.Helper()
	keys := filepath.Join(work, "bookstack-keys")
	os.Mkdir(keys, 0o700)
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(hostPriv, "")
	os.WriteFile(filepath.Join(keys, "host_ed25519"), pem.EncodeToMemory(block), 0o600)
	hostSigner, _ := ssh.NewSignerFromKey(hostPriv)
	_, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	clientSigner, _ := ssh.NewSignerFromKey(clientPriv)
	os.WriteFile(filepath.Join(keys, "authorized_keys"), ssh.MarshalAuthorizedKey(clientSigner.PublicKey()), 0o600)
	serveAgent(t, work, clientPriv)

	name := "kyq-e2e-bookstack-" + hex.EncodeToString(id16()[:4])
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
			for _, svc := range []string{"bookstack", "mariadb", "directory"} {
				logs, _ := exec.Command("docker", "logs", "--tail", "80", "kyq-bookstack-"+svc+"-1").CombinedOutput()
				t.Logf("bookstack %s logs:\n%s", svc, logs)
			}
		}
		exec.Command("docker", "rm", "-f", "kyq-bookstack-standard").Run()
		exec.Command("docker", "compose", "-p", "kyq-bookstack", "down").Run()
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
	for role := range bookstackHosts {
		l, err := net.Listen("tcp", net.JoinHostPort(gateway, "0"))
		if err != nil {
			t.Fatal(err)
		}
		ports[role] = l.Addr().(*net.TCPAddr).Port
		l.Close()
	}
	conf := filepath.Join(work, "bookstack-relay.conf")
	os.WriteFile(conf, []byte("events {}\nstream {\n  server {\n    listen 30443;\n    proxy_pass "+nodeIP+":30443;\n  }\n}\n"), 0o644)
	relay := name + "-relay"
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", relay).Run() })
	docker(t, "run", "-d", "--name", relay, "--network", "kind", "--memory", "64m", "--memory-swap", "64m", "-p", gateway+":30443:30443", "-v", conf+":/etc/nginx/nginx.conf:ro", relayImage)

	ca := filepath.Join(work, "bookstack-ca.pem")
	os.WriteFile(ca, certPEM, 0o644)
	dir := filepath.Join(catalogDir, "bookstack")
	if err := os.CopyFS(dir, os.DirFS("../../internal/catalog/apps/bookstack")); err != nil {
		t.Fatal(err)
	}
	manifest, _ := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	receiver, local := kyoffboardImage(t, string(manifest))
	if local {
		patch(t, filepath.Join(dir, "manifest.yaml"), [][2]string{
			{"pending_images: [kyoffboard]\n", ""},
			{"images:\n", "images:\n  kyoffboard: " + receiver + "\n"},
		})
	}
	patch(t, filepath.Join(dir, "compose.yaml.tmpl"), [][2]string{
		{"      - ./data/oidc_client_secret:/kyq/oidc_client_secret:ro\n", "      - ./data/oidc_client_secret:/kyq/oidc_client_secret:ro\n      - " + ca + ":/etc/ssl/certs/ca-certificates.crt:ro\n" +
			`    extra_hosts: ["` + catalogIdentityHost + ":" + gateway + `"]` + "\n"},
	})
	return bookstackTarget{name: name, port: port, root: base + "/root", fp: fp, forward: gateway, ports: ports, relay: relay, receiver: receiver, local: local}
}

func addCatalogBookStack(st *stack.Stack, certID int, d bookstackTarget) {
	port, _ := strconv.Atoi(d.port)
	st.Targets = append(st.Targets, stack.Target{Name: "nas", Host: "127.0.0.1", Port: port, User: "kyq", Root: d.root})
	st.Apps = append(st.Apps, stack.App{Name: "bookstack", Target: "nas"})
	for _, role := range []string{"", "directory"} {
		st.Edges[0].Routes = append(st.Edges[0].Routes, stack.Route{App: "bookstack", Role: role, Hostname: bookstackHosts[role],
			ForwardHost: d.forward, ForwardPort: d.ports[role], CertificateID: certID, StatusPath: "/status"})
	}
	st.Integrations = append(st.Integrations, stack.Integration{Product: "bookstack", Target: "nas",
		Bundle: bookstackSetupDir + "/setup.json", IdentityClient: "bookstack"})
}

// bookstackTransport dials BookStack's hostnames and KyIdentity at the lan edge's NodePort.
func bookstackTransport(t *testing.T, certPEM []byte, nodeIP string) *http.Transport {
	t.Helper()
	transport := driveTransport(t, certPEM, nodeIP)
	allowed := []string{catalogIdentityURLHost}
	for _, h := range bookstackHosts {
		allowed = append(allowed, h+":30443")
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if !slices.Contains(allowed, address) {
			return nil, errors.New("unselected destination " + address)
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(nodeIP, "30443"))
	}
	return transport
}

// argvWatch samples every process's command line on this host while a run lasts: the
// installer's SSH, docker and compose commands, and the containers' own processes.
type argvWatch struct {
	mu   sync.Mutex
	seen map[string]bool
	stop chan struct{}
	done sync.WaitGroup
}

func watchArgv() *argvWatch {
	w := &argvWatch{seen: map[string]bool{}, stop: make(chan struct{})}
	w.done.Add(1)
	go func() {
		defer w.done.Done()
		for {
			out, _ := exec.Command("ps", "-eo", "args").Output()
			w.mu.Lock()
			for _, l := range strings.Split(string(out), "\n") {
				w.seen[l] = true
			}
			w.mu.Unlock()
			select {
			case <-w.stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	return w
}

func (w *argvWatch) end() []string {
	close(w.stop)
	w.done.Wait()
	var out []string
	for l := range w.seen {
		out = append(out, l)
	}
	return out
}

// bookstackBrowser is one person's browser: KyIdentity's session and BookStack's, through the
// edge, never following a redirect.
type bookstackBrowser struct {
	t         *testing.T
	ctx       context.Context
	c         *http.Client
	csrf      string
	authorize *url.URL // the authorization request of its sign-in
}

// do sends one request to BookStack's origin (or an absolute URL), keeping the page's CSRF token.
func (b *bookstackBrowser) do(method, path string, body io.Reader, contentType string) (int, string, string) {
	b.t.Helper()
	u := path
	if strings.HasPrefix(path, "/") {
		u = bookstackOrigin + path
	}
	req, _ := http.NewRequestWithContext(b.ctx, method, u, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	r, err := b.c.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer r.Body.Close()
	page, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if m := regexp.MustCompile(`<meta name="token" content="([^"]+)">`).FindSubmatch(page); m != nil {
		b.csrf = string(m[1])
	}
	return r.StatusCode, r.Header.Get("Location"), string(page)
}

func (b *bookstackBrowser) get(path string) (int, string, string) { return b.do("GET", path, nil, "") }

// form posts form with the CSRF token of the last page.
func (b *bookstackBrowser) form(path string, form url.Values) (int, string) {
	b.t.Helper()
	form.Set("_token", b.csrf)
	code, loc, _ := b.do("POST", path, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	return code, loc
}

// signIn signs a person in to KyIdentity with their password and then to BookStack through
// KyIdentity, checking the authorization request's client and PKCE. ok is false when
// KyIdentity refuses the authorization; b.authorize keeps the request either way.
func bookstackSignIn(t *testing.T, ctx context.Context, transport http.RoundTripper, username string, password []byte) (b *bookstackBrowser, ok bool) {
	t.Helper()
	id := newIdentityAdmin(t, ctx, transport, username, password)
	b = &bookstackBrowser{t: t, ctx: ctx, c: id.c}
	b.c.Timeout = 60 * time.Second
	if code, _, page := b.get("/login"); code != 200 || !strings.Contains(page, `action="`+bookstackOrigin+`/oidc/login"`) {
		t.Fatalf("BookStack's sign-in page: %d %.300s", code, page)
	}
	code, loc := b.form("/oidc/login", url.Values{})
	authorize, err := url.Parse(loc)
	if code != 302 || err != nil {
		t.Fatalf("BookStack's OIDC start: %d %s", code, loc)
	}
	q := authorize.Query()
	if authorize.Host != catalogIdentityURLHost || q.Get("client_id") != "bookstack" || q.Get("redirect_uri") != bookstackOrigin+"/oidc/callback" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("response_type") != "code" {
		t.Fatalf("authorization request: %s", authorize)
	}
	b.authorize = authorize
	if !b.authorized() {
		return b, false
	}
	return b, true
}

// authorized replays the sign-in's authorization request with the KyIdentity session and, if
// KyIdentity answers with a code, completes BookStack's callback.
func (b *bookstackBrowser) authorized() bool {
	b.t.Helper()
	code, loc, _ := b.do("GET", b.authorize.String(), nil, "")
	callback, err := url.Parse(loc)
	if code != 302 || err != nil || callback.Query().Get("code") == "" || !strings.HasPrefix(loc, bookstackOrigin+"/oidc/callback") {
		b.t.Logf("KyIdentity refused the authorization: %d %.200s", code, loc)
		return false
	}
	if code, loc, page := b.get(loc); code != 302 || strings.Contains(loc, "/login") {
		b.t.Fatalf("BookStack's OIDC callback: %d %s %.300s", code, loc, page)
	}
	return true
}

// signedIn reports whether BookStack serves the person's own profile.
func (b *bookstackBrowser) signedIn() bool {
	code, _, _ := b.get("/my-account/profile")
	return code == 200
}

// createBook creates a book and returns its slug.
func (b *bookstackBrowser) createBook(name string) (int, string) {
	b.t.Helper()
	if code, _, _ := b.get("/create-book"); code != 200 {
		return code, ""
	}
	code, loc := b.form("/books", url.Values{"name": {name}, "description_html": {"<p>" + name + "</p>"}})
	if code != 302 || !strings.HasPrefix(loc, bookstackOrigin+"/books/") {
		return code, ""
	}
	return 200, strings.TrimPrefix(loc, bookstackOrigin+"/books/")
}

// createPage creates a page in book and returns its URL path.
func (b *bookstackBrowser) createPage(book, name, html string) (int, string) {
	b.t.Helper()
	code, draft, _ := b.get("/books/" + book + "/create-page")
	if code != 302 {
		return code, ""
	}
	draft = strings.TrimPrefix(draft, bookstackOrigin)
	if code, _, _ := b.get(draft); code != 200 {
		return code, ""
	}
	code, loc := b.form(draft, url.Values{"name": {name}, "html": {html}})
	if code != 302 || !strings.Contains(loc, "/page/") {
		return code, ""
	}
	return 200, strings.TrimPrefix(loc, bookstackOrigin)
}

// uploadImage uploads a small PNG to the gallery of page pageID and returns its URL path.
func (b *bookstackBrowser) uploadImage(pageID int) string {
	b.t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 2, color.RGBA{R: 200, A: 255})
	var file bytes.Buffer
	png.Encode(&file, img)
	var body bytes.Buffer
	m := multipart.NewWriter(&body)
	m.WriteField("_token", b.csrf)
	m.WriteField("uploaded_to", strconv.Itoa(pageID))
	w, _ := m.CreateFormFile("file", "pixel.png")
	w.Write(file.Bytes())
	m.Close()
	code, _, page := b.do("POST", "/images/gallery", &body, m.FormDataContentType())
	var res struct{ URL string }
	if code != 200 || json.Unmarshal([]byte(page), &res) != nil || !strings.HasPrefix(res.URL, bookstackOrigin+"/uploads/images/") {
		b.t.Fatalf("image upload: %d %.300s", code, page)
	}
	return strings.TrimPrefix(res.URL, bookstackOrigin)
}

// mintToken creates an API token for the signed-in person through BookStack's UI.
func (b *bookstackBrowser) mintToken() string {
	b.t.Helper()
	_, _, page := b.get("/my-account/auth")
	m := regexp.MustCompile(`/api-tokens/([0-9]+)/create`).FindStringSubmatch(page)
	if m == nil {
		b.t.Fatal("no token form on the account's page")
	}
	code, loc := b.form("/api-tokens/"+m[1]+"/create", url.Values{"name": {"mine"}, "expires_at": {""}})
	if code != 302 {
		b.t.Fatalf("token create: %d", code)
	}
	_, _, page = b.get(strings.TrimPrefix(loc, bookstackOrigin))
	id := regexp.MustCompile(`name="token_id"[^>]*value="([A-Za-z0-9]{32})"`).FindStringSubmatch(page)
	secret := regexp.MustCompile(`readonly="readonly" value="([A-Za-z0-9]{32})"`).FindStringSubmatch(page)
	if id == nil || secret == nil {
		b.t.Fatal("BookStack showed no new token")
	}
	return id[1] + ":" + secret[1]
}

// bookstackAPI calls BookStack's API through the edge with token.
func bookstackAPI(t *testing.T, ctx context.Context, transport http.RoundTripper, token, method, path string, v any) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, method, bookstackOrigin+"/api/"+path, nil)
	req.Header.Set("Authorization", "Token "+token)
	r, err := (&http.Client{Transport: transport, Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("API %s: %v", path, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if v != nil && r.StatusCode == 200 {
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("API %s: %v %.200s", path, err, b)
		}
	}
	return r.StatusCode
}

// bookstackAccount is the BookStack account bound to KyIdentity ID external, read with the
// service token: its ID and role names, sorted; ID 0 for none.
func bookstackAccount(t *testing.T, ctx context.Context, transport http.RoundTripper, token, external string) (int, []string) {
	t.Helper()
	var list struct{ Data []struct{ ID int } }
	if code := bookstackAPI(t, ctx, transport, token, "GET", "users?"+url.Values{"filter[external_auth_id]": {external}}.Encode(), &list); code != 200 {
		t.Fatalf("users: %d", code)
	}
	if len(list.Data) == 0 {
		return 0, nil
	}
	if len(list.Data) != 1 {
		t.Fatalf("%d BookStack accounts for %s", len(list.Data), external)
	}
	var u struct {
		Roles []struct {
			DisplayName string `json:"display_name"`
		}
	}
	bookstackAPI(t, ctx, transport, token, "GET", fmt.Sprint("users/", list.Data[0].ID), &u)
	roles := []string{}
	for _, r := range u.Roles {
		roles = append(roles, r.DisplayName)
	}
	slices.Sort(roles)
	return list.Data[0].ID, roles
}
