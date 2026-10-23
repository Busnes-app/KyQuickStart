// Package appversion is the one answer to "which version is this?": the release set.
//
// VERSION holds the release line, X.Y.Z, and is the only file a human bumps.
// Automated dependency releases add a fourth number (X.Y.Z.N) without a commit:
// release-image.yml passes the release tag's version to the image build and to
// scripts/release-binaries.sh, which set release through -ldflags. Source and CI
// builds report the release line.
package appversion

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var line string

// release is set at release build time with
// -ldflags "-X github.com/Busnes-app/kyquickstart/internal/appversion.release=X.Y.Z[.N]".
var release string

// String is the release set: printed by version and recorded on every deployment.
func String() string {
	if release != "" {
		return release
	}
	return strings.TrimSpace(line)
}
