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

Certificates and LAN DNS for K80, per `docs/edge.md`. Both Secrets are created on the cluster
by the operator with the commands there; nothing below is a credential.

```yaml
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
```

Routes use `certificate: k80` and hostnames `<app>.k80.urlxl.us`. Open before relying on them:

- `192.168.1.81` is k81, the node running NPM; it answers only on the NodePorts 30180/30543,
  so a LAN client resolving `<app>.k80.urlxl.us` reaches HTTPS on 443 only if k81 forwards 443
  to 30543. Nothing in KyQuickStart sets that up.
- KyDNS must hold `k80.urlxl.us` names inside its private zone; preflight reports the zone if not.
