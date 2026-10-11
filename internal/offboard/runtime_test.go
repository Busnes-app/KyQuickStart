package offboard

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func runtimeCertificate(t *testing.T, modify ...func(*x509.Certificate)) ([]byte, []byte, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	for _, change := range modify {
		change(template)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), certificate
}

func runtimeFixture(t *testing.T) (string, runtimeFile, *x509.Certificate, *atomic.Int32) {
	t.Helper()
	directory := stateDir(t)
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	state := stateDir(t)
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	b := fixtureBinding()
	productToken := strings.Repeat("ab", 32)
	mutations := &atomic.Int32{}
	native := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+productToken {
			w.WriteHeader(401)
			return
		}
		var input struct {
			Action, Issuer, Subject string
			ID                      int64 `json:"user_id"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.ID != b.NativeID || input.Issuer != b.Issuer || input.Subject != b.Subject {
			w.WriteHeader(400)
			return
		}
		if input.Action == "deprovision" {
			mutations.Add(1)
		}
		complete := mutations.Load() > 0
		tokens := 1
		if complete {
			tokens = 0
		}
		sum := sha256.Sum256([]byte(b.Issuer + "\x00" + b.Subject))
		json.NewEncoder(w).Encode(map[string]any{"contract": "ky.bookstack-offboard/1", "module_version": "0.15.0", "user_id": b.NativeID, "binding_digest": hex.EncodeToString(sum[:]), "disabled": complete, "revocation_complete": complete, "tokens_remaining": tokens})
	}))
	t.Cleanup(native.Close)
	trustFixture(t, native)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	cert, key, leaf := runtimeCertificate(t)
	for name, data := range map[string][]byte{"server.pem": cert, "server-key.pem": key, "product-token": []byte(productToken + "\n"), "scim-token": []byte(strings.Repeat("cd", 32) + "\n"), "product-ca.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: native.Certificate().Raw})} {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := runtimeFile{Schema: "ky.offboarding-runtime/1", SetupID: b.SetupID, Listen: address, StateDir: state, CertFile: "server.pem", KeyFile: "server-key.pem"}
	cfg.Apps = append(cfg.Apps, runtimeAppFile{App: b.App, Kind: b.Kind, Origin: native.URL, Issuer: b.Issuer, TokenFile: "product-token", SCIMFile: "scim-token"})
	file := filepath.Join(directory, "config.json")
	writeRuntime(t, file, cfg)
	return file, cfg, leaf, mutations
}
func writeRuntime(t *testing.T, file string, cfg runtimeFile) {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestBookStackRuntimeCreationNeedsDistinctProtectedAuthority(t *testing.T) {
	for _, mode := range []string{"enabled", "not-opted-in", "missing-file", "reused-file", "reused-token", "unsafe-file", "symlink", "foreign-kind"} {
		t.Run(mode, func(t *testing.T) {
			file, cfg, _, mutations := runtimeFixture(t)
			root := filepath.Dir(file)
			cfg.Apps[0].Directory = &runtimeDirectoryFile{AdministratorRole: "admin", EverydayRole: "editor", CreateAccounts: true, TokenFile: "directory-token", CreationTokenFile: "creation-token", BookStack: &BookStackDirectoryConfig{ClientID: "books-client", OrdinaryRoleIDs: []int64{2}, Accounts: map[string]BookStackAccount{"owned": {ID: 2, SetupID: "43f864f0-991c-43bf-8b94-d93c2916a98f"}}}}
			for name, token := range map[string]string{"directory-token": strings.Repeat("ef", 32), "creation-token": strings.Repeat("01", 32)} {
				if os.WriteFile(filepath.Join(root, name), []byte(token), 0600) != nil {
					t.Fatal("test credential staging failed")
				}
			}
			switch mode {
			case "not-opted-in":
				cfg.Apps[0].Directory.CreateAccounts = false
			case "missing-file":
				cfg.Apps[0].Directory.CreationTokenFile = ""
			case "reused-file":
				cfg.Apps[0].Directory.CreationTokenFile = "directory-token"
			case "reused-token":
				os.WriteFile(filepath.Join(root, "creation-token"), []byte(strings.Repeat("cd", 32)), 0600)
			case "unsafe-file":
				os.Chmod(filepath.Join(root, "creation-token"), 0644)
			case "symlink":
				os.Remove(filepath.Join(root, "creation-token"))
				os.Symlink("directory-token", filepath.Join(root, "creation-token"))
			case "foreign-kind":
				cfg.Apps[0].Kind = "forgejo"
				cfg.Apps[0].SourceID = 1
			}
			writeRuntime(t, file, cfg)
			loaded, err := LoadRuntime(file)
			if mode == "enabled" || mode == "not-opted-in" {
				if err != nil {
					t.Fatal("protected scope lookup unavailable", err)
				}
				if (loaded.apps[0].target.create != nil) != (mode == "enabled") || (loaded.apps[0].target.absent != nil) != (mode == "enabled") {
					t.Fatal("creation opt-in not enforced")
				}
			} else if !errors.Is(err, ErrRuntime) {
				t.Fatal("unsafe authority accepted", err)
			}
			files, err := os.ReadDir(cfg.StateDir)
			if err != nil || len(files) != 0 || mutations.Load() != 0 {
				t.Fatal("loading changed state")
			}
		})
	}
}

func TestRuntimeLoadIsPrivateReadOnlyAndRedactsReports(t *testing.T) {
	file, cfg, _, mutations := runtimeFixture(t)
	s := openStore(t, cfg.StateDir)
	defer s.Close()
	runtime, err := LoadRuntime(file)
	if err != nil {
		t.Fatal(err)
	}
	if mutations.Load() != 0 {
		t.Fatal("check mutated native service")
	}
	files, err := os.ReadDir(cfg.StateDir)
	if err != nil || len(files) != 0 {
		t.Fatal("check wrote state")
	}
	report := runtime.Report()
	if report.Schema != cfg.Schema || report.Status != "valid" || report.Applications != 1 {
		t.Fatal("unsafe report")
	}
	data, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := fmt.Sprintf("%v %+v %#v %v %+v %#v %s", runtime, runtime, runtime, *runtime, *runtime, *runtime, data)
	if strings.Contains(diagnostics, strings.Repeat("ab", 32)) || strings.Contains(diagnostics, strings.Repeat("cd", 32)) {
		t.Fatal("runtime formatting exposed credentials")
	}
	for _, target := range runtime.apps {
		foreign := fixtureBinding()
		foreign.Origin = target.target.spec.Origin
		foreign.SetupID = "371333e2-c221-4601-b249-563b3d828530"
		if target.target.matches(foreign) {
			t.Fatal("runtime accepted another installation")
		}
	}
}

func TestRuntimeImmichConfigurationIsReadOnly(t *testing.T) {
	file, cfg, _, mutations := runtimeFixture(t)
	cfg.Apps[0].Kind, cfg.Apps[0].ClientID = "immich", "photos-client"
	writeRuntime(t, file, cfg)
	runtime, err := LoadRuntime(file)
	if err != nil || len(runtime.apps) != 1 || runtime.apps[0].target.spec.Kind != "immich" || runtime.apps[0].target.spec.ClientID != "photos-client" || mutations.Load() != 0 {
		t.Fatal("protected Immich intent failed read-only loading", err)
	}
	entries, err := os.ReadDir(cfg.StateDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("Immich configuration check created state", err)
	}
}

func TestRuntimeRejectsUnsafeFilesAndIntent(t *testing.T) {
	for _, mode := range []string{"config mode", "token mode", "key mode", "directory mode", "symlink", "escape", "same token", "inline token", "duplicate schema", "duplicate app", "origin alias", "unknown kind", "foreign client pin", "immich missing client", "immich source injection", "immich creation", "state overlap", "bad listen", "bad setup", "oversize", "wrong key", "expired certificate", "CA key", "client certificate"} {
		t.Run(mode, func(t *testing.T) {
			file, cfg, _, mutations := runtimeFixture(t)
			directory := filepath.Dir(file)
			switch mode {
			case "foreign client pin":
				cfg.Apps[0].ClientID = "photos-client"
				writeRuntime(t, file, cfg)
			case "immich missing client":
				cfg.Apps[0].Kind = "immich"
				writeRuntime(t, file, cfg)
			case "immich source injection":
				cfg.Apps[0].Kind, cfg.Apps[0].ClientID, cfg.Apps[0].SourceID = "immich", "photos-client", 1
				writeRuntime(t, file, cfg)
			case "immich creation":
				cfg.Apps[0].Kind, cfg.Apps[0].ClientID = "immich", "photos-client"
				cfg.Apps[0].Directory = &runtimeDirectoryFile{CreateAccounts: true}
				writeRuntime(t, file, cfg)
			case "config mode":
				os.Chmod(file, 0644)
			case "token mode":
				os.Chmod(filepath.Join(directory, "product-token"), 0644)
			case "key mode":
				os.Chmod(filepath.Join(directory, "server-key.pem"), 0644)
			case "directory mode":
				os.Chmod(directory, 0755)
			case "symlink":
				os.Remove(filepath.Join(directory, "product-token"))
				os.Symlink("scim-token", filepath.Join(directory, "product-token"))
			case "escape":
				cfg.Apps[0].TokenFile = "../product-token"
				writeRuntime(t, file, cfg)
			case "same token":
				os.WriteFile(filepath.Join(directory, "scim-token"), []byte(strings.Repeat("ab", 32)), 0600)
			case "inline token":
				data, _ := os.ReadFile(file)
				data = append(data[:len(data)-1], []byte(`,"token":"PRIVATE-CANARY"}`)...)
				os.WriteFile(file, data, 0600)
			case "duplicate schema":
				data, _ := os.ReadFile(file)
				data = append(data[:len(data)-1], []byte(`,"schema":"ky.offboarding-runtime/1"}`)...)
				os.WriteFile(file, data, 0600)
			case "duplicate app":
				cfg.Apps = append(cfg.Apps, cfg.Apps[0])
				writeRuntime(t, file, cfg)
			case "origin alias":
				cfg.Apps[0].Origin = "https://EXAMPLE.test"
				other := cfg.Apps[0]
				other.App, other.Origin, other.TokenFile, other.SCIMFile = "other", "https://example.test.:443", "other-product", "other-scim"
				cfg.Apps = append(cfg.Apps, other)
				for name, token := range map[string]string{"other-product": strings.Repeat("ef", 32), "other-scim": strings.Repeat("de", 32)} {
					if err := os.WriteFile(filepath.Join(directory, name), []byte(token), 0600); err != nil {
						t.Fatal(err)
					}
				}
				writeRuntime(t, file, cfg)
			case "unknown kind":
				cfg.Apps[0].Kind = "unknown"
				writeRuntime(t, file, cfg)
			case "state overlap":
				cfg.StateDir = directory
				writeRuntime(t, file, cfg)
			case "bad listen":
				cfg.Listen = "bridge.example.test:8443"
				writeRuntime(t, file, cfg)
			case "bad setup":
				cfg.SetupID = "foreign"
				writeRuntime(t, file, cfg)
			case "oversize":
				os.WriteFile(filepath.Join(directory, "product-token"), []byte(strings.Repeat("a", 16385)), 0600)
			case "wrong key":
				_, key, _ := runtimeCertificate(t)
				os.WriteFile(filepath.Join(directory, "server-key.pem"), key, 0600)
			case "expired certificate", "CA key", "client certificate":
				cert, key, _ := runtimeCertificate(t, func(c *x509.Certificate) {
					switch mode {
					case "expired certificate":
						c.NotBefore, c.NotAfter = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
					case "CA key":
						c.IsCA = true
						c.KeyUsage |= x509.KeyUsageCertSign
					case "client certificate":
						c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
					}
				})
				for name, data := range map[string][]byte{"server.pem": cert, "server-key.pem": key} {
					if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := LoadRuntime(file); !errors.Is(err, ErrRuntime) || strings.Contains(err.Error(), "PRIVATE-CANARY") {
				t.Fatal("unsafe runtime accepted or leaked", err)
			}
			if mutations.Load() != 0 {
				t.Fatal("invalid configuration invoked product")
			}
		})
	}
}

func TestRuntimeTLSWorkerAndJoinedShutdown(t *testing.T) {
	file, cfg, leaf, mutations := runtimeFixture(t)
	runtime, err := LoadRuntime(file)
	if err != nil {
		t.Fatal(err)
	}
	s := openStore(t, cfg.StateDir)
	b := fixtureBinding()
	b.Origin = cfg.Apps[0].Origin
	id, err := s.Register(context.Background(), runtime.apps[0].target, b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Request(id, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runtime.Serve(ctx) }()
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	client := &http.Client{Transport: transport, Timeout: time.Second}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, _ := http.NewRequest("GET", "https://"+cfg.Listen+"/scim/wiki/Users/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("cd", 32))
		response, err := client.Do(req)
		if err == nil {
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			var resource struct{ Active *bool }
			if err != nil || response.StatusCode != 200 || json.Unmarshal(data, &resource) != nil || resource.Active == nil {
				t.Fatal("invalid TLS observation")
			}
			if !*resource.Active {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime did not serve verified completion", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if mutations.Load() != 1 {
		t.Fatal("runtime native dispatch did not complete exactly once")
	}
	if duplicate, err := Open(cfg.StateDir); err == nil {
		duplicate.Close()
		t.Fatal("runtime lost exclusive state lease")
	}
	if err := runtime.Serve(context.Background()); !errors.Is(err, ErrRuntime) {
		t.Fatal("second runtime accepted")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("runtime shutdown failed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runtime shutdown did not join")
	}
	s = openStore(t, cfg.StateDir)
	defer s.Close()
	job, present, err := s.Inspect(id)
	if err != nil || !present || job.Status != "complete" || job.Attempts != 1 {
		t.Fatal("runtime lost completion proof", err)
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatal("runtime listener not released")
	}
	listener.Close()
}

func TestRuntimeCommandReadOnlyCheck(t *testing.T) {
	file, cfg, _, mutations := runtimeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "go", "run", "../../cmd/kyoffboard", "--config", file, "--check").Output()
	if err != nil {
		t.Fatal("runtime command check failed")
	}
	var report RuntimeReport
	if json.Unmarshal(output, &report) != nil || report.Schema != cfg.Schema || report.Applications != 1 || report.Status != "valid" {
		t.Fatal("runtime command report invalid")
	}
	files, err := os.ReadDir(cfg.StateDir)
	if err != nil || len(files) != 0 || mutations.Load() != 0 {
		t.Fatal("command check changed native/state data")
	}
}

func TestRuntimeBinaryTLSAndSIGTERM(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux resident target standard-trust fixture")
	}
	file, cfg, leaf, mutations := runtimeFixture(t)
	loaded, err := LoadRuntime(file)
	if err != nil {
		t.Fatal(err)
	}
	s := openStore(t, cfg.StateDir)
	b := fixtureBinding()
	b.Origin = cfg.Apps[0].Origin
	id, err := s.Register(context.Background(), loaded.apps[0].target, b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Request(id, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	binary := filepath.Join(t.TempDir(), "kyoffboard")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/kyoffboard").Run(); err != nil {
		t.Fatal("resident binary build failed")
	}
	command := exec.CommandContext(ctx, binary, "--config", file)
	command.Env = append(os.Environ(), "SSL_CERT_FILE="+filepath.Join(filepath.Dir(file), "product-ca.pem")) // Public fixture trust only.
	if err := command.Start(); err != nil {
		t.Fatal("resident binary start failed")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			command.Process.Kill()
			<-done
		}
	}()
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	client := &http.Client{Transport: transport, Timeout: time.Second}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, _ := http.NewRequest("GET", "https://"+cfg.Listen+"/scim/wiki/Users/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("cd", 32))
		response, err := client.Do(req)
		if err == nil {
			data, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			var resource struct{ Active *bool }
			if readErr != nil || response.StatusCode != 200 || json.Unmarshal(data, &resource) != nil || resource.Active == nil {
				t.Fatal("binary TLS response invalid")
			}
			if !*resource.Active {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("binary did not complete native wire revocation")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if mutations.Load() != 1 {
		t.Fatal("binary native dispatch repeated")
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatal("SIGTERM did not exit cleanly")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("binary did not join on SIGTERM")
	}
	s = openStore(t, cfg.StateDir)
	defer s.Close()
	job, present, err := s.Inspect(id)
	if err != nil || !present || job.Status != "complete" || job.Attempts != 1 {
		t.Fatal("binary lost completion proof", err)
	}
}

func TestRuntimeCancelsAndJoinsHTTPDeliveryBeforeStateClose(t *testing.T) {
	file, cfg, leaf, _ := runtimeFixture(t)
	runtime, err := LoadRuntime(file)
	if err != nil {
		t.Fatal(err)
	}
	s := openStore(t, cfg.StateDir)
	b := fixtureBinding()
	b.Origin = cfg.Apps[0].Origin
	id, err := s.Register(context.Background(), runtime.apps[0].target, b)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	started := make(chan struct{}, 1)
	var active atomic.Bool
	runtime.apps[0].target.revoke = func(ctx context.Context, _ Binding) error {
		active.Store(true)
		defer active.Store(false)
		started <- struct{}{}
		<-ctx.Done()
		if unowned, err := Open(cfg.StateDir); err == nil {
			unowned.Close()
			t.Error("state lease released before HTTP/native join")
		}
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runtime.Serve(ctx) }()
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	path := "https://" + cfg.Listen + "/scim/wiki/Users/" + id
	request := func(method, body string) error {
		req, _ := http.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("cd", 32))
		req.Header.Set("Content-Type", "application/scim+json")
		response, err := client.Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		io.Copy(io.Discard, response.Body)
		return nil
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if request("GET", "") == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	requestDone := make(chan error, 1)
	go func() { requestDone <- request("PATCH", deactivateBody) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP native delivery did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("shutdown failed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runtime did not join HTTP native delivery")
	}
	if active.Load() {
		t.Fatal("runtime returned with native call active")
	}
	<-requestDone
	s = openStore(t, cfg.StateDir)
	defer s.Close()
	job, present, err := s.Inspect(id)
	if err != nil || !present || job.Status != "pending" || job.Attempts != 1 {
		t.Fatal("HTTP cancellation lost native intent", job, err)
	}
}

// Native-container harness: protected configuration starts the real service and
// worker, while authenticated TLS reads only observe completion after backoff.
func resumeThroughRuntime(t *testing.T, s *Store, id, productToken string, next time.Time) *Store {
	t.Helper()
	b, err := s.Binding(id)
	if err != nil {
		t.Fatal(err)
	}
	statePath := s.root.Name()
	directory := stateDir(t)
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	cert, key, leaf := runtimeCertificate(t)
	for name, data := range map[string][]byte{"server.pem": cert, "server-key.pem": key, "product-token": []byte(productToken), "scim-token": []byte(strings.Repeat("cd", 32))} {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	cfg := runtimeFile{Schema: "ky.offboarding-runtime/1", SetupID: b.SetupID, Listen: address, StateDir: statePath, CertFile: "server.pem", KeyFile: "server-key.pem",
		Apps: []runtimeAppFile{{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, SourceID: b.SourceID, TokenFile: "product-token", SCIMFile: "scim-token"}}}
	file := filepath.Join(directory, "config.json")
	writeRuntime(t, file, cfg)
	runtime, err := LoadRuntime(file)
	if err != nil {
		t.Fatal("native private runtime load failed", err)
	}
	if delay := time.Until(next); delay < 0 || delay > 35*time.Second {
		t.Fatal("unexpected native runtime due time")
	}
	s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Serve(ctx) }()
	stopped := false
	defer func() {
		if !stopped {
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("native runtime did not stop")
			}
		}
	}()
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	client := &http.Client{Transport: transport, Timeout: time.Second}
	defer client.CloseIdleConnections()
	sawPending := false
	deadline := next.Add(5 * time.Second)
	for {
		req, _ := http.NewRequest("GET", "https://"+address+"/scim/"+b.App+"/Users/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("cd", 32))
		response, err := client.Do(req)
		if err == nil {
			data, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			var resource struct {
				ID     string
				Active *bool
			}
			if readErr != nil || response.StatusCode != 200 || json.Unmarshal(data, &resource) != nil || resource.ID != id || resource.Active == nil {
				t.Fatal("native runtime observation invalid")
			}
			if *resource.Active {
				sawPending = true
			} else {
				if !sawPending || time.Now().Before(next) {
					t.Fatal("native runtime discarded backoff")
				}
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("native runtime did not complete retained intent")
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("native runtime shutdown failed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("native runtime did not join")
	}
	stopped = true
	return openStore(t, statePath)
}
