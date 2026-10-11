package offboard

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type nativeDirectoryFixture struct {
	Container, Image, Token, DirectoryToken string
	CA                                      []byte
	Binding                                 Binding
	BookStack                               *BookStackDirectoryConfig
	AdditionalBindings                      []Binding
}

// Invoked only by the pinned Forgejo/actual KyIdentity fixture after its native
// app-role removal. That parent proves old-session denial before a fresh login.
func TestNativeForgejoDirectory(t *testing.T) {
	input := os.Getenv("KYQ_TEST_FORGEJO_DIRECTORY_JSON")
	if input == "" {
		t.Skip("run native Forgejo/identity fixture with KYQ_TEST_FORGEJO_DIRECTORY=1")
	}
	var fixture nativeDirectoryFixture
	if len(input) > 32768 || json.Unmarshal([]byte(input), &fixture) != nil || !regexp.MustCompile(`^kyq-forgejo-api-[0-9]+$`).MatchString(fixture.Container) ||
		!regexp.MustCompile(`^\S+@sha256:[0-9a-f]{64}$`).MatchString(fixture.Image) || validate(fixture.Binding) != nil ||
		fixture.Binding.Kind != "forgejo" || !strings.HasPrefix(fixture.Binding.Origin, "https://127.0.0.1:") {
		t.Fatal("unowned native directory fixture input")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Config}}", fixture.Container).Output()
	var config struct {
		Image  string
		Labels map[string]string
	}
	if err != nil || json.Unmarshal(data, &config) != nil || config.Image != fixture.Image || config.Labels["kyq.fixture"] != "forgejo" || config.Labels["kyq.fixture-owner"] != fixture.Container {
		t.Fatal("native directory fixture ownership unverified")
	}
	runNativeDirectory(t, fixture)
}

func runNativeDirectory(t *testing.T, fixture nativeDirectoryFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	previous := http.DefaultTransport
	nativeTransport := previous.(*http.Transport).Clone()
	nativeRoots := x509.NewCertPool()
	if !nativeRoots.AppendCertsFromPEM(fixture.CA) {
		t.Fatal("native directory fixture trust invalid")
	}
	nativeTransport.TLSClientConfig = &tls.Config{RootCAs: nativeRoots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = nativeTransport
	defer func() { http.DefaultTransport = previous; nativeTransport.CloseIdleConnections() }()

	certificate, key, leaf := runtimeCertificate(t)
	directory, state := stateDir(t), stateDir(t)
	for _, path := range []string{directory, state} {
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string][]byte{"server.pem": certificate, "server-key.pem": key, "product-token": []byte(fixture.Token), "scim-token": []byte(strings.Repeat("cd", 32))} {
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
	b := fixture.Binding
	cfg := runtimeFile{Schema: "ky.offboarding-runtime/1", SetupID: b.SetupID, Listen: address, StateDir: state, CertFile: "server.pem", KeyFile: "server-key.pem",
		Apps: []runtimeAppFile{{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, SourceID: b.SourceID, TokenFile: "product-token", SCIMFile: "scim-token",
			Directory: &runtimeDirectoryFile{AdministratorRole: "admin", EverydayRole: "editor"}}}}
	if b.Kind == "bookstack" {
		cfg.Apps[0].Directory.TokenFile = "directory-token"
		cfg.Apps[0].Directory.BookStack = fixture.BookStack
		if err := os.WriteFile(filepath.Join(directory, "directory-token"), []byte(fixture.DirectoryToken), 0600); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(directory, "runtime.json")
	writeRuntime(t, file, cfg)
	loaded, err := LoadRuntime(file)
	if err != nil || loaded.apps[0].target.directory == nil {
		t.Fatal("protected directory runtime failed", err)
	}
	s := openStore(t, state)
	id, err := s.Register(ctx, loaded.apps[0].target, b)
	if err != nil {
		t.Fatal("native directory binding failed", err)
	}
	for _, extra := range fixture.AdditionalBindings {
		if validate(extra) != nil || extra.App != b.App || extra.Kind != b.Kind || extra.SetupID != b.SetupID || extra.Origin != b.Origin || extra.Issuer != b.Issuer {
			t.Fatal("additional owned directory scope conflicts")
		}
		if _, err := s.Register(ctx, loaded.apps[0].target, extra); err != nil {
			t.Fatal("additional native directory registration failed", err)
		}
	}
	s.Close()
	serviceCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- loaded.Serve(serviceCtx) }()
	stopped := false
	defer func() {
		if !stopped {
			stop()
			<-done
		}
	}()
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	transport := nativeTransport.Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	endpoint := "https://" + address + "/scim/" + b.App + "/Users/" + id
	request := func(method string, body []byte) (int, []byte, error) {
		req, _ := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("cd", 32))
		req.Header.Set("Content-Type", "application/scim+json")
		response, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 16385))
		if len(data) > 16384 {
			return 0, nil, ErrState
		}
		return response.StatusCode, data, err
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		status, _, err := request(http.MethodGet, nil)
		if err == nil && status == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native directory runtime failed to listen")
		}
		time.Sleep(20 * time.Millisecond)
	}
	body, _ := json.Marshal(map[string]any{"schemas": []string{userSchema}, "externalId": b.Subject, "userName": directoryUserName(b),
		"displayName": "Directory owner 名", "active": true, "emails": []any{map[string]any{"value": "owner-directory@example.test", "type": "work", "primary": true}}, "roles": []any{}})
	if b.Kind == "bookstack" {
		var grant map[string]any
		if json.Unmarshal(body, &grant) != nil {
			t.Fatal("directory fixture encoding failed")
		}
		grant["roles"] = []any{map[string]string{"value": "admin"}}
		granted, _ := json.Marshal(grant)
		if status, _, err := request(http.MethodPut, granted); err != nil || status != http.StatusNoContent {
			t.Fatal("native resident grant failed", status, err)
		}
		status, raw, err := request(http.MethodGet, nil)
		var resource struct{ Roles []directoryValue }
		if err != nil || status != 200 || json.Unmarshal(raw, &resource) != nil || len(resource.Roles) != 1 || resource.Roles[0].Value != "admin" {
			t.Fatal("resident native administration not verified", status, err)
		}
	}
	for i := 0; i < 2; i++ {
		if status, _, err := request(http.MethodPut, body); err != nil || status != http.StatusNoContent {
			t.Fatal("native directory replacement failed", status, err)
		}
	}
	status, raw, err := request(http.MethodGet, nil)
	var resource struct {
		DisplayName string
		Roles       []directoryValue
		Active      bool
	}
	if err != nil || status != 200 || json.Unmarshal(raw, &resource) != nil || !resource.Active || resource.DisplayName != "Directory owner 名" || len(resource.Roles) != 1 || resource.Roles[0].Value != "editor" {
		t.Fatal("native directory profile/role not verified", status, err)
	}
	stop()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal("directory runtime failed to join", err)
	}
	stopped = true
	s = openStore(t, state)
	defer s.Close()
	job, err := s.directoryJob(id)
	attempts := uint32(2)
	if b.Kind == "bookstack" {
		attempts = 3
	}
	if err != nil || job.Status != "complete" || job.Attempt != attempts || job.Profile.Administrator {
		t.Fatal("native directory completion lost", err)
	}
	if retained, err := s.Binding(id); err != nil || retained != b {
		t.Fatal("native directory binding changed", err)
	}
	for _, extra := range fixture.AdditionalBindings {
		if retained, err := s.Binding(bindingID(extra)); err != nil || retained != extra {
			t.Fatal("additional native binding changed", err)
		}
	}
	for attempt := uint32(1); attempt <= attempts; attempt++ {
		for phase, outcome := range map[string]string{"started": "started", "result": "success"} {
			var audit directoryAudit
			if s.read(directoryAuditName(id, attempt, phase), &audit) != nil || audit.Schema != "ky.directory-audit/1" || audit.Binding != id || audit.Attempt != attempt || audit.Outcome != outcome {
				t.Fatal("native directory audit proof missing")
			}
		}
	}
	t.Log("real", b.Kind, "directory replacement, repeat, protected TLS runtime and reopened native binding/audit proofs passed")
}

func directoryUserName(b Binding) string {
	if b.Kind == "forgejo" {
		return b.Username
	}
	return b.Subject
}
