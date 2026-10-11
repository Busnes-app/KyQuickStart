# BookStack OIDC test gateway

## Purpose

Provide disposable TLS termination and signed fixture or native KyIdentity routing for BookStack qualification.

## Ownership

- This directory owns the static Go fixture executable.
- The parent owns container/network lifetimes, account provisioning and acceptance assertions.

## Local Contracts

- Run only in a nonce-owned disposable Docker network with loopback-only host publication.
- Signing/TLS private keys stay in memory. The shared fixture directory contains only
  the executable, public certificate and non-secret PHP trust configuration.
- Authorize only the exact fixture client/redirect. Consume each code once and require PKCE S256.
- Fake actor cookies deliberately choose test identities; this program provides no production auth.
- Synthetic signed cases bind the existing daily subject while varying administrator,
  missing/empty, unknown, duplicate, mixed-type and scalar role claims. The parent verifies
  native refusal/ordinary access and counts one PKCE exchange per attempted authorization.
- Native mode forwards the issuer prefix to only the nonce-owned KyIdentity backend; its
  real discovery, authorization, token, session and CSRF endpoints retain authentication.
- The reverse proxy performs TLS transport only; BookStack validates OIDC and authorizes requests.

## Work Guidance

Use Go's existing standard-library TLS, RSA, HTTP and reverse-proxy implementation.

## Verification

- The parent signed-OIDC fixture verifies the provider through the native consumer.
- The same fixture with an explicit KyIdentity binary verifies the actual provider path.

## Child DOX Index

None.
