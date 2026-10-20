# The offboarding bridge: a static binary on distroless, no shell. Build from the repo root:
#   docker build -f cmd/kyoffboard/Dockerfile .
# kyoffboard-image.yml publishes it; the base digest is the index from
#   docker buildx imagetools inspect gcr.io/distroless/static-debian12:nonroot
FROM golang:1.26.6@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kyoffboard ./cmd/kyoffboard

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/Busnes-app/KyQuickStart" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/kyoffboard /usr/local/bin/kyoffboard
USER 65532:65532
ENTRYPOINT ["kyoffboard"]
