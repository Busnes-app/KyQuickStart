# frp edge research

Investigated 2026-10-08 for Phase 4. This is evidence and a recommended implementation
contract; it does not amend the approved catalog design by itself.

## Shared HTTPS port and hostnames

**Resolved:** one frpc process can carry multiple suite hostnames to one LAN NPM while frps
uses one public HTTPS listener. Configure `vhostHTTPSPort = 443` on frps and an `https`
proxy with `customDomains = ["identity.example.com", "drive.example.com"]` on frpc.
Separate proxies to different targets also share that server listener; hostname ownership
must remain unique. Public DNS points these names at the VPS.

frps selects a route from the TLS ClientHello ServerName (SNI), replaying the captured bytes
into the connection. It does not terminate application TLS. The control connection's TLS
is separate. Do not select the `https2http` plugin, which terminates application TLS at the
client, when the contract requires TLS termination at NPM.

Sources: [frp HTTP/HTTPS documentation](https://gofrp.org/en/docs/features/http-https/),
[frp v0.71.0 HTTPS multiplexer source](https://github.com/fatedier/frp/blob/v0.71.0/pkg/util/vhost/https.go),
[HTTPS termination plugin](https://gofrp.org/en/docs/examples/https2http/).

## Client IP and NPM compatibility

**frp supports the required wire format:** `transport.proxyProtocolVersion = "v2"` on
an HTTPS proxy makes frpc prepend the original connection address to the local TLS stream.
The destination listener must explicitly accept PROXY protocol.
[Official real-IP documentation](https://gofrp.org/en/docs/features/common/realip/).

**Standard NPM 443 does not:** the inspected v2.16.0 listener template emits `listen 443 ssl`
without `proxy_protocol`. Upstream PR #4505 was still open and unmerged when checked.
Sending a PROXY preamble directly to that listener therefore fails TLS negotiation.
Sources: [v2.16.0 listener template](https://github.com/NginxProxyManager/nginx-proxy-manager/blob/v2.16.0/backend/templates/_listen.conf),
[upstream PROXY support proposal](https://github.com/NginxProxyManager/nginx-proxy-manager/pull/4505).

### Recommended contract

Use the supported proxy-host API's `advanced_config` on installer-owned hosts to add a
**dedicated additional listener**, for example:

```nginx
listen 444 ssl proxy_protocol;
set_real_ip_from 172.30.0.3/32;
real_ip_header proxy_protocol;
real_ip_recursive off;
```

The address above is illustrative: use the actual pinned frpc address. Add the corresponding
IPv6 listener only when enabled. Forward frpc to this listener, keeping existing NPM 443
traffic ordinary TLS. All installer hosts sharing 444 must use identical listener options.
Require no other host to own this port. Publish no public route to 444 and enforce that only
frpc can reach it. The extra listener must not expose the admin port or unrelated hosts.
Only create it after a valid certificate exists; otherwise `listen ... ssl` lacks certificate
configuration. Use DNS-01 issuance before enabling the host.

This uses NPM's server-context insertion point and nginx's existing real-IP support; it
is **not** a native NPM PROXY setting. Do not edit generated files, global templates,
unmanaged hosts or NPM's database. An existing NPM that cannot satisfy these prerequisites
must fail preflight with an actionable diagnostic, rather than silently lose client IP.
Sources: [proxy-host template](https://github.com/NginxProxyManager/nginx-proxy-manager/blob/v2.16.0/backend/templates/proxy_host.conf),
[nginx real-IP module](https://nginx.org/en/docs/http/ngx_http_realip_module.html).

Use an API-defined custom location `/`, with WebSocket support where the app needs it.
NPM v2.16.0's custom-location template already overwrites `X-Forwarded-For` and `X-Real-IP`
with `$remote_addr`, and scheme headers with `$scheme`. Clear `Forwarded` and other
product-recognized forwarding headers in that location's advanced config. Restrict server
real-IP trust to the immediate frpc peer; product trust is separately restricted to NPM.
Do not rely on server-level header directives: the default proxy location defines its own
headers, appends incoming X-Forwarded-For, and its scheme variables preserve incoming values.
Sources: [custom location template](https://github.com/NginxProxyManager/nginx-proxy-manager/blob/v2.16.0/backend/templates/_location.conf),
[default proxy include](https://github.com/NginxProxyManager/nginx-proxy-manager/blob/v2.16.0/docker/rootfs/etc/nginx/conf.d/include/proxy.conf),
[global real-IP defaults and scheme maps](https://github.com/NginxProxyManager/nginx-proxy-manager/blob/v2.16.0/docker/rootfs/etc/nginx/nginx.conf).

## Local proof

Executed a loopback-only experiment using official frp v0.71.0 binaries and an existing
`nginx:alpine` image; no live hosts or external DNS were modified. One frps listener on
19443 and one frpc HTTPS proxy carried two SNI names to nginx on 18443 with PROXY v2.
A local HTTP backend returned its received headers. Both requests passed these assertions:

- The requested hostname reached the backend correctly.
- Client IP was `127.0.0.1`, recovered from PROXY v2.
- Supplied `X-Forwarded-For: 198.51.100.66`, matching `X-Real-IP`, and a false HTTP scheme
  were replaced by the connection IP and `https`.
- A supplied `Forwarded` header was removed.

The experiment used unprivileged ports and a throwaway self-signed certificate, disabling
certificate verification only in the local test client. Both frp processes and the nginx
container were stopped afterward. Scratch reproducer: `/tmp/kyq-frp-research/proof.py`.

**Limits:** this proves frp SNI multiplexing, PROXY v2 reception, and nginx sanitization.
It does not prove NPM API lifecycle, certificate issuance, Kubernetes source addressing,
Internet IPv6 behavior, or any product's G11 endpoint. Add integration coverage against a
pinned NPM image and both deployment drivers before marking Phase 4 ready. Assert unknown
SNI rejection, duplicate hostname refusal, forged header rejection, WebSocket operation,
and that plain TLS on 443 and PROXY TLS on 444 coexist without changing unmanaged hosts.

## Container and secret-file wiring

frpc v0.71.0 supports a token file without embedding the token in generated TOML:

```toml
auth.method = "token"
auth.tokenSource.type = "file"
auth.tokenSource.file.path = "/run/secrets/frp-token"
```

This is mutually exclusive with `auth.token`. The local proof was repeated using a token
file and passed. [Official full example](https://github.com/fatedier/frp/blob/v0.71.0/conf/frpc_full_example.toml).

The upstream release workflow publishes `ghcr.io/fatedier/frpc:v0.71.0` and
`fatedier/frpc:v0.71.0` (the `v` prefix is significant), with linux/amd64 support. The Dockerfile
uses `/usr/bin/frpc` as entrypoint and does not require a host install. Supply `-c` and a
read-only config mount; run nonroot with dropped capabilities and a read-only root filesystem.
A registry manifest check confirmed linux/amd64 image digest
`sha256:8dd029fa1f995629d6f31157f270633224f39492da079b099ce39dddaad3e191`.
Sources: [release image workflow](https://github.com/fatedier/frp/blob/v0.71.0/.github/workflows/build-and-push-image.yml),
[frpc Dockerfile](https://github.com/fatedier/frp/blob/v0.71.0/dockerfiles/Dockerfile-for-frpc).

**NPM Kubernetes admission caveat:** setting PUID/PGID does not make the stock NPM bootstrap
nonroot. Its prepare scripts edit users/groups and chown root-owned system paths, before
runtime services drop privileges. Thus the stock entrypoint is not an established fit for
this repository's `restricted` namespace contract. Do not weaken Pod Security or silently
claim Kubernetes NPM deployment works. A compatible image/startup contract needs independent
implementation and launch verification, or Kubernetes targets must use an existing compatible
external NPM. This caveat is source inspection, not a successful/failed container launch.
Sources: [NPM user/group bootstrap](https://github.com/NginxProxyManager/nginx-proxy-manager/blob/v2.16.0/docker/rootfs/etc/s6-overlay/s6-rc.d/prepare/10-usergroup.sh),
[ownership bootstrap](https://github.com/NginxProxyManager/nginx-proxy-manager/blob/v2.16.0/docker/rootfs/etc/s6-overlay/s6-rc.d/prepare/30-ownership.sh),
[runtime privilege drop](https://github.com/NginxProxyManager/nginx-proxy-manager/blob/v2.16.0/docker/rootfs/etc/s6-overlay/s6-rc.d/nginx/run).
