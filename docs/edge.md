# Edge support

This build adds edge configuration to `stack.yaml`, reconciles Nginx Proxy Manager (NPM) hosts
through its API, deploys and bootstraps NPM itself on a Kubernetes target, and optionally deploys
a Docker connector for an existing Cloudflare tunnel or frp server. It is one part of the
installer, not a complete suite release: NPM is the only production catalog app and product setup
interfaces remain gated.

## Supported paths

| Connection | Current support |
| --- | --- |
| Existing NPM | Reuse existing certificates or, on a Kubernetes edge, have NPM request Let's Encrypt certificates through Cloudflare DNS-01; create and repair installer-owned proxy hosts only |
| Existing Cloudflare tunnel | Locally managed tunnel credentials; optional cloudflared deployment on Docker |
| Existing frp server | Optional frpc deployment on Docker, verified server TLS and PROXY v2 |
| LAN clients only (`lan`) | Clients connect to NPM directly, no tunnel; NPM serves only `allow_cidrs` |
| Kubernetes target | NPM from the catalog (`npm` app) managed through `kube://`, or an existing NPM's API; connector deployment is rejected |

Fresh NPM on a Docker host, DNS-01 certificates on a Docker edge or through a DNS provider other
than Cloudflare, creating Cloudflare tunnels and public DNS records, provisioning an frps server, remotely managed Cloudflare token tunnels, and Kubernetes
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

## LAN clients (`provider: lan`)

Use `lan` when the clients are on your own network and connect to NPM directly, with no tunnel
or connector: an in-cluster NPM reached on its NodePort, or an NPM on the LAN. It is not
internet-facing. Do not forward a public port to it; use `cloudflare` or `frp` for that.

```yaml
edges:
  - target: k80
    provider: lan
    npm_url: kube://kyq-npm/app:81
    allow_cidrs: [192.168.1.0/24]   # 1-32 prefixes NPM serves; everyone else gets 403
    client_port: 30543              # the port clients reach NPM's HTTPS on (default 443)
    routes: [...]                   # as for any edge
```

Apps that take their URL from their route (`{{host}}`, KyIdentity's issuer) carry `client_port`
when it is not 443. Once KyIdentity's setup has run, `apply` refuses a `client_port` or hostname
change that moves its issuer or KyRecovery's client (`issuer_changed`, `guided-setup.md`).

`connector_cidr`, `connector` and `proxy_port` are refused. Each `allow_cidrs` entry must be a
canonical prefix (host bits zero, as `netip` prints it) no wider than /8 for IPv4 or /32 for
IPv6, so `0.0.0.0/0` and `::/0` are refused. Validation does not check that a prefix is
private: a public prefix is accepted, but then the edge is reachable from it. Keep the list to
the networks your clients are on.

**What it trusts.** Only the TCP peer address. NPM's own `nginx.conf` trusts `X-Real-IP` from
every private range (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), so any LAN host could
claim another address. Each lan host overrides that with `set_real_ip_from unix:;` (NPM never
listens on a unix socket), so nginx's `$remote_addr` is the real peer. The host then allows
each prefix, denies everyone else, keeps the SNI check and replaces every forwarding header
before the product:

```nginx
# kyquickstart:<installation_id>:k80:<app>:<hostname>
set_real_ip_from unix:;
if ($ssl_server_name != <hostname>) { return 421; }
allow 192.168.1.0/24;
deny all;
location / {
  proxy_set_header X-Forwarded-For $remote_addr;   # set, never appended
  proxy_set_header X-Real-IP $remote_addr;
  proxy_set_header X-Forwarded-Port 30543;          # client_port
  ...                                               # the same location as every edge
}
```

The product therefore sees the client's own address in `X-Forwarded-For` and `X-Real-IP`; a
client's forged forwarding headers are dropped. This needs NPM to see the real client
address: on Kubernetes the `npm` app's NodePort uses `externalTrafficPolicy: Local`, so it
does. A Docker NPM behind `docker-proxy` or any NAT sees the gateway instead, which makes the
allow-list meaningless; `verify-edge` then fails because the product reports the wrong
address. NPM's ACME challenge location (`/.well-known/acme-challenge/`, Let's Encrypt
certificates only) keeps NPM's own `allow all`; it serves only challenge files, never the
product.

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
  them within 30000-32767). Preflight lists the cluster's Services and refuses a port any
  Service but this one already holds, naming it, before anything is created. Its
  `externalTrafficPolicy: Local` keeps the client address, which each route's `connector_cidr`
  check, or a lan edge's `allow_cidrs`, depends on: only the node running NPM answers.
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

NPM must be running before an edge can use it: apply once with the `npm` app and no edge, then
add the edge (with an existing certificate's ID, or a [managed certificate](#managed-certificates))
and apply again. Inside the cluster, forward to an app's Service, for example
`forward_host: app.kyq-hello.svc.cluster.local`.

## Managed certificates

On a Kubernetes edge, NPM can request a Let's Encrypt certificate through a DNS-01 challenge, so
the names never need to be public on port 80. A route then names the certificate instead of
giving `certificate_id`; each route has exactly one of the two.

```yaml
edges:
  - target: k80
    npm_url: kube://kyq-npm/app:81
    certificates:
      - name: k80                       # a DNS label, unique on the edge
        domains: ['*.k80.example.com']  # a wildcard only as the whole leftmost label
        email: ops+acme@example.com     # the Let's Encrypt account contact
        dns_provider: cloudflare        # the only provider allowed for now
        credentials: {secret: cloudflare-dns, key: api_token}
    routes:
      - app: hello
        hostname: hello.k80.example.com
        certificate: k80                # must cover the hostname
        # ...forward_host, forward_port as usual
```

`apply` runs one step per certificate, before the routes. It reuses this installation's
certificate with exactly those domains, and otherwise has NPM request it (certbot's
`dns-cloudflare` plugin) and waits for the answer, up to 15 minutes. A failed request is not
retried within the run, because each attempt counts against Let's Encrypt's rate limits; NPM's
`/data/logs/letsencrypt.log` holds certbot's reason. NPM renews the certificate; later runs only
check that it exists, is unexpired and has the configured domains, and fail if it has expired. A
certificate removed from `stack.yaml` stays in NPM.

**Ownership.** NPM 2.16 takes the Let's Encrypt contact from the NPM user who requests the
certificate and accepts no extra certificate metadata. So for each `email` the installer keeps
an NPM user with that email and nickname `kyquickstart:<installation_id>:<target>`, no password
and no role: certificates `manage`, everything else `hidden`. It reaches that user only with the
administrator's "sign in as". A certificate is this installation's only when that user owns it;
certificates of any other owner, including the administrator, are never reused or changed. If
another NPM user already has the email, apply refuses: NPM signs in by email, and a second user
could shadow the operator's login. Use an address of its own, such as a `+acme` alias.

**The Cloudflare token.** Create an API token in Cloudflare with only **Zone > DNS > Edit**,
limited to **Include > Specific zone > `example.com`** (for the pilot, `urlxl.us`). Then store it
in the cluster from a hidden prompt, so it never appears on a command line or in shell history:

```sh
# in bash; the namespace exists once the npm app has been applied
read -rs CF_TOKEN && printf %s "$CF_TOKEN" | kubectl -n kyq-edge create secret generic cloudflare-dns --from-file=api_token=/dev/stdin; unset CF_TOKEN
kubectl -n kyq-edge label secret cloudflare-dns ky.managed-by=kyquickstart
```

The installer reads only Secrets in `kyq-edge` that carry `ky.managed-by=kyquickstart`, never
`kyq-credentials` through a certificate reference, and never writes them. The workstation does
not store the token; it goes to NPM only in the certificate request body and is redacted from
every message. NPM keeps it in its database on the `vol-data` claim (and as a 0600 file under
`/etc/letsencrypt/credentials` while certbot runs) to renew the certificate. Changing the token
in the Secret does not change the copy NPM renews with. Keep the old token valid until the
certificate is reissued: delete the certificate in NPM, rerun apply (it requests a new one with
the current token and moves the routes to it), then revoke the old token.

## LAN DNS (KyDNS)

LAN clients can reach the edge directly instead of through the public connection. With
`lan_dns`, apply keeps one A record per route hostname in KyDNS,
pointing at the address LAN clients reach the edge on (for in-cluster NPM, the node that answers
its NodePorts). This is also a Kubernetes edge feature for now.

```yaml
edges:
  - target: k80
    lan_dns:
      provider: kydns                       # the only provider
      url: https://kydns.example.com/       # its admin API origin
      credentials: {secret: kydns-api, key: token}
      address: 192.168.1.81
```

The URL follows the `https://` `npm_url` rules: it must resolve to private or loopback
addresses, present a certificate the workstation trusts, and is used without a proxy or
redirects. KyDNS accepts records only strictly inside its private zone and supports no
wildcards, so every route hostname must lie inside that zone (KyDNS's `private_domain`); apply
and preflight refuse one outside it before writing anything, and report KyDNS's own refusal
code if it still declines a name.

**Ownership.** KyDNS records have no owner or comment field, so the installer keeps a ledger of
the records it created (server, record ID, name and address) under key `kydns_records` of
`kyq-edge/kyq-credentials`. A record is this installation's only while the ledger lists its ID
with the same name and address; a record changed or recreated by anyone else drops out of the
ledger and is never touched. Apply creates the missing records, deletes its own records for
hostnames that left `routes` (KyDNS records cannot be disabled), replaces its own records when
`address` changes, and refuses a hostname that any other record, service or service alias
already publishes. If a run is interrupted after KyDNS created a record but before the ledger
saved it, the next run refuses that record as foreign: delete it in KyDNS and rerun. Removing
`lan_dns` leaves the records in place.

**The KyDNS token.** KyDNS API tokens are not scoped: every token has full administrator access
to that KyDNS. The least privilege available is a token of its own, labelled for this
installation and revoked on its own with `kydns token rm <id>`:

```sh
kydns token add kyquickstart-k80   # prints the token once
# in bash, paste it at the hidden prompt
read -rs KYDNS_API_TOKEN && printf %s "$KYDNS_API_TOKEN" | kubectl -n kyq-edge create secret generic kydns-api --from-file=token=/dev/stdin; unset KYDNS_API_TOKEN
kubectl -n kyq-edge label secret kydns-api ky.managed-by=kyquickstart
```

The token stays on the cluster and in memory, is sent only as KyDNS's bearer token, and is
redacted from every message; KyDNS error bodies are never shown, only their error code.

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

`stack.yaml` holds an `installation_id`, a random UUID written once by `plan` or, for a plan
that lacks one, by the first `apply` or plan change (a plan change: it bumps `revision`, is saved only
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

For a lan edge, run `verify-edge` from a workstation inside `allow_cidrs` and pass its LAN
address; it is refused otherwise. It probes each route on `client_port`.

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
The same kind test switches the route to `lan`: the allowed client is served with its own
address in `X-Forwarded-For` and `X-Real-IP` despite forged ones, and a client outside
`allow_cidrs` is refused, also when it forges an allowed address in `X-Real-IP` or
`X-Forwarded-For` from inside NPM's trusted private ranges.
Separate connector tests prove authenticated frpc login with verified TLS and parse generated
cloudflared ingress with the pinned image. No live Cloudflare account or operator server is
used. A complete public frp/Cloudflare-to-product G11 pilot is still required.

Managed certificates and LAN DNS are covered by unit tests against fake NPM 2.16 and KyDNS APIs
written from their source (NPM `v2.16.0` `backend/schema` and `backend/internal/certificate.js`;
KyDNS `internal/adminapi`). No test reaches Let's Encrypt, Cloudflare or a real KyDNS.
