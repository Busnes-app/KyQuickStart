# Edge support

This build adds edge configuration to `stack.yaml`, reconciles Nginx Proxy Manager (NPM) hosts
through its API, deploys and bootstraps NPM itself on a Kubernetes target, and optionally deploys
a Docker connector for an existing Cloudflare tunnel or frp server. It is one part of the
installer, not a complete suite release: NPM is the only production catalog app and product setup
interfaces remain gated.

## Supported paths

| Connection | Current support |
| --- | --- |
| Existing NPM | Reuse existing certificates; create and repair installer-owned proxy hosts only |
| Existing Cloudflare tunnel | Locally managed tunnel credentials; optional cloudflared deployment on Docker |
| Existing frp server | Optional frpc deployment on Docker, verified server TLS and PROXY v2 |
| Kubernetes target | NPM from the catalog (`npm` app) managed through `kube://`, or an existing NPM's API; connector deployment is rejected |

Fresh NPM on a Docker host, DNS-01 certificate provisioning, creating Cloudflare tunnels and DNS
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
The network must have an explicit subnet and a dynamic `--ip-range` that excludes
`connector_cidr`, so Docker never assigns NPM's trusted address to another container; for
example `docker network create --subnet 172.30.0.0/24 --ip-range 172.30.0.128/25 npm-net`.
Preflight refuses a network without one.

Omit `connector` when a connector is already managed outside the installer. Its actual source
address must match `connector_cidr`. External DNS and server-side frp settings must already
route the listed hostnames. frp reaches the dedicated NPM PROXY-protocol listener (default
8443), not its ordinary 443 listener. Keep that listener private to the connector network.

For Cloudflare set `provider: cloudflare`, omit `proxy_port` and replace the frp connector
fields with `tunnel_id: <existing-tunnel-UUID>`. The generated ingress configuration routes
only the listed hostnames, verifies origin TLS using each hostname and ends with a 404 rule.
Use dedicated tunnel credentials: an existing tunnel may have connectors with different
ingress configuration, and this build does not reconcile those other connectors.

## NPM on Kubernetes

Add the `npm` app to a Kubernetes target. NPM has no Docker deployment yet; placing it on a
Docker host is refused.

```yaml
apps:
  - name: npm
    target: k80
    node_ports: {80: 30080, 443: 30443}  # optional; these are the defaults
```

`apply` deploys it in namespace `kyq-npm`:

- Two claims of the target's `storage_class`: `vol-data` (1Gi, `/data`, NPM's database and
  configuration) and `vol-letsencrypt` (256Mi). Both are in the manifest's backup block.
- Service `app-public`, a NodePort for 80 and 443 (default 30080/30443; `node_ports` overrides
  them within 30000-32767). Its `externalTrafficPolicy: Local` keeps the client address, which
  each route's `connector_cidr` check depends on: only the node running NPM answers.
- Service `app`, ClusterIP, for the admin API on port 81 only. Port 81 can never be published:
  the catalog refuses it in `node_ports`, and `apply` repairs a Service edited to expose other
  ports.
- A NetworkPolicy (`default-deny-ingress`) that admits ingress only to 80 and 443, from
  anywhere, and no pod at all to 81. Egress is limited to DNS (kube-dns in `kube-system`), TCP
  80/443 anywhere (ACME and HTTPS upstreams), and any port on pods in installer-managed
  namespaces. That last rule is the simplest correct one for upstreams: each app's own policy
  decides what NPM reaches, and on a cluster with NPM every other app's policy admits only NPM's
  pods (by namespace name, which the API server sets) to its ports.

### The baseline exception

Every other app namespace enforces Pod Security `restricted`. `kyq-npm` enforces `baseline`
(the manifest's `pod_security`), because NPM's s6-overlay init must start as root: it creates
its service user, chowns `/data` and its nginx directories, and rewrites configuration in the
image. nginx and the API then run as uid 1000 (`PUID`/`PGID`); only the s6 supervisors stay
root. The container still drops every capability and adds back only what the init needs,
measured in kind by removing one at a time:

| Setting | Value | Why |
| --- | --- | --- |
| `runAsUser` | 0 | s6 init |
| `capabilities` | drop `ALL`; add `CHOWN`, `DAC_OVERRIDE`, `SETUID`, `SETGID`, `KILL` | without CHOWN, `useradd` fails; without DAC_OVERRIDE, writes to files it chowned fail; without SETUID or SETGID, the drop to uid 1000 fails; without KILL, s6 cannot stop the uid-1000 processes, so a pod stop waits the full grace period and ends in SIGKILL |
| `allowPrivilegeEscalation` | false | |
| `readOnlyRootFilesystem` | false | the init edits `/etc/passwd` and `/etc/nginx` |
| `seccompProfile` | RuntimeDefault | |
| pod sysctl | `net.ipv4.ip_unprivileged_port_start=0` | lets uid 1000 bind 80, 443 and 81 without NET_BIND_SERVICE (a safe sysctl; containerd 2 already sets it) |

No `privileged`, no host network, ports or paths, and no service-account token.

### Bootstrap

NPM 2.13 and later start with no user ("setup mode"): whoever reaches the API first creates the
administrator. The `npm.bootstrap` step, after NPM is ready, does that through a port-forward,
so no pod and no LAN client can race it. The administrator is `admin@kyquickstart.invalid`;
its password is `admin_password` in Secret `kyq-npm/kyq-secrets`, generated on the cluster like
every app secret. Data from an older NPM that still accepts the default
`admin@example.com`/`changeme` login is rotated to the installer's login instead. The step then
stores an API token as `npm_token` in Secret `kyq-edge/kyq-credentials` (both installer-owned).

Reruns skip it while the stored token verifies and the default login is refused. NPM tokens
expire after a day and are revoked by a password change; a later apply mints a fresh one, and
until then every command signs in with the stored password and keeps that token in memory only.
An NPM with another administrator, or one that rejects both logins, is refused, not reset.

For the NPM web UI, forward the admin Service yourself:
`kubectl -n kyq-npm port-forward svc/app 8181:81`, then open `http://127.0.0.1:8181`.

### `kube://` management URLs

For an NPM deployed by this installer, set `npm_url: kube://kyq-npm/app:81`. The Kubernetes
driver reaches it through the API server's `pods/portforward` (SPDY), so the hop from the
workstation is the kubeconfig's TLS connection; NPM's API is plain HTTP inside the tunnel, which
ends in the pod. No local port is opened. The private-address rule for `https://` URLs does not
apply to it, and `https://` URLs keep that rule and verified TLS. A `kube://` URL is accepted
only on a Kubernetes target, must name the `npm` app's admin Service and port, and needs `pods`
`list` and `pods/portforward` `create`, which preflight checks.

Routes need an existing NPM certificate, so add the edge after NPM is running: apply once with
the `npm` app and no edge, add a certificate in NPM, then add the edge and apply again. Inside
the cluster, forward to an app's Service, for example
`forward_host: app.kyq-hello.svc.cluster.local`.

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

For an existing NPM reached over `https://` from a Kubernetes target, the token is read from
key `npm_token` in Secret `kyq-edge/kyq-credentials`. Both namespace and Secret must already
carry `ky.managed-by=kyquickstart`; the installer will not adopt foreign objects.

## Host ownership

`stack.yaml` holds an `installation_id`, a random UUID written once by `plan` or, for a plan with
edges that lacks one, by the first `apply` (a plan change: it bumps `revision`, is saved only
after every step passed, and is refused on a runner-owned target). A host is this
installation's only if its `meta` holds `ky.managed-by`, `ky.target` and `ky.installation` and
its advanced configuration starts with `# kyquickstart:<installation_id>:<target>:`. Two
installations whose targets share a name therefore never touch each other's hosts.

Hosts written by builds before installation IDs carry only the target marks. They are refused,
not adopted, until you run `kyquickstart apply --adopt-edge-hosts` once: that claims every such
host on the edge's target and rewrites it to the new marks, including disabled and removed ones.
Use the flag only if no other installation shares that target name on the same NPM.

## Applying and verifying

```sh
kyquickstart preflight --state ./installation
kyquickstart apply --state ./installation
kyquickstart verify-edge --state ./installation --expected-client-ip <workstation-public-IP>
```

Preflight checks ownership, hostname/listener conflicts, certificate coverage and expiry,
and optional connector network/credentials without deploying. Apply uses the normal target
lock and resumable steps. Existing foreign hosts and wildcard conflicts are refused; an
unchanged second apply skips completed work. Removing a route from `stack.yaml` makes the next
apply disable that installer-owned NPM host, and `verify-edge` fails while it is enabled. The
host is kept so the change is reversible; delete it in NPM yourself once it is no longer needed.
Removing a whole edge from `stack.yaml` does not disable its hosts.

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

`make e2e` uses disposable local Docker resources. On kind it deploys `npm` from the catalog
with claims, bootstraps it, checks that the default login and a second unauthenticated
administrator are refused and that pods reach 80 but not 81, routes the demo app through the
NodePort with the client address preserved, reruns apply with every step skipped, and refreshes
a rejected token. Rotation of a real default login is covered by unit tests only: NPM 2.16 no
longer creates one. It proves the real NPM API accepts generated
configuration, real frps and generated frpc preserve client IP through two TLS hostnames,
unknown SNI is rejected, forwarding headers are
sanitized for frp and simulated Cloudflare traffic, the second apply is a no-op, and a removed
route is disabled, stops serving and is re-enabled when restored.
Separate connector tests prove authenticated frpc login with verified TLS and parse generated
cloudflared ingress with the pinned image. No live Cloudflare account or operator server is
used. A complete public frp/Cloudflare-to-product G11 pilot is still required.
