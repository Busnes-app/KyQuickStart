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

// The parent owns native products and performs only actual identity mutations.
// Configuration and a later shutdown message arrive on bounded stdin.
func TestNativeImmichProducerBridge(t *testing.T) {
	if os.Getenv("KYQ_TEST_IMMICH_PRODUCER") != "1" {
		t.Skip("run the owned Immich native producer fixture")
	}
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 65536))
	var raw json.RawMessage
	var input struct {
		Container, Image, Owner, Token, Listen, SCIMToken string
		NewSubject                                        string
		CA, Certificate, Key                              []byte
		Bindings                                          []Binding
		Cross                                             *struct {
			Container, Image, Token, SCIMToken string
			Bindings                           []Binding
		}
	}
	if decoder.Decode(&raw) != nil || !uniqueJSON(raw) || json.Unmarshal(raw, &input) != nil || len(input.Bindings) != 2 ||
		!regexp.MustCompile(`^kyq-immich-[0-9]+$`).MatchString(input.Owner) || input.Container != input.Owner+"-server" ||
		!regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(input.Image) {
		t.Fatal("unowned Immich producer input")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	configuration, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Config}}", input.Container).Output()
	var config struct {
		Image  string
		Labels map[string]string
	}
	if err != nil || json.Unmarshal(configuration, &config) != nil || config.Image != input.Image || config.Labels["kyq.fixture"] != "immich" || config.Labels["kyq.fixture-owner"] != input.Owner {
		t.Fatal("Immich producer deployment ownership unverified")
	}
	host, _, err := net.SplitHostPort(input.Listen)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		t.Fatal("Immich producer listener is not loopback")
	}
	b := input.Bindings[0]
	for _, binding := range input.Bindings {
		if validate(binding) != nil || binding.Kind != "immich" || !strings.HasPrefix(binding.Origin, "https://127.0.0.1:") || binding.App != b.App || binding.Origin != b.Origin || binding.SetupID != b.SetupID || binding.ClientID != b.ClientID || binding.Issuer != b.Issuer {
			t.Fatal("Immich producer binding scope conflicts")
		}
	}
	if b.Subject == input.Bindings[1].Subject || b.NativeUUID == input.Bindings[1].NativeUUID {
		t.Fatal("Immich producer identities are not distinct")
	}
	if input.NewSubject != "" && (!nativeUUID.MatchString(input.NewSubject) || input.NewSubject == b.Subject || input.NewSubject == input.Bindings[1].Subject) {
		t.Fatal("Immich producer creation subject conflicts")
	}
	if input.Cross != nil {
		c := input.Cross
		if c.Container != input.Owner+"-forgejo" || !regexp.MustCompile(`^[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}$`).MatchString(c.Image) || len(c.Bindings) != 2 || c.SCIMToken == input.SCIMToken || c.Token == input.Token {
			t.Fatal("cross-product protected input conflicts")
		}
		configuration, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Config}}", c.Container).Output()
		if err != nil || json.Unmarshal(configuration, &config) != nil || config.Image != c.Image || config.Labels["kyq.fixture"] != "forgejo" || config.Labels["kyq.fixture-owner"] != input.Owner {
			t.Fatal("cross-product deployment ownership unverified")
		}
		for i, binding := range c.Bindings {
			if validate(binding) != nil || binding.Kind != "forgejo" || binding.App != "git" || binding.SetupID != b.SetupID || binding.Issuer != b.Issuer || binding.Subject != input.Bindings[i].Subject || !strings.HasPrefix(binding.Origin, "https://127.0.0.1:") || binding.Origin != c.Bindings[0].Origin || binding.SourceID != c.Bindings[0].SourceID {
				t.Fatal("cross-product native binding scope conflicts")
			}
		}
	}
	previous := http.DefaultTransport
	transport := previous.(*http.Transport).Clone()
	transport.Proxy = nil
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(input.CA) {
		t.Fatal("Immich producer trust invalid")
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
	directory, state := stateDir(t), stateDir(t)
	for _, path := range []string{directory, state} {
		if os.Chmod(path, 0700) != nil {
			t.Fatal("Immich producer private directories failed")
		}
	}
	for name, data := range map[string][]byte{"server.pem": input.Certificate, "server-key.pem": input.Key, "product-token": []byte(input.Token), "scim-token": []byte(input.SCIMToken)} {
		if os.WriteFile(filepath.Join(directory, name), data, 0600) != nil {
			t.Fatal("Immich producer private runtime material failed")
		}
	}
	cfg := runtimeFile{Schema: "ky.offboarding-runtime/1", SetupID: b.SetupID, Listen: input.Listen, StateDir: state, CertFile: "server.pem", KeyFile: "server-key.pem", Apps: []runtimeAppFile{{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, ClientID: b.ClientID, TokenFile: "product-token", SCIMFile: "scim-token", Directory: &runtimeDirectoryFile{AdministratorRole: "admin", EverydayRole: "user", CreateAccounts: input.NewSubject != ""}}}}
	if input.Cross != nil {
		c := input.Cross
		for name, data := range map[string]string{"cross-product-token": c.Token, "cross-scim-token": c.SCIMToken} {
			if os.WriteFile(filepath.Join(directory, name), []byte(data), 0600) != nil {
				t.Fatal("cross-product private material failed")
			}
		}
		cb := c.Bindings[0]
		cfg.Apps = append(cfg.Apps, runtimeAppFile{App: cb.App, Kind: cb.Kind, Origin: cb.Origin, Issuer: cb.Issuer, SourceID: cb.SourceID, TokenFile: "cross-product-token", SCIMFile: "cross-scim-token", Directory: &runtimeDirectoryFile{AdministratorRole: "admin", EverydayRole: "user", CreateAccounts: true}})
	}
	file := filepath.Join(directory, "runtime.json")
	writeRuntime(t, file, cfg)
	runtime, err := LoadRuntime(file)
	if err != nil {
		t.Fatal("Immich producer protected runtime refused", err)
	}
	store := openStore(t, state)
	ids := []string{}
	for _, binding := range input.Bindings {
		id, err := store.Register(ctx, runtime.apps[0].target, binding)
		if err != nil {
			store.Close()
			t.Fatal("Immich producer native binding refused", err)
		}
		ids = append(ids, id)
	}
	if input.Cross != nil {
		for _, binding := range input.Cross.Bindings {
			if _, err := store.Register(ctx, runtime.apps[1].target, binding); err != nil {
				store.Close()
				t.Fatal("cross-product native registration refused", err)
			}
		}
	}
	store.Close()
	serviceCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runtime.Serve(serviceCtx) }()
	control := make(chan error, 1)
	go func() {
		var message struct{ Stop bool }
		err := decoder.Decode(&message)
		if err == nil && !message.Stop {
			err = ErrState
		}
		control <- err
	}()
	select {
	case err := <-control:
		stop()
		serveErr := <-done
		if err != nil || serveErr != nil && !errors.Is(serveErr, context.Canceled) {
			t.Fatal("Immich producer shutdown failed")
		}
	case err := <-done:
		stop()
		t.Fatal("Immich producer stopped before parent", err)
	case <-ctx.Done():
		stop()
		<-done
		t.Fatal("Immich producer fixture expired")
	}
	store = openStore(t, state)
	defer store.Close()
	for i, binding := range input.Bindings {
		retained, err := store.Binding(ids[i])
		if err != nil || retained != binding {
			t.Fatal("Immich producer immutable identity lost")
		}
		proof, err := store.directoryJob(ids[i])
		if err != nil || proof.Status != "complete" || proof.Profile.Administrator != (i == 0) || i == 1 && (proof.Attempt < 5 || proof.Profile.Name != "daily" || proof.Profile.Email != "daily@example.test") {
			t.Fatal("Immich producer directory/audit proofs missing", err)
		}
	}
	revoked, err := store.job(ids[1])
	if err != nil || revoked.Status != "complete" || revoked.Attempts != 2 {
		t.Fatal("Immich producer revocation/audit proofs missing", err)
	}
	if input.NewSubject != "" {
		id := bindingID(Binding{App: b.App, Issuer: b.Issuer, Subject: input.NewSubject})
		creation, err := store.creationJob(id)
		if err != nil || creation.Status != "complete" || creation.Scope.Subject != input.NewSubject || creation.Scope.NativeUUID != "" || creation.Scope.NativeID != 0 || creation.Scope.Username != "" || creation.Scope.ClientID != b.ClientID {
			t.Fatal("Immich producer UUID creation/audit proof missing", err)
		}
		retained, err := store.Binding(id)
		if err != nil || retained.Subject != input.NewSubject || !nativeUUID.MatchString(retained.NativeUUID) || runtime.apps[0].target.inspect(ctx, retained) != nil {
			t.Fatal("Immich producer created UUID/native ownership lost", err)
		}
		profile, err := store.directoryJob(id)
		if err != nil || profile.Status != "complete" || profile.Profile != (DirectoryProfile{Name: "unassigned", Email: "unassigned@example.test"}) {
			t.Fatal("Immich producer created ordinary initialization proof missing", err)
		}
		revoked, err := store.job(id)
		if err != nil || revoked.Status != "complete" || revoked.Attempts != 1 {
			t.Fatal("Immich producer created-account revocation proof missing", err)
		}
	}
	if input.Cross != nil {
		for i, binding := range input.Cross.Bindings {
			id := bindingID(binding)
			retained, err := store.Binding(id)
			if err != nil || retained != binding || runtime.apps[1].target.inspect(ctx, retained) != nil {
				t.Fatal("cross-product reopened binding lost", err)
			}
			proof, err := store.directoryJob(id)
			if err != nil || proof.Status != "complete" || proof.Profile.Administrator != (i == 0) || proof.Profile.Name != binding.Username {
				t.Fatal("cross-product directory proof lost", err)
			}
			if _, err := store.job(id); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("primary app loss escaped to independent target", err)
			}
		}
		id := bindingID(Binding{App: "git", Issuer: b.Issuer, Subject: input.NewSubject})
		creation, err := store.creationJob(id)
		if err != nil || creation.Status != "complete" || creation.Scope.NativeID != 0 || creation.Scope.NativeUUID != "" || creation.Scope.Subject != input.NewSubject {
			t.Fatal("cross-product creation proof lost", err)
		}
		retained, err := store.Binding(id)
		if err != nil || retained.NativeID <= 0 || retained.NativeUUID != "" || retained.Username != "unassigned" || runtime.apps[1].target.inspect(ctx, retained) != nil {
			t.Fatal("cross-product created binding lost", err)
		}
		profile, err := store.directoryJob(id)
		if err != nil || profile.Status != "complete" || profile.Profile != (DirectoryProfile{Name: "unassigned", Email: "unassigned@example.test"}) {
			t.Fatal("cross-product creation initialization lost", err)
		}
		revoked, err := store.job(id)
		if err != nil || revoked.Status != "complete" || revoked.Attempts != 1 {
			t.Fatal("cross-product global revocation proof lost", err)
		}
		t.Log("reopened numeric Forgejo and UUID Immich creation/directory/global-revocation proofs and independent active bindings verified")
	}
	t.Log("actual identity worker delivered owned profiles, role projection and permanent revocation through protected resident runtime; reopened proofs verified")
}
