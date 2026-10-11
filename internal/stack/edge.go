package stack

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Edge references infrastructure and target-held credentials, never credential values.
type Edge struct {
	Target        string     `yaml:"target"`
	Provider      string     `yaml:"provider"` // cloudflare or frp
	NPMURL        string     `yaml:"npm_url"`
	ConnectorCIDR string     `yaml:"connector_cidr"`
	ProxyPort     int        `yaml:"proxy_port,omitempty"` // separate PROXY-protocol TLS listener for frp
	Routes        []Route    `yaml:"routes"`
	Connector     *Connector `yaml:"connector,omitempty"`
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
	CertificateID int    `yaml:"certificate_id"`
	StatusPath    string `yaml:"status_path,omitempty"`
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
		if e.Provider != "cloudflare" && e.Provider != "frp" {
			return fmt.Errorf("edge %s: provider must be cloudflare or frp", e.Target)
		}
		u, err := url.Parse(e.NPMURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Opaque != "" {
			return fmt.Errorf("edge %s: npm_url must be an HTTPS origin with no credentials, path or query", e.Target)
		}
		if apis[e.NPMURL] {
			return fmt.Errorf("edge %s: npm_url is already owned by another target", e.Target)
		}
		apis[e.NPMURL] = true
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
			target, _ := s.Target(e.Target)
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
		if len(e.Routes) == 0 {
			return fmt.Errorf("edge %s: no routes", e.Target)
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
			adminPort := u.Port()
			if adminPort == "" {
				adminPort = "443"
			}
			if strings.EqualFold(r.ForwardHost, u.Hostname()) && strconv.Itoa(r.ForwardPort) == adminPort {
				return fmt.Errorf("edge %s: a route cannot forward to its NPM administration endpoint", e.Target)
			}
			if r.ForwardScheme == "" {
				r.ForwardScheme = "http"
			}
			if r.ForwardScheme != "http" && r.ForwardScheme != "https" {
				return fmt.Errorf("edge %s: forward_scheme must be http or https", e.Target)
			}
			if r.CertificateID < 1 {
				return fmt.Errorf("edge %s: certificate_id must reference an existing NPM certificate", e.Target)
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

var tunnelRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
