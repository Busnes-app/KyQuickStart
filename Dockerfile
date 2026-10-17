# The suite runner: kyquickstart serve beside KyYard (docs/kyyard-runner.compose.yaml). A static
# binary on distroless, no shell: the serve-* commands that handoff and pull-plan run through
# docker exec are subcommands of the same binary.
FROM golang:1.26.6@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
# The release set; release-image.yml passes the tag's version.
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/kyquickstart ./cmd/kyquickstart \
 && mkdir /out/state /out/socket

FROM gcr.io/distroless/static-debian12@sha256:6447365a6337c3732f412d1b74357b30a633831955b2bc45552b0086be907687
COPY --from=build /out/kyquickstart /usr/local/bin/kyquickstart
# A new named volume copies these: private to uid 10001, which KyYard also runs as.
COPY --from=build --chown=10001:10001 --chmod=0700 /out/state /state
COPY --from=build --chown=10001:10001 --chmod=0700 /out/socket /run/kyquickstart
USER 10001:10001
ENTRYPOINT ["kyquickstart", "serve", "--state", "/state", "--socket", "/run/kyquickstart/runner.sock"]
