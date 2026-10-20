# The catalog's Immich server: upstream v3.2.0's amd64 image with this module's server dist.
# Build context: the patched source's server directory after `pnpm --filter immich build`.
# immich-image.yml publishes it as ghcr.io/busnes-app/kyq-immich-server:3.2.0-ky.<n>.
FROM ghcr.io/immich-app/immich-server@sha256:949bbf9ff32543539f471bc275437e6d1a1a4fee43c55bb9e4c06b946909bf1c
ARG KYQ_SOURCE_PATCH_SHA256
ARG VERSION=dev
ARG REVISION=unknown
ARG SOURCE_TAG=unknown
LABEL kyq.immich-access.contract="ky.immich-access/1" \
      kyq.immich-access.source="1b6098c9dbfffe978bec2d414606ed7a4c8e019a" \
      kyq.immich-access.patch-sha256="$KYQ_SOURCE_PATCH_SHA256" \
      org.opencontainers.image.source="https://github.com/Busnes-app/KyQuickStart" \
      org.opencontainers.image.revision="$REVISION" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
# The AGPL source offer /api/server/about shows: this repository at the image's tag.
ENV IMMICH_BUILD="" \
    IMMICH_BUILD_URL="" \
    IMMICH_BUILD_IMAGE="$VERSION" \
    IMMICH_BUILD_IMAGE_URL="https://github.com/Busnes-app/KyQuickStart/pkgs/container/kyq-immich-server" \
    IMMICH_REPOSITORY="Busnes-app/KyQuickStart" \
    IMMICH_REPOSITORY_URL="https://github.com/Busnes-app/KyQuickStart" \
    IMMICH_SOURCE_REF="$SOURCE_TAG" \
    IMMICH_SOURCE_COMMIT="$REVISION" \
    IMMICH_SOURCE_URL="https://github.com/Busnes-app/KyQuickStart/tree/$SOURCE_TAG/internal/adapters/immich/module"
COPY dist /usr/src/app/server/dist
