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

// A separate test executable avoids the adapter/bridge import cycle. The parent
// drives only actual identity mutations; stdin is a bounded shutdown command.
func TestNativeIdentityProducerBridge(t *testing.T) {
	input := os.Getenv("KYQ_TEST_FORGEJO_PRODUCER_JSON")
	bookstackMode := os.Getenv("KYQ_TEST_BOOKSTACK_PRODUCER") == "1"
	var controlDecoder *json.Decoder
	if bookstackMode {
		controlDecoder = json.NewDecoder(io.LimitReader(os.Stdin, 65536))
		var raw json.RawMessage
		if controlDecoder.Decode(&raw) != nil || !uniqueJSON(raw) {
			t.Fatal("invalid producer input")
		}
		input = string(raw)
	}
	if input == "" {
		t.Skip("run pinned Forgejo fixture with KYQ_TEST_FORGEJO_PRODUCER=1")
	}
	var fixture struct {
		Container, Image, Token, Listen string
		CA, Certificate, Key            []byte
		SCIMToken                       string `json:"scim_token"`
		NewSubject                      string `json:"new_subject"`
		GroupID                         string `json:"group_id"`
		OrganizationID                  int64  `json:"organization_id"`
		Bindings                        []Binding
		BookStack                       *BookStackDirectoryConfig
		DirectoryToken                  string
		CreationToken                   string
	}
	if len(input) > 65536 || json.Unmarshal([]byte(input), &fixture) != nil || len(fixture.Bindings) != 2 || !regexp.MustCompile(`^\S+@sha256:[0-9a-f]{64}$`).MatchString(fixture.Image) {
		t.Fatal("unowned native producer fixture")
	}
	host, _, err := net.SplitHostPort(fixture.Listen)
	ip := net.ParseIP(host)
	if err != nil || ip.To4() == nil || !ip.IsLoopback() {
		t.Fatal("producer listener is not loopback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Config}}", fixture.Container).Output()
	var config struct {
		Image  string
		Labels map[string]string
	}
	kind, fixtureOwner := "forgejo", fixture.Container
	if bookstackMode {
		kind = "bookstack"
		if !regexp.MustCompile(`^kyq-bookstack-[a-f0-9]{12}-app$`).MatchString(fixture.Container) {
			t.Fatal("unowned BookStack producer")
		}
		fixtureOwner = strings.TrimSuffix(strings.TrimPrefix(fixture.Container, "kyq-bookstack-"), "-app")
	} else if !regexp.MustCompile(`^kyq-forgejo-api-[0-9]+$`).MatchString(fixture.Container) {
		t.Fatal("unowned Forgejo producer")
	}
	if err != nil || json.Unmarshal(raw, &config) != nil || config.Image != fixture.Image || config.Labels["kyq.fixture"] != kind || config.Labels["kyq.fixture-owner"] != fixtureOwner {
		t.Fatal("producer native container ownership unverified")
	}
	b := fixture.Bindings[0]
	for _, binding := range fixture.Bindings {
		if validate(binding) != nil || binding.Kind != kind || !strings.HasPrefix(binding.Origin, "https://127.0.0.1:") || binding.App != b.App || binding.Origin != b.Origin || binding.SetupID != b.SetupID || binding.SourceID != b.SourceID || binding.Issuer != b.Issuer {
			t.Fatal("producer native account scope conflicts")
		}
	}
	if fixture.Bindings[0].Subject == fixture.Bindings[1].Subject || fixture.Bindings[0].NativeID == fixture.Bindings[1].NativeID {
		t.Fatal("producer subjects are not distinct")
	}
	previous := http.DefaultTransport
	transport := previous.(*http.Transport).Clone()
	transport.Proxy = nil
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(fixture.CA) {
		t.Fatal("producer native trust invalid")
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
	directory, state := stateDir(t), stateDir(t)
	for _, path := range []string{directory, state} {
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string][]byte{"server.pem": fixture.Certificate, "server-key.pem": fixture.Key, "product-token": []byte(fixture.Token), "scim-token": []byte(fixture.SCIMToken)} {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := runtimeFile{Schema: "ky.offboarding-runtime/1", SetupID: b.SetupID, Listen: fixture.Listen, StateDir: state, CertFile: "server.pem", KeyFile: "server-key.pem", Apps: []runtimeAppFile{{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, SourceID: b.SourceID, TokenFile: "product-token", SCIMFile: "scim-token", Directory: &runtimeDirectoryFile{AdministratorRole: "admin", EverydayRole: "editor", CreateAccounts: fixture.NewSubject != ""}}}}
	if bookstackMode {
		if fixture.GroupID != "" || !validBookStackDirectory(fixture.BookStack) {
			t.Fatal("unsupported producer policy")
		}
		if os.WriteFile(filepath.Join(directory, "directory-token"), []byte(fixture.DirectoryToken), 0600) != nil {
			t.Fatal("producer directory token failed")
		}
		cfg.Apps[0].Directory.BookStack = fixture.BookStack
		cfg.Apps[0].Directory.TokenFile = "directory-token"
		if fixture.NewSubject != "" {
			if !validSubject(fixture.NewSubject) || fixture.NewSubject == b.Subject || fixture.NewSubject == fixture.Bindings[1].Subject || os.WriteFile(filepath.Join(directory, "creation-token"), []byte(fixture.CreationToken), 0600) != nil {
				t.Fatal("producer creation authority invalid")
			}
			cfg.Apps[0].Directory.CreationTokenFile = "creation-token"
		}
	}
	if fixture.GroupID != "" {
		cfg.Apps[0].Groups = &GroupConfig{OrganizationID: fixture.OrganizationID, Organization: "fixture-org", Permission: "read", Permissions: map[string]string{"repo.code": "read"}}
	}
	file := filepath.Join(directory, "runtime.json")
	writeRuntime(t, file, cfg)
	loaded, err := LoadRuntime(file)
	if err != nil {
		t.Fatal("producer runtime rejected", err)
	}
	store := openStore(t, state)
	ids := []string{}
	for _, binding := range fixture.Bindings {
		id, err := store.Register(ctx, loaded.apps[0].target, binding)
		if err != nil {
			t.Fatal("producer native binding failed", err)
		}
		ids = append(ids, id)
	}
	store.Close()
	serviceCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- loaded.Serve(serviceCtx) }()
	control := make(chan error, 1)
	go func() {
		var message struct {
			Stop bool `json:"stop"`
		}
		decoder := controlDecoder
		if decoder == nil {
			decoder = json.NewDecoder(io.LimitReader(os.Stdin, 1024))
		}
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
		if err != nil || (serveErr != nil && !errors.Is(serveErr, context.Canceled)) {
			t.Fatal("producer bridge shutdown failed")
		}
	case err := <-done:
		stop()
		t.Fatal("producer bridge stopped before parent", err)
	case <-ctx.Done():
		stop()
		<-done
		t.Fatal("producer fixture expired")
	}
	store = openStore(t, state)
	defer store.Close()
	owner, err := store.directoryJob(ids[0])
	if err != nil || owner.Status != "complete" || owner.Attempt < 2 || owner.Profile.Administrator != bookstackMode {
		t.Fatal("producer administrator removal lacks durable directory proofs", err)
	}
	for i, binding := range fixture.Bindings {
		retained, err := store.Binding(ids[i])
		if err != nil || retained != binding {
			t.Fatal("producer native binding lost")
		}
	}
	if bookstackMode {
		for _, id := range ids {
			proof, err := store.directoryJob(id)
			if err != nil || proof.Status != "complete" {
				t.Fatal("producer directory proof lost")
			}
			for attempt := uint32(1); attempt <= proof.Attempt; attempt++ {
				for phase, outcome := range map[string]string{"started": "started", "result": "success"} {
					var audit directoryAudit
					if store.read(directoryAuditName(id, attempt, phase), &audit) != nil || audit.Schema != "ky.directory-audit/1" || audit.Binding != id || audit.Attempt != attempt || audit.Outcome != outcome {
						t.Fatal("producer directory audit lost")
					}
				}
			}
		}
	}
	daily, err := store.job(ids[1])
	if err != nil || daily.Status != "complete" {
		t.Fatal("producer revocation lacks durable completion proofs", err)
	}
	if bookstackMode {
		globallyRevoked, err := store.job(ids[0])
		if err != nil || globallyRevoked.Status != "complete" || globallyRevoked.Attempts < 1 {
			t.Fatal("global owner revocation proof missing", err)
		}
		for _, id := range ids {
			job, err := store.job(id)
			if err != nil {
				t.Fatal(err)
			}
			for attempt := uint32(1); attempt <= job.Attempts; attempt++ {
				for phase, outcome := range map[string]string{"started": "started", "result": "success"} {
					var audit Audit
					if store.read(auditName(id, attempt, phase), &audit) != nil || audit.Schema != "ky.offboarding-audit/1" || audit.Binding != id || audit.Attempt != attempt || audit.Outcome != outcome {
						t.Fatal("revocation audit proof lost")
					}
				}
			}
		}
	}
	if fixture.NewSubject != "" {
		id := bindingID(Binding{App: b.App, Issuer: b.Issuer, Subject: fixture.NewSubject})
		creation, err := store.creationJob(id)
		if err != nil || creation.Status != "complete" || creation.Scope.Subject != fixture.NewSubject || (bookstackMode && creation.Scope.Username != "" || !bookstackMode && creation.Scope.Username != "new-daily") {
			t.Fatal("native producer creation lacks owned proofs", err)
		}
		revoked, err := store.job(id)
		if err != nil || revoked.Status != "complete" {
			t.Fatal("newly provisioned account did not complete revocation", err)
		}
		if bookstackMode {
			// Reconstruct clients from protected files after shutdown, with birth disabled.
			// Existing created accounts must still resolve without a mutable runtime map.
			cfg.Apps[0].Directory.CreateAccounts = false
			writeRuntime(t, file, cfg)
			reloaded, reloadErr := LoadRuntime(file)
			if reloadErr != nil || reloaded.apps[0].target.create != nil || reloaded.apps[0].target.absent != nil {
				t.Fatal("reloaded creation opt-out failed", reloadErr)
			}
			retained, err := store.Binding(id)
			if err != nil || retained.Kind != "bookstack" || retained.NativeID < 1 || retained.NativeUUID != "" || retained.Username != "" || reloaded.apps[0].target.inspect(ctx, retained) != nil {
				t.Fatal("created native scope did not survive reopened lookup", err)
			}
			if _, mapped := reloaded.apps[0].target.spec.BookStackDirectory.Accounts[fixture.NewSubject]; mapped {
				t.Fatal("birth mutated protected account map")
			}
			directory, err := store.directoryJob(id)
			if err != nil || directory.Status != "complete" || directory.Attempt < 2 {
				t.Fatal("born account lacks projected directory proofs", err)
			}
			for attempt := uint32(1); attempt <= directory.Attempt; attempt++ {
				for phase, outcome := range map[string]string{"started": "started", "result": "success"} {
					var audit directoryAudit
					if store.read(directoryAuditName(id, attempt, phase), &audit) != nil || audit.Binding != id || audit.Attempt != attempt || audit.Outcome != outcome {
						t.Fatal("born directory audit lost")
					}
				}
			}
		}
	}
	if fixture.GroupID != "" {
		g, err := store.GroupBinding(groupID(GroupBinding{App: b.App, Issuer: b.Issuer, ExternalID: fixture.GroupID}))
		if err != nil {
			t.Fatal("producer group ownership proof missing", err)
		}
		job, err := store.membershipJob(groupID(g))
		if err != nil || job.Status != "complete" || job.Attempt < 5 || !job.Scope.Deleted || job.Scope.Label != "Renamed developers" || len(job.Scope.Members) != 0 {
			t.Fatal("producer group retirement proof missing", err)
		}
	}
	t.Log("actual identity producer delivered initial profiles, administrator removal and native revocation with durable completion proofs")
}
