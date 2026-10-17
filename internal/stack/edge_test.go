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
