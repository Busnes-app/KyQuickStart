package offboard

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The parent owns the native deployment and proves key/session/socket denial and
// retained account/photo data afterward. Credentials arrive only on bounded stdin.
func TestNativeImmichQueue(t *testing.T) {
	if os.Getenv("KYQ_TEST_IMMICH_BRIDGE") != "1" {
		t.Skip("run the pinned Immich module fixture with KYQ_TEST_IMMICH_BRIDGE=1")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 16385))
	var input struct {
		Container, Image, Owner, Token string
		CA                             []byte
		Directory                      bool
		Binding                        Binding
	}
	if err != nil || len(raw) > 16384 || !uniqueJSON(raw) || json.Unmarshal(raw, &input) != nil ||
		!regexp.MustCompile(`^kyq-immich-[0-9]+$`).MatchString(input.Owner) || input.Container != input.Owner+"-server" ||
		!regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(input.Image) ||
		!strings.HasPrefix(input.Binding.Origin, "https://127.0.0.1:") || input.Binding.Kind != "immich" || validate(input.Binding) != nil {
		t.Fatal("unowned native Immich bridge input")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	configuration, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Config}}", input.Container).Output()
	var config struct {
		Image  string
		Labels map[string]string
	}
	if err != nil || json.Unmarshal(configuration, &config) != nil || config.Image != input.Image || config.Labels["kyq.fixture"] != "immich" || config.Labels["kyq.fixture-owner"] != input.Owner {
		t.Fatal("native Immich deployment ownership unverified")
	}
	previous := http.DefaultTransport
	transport := previous.(*http.Transport).Clone()
	transport.Proxy = nil
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(input.CA) {
		t.Fatal("native Immich fixture trust invalid")
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
	b := input.Binding
	spec := TargetSpec{SetupID: b.SetupID, App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, ClientID: b.ClientID}
	if input.Directory {
		spec.AdministratorRole, spec.EverydayRole = "admin", "user"
	}
	target, err := NewTarget(spec, input.Token)
	if err != nil {
		t.Fatal("native Immich target rejected")
	}
	path := stateDir(t)
	s := openStore(t, path)
	defer func() { s.Close() }()
	id, err := s.Register(ctx, target, b)
	if err != nil {
		t.Fatal("native UUID ownership verification failed", err)
	}
	s.Close()
	s = openStore(t, path)
	retained, err := s.Binding(id)
	if err != nil || retained != b {
		t.Fatal("native UUID/client receipt lost across restart")
	}
	receiver, err := NewReceiver(s, target, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(receiver)
	defer server.Close()
	endpoint := server.URL + "/scim/" + b.App + "/Users/" + id
	var original DirectoryProfile
	put := func(subject, username string, profile DirectoryProfile, roles []map[string]string, status int) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"schemas": []string{userSchema}, "externalId": subject,
			"userName": username, "displayName": profile.Name, "active": true,
			"emails": []map[string]any{{"value": profile.Email, "type": "work", "primary": true}}, "roles": roles})
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
		req.Header.Set("Content-Type", "application/scim+json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal("native directory SCIM write failed")
		}
		response.Body.Close()
		if response.StatusCode != status {
			t.Fatalf("native directory SCIM status %d, want %d", response.StatusCode, status)
		}
	}
	read := func(profile DirectoryProfile, active bool) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal("native directory SCIM read failed")
		}
		defer response.Body.Close()
		var resource struct {
			ID, ExternalID, UserName, DisplayName string
			Active                                *bool
			Emails                                []directoryValue
			Roles                                 []directoryValue
		}
		role := "user"
		if profile.Administrator {
			role = "admin"
		}
		if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&resource) != nil ||
			resource.ID != id || resource.ExternalID != b.Subject || resource.UserName != b.Subject || resource.DisplayName != profile.Name ||
			resource.Active == nil || *resource.Active != active || len(resource.Emails) != 1 || resource.Emails[0].Value != profile.Email ||
			len(resource.Roles) != 1 || resource.Roles[0].Value != role {
			t.Fatal("native directory SCIM identity/profile/role proof conflicts")
		}
	}
	ordinary := []map[string]string{{"value": "user"}}
	if input.Directory {
		original, err = target.directoryRead(ctx, b)
		if err != nil || original.Administrator {
			t.Fatal("native ordinary directory receipt unavailable")
		}
		read(original, true)
		updated := DirectoryProfile{Name: "SCIM daily", Email: "scim-daily@example.test"}
		put("foreign-subject", "daily", updated, ordinary, http.StatusBadRequest)
		put(b.Subject, "daily", updated, []map[string]string{{"value": "foreign-role"}}, http.StatusBadRequest)
		read(original, true)
		put(b.Subject, "daily", updated, ordinary, http.StatusNoContent)
		// Immich has no native username: changing this metadata must not rebind
		// the immutable subject/UUID or cause a second native profile mutation.
		put(b.Subject, "metadata.changed", updated, ordinary, http.StatusNoContent)
		read(updated, true)
		updated.Administrator = true
		put(b.Subject, "daily", updated, []map[string]string{{"value": "admin"}}, http.StatusNoContent)
		read(updated, true)
		updated.Administrator = false
		put(b.Subject, "daily", updated, []map[string]string{}, http.StatusNoContent)
		read(updated, true)
		put(b.Subject, "daily", original, ordinary, http.StatusNoContent)
		read(original, true)
	}
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.ServeWorker(workerCtx, []*Target{target}) }()
	joined := false
	defer func() {
		if !joined {
			stop()
			<-done
		}
	}()
	// Exercise a job published after the worker's initial empty directory scan.
	time.Sleep(600 * time.Millisecond)
	body := []byte(`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}`)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, server.URL+"/scim/"+b.App+"/Users/"+id, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	req.Header.Set("Content-Type", "application/scim+json")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal("native bridge SCIM delivery failed")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatal("native bridge did not retain the unavailable write for retry")
	}
	for deadline := time.Now().Add(45 * time.Second); ; {
		select {
		case err := <-done:
			joined = true
			t.Fatal("native Immich worker stopped before verification", err)
		default:
		}
		job, _, err := s.Inspect(id)
		if err == nil && job.Status == "complete" && job.Attempts == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native Immich worker did not complete late-arriving revocation")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("native Immich worker did not join", err)
	}
	joined = true
	s.Close()
	s = openStore(t, path)
	job, _, err := s.Inspect(id)
	if err != nil || job.Status != "complete" || job.Attempts != 2 || s.Deliver(ctx, target, id, time.Now()) != nil {
		t.Fatal("native Immich completed intent/audit proof lost across restart")
	}
	if input.Directory {
		// The old receiver owns the closed store. Rebuild transport around the
		// reopened state before testing retained directory/revocation proofs.
		server.Close()
		receiver, err = NewReceiver(s, target, strings.Repeat("a", 64))
		if err != nil {
			t.Fatal(err)
		}
		server = httptest.NewTLSServer(receiver)
		defer server.Close()
		endpoint = server.URL + "/scim/" + b.App + "/Users/" + id
		proof, err := s.directoryJob(id)
		if err != nil || proof.Status != "complete" || proof.Profile != original {
			t.Fatal("native directory completion/audit proof lost across restart")
		}
		put(b.Subject, "daily", original, ordinary, http.StatusConflict)
		read(original, false)
		t.Log("verified native SCIM profile/repeat/role projection, immutable identity, restarted directory proofs and permanent revocation")
	}
	t.Log("verified native UUID ownership, restarted receipt, authenticated SCIM intent, late worker delivery and retained completion/audit proof")
}
