package stack

import (
	"strings"
	"testing"
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
