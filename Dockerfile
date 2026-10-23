# The suite runner: kyquickstart serve beside KyYard (docs/kyyard-runner.compose.yaml). A static
# binary on distroless, no shell: the serve-* commands that handoff and pull-plan run through
# docker exec are subcommands of the same binary.
FROM golang:1.26.10@sha256:672c944cfd07599b5f1bb179869270cdc94c6254e41db4d431259693cb286b82 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
# The release set: release-image.yml passes the tag's version (X.Y.Z or X.Y.Z.N); other
# builds report internal/appversion/VERSION.
ARG VERSION=
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/Busnes-app/kyquickstart/internal/appversion.release=${VERSION}" \
    -o /out/kyquickstart ./cmd/kyquickstart \
 && mkdir -p /out/root/state /out/root/run/kyquickstart \
 && chmod 0700 /out/root/state /out/root/run/kyquickstart \
 && chown 10001:10001 /out/root/state /out/root/run/kyquickstart

FROM gcr.io/distroless/static-debian12@sha256:6447365a6337c3732f412d1b74357b30a633831955b2bc45552b0086be907687
COPY --from=build /out/kyquickstart /usr/local/bin/kyquickstart
# A new named volume copies these: private to uid 10001, which KyYard also runs as. Modes and
# owners are set in the build stage: COPY --chmod does not reach a destination directory on
# every builder, and --chown would also hand /run to 10001.
COPY --from=build /out/root/ /
USER 10001:10001
ENTRYPOINT ["kyquickstart", "serve", "--state", "/state", "--socket", "/run/kyquickstart/runner.sock"]
