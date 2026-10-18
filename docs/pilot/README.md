# Pilot cluster K80

`k80.urlxl.us` (kubeadm 1.37, nodes k80 control plane and k81 worker). It also runs a hand-built
`ky-stack`; KyQuickStart installs alongside it in its own `kyq-*` namespaces and never adopts
`ky-stack` objects. Migrating `ky-stack` is a separate, later step.

K80 has no default StorageClass, by choice. `k80-local-path.yaml` is
[local-path-provisioner](https://github.com/rancher/local-path-provisioner) v0.0.37 as applied on
2026-10-17, changed from upstream only to pin both images by digest and to `reclaimPolicy: Retain`,
so deleting a claim or namespace never deletes app data. Its `local-path` class is not the
default; stack targets on K80 name it with `storage_class: local-path`. Volumes live under
`/opt/local-path-provisioner` on the node that runs the pod. Remove with
`kubectl delete -f k80-local-path.yaml` (retained volumes and their directories stay).

## Runs

**2026-10-17: Nginx Proxy Manager (master 935551a).** Plan at revision 1 with `npm` on k80 and
`node_ports: {80: 30180, 443: 30543}`, because `ky-stack`'s LiveKit holds 30000-30100.
`apply` ran secrets, deploy, health and bootstrap; a rerun skipped all four. NPM 2.16.0 runs on
k81 in `kyq-npm` (Pod Security `baseline`) with both claims bound on `local-path`. Proxy traffic
answers on `192.168.1.81:30180` only (`externalTrafficPolicy: Local`); `.80` does not. Port 81 has
no NodePort. NPM's default login is refused, and the API token is in `kyq-edge/kyq-credentials`.
No routes yet: a route needs a certificate in NPM first.

The earlier demo plan's `hello` was uninstalled first (its namespace and retained volume remain).

## Edge values (not yet applied)

A `lan` edge (see `docs/edge.md`): LAN clients reach NPM on k81's NodePort directly, no tunnel.
The certificate and KyDNS Secrets are created on the cluster by the operator with the commands
there; nothing below is a credential.

```yaml
edges:
  - target: k80
    provider: lan
    npm_url: kube://kyq-npm/app:81
    allow_cidrs: [192.168.1.0/24]
    client_port: 30543
    certificates:
      - name: k80
        domains: ['*.k80.urlxl.us']
        email: <an address of its own, not NPM's admin login; a +acme alias works>
        dns_provider: cloudflare
        credentials: {secret: cloudflare-dns, key: api_token}   # Zone:DNS:Edit on urlxl.us only
    lan_dns:
      provider: kydns
      url: https://kydns.urlxl.us/
      credentials: {secret: kydns-api, key: token}
      address: 192.168.1.81
    routes:
      - app: <app>
        hostname: <app>.k80.urlxl.us
        forward_host: app.kyq-<app>.svc.cluster.local
        forward_port: <its port>
        certificate: k80
```

Clients open `https://<app>.k80.urlxl.us:30543/`. Run `verify-edge` from a workstation on
`192.168.1.0/24` with `--expected-client-ip` set to its LAN address. Open before relying on it:

- `192.168.1.81` is k81, the node running NPM; it answers only on the NodePorts 30180/30543.
  A product that builds absolute URLs without the forwarded port sends clients to 443, which
  answers only if k81 forwards 443 to 30543. Nothing in KyQuickStart sets that up.
- KyDNS must hold `k80.urlxl.us` names inside its private zone; preflight reports the zone if not.

**2026-10-17: runner pod hand-off (master 3c82cd6, runner image 1ac7972).** `k80-runner.yaml`
runs the runner on k81 from an image imported into containerd by hand (`imagePullPolicy:
Never`), with `--pod-volumes` on a `local-path` claim and an emptyDir. Cilium does not match the
API server's node address with `ipBlock`, so a CiliumNetworkPolicy allows `kube-apiserver`.
`handoff --kyyard-target k80 --runner-pod kyq-runner/app=runner` handed revision 2 to the
runner. Through its socket (from an ephemeral curl container as uid 10001) `status` read the
cluster in-cluster, and an `upgrade npm` operation succeeded; `npm.deploy` re-ran because the
runner's release set (`0.0.0-pilot.1ac797238207`) differs from the local CLI build's (`dev`).
`pull-plan` returned revision 2, a CLI `uninstall` was refused `owned_by_runner`, and
`handoff --revoke` took revision 3 back, deleted the runner's ServiceAccount and bindings, and
left the runner `Unauthorized`. The runner pod stays deployed with no cluster access.

**2026-10-18: LAN edge and KyIdentity (master 8dc572c + cpu_request).** The plan gained a `lan`
edge (`allow_cidrs: [192.168.1.0/24]`, `client_port: 30543`) with the `k80` certificate and
KyDNS LAN DNS, and `kyidentity` routed at `identity.k80.urlxl.us`. The first apply stopped at
`kyidentity.health`: KyIdentity requested a whole CPU and k81 had about 230m unrequested
(`ky-stack` requests 94% of it), so the pod stayed Pending. With `cpu_request: 100m` under its
1-CPU limit it scheduled, and apply ran every step: NPM requested `*.k80.urlxl.us` from Let's
Encrypt by DNS-01 through the Cloudflare token (issuer YE1, valid to 2027-01-15), KyDNS got an
A record `identity.k80.urlxl.us → 192.168.1.81`, and NPM serves the route. From a LAN
workstation (192.168.1.156) the name resolves through the normal resolver and
`https://identity.k80.urlxl.us:30543/.well-known/openid-configuration` answers 200 with full
certificate verification and issuer `https://identity.k80.urlxl.us`. A rerun skipped every step.
KyIdentity's installer setup has not run yet (it needs the owner's admin and everyday logins).
