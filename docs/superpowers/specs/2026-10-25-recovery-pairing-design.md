# Recovery pairing: catalog products deposit sealed backups in KyRecovery

Status: proposed 2026-10-25. Refs #93. Stage 1 shipped (#114). Stage 2's installer part is built
for KyPost and waits on KyRecovery ("Stage 2 as built" below).

## Problem

`apply` installs KyRecovery and prints the custodian ceremony. After that, nothing pairs a
product with it, so no product ever deposits a backup. Two separate gaps cause this:

1. **Private address.** KyRecovery is internal-only, so its URL resolves to a private
   address. Every product's recovery client refuses a private address unless its opt-in
   variable is set, and the catalog sets it for none of them (KyDNS is set to `false`).
2. **No pairing step.** Even with the opt-in set, `apply` never mints a pairing code and
   never hands one to a product.

The kyquickstart restic `backup` covers Docker apps only, and `upgrade` still refuses
`migrates_data` versions with `backup_required`. Today a Kubernetes install has no backup
of any kind.

## What the products accept today

Every fact below was read at the catalog's pinned tags. KyIdentity 0.4.8.1 behaves like 0.4.7.

**KyRecovery 0.4.3**
- `kyrecovery pair generate [--ttl M] [--service S] [--app A]` prints `Pairing Code: NNNNNN` on stdout (`app.go:226-259`).
- The code is a single-use 6-digit code from crypto/rand. Its default lifetime is 15 minutes, and a second claim gets 409 (`pairing.go:16-31`, `db.go:707-738`).
- Generate always succeeds. Before the ceremony, the *claim* gets 409 "No recovery key imported" and the code is not consumed (`server.go:764-773`).
- `--service` is only a label: the claiming product's own service name replaces it.
- Each deposit updates `paired_apps.last_backup_at`. That value is readable through `GET /api/pairing/list`, which needs the viewer role. No CLI shows it: `pair list` shows status only.

**Shared recovery client** (ky-primitives v0.10.0)
- HTTPS only, and redirects are refused.
- The private opt-in adds RFC1918, ULA and CGNAT addresses. Loopback, link-local and reserved ranges stay refused.
- The address is checked at dial time, so DNS rebinding is covered.
- Only the system trust store is used. The catalog's internal certificate comes from Let's Encrypt, so that is enough.

| Product | Private opt-in | Pairing without a browser | Deposit schedule |
| --- | --- | --- | --- |
| KyNotes | `KYNOTES_BACKUP_ALLOW_PRIVATE_RECOVERY` (`true`/`1`) | `kynotes.setup/1` `backup.recovery.{url, pairingCodeFile}`, claimed by `apply-setup` through the running server. The code file must be private and owned by the app user, so a default Kubernetes Secret projection (0440) is refused | `KYNOTES_BACKUP_DEPOSIT_INTERVAL` |
| KyPost | `KYPOST_BACKUP_ALLOW_PRIVATE_RECOVERY` (`ParseBool`) | `kypost.setup/1` `recovery.{url, codeFile}`; the bundle can also arrive on stdin (`--file -`). An existing pairing with the same URL is skipped | `KYPOST_BACKUP_DEPOSIT_INTERVAL` |
| KyIdentity | `KYIDENTITY_BACKUP_ALLOW_PRIVATE_RECOVERY` | none: admin API `POST /api/admin/backup/pair-remote` only | `KYIDENTITY_BACKUP_DEPOSIT_INTERVAL` (default 24h) |
| KyCalendar | `KYCALENDAR_BACKUP_ALLOW_PRIVATE_RECOVERY` | none: the bundle's `backup.recovery` is refused ("not yet supported"); admin API with step-up | `KYCALENDAR_BACKUP_DEPOSIT_INTERVAL` |
| kyPulse | `KYPULSE_BACKUP_ALLOW_PRIVATE_RECOVERY` (strict; an invalid value fails startup) | none (refused); admin API | `KYPULSE_BACKUP_DEPOSIT_INTERVAL` |
| KyYard | `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` | none (refused); admin API | `KY_BACKUP_DEPOSIT_INTERVAL` |
| KyDrive | `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` | none (refused); admin API | `KY_BACKUP_DEPOSIT_INTERVAL` |
| KyMessages | `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` | none (refused); admin API with a fresh admin session | `KY_BACKUP_DEPOSIT_INTERVAL` |

Three products read the shared `KY_` name, not a product prefix. KyCalendar refuses the
old `KY_` name outright.

## Decision

Pairing ships in three stages. Each stage helps on its own.

### Stage 1: allow the internal KyRecovery (catalog only)

When the plan installs KyRecovery and its route is `exposure: internal`, every catalog Ky
product gets its own opt-in variable from the table, set to `true`. Without that route the
variable stays unset (KyDNS keeps `false`).

This is safe because the opt-in relaxes only RFC1918, ULA and CGNAT addresses. Loopback,
link-local and metadata ranges stay refused, and the address is checked at dial time. The
products are the only callers, and the URL is the plan's own.

Deploy hashes change, so the next `apply` redeploys each product once. `review` lists the
products that will accept a private recovery address.

At the end of this stage pairing works by hand. The handover replaces "no catalog product
can pair" with one step per product:
1. On KyRecovery's target, mint a code with `kyrecovery pair generate --service <app>` (Docker: `docker exec`; Kubernetes: `kubectl exec`).
2. Type the code into the product's backup page.

The code never passes through kyquickstart.

### Stage 2: pair KyNotes and KyPost during `apply` (catalog and CLI)

A new step `<app>.recovery-pair` runs after the app's setup. It runs only when the plan has
KyRecovery and the app supports setup pairing:

1. **Gate on the ceremony.** Generating a code proves nothing, because only a claim fails before the ceremony. Instead, the step reads KyRecovery's key state and, without a key, records `waiting_for_ceremony` and stays pending, so `apply` ends with the ceremony step as today. Open question 1 covers how to read the key state.
2. **Mint the code** on KyRecovery's target: `kyrecovery pair generate --ttl 10 --service <app> --app <display name>`. The `Pairing Code: ` line is parsed in memory and never written to state or output, like `credential_transfer.go` does with client secrets.
3. **Deliver it**, still in memory:
   - KyNotes: write it to a private file owned by the app user inside the app's private volume, with `umask 077` through exec, never a Secret. Then run `apply-setup` with `backup.recovery.{url, pairingCodeFile}`, and remove the file afterwards.
   - KyPost: send the bundle with `recovery.{url, code}` on stdin (`apply-setup --file -`), so no file exists at all.
4. **Record** `paired` with KyRecovery's URL. A rerun is skipped when it matches. KyPost skips by itself; KyNotes needs the same check. A changed KyRecovery URL is refused (`recovery_changed`), like `issuer_changed`.
5. **Deposit and check.** The step runs the product's `deposit` CLI and then checks the result:
   - KyNotes reports `last_result`/`receipt`; KyPost reports `lastReceipt`.
   - It then checks `last_backup_at` for that service on KyRecovery (open question 2).
   - Only then does the handover call the product "backed up".

A failed claim (expired, used, 409) fails the step. Rerunning mints a new code, and an
unclaimed code expires on its own.

### Stage 2 as built (2026-10-28)

Reading the products at their pins changed three points:

- **The owner confirms the key.** A key KyRecovery reports is not one the owner has checked. The
  step pairs only to `recovery_key_id` on the plan's `kyrecovery` app: the key ID the ceremony
  page shows and the custodian cards carry (lowercase hex SHA-256 of the public key). Without
  it the step passes with a note. A KyRecovery holding another key, or a product paired to
  another, fails the step. After the claim the step reads the new pairing's key again before
  the first deposit, so a key rotated in during the claim seals nothing.
- **KyRecovery's state comes from KyRecovery.** The step asks KyRecovery, not the product,
  whether the product is paired and has deposited. A product paired by hand, even under
  another URL spelling, is not paired again, and nothing is recorded on the workstation.
- **KyNotes cannot take a code file.** Its image has no shell, and its bundle refuses a file
  the container user does not own, which rules out a Secret on Kubernetes. KyNotes needs a
  release that reads the code on stdin, as it already reads `--import-client`.

The step therefore runs for KyPost only (`kypost.recovery-pair`, after the edges, since KyPost
reaches KyRecovery through its route). It needs a KyRecovery release with this command, which
the installer reads and nothing else:

- `kyrecovery status --json`, advertised as setup capability `status`, reading the same
  database as `pair list`:
  `{"schema": "kyrecovery.status/1", "recovery_key_id": "<hex, or empty before the ceremony>",
  "paired": [{"service_name", "recovery_key_id", "last_backup_at"}]}`, with only `paired`
  pairings listed and `last_backup_at` empty before the first deposit.

Until then, with KyRecovery 0.4.3, the step says to pair KyPost by hand. A suite runner older
than this release refuses a plan carrying `recovery_key_id`.

### Stage 3: the other six products (product releases)

KyIdentity, KyCalendar, kyPulse, KyYard, KyDrive and KyMessages each need a release whose
`apply-setup` accepts `backup.recovery.{url, pairingCodeFile}`, with KyNotes' file rules.
The catalog then follows each release and turns on stage 2 for that app. One issue per
product repo, linking this note. Until each release ships, that app stays on stage 1's
manual pairing.

## Upgrade gate

A verified deposit is a verified pre-upgrade backup for that app. Once stage 2 records a
deposit newer than the app's last change, `upgrade` can accept it for `migrates_data` instead
of refusing with `backup_required`. That would unblock kyPulse 0.4.3.1 (held back in #111) on
Kubernetes. This is a separate change: listed here, not decided.

## Open questions

Questions 1 and 2 are answered by the `status --json` contract in "Stage 2 as built"; the
command does not exist yet.

1. **Ceremony state without a browser.** KyRecovery's public API needs a viewer session. Is
   there a CLI or unauthenticated endpoint that reports whether a recovery key is imported?
   If not, KyRecovery needs `kyrecovery status --json` (key imported, paired services, last
   backup per service). That command would also answer question 2.
2. **Deposit check without a browser.** `pair list` does not show `last_backup_at`. Either
   the same `status --json` or a `pair list --json` that includes it.
3. **CA for a private CA.** Stage 1 relies on KyRecovery's public certificate. A site with a
   private CA also needs the combined bundle mounted in each product (only KyIdentity
   documents `SSL_CERT_DIR`).
4. **Deposit interval.** Should the catalog set each product's interval (for example 24h), or
   leave the products' defaults? KyIdentity defaults to 24h; the others were not compared.
5. **KyDNS.** It is pinned to `false` on purpose, and its repo was not read for this note.

## Not in scope

- Restoring from KyRecovery: that is a KyRecovery restore-start flow.
- kyquickstart's restic `backup` for Kubernetes.
- Third-party apps.
