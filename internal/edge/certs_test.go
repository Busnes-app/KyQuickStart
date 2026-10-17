package edge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

const cfToken = "cf_TOKEN_0123456789abcdefghij"

type fakeSecrets map[string]string

func (f fakeSecrets) EdgeSecret(_ context.Context, name, key string) ([]byte, error) {
	v, ok := f[name+"/"+key]
	if !ok {
		return nil, fmt.Errorf("secret kyq-edge/%s is absent", name)
	}
	return []byte(v), nil
}

// fakeCertNPM serves NPM 2.16's users and certificates API. Every request is logged.
type fakeCertNPM struct {
	mu       sync.Mutex
	users    []npmUser
	certs    []certificate
	perms    map[int]map[string]string
	failCert bool
	log      []string // "METHOD path auth body"
}

func (f *fakeCertNPM) serve(t *testing.T) *NPM {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		auth := r.Header.Get("Authorization")
		f.log = append(f.log, r.Method+" "+r.URL.Path+" "+auth+" "+string(body))
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/users":
			json.NewEncoder(w).Encode(f.users)
		case r.Method == "POST" && r.URL.Path == "/api/users":
			var u npmUser
			json.Unmarshal(body, &u)
			if strings.Contains(string(body), `"auth"`) {
				t.Error("certificate user was given a password")
			}
			u.ID = 50 + len(f.users)
			f.users = append(f.users, u)
			json.NewEncoder(w).Encode(u)
		case r.Method == "PUT" && len(parts) == 4 && parts[3] == "permissions":
			id, _ := strconv.Atoi(parts[2])
			p := map[string]string{}
			json.Unmarshal(body, &p)
			f.perms[id] = p
			fmt.Fprint(w, "true")
		case r.Method == "POST" && len(parts) == 4 && parts[3] == "login":
			fmt.Fprintf(w, `{"token":"acme-session-%s"}`, parts[2])
		case r.Method == "GET" && r.URL.Path == certPath:
			json.NewEncoder(w).Encode(f.certs)
		case r.Method == "POST" && r.URL.Path == certPath:
			if f.failCert {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write(body) // a hostile answer echoing the credential
				return
			}
			owner, _ := strconv.Atoi(strings.TrimPrefix(auth, "Bearer acme-session-"))
			var req struct {
				Domains []string `json:"domain_names"`
			}
			json.Unmarshal(body, &req)
			c := certificate{ID: 70 + len(f.certs), Owner: owner, Provider: "letsencrypt", Domains: req.Domains, Expires: "2099-01-01 00:00:00"}
			f.certs = append(f.certs, c)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(c)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	n, err := NewNPM(srv.URL, "admin-session-token", testID, nil)
	if err != nil {
		t.Fatal(err)
	}
	n.http.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	n.Secrets = fakeSecrets{"cloudflare-dns/api_token": cfToken + "\n"}
	t.Cleanup(n.Close)
	return n
}

func (f *fakeCertNPM) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, l := range f.log {
		if !strings.HasPrefix(l, "GET ") {
			out = append(out, l)
		}
	}
	return out
}

func certEdge() (stack.Edge, stack.Certificate) {
	c := stack.Certificate{Name: "k80", Domains: []string{"*.k80.example.com"}, Email: "ops+acme@example.com",
		DNSProvider: "cloudflare", Credentials: stack.SecretRef{Secret: "cloudflare-dns", Key: "api_token"}}
	e := testEdge()
	e.Certificates = []stack.Certificate{c}
	e.Routes[0].CertificateID, e.Routes[0].Certificate, e.Routes[0].Hostname = 0, "k80", "hello.k80.example.com"
	return e, c
}

func TestCertificateRequestReuseAndRouteResolution(t *testing.T) {
	ctx := context.Background()
	f := &fakeCertNPM{perms: map[int]map[string]string{}, users: []npmUser{{ID: 1, Email: "admin@kyquickstart.invalid", Roles: []string{"admin"}}}}
	n := f.serve(t)
	e, c := certEdge()
	s := certStep{n: n, e: e, c: c}
	if err := n.Preflight(ctx, stack.Edge{Target: e.Target, Certificates: e.Certificates}); err != nil {
		t.Fatal(err)
	}
	if _, err := n.certificateID(ctx, e, e.Routes[0]); err == nil || !strings.Contains(err.Error(), "not issued yet") {
		t.Fatalf("route resolved before issuance: %v", err)
	}
	if done, err := s.Inspect(ctx); done || err != nil {
		t.Fatalf("absent certificate inspected as %v %v", done, err)
	}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	u := f.users[1]
	if u.Nickname != "kyquickstart:"+testID+":nas" || u.Email != c.Email || len(u.Roles) != 0 {
		t.Fatalf("certificate user %+v", u)
	}
	if p := f.perms[u.ID]; p["certificates"] != "manage" || p["proxy_hosts"] != "hidden" || p["access_lists"] != "hidden" || p["visibility"] != "user" {
		t.Fatalf("certificate user permissions %v", p)
	}
	var withToken []string
	for _, l := range f.log {
		if strings.Contains(l, cfToken) {
			withToken = append(withToken, l)
		}
	}
	if len(withToken) != 1 || !strings.HasPrefix(withToken[0], "POST "+certPath+" Bearer acme-session-51 {") {
		t.Fatalf("the DNS token must travel only in the certificate request body, as the certificate user: %q", withToken)
	}
	var req struct {
		Provider string         `json:"provider"`
		Domains  []string       `json:"domain_names"`
		Meta     map[string]any `json:"meta"`
	}
	json.Unmarshal([]byte(withToken[0][strings.Index(withToken[0], "{"):]), &req)
	want := map[string]any{"dns_challenge": true, "dns_provider": "cloudflare", "dns_provider_credentials": "dns_cloudflare_api_token = " + cfToken}
	if req.Provider != "letsencrypt" || !sameSet(req.Domains, c.Domains) || fmt.Sprint(req.Meta) != fmt.Sprint(want) {
		t.Fatalf("certificate request %+v", req)
	}
	if n.Redact.Redact(cfToken+" acme-session-51") != "[redacted] [redacted]" {
		t.Fatal("credentials were not registered for redaction")
	}

	// A rerun reuses the owned certificate; routes resolve to it.
	before := len(f.mutations())
	if done, err := s.Inspect(ctx); !done || err != nil {
		t.Fatalf("owned certificate not reused: %v %v", done, err)
	}
	id, err := n.certificateID(ctx, e, e.Routes[0])
	if err != nil || id != f.certs[0].ID {
		t.Fatalf("route resolved to %d %v", id, err)
	}
	if h, err := (hostStep{n: n, e: e, r: e.Routes[0]}).want(ctx); err != nil || h.Certificate != id {
		t.Fatalf("host config certificate %d %v", h.Certificate, err)
	}
	if len(f.mutations()) != before {
		t.Fatal("rerun changed NPM")
	}
}

func TestCertificateOwnershipRefusesForeign(t *testing.T) {
	ctx := context.Background()
	e, c := certEdge()
	foreignCert := certificate{ID: 9, Owner: 1, Provider: "letsencrypt", Domains: c.Domains, Expires: "2099-01-01 00:00:00"}
	// A user marked by another installation, owning a matching certificate.
	other := npmUser{ID: 2, Email: "other@example.com", Nickname: "kyquickstart:" + otherID + ":nas"}
	otherCert := certificate{ID: 10, Owner: 2, Provider: "letsencrypt", Domains: c.Domains, Expires: "2099-01-01 00:00:00"}
	f := &fakeCertNPM{perms: map[int]map[string]string{}, users: []npmUser{{ID: 1, Email: "admin@example.com", Roles: []string{"admin"}}, other}, certs: []certificate{foreignCert, otherCert}}
	n := f.serve(t)
	s := certStep{n: n, e: e, c: c}
	if done, err := s.Inspect(ctx); done || err != nil {
		t.Fatalf("foreign certificate adopted: %v %v", done, err)
	}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if id, err := n.certificateID(ctx, e, e.Routes[0]); err != nil || id != f.certs[2].ID {
		t.Fatalf("route resolved to %d %v, not this installation's certificate", id, err)
	}
	if done, err := s.Inspect(ctx); !done || err != nil {
		t.Fatalf("own certificate not recognised beside foreign ones: %v %v", done, err)
	}
	for _, m := range f.mutations() {
		if strings.Contains(m, certPath+"/") || strings.Contains(m, "/api/users/1/") || strings.Contains(m, "/api/users/2/") {
			t.Fatalf("touched a foreign object: %s", m)
		}
	}

	// Another user already holds the email: NPM signs in by email, so it is refused.
	for _, u := range []npmUser{
		{ID: 3, Email: c.Email, Nickname: "ops"},
		{ID: 3, Email: strings.ToUpper(c.Email), Nickname: "kyquickstart:" + otherID + ":nas"},
		{ID: 3, Email: c.Email, Nickname: "kyquickstart:" + testID + ":nas", Roles: []string{"admin"}},
	} {
		f := &fakeCertNPM{perms: map[int]map[string]string{}, users: []npmUser{u}}
		n := f.serve(t)
		s := certStep{n: n, e: e, c: c}
		if err := s.Apply(ctx); err == nil || !strings.Contains(err.Error(), "not this installation's certificate user") {
			t.Fatalf("user %+v: %v", u, err)
		}
		if err := n.Preflight(ctx, stack.Edge{Target: e.Target, Certificates: e.Certificates}); err == nil {
			t.Fatalf("preflight accepted user %+v", u)
		}
		if m := f.mutations(); len(m) != 0 {
			t.Fatalf("changed NPM: %q", m)
		}
	}
}

func TestCertificateExpiredOrPendingIsAnError(t *testing.T) {
	ctx := context.Background()
	e, c := certEdge()
	for expires, want := range map[string]string{"2001-01-01 00:00:00": "expired", "": "no expiry yet"} {
		user := npmUser{ID: 5, Email: c.Email, Nickname: "kyquickstart:" + testID + ":nas"}
		f := &fakeCertNPM{perms: map[int]map[string]string{}, users: []npmUser{user}, certs: []certificate{{ID: 7, Owner: 5, Provider: "letsencrypt", Domains: c.Domains, Expires: expires}}}
		n := f.serve(t)
		s := certStep{n: n, e: e, c: c}
		if _, err := s.Inspect(ctx); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: inspect %v", expires, err)
		}
		if err := s.Verify(ctx); err == nil {
			t.Fatalf("%q: verified", expires)
		}
		if err := n.Preflight(ctx, stack.Edge{Target: e.Target, Certificates: e.Certificates}); err == nil {
			t.Fatalf("%q: preflight passed", expires)
		}
		if m := f.mutations(); len(m) != 0 {
			t.Fatalf("changed NPM: %q", m)
		}
	}
}

func TestCertificateCredentialNeverInErrors(t *testing.T) {
	ctx := context.Background()
	e, c := certEdge()
	f := &fakeCertNPM{perms: map[int]map[string]string{}, failCert: true}
	n := f.serve(t)
	err := certStep{n: n, e: e, c: c}.Apply(ctx)
	if err == nil || strings.Contains(err.Error(), cfToken) || strings.Contains(err.Error(), "acme-session") || errors.Is(err, engine.ErrTransient) {
		t.Fatalf("failed issuance must be final and carry no credential: %v", err)
	}
	if n.Redact.Redact(cfToken) != "[redacted]" {
		t.Fatal("DNS credential was not registered for redaction")
	}

	// A value that would add lines to certbot's credentials file is refused unsent.
	for _, bad := range []string{cfToken + "\ndns_cloudflare_email = x@example.com", "short", cfToken + " x"} {
		f := &fakeCertNPM{perms: map[int]map[string]string{}}
		n := f.serve(t)
		n.Secrets = fakeSecrets{"cloudflare-dns/api_token": bad}
		err := certStep{n: n, e: e, c: c}.Apply(ctx)
		if err == nil || strings.Contains(err.Error(), cfToken) || strings.Contains(err.Error(), "short") {
			t.Fatalf("%q: %v", bad, err)
		}
		if m := f.mutations(); len(m) != 0 {
			t.Fatalf("%q reached NPM: %q", bad, m)
		}
	}
}
