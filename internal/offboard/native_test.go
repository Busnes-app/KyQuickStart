package offboard

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The Python harness owns every fixture and subsequently proves old web/API and
// fresh OIDC denial, retained identity/roles/content and actual KyIdentity login.
func TestNativeBookStackQueue(t *testing.T) {
	container := os.Getenv("KYQ_TEST_BOOKSTACK_HTTP_CONTAINER")
	if container == "" {
		t.Skip("run disposable BookStack OIDC qualification with KYQ_TEST_BOOKSTACK_QUEUE=1")
	}
	owner := os.Getenv("KYQ_TEST_BOOKSTACK_FIXTURE_OWNER")
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(owner) || container != "kyq-bookstack-"+owner+"-app" {
		t.Fatal("refusing unowned fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	docker := func(args ...string) []byte {
		t.Helper()
		data, err := exec.CommandContext(ctx, "docker", args...).Output()
		if err != nil {
			t.Fatal("owned fixture operation failed")
		}
		return data
	}
	var config struct {
		Image  string
		Labels map[string]string
	}
	image := os.Getenv("KYQ_TEST_BOOKSTACK_IMAGE")
	if json.Unmarshal(docker("inspect", "--format", "{{json .Config}}", container), &config) != nil || config.Labels["kyq.fixture"] != "bookstack" || config.Labels["kyq.fixture-owner"] != owner || !regexp.MustCompile(`^\S+@sha256:[0-9a-f]{64}$`).MatchString(image) || config.Image != image {
		t.Fatal("fixture ownership/image unverified")
	}
	hostPort := strings.TrimSpace(string(docker("port", "kyq-bookstack-"+owner+"-gateway", "8443")))
	if !strings.HasPrefix(hostPort, "127.0.0.1:") || strings.Contains(hostPort, "\n") {
		t.Fatal("gateway is not loopback-only")
	}
	var credential struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(docker("exec", "-u", "abc", container, "cat", "/config/kyquickstart-bridge/token.json"), &credential) != nil {
		t.Fatal("fixture credential unreadable")
	}
	previous := http.DefaultTransport
	transport := previous.(*http.Transport).Clone()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(docker("exec", container, "cat", "/fixture/ca.pem")) {
		t.Fatal("fixture public CA invalid")
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
	b := fixtureBinding()
	b.Origin = "https://" + hostPort
	b.Issuer = "https://kyq-bookstack-" + owner + "-gateway:8443/fixture-idp"
	b.Subject = os.Getenv("KYQ_TEST_BOOKSTACK_DAILY_SUBJECT")
	var err error
	b.NativeID, err = strconv.ParseInt(os.Getenv("KYQ_TEST_BOOKSTACK_USER_ID"), 10, 64)
	if err != nil {
		t.Fatal("invalid native ID")
	}
	target, err := NewTarget(TargetSpec{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer}, credential.Token)
	if err != nil {
		t.Fatal(err)
	}
	dir := stateDir(t)
	s := openStore(t, dir)
	defer func() { s.Close() }()
	id, err := s.Register(ctx, target, b)
	if err != nil {
		t.Fatal("native registration failed", err)
	}
	now := time.Now()
	if _, err := s.Request(id, now); err != nil {
		t.Fatal(err)
	}
	// Simulate an interrupted response/publication after native revocation: intent
	// must remain pending and the next delivery must repair incomplete native state.
	err = s.Run(ctx, id, now, func(ctx context.Context, retained Binding) error {
		if err := target.revoke(ctx, retained); err != nil {
			t.Fatal("native revocation failed", err)
		}
		sum := sha256.Sum256([]byte(b.Issuer + "\x00" + b.Subject))
		docker("exec", "-u", "abc", container, "rm", "/config/kyquickstart-revocations/subject-"+hex.EncodeToString(sum[:])+".json")
		return ErrPending
	})
	if err != ErrPending {
		t.Fatal("interrupted native outcome reported complete", err)
	}
	job, _, err := s.Inspect(id)
	if err != nil || job.Status != "pending" || job.Attempts != 1 {
		t.Fatal("pending intent lost", err)
	}
	s.Close()
	s = openStore(t, dir)
	if _, err := s.Register(ctx, target, b); err != nil {
		t.Fatal("retained disabled registration failed", err)
	}
	if os.Getenv("KYQ_TEST_BOOKSTACK_RUNTIME") == "1" {
		s = resumeThroughRuntime(t, s, id, credential.Token, job.Next)
	} else if os.Getenv("KYQ_TEST_BOOKSTACK_WORKER") == "1" {
		resumeThroughWorker(t, s, target, id, job.Next)
	} else if os.Getenv("KYQ_TEST_BOOKSTACK_SCIM") == "1" {
		resumeThroughSCIM(t, s, target, id, job.Next)
	} else if err := s.Deliver(ctx, target, id, job.Next); err != nil {
		t.Fatal("durable native repair failed", err)
	}
	job, _, err = s.Inspect(id)
	if err != nil || job.Status != "complete" || job.Attempts != 2 {
		t.Fatal("native completion not recorded", err)
	}
	if err := s.Deliver(ctx, target, id, job.Next); err != nil {
		t.Fatal("completed resume failed", err)
	}
}

func TestNativeForgejoQueue(t *testing.T) {
	input := os.Getenv("KYQ_TEST_FORGEJO_QUEUE_JSON")
	if input == "" {
		t.Skip("run disposable Forgejo qualification with KYQ_TEST_FORGEJO_QUEUE=1")
	}
	var fixture struct {
		Container, Image, Token string
		CA                      []byte
		Binding                 Binding
	}
	if len(input) > 32768 || json.Unmarshal([]byte(input), &fixture) != nil || !regexp.MustCompile(`^kyq-forgejo-api-[0-9]+$`).MatchString(fixture.Container) || !regexp.MustCompile(`^\S+@sha256:[0-9a-f]{64}$`).MatchString(fixture.Image) {
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
	target, err := NewTarget(TargetSpec{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, SourceID: b.SourceID}, fixture.Token)
	if err != nil {
		t.Fatal(err)
	}
	dir := stateDir(t)
	s := openStore(t, dir)
	defer func() { s.Close() }()
	id, err := s.Register(ctx, target, b)
	if err != nil {
		t.Fatal("native binding registration failed", err)
	}
	now := time.Now()
	if _, err := s.Request(id, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(ctx, id, now, func(ctx context.Context, retained Binding) error {
		if err := target.revoke(ctx, retained); err != nil {
			t.Fatal("native revocation failed", err)
		}
		return ErrPending // Native completion with interrupted bridge result publication.
	}); err != ErrPending {
		t.Fatal("interrupted outcome reported complete", err)
	}
	job, _, err := s.Inspect(id)
	if err != nil || job.Status != "pending" {
		t.Fatal("durable intent lost", err)
	}
	s.Close()
	s = openStore(t, dir)
	if _, err := s.Register(ctx, target, b); err != nil {
		t.Fatal("disabled native binding failed to resume", err)
	}
	if os.Getenv("KYQ_TEST_FORGEJO_RUNTIME") == "1" {
		s = resumeThroughRuntime(t, s, id, fixture.Token, job.Next)
	} else if os.Getenv("KYQ_TEST_FORGEJO_WORKER") == "1" {
		resumeThroughWorker(t, s, target, id, job.Next)
	} else if os.Getenv("KYQ_TEST_FORGEJO_SCIM") == "1" {
		resumeThroughSCIM(t, s, target, id, job.Next)
	} else if err := s.Deliver(ctx, target, id, job.Next); err != nil {
		t.Fatal("native verification did not resume", err)
	}
	job, _, err = s.Inspect(id)
	if err != nil || job.Status != "complete" || job.Attempts != 2 {
		t.Fatal("native completion not recorded", err)
	}
	if err := s.Deliver(ctx, target, id, job.Next); err != nil {
		t.Fatal("completed retry failed", err)
	}
}
