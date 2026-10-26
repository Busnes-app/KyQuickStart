# Supported key-strength and TLS configuration

This profile configures KyQuickStart's own SSH/HTTPS/Kubernetes clients and its
explicitly invoked Restic backup client. Use a build containing this profile and
the Restic container policy. It preserves existing identities, credentials,
repositories and data. It is not a FIPS certification or certification of the
applications the installer deploys.

## Start each installer process with the profile

For a workstation binary built with the pinned Go 1.26.10 toolchain:

```sh
GODEBUG=fips140=on ./kyquickstart preflight
GODEBUG=fips140=on ./kyquickstart apply
GODEBUG=fips140=on ./kyquickstart handoff
```

Use the same setting for later installer commands. Preserve any necessary existing
GODEBUG entries and keep `fips140=on` in the effective value. The Go setting is read
at process startup; changing the environment of an already running process is not
activation. Do not export this setting into unrelated product containers.

The runner image from this source sets `GODEBUG=fips140=on` itself. This covers its
standalone Docker deployment, standalone Kubernetes deployment and catalog KyYard
sidecar: they use the same runner image. If deployment configuration replaces
GODEBUG, retain the setting. Upgrade earlier images to a release containing both
this profile and the independent Restic policy. Verify release provenance and the
pinned runner image as described in [Verifying releases](VERIFYING-RELEASES.md).
A workstation setting alone does not enter a resident or backup container.

On Linux, normal system trust remains available. For a private administration CA,
Go also supports `SSL_CERT_FILE` and `SSL_CERT_DIR`; files/directories must be
readable by the actual installer/uid-10001 runner. Mount only public CA material
read-only. Kubernetes CA data/files are selected by its kubeconfig; Restic has its
own optional `ca_file`. These are separate trust stores. Never use a verification
bypass to make a private CA work.

## Keys and selected credentials

| Mechanism | Configuration |
| --- | --- |
| SSH transport and administrator/agent identity | Existing enforced policy: RSA at least2048 actual bits, supported ECDSA or Ed25519; the same floor for certificate subjects/signing CAs; RSA SHA-2 signatures. Compare host fingerprints and retain authorized access during deliberate rotation. |
| Go HTTPS peer authentication | With normal verification enabled, the profile refuses RSA below2048 throughout an accepted chain, including its trust anchor. Supported EC certificates include P-256/P-384/P-521 and Ed25519. Name, time and trust checks remain enabled. |
| Kubernetes API | Authenticated `https://` endpoint; `insecure-skip-tls-verify: false`; correct CA data/file and expected name (or intentional valid `tls-server-name`). Every followed endpoint must remain verified HTTPS. |
| Resident Kubernetes credential | Existing handoff imports only token-authenticated kubeconfigs with inline CA, no exec/proxy/file credentials. Protect the retained runner state; never place a private kubeconfig inside the workstation's secret-free plan directory. |
| Workstation bearer credential | A protected token/token file issued by the intended cluster; no competing certificate/exec credential in that selected context. It travels only over verified API TLS. |
| Workstation mTLS alternative | Select a verified strong client key/certificate and strong issuing chain as the sole certificate source, using RSA at least2048 or supported strong EC. Preserve the original key/identity; arrange deliberate renewal with the same floor. |
| Restic transport and repository encryption | Every installer-owned container independently sets its Go profile. Strong private/public CA chains remain supported. Repository keys remain the fixed AES256/Poly1305-AES format; existing passwords and archives are retained. |
| Generated runner/target keys | Runner SSH key is fixed-size Ed25519 from crypto/rand; generated target secrets use32 OS-random bytes. TLS/SSH session key sizes use their maintained implementations. Non-secret UUID/hash resource identifiers are not authentication keys. |

For a direct workstation context, the following shape selects bearer authentication
without introducing another TLS client or certificate source. Keep the referenced
files private and outside the plan/state directory; use your actual trusted API
and protected paths. The token value is not part of the plan or this example.

```yaml
apiVersion: v1
kind: Config
current-context: ky
clusters:
  - name: ky
    cluster:
      server: https://api.example.lan:6443
      certificate-authority: /protected/kubernetes/ca.pem
      insecure-skip-tls-verify: false
contexts:
  - name: ky
    context:
      cluster: ky
      user: operator
users:
  - name: operator
    user:
      tokenFile: /protected/kubernetes/token
```

The baseline direct profile uses no kubeconfig proxy and no environment proxy.
It supports Docker and Kubernetes targets, administration and resident operation.
It does not change the product's valid client-certificate, exec, proxy,
impersonation or private-CA features.

**Local mTLS needs its own credential condition.** `fips140=on` filters remote
peer chains; it does not validate the size of a local client key returned by
client-go. Native tests demonstrate RSA1024/2047 client signing still works in that
mode. For a fixed mTLS configuration, use only verified strong certificate/key
material, with no alternative weak identity or unreviewed renewal source. Keep
certificate/key files protected and review replacements before activating them.
Do not describe the runtime setting as automatic client-key rejection.

A selected exec authenticator can remain valid when its actual implementation,
network trust and returned credentials meet the same conditions. Likewise, proxies
need a qualified endpoint/trust/authentication configuration; an HTTPS proxy adds
its own TLS peer. Unrestricted plugins, renewal or arbitrary proxy settings are
not qualified by the direct profile. `fips140=only` is not a production remedy:
Go documents it as an assessment mode that rejects non-approved operations.

## Retain access during certificate migration

Before changing a destination with an undersized certificate/CA, keep working
administrative access, issue a strong replacement and verify its identity. Update
only the relevant trusted CA/credential configuration after that verification,
then test the API/admin connection or snapshot listing. Keep the old data,
repository password, installer SSH identity/pins and existing user access until
the replacement is verified. Never reset state, rotate all secrets, recreate an
archive or weaken certificate verification to recover from a TLS refusal.

The image default also narrows TLS negotiation according to the maintained Go
profile. A legacy-only server may need a TLS configuration update even if its
certificate key is already strong. The [backup runbook](guided-setup.md#backups-and-restore-on-docker)
explains the independent Restic destination migration.

## Scope and verification

The resident API is a private Unix socket, not another TLS listener. Inner NPM
HTTP goes through the installer's authenticated Kubernetes port-forward. The
KyDNS forwarding-header probes use explicit unauthenticated HTTP, including the
curl invocation in NPM; they do not negotiate TLS. Product setup commands execute
inside separately installed products. Their protocols, Docker/cluster image
delivery, frpc/cloudflared and platform credential issuance retain their own
configuration/trust contracts; installer GODEBUG does not configure them.

Local Linux/amd64 Go1.26.10 race probes exercised actual Kubernetes REST, exec and port-forward
TLS startup and actual NPM, KyDNS and route HTTPS clients:132 outcomes cover weak
RSA leaf/intermediate/root, strong RSA/P-256 private CA, hostname, expiry and trust
controls, in ordinary and configured runtimes. These stream-startup probes do not
execute a real pod command or transfer a port-forward payload. Six native mTLS
outcomes establish the separate local-key limitation above. Restic's real-image
regression and ordinary backup/restore tests provide its separate evidence.
These local qualifications do not certify a deployment or establish broad test
coverage. Other platform trust-store behavior was not executed by these probes. Full image, integration and review gates remain required for releases.

Primary runtime sources:

- [Go FIPS runtime setting and validation limits](https://go.dev/doc/security/fips140#the-fips140-godebug-option).
- [Go1.26.10 TLS key/algorithm policy](https://github.com/golang/go/blob/go1.26.10/src/crypto/tls/defaults_fips140.go#L17-L77).
- [All certificates in accepted peer chains](https://github.com/golang/go/blob/go1.26.10/src/crypto/tls/common.go#L1824-L1860).
- [Client-go credential callback](https://github.com/kubernetes/client-go/blob/v0.37.1/transport/transport.go#L80-L207).
- [RSA local-key restriction is specific to only-mode](https://github.com/golang/go/blob/go1.26.10/src/crypto/rsa/fips.go#L418-L444).
