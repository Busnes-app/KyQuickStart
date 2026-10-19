#!/bin/sh
# release-binaries.sh VERSION RUNNER_IMAGE OUTDIR
# Builds the release kyquickstart binaries, stamped with VERSION and the runner image they install
# KyYard with, into OUTDIR (which must not exist). The only home of the release -ldflags.
set -eu

die() {
	echo "release-binaries.sh: $*" >&2
	exit 1
}

[ "$#" -eq 3 ] || die "usage: release-binaries.sh VERSION RUNNER_IMAGE OUTDIR"
version=$1 image=$2 out=$3

# grep matches per line, so a value holding a newline could smuggle a matching line past it.
nl='
'
case "$version$image" in *"$nl"*) die "VERSION and RUNNER_IMAGE must be one line" ;; esac
printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' ||
	die "VERSION must be X.Y.Z, got: $version"
printf '%s\n' "$image" | grep -Eq "^ghcr\.io/busnes-app/kyquickstart-runner:$(printf '%s' "$version" | sed 's/\./\\./g')@sha256:[0-9a-f]{64}\$" ||
	die "RUNNER_IMAGE must be ghcr.io/busnes-app/kyquickstart-runner:$version@sha256:<64 lowercase hex>, got: $image"
if [ -e "$out" ] || [ -L "$out" ]; then
	die "$out already exists"
fi

mkdir -- "$out"
out=$(cd -- "$out" && pwd)
cd -- "$(dirname -- "$0")/.."
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
	goos=${target%/*} goarch=${target#*/}
	GOFLAGS='' CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath \
		-ldflags="-s -w -X main.version=$version -X main.runnerImage=$image" \
		-o "$out/kyquickstart-$version-$goos-$goarch" ./cmd/kyquickstart
done
