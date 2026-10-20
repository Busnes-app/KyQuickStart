# Pilot cluster K80

`k80.example.com` (kubeadm 1.37, nodes k80 control plane and k81 worker). It also runs a hand-built
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
        domains: ['*.k80.example.com']
        email: <an address of its own, not NPM's admin login; a +acme alias works>
        dns_provider: cloudflare
        credentials: {secret: cloudflare-dns, key: api_token}   # Zone:DNS:Edit on example.com only
    lan_dns:
      provider: kydns
      url: https://kydns.example.com/
      credentials: {secret: kydns-api, key: token}
      address: 192.168.1.81
    routes:
      - app: <app>
        hostname: <app>.k80.example.com
        forward_host: app.kyq-<app>.svc.cluster.local
        forward_port: <its port>
        certificate: k80
```

Clients open `https://<app>.k80.example.com:30543/`. Run `verify-edge` from a workstation on
`192.168.1.0/24` with `--expected-client-ip` set to its LAN address. Open before relying on it:

- `192.168.1.81` is k81, the node running NPM; it answers only on the NodePorts 30180/30543.
  A product that builds absolute URLs without the forwarded port sends clients to 443, which
  answers only if k81 forwards 443 to 30543. Nothing in KyQuickStart sets that up.
- KyDNS must hold `k80.example.com` names inside its private zone; preflight reports the zone if not.

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
KyDNS LAN DNS, and `kyidentity` routed at `identity.k80.example.com`. The first apply stopped at
`kyidentity.health`: KyIdentity requested a whole CPU and k81 had about 230m unrequested
(`ky-stack` requests 94% of it), so the pod stayed Pending. With `cpu_request: 100m` under its
1-CPU limit it scheduled, and apply ran every step: NPM requested `*.k80.example.com` from Let's
Encrypt by DNS-01 through the Cloudflare token (issuer YE1, valid to 2027-01-15), KyDNS got an
A record `identity.k80.example.com → 192.168.1.81`, and NPM serves the route. From a LAN
workstation (192.168.1.156) the name resolves through the normal resolver and
`https://identity.k80.example.com:30543/.well-known/openid-configuration` answers 200 with full
certificate verification and issuer `https://identity.k80.example.com`. A rerun skipped every step.
KyIdentity's installer setup has not run yet (it needs the owner's admin and everyday logins).

**2026-10-18: KyIdentity installer setup (master cc59590).** The plan gained a `kyidentity`
integration with pilot-only accounts `k80-admin` and `k80-user` (no mail relay is configured;
setup needs none). `apply` ran `kyidentity.setup`, which generated both passwords as 0600 files in
`/data/installer/<setup_id>/` in the pod. Over the LAN route, with full certificate verification
and each password piped from the pod (never printed), both accounts signed in (200 and a session);
a wrong password got 401; `k80-admin` read `/api/admin/users` (200) and `k80-user` was refused (403).
A rerun skipped every step. Still open: `kyq-bootstrap` stays an active administrator until
KyIdentity-server#109 lands, and no mail relay is set.

**2026-10-18: KyIdentity reset and KyRecovery with SSO (master 88834c2).** KyIdentity's first
setup had no clients, and KyIdentity 0.4.2 cannot add one later (KyIdentity-server#110), so the
pilot KyIdentity was reset: namespace `kyq-kyidentity` and its retained volume deleted (only the
test accounts were lost; the volume's directory on k81 needs `sudo rm` by hand). The plan then
gained `kyrecovery` at `recovery.k80.example.com`, a new KyIdentity setup ID, and `10.0.0.0/8`
(Cilium's pod pool) in the lan edge's `allow_cidrs`, because KyRecovery's pod reaches the issuer
through NPM. With `{{host}}` the issuer is `https://identity.k80.example.com:30543`. `apply` ran
every step: KyIdentity setup with the derived `kyrecovery` client, KyRecovery deploy and setup,
its route and KyDNS record, and printed the custodian ceremony as an outstanding human step.
From the LAN, `k80-admin` signed in to KyIdentity, KyRecovery redirected to it with
`client_id=kyrecovery` and its `:30543` callback, KyIdentity issued a code, and KyRecovery's
session reports role `admin`; `k80-user` got `access_denied`. A rerun skipped every step.

**2026-10-18: KyIdentity 0.4.3 and the first setup amendment (master 9389537).** The pilot apps
were reset once more onto 0.4.3 (the upgrade gate refuses 0.4.2 → 0.4.3 without a verified
backup). The plan was applied in two passes. First KyIdentity and KyRecovery: setup ran with the
derived `kyrecovery` client and `kyidentity.retire-bootstrap` disabled `kyq-bootstrap` (its
first-run password file is gone; `disable-user` now answers `unchanged`). Then kyPulse was added
to the plan: `apply` found KyIdentity's setup without its client, created setup amendment
`5481a036-…` (recorded as `identity_amendment` at plan revision 4), ran
`kyidentity.amend.kypulse`, and set kyPulse up with the client the amendment issued. From the
LAN, `k80-admin` signed in to KyRecovery (role `admin`) and to kyPulse, whose targets
`kyidentity` and `kyrecovery` report `ok`; `k80-user` got `access_denied` from both. A rerun
skipped every step. Adding an app after KyIdentity's setup no longer needs a reset.

**2026-10-18: KyDrive on a Docker host, joined by amendment (master 5035865).** The plan gained
Docker target `hlwcdocker02` (192.168.1.91, root `/home/yoshi/kyq`; its ECDSA host key
`SHA256:F33QGW…` matched `/etc/ssh` on the host and the operator's `known_hosts`) and `kydrive`
there, routed through K80's lan edge at `drive.k80.example.com` → `192.168.1.91:18443`. `apply`
created KyIdentity setup amendment `kydrive`, deployed KyDrive (uid 1000, read-only root,
`cap_drop ALL`, `no-new-privileges`; data and secrets 0700/0600), moved its client secret from
the K80 pod to the container over SSH stdin, and set it up; NPM's egress now admits exactly that
IP and port, which Cilium enforces (the route works). From the LAN, `k80-admin` signed in to
KyDrive through KyIdentity (role `admin`); `k80-user` got 403 "User account not provisioned"
(KyIdentity's SCIM connector to KyDrive is not built yet). A rerun skipped every step.
**Open:** port 18443 also answers LAN clients directly (bypassing NPM's allow-list) until the
operator adds the documented `DOCKER-USER` rule. KyQuickStart needs an SSH agent; with none
running, start one for the run (`ssh-agent`, `ssh-add`).

**2026-10-18: KyIdentity SCIM to KyDrive (master 41f9e27).** `apply` restarted KyIdentity (its
env gained `KYIDENTITY_ALLOW_PRIVATE_CALLBACKS`) and ran `kyidentity.scim.kydrive`, which copied
KyDrive's SCIM token from hlwcdocker02 into KyIdentity's pod and applied a connector pass of the
existing `kydrive` amendment. Delivery first failed: the operator had added the documented
`DOCKER-USER` rule (`! -s 192.168.1.81 -j DROP` for port 18443), and on this host (Docker 29,
firewalld, userland proxy) it dropped k81 too, so NPM got no answer and the drive route hung
after TLS. With the rule removed, the route answered and KyIdentity's retry provisioned
`k80-user` within about two minutes; `k80-user` then signed in to KyDrive with role `user` and
`k80-admin` with role `admin`. A rerun skipped every step. KyDrive's port is again reachable
from the LAN directly; restricting it is open.
