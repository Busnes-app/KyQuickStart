package stack

import (
	"errors"
	"fmt"
	"net/mail"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Edge references infrastructure and target-held credentials, never credential values.
type Edge struct {
	Target        string     `yaml:"target"`
	Provider      string     `yaml:"provider"` // cloudflare, frp or lan
	NPMURL        string     `yaml:"npm_url"`
	ConnectorCIDR string     `yaml:"connector_cidr,omitempty"`
	ProxyPort     int        `yaml:"proxy_port,omitempty"` // separate PROXY-protocol TLS listener for frp
	Routes        []Route    `yaml:"routes"`
	Connector     *Connector `yaml:"connector,omitempty"`
	// lan: clients reach NPM directly; only these prefixes are served.
	AllowCIDRs []string `yaml:"allow_cidrs,omitempty"`
	ClientPort int      `yaml:"client_port,omitempty"` // lan: the port clients reach NPM's 443 on
	// Certificates are requested by NPM from Let's Encrypt through a DNS-01 challenge.
	Certificates []Certificate `yaml:"certificates,omitempty"`
	LANDNS       *LANDNS       `yaml:"lan_dns,omitempty"`
}

// LANDNS publishes each route's hostname on the LAN as an A record for Address.
type LANDNS struct {
	Provider    string    `yaml:"provider"` // kydns
	URL         string    `yaml:"url"`      // admin API origin, reached like an https:// npm_url
	Credentials SecretRef `yaml:"credentials"`
	Address     string    `yaml:"address"`
}

// SecretRef names a key of an operator-created Secret in namespace kyq-edge.
type SecretRef struct {
	Secret string `yaml:"secret"`
	Key    string `yaml:"key"`
}

type Certificate struct {
	Name        string    `yaml:"name"`
	Domains     []string  `yaml:"domains"`
	Email       string    `yaml:"email"` // Let's Encrypt account contact
	DNSProvider string    `yaml:"dns_provider"`
	Credentials SecretRef `yaml:"credentials"`
}

// Connector attaches to an existing Docker network shared with NPM. The server or
// Cloudflare tunnel already exists; its credential remains on the target.
type Connector struct {
	Network   string `yaml:"network"`
	NPMHost   string `yaml:"npm_host"`
	FRPServer string `yaml:"frp_server,omitempty"`
	FRPPort   int    `yaml:"frp_port,omitempty"`
	TunnelID  string `yaml:"tunnel_id,omitempty"`
}

type Route struct {
	App           string `yaml:"app"`
	Hostname      string `yaml:"hostname"`
	ForwardHost   string `yaml:"forward_host"`
	ForwardPort   int    `yaml:"forward_port"`
	ForwardScheme string `yaml:"forward_scheme"`
	CertificateID int    `yaml:"certificate_id,omitempty"`
	Certificate   string `yaml:"certificate,omitempty"` // an edge certificate's name, instead of an ID
	StatusPath    string `yaml:"status_path,omitempty"`
}

// URLHost is the host an app's route is reached at: its hostname, plus the port when clients
// reach the edge on another than 443 (a lan edge's client_port).
func (e Edge) URLHost(r Route) string {
	if e.ClientPort != 0 && e.ClientPort != 443 {
		return r.Hostname + ":" + strconv.Itoa(e.ClientPort)
	}
	return r.Hostname
}

// Covers reports whether certificate domain covers host: equal, or a wildcard for its parent.
func Covers(domain, host string) bool {
	_, parent, _ := strings.Cut(host, ".")
	return domain == host || domain == "*."+parent
}

// Hostname accepts a concrete DNS name, not wildcards, URLs or nginx directives.
func Hostname(s string) bool {
	if s != strings.ToLower(s) || len(s) > 253 || !strings.Contains(s, ".") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func (s *Stack) validateEdges() error {
	targets, hosts, apis := map[string]bool{}, map[string]bool{}, map[string]bool{}
	apps := map[string]string{}
	for _, a := range s.Apps {
		apps[a.Name] = a.Target
	}
	for i := range s.Edges {
		e := &s.Edges[i]
		if _, ok := s.Target(e.Target); !ok || targets[e.Target] {
			return fmt.Errorf("edge target %q must exist and have only one edge", e.Target)
		}
		targets[e.Target] = true
		if e.Provider != "cloudflare" && e.Provider != "frp" && e.Provider != "lan" {
			return fmt.Errorf("edge %s: provider must be cloudflare, frp or lan", e.Target)
		}
		target, _ := s.Target(e.Target)
		// adminHost and adminPort name the NPM API, which no route may forward to.
		var adminHost, adminPort string
		if ns, svc, port, ok := KubeService(e.NPMURL); ok {
			if !target.Kubernetes() {
				return fmt.Errorf("edge %s: a kube:// npm_url needs a Kubernetes target", e.Target)
			}
			adminHost, adminPort = svc+"."+ns, strconv.Itoa(port)
		} else {
			u, err := url.Parse(e.NPMURL)
			if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Opaque != "" {
				return fmt.Errorf("edge %s: npm_url must be an HTTPS origin with no credentials, path or query, or kube://<namespace>/<service>:<port>", e.Target)
			}
			adminHost, adminPort = strings.ToLower(u.Hostname()), u.Port()
			if adminPort == "" {
				adminPort = "443"
			}
		}
		if apis[e.NPMURL] {
			return fmt.Errorf("edge %s: npm_url is already owned by another target", e.Target)
		}
		apis[e.NPMURL] = true
		var err error
		if e.Provider == "lan" {
			err = validateLAN(e)
		} else {
			err = validateConnector(e, target)
		}
		if err != nil {
			return err
		}
		if len(e.Routes) == 0 {
			return fmt.Errorf("edge %s: no routes", e.Target)
		}
		certs, err := validateCertificates(*e, target)
		if err != nil {
			return err
		}
		if err := validateLANDNS(*e, target); err != nil {
			return err
		}
		for j := range e.Routes {
			r := &e.Routes[j]
			if apps[r.App] != e.Target {
				return fmt.Errorf("edge %s: route app %q must be installed on this target", e.Target, r.App)
			}
			if !Hostname(r.Hostname) || hosts[r.Hostname] {
				return fmt.Errorf("edge %s: hostname %q must be a unique lowercase DNS name", e.Target, r.Hostname)
			}
			hosts[r.Hostname] = true
			if !hostRE.MatchString(r.ForwardHost) || strings.Contains(r.ForwardHost, "..") {
				return fmt.Errorf("edge %s: invalid forward_host", e.Target)
			}
			if r.ForwardPort < 1 || r.ForwardPort > 65535 || r.ForwardPort == 81 {
				return fmt.Errorf("edge %s: forward_port must be 1-65535 and cannot be NPM admin port 81", e.Target)
			}
			host := strings.ToLower(r.ForwardHost)
			if (host == adminHost || strings.HasPrefix(host, adminHost+".svc")) && strconv.Itoa(r.ForwardPort) == adminPort {
				return fmt.Errorf("edge %s: a route cannot forward to its NPM administration endpoint", e.Target)
			}
			if r.ForwardScheme == "" {
				r.ForwardScheme = "http"
			}
			if r.ForwardScheme != "http" && r.ForwardScheme != "https" {
				return fmt.Errorf("edge %s: forward_scheme must be http or https", e.Target)
			}
			if (r.CertificateID == 0) == (r.Certificate == "") || r.CertificateID < 0 {
				return fmt.Errorf("edge %s: route %s needs exactly one of certificate_id (an existing NPM certificate) or certificate (one of the edge's certificates)", e.Target, r.Hostname)
			}
			if c, ok := certs[r.Certificate]; r.Certificate != "" && (!ok || !slices.ContainsFunc(c.Domains, func(d string) bool { return Covers(d, r.Hostname) })) {
				return fmt.Errorf("edge %s: route %s: certificate %q must be one of the edge's certificates and cover the hostname", e.Target, r.Hostname, r.Certificate)
			}
			if r.StatusPath == "" {
				r.StatusPath = "/api/status"
			}
			p, err := url.ParseRequestURI(r.StatusPath)
			if err != nil || !strings.HasPrefix(r.StatusPath, "/") || strings.HasPrefix(r.StatusPath, "//") || p.RawQuery != "" || p.Fragment != "" || strings.ContainsAny(r.StatusPath, "\x00\r\n") {
				return fmt.Errorf("edge %s: status_path must be an absolute request path without a query or fragment", e.Target)
			}
		}
	}
	return nil
}

// validateConnector checks a tunnel edge: one trusted connector address.
func validateConnector(e *Edge, target Target) error {
	if len(e.AllowCIDRs) > 0 || e.ClientPort != 0 {
		return fmt.Errorf("edge %s: allow_cidrs and client_port are only for lan", e.Target)
	}
	p, err := netip.ParsePrefix(e.ConnectorCIDR)
	if err != nil || !p.Addr().Is4() || p.Bits() != 32 || p.Addr().IsUnspecified() || p.Addr().IsMulticast() {
		return fmt.Errorf("edge %s: connector_cidr must pin one IPv4 address (/32)", e.Target)
	}
	if e.Provider == "frp" {
		if e.ProxyPort == 0 {
			e.ProxyPort = 8443
		}
		if e.ProxyPort < 1024 || e.ProxyPort > 65535 {
			return fmt.Errorf("edge %s: proxy_port must be a dedicated unprivileged port (1024-65535)", e.Target)
		}
	} else if e.ProxyPort != 0 {
		return fmt.Errorf("edge %s: proxy_port is only for frp", e.Target)
	}
	if c := e.Connector; c != nil {
		if target.Kubernetes() {
			return fmt.Errorf("edge %s: connector deployment currently requires a Docker target; Kubernetes source-address isolation is not implemented", e.Target)
		}
		if !contextRE.MatchString(c.Network) || strings.ContainsAny(c.Network, "@:/") || !hostRE.MatchString(c.NPMHost) {
			return fmt.Errorf("edge %s: connector needs a valid Docker network and NPM hostname", e.Target)
		}
		if e.Provider == "frp" {
			if c.FRPPort == 0 {
				c.FRPPort = 7000
			}
			if !hostRE.MatchString(c.FRPServer) || c.FRPPort < 1 || c.FRPPort > 65535 || c.TunnelID != "" {
				return fmt.Errorf("edge %s: frp connector needs frp_server and frp_port, with no tunnel_id", e.Target)
			}
		} else {
			if !tunnelRE.MatchString(c.TunnelID) || c.FRPServer != "" || c.FRPPort != 0 {
				return fmt.Errorf("edge %s: cloudflare connector needs a tunnel_id UUID, with no frp fields", e.Target)
			}
		}
	}
	return nil
}

// validateLAN checks an edge LAN clients reach directly: NPM trusts no forwarding header.
func validateLAN(e *Edge) error {
	if e.ConnectorCIDR != "" || e.Connector != nil || e.ProxyPort != 0 {
		return fmt.Errorf("edge %s: lan takes no connector_cidr, connector or proxy_port; clients reach NPM directly", e.Target)
	}
	if len(e.AllowCIDRs) == 0 || len(e.AllowCIDRs) > 32 {
		return fmt.Errorf("edge %s: lan needs allow_cidrs, 1-32 client prefixes", e.Target)
	}
	seen := map[string]bool{}
	for _, c := range e.AllowCIDRs {
		if !AllowCIDR(c) || seen[c] {
			return fmt.Errorf("edge %s: allow_cidrs entry %q must be a unique canonical prefix, at least /8 for IPv4 or /32 for IPv6", e.Target, c)
		}
		seen[c] = true
	}
	if e.ClientPort == 0 {
		e.ClientPort = 443
	}
	if e.ClientPort < 1 || e.ClientPort > 65535 {
		return fmt.Errorf("edge %s: client_port must be 1-65535", e.Target)
	}
	return nil
}

// AllowCIDR accepts a canonical network prefix no wider than /8 (IPv4) or /32 (IPv6).
func AllowCIDR(s string) bool {
	p, err := netip.ParsePrefix(s)
	min := 8
	if p.Addr().Is6() {
		min = 32
	}
	return err == nil && p.String() == s && p == p.Masked() && !p.Addr().Is4In6() && p.Bits() >= min
}

// validateCertificates returns e's certificates by name.
func validateCertificates(e Edge, target Target) (map[string]Certificate, error) {
	certs := map[string]Certificate{}
	if len(e.Certificates) > 0 && !target.Kubernetes() {
		return nil, fmt.Errorf("edge %s: certificates read their DNS credential from a Secret in kyq-edge, so they need a Kubernetes target for now", e.Target)
	}
	for _, c := range e.Certificates {
		if len(validation.IsDNS1123Label(c.Name)) > 0 || certs[c.Name].Name != "" {
			return nil, fmt.Errorf("edge %s: certificate name %q must be a unique DNS label", e.Target, c.Name)
		}
		certs[c.Name] = c
		seen := map[string]bool{}
		for _, d := range c.Domains {
			// A wildcard only as the whole leftmost label, as Let's Encrypt allows.
			if !Hostname(strings.TrimPrefix(d, "*.")) || seen[d] {
				return nil, fmt.Errorf("edge %s: certificate %s: domain %q must be a unique lowercase DNS name, a wildcard only as the leftmost label", e.Target, c.Name, d)
			}
			seen[d] = true
		}
		if len(c.Domains) == 0 || len(c.Domains) > 100 {
			return nil, fmt.Errorf("edge %s: certificate %s needs 1-100 domains", e.Target, c.Name)
		}
		a, err := mail.ParseAddress(c.Email)
		_, domain, _ := strings.Cut(c.Email, "@")
		if err != nil || a.Address != c.Email || a.Name != "" || len(c.Email) > 254 || !Hostname(strings.ToLower(domain)) {
			return nil, fmt.Errorf("edge %s: certificate %s: email must be a plain address for the Let's Encrypt account", e.Target, c.Name)
		}
		// NPM supports many DNS providers; each one added here needs its credential format.
		if c.DNSProvider != "cloudflare" {
			return nil, fmt.Errorf("edge %s: certificate %s: dns_provider must be cloudflare", e.Target, c.Name)
		}
		if err := c.Credentials.validate(); err != nil {
			return nil, fmt.Errorf("edge %s: certificate %s: %w", e.Target, c.Name, err)
		}
	}
	return certs, nil
}

func validateLANDNS(e Edge, target Target) error {
	d := e.LANDNS
	if d == nil {
		return nil
	}
	if !target.Kubernetes() {
		return fmt.Errorf("edge %s: lan_dns reads its token from a Secret in kyq-edge, so it needs a Kubernetes target for now", e.Target)
	}
	if d.Provider != "kydns" {
		return fmt.Errorf("edge %s: lan_dns provider must be kydns", e.Target)
	}
	u, err := url.Parse(d.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Path != "" && u.Path != "/" {
		return fmt.Errorf("edge %s: lan_dns url must be an HTTPS origin with no credentials, path or query", e.Target)
	}
	a, err := netip.ParseAddr(d.Address)
	if err != nil || !a.Is4() || a.IsUnspecified() || a.IsMulticast() || a.IsLoopback() {
		return fmt.Errorf("edge %s: lan_dns address must be the IPv4 address LAN clients reach the edge on", e.Target)
	}
	if err := d.Credentials.validate(); err != nil {
		return fmt.Errorf("edge %s: lan_dns: %w", e.Target, err)
	}
	return nil
}

// validate accepts an operator Secret in kyq-edge, never the installer's own kyq-credentials.
func (r SecretRef) validate() error {
	if len(validation.IsDNS1123Subdomain(r.Secret)) > 0 || r.Secret == "kyq-credentials" || len(validation.IsConfigMapKey(r.Key)) > 0 {
		return errors.New("credentials need a Secret name (not kyq-credentials) and a key")
	}
	return nil
}

var (
	tunnelRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	kubeRE   = regexp.MustCompile(`^kube://([^/]+)/([^:]+):([1-9][0-9]{0,4})$`)
)

// KubeService parses npm_url kube://<namespace>/<service>:<port>: an NPM Service reached
// through a port-forward the Kubernetes driver opens.
func KubeService(s string) (namespace, service string, port int, ok bool) {
	m := kubeRE.FindStringSubmatch(s)
	if m == nil || len(validation.IsDNS1123Label(m[1])) > 0 || len(validation.IsDNS1035Label(m[2])) > 0 {
		return "", "", 0, false
	}
	port, _ = strconv.Atoi(m[3])
	return m[1], m[2], port, port <= 65535
}
