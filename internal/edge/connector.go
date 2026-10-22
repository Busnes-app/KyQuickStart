package edge

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path"
	"strconv"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

const frpcImage = "ghcr.io/fatedier/frpc:v0.71.0@sha256:8dd029fa1f995629d6f31157f270633224f39492da079b099ce39dddaad3e191"
const cloudflaredImage = "cloudflare/cloudflared:2026.10.0@sha256:1a50cc8893b3f0dd283805d2efcd3336210aced60a83d967fb2bbc8ab69b5f35"

func connectorName(target string) string {
	sum := sha256.Sum256([]byte(target))
	return "edge-" + hex.EncodeToString(sum[:8])
}

func credentialName(provider string) string {
	if provider == "frp" {
		return "frp_token"
	}
	return "cloudflare_tunnel_credentials"
}

// connectorConfig has no secret; the programs read the protected file at runtime. A direct
// edge routes each hostname to its Origin; the others route to NPM.
func connectorConfig(e stack.Edge) ([]byte, string, []string) {
	c := e.Connector
	if e.Provider == "frp" {
		hosts := make([]string, 0, len(e.Routes))
		for _, route := range e.Routes {
			hosts = append(hosts, strconv.Quote(route.Hostname))
		}
		text := fmt.Sprintf(`serverAddr = %s
serverPort = %d
loginFailExit = true
auth.method = "token"
auth.tokenSource.type = "file"
auth.tokenSource.file.path = "/run/secrets/credential"
transport.tls.enable = true
transport.tls.serverName = %s
transport.tls.trustedCaFile = "/etc/kyq/frp-ca.pem"
[[proxies]]
name = %s
type = "https"
localIP = %s
localPort = %d
customDomains = [%s]
transport.proxyProtocolVersion = "v2"
`, strconv.Quote(c.FRPServer), c.FRPPort, strconv.Quote(c.FRPServer), strconv.Quote(connectorName(e.Target)), strconv.Quote(c.NPMHost), e.ProxyPort, strings.Join(hosts, ", "))
		return []byte(text), frpcImage, []string{"-c", "/etc/kyq/connector.conf"}
	}
	var ingress []map[string]any
	for _, route := range e.Routes {
		if e.Direct() {
			// Plain HTTP on the dedicated edge network, which never leaves the host.
			ingress = append(ingress, map[string]any{"hostname": route.Hostname, "service": route.Origin})
			continue
		}
		ingress = append(ingress, map[string]any{
			"hostname": route.Hostname, "service": "https://" + c.NPMHost + ":443",
			"originRequest": map[string]any{"originServerName": route.Hostname, "httpHostHeader": route.Hostname},
		})
	}
	ingress = append(ingress, map[string]any{"service": "http_status:404"})
	b, err := yaml.Marshal(map[string]any{"tunnel": c.TunnelID, "credentials-file": "/run/secrets/credential", "ingress": ingress})
	if err != nil {
		panic(err)
	}
	return b, cloudflaredImage, []string{"tunnel", "--no-autoupdate", "--config", "/etc/kyq/connector.conf", "run"}
}

func connectorCredential(ctx context.Context, r remote.Runner, root string, e stack.Edge) ([]byte, error) {
	b, err := remote.ReadSecret(ctx, r, path.Join(root, "edge", "secrets", credentialName(e.Provider)))
	if err != nil {
		return nil, err
	}
	if e.Provider != "frp" {
		var credentials struct {
			Account string `json:"AccountTag"`
			Secret  string `json:"TunnelSecret"`
			ID      string `json:"TunnelID"`
		}
		if json.Unmarshal(b, &credentials) != nil || credentials.Account == "" || credentials.Secret == "" || credentials.ID != e.Connector.TunnelID {
			return nil, errors.New("Cloudflare credential must contain AccountTag, TunnelSecret and the planned TunnelID")
		}
	} else if strings.ContainsAny(string(b), "\x00\r\n") {
		return nil, errors.New("frp credential must contain one token with no newline")
	}
	return b, nil
}

// CheckConnector reads infrastructure and credentials, with no deployment side effects.
func CheckConnector(ctx context.Context, r remote.Runner, root string, e stack.Edge) error {
	if _, err := connectorCredential(ctx, r, root, e); err != nil {
		return err
	}
	if e.Provider == "frp" {
		if _, err := frpCA(ctx, r, root); err != nil {
			return err
		}
	}
	b, err := r.Run(ctx, "docker network inspect "+remote.Quote(e.Connector.Network), nil)
	if err != nil {
		return errors.New("connector's existing Docker network is unavailable")
	}
	var networks []struct {
		EnableIPv6 bool
		IPAM       struct {
			Config []struct{ Subnet, IPRange string }
		}
		Containers map[string]struct {
			Name    string
			Address string `json:"IPv4Address"`
		}
	}
	if json.Unmarshal(b, &networks) != nil || len(networks) != 1 {
		return errors.New("invalid Docker network response")
	}
	// With IPv6, the apps could see cloudflared from an address they do not trust.
	if e.Direct() && networks[0].EnableIPv6 {
		return errors.New("a cloudflare-direct network must not enable IPv6: its apps trust cloudflared's IPv4 address alone")
	}
	ip := netip.MustParsePrefix(e.ConnectorCIDR).Addr()
	// NPM trusts this address, so Docker must never hand it to another container:
	// the network needs an explicit dynamic --ip-range that excludes it.
	reserved := false
	for _, c := range networks[0].IPAM.Config {
		subnet, err := netip.ParsePrefix(c.Subnet)
		if err != nil || !subnet.Contains(ip) {
			continue
		}
		dynamic, err := netip.ParsePrefix(c.IPRange)
		if err != nil {
			return errors.New("connector network needs an explicit --subnet and --ip-range that excludes connector_cidr")
		}
		reserved = !dynamic.Contains(ip)
	}
	if !reserved {
		return errors.New("connector_cidr must be inside the Docker network's subnet and outside its --ip-range")
	}
	for id, c := range networks[0].Containers {
		p, err := netip.ParsePrefix(c.Address)
		at := err == nil && p.Addr() == ip
		// A direct edge's apps are reached by alias on this network, so it holds only ours.
		if !at && !e.Direct() {
			continue
		}
		b, err := r.Run(ctx, "docker inspect --format '{{json .Config.Labels}}' "+remote.Quote(id), nil)
		var labels map[string]string
		ours := err == nil && json.Unmarshal(b, &labels) == nil && labels[managed.Label] == managed.Value
		if at && (!ours || labels["com.docker.compose.project"] != "kyq-"+connectorName(e.Target)) {
			return errors.New("connector_cidr is occupied by a foreign container")
		}
		if !ours {
			return fmt.Errorf("a cloudflare-direct network must hold only this installer's containers; %q is not one", c.Name)
		}
	}
	return nil
}

func frpCA(ctx context.Context, r remote.Runner, root string) ([]byte, error) {
	b, err := remote.ReadSecret(ctx, r, path.Join(root, "edge", "secrets", "frp_ca"))
	if err != nil {
		return nil, err
	}
	if !x509.NewCertPool().AppendCertsFromPEM(b) || strings.Contains(string(b), "PRIVATE KEY") {
		return nil, errors.New("frp_ca must contain trusted public PEM certificates, without private keys")
	}
	return b, nil
}

type connectorFiles struct {
	e                   stack.Edge
	r                   remote.Runner
	root, dir, uid, gid string
	config              []byte
	redact              *engine.Redactor
}

func (s connectorFiles) ID() string { return s.e.Target + ".edge-credentials" }
func (s connectorFiles) InputHash() string {
	sum := sha256.Sum256(append(append([]byte(s.uid+":"+s.gid+"\x00"), s.config...), []byte(s.e.Connector.Network)...))
	return hex.EncodeToString(sum[:])
}
func (s connectorFiles) Inspect(ctx context.Context) (bool, error) {
	if _, err := connectorCredential(ctx, s.r, s.root, s.e); err != nil {
		return false, err
	}
	if s.e.Provider == "frp" {
		if _, err := frpCA(ctx, s.r, s.root); err != nil {
			return false, err
		}
		cmd := "cmp -s " + remote.Quote(path.Join(s.root, "edge", "secrets", "frp_ca")) + " " + remote.Quote(path.Join(s.dir, "frp-ca.pem"))
		if _, err := s.r.Run(ctx, cmd, nil); err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, nil
		}
	}
	credential := remote.Quote(path.Join(s.dir, "credential"))
	cmd := `test -n "$(find ` + credential + ` -prune -type f -user ` + s.uid + ` -perm 600)" && cmp -s ` + remote.Quote(path.Join(s.root, "edge", "secrets", credentialName(s.e.Provider))) + " " + credential + " && cat " + remote.Quote(path.Join(s.dir, "connector.conf"))
	b, err := s.r.Run(ctx, cmd, nil)
	// Missing/changed target files are an apply, not a disclosure of remote stderr.
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	return string(b) == string(s.config), nil
}
func (s connectorFiles) Apply(ctx context.Context) error {
	b, err := connectorCredential(ctx, s.r, s.root, s.e)
	if err != nil {
		return err
	}
	s.redact.Add(string(b))
	if s.e.Provider == "frp" {
		ca, err := frpCA(ctx, s.r, s.root)
		if err != nil {
			return err
		}
		if err := dockerhost.WriteConfig(ctx, s.r, path.Join(s.dir, "frp-ca.pem"), ca); err != nil {
			return err
		}
		if _, err := s.r.Run(ctx, "chmod 644 "+remote.Quote(path.Join(s.dir, "frp-ca.pem")), nil); err != nil {
			return errors.New("could not set connector CA permissions")
		}
	}
	if err := dockerhost.WriteConfig(ctx, s.r, path.Join(s.dir, "connector.conf"), s.config); err != nil {
		return err
	}
	if err := dockerhost.WriteConfig(ctx, s.r, path.Join(s.dir, "credential"), b); err != nil {
		return err
	}
	cmd := "chmod 644 " + remote.Quote(path.Join(s.dir, "connector.conf")) + " && chown " + remote.Quote(s.uid+":"+s.gid) + " " + remote.Quote(path.Join(s.dir, "credential")) + " && rm -f " + remote.Quote(path.Join(s.dir, ".input-hash"))
	if _, err := s.r.Run(ctx, cmd, nil); err != nil {
		return errors.New("could not set connector file ownership or invalidate its deployment")
	}
	return nil
}
func (s connectorFiles) Verify(ctx context.Context) error {
	ok, err := s.Inspect(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("connector configuration or credential differs from target input")
	}
	return nil
}

// DockerConnector reuses the host driver and its managed labels, deployment and health
// steps. Supplied credentials are copied on the target; nothing generates a new token.
func DockerConnector(ctx context.Context, r remote.Runner, root, release string, e stack.Edge, redact *engine.Redactor) ([]engine.Step, error) {
	b, err := r.Run(ctx, "id -u; id -g", nil)
	if err != nil {
		return nil, errors.New("could not determine target connector user")
	}
	ids := strings.Fields(string(b))
	if len(ids) != 2 {
		return nil, errors.New("invalid target user/group response")
	}
	for _, id := range ids {
		if _, err := strconv.ParseUint(id, 10, 31); err != nil {
			return nil, errors.New("invalid target uid/gid")
		}
	}
	if ids[0] == "0" {
		ids = []string{"65532", "65532"}
	}
	conf, image, args := connectorConfig(e)
	name := connectorName(e.Target)
	volumes := []string{"./connector.conf:/etc/kyq/connector.conf:ro", "./credential:/run/secrets/credential:ro"}
	if e.Provider == "frp" {
		volumes = append(volumes, "./frp-ca.pem:/etc/kyq/frp-ca.pem:ro")
	}
	compose, err := yaml.Marshal(map[string]any{
		"services": map[string]any{"connector": map[string]any{
			"image": image, "command": args, "user": ids[0] + ":" + ids[1], "read_only": true,
			"cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges:true"}, "restart": "unless-stopped",
			"volumes":  volumes,
			"networks": map[string]any{"edge": map[string]any{"ipv4_address": strings.TrimSuffix(e.ConnectorCIDR, "/32")}},
		}},
		"networks": map[string]any{"edge": map[string]any{"external": true, "name": e.Connector.Network}},
	})
	if err != nil {
		return nil, err
	}
	a := catalog.App{Manifest: catalog.Manifest{Name: name, Category: "edge", Images: map[string]string{"connector": image}, Health: catalog.Health{TimeoutSeconds: 60}}, Compose: compose, Services: []string{"connector"}}
	files := connectorFiles{e: e, r: r, root: root, dir: path.Join(root, name), uid: ids[0], gid: ids[1], config: conf, redact: redact}
	steps := []engine.Step{files}
	return append(steps, dockerhost.Steps(dockerhost.App{Name: name, Root: root, Runner: r, Catalog: a, ReleaseSet: release, Redact: redact, ForceRecreate: true})...), nil
}
