# Edge support

This build adds edge configuration to `stack.yaml`, reconciles existing Nginx Proxy Manager
(NPM) hosts through its API, and optionally deploys a Docker connector for an existing
Cloudflare tunnel or frp server. It is one part of the installer, not a complete suite release:
the embedded production app catalog is still empty and product setup interfaces remain gated.

## Supported paths

| Connection | Current support |
| --- | --- |
| Existing NPM | Reuse existing certificates; create and repair installer-owned proxy hosts only |
| Existing Cloudflare tunnel | Locally managed tunnel credentials; optional cloudflared deployment on Docker |
| Existing frp server | Optional frpc deployment on Docker, verified server TLS and PROXY v2 |
| Kubernetes target | Existing NPM API reconciliation; connector deployment is rejected |

Fresh NPM bootstrap, DNS-01 certificate provisioning, creating Cloudflare tunnels and DNS
records, provisioning an frps server, remotely managed Cloudflare token tunnels, and Kubernetes
connector isolation remain to be implemented. NPS is not implemented.

## Configuration

Add this `edges` section alongside targets and selected apps. Replace `your-app` with an
actual catalog app installed on target `nas`; it is an illustrative name, not a shipped app.
Connection choices belong in this runtime configuration, not in installer development inputs.

```yaml
edges:
  - target: nas
    provider: frp
    npm_url: https://npm-admin.lan.example
    connector_cidr: 172.30.0.10/32
    proxy_port: 8443
    connector:
      network: existing-edge-network
      npm_host: npm.lan.example
      frp_server: relay.example.com
      frp_port: 7000
    routes:
      - app: your-app
        hostname: app.example.com
        forward_host: your-app
        forward_port: 8080
        forward_scheme: http
        certificate_id: 12
        status_path: /api/status
```

There is one edge per target and one owner per hostname. The connector address must be a
free fixed IPv4 address on the existing Docker network. NPM and the connector must be able
to reach each other there; the NPM hostname must resolve inside the container and its
certificate must cover each routed hostname. The management URL must resolve to private or
loopback addresses and present a certificate trusted by the workstation. There is no TLS bypass.
NPM administration stays on the LAN. Routes cannot forward to port 81 or their management URL.

Omit `connector` when a connector is already managed outside the installer. Its actual source
address must match `connector_cidr`. External DNS and server-side frp settings must already
route the listed hostnames. frp reaches the dedicated NPM PROXY-protocol listener (default
8443), not its ordinary 443 listener. Keep that listener private to the connector network.

For Cloudflare set `provider: cloudflare`, omit `proxy_port` and replace the frp connector
fields with `tunnel_id: <existing-tunnel-UUID>`. The generated ingress configuration routes
only the listed hostnames, verifies origin TLS using each hostname and ends with a 404 rule.
Use dedicated tunnel credentials: an existing tunnel may have connectors with different
ingress configuration, and this build does not reconcile those other connectors.

## Credentials

On a Docker target, place existing credentials beneath its configured root:

- `edge/secrets/npm_token`: NPM API bearer token.
- `edge/secrets/frp_token`: frp authentication token, without a trailing newline.
- `edge/secrets/frp_ca`: trusted PEM public certificates for the frp server.
- `edge/secrets/cloudflare_tunnel_credentials`: Cloudflare JSON containing `AccountTag`,
  `TunnelSecret` and the matching `TunnelID`.

Files must be regular, owned by the SSH user and mode 0600, beneath trusted directories.
The installer makes protected runtime copies on the target. Credential changes invalidate
the connector deployment and recreate the container. Credentials do not enter workstation
state, command arguments or configuration hashes. Keep their original source files for resume.

For an existing Kubernetes edge, the NPM token is read from key `npm_token` in Secret
`kyq-edge/kyq-credentials`. Both namespace and Secret must already carry
`ky.managed-by=kyquickstart`; the installer will not adopt foreign objects.

## Applying and verifying

```sh
kyquickstart preflight --state ./installation
kyquickstart apply --state ./installation
kyquickstart verify-edge --state ./installation --expected-client-ip <workstation-public-IP>
```

Preflight checks ownership, hostname/listener conflicts, certificate coverage and expiry,
and optional connector network/credentials without deploying. Apply uses the normal target
lock and resumable steps. Existing foreign hosts and wildcard conflicts are refused; an
unchanged second apply skips completed work.

The product must already trust its NPM peer according to the suite proxy contract. Product
configuration through `apply-setup` is still pending. Its status endpoint must return:

```json
{"clientIp":"203.0.113.10","proxyHeadersTrusted":true,"scheme":"https"}
```

These values must describe the current request after the product's own proxy handling.
`verify-edge` sends forged forwarding headers through public HTTPS and requires the expected
client address and HTTPS scheme. A missing G11 implementation fails verification. The check
does not grant administrator access, verify SSO, or certify the complete installation.

## Test scope

`make e2e` uses disposable local Docker resources. It proves the real NPM API accepts generated
configuration, real frps and generated frpc preserve client IP through two TLS hostnames,
unknown SNI is rejected, forwarding headers are
sanitized for frp and simulated Cloudflare traffic, and the second apply is a no-op.
Separate connector tests prove authenticated frpc login with verified TLS and parse generated
cloudflared ingress with the pinned image. No live Cloudflare account or operator server is
used. A complete public frp/Cloudflare-to-product G11 pilot is still required.
