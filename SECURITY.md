# Security Policy

KyQuickStart is the deterministic execution layer for suite installation. It enforces validated operations, ownership, credential handling, and verifiable results against operator-provided Docker hosts over SSH and Kubernetes clusters.

## Reporting vulnerabilities

Report security vulnerabilities privately through GitHub Security Advisories:
<https://github.com/Busnes-app/KyQuickStart/security/advisories>. Do not open a public issue.
Include the affected area, reproduction steps, impact, and affected versions.
We aim to acknowledge reports within two business days and will coordinate a fix
and disclosure.

## Trust boundaries and limitations

- It never installs Docker or Kubernetes; it acts on bring-your-own, operator-provided targets only.
- Operator secrets are handled with least privilege and never logged; generated secret files are written with restrictive permissions.
- Every operation is validated and ownership-checked before execution; a verifiable result is the only evidence of success.
- Bulk backups follow the suite contract: restic to an append-only rest-server beside KyRecovery; nothing decrypts inside KyRecovery and restores run on the operator workstation.

## Operating requirements

- Verify SSH fingerprints before first contact and use a trusted kubeconfig for Kubernetes.
  Docker targets may be on the operator's private network; the installer does not impose a
  blanket private-address refusal. Product recovery URLs have their own separate policy.
- Preserve existing suite services and credentials; never uninstall a skipped-but-installed product or delete its data.
- Verify each gate before declaring readiness.

## SSH policy and key rotation

Every installer and suite-runner SSH connection uses the shared `remote.Dial` boundary.
Transport negotiation uses the pinned `x/crypto/ssh` dependency's
[`SupportedAlgorithms`](https://pkg.go.dev/golang.org/x/crypto/ssh@v0.58.0#SupportedAlgorithms):
modern key exchanges, AES-GCM/CTR or ChaCha20-Poly1305, and untruncated HMACs. SHA-1 key
exchange, CBC/RC4/3DES and 96-bit MACs are excluded. Full HMAC-SHA1 remains allowed by
that upstream policy; SHA-1 collision attacks do not break the keyed MAC
([RFC 6194 section 3.3](https://www.rfc-editor.org/rfc/rfc6194.html#section-3.3)).

Host and authentication keys must be Ed25519, supported ECDSA, or RSA of at least 2048
bits. RSA host/authentication signatures use SHA-2; authentication cannot fall back to
`ssh-rsa`/SHA-1 even when the server offers only that signature. User certificates supplied
by the agent must also have a policy-sized subject and authority and a supported certificate
signature. Normal server-side certificate trust/validity checks remain the server's job.
Agent identities are decoded from their public-key bytes, rather than trusting the wrapper's
type. Unusable identities are not offered; an agent with no usable identity refuses login.
A private key file that fails policy is refused. No key or pin is rewritten automatically.

Before upgrading a legacy target, keep a working administrator session open. Add a strong
host key and authorize a new strong user key through that existing access, then test the new
login separately. Compare the host's new fingerprint out of band before deliberately updating
this installer's `<state>/known_hosts`. A pin mismatch is never bypassed by a trust flag or
prompt. Keep the old access until the new key works; do not reset the installer state or
delete target data to recover from a policy refusal. An older agent without RSA SHA-2 support
needs an update or a supported Ed25519/ECDSA identity.

This is an SSH policy. It does not establish strong-key compliance for Kubernetes TLS,
external authentication plugins, deployed products or the AI launcher's separate OpenSSH path.

## Security-sensitive changes

Changes to credential handling, SSH/remote execution, driver operations, the reverse proxy / edge, or backup orchestration require tests for both the expected path and the relevant
failure or attack path, plus a description of the trust boundary in the pull
request.

## Related documents

- [CONTRIBUTING.md](CONTRIBUTING.md) — contribution and review requirements
- [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) — community expectations
- [README.md](README.md) — what this project is and how to run it

