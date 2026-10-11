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
	base  string
	token string
	http  *http.Client
}

// NewNPM uses verified HTTPS on LAN-only administration. Credentials stay in memory.
func NewNPM(origin, token string) (*NPM, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("NPM requires an HTTPS origin without credentials, path or query")
	}
	if token == "" || len(token) > 16384 || strings.ContainsAny(token, "\r\n\x00") {
		return nil, errors.New("NPM token is empty or invalid")
	}
	transport := &http.Transport{
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
					return nil, errors.New("NPM administration must resolve only to private or loopback addresses")
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
				last = errors.New("NPM hostname has no addresses")
			}
			return nil, last
		},
	}
	return &NPM{base: origin, token: token, http: &http.Client{
		Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (n *NPM) Close() { n.http.CloseIdleConnections() }

func (n *NPM) request(ctx context.Context, method, path string, body any, result any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, n.base+path, bytes.NewReader(data))
	if err != nil {
		return errors.New("NPM request construction failed")
	}
	req.Header.Set("Authorization", "Bearer "+n.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := n.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("NPM %s request failed: %w", method, engine.ErrTransient)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// Never echo an external API body; it may contain credentials or nginx config.
		if res.StatusCode == 429 || res.StatusCode >= 500 {
			return fmt.Errorf("NPM HTTP %d: %w", res.StatusCode, engine.ErrTransient)
		}
		return fmt.Errorf("NPM HTTP %d", res.StatusCode)
	}
	if result == nil {
		return nil
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 || json.Unmarshal(data, result) != nil {
		return errors.New("NPM response is invalid or exceeds 4 MiB")
	}
	return nil
}

func marker(e stack.Edge, r stack.Route) string {
	return "# kyquickstart:" + e.Target + ":" + r.App + ":" + r.Hostname + "\n"
}

func config(e stack.Edge, r stack.Route) hostConfig {
	advanced := marker(e, r) + "set_real_ip_from " + e.ConnectorCIDR + ";\nreal_ip_recursive off;\n"
	advanced += fmt.Sprintf("if ($realip_remote_addr != %s) { return 403; }\nif ($ssl_server_name != %s) { return 421; }\n", strings.TrimSuffix(e.ConnectorCIDR, "/32"), r.Hostname)
	if e.Provider == "frp" {
		advanced += fmt.Sprintf("listen %d ssl proxy_protocol;\nreal_ip_header proxy_protocol;\n", e.ProxyPort)
	} else {
		advanced += "real_ip_header CF-Connecting-IP;\n"
	}
	// A server-level proxy_set_header loses to NPM's default location. Own the whole
	// location, which also suppresses NPM's default forwarding-header configuration.
	advanced += `location / {
  proxy_set_header Host $host;
  proxy_set_header X-Forwarded-Host $host;
  proxy_set_header X-Forwarded-For $remote_addr;
  proxy_set_header X-Real-IP $remote_addr;
  proxy_set_header X-Forwarded-Proto $scheme;
  proxy_set_header X-Forwarded-Scheme $scheme;
  proxy_set_header X-Forwarded-Port 443;
  proxy_set_header Forwarded "";
  proxy_set_header CF-Connecting-IP "";
  proxy_set_header True-Client-IP "";
  proxy_set_header Upgrade $http_upgrade;
  proxy_set_header Connection $http_connection;
  proxy_http_version 1.1;
  proxy_pass $forward_scheme://$server:$port$request_uri;
}
`
	return hostConfig{
		Domains: []string{r.Hostname}, Scheme: r.ForwardScheme, Host: r.ForwardHost,
		Port: r.ForwardPort, Certificate: r.CertificateID, SSLForced: true,
		Websocket: true, HTTP2: true, Advanced: advanced, Locations: []any{},
		Meta: map[string]any{"ky.managed-by": "kyquickstart", "ky.target": e.Target},
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
		if e.Provider == "frp" && listener.MatchString(h.Advanced) && (h.Meta["ky.managed-by"] != "kyquickstart" || h.Meta["ky.target"] != e.Target) {
			return nil, fmt.Errorf("frp listener port %d is already claimed by a foreign NPM host", e.ProxyPort)
		}
		for _, name := range h.Domains {
			if !overlaps(name, r.Hostname) {
				continue
			}
			if h.ID < 1 || len(h.Domains) != 1 || name != r.Hostname || h.Meta["ky.managed-by"] != "kyquickstart" || h.Meta["ky.target"] != e.Target || !strings.HasPrefix(h.Advanced, marker(e, r)) {
				return nil, fmt.Errorf("hostname %s is claimed by an unmanaged or different edge host; refusing adoption", r.Hostname)
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

func (s hostStep) ID() string {
	sum := sha256.Sum256([]byte(s.r.Hostname))
	return s.e.Target + ".edge." + hex.EncodeToString(sum[:8])
}
func (s hostStep) InputHash() string {
	b, _ := json.Marshal(struct {
		API    string
		Config hostConfig
	}{s.n.base, config(s.e, s.r)})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (s hostStep) Inspect(ctx context.Context) (bool, error) {
	h, err := s.n.find(ctx, s.e, s.r)
	return h != nil && matches(h, config(s.e, s.r)) && h.Enabled && h.Meta["nginx_online"] == true, err
}
func (s hostStep) Apply(ctx context.Context) error {
	h, err := s.n.find(ctx, s.e, s.r)
	if err != nil {
		return err
	}
	want := config(s.e, s.r) // disabled until its API representation is verified
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

// Steps requires an Edge already validated by stack.Parse.
func (n *NPM) Steps(e stack.Edge) []engine.Step {
	var out []engine.Step
	for _, r := range e.Routes {
		out = append(out, hostStep{n: n, e: e, r: r})
	}
	return out
}

// Preflight is read-only and checks ownership of every intended route before mutations.
func (n *NPM) Preflight(ctx context.Context, e stack.Edge) error {
	for _, r := range e.Routes {
		if _, err := n.find(ctx, e, r); err != nil {
			return err
		}
		var cert struct {
			Domains []string `json:"domain_names"`
			Expires string   `json:"expires_on"`
		}
		if err := n.request(ctx, http.MethodGet, fmt.Sprintf("/api/nginx/certificates/%d", r.CertificateID), nil, &cert); err != nil {
			return err
		}
		covers := false
		for _, name := range cert.Domains {
			if name == r.Hostname || strings.HasPrefix(name, "*.") && strings.Join(strings.Split(r.Hostname, ".")[1:], ".") == name[2:] {
				covers = true
			}
		}
		expires, err := time.Parse(time.RFC3339, cert.Expires)
		if err != nil {
			// NPM's SQLite-backed API emits this UTC SQL timestamp format.
			expires, err = time.Parse("2006-01-02 15:04:05", cert.Expires)
		}
		if !covers || err != nil || !expires.After(time.Now()) {
			return fmt.Errorf("certificate %d must be unexpired and cover %s", r.CertificateID, r.Hostname)
		}
	}
	return nil
}
