// Package edge reconciles only installer-owned proxy hosts through NPM's API.
package edge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

const hostPath = "/api/nginx/proxy-hosts"

type hostConfig struct {
	Domains     []string       `json:"domain_names"`
	Scheme      string         `json:"forward_scheme"`
	Host        string         `json:"forward_host"`
	Port        int            `json:"forward_port"`
	Certificate int            `json:"certificate_id"`
	SSLForced   bool           `json:"ssl_forced"`
	Websocket   bool           `json:"allow_websocket_upgrade"`
	HTTP2       bool           `json:"http2_support"`
	Cache       bool           `json:"caching_enabled"`
	Exploits    bool           `json:"block_exploits"`
	HSTS        bool           `json:"hsts_enabled"`
	Subdomains  bool           `json:"hsts_subdomains"`
	TrustProto  bool           `json:"trust_forwarded_proto"`
	AccessList  int            `json:"access_list_id"`
	Advanced    string         `json:"advanced_config"`
	Locations   []any          `json:"locations"`
	Enabled     bool           `json:"enabled"`
	Meta        map[string]any `json:"meta"`
}

type host struct {
	ID int `json:"id"`
	hostConfig
}

type NPM struct {
	base         string
	token        string
	installation string
	http         *http.Client
	lan          *http.Client // other LAN administration APIs: KyDNS
	// Adopt claims this target's hosts marked by builds that predate installation IDs, and
	// rewrites them to this installation's marks (apply --adopt-edge-hosts).
	Adopt bool
	// Secrets reads the edge's referenced credentials; set for a Kubernetes target.
	Secrets Secrets
	// Redact receives every credential read, before it is used.
	Redact *engine.Redactor
	// Bridges maps an app to the path its bridge serves on its route, over TLS on the port
	// after the app's forward_port.
	Bridges map[string]string
}

// Secrets reads target-held edge credentials by reference, and keeps the LAN DNS ledger
// (records carry no owner in KyDNS). Values stay in memory.
type Secrets interface {
	EdgeSecret(ctx context.Context, name, key string) ([]byte, error)
	LANDNSLedger(ctx context.Context) ([]byte, error) // empty when none yet
	SetLANDNSLedger(ctx context.Context, ledger []byte) error
}

// Dial opens one connection to NPM's API; for kube:// it is the Kubernetes driver's port-forward.
type Dial = func(ctx context.Context, network, address string) (net.Conn, error)

// NewNPM uses verified HTTPS on LAN-only administration, or for a kube:// origin plain HTTP
// inside tunnel, a port-forward the API server's TLS carries. Credentials stay in memory.
// installation is stack.yaml's installation_id, which marks the hosts this installation owns.
func NewNPM(origin, token, installation string, tunnel Dial) (*NPM, error) {
	if installation == "" {
		return nil, errors.New("NPM host ownership needs stack.yaml's installation_id; run kyquickstart apply")
	}
	if token == "" || len(token) > 16384 || strings.ContainsAny(token, "\r\n\x00") {
		return nil, errors.New("NPM token is empty or invalid")
	}
	base, transport, err := npmTransport(origin, tunnel)
	if err != nil {
		return nil, err
	}
	return &NPM{base: base, token: token, installation: installation, http: npmClient(transport), lan: npmClient(lanTransport()), Redact: &engine.Redactor{}}, nil
}

func npmClient(t *http.Transport) *http.Client {
	return &http.Client{Transport: t, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// npmTransport returns the base URL and transport for origin.
func npmTransport(origin string, tunnel Dial) (string, *http.Transport, error) {
	if ns, svc, port, ok := stack.KubeService(origin); ok {
		if tunnel == nil {
			return "", nil, errors.New("a kube:// NPM is reached only through the Kubernetes driver's port-forward")
		}
		// No environment proxy, and every connection goes through the tunnel.
		return fmt.Sprintf("http://%s.%s:%d", svc, ns, port), &http.Transport{DialContext: tunnel}, nil
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || tunnel != nil {
		return "", nil, errors.New("NPM requires an HTTPS origin without credentials, path or query")
	}
	return origin, lanTransport(), nil
}

// lanTransport verifies TLS and dials only private or loopback addresses, with no proxy.
func lanTransport() *http.Transport {
	return &http.Transport{
		// No environment proxy: administration and its bearer token stay on the LAN.
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				ip = ip.Unmap()
				if !ip.IsPrivate() && !ip.IsLoopback() {
					return nil, errors.New("LAN administration must resolve only to private or loopback addresses")
				}
			}
			var last error
			for _, ip := range ips {
				c, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return c, nil
				}
				last = err
			}
			if last == nil {
				last = errors.New("hostname has no addresses")
			}
			return nil, last
		},
	}
}

func (n *NPM) Close() {
	n.http.CloseIdleConnections()
	n.lan.CloseIdleConnections()
}

func (n *NPM) request(ctx context.Context, method, path string, body any, result any) error {
	_, err := call(ctx, n.http, n.base, n.token, method, path, body, result)
	return err
}

// call sends one API request; an empty token sends none. A non-2xx answer is an error carrying
// its status, never its body.
func call(ctx context.Context, client *http.Client, base, token, method, path string, body any, result any) (int, error) {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(data))
	if err != nil {
		return 0, errors.New("NPM request construction failed")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("NPM %s request failed: %w", method, engine.ErrTransient)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// Never echo an external API body; it may contain credentials or nginx config.
		if res.StatusCode == 429 || res.StatusCode >= 500 {
			return res.StatusCode, fmt.Errorf("NPM HTTP %d: %w", res.StatusCode, engine.ErrTransient)
		}
		return res.StatusCode, fmt.Errorf("NPM HTTP %d", res.StatusCode)
	}
	if result == nil {
		return res.StatusCode, nil
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 || json.Unmarshal(data, result) != nil {
		return res.StatusCode, errors.New("NPM response is invalid or exceeds 4 MiB")
	}
	return res.StatusCode, nil
}

// Ownership marks: an advanced_config first line and meta keys. Builds before installation IDs
// wrote only the target; those hosts are legacy, claimed only with Adopt.
func (n *NPM) prefix(e stack.Edge) string {
	return "# kyquickstart:" + n.installation + ":" + e.Target + ":"
}

func legacyPrefix(e stack.Edge) string { return "# kyquickstart:" + e.Target + ":" }

func (n *NPM) marker(e stack.Edge, r stack.Route) string {
	return n.prefix(e) + r.App + ":" + r.Hostname + "\n"
}

type claim int

const (
	foreign claim = iota
	legacy        // this target's marks from before installation IDs
	ours
)

// claim reads h's ownership; rest narrows the marker to one route ("" for any of the target's).
func (n *NPM) claim(h host, e stack.Edge, rest string) claim {
	if h.Meta["ky.managed-by"] != "kyquickstart" || h.Meta["ky.target"] != e.Target {
		return foreign
	}
	id, tagged := h.Meta["ky.installation"]
	switch {
	case id == n.installation && strings.HasPrefix(h.Advanced, n.prefix(e)+rest):
		return ours
	case !tagged && strings.HasPrefix(h.Advanced, legacyPrefix(e)+rest):
		return legacy
	}
	return foreign
}

// owns reports whether this run may change h: ours, or legacy when adopting.
func (n *NPM) owns(h host, e stack.Edge, rest string) bool {
	c := n.claim(h, e, rest)
	return c == ours || c == legacy && n.Adopt
}

// refuse explains why h cannot be changed, pointing a legacy host at --adopt-edge-hosts.
func (n *NPM) refuse(h host, e stack.Edge, rest, why string) error {
	if n.claim(h, e, rest) == legacy {
		return errors.New(why + ": it carries this target's marks from a build without installation IDs; if this installation owns it, rerun apply with --adopt-edge-hosts")
	}
	return errors.New(why)
}

// config is r's host; cert is its resolved certificate ID.
func (n *NPM) config(e stack.Edge, r stack.Route, cert int) hostConfig {
	sni := fmt.Sprintf("if ($ssl_server_name != %s) { return 421; }\n", r.Hostname)
	advanced, port := n.marker(e, r), 443
	if e.Provider == "lan" {
		// NPM's http block trusts X-Real-IP from every private range. Trust only unix
		// sockets, which NPM never listens on, so $remote_addr stays the TCP peer.
		advanced += "set_real_ip_from unix:;\n" + sni
		for _, c := range e.AllowCIDRs {
			advanced += "allow " + c + ";\n"
		}
		advanced += "deny all;\n"
		port = e.ClientPort
	} else {
		advanced += "set_real_ip_from " + e.ConnectorCIDR + ";\nreal_ip_recursive off;\n"
		advanced += fmt.Sprintf("if ($realip_remote_addr != %s) { return 403; }\n", strings.TrimSuffix(e.ConnectorCIDR, "/32")) + sni
		if e.Provider == "frp" {
			advanced += fmt.Sprintf("listen %d ssl proxy_protocol;\nreal_ip_header proxy_protocol;\n", e.ProxyPort)
		} else {
			advanced += "real_ip_header CF-Connecting-IP;\n"
		}
	}
	// A server-level proxy_set_header loses to NPM's default location. Own the whole
	// location, which also suppresses NPM's default forwarding-header configuration.
	headers := fmt.Sprintf(`  proxy_set_header Host $host;
  proxy_set_header X-Forwarded-Host $host;
  proxy_set_header X-Forwarded-For $remote_addr;
  proxy_set_header X-Real-IP $remote_addr;
  proxy_set_header X-Forwarded-Proto $scheme;
  proxy_set_header X-Forwarded-Scheme $scheme;
  proxy_set_header X-Forwarded-Port %d;
  proxy_set_header Forwarded "";
  proxy_set_header CF-Connecting-IP "";
  proxy_set_header True-Client-IP "";
  proxy_set_header Upgrade $http_upgrade;
  proxy_set_header Connection $http_connection;
  proxy_http_version 1.1;
`, port)
	advanced += "location / {\n" + headers + "  proxy_pass $forward_scheme://$server:$port$request_uri;\n}\n"
	if p, ok := n.Bridges[r.App]; ok {
		// The bridge's leaf is self-signed on its target, and NPM leaves proxy_ssl_verify off:
		// this hop's TLS hides the token on the LAN. KyIdentity verifies the edge's certificate.
		advanced += fmt.Sprintf("location %s/ {\n%s  proxy_pass https://%s:%d;\n}\n", p, headers, r.ForwardHost, r.ForwardPort+1)
	}
	return hostConfig{
		Domains: []string{r.Hostname}, Scheme: r.ForwardScheme, Host: r.ForwardHost,
		Port: r.ForwardPort, Certificate: cert, SSLForced: true,
		Websocket: true, HTTP2: true, Advanced: advanced, Locations: []any{},
		Meta: map[string]any{"ky.managed-by": "kyquickstart", "ky.target": e.Target, "ky.installation": n.installation},
	}
}

// overlaps detects foreign wildcard hosts as well as an exact hostname collision.
func overlaps(pattern, name string) bool {
	pattern = strings.ToLower(pattern)
	return pattern == name || strings.HasPrefix(pattern, "*.") && strings.HasSuffix(name, pattern[1:])
}

func (n *NPM) find(ctx context.Context, e stack.Edge, r stack.Route) (*host, error) {
	var hosts []host
	if err := n.request(ctx, http.MethodGet, hostPath, nil, &hosts); err != nil {
		return nil, err
	}
	for _, path := range []string{"/api/nginx/redirection-hosts", "/api/nginx/dead-hosts"} {
		var foreign []struct {
			Domains []string `json:"domain_names"`
		}
		if err := n.request(ctx, http.MethodGet, path, nil, &foreign); err != nil {
			return nil, err
		}
		for _, h := range foreign {
			for _, name := range h.Domains {
				if overlaps(name, r.Hostname) {
					return nil, fmt.Errorf("hostname %s is claimed by an NPM redirect or dead host", r.Hostname)
				}
			}
		}
	}
	if e.Provider == "frp" {
		var streams []struct {
			Port int `json:"incoming_port"`
		}
		if err := n.request(ctx, http.MethodGet, "/api/nginx/streams", nil, &streams); err != nil {
			return nil, err
		}
		for _, stream := range streams {
			if stream.Port == e.ProxyPort {
				return nil, fmt.Errorf("frp listener port %d is already claimed by an NPM stream", e.ProxyPort)
			}
		}
	}
	var found *host
	listener := regexp.MustCompile(fmt.Sprintf(`\blisten\s+(?:[^\s;]+:)?%d(?:\s|;)`, e.ProxyPort))
	for i := range hosts {
		h := &hosts[i]
		if e.Provider == "frp" && listener.MatchString(h.Advanced) && !n.owns(*h, e, "") {
			return nil, n.refuse(*h, e, "", fmt.Sprintf("frp listener port %d is already claimed by a foreign NPM host", e.ProxyPort))
		}
		for _, name := range h.Domains {
			if !overlaps(name, r.Hostname) {
				continue
			}
			rest := r.App + ":" + r.Hostname + "\n"
			if h.ID < 1 || len(h.Domains) != 1 || name != r.Hostname || !n.owns(*h, e, rest) {
				return nil, n.refuse(*h, e, rest, fmt.Sprintf("hostname %s is claimed by an unmanaged or different edge host; refusing adoption", r.Hostname))
			}
			if found != nil {
				return nil, fmt.Errorf("hostname %s has multiple NPM owners", r.Hostname)
			}
			found = h
		}
	}
	return found, nil
}

func matches(h *host, want hostConfig) bool {
	if h == nil {
		return false
	}
	have := h.hostConfig
	// NPM adds runtime metadata; compare our ownership keys separately in find.
	have.Meta, want.Meta = nil, nil
	have.Enabled, want.Enabled = false, false
	return reflect.DeepEqual(have, want)
}

type hostStep struct {
	n *NPM
	e stack.Edge
	r stack.Route
}

// RouteApp names the app whose route step st is, "" for any other step.
func RouteApp(st engine.Step) string {
	if h, ok := st.(hostStep); ok {
		return h.r.App
	}
	return ""
}

func (s hostStep) ID() string {
	sum := sha256.Sum256([]byte(s.r.Hostname))
	return s.e.Target + ".edge." + hex.EncodeToString(sum[:8])
}
func (s hostStep) InputHash() string {
	b, _ := json.Marshal(struct {
		API         string
		Config      hostConfig
		Certificate string
	}{s.n.base, s.n.config(s.e, s.r, s.r.CertificateID), s.r.Certificate})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// want resolves the route's certificate and returns its host configuration.
func (s hostStep) want(ctx context.Context) (hostConfig, error) {
	cert, err := s.n.certificateID(ctx, s.e, s.r)
	return s.n.config(s.e, s.r, cert), err
}
func (s hostStep) Inspect(ctx context.Context) (bool, error) {
	want, err := s.want(ctx)
	if err != nil {
		return false, err
	}
	h, err := s.n.find(ctx, s.e, s.r)
	return h != nil && matches(h, want) && h.Enabled && h.Meta["nginx_online"] == true, err
}
func (s hostStep) Apply(ctx context.Context) error {
	want, err := s.want(ctx) // disabled until its API representation is verified
	if err != nil {
		return err
	}
	h, err := s.n.find(ctx, s.e, s.r)
	if err != nil {
		return err
	}
	if h == nil {
		if err = s.n.request(ctx, http.MethodPost, hostPath, want, &host{}); err != nil {
			return err
		}
	} else if !matches(h, want) {
		// Disable first: a failed update must not leave a stale route serving traffic.
		if h.Enabled {
			if err = s.n.request(ctx, http.MethodPost, fmt.Sprintf("%s/%d/disable", hostPath, h.ID), nil, nil); err != nil {
				return err
			}
		}
		if err = s.n.request(ctx, http.MethodPut, fmt.Sprintf("%s/%d", hostPath, h.ID), want, nil); err != nil {
			return err
		}
	}
	h, err = s.n.find(ctx, s.e, s.r)
	if err != nil {
		return err
	}
	if !matches(h, want) {
		return errors.New("NPM did not persist the requested disabled configuration")
	}
	if h.Enabled && h.Meta["nginx_online"] != true {
		if err = s.n.request(ctx, http.MethodPost, fmt.Sprintf("%s/%d/disable", hostPath, h.ID), nil, nil); err != nil {
			return err
		}
		h.Enabled = false
	}
	if !h.Enabled {
		if err = s.n.request(ctx, http.MethodPost, fmt.Sprintf("%s/%d/enable", hostPath, h.ID), nil, nil); err != nil {
			return err
		}
		if err = s.Verify(ctx); err != nil {
			cleanup := s.n.request(ctx, http.MethodPost, fmt.Sprintf("%s/%d/disable", hostPath, h.ID), nil, nil)
			return errors.Join(err, cleanup)
		}
	}
	return nil
}
func (s hostStep) Verify(ctx context.Context) error {
	ok, err := s.Inspect(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("NPM host is absent, differs, disabled or failed its nginx configuration check")
	}
	return nil
}

// removedStep disables this target's installer-owned hosts whose hostname left stack.yaml.
// Disabling is reversible; deletion is left to the operator. When adopting, it also re-marks
// this target's legacy hosts outside the routes, so no legacy mark outlives adoption.
type removedStep struct {
	n *NPM
	e stack.Edge
}

func (s removedStep) ID() string { return s.e.Target + ".edge.removed" }
func (s removedStep) InputHash() string {
	names := []string{s.n.base, s.n.installation, fmt.Sprint(s.n.Adopt)}
	for _, r := range s.e.Routes {
		names = append(names, r.Hostname)
	}
	sum := sha256.Sum256([]byte(strings.Join(names, "\n")))
	return hex.EncodeToString(sum[:])
}

// pending lists hosts outside the routes this run must change: enabled ones it owns, and when
// adopting, legacy ones.
func (s removedStep) pending(ctx context.Context) ([]host, error) {
	var hosts []host
	if err := s.n.request(ctx, http.MethodGet, hostPath, nil, &hosts); err != nil {
		return nil, err
	}
	routed := map[string]bool{}
	for _, r := range s.e.Routes {
		routed[r.Hostname] = true
	}
	var out []host
	for _, h := range hosts {
		if len(h.Domains) == 1 && routed[h.Domains[0]] {
			continue
		}
		if h.Enabled && s.n.owns(h, s.e, "") || s.n.Adopt && s.n.claim(h, s.e, "") == legacy {
			out = append(out, h)
		}
	}
	return out, nil
}
func (s removedStep) Inspect(ctx context.Context) (bool, error) {
	hosts, err := s.pending(ctx)
	return len(hosts) == 0, err
}
func (s removedStep) Apply(ctx context.Context) error {
	hosts, err := s.pending(ctx)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		if h.Enabled {
			if err := s.n.request(ctx, http.MethodPost, fmt.Sprintf("%s/%d/disable", hostPath, h.ID), nil, nil); err != nil {
				return err
			}
		}
		if s.n.claim(h, s.e, "") == legacy {
			// NPM merges a partial update; the host stays disabled.
			remark := map[string]any{
				"advanced_config": s.n.prefix(s.e) + strings.TrimPrefix(h.Advanced, legacyPrefix(s.e)),
				"meta":            map[string]any{"ky.installation": s.n.installation},
			}
			if err := s.n.request(ctx, http.MethodPut, fmt.Sprintf("%s/%d", hostPath, h.ID), remark, nil); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s removedStep) Verify(ctx context.Context) error {
	hosts, err := s.pending(ctx)
	if err != nil {
		return err
	}
	if len(hosts) > 0 {
		return fmt.Errorf("installer-owned NPM host %q is enabled or still legacy-marked but absent from stack.yaml", strings.Join(hosts[0].Domains, ","))
	}
	return nil
}

// Steps requires an Edge already validated by stack.Parse. Removed routes are disabled first,
// then certificates are issued before the routes that use them.
func (n *NPM) Steps(e stack.Edge) []engine.Step {
	out := []engine.Step{removedStep{n: n, e: e}}
	for _, c := range e.Certificates {
		out = append(out, certStep{n: n, e: e, c: c})
	}
	for _, r := range e.Routes {
		out = append(out, hostStep{n: n, e: e, r: r})
	}
	if e.LANDNS != nil {
		out = append(out, dnsStep{n: n, e: e})
	}
	return out
}

// Preflight is read-only and checks ownership of every intended route and certificate before
// mutations. A named certificate not yet issued is checked by its credential and NPM user.
func (n *NPM) Preflight(ctx context.Context, e stack.Edge) error {
	for _, c := range e.Certificates {
		if _, err := (certStep{n: n, e: e, c: c}).check(ctx); err != nil {
			return err
		}
		if _, err := n.acmeUser(ctx, e, c.Email); err != nil {
			return err
		}
		if _, err := n.dnsCredential(ctx, c); err != nil {
			return err
		}
	}
	for _, r := range e.Routes {
		if _, err := n.find(ctx, e, r); err != nil {
			return err
		}
		if r.Certificate != "" {
			continue // covered by validation; issued and checked by its step
		}
		var cert certificate
		if err := n.request(ctx, http.MethodGet, fmt.Sprintf("/api/nginx/certificates/%d", r.CertificateID), nil, &cert); err != nil {
			return err
		}
		expires, err := expiry(cert.Expires)
		if !slices.ContainsFunc(cert.Domains, func(d string) bool { return stack.Covers(d, r.Hostname) }) || err != nil || !expires.After(time.Now()) {
			return fmt.Errorf("certificate %d must be unexpired and cover %s", r.CertificateID, r.Hostname)
		}
	}
	if e.LANDNS != nil {
		s := dnsStep{n: n, e: e}
		k, err := s.client(ctx)
		if err != nil {
			return err
		}
		if _, err := s.plan(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

// expiry parses NPM's expires_on: RFC 3339, or the UTC SQL timestamp its SQLite backend emits.
func expiry(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse("2006-01-02 15:04:05", s)
	}
	return t, err
}
