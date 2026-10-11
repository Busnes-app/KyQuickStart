package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

func testEdge() stack.Edge {
	return stack.Edge{Target: "nas", Provider: "frp", NPMURL: "https://npm.test", ConnectorCIDR: "172.30.0.3/32", ProxyPort: 8443,
		Routes: []stack.Route{{App: "hello", Hostname: "hello.example.com", ForwardHost: "app", ForwardPort: 8080, ForwardScheme: "http", CertificateID: 1}}}
}

func TestReconcileResumeDriftAndForeignOwnership(t *testing.T) {
	ctx := context.Background()
	e := testEdge()
	foreign := host{ID: 19, hostConfig: hostConfig{Domains: []string{"other.example.com"}, Advanced: "# operator config", Enabled: true}}
	hosts := []host{foreign}
	writes := 0
	failUpdate := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Error("missing bearer")
		}
		if strings.HasPrefix(r.URL.Path, "/api/nginx/certificates/") {
			fmt.Fprint(w, `{"domain_names":["*.example.com"],"expires_on":"2099-01-01T00:00:00Z"}`)
			return
		}
		if r.Method == "GET" {
			if r.URL.Path != hostPath {
				fmt.Fprint(w, `[]`)
				return
			}
			json.NewEncoder(w).Encode(hosts)
			return
		}
		writes++
		if r.URL.Path == hostPath && r.Method == "POST" {
			var c hostConfig
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				t.Fatal(err)
			}
			if c.Enabled {
				t.Error("created enabled before verification")
			}
			h := host{ID: 20, hostConfig: c}
			hosts = append(hosts, h)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(h)
			return
		}
		h := &hosts[1]
		switch {
		case r.URL.Path == hostPath+"/20/enable":
			h.Enabled = true
			h.Meta["nginx_online"] = true
		case r.URL.Path == hostPath+"/20/disable":
			h.Enabled = false
		case r.URL.Path == hostPath+"/20" && r.Method == "PUT":
			if h.Enabled {
				t.Error("changed active host before disabling")
			}
			if failUpdate {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, "secret-token")
				return
			}
			if err := json.NewDecoder(r.Body).Decode(&h.hostConfig); err != nil {
				t.Fatal(err)
			}
		default:
			t.Errorf("unexpected mutation %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	n, err := NewNPM(srv.URL, "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	n.http.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	defer n.Close()
	if err := n.Preflight(ctx, e); err != nil {
		t.Fatal(err)
	}
	s := n.Steps(e)[0]
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Inspect(ctx); !ok || err != nil {
		t.Fatalf("second run isn't a no-op: %v %v", ok, err)
	}
	if writes != 2 || !reflect.DeepEqual(hosts[0], foreign) {
		t.Fatalf("writes=%d or foreign host changed", writes)
	}
	hosts[1].Port = 9999
	failUpdate = true
	if err := s.Apply(ctx); err == nil || strings.Contains(err.Error(), "secret-token") || hosts[1].Enabled {
		t.Fatalf("failed update wasn't safely disabled/redacted: %v", err)
	}
	failUpdate = false
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	// Simulate a create that landed before the caller timed out: resume only enables.
	hosts[1].Enabled = false
	before := writes
	if err := s.Apply(ctx); err != nil || writes != before+1 {
		t.Fatalf("resume recreated host: %v", err)
	}
	hosts[1].Meta["ky.target"] = "somebody-else"
	before = writes
	if err := s.Apply(ctx); err == nil || writes != before {
		t.Fatalf("adopted a foreign host: %v", err)
	}
	hosts[1].Domains = []string{"*.example.com"}
	if err := n.Preflight(ctx, e); err == nil {
		t.Fatal("accepted a foreign wildcard collision")
	}
}

func TestRedirectNeverReceivesCredential(t *testing.T) {
	calls := 0
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer other.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	n, err := NewNPM(srv.URL, "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	n.http.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	defer n.Close()
	err = n.Preflight(context.Background(), testEdge())
	if err == nil || calls != 0 || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("redirect/error leaked token: %v, calls=%d", err, calls)
	}
}

func TestHeadersAndDedicatedListener(t *testing.T) {
	e := testEdge()
	c := config(e, e.Routes[0])
	for _, want := range []string{"listen 8443 ssl proxy_protocol;", "real_ip_header proxy_protocol;", "set_real_ip_from 172.30.0.3/32;", "location / {", `proxy_set_header Forwarded "";`, "proxy_set_header X-Forwarded-For $remote_addr;", "proxy_set_header X-Forwarded-Proto $scheme;"} {
		if !strings.Contains(c.Advanced, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(c.Advanced, "proxy_add_x_forwarded_for") {
		t.Fatal("untrusted forwarding header appended")
	}
	e.Provider, e.ProxyPort = "cloudflare", 0
	cf := config(e, e.Routes[0]).Advanced
	if strings.Contains(cf, "proxy_protocol") || !strings.Contains(cf, "real_ip_header CF-Connecting-IP;") {
		t.Fatal("wrong Cloudflare listener/trust")
	}
}
