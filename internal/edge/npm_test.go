package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

const testID = "9f1c2b6e-3d4a-4f5b-8c7d-0e1f2a3b4c5d"

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
	n, err := NewNPM(srv.URL, "secret-token", testID, nil)
	if err != nil {
		t.Fatal(err)
	}
	n.http.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	defer n.Close()
	if err := n.Preflight(ctx, e); err != nil {
		t.Fatal(err)
	}
	s := hostStep{n: n, e: e, r: e.Routes[0]}
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

func TestRemovedRouteIsDisabledOnlyWhenOwned(t *testing.T) {
	ctx := context.Background()
	e := testEdge()
	owned := func(id int, name, target string) host {
		r := e.Routes[0]
		r.Hostname = name
		c := (&NPM{installation: testID}).config(stack.Edge{Target: target, Provider: e.Provider, ConnectorCIDR: e.ConnectorCIDR, ProxyPort: e.ProxyPort}, r, r.CertificateID)
		c.Enabled = true
		return host{ID: id, hostConfig: c}
	}
	hosts := []host{
		owned(1, "hello.example.com", "nas"),       // configured route
		owned(2, "removed.example.com", "nas"),     // removed from stack.yaml
		owned(3, "other.example.com", "elsewhere"), // another target's host
		{ID: 4, hostConfig: hostConfig{Domains: []string{"foreign.example.com"}, Enabled: true}},
		owned(5, "unmarked.example.com", "nas"),
		owned(6, "retargeted.example.com", "nas"),
		owned(7, "unlabelled.example.com", "nas"),
	}
	// Each lacks one ownership mark.
	hosts[4].Advanced = "# operator config"
	hosts[5].Meta["ky.target"] = "elsewhere"
	delete(hosts[6].Meta, "ky.managed-by")
	var disabled []string
	fail := true
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == hostPath {
			json.NewEncoder(w).Encode(hosts)
			return
		}
		disabled = append(disabled, r.Method+" "+r.URL.Path)
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		hosts[1].Enabled = false
		fmt.Fprint(w, `true`)
	}))
	defer srv.Close()
	n, err := NewNPM(srv.URL, "secret-token", testID, nil)
	if err != nil {
		t.Fatal(err)
	}
	n.http.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	defer n.Close()
	s := n.Steps(e)[0]
	if err := s.Verify(ctx); err == nil || !strings.Contains(err.Error(), "removed.example.com") {
		t.Fatalf("verify accepted an enabled removed host: %v", err)
	}
	// A failed disable leaves the step incomplete; the rerun finishes it.
	if err := s.Apply(ctx); err == nil {
		t.Fatal("failed disable reported success")
	}
	fail = false
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Inspect(ctx); !ok || err != nil {
		t.Fatalf("rerun isn't a no-op: %v %v", ok, err)
	}
	want := []string{"POST " + hostPath + "/2/disable", "POST " + hostPath + "/2/disable"}
	if !reflect.DeepEqual(disabled, want) {
		t.Fatalf("touched a configured or unowned host: %v", disabled)
	}
	for i, h := range hosts {
		if h.Enabled == (i == 1) {
			t.Fatalf("host %d enabled=%v", h.ID, h.Enabled)
		}
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
	n, err := NewNPM(srv.URL, "secret-token", testID, nil)
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
	n := &NPM{installation: testID}
	c := n.config(e, e.Routes[0], 1)
	for _, want := range []string{"listen 8443 ssl proxy_protocol;", "real_ip_header proxy_protocol;", "set_real_ip_from 172.30.0.3/32;", "location / {", `proxy_set_header Forwarded "";`, "proxy_set_header X-Forwarded-For $remote_addr;", "proxy_set_header X-Forwarded-Proto $scheme;"} {
		if !strings.Contains(c.Advanced, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(c.Advanced, "proxy_add_x_forwarded_for") {
		t.Fatal("untrusted forwarding header appended")
	}
	e.Provider, e.ProxyPort = "cloudflare", 0
	cf := n.config(e, e.Routes[0], 1).Advanced
	if strings.Contains(cf, "proxy_protocol") || !strings.Contains(cf, "real_ip_header CF-Connecting-IP;") {
		t.Fatal("wrong Cloudflare listener/trust")
	}
}

// A lan host trusts no forwarding header, serves only allow_cidrs and overwrites XFF.
func TestLANHostConfig(t *testing.T) {
	e := testEdge()
	e.Provider, e.ConnectorCIDR, e.ProxyPort = "lan", "", 0
	e.AllowCIDRs, e.ClientPort = []string{"192.168.1.0/24", "fd00:1::/48"}, 30543
	want := "# kyquickstart:" + testID + ":nas:hello:hello.example.com\n" + `set_real_ip_from unix:;
if ($ssl_server_name != hello.example.com) { return 421; }
allow 192.168.1.0/24;
allow fd00:1::/48;
deny all;
location / {
  proxy_set_header Host hello.example.com:30543;
  proxy_set_header X-Forwarded-Host hello.example.com:30543;
  proxy_set_header X-Forwarded-For $remote_addr;
  proxy_set_header X-Real-IP $remote_addr;
  proxy_set_header X-Forwarded-Proto $scheme;
  proxy_set_header X-Forwarded-Scheme $scheme;
  proxy_set_header X-Forwarded-Port 30543;
  proxy_set_header Forwarded "";
  proxy_set_header CF-Connecting-IP "";
  proxy_set_header True-Client-IP "";
  proxy_set_header Upgrade $http_upgrade;
  proxy_set_header Connection $http_connection;
  proxy_http_version 1.1;
  proxy_pass $forward_scheme://$server:$port$request_uri;
}
`
	if got := (&NPM{installation: testID}).config(e, e.Routes[0], 1).Advanced; got != want {
		t.Fatalf("lan advanced_config:\n%s", got)
	}
}

// On a tunnel edge an internal route is configured as on a lan edge: allow_cidrs only, no
// connector trust and no PROXY-protocol listener; its external neighbour keeps the tunnel's.
func TestInternalRouteOnTunnelEdge(t *testing.T) {
	e := testEdge()
	e.AllowCIDRs, e.ClientPort = []string{"192.168.1.0/24"}, 443
	e.Routes = append(e.Routes, stack.Route{App: "kypulse", Hostname: "pulse.lan.example.com", ForwardHost: "app", ForwardPort: 8080, ForwardScheme: "http", CertificateID: 1, Exposure: "internal"})
	n := &NPM{installation: testID}
	internal, external := n.config(e, e.Routes[1], 1).Advanced, n.config(e, e.Routes[0], 1).Advanced
	if !strings.Contains(internal, "allow 192.168.1.0/24;\ndeny all;") || strings.Contains(internal, "proxy_protocol") || strings.Contains(internal, "172.30.0.3") {
		t.Fatalf("internal route:\n%s", internal)
	}
	if !strings.Contains(external, "listen 8443 ssl proxy_protocol;") || strings.Contains(external, "allow 192.168.1.0/24") {
		t.Fatalf("external route:\n%s", external)
	}
}

// A kube:// NPM is reached only through the supplied tunnel, as plain HTTP inside it.
func TestKubeTunnel(t *testing.T) {
	var hostHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostHeader = r.Host
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Error("missing bearer")
		}
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()
	dials := 0
	tunnel := func(ctx context.Context, network, address string) (net.Conn, error) {
		dials++
		if address != "app.kyq-npm:81" {
			t.Errorf("tunnel asked for %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}
	origin := "kube://kyq-npm/app:81"
	if _, err := NewNPM(origin, "secret-token", testID, nil); err == nil {
		t.Fatal("kube:// without a tunnel accepted")
	}
	if _, err := NewNPM("https://npm.test", "secret-token", testID, tunnel); err == nil {
		t.Fatal("https:// with a tunnel accepted")
	}
	if _, err := NewNPM(origin, "secret-token", "", tunnel); err == nil {
		t.Fatal("no installation id accepted")
	}
	n, err := NewNPM(origin, "secret-token", testID, tunnel)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	var hosts []host
	if err := n.request(context.Background(), http.MethodGet, hostPath, nil, &hosts); err != nil {
		t.Fatal(err)
	}
	if dials != 1 || hostHeader != "app.kyq-npm:81" {
		t.Fatalf("dials=%d host=%q", dials, hostHeader)
	}
	// https:// keeps the private-address rule.
	public, _ := NewNPM("https://192.0.2.1", "secret-token", testID, nil)
	if err := public.request(context.Background(), http.MethodGet, hostPath, nil, &hosts); err == nil {
		t.Fatal("public NPM address dialled")
	}
}
