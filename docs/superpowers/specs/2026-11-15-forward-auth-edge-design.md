# Forward-auth operator routes, design

Date: 2026-11-15. Owner decision: 2026-11-15, "build forward-auth". This reverses "No
auth-proxy gate is built" in [the third-party catalog spec](2026-10-06-third-party-catalog-design.md).
Worker: f-fwdauth. The KyIdentity side is KyIdentity's `docs/FORWARD-AUTH.md` (proxy apps,
`GET /api/forward/check`, `kyidentity proxy-app`).

## What it is for

The operator's own web apps that KyQuickStart does not install, such as Sonarr or Grafana,
sit behind the KyQuickStart edge. KyIdentity decides who reaches each one, using the nginx
`auth_request` the edge already runs. Catalog apps keep native OIDC; forward-auth never
replaces it. The catalog freeze still holds: no catalog app is added.

## Route shape

The change is the smallest one that fits `stack.Route`. An **operator route** is a route with
no `app`, carrying `protect: kyidentity`:

```yaml
edges:
  - name: home
    routes:
      - hostname: grafana.example.com
        forward_host: 192.168.1.20
        forward_port: 3000
        protect: kyidentity
        login_header: X-WEBAUTH-USER   # optional: header login (see below)
        bypass: [/api/health]          # optional: paths KyIdentity does not check
        certificate: ...               # as for any route
```

The rules:

- A route with no `app` must carry `protect: kyidentity`. Without it, the route is refused.
- A route that names an `app` must not carry `protect`, `login_header` or `bypass`. Catalog
  apps use native OIDC, so naming one with `protect` is refused.
- `protect` accepts only `kyidentity`.
- KyIdentity must be installed in the same stack with a route on an edge. That route gives the
  issuer (its hostname) and the upstream the edge reaches it on (its `forward_host` and
  `forward_port`).
- `forward_host`, `forward_port`, `forward_scheme`, certificates, `exposure` and the
  one-owner-per-hostname rule are unchanged. On a cluster edge, an operator route's
  `forward_host` must be an IP, as for any upstream that is not on the cluster. That IP goes
  into the edge's egress policy as today.
- The marker is `operator:<hostname>`, written where a catalog route has `<app>:<hostname>`.
  No catalog app is named `operator`, and the plan refuses one that is.
- An operator route has no catalog `Paths` or `DenyPaths`: `location /` proxies everything, and
  `verify-edge` does not probe a status path for it.

## Generated nginx

`edge.serverConfig` stays the only generator, for NPM's `advanced_config` and the nginx edge
alike (`internal/edge/AGENTS.md`). For a protected route it adds the following:

- The internal `location = /.ky/check`. It proxies to KyIdentity's `/api/forward/check` with a
  fixed `X-Forwarded-Host`, `X-Original-URI $request_uri` and the edge secret.
- `/.ky/start`, `/.ky/callback`, `/.ky/logout` and `@ky_login`, proxied to KyIdentity's
  `/forward/*` routes.
- In `location /`: `auth_request`, `auth_request_set` and `error_page 401 = @ky_login`. Then
  `proxy_set_header` sets `X-Ky-User`, `X-Ky-Email`, `X-Ky-Groups` and the login header from
  the check's answer, and sets `X-Ky-Edge-Secret ""`. nginx never forwards the client's copy of
  a header it sets, so the client's copies are gone, and an empty value removes the header.
- `underscores_in_headers off` (nginx's default) stays, so `X_WEBAUTH_USER` cannot get through.

Where the secret lives:

- **nginx edge:** route files are in a ConfigMap, so the secret must not be inlined there. The
  route's file `include`s `/etc/nginx/ky-forward/<route key>.conf` (one `proxy_set_header
  X-Ky-Edge-Secret …;` line) from an installer-owned Secret in the nginx namespace, mounted
  like the TLS Secrets.
- **NPM:** NPM keeps `advanced_config` in its own database, as it keeps certificate keys, so
  the line is inline there.

`serverConfig` emits either the include or the inline line. The caller says which; nothing
else differs between the two edges.

## Registering the proxy app

A step runs `kyidentity proxy-app` inside the KyIdentity container, before the route step,
through the exec paths `apply-setup` uses (Docker `docker exec`, Kubernetes pod exec). It runs
with these arguments:

- `--owner <installation ID>`
- `--host <hostname>`
- `--name <hostname>`
- `--login-header`
- `--bypass`

It first requires the `proxy-apps` capability from `apply-setup --capabilities`.

- **First run:** KyIdentity creates the app and prints the edge secret once. The step
  registers the secret with the redactor and writes it only into the route's generated
  configuration.
- **Rerun:** KyIdentity answers `unchanged` or `updated` with no secret, so the step reads the
  secret back from the route's current configuration on the target (NPM's `advanced_config`
  or the nginx edge file). A second app or a second secret is never created. If that
  configuration is gone, the step asks for `--rotate-secret` and writes the new secret.
- The secret is never in a step's `InputHash`, in state, in an error, in the plan or in the
  handover output. The hash covers the configuration with the secret replaced by a fixed
  placeholder.

KyIdentity owns users, groups and policy. A new proxy app admits only assigned users and
requires MFA. The handover tells the operator to assign users on KyIdentity's App connections
page.

## The three ways an app takes the sign-in

These are documented in `docs/edge.md` and in KyIdentity's proxy-app UI:

- **Header login** (`login_header` set): the app trusts the header the edge sets.
- **OIDC:** the app should use OIDC directly, so no operator route is used.
- **Gate only** (no `login_header`): KyIdentity decides who reaches the app, and the user
  still signs in to the app itself. The edge still sets `X-Ky-User`, and the app ignores it.

Examples are given only for apps whose header login was confirmed in their own current
documentation, with the source cited.

## Isolation: what KyQuickStart checks and what it does not

Header login is safe only when the app cannot be reached except through the edge. KyQuickStart
does not deploy these apps, so it cannot prove that.

- **The strongest check it makes:** for a `login_header` route, apply opens a TCP connection
  from the workstation to `forward_host:forward_port`. If the connection succeeds, the app is
  reachable without the edge from at least one place. Apply then refuses the route and names
  the host and port. On a cluster edge whose upstream is off-cluster, the egress policy is
  unchanged.
- **Unproven:** reachability from any other machine, network or container. A firewall that
  admits the edge host and also admits others is not detected. Neither is an app that listens
  on another interface or port.
- **The operator's job:** bind the app to an address only the edge reaches, or firewall it to
  the edge host, and keep it that way. KyQuickStart does not claim to enforce isolation.

## Not built

- An operator route without `protect: kyidentity`.
- Forward-auth for catalog apps.
- Non-HTTP services, wildcard hosts, a parent-domain cookie and native-app token flows.
- Assigning users from `stack.yaml`; that is done in KyIdentity.
- Removing the KyIdentity proxy app when a route is removed. The route is disabled as today,
  and the proxy app stays for the operator to delete.

## Owner questions (defaults chosen)

1. Registration uses the container-local command, not an HTTP admin API, the same channel as
   `apply-setup`. Default: the command.
2. Header login is refused when the workstation can reach the upstream directly. Default:
   refuse. A warning only would be the alternative.
