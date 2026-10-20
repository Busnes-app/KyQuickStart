package offboard

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func immichRuntime(t *testing.T) (string, runtimeFile, *immichModule) {
	t.Helper()
	file, cfg, _, _ := runtimeFixture(t)
	m := &immichModule{b: immichBinding(), name: "Owner", email: "owner@example.test", admin: true, reactivateStatus: 200}
	m.serve(t)
	if os.WriteFile(filepath.Join(filepath.Dir(file), "product-token"), []byte("private-fixture-key\n"), 0600) != nil {
		t.Fatal("product key staging failed")
	}
	app := &cfg.Apps[0]
	app.App, app.Kind, app.Origin, app.ClientID = m.b.App, "immich", m.b.Origin, m.b.ClientID
	app.Directory = &runtimeDirectoryFile{AdministratorRole: "admin", EverydayRole: "user", CreateAccounts: true}
	app.Accounts = []runtimeAccountFile{{NativeUUID: m.b.NativeUUID, Subject: m.b.Subject}}
	cfg.SetupID = m.b.SetupID
	writeRuntime(t, file, cfg)
	return file, cfg, m
}

// serveUntilHealthy runs Serve until its own probe passes, then returns its stop function.
func serveUntilHealthy(t *testing.T, runtime *Runtime) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Serve(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.Healthcheck(context.Background()) != nil {
		select {
		case err := <-done:
			cancel()
			t.Fatal("runtime stopped before serving", err)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("runtime never became healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal("runtime shutdown failed", err)
		}
	}
}

func TestRuntimeRegistersImmichAccountsBeforeServing(t *testing.T) {
	file, cfg, m := immichRuntime(t)
	runtime, err := LoadRuntime(file)
	entries, _ := os.ReadDir(cfg.StateDir)
	if err != nil || m.calls != 0 || len(entries) != 0 {
		t.Fatal("loading accounts made native calls or wrote state", err, m.calls)
	}
	stop := serveUntilHealthy(t, runtime)
	stop()
	if runtime.Healthcheck(context.Background()) == nil {
		t.Fatal("healthcheck passed with no listener")
	}
	id := bindingID(m.b)
	s := openStore(t, cfg.StateDir)
	if b, err := s.Binding(id); err != nil || b != m.b || m.calls == 0 {
		t.Fatal("setup account was not registered after native proof", err)
	}
	s.Close()
	record, err := os.ReadFile(filepath.Join(cfg.StateDir, id+".binding"))
	if err != nil {
		t.Fatal(err)
	}
	// A restart keeps the retained binding without another native proof.
	calls := m.calls
	stop = serveUntilHealthy(t, runtime)
	stop()
	if m.calls != calls {
		t.Fatal("restart re-proved a retained binding")
	}
	// A different binding for the same subject refuses before native I/O and before listening.
	cfg.Apps[0].Accounts[0].NativeUUID = "371333e2-c221-4601-b249-563b3d828531"
	writeRuntime(t, file, cfg)
	conflicting, err := LoadRuntime(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := conflicting.Serve(context.Background()); !errors.Is(err, ErrRuntime) || m.calls != calls {
		t.Fatal("conflicting registration accepted", err)
	}
	if after, err := os.ReadFile(filepath.Join(cfg.StateDir, id+".binding")); err != nil || !bytes.Equal(after, record) {
		t.Fatal("conflicting registration rewrote the binding")
	}
	if listener, err := net.Listen("tcp", cfg.Listen); err != nil {
		t.Fatal("refused runtime left its listener open")
	} else {
		listener.Close()
	}
}

func TestRuntimeImmichAccountsAndInternalOriginAreStrict(t *testing.T) {
	other := runtimeAccountFile{NativeUUID: "371333e2-c221-4601-b249-563b3d828531", Subject: "other-subject"}
	for name, change := range map[string]func(*runtimeAppFile){
		"duplicate uuid": func(a *runtimeAppFile) {
			a.Accounts = append(a.Accounts, runtimeAccountFile{a.Accounts[0].NativeUUID, "other-subject"})
		},
		"duplicate subject": func(a *runtimeAppFile) {
			a.Accounts = append(a.Accounts, runtimeAccountFile{other.NativeUUID, a.Accounts[0].Subject})
		},
		"bad uuid":      func(a *runtimeAppFile) { a.Accounts[0].NativeUUID = "../foreign" },
		"subject space": func(a *runtimeAppFile) { a.Accounts[0].Subject = "two words" },
		"too many accounts": func(a *runtimeAppFile) {
			for i := range 32 {
				a.Accounts = append(a.Accounts, runtimeAccountFile{fmt.Sprintf("371333e2-c221-4601-b249-5630000000%02d", i), fmt.Sprintf("subject-%d", i)})
			}
		},
		"public api origin":  func(a *runtimeAppFile) { a.APIOrigin = "http://immich.example.test" },
		"ip api origin":      func(a *runtimeAppFile) { a.APIOrigin = "http://127.0.0.1:2283" },
		"localhost origin":   func(a *runtimeAppFile) { a.APIOrigin = "http://localhost:2283" },
		"api origin path":    func(a *runtimeAppFile) { a.APIOrigin = "http://immich-server:2283/api" },
		"bookstack accounts": func(a *runtimeAppFile) { a.Kind, a.ClientID, a.Directory = "bookstack", "", nil },
		"bookstack apiorigin": func(a *runtimeAppFile) {
			a.Kind, a.ClientID, a.Directory, a.Accounts, a.APIOrigin = "bookstack", "", nil, nil, "http://wiki:80"
		},
	} {
		t.Run(name, func(t *testing.T) {
			file, cfg, m := immichRuntime(t)
			change(&cfg.Apps[0])
			writeRuntime(t, file, cfg)
			if _, err := LoadRuntime(file); !errors.Is(err, ErrRuntime) || m.calls != 0 {
				t.Fatal("invalid Immich runtime intent accepted", err)
			}
		})
	}
	file, cfg, m := immichRuntime(t)
	raw, _ := os.ReadFile(file)
	unknown := bytes.Replace(raw, []byte(`"subject":`), []byte(`"native_id":1,"subject":`), 1)
	if os.WriteFile(file, unknown, 0600) != nil {
		t.Fatal("config staging failed")
	}
	if _, err := LoadRuntime(file); !errors.Is(err, ErrRuntime) {
		t.Fatal("unknown account field accepted")
	}
	start := bytes.Index(raw, []byte(`"accounts":[`)) + len(`"accounts":`)
	end := start + bytes.IndexByte(raw[start:], ']') + 1
	empty := append(append(append([]byte{}, raw[:start]...), "[]"...), raw[end:]...)
	if os.WriteFile(file, empty, 0600) != nil {
		t.Fatal("config staging failed")
	}
	if _, err := LoadRuntime(file); !errors.Is(err, ErrRuntime) {
		t.Fatal("empty account list accepted")
	}
	cfg.Apps[0].APIOrigin = "http://immich-server:2283"
	cfg.Apps[0].Accounts = append(cfg.Apps[0].Accounts, other)
	writeRuntime(t, file, cfg)
	runtime, err := LoadRuntime(file)
	if err != nil || runtime.apps[0].target.spec.APIOrigin != "http://immich-server:2283" || runtime.apps[0].target.spec.Origin != m.b.Origin || len(runtime.apps[0].accounts) != 2 || m.calls != 0 {
		t.Fatal("internal API origin or second account refused", err)
	}
}

func TestSCIMTokenAcceptsHexOrCanonicalBase64URL(t *testing.T) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	canonical := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb, 0xef}, 16))
	last := strings.IndexByte(alphabet, canonical[42])
	for token, want := range map[string]bool{
		strings.Repeat("cd", 32): true, canonical: true,
		strings.Repeat("CD", 32): false, strings.Repeat("cd", 31): false, strings.Repeat("cd", 33): false,
		base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32)):    false, // padded
		base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32)):    false,
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 31)): false,
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 33)): false,
		canonical[:42] + string(alphabet[last|1]):                            false, // nonzero trailing bits
		canonical + "\n": false, "": false,
	} {
		if validSCIMToken(token) != want {
			t.Error("SCIM token encoding", token, want)
		}
	}
	file, cfg, _, _ := runtimeFixture(t)
	if os.WriteFile(filepath.Join(filepath.Dir(file), "scim-token"), []byte(canonical+"\n"), 0600) != nil {
		t.Fatal("token staging failed")
	}
	runtime, err := LoadRuntime(file)
	if err != nil || runtime.apps[0].token != canonical || len(cfg.Apps) != 1 {
		t.Fatal("installer base64url SCIM token refused", err)
	}
}

func TestInitTLSCreatesPrivateLeafOnce(t *testing.T) {
	file, cfg, _, _ := runtimeFixture(t)
	directory := filepath.Dir(file)
	cert, key := filepath.Join(directory, cfg.CertFile), filepath.Join(directory, cfg.KeyFile)
	original, _ := os.ReadFile(cert)
	// An existing valid pair is kept.
	if err := InitTLS(file); err != nil {
		t.Fatal(err)
	}
	if kept, _ := os.ReadFile(cert); !bytes.Equal(kept, original) {
		t.Fatal("existing certificate replaced")
	}
	// Exactly one file is refused and nothing is created.
	if os.Remove(cert) != nil {
		t.Fatal("cannot stage missing certificate")
	}
	if err := InitTLS(file); !errors.Is(err, ErrRuntime) {
		t.Fatal("lone key accepted", err)
	}
	if _, err := os.Stat(cert); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("certificate created beside a lone key")
	}
	if os.Remove(key) != nil {
		t.Fatal("cannot stage missing key")
	}
	if err := InitTLS(file); err != nil {
		t.Fatal(err)
	}
	created := map[string][]byte{}
	for _, path := range []string{cert, key} {
		st, err := os.Lstat(path)
		if err != nil || st.Mode().Perm() != 0600 || !st.Mode().IsRegular() {
			t.Fatal("generated TLS file is not private", path)
		}
		created[path], _ = os.ReadFile(path)
	}
	block, _ := pem.Decode(created[cert])
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	public, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || public.Curve != elliptic.P256() || leaf.IsCA || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "directory" ||
		len(leaf.IPAddresses) != 1 || !leaf.IPAddresses[0].Equal(net.ParseIP("127.0.0.1")) || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth ||
		leaf.NotAfter.Sub(leaf.NotBefore) > 826*24*time.Hour {
		t.Fatal("generated leaf is not a short-lived P-256 server leaf")
	}
	if _, err := LoadRuntime(file); err != nil {
		t.Fatal("generated pair does not load", err)
	}
	if err := InitTLS(file); err != nil {
		t.Fatal(err)
	}
	for path, raw := range created {
		if after, _ := os.ReadFile(path); !bytes.Equal(after, raw) {
			t.Fatal("repeated init rewrote", path)
		}
	}
	// An invalid existing pair is refused, not replaced.
	other, _, _ := runtimeCertificate(t)
	if os.WriteFile(cert, other, 0600) != nil {
		t.Fatal("cannot stage mismatched pair")
	}
	if err := InitTLS(file); !errors.Is(err, ErrRuntime) {
		t.Fatal("mismatched pair accepted", err)
	}
	if after, _ := os.ReadFile(cert); !bytes.Equal(after, other) {
		t.Fatal("invalid pair replaced")
	}
	// An unspecified listen address gets no IP SAN.
	for _, path := range []string{cert, key} {
		os.Remove(path)
	}
	cfg.Listen = "0.0.0.0:8443"
	writeRuntime(t, file, cfg)
	if err := InitTLS(file); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(cert)
	block, _ = pem.Decode(raw)
	if leaf, err := x509.ParseCertificate(block.Bytes); err != nil || len(leaf.IPAddresses) != 0 {
		t.Fatal("unspecified listen address entered the leaf")
	}
	entries, _ := os.ReadDir(cfg.StateDir)
	if len(entries) != 0 {
		t.Fatal("init wrote state")
	}
}

func TestHealthcheckRequiresAnUnauthenticatedRefusal(t *testing.T) {
	for status, healthy := range map[int]bool{401: true, 403: true, 200: false, 404: false, 503: false} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/scim/wiki/Users" || r.Header.Get("Authorization") != "" {
				t.Error("probe sent the wrong request")
			}
			w.WriteHeader(status)
		}))
		file, _, _, _ := runtimeFixture(t)
		runtime, err := LoadRuntime(file)
		if err != nil {
			t.Fatal(err)
		}
		runtime.listen = server.Listener.Addr().String()
		if err := runtime.Healthcheck(context.Background()); (err == nil) != healthy {
			t.Error("healthcheck for status", status, err)
		}
		server.Close()
	}
}
