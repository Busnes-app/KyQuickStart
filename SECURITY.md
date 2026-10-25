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

- Connect over HTTPS/SSH only; refuse redirects and, by default, private, loopback, and link-local targets.
- Preserve existing suite services and credentials; never uninstall a skipped-but-installed product or delete its data.
- Verify each gate before declaring readiness.

## Security-sensitive changes

Changes to credential handling, SSH/remote execution, driver operations, the reverse proxy / edge, or backup orchestration require tests for both the expected path and the relevant
failure or attack path, plus a description of the trust boundary in the pull
request.

## Related documents

- [CONTRIBUTING.md](CONTRIBUTING.md) — contribution and review requirements
- [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) — community expectations
- [README.md](README.md) — what this project is and how to run it

