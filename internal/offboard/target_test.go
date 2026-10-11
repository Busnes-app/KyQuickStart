package offboard

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Test only: trust this HTTPS fixture's public certificate. Native clients still
// verify TLS and hostnames and retain their normal redirect/proxy restrictions.
func trustFixture(t *testing.T, server *httptest.Server) {
	t.Helper()
	previous := http.DefaultTransport
	transport := previous.(*http.Transport).Clone()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous; transport.CloseIdleConnections() })
}

func TestNativeTargetsRegisterAndResumeDurableRevocation(t *testing.T) {
	for _, kind := range []string{"bookstack", "forgejo"} {
		t.Run(kind, func(t *testing.T) {
			b := fixtureBinding()
			b.Kind = kind
			if kind == "forgejo" {
				b.SourceID, b.Username = 7, "day"
			}
			var calls, mutations int
			wrongIdentity, disabled, complete := true, false, false
			tokenPresent := true
			credential := strings.Repeat("ab", 32)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				prefix := "Bearer "
				if kind == "forgejo" {
					prefix = "token "
				}
				if r.Header.Get("Authorization") != prefix+credential {
					t.Error("native credential missing")
					w.WriteHeader(401)
					return
				}
				id := b.NativeID
				if wrongIdentity {
					id++
				}
				w.Header().Set("Content-Type", "application/json")
				if kind == "bookstack" {
					var input struct {
						Contract, Action, Issuer, Subject string
						ID                                int64 `json:"user_id"`
					}
					if r.Method != "POST" || r.URL.Path != "/api/kyquickstart/offboard" || json.NewDecoder(r.Body).Decode(&input) != nil || input.ID != b.NativeID || input.Issuer != b.Issuer || input.Subject != b.Subject {
						t.Error("wrong native request")
						w.WriteHeader(400)
						return
					}
					if input.Action == "deprovision" {
						mutations++
						disabled, tokenPresent = true, false
						if mutations == 1 {
							w.WriteHeader(503)
							w.Write([]byte("PRIVATE-NATIVE-ERROR"))
							return
						}
						complete = true
					}
					sum := sha256.Sum256([]byte(b.Issuer + "\x00" + b.Subject))
					tokens := 0
					if tokenPresent {
						tokens = 1
					}
					json.NewEncoder(w).Encode(map[string]any{"contract": "ky.bookstack-offboard/1", "module_version": "0.15.0", "user_id": id, "binding_digest": hex.EncodeToString(sum[:]), "disabled": disabled, "revocation_complete": complete, "tokens_remaining": tokens})
					return
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/users/day":
					json.NewEncoder(w).Encode(map[string]any{"id": id, "login": b.Username, "source_id": b.SourceID, "login_name": b.Subject, "is_admin": false, "active": !disabled, "prohibit_login": disabled})
				case "PATCH /api/v1/admin/users/day":
					mutations++
					disabled = true
					w.WriteHeader(503)
					w.Write([]byte("PRIVATE-NATIVE-ERROR"))
				case "GET /api/v1/admin/users/day/tokens":
					if tokenPresent {
						w.Write([]byte(`[{"id":4}]`))
					} else {
						w.Write([]byte(`[]`))
					}
				case "DELETE /api/v1/admin/users/day/tokens/4":
					tokenPresent = false
					w.WriteHeader(204)
				default:
					t.Error("unexpected native request", r.Method, r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			trustFixture(t, server)
			b.Origin = server.URL
			spec := TargetSpec{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, SourceID: b.SourceID}
			target, err := NewTarget(spec, credential)
			if err != nil {
				t.Fatal(err)
			}
			dir := stateDir(t)
			s := openStore(t, dir)
			defer func() { s.Close() }()
			ctx := context.Background()
			if _, err := s.Register(ctx, target, b); !errors.Is(err, ErrState) {
				t.Fatal("unverified native identity registered", err)
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 0 {
				t.Fatal("failed native proof published state")
			}
			wrongIdentity = false
			id, err := s.Register(ctx, target, b)
			if err != nil {
				t.Fatal(err)
			}
			before := calls
			for _, change := range []func(*Binding){func(b *Binding) { b.App = "foreign" }, func(b *Binding) { b.Issuer = "https://foreign.test" }, func(b *Binding) { b.Origin = "https://foreign.test" }, func(b *Binding) { b.SourceID++ }} {
				changed := b
				change(&changed)
				if _, err := s.Register(ctx, target, changed); !errors.Is(err, ErrState) {
					t.Fatal("foreign scope accepted")
				}
			}
			if calls != before {
				t.Fatal("foreign scope contacted native service")
			}
			now := time.Now()
			if _, err := s.Request(id, now); err != nil {
				t.Fatal(err)
			}
			if err := s.Deliver(ctx, target, id, now); !errors.Is(err, ErrPending) {
				t.Fatal("unknown mutation reported complete", err)
			}
			job, _, err := s.Inspect(id)
			if err != nil || job.Status != "pending" || !disabled {
				t.Fatal("lost response did not retain pending intent", job, err)
			}
			s.Close()
			s = openStore(t, dir)
			if err := s.Deliver(ctx, target, id, job.Next); err != nil {
				t.Fatal(err)
			}
			job, present, err := s.Inspect(id)
			if err != nil || !present || job.Status != "complete" || job.Attempts != 2 || tokenPresent {
				t.Fatal("native revocation not verified", job, err)
			}
			if kind == "forgejo" && mutations != 1 {
				t.Fatal("retry repeated disable instead of inspecting")
			}
			if kind == "bookstack" && mutations != 2 {
				t.Fatal("partial native fence not repaired")
			}
			before = calls
			foreignSpec := spec
			foreignSpec.Issuer = "https://foreign.test"
			foreign, err := NewTarget(foreignSpec, credential)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Deliver(ctx, foreign, id, job.Next); !errors.Is(err, ErrState) || calls != before {
				t.Fatal("completed marker accepted a foreign target", err)
			}
			if err := s.Deliver(ctx, target, id, job.Next); err != nil || calls != before {
				t.Fatal("completed delivery replayed", err)
			}
			if _, err := s.Register(ctx, target, b); err != nil {
				t.Fatal("revoked registration failed to resume", err)
			}
			files, _ = os.ReadDir(dir)
			for _, file := range files {
				data, _ := os.ReadFile(dir + "/" + file.Name())
				if strings.Contains(string(data), credential) || strings.Contains(string(data), "PRIVATE-NATIVE-ERROR") {
					t.Fatal("native secret/error persisted")
				}
			}
		})
	}
}

func TestTargetConfigurationBoundary(t *testing.T) {
	valid := TargetSpec{App: "wiki", Kind: "bookstack", Origin: "https://wiki.example.test", Issuer: "https://identity.example.test"}
	for _, change := range []func(*TargetSpec){
		func(s *TargetSpec) { s.App = "../../foreign" },
		func(s *TargetSpec) { s.Kind = "unsupported" },
		func(s *TargetSpec) { s.Origin = "http://wiki.example.test" },
		func(s *TargetSpec) { s.Origin += "?" },
		func(s *TargetSpec) { s.Issuer += "#" },
		func(s *TargetSpec) { s.SourceID = 1 },
		func(s *TargetSpec) { s.Kind = "forgejo" },
	} {
		changed := valid
		change(&changed)
		if _, err := NewTarget(changed, strings.Repeat("ab", 32)); !errors.Is(err, ErrState) {
			t.Fatal("invalid target accepted", changed)
		}
	}
	if _, err := NewTarget(valid, "private-invalid-credential"); !errors.Is(err, ErrState) {
		t.Fatal("invalid scoped credential accepted")
	}
}
