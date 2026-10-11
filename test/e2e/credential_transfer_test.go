//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"
)

type transferTLSRequests struct{ discovery, tokens, jwks atomic.Int64 }

func transferTLS(t *testing.T) (*httptest.Server, int, *transferTLSRequests) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	u, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusBadGateway) }
	director := proxy.Director
	proxy.Director = func(r *http.Request) { director(r); r.Header.Set("X-Forwarded-Proto", "https") }
	seen := &transferTLSRequests{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Count fixed endpoint names only; retain no credentials, query or body.
		switch r.Method + " " + r.URL.Path {
		case "GET /.well-known/openid-configuration":
			seen.discovery.Add(1)
		case "POST /oauth/token":
			seen.tokens.Add(1)
		case "GET /.well-known/jwks.json":
			seen.jwks.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, port, seen
}

// Real product binaries and TLS discovery, through the actual SSH installer.
// Linux host networking keeps the disposable TLS endpoints loopback-only.
func testCredentialTransfer(t *testing.T, ctx context.Context, originalState, hostFP string) {
	identityBinary, notesBinary := os.Getenv("KYQ_TEST_IDENTITY_BINARY"), os.Getenv("KYQ_TEST_NOTES_BINARY")
	if identityBinary == "" || notesBinary == "" {
		return
	}
	if !filepath.IsAbs(identityBinary) || !filepath.IsAbs(notesBinary) {
		t.Fatal("native fixture binaries must be absolute")
	}
	issuer, identityPort, issuerRequests := transferTLS(t)
	destination, notesPort, _ := transferTLS(t)
	// httptest's certificate names example.com. Resolve it only inside the owned
	// containers; retain native refusal of literal-loopback setup URLs.
	issuerURL := strings.Replace(issuer.URL, "127.0.0.1", "example.com", 1)
	destinationURL := strings.Replace(destination.URL, "127.0.0.1", "example.com", 1)
	identityData, notesData := t.TempDir(), t.TempDir()
	ca := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuer.Certificate().Raw}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: destination.Certificate().Raw})...)
	for _, data := range []string{identityData, notesData} {
		if err := os.WriteFile(filepath.Join(data, "fixture-ca.pem"), ca, 0600); err != nil {
			t.Fatal(err)
		}
	}
	settings := fmt.Sprintf("data_dir: /data\nserver:\n  bind: 127.0.0.1:%d\n  behind_proxy: true\n  trusted_proxies: [127.0.0.1/32]\n", notesPort)
	if err := os.WriteFile(filepath.Join(notesData, "kynotes.yaml"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()[:8]
	identityName, notesName := "kyq-transfer-identity-"+suffix, "kyq-transfer-notes-"+suffix
	start := func(name, binary, containerBinary, data string, environment []string, args ...string) {
		words := []string{"run", "-d", "--name", name, "--network", "host", "--add-host", "example.com:127.0.0.1", "--memory", "512m", "--memory-swap", "512m", "--cpus", "1", "--pids-limit", "128", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "-v", binary + ":" + containerBinary + ":ro", "-v", data + ":/data", "-e", "GOMAXPROCS=2", "-e", "GOMEMLIMIT=384MiB", "-e", "SSL_CERT_FILE=/data/fixture-ca.pem"}
		for _, e := range environment {
			words = append(words, "-e", e)
		}
		words = append(words, "--entrypoint", containerBinary, "kyq-e2e-sshd")
		words = append(words, args...)
		docker(t, words...)
		t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	}
	start(identityName, identityBinary, "/usr/local/bin/kyidentity", identityData, []string{"KYIDENTITY_DATA_DIR=/data", "KYIDENTITY_PORT=" + fmt.Sprint(identityPort), "KYIDENTITY_LISTEN_HOST=127.0.0.1", "KYIDENTITY_ISSUER_URL=" + issuerURL, "KYIDENTITY_ALLOW_PRIVATE_CALLBACKS=true", "TRUSTED_PROXY_CIDRS=127.0.0.1/32"})
	start(notesName, notesBinary, "/kynotes-server", notesData, nil, "--config", "/data/kynotes.yaml")
	for _, endpoint := range []string{issuer.URL + "/healthz", destination.URL + "/healthz"} {
		client := issuer.Client()
		deadline := time.Now().Add(30 * time.Second)
		for {
			r, err := client.Get(endpoint)
			if err == nil {
				r.Body.Close()
				if r.StatusCode == 200 {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("native transfer fixture not ready")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	st, err := stack.Load(filepath.Join(originalState, "stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	st.Apps, st.Edges, st.Integrations = nil, nil, nil
	sourceTarget := st.Targets[0]
	sourceTarget.Name = "source"
	sourceTarget.Root += "-source"
	st.Targets = append(st.Targets, sourceTarget)
	st.Existing = []stack.Existing{{Product: "kyidentity", Target: "source", URL: issuerURL}, {Product: "kynotes", Target: "e2e", URL: destinationURL}}
	setupID := uuid.NewString()
	callback := destinationURL + "/api/v1/auth/oidc/callback"
	identity := &stack.IdentityPreparation{Schema: "kyidentity.setup/1", SetupID: setupID, Admin: stack.IdentityAccount{Username: "owner-admin", Email: "owner-admin@example.test"}, Everyday: stack.IdentityAccount{Username: "owner", Email: "owner@example.test"}, Clients: []stack.IdentityClient{{ID: "notes", Name: "KyNotes", RedirectURIs: []string{callback}, AdministratorRole: "kynotes.admin", EverydayRole: "kynotes.user", SuiteWebhook: &stack.IdentityWebhook{URL: destinationURL + "/api/v1/sync/events"}}}}
	st.Integrations = []stack.Integration{{Product: "kynotes", Target: "e2e", Container: notesName, Bundle: "/data/installer/setup.json", IdentityClient: "notes", Notes: &stack.NotesPreparation{Version: 1, SSO: &stack.NotesSSO{IssuerURL: issuerURL, ClientID: "notes", RedirectURI: callback, ClientSecretFile: "/data/installer/client.secret", DirectoryHMACSecretFile: "/data/installer/directory.secret"}}}, {Product: "kyidentity", Target: "source", Container: identityName, Bundle: "/data/installer/" + setupID + "/setup.json", Identity: identity}}
	// An unrelated edge has neither a usable connection nor an NPM token. Product
	// integration must not connect it or initialize its edge client.
	st.Targets = append(st.Targets, stack.Target{Name: "unrelated", Host: "unreachable.invalid", User: "root"})
	st.Apps = []stack.App{{Name: "hello", Target: "unrelated"}}
	st.Edges = []stack.Edge{{Target: "unrelated", Provider: "cloudflare", NPMURL: "https://npm.unreachable.invalid", ConnectorCIDR: "192.0.2.1/32", Routes: []stack.Route{{App: "hello", Hostname: "hello.example.test", ForwardHost: "hello", ForwardPort: 8080, CertificateID: 1}}}}
	state := t.TempDir()
	raw, err := yaml.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "stack.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(check bool) ([]byte, error) {
		args := []string{"integrate-stack", "--state", state, "--trust-host-key", "e2e=" + hostFP, "--trust-host-key", "source=" + hostFP}
		if check {
			args = append(args, "--check")
		}
		var out bytes.Buffer
		err := cli.Run(ctx, args, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "e2e", Out: &out})
		return out.Bytes(), err
	}
	if out, err := run(true); err == nil || len(out) != 0 {
		t.Fatal("absent transfer inspected as ready")
	}
	ownerSubject := ""
	var reports []byte
	for _, check := range []bool{false, true, false} {
		out, err := run(check)
		if err != nil {
			t.Fatal("native SSH credential transfer failed", err)
		}
		if !bytes.Contains(out, []byte(`"product":"kynotes"`)) || !bytes.Contains(out, []byte(`"section":"sso"`)) {
			t.Fatal("native SSO setup was not verified")
		}
		reports = append(reports, out...)
		decoder := json.NewDecoder(bytes.NewReader(out))
		for {
			var row struct {
				Product string
				Report  struct {
					Created, Reused []struct{ Kind, Name, ID string }
				}
			}
			if err := decoder.Decode(&row); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal("unreadable sanitized product reports")
			}
			if row.Product == "kyidentity" {
				for _, item := range append(row.Report.Created, row.Report.Reused...) {
					if item.Kind == "user" && item.Name == "owner-admin" {
						ownerSubject = item.ID
					}
				}
			}
		}
	}
	clientSecret, err := os.ReadFile(filepath.Join(identityData, "installer", setupID, "client-notes.secret"))
	if err != nil {
		t.Fatal(err)
	}
	directorySecret, err := os.ReadFile(filepath.Join(identityData, "installer", setupID, "client-notes.webhook"))
	if err != nil {
		t.Fatal(err)
	}
	privateValues := [][]byte{clientSecret, directorySecret}
	for _, name := range []string{"admin.password", "everyday.password"} {
		value, err := os.ReadFile(filepath.Join(identityData, "installer", setupID, name))
		if err != nil || len(value) == 0 {
			t.Fatal("native account credential missing")
		}
		privateValues = append(privateValues, value)
	}
	defer func() {
		for _, value := range privateValues {
			clear(value)
		}
	}()
	for _, value := range privateValues {
		if bytes.Contains(reports, value) {
			t.Fatal("private credential entered installer reports")
		}
	}
	for _, pair := range []struct {
		name   string
		secret []byte
	}{{"client.secret", clientSecret}, {"directory.secret", directorySecret}} {
		actual, err := os.ReadFile(filepath.Join(notesData, "installer", pair.name))
		if err != nil || !bytes.Equal(actual, pair.secret) {
			t.Fatal("native import changed source credential bytes")
		}
	}
	inspectState := func() {
		if err := filepath.WalkDir(state, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				for _, value := range privateValues {
					if bytes.Contains(raw, value) {
						t.Fatal("private credential persisted in workstation state")
					}
				}
			}
			return nil
		}); err != nil {
			t.Fatal("could not inspect all workstation state", err)
		}
	}
	// The native owner binding exists only in the destination product bundle.
	bundle, err := os.ReadFile(filepath.Join(notesData, "installer", "setup.json"))
	if err != nil {
		t.Fatal(err)
	}
	var native struct {
		Admins []stack.NotesAdmin `json:"admins"`
	}
	if json.Unmarshal(bundle, &native) != nil || len(native.Admins) != 1 || native.Admins[0].Username != "owner-admin" || native.Admins[0].Issuer != issuerURL || ownerSubject == "" || native.Admins[0].Subject != ownerSubject {
		t.Fatal("native owner binding missing")
	}
	// Changed destination bytes refuse inspection; no automatic overwrite/re-key.
	path := filepath.Join(notesData, "installer", "client.secret")
	if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, 43), 0600); err != nil {
		t.Fatal(err)
	}
	for _, check := range []bool{true, false} {
		if _, err := run(check); err == nil {
			t.Fatal("changed imported credentials accepted")
		}
	}
	if retained, _ := os.ReadFile(path); !bytes.Equal(retained, bytes.Repeat([]byte{'x'}, 43)) {
		t.Fatal("inspection repaired changed credentials")
	}
	if err := os.WriteFile(path, clientSecret, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(true); err != nil {
		t.Fatal("retained source/destination could not be reinspected", err)
	}
	issuerRequests.discovery.Store(0)
	issuerRequests.tokens.Store(0)
	issuerRequests.jwks.Store(0)
	testInstallerCredentialSignOn(t, ctx, issuerURL, destinationURL, identityData, setupID, ca)
	if issuerRequests.discovery.Load() < 1 || issuerRequests.tokens.Load() != 2 || issuerRequests.jwks.Load() < 1 {
		t.Fatal("native discovery, confidential token exchange or JWKS verification missing")
	}
	for _, check := range []bool{true, false} {
		out, err := run(check)
		if err != nil {
			t.Fatal("native setup could not resume after real sign-on", err)
		}
		for _, value := range privateValues {
			if bytes.Contains(out, value) {
				t.Fatal("private credential entered post-sign-on resume report")
			}
		}
	}
	inspectState()
	t.Log("actual SSH private source/destination transfer, native TLS SSO setup, owner binding, resume, drift refusal and unrelated-edge exclusion passed")
}
