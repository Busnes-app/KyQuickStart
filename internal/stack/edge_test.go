package stack

import (
	"fmt"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const edgeStack = `version: 1
targets:
  - name: nas
    host: 192.0.2.1
    user: kyq
apps:
  - name: hello
    target: nas
edges:
  - target: nas
    provider: frp
    npm_url: https://npm.example.com:8444
    connector_cidr: 172.30.0.3/32
    routes:
      - app: hello
        hostname: hello.example.com
        forward_host: app
        forward_port: 8080
        certificate_id: 1
`

func TestEdgeBoundary(t *testing.T) {
	s, err := Parse([]byte(edgeStack))
	if err != nil {
		t.Fatal(err)
	}
	if s.Edges[0].ProxyPort != 8443 || s.Edges[0].Routes[0].ForwardScheme != "http" {
		t.Fatal("defaults missing")
	}
	for name, pair := range map[string][2]string{
		"plaintext API":       {"https://npm", "http://npm"},
		"URL secret":          {"https://npm", "https://secret@npm"},
		"broad trust":         {"172.30.0.3/32", "172.30.0.0/24"},
		"admin port":          {"forward_port: 8080", "forward_port: 81"},
		"wildcard":            {"hello.example.com", "'*.example.com'"},
		"unknown app":         {"app: hello", "app: absent"},
		"unknown provider":    {"provider: frp", "provider: nps"},
		"directive injection": {"forward_host: app", "forward_host: 'app; return 200;'"},
		"no cert":             {"certificate_id: 1", "certificate_id: 0"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(strings.Replace(edgeStack, pair[0], pair[1], 1))); err == nil {
				t.Fatal("accepted unsafe edge")
			}
		})
	}
}

const kubeEdgeStack = `version: 1
targets:
  - name: k8s
    kubeconfig: /kc
  - name: nas
    host: 192.0.2.1
    user: kyq
apps:
  - name: npm
    target: k8s
  - name: hello
    target: k8s
edges:
  - target: k8s
    provider: cloudflare
    npm_url: kube://kyq-npm/app:81
    connector_cidr: 172.18.0.2/32
    routes:
      - app: hello
        hostname: hello.example.com
        forward_host: app.kyq-hello.svc.cluster.local
        forward_port: 8080
        certificate_id: 1
`

func TestKubeNPMURL(t *testing.T) {
	if _, err := Parse([]byte(kubeEdgeStack)); err != nil {
		t.Fatal(err)
	}
	ns, svc, port, ok := KubeService("kube://kyq-npm/app:81")
	if !ok || ns != "kyq-npm" || svc != "app" || port != 81 {
		t.Fatalf("parsed %q %q %d %v", ns, svc, port, ok)
	}
	for name, url := range map[string]string{
		"no port":         "kube://kyq-npm/app",
		"zero port":       "kube://kyq-npm/app:0",
		"leading zero":    "kube://kyq-npm/app:081",
		"port too large":  "kube://kyq-npm/app:65536",
		"upper case":      "kube://Kyq-npm/app:81",
		"dotted ns":       "kube://kyq.npm/app:81",
		"service digit":   "kube://kyq-npm/1app:81",
		"extra path":      "kube://kyq-npm/app/x:81",
		"query":           "kube://kyq-npm/app:81?x=1",
		"credentials":     "kube://u@kyq-npm/app:81",
		"long label":      "kube://" + strings.Repeat("a", 64) + "/app:81",
		"trailing hyphen": "kube://kyq-npm-/app:81",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, ok := KubeService(url); ok {
				t.Fatal("parsed")
			}
			if _, err := Parse([]byte(strings.Replace(kubeEdgeStack, "kube://kyq-npm/app:81", url, 1))); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	onDocker := strings.Replace(kubeEdgeStack, "  - target: k8s\n    provider", "  - target: nas\n    provider", 1)
	onDocker = strings.Replace(onDocker, "  - name: hello\n    target: k8s", "  - name: hello\n    target: nas", 1)
	if _, err := Parse([]byte(onDocker)); err == nil || !strings.Contains(err.Error(), "needs a Kubernetes target") {
		t.Fatalf("kube:// on a Docker target: %v", err)
	}
	// The cluster's edge routes to an app on a Docker host; a Docker edge never reaches into a cluster.
	crossDocker := strings.Replace(kubeEdgeStack, "  - name: hello\n    target: k8s", "  - name: hello\n    target: nas", 1)
	if _, err := Parse([]byte(crossDocker)); err != nil {
		t.Fatalf("lan route to a Docker app: %v", err)
	}
	crossCluster := strings.Replace(edgeStack, "  - name: nas\n", "  - name: k8s\n    kubeconfig: /kc\n  - name: nas\n", 1)
	crossCluster = strings.Replace(crossCluster, "    target: nas\nedges", "    target: k8s\nedges", 1)
	if _, err := Parse([]byte(crossCluster)); err == nil || !strings.Contains(err.Error(), "on this target or on a Docker target") {
		t.Fatalf("Docker edge route into a cluster: %v", err)
	}
	admin := strings.Replace(kubeEdgeStack, "forward_host: app.kyq-hello.svc.cluster.local\n        forward_port: 8080", "forward_host: app.kyq-npm.svc\n        forward_port: 82", 1)
	admin = strings.Replace(admin, "kube://kyq-npm/app:81", "kube://kyq-npm/app:82", 1)
	if _, err := Parse([]byte(admin)); err == nil || !strings.Contains(err.Error(), "administration endpoint") {
		t.Fatalf("route to the kube:// admin service: %v", err)
	}
}

func TestInstallationIDAndNodePorts(t *testing.T) {
	id := NewInstallationID()
	s, err := Parse([]byte(kubeEdgeStack + "installation_id: " + id + "\n"))
	if err != nil || s.InstallationID != id || id == NewInstallationID() {
		t.Fatalf("id %q: %v", id, err)
	}
	for name, bad := range map[string]string{
		"upper":    strings.ToUpper(id),
		"braces":   "{" + id + "}",
		"urn":      "urn:uuid:" + id,
		"version1": "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		"short":    id[:35],
	} {
		if _, err := Parse([]byte(kubeEdgeStack + "installation_id: '" + bad + "'\n")); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	ports := strings.Replace(kubeEdgeStack, "  - name: npm\n    target: k8s\n", "  - name: npm\n    target: k8s\n    node_ports: {80: 31080, 443: 31443}\n", 1)
	s, err = Parse([]byte(ports))
	if err != nil || s.Apps[0].NodePorts[443] != 31443 {
		t.Fatalf("node_ports: %v", err)
	}
	for _, bad := range []string{"{80: 8080}", "{80: 31080, 443: 31080}"} {
		if _, err := Parse([]byte(strings.Replace(ports, "{80: 31080, 443: 31443}", bad, 1))); err == nil {
			t.Errorf("node_ports %s accepted", bad)
		}
	}
}

const certEdgeStack = `version: 1
targets:
  - name: k8s
    kubeconfig: /kc
  - name: nas
    host: 192.0.2.1
    user: kyq
apps:
  - name: hello
    target: k8s
edges:
  - target: k8s
    provider: cloudflare
    npm_url: kube://kyq-npm/app:81
    connector_cidr: 172.18.0.2/32
    certificates:
      - name: k80
        domains: ['*.k80.example.com', k80.example.com]
        email: ops+acme@example.com
        dns_provider: cloudflare
        credentials: {secret: cloudflare-dns, key: api_token}
    routes:
      - app: hello
        hostname: hello.k80.example.com
        forward_host: app.kyq-hello.svc.cluster.local
        forward_port: 8080
        certificate: k80
`

func TestEdgeCertificates(t *testing.T) {
	s, err := Parse([]byte(certEdgeStack))
	if err != nil {
		t.Fatal(err)
	}
	if c := s.Edges[0].Certificates[0]; c.Credentials.Secret != "cloudflare-dns" || s.Edges[0].Routes[0].Certificate != "k80" {
		t.Fatalf("parsed %+v", c)
	}
	for name, pair := range map[string][2]string{
		"both certificate fields":   {"certificate: k80", "certificate: k80\n        certificate_id: 3"},
		"no certificate":            {"        certificate: k80\n", ""},
		"unknown certificate":       {"certificate: k80", "certificate: other"},
		"not covered":               {"hostname: hello.k80.example.com", "hostname: hello.example.org"},
		"wildcard covers one level": {"hostname: hello.k80.example.com", "hostname: a.hello.k80.example.com"},
		"inner wildcard":            {"'*.k80.example.com'", "'a.*.example.com'"},
		"double wildcard":           {"'*.k80.example.com'", "'*.*.example.com'"},
		"wildcard tld":              {"'*.k80.example.com'", "'*.com'"},
		"upper case domain":         {"k80.example.com]", "K80.example.com]"},
		"duplicate domain":          {"k80.example.com]", "'*.k80.example.com']"},
		"no domains":                {"['*.k80.example.com', k80.example.com]", "[]"},
		"provider allowlist":        {"dns_provider: cloudflare", "dns_provider: route53"},
		"no provider":               {"        dns_provider: cloudflare\n", ""},
		"display-name email":        {"email: ops+acme@example.com", "email: 'Ops <ops@example.com>'"},
		"bare email":                {"email: ops+acme@example.com", "email: ops"},
		"dotless email domain":      {"email: ops+acme@example.com", "email: ops@example"},
		"no email":                  {"        email: ops+acme@example.com\n", ""},
		"bad name":                  {"name: k80\n", "name: K_80\n"},
		"installer secret":          {"secret: cloudflare-dns", "secret: kyq-credentials"},
		"no key":                    {"key: api_token", "key: ''"},
		"bad key":                   {"key: api_token", "key: a/b"},
		"docker target":             {"  - name: hello\n    target: k8s", "  - name: hello\n    target: nas"},
	} {
		t.Run(name, func(t *testing.T) {
			bad := strings.Replace(certEdgeStack, pair[0], pair[1], 1)
			if name == "docker target" {
				bad = strings.Replace(bad, "  - target: k8s\n    provider: cloudflare\n    npm_url: kube://kyq-npm/app:81", "  - target: nas\n    provider: cloudflare\n    npm_url: https://npm.example.com", 1)
			}
			if bad == certEdgeStack {
				t.Fatal("replacement did not apply")
			}
			_, err := Parse([]byte(bad))
			if err == nil || name == "docker target" && !strings.Contains(err.Error(), "need a Kubernetes target") {
				t.Fatalf("accepted invalid certificate configuration: %v", err)
			}
		})
	}
	dup := strings.Replace(certEdgeStack, "    routes:", `      - name: k80
        domains: [k80.example.com]
        email: ops@example.com
        dns_provider: cloudflare
        credentials: {secret: cloudflare-dns, key: api_token}
    routes:`, 1)
	if _, err := Parse([]byte(dup)); err == nil {
		t.Fatal("accepted a duplicate certificate name")
	}
}

func TestCovers(t *testing.T) {
	for _, c := range []struct {
		domain, host string
		ok           bool
	}{
		{"a.example.com", "a.example.com", true},
		{"*.example.com", "a.example.com", true},
		{"*.example.com", "a.b.example.com", false},
		{"*.example.com", "example.com", false},
		{"*.b.example.com", "a.example.com", false},
	} {
		if Covers(c.domain, c.host) != c.ok {
			t.Errorf("Covers(%q, %q) != %v", c.domain, c.host, c.ok)
		}
	}
}

func TestEdgeLANDNS(t *testing.T) {
	good := strings.Replace(certEdgeStack, "    routes:", `    lan_dns:
      provider: kydns
      url: https://kydns.example.com/
      credentials: {secret: kydns-api, key: token}
      address: 192.168.1.81
    routes:`, 1)
	s, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if d := s.Edges[0].LANDNS; d == nil || d.Address != "192.168.1.81" || d.Credentials.Secret != "kydns-api" {
		t.Fatalf("parsed %+v", d)
	}
	for name, pair := range map[string][2]string{
		"provider":       {"provider: kydns", "provider: pihole"},
		"plain http":     {"https://kydns.example.com/", "http://kydns.example.com/"},
		"url path":       {"https://kydns.example.com/", "https://kydns.example.com/api"},
		"url secret":     {"https://kydns.example.com/", "https://u:p@kydns.example.com/"},
		"url query":      {"https://kydns.example.com/", "https://kydns.example.com/?x=1"},
		"ipv6 address":   {"address: 192.168.1.81", "address: 'fd00::81'"},
		"loopback":       {"address: 192.168.1.81", "address: 127.0.0.1"},
		"unspecified":    {"address: 192.168.1.81", "address: 0.0.0.0"},
		"not an address": {"address: 192.168.1.81", "address: k80"},
		"secret":         {"secret: kydns-api", "secret: kyq-credentials"},
	} {
		t.Run(name, func(t *testing.T) {
			bad := strings.Replace(good, pair[0], pair[1], 1)
			if bad == good {
				t.Fatal("replacement did not apply")
			}
			if _, err := Parse([]byte(bad)); err == nil {
				t.Fatal("accepted invalid lan_dns")
			}
		})
	}
	docker := strings.Replace(edgeStack, "    routes:", "    lan_dns: {provider: kydns, url: 'https://kydns.example.com', credentials: {secret: kydns-api, key: token}, address: 192.168.1.81}\n    routes:", 1)
	if _, err := Parse([]byte(docker)); err == nil || !strings.Contains(err.Error(), "needs a Kubernetes target") {
		t.Fatalf("lan_dns on a Docker edge: %v", err)
	}
}

func TestLANEdge(t *testing.T) {
	lan := strings.Replace(kubeEdgeStack, "provider: cloudflare\n    npm_url: kube://kyq-npm/app:81\n    connector_cidr: 172.18.0.2/32\n",
		"provider: lan\n    npm_url: kube://kyq-npm/app:81\n    allow_cidrs: [192.168.1.0/24, 'fd00:1::/48']\n", 1)
	s, err := Parse([]byte(lan))
	if err != nil {
		t.Fatal(err)
	}
	if e := s.Edges[0]; e.ClientPort != 443 || len(e.AllowCIDRs) != 2 || e.ProxyPort != 0 {
		t.Fatalf("parsed %+v", e)
	}
	for name, pair := range map[string][2]string{
		"no allow_cidrs":      {"    allow_cidrs: [192.168.1.0/24, 'fd00:1::/48']\n", ""},
		"empty allow_cidrs":   {"[192.168.1.0/24, 'fd00:1::/48']", "[]"},
		"v4 default":          {"192.168.1.0/24", "0.0.0.0/0"},
		"v6 default":          {"'fd00:1::/48'", "'::/0'"},
		"v4 too wide":         {"192.168.1.0/24", "128.0.0.0/7"},
		"v6 too wide":         {"'fd00:1::/48'", "'fd00::/31'"},
		"host bits":           {"192.168.1.0/24", "192.168.1.5/24"},
		"bare address":        {"192.168.1.0/24", "192.168.1.5"},
		"not canonical":       {"'fd00:1::/48'", "'FD00:1::/48'"},
		"mapped v4":           {"'fd00:1::/48'", "'::ffff:192.168.1.0/120'"},
		"duplicate":           {"'fd00:1::/48'", "192.168.1.0/24"},
		"directive injection": {"'fd00:1::/48'", "'10.0.0.0/8; allow all'"},
		"connector_cidr":      {"provider: lan\n", "provider: lan\n    connector_cidr: 172.18.0.2/32\n"},
		"proxy_port":          {"provider: lan\n", "provider: lan\n    proxy_port: 8443\n"},
		"connector":           {"provider: lan\n", "provider: lan\n    connector: {network: edge, npm_host: npm}\n"},
		"client_port range":   {"provider: lan\n", "provider: lan\n    client_port: 70000\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(strings.Replace(lan, pair[0], pair[1], 1))); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	many := "["
	for i := range 33 {
		many += fmt.Sprintf("10.%d.0.0/16,", i)
	}
	if _, err := Parse([]byte(strings.Replace(lan, "[192.168.1.0/24, 'fd00:1::/48']", strings.TrimSuffix(many, ",")+"]", 1))); err == nil {
		t.Fatal("accepted 33 prefixes")
	}
	// Tunnel edges take no lan fields and are otherwise unchanged.
	for _, extra := range []string{"    allow_cidrs: [192.168.1.0/24]\n", "    client_port: 30543\n"} {
		if _, err := Parse([]byte(strings.Replace(kubeEdgeStack, "    connector_cidr: 172.18.0.2/32\n", "    connector_cidr: 172.18.0.2/32\n"+extra, 1))); err == nil {
			t.Fatalf("cloudflare accepted %s", extra)
		}
	}
	if _, err := Parse([]byte(strings.Replace(kubeEdgeStack, "    connector_cidr: 172.18.0.2/32\n", "", 1))); err == nil {
		t.Fatal("cloudflare accepted no connector_cidr")
	}
}

func TestURLHost(t *testing.T) {
	r := Route{Hostname: "identity.example.com"}
	for _, c := range []struct {
		port int
		want string
	}{{0, "identity.example.com"}, {443, "identity.example.com"}, {30543, "identity.example.com:30543"}} {
		if got := (Edge{Provider: "lan", ClientPort: c.port}).URLHost(r); got != c.want {
			t.Errorf("client_port %d: URLHost = %q, want %q", c.port, got, c.want)
		}
	}
	// client_port is how LAN clients reach NPM; the tunnel serves external routes on 443.
	tunnel := Edge{Provider: "cloudflare", ClientPort: 30543}
	if got := tunnel.URLHost(r); got != "identity.example.com" {
		t.Errorf("external route: URLHost = %q", got)
	}
	r.Exposure = "internal"
	if got := tunnel.URLHost(r); got != "identity.example.com:30543" {
		t.Errorf("internal route on a tunnel edge: URLHost = %q", got)
	}
}

// A tunnel edge serves internal routes only to allow_cidrs, which must not hold the connector;
// a lan edge has no external routes and a cloudflare-direct edge no internal ones.
func TestRouteExposure(t *testing.T) {
	internal := strings.Replace(kubeEdgeStack, "connector_cidr: 172.18.0.2/32\n", "connector_cidr: 172.18.0.2/32\n    allow_cidrs: [192.168.1.0/24]\n", 1)
	internal = strings.Replace(internal, "        certificate_id: 1\n", "        certificate_id: 1\n        exposure: internal\n", 1)
	internal += "      - app: npm\n        hostname: www.example.com\n        forward_host: app.kyq-npm.svc.cluster.local\n        forward_port: 8080\n        certificate_id: 1\n"
	s, err := Parse([]byte(internal))
	if err != nil {
		t.Fatal(err)
	}
	if e := s.Edges[0]; !e.Internal(e.Routes[0]) || e.ClientPort != 443 {
		t.Fatalf("parsed %+v", e)
	}
	if e := s.Edges[0]; e.Internal(Route{}) || !(Edge{Provider: "lan"}).Internal(Route{}) {
		t.Fatal("an unmarked route must take its edge's exposure")
	}
	lan := strings.Replace(kubeEdgeStack, "provider: cloudflare\n    npm_url: kube://kyq-npm/app:81\n    connector_cidr: 172.18.0.2/32\n",
		"provider: lan\n    npm_url: kube://kyq-npm/app:81\n    allow_cidrs: [192.168.1.0/24]\n", 1)
	for name, c := range map[string]struct{ plan, old, new, want string }{
		"unknown exposure":         {internal, "exposure: internal", "exposure: public", "internal or external"},
		"internal needs allow":     {internal, "    allow_cidrs: [192.168.1.0/24]\n", "", "need allow_cidrs"},
		"allow without internal":   {internal, "        exposure: internal\n", "", "has none"},
		"connector in allow_cidrs": {internal, "[192.168.1.0/24]", "[172.16.0.0/12]", "must not contain the connector"},
		"every route internal":     {internal, "kyq-npm.svc.cluster.local\n        forward_port: 8080\n        certificate_id: 1\n", "kyq-npm.svc.cluster.local\n        forward_port: 8080\n        certificate_id: 1\n        exposure: internal\n", "every route is internal"},
		"external on lan":          {lan, "        certificate_id: 1\n", "        certificate_id: 1\n        exposure: external\n", "no public connection"},
	} {
		t.Run(name, func(t *testing.T) {
			bad := strings.Replace(c.plan, c.old, c.new, 1)
			if bad == c.plan {
				t.Fatal("replacement did not apply")
			}
			if _, err := Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
	if _, err := Parse([]byte(strings.Replace(lan, "        certificate_id: 1\n", "        certificate_id: 1\n        exposure: internal\n", 1))); err != nil {
		t.Fatalf("explicit internal on lan: %v", err)
	}
}

// domains keeps internal names under the internal domain and external names outside it.
func TestDomains(t *testing.T) {
	lan := strings.Replace(kubeEdgeStack, "provider: cloudflare\n    npm_url: kube://kyq-npm/app:81\n    connector_cidr: 172.18.0.2/32\n",
		"provider: lan\n    npm_url: kube://kyq-npm/app:81\n    allow_cidrs: [192.168.1.0/24]\n", 1)
	withDomains := func(plan, internal, external string) string {
		return strings.Replace(plan, "edges:\n", fmt.Sprintf("domains: {internal: %s, external: %s}\nedges:\n", internal, external), 1)
	}
	for name, c := range map[string]struct {
		plan string
		ok   bool
	}{
		"internal route under internal": {withDomains(strings.Replace(lan, "hello.example.com", "hello.lan.example.com", 1), "lan.example.com", "example.com"), true},
		"internal route outside it":     {withDomains(lan, "lan.example.com", "example.com"), false},
		"external route under external": {withDomains(kubeEdgeStack, "lan.example.com", "example.com"), true},
		"external route in internal":    {withDomains(strings.Replace(kubeEdgeStack, "hello.example.com", "hello.lan.example.com", 1), "lan.example.com", "example.com"), false},
		"external route elsewhere":      {withDomains(kubeEdgeStack, "lan.example.com", "example.net"), false},
		"same domain":                   {withDomains(kubeEdgeStack, "example.com", "example.com"), false},
		"external inside internal":      {withDomains(kubeEdgeStack, "example.com", "pub.example.com"), false},
		"not a name":                    {withDomains(kubeEdgeStack, "'*.lan.example.com'", "example.com"), false},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(c.plan)); (err == nil) != c.ok {
				t.Fatalf("ok=%v: %v", c.ok, err)
			}
		})
	}
}

// AC3: egress_address is one IP literal, never a range, name, loopback or unspecified address.
func TestEgressAddress(t *testing.T) {
	if s, err := Parse([]byte(edgeStack)); err != nil || s.Edges[0].EgressAddress != "" {
		t.Fatalf("edge without egress_address: %v", err)
	}
	with := func(v string) string {
		return strings.Replace(edgeStack, "    connector_cidr:", "    egress_address: '"+v+"'\n    connector_cidr:", 1)
	}
	for _, ok := range []string{"192.168.1.20", "10.0.0.7", "fd00::20", "2001:db8::1"} {
		if s, err := Parse([]byte(with(ok))); err != nil || s.Edges[0].EgressAddress != ok {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"192.168.1.0/24", "192.168.1.20/32", "fd00::/64", "npm.example.com", "0.0.0.0", "::", "127.0.0.1", "::1", "fe80::1%eth0", "192.168.1.20 ", " "} {
		if _, err := Parse([]byte(with(bad))); err == nil || !strings.Contains(err.Error(), "egress_address") {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}

const directStack = `version: 1
targets:
  - name: nas
    host: 192.0.2.1
    user: kyq
  - name: other
    host: 192.0.2.2
    user: kyq
  - name: k8s
    kubeconfig: /kc
apps:
  - name: hello
    target: nas
  - name: far
    target: other
edges:
  - target: nas
    provider: cloudflare-direct
    connector_cidr: 172.30.0.3/32
    connector:
      network: edge-net
      tunnel_id: 12345678-1234-1234-1234-123456789012
    routes:
      - app: hello
        hostname: hello.example.com
        forward_host: 127.0.0.1
        forward_port: 18080
`

func TestCloudflareDirectEdge(t *testing.T) {
	s, err := Parse([]byte(directStack))
	if err != nil {
		t.Fatal(err)
	}
	e := s.Edges[0]
	if !e.Direct() || e.TrustedAddress() != "172.30.0.3" || e.Routes[0].ForwardScheme != "http" || e.Routes[0].StatusPath != "/api/status" {
		t.Fatalf("direct edge = %+v", e)
	}
	if out, err := yaml.Marshal(s); err != nil || strings.Contains(string(out), "npm_url") || strings.Contains(string(out), "npm_host") {
		t.Fatalf("saved plan writes empty NPM fields: %v\n%s", err, out)
	}
	for name, pair := range map[string][2]string{
		"kubernetes target":   {"  - target: nas\n    provider", "  - target: k8s\n    provider"},
		"npm_url":             {"    connector_cidr:", "    npm_url: https://npm.example.com\n    connector_cidr:"},
		"egress_address":      {"    connector_cidr:", "    egress_address: 172.30.0.9\n    connector_cidr:"},
		"allow_cidrs":         {"    connector_cidr:", "    allow_cidrs: [192.168.1.0/24]\n    connector_cidr:"},
		"proxy_port":          {"    connector_cidr:", "    proxy_port: 8443\n    connector_cidr:"},
		"broad trust":         {"172.30.0.3/32", "172.30.0.0/24"},
		"no connector":        {"    connector:\n      network: edge-net\n      tunnel_id: 12345678-1234-1234-1234-123456789012\n", ""},
		"npm_host":            {"      network: edge-net\n", "      network: edge-net\n      npm_host: npm\n"},
		"frp fields":          {"      network: edge-net\n", "      network: edge-net\n      frp_server: relay.example.com\n"},
		"bad tunnel":          {"12345678-1234-1234-1234-123456789012", "not-a-uuid"},
		"LAN forward_host":    {"forward_host: 127.0.0.1", "forward_host: 192.168.1.10"},
		"https origin":        {"forward_port: 18080", "forward_port: 18080\n        forward_scheme: https"},
		"certificate":         {"forward_port: 18080", "forward_port: 18080\n        certificate_id: 1"},
		"app on other target": {"app: hello", "app: far"},
		"internal route":      {"forward_port: 18080", "forward_port: 18080\n        exposure: internal"},
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(directStack, pair[0]) {
				t.Fatalf("fixture lacks %q", pair[0])
			}
			_, err := Parse([]byte(strings.Replace(directStack, pair[0], pair[1], 1)))
			if err == nil {
				t.Fatal("accepted unsafe cloudflare-direct edge")
			}
			t.Log(err)
		})
	}
}
