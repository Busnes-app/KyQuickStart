package offboard

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/scim"
)

// TestNativeForgejoReactivation is invoked twice by the parent Forgejo fixture with
// KYQ_TEST_FORGEJO_REACTIVATE=1, against the store its queue phase revoked. Each
// phase sends KyIdentity v0.4.3's wire over the TLS receiver: the mapping GET, then
// the full re-assignment PUT (refused: Forgejo cannot revoke web sessions) or the
// PATCH replace active=false.
func TestNativeForgejoReactivation(t *testing.T) {
	input := os.Getenv("KYQ_TEST_FORGEJO_REACTIVATE_JSON")
	if input == "" {
		t.Skip("run disposable Forgejo qualification with KYQ_TEST_FORGEJO_REACTIVATE=1")
	}
	var fixture struct {
		Container, Image, Token, State, Phase, Name, Email string
		CA                                                 []byte
		Binding                                            Binding
	}
	if len(input) > 32768 || json.Unmarshal([]byte(input), &fixture) != nil || !regexp.MustCompile(`^kyq-forgejo-api-[0-9]+$`).MatchString(fixture.Container) ||
		!regexp.MustCompile(`^\S+@sha256:[0-9a-f]{64}$`).MatchString(fixture.Image) || (fixture.Phase != "reactivate" && fixture.Phase != "revoke") {
		t.Fatal("unowned native fixture input")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	data, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Config}}", fixture.Container).Output()
	var config struct {
		Image  string
		Labels map[string]string
	}
	if err != nil || json.Unmarshal(data, &config) != nil || config.Image != fixture.Image || config.Labels["kyq.fixture"] != "forgejo" || config.Labels["kyq.fixture-owner"] != fixture.Container {
		t.Fatal("native fixture ownership unverified")
	}
	b := fixture.Binding
	if !strings.HasPrefix(b.Origin, "https://127.0.0.1:") || b.Kind != "forgejo" {
		t.Fatal("native fixture endpoint unverified")
	}
	previous := http.DefaultTransport
	transport := previous.(*http.Transport).Clone()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(fixture.CA) {
		t.Fatal("native public trust invalid")
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
	// Directory mode: the PUT's roles map onto native administration.
	target, err := NewTarget(TargetSpec{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, SourceID: b.SourceID,
		AdministratorRole: "admin", EverydayRole: "user"}, fixture.Token)
	if err != nil {
		t.Fatal(err)
	}
	s := openStore(t, fixture.State)
	defer s.Close()
	id, err := s.Register(ctx, target, b)
	if err != nil {
		t.Fatal("retained native binding failed to resume", err)
	}
	before := epochFiles(t, fixture.State)
	token := strings.Repeat("cd", 32)
	receiver, err := NewReceiver(s, target, token)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(receiver)
	defer server.Close()
	client := &scim.Client{BaseURL: server.URL + receiver.base, Token: token, HTTPClient: server.Client()}
	// KyIdentity re-reads a stored mapping before every PUT or PATCH.
	mapped := func(active bool) {
		t.Helper()
		user, err := client.GetUser(ctx, id)
		if err != nil || user.ID != id || user.ExternalID != b.Subject || user.Active != active {
			t.Fatal("mapping read did not report the expected state", err)
		}
	}
	if fixture.Phase == "revoke" {
		mapped(false)
		if _, err := client.PatchUser(ctx, id, scim.PatchOperation{Op: "replace", Path: "active", Value: false}); err != nil {
			t.Fatal("second revocation not completed", err)
		}
		mapped(false)
		j, epoch, active, err := s.latest(id)
		if err != nil || epoch != 3 || j.Status != "complete" || j.Attempts != 1 || active {
			t.Fatal("second revocation epoch not settled", err)
		}
		unchanged(t, fixture.State, before)
		t.Log("second PATCH revoked again in epoch 3; earlier epochs unchanged")
		return
	}
	mapped(false)
	body := kyIdentityPut(b.Subject, b.Username, fixture.Name, fixture.Email, "user")
	for attempt := uint32(1); attempt <= 2; attempt++ { // The repeat is KyIdentity's retry.
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, client.BaseURL+"/Users/"+id, bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/scim+json")
		req.Header.Set("Content-Type", "application/scim+json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal("re-assignment PUT failed")
		}
		response.Body.Close()
		a, err := s.epochAudit(id, 2, attempt, "result")
		if response.StatusCode != http.StatusConflict || err != nil || a.Outcome != "reactivation_unsupported" {
			t.Fatal("re-assignment PUT not refused and audited", response.StatusCode)
		}
	}
	mapped(false)
	if j, epoch, _, err := s.latest(id); err != nil || epoch != 2 || j.Status != "refused" || j.Attempts != 2 {
		t.Fatal("refused reactivation epoch not recorded", err)
	}
	unchanged(t, fixture.State, before)
	t.Log("re-assignment PUT refused as reactivation_unsupported in epoch 2, twice; epoch 1 unchanged")
}
