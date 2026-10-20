package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

func messageRoutes() (stack.Edge, map[string]stack.Route) {
	e := testEdge()
	e.Provider, e.ConnectorCIDR, e.ProxyPort = "lan", "", 0
	e.AllowCIDRs, e.ClientPort = []string{"192.168.1.0/24"}, 443
	r := map[string]stack.Route{
		"server_name": {App: "kymessage", Role: "server_name", Hostname: "example.com", ForwardHost: "192.168.1.91", ForwardPort: 18080, ForwardScheme: "http", CertificateID: 1,
			Paths: []string{"/.well-known/matrix/client", "/.well-known/matrix/server"}},
		"matrix": {App: "kymessage", Role: "matrix", Hostname: "matrix.example.com", ForwardHost: "192.168.1.91", ForwardPort: 18008, ForwardScheme: "http", CertificateID: 1,
			DenyPaths: []string{"^/_synapse/(admin|mas)/"}},
		"rtc": {App: "kymessage", Role: "rtc", Hostname: "rtc.example.com", ForwardHost: "192.168.1.91", ForwardPort: 17880, ForwardScheme: "http", CertificateID: 1,
			DenyPaths: []string{"^/twirp/"}},
	}
	return e, r
}

// AC12: the server-name host proxies its two paths alone; matrix and rtc close their APIs.
func TestPathRoutes(t *testing.T) {
	e, routes := messageRoutes()
	n := &NPM{installation: testID}
	c := n.config(e, routes["server_name"], 1)
	adv := c.Advanced
	if !strings.HasPrefix(adv, "# kyquickstart:"+testID+":nas:kymessage:server_name:example.com\n") {
		t.Fatalf("marker: %s", adv)
	}
	client := strings.Index(adv, "location = /.well-known/matrix/client {\n  proxy_set_header Host $host;")
	server := strings.Index(adv, "location = /.well-known/matrix/server {\n  proxy_set_header Host $host;")
	closed := strings.Index(adv, "location / {\n  return 404;\n}\n")
	if client < 0 || server < client || closed < server || strings.Count(adv, "location ") != 3 || strings.Count(adv, "proxy_pass") != 2 || c.Port != 18080 {
		t.Fatalf("server-name host:\n%s", adv)
	}
	for role, re := range map[string]string{"matrix": "^/_synapse/(admin|mas)/", "rtc": "^/twirp/"} {
		c := n.config(e, routes[role], 1)
		deny := strings.Index(c.Advanced, "location ~ "+re+" {\n  return 404;\n}\n")
		root := strings.Index(c.Advanced, "location / {\n  proxy_set_header Host $host;")
		if deny < 0 || root < deny || !c.Websocket || !strings.Contains(c.Advanced, "proxy_set_header Upgrade $http_upgrade;") || strings.Count(c.Advanced, "location ") != 2 {
			t.Errorf("%s host:\n%s", role, c.Advanced)
		}
	}
}

// AC12: a foreign host holding the server-name hostname is refused, never adopted; this
// installation's own host is recognised by its role marker.
func TestServerNameForeignHost(t *testing.T) {
	e, routes := messageRoutes()
	r := routes["server_name"]
	var hosts []host
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != hostPath {
			fmt.Fprint(w, `[]`)
			return
		}
		json.NewEncoder(w).Encode(hosts)
	}))
	defer srv.Close()
	n, err := NewNPM(srv.URL, "secret-token", testID, nil)
	if err != nil {
		t.Fatal(err)
	}
	n.http.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	ctx := context.Background()
	hosts = []host{{ID: 7, hostConfig: hostConfig{Domains: []string{"example.com"}, Advanced: "# the operator's apex", Enabled: true}}}
	if _, err := n.find(ctx, e, r); err == nil || !strings.Contains(err.Error(), "refusing adoption") {
		t.Fatalf("foreign apex: %v", err)
	}
	own := n.config(e, r, 1)
	hosts = []host{{ID: 8, hostConfig: own}}
	if h, err := n.find(ctx, e, r); err != nil || h == nil || h.ID != 8 {
		t.Fatalf("own host: %v %v", h, err)
	}
	// The same hostname marked for another role is not this route's.
	other := r
	other.Role = "console"
	if _, err := n.find(ctx, e, other); err == nil {
		t.Fatal("adopted a host marked for another role")
	}
}
