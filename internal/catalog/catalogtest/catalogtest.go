// Package catalogtest holds test helpers for the embedded catalog. It is imported only by
// tests.
package catalogtest

import (
	"io/fs"
	"path"
	"regexp"
	"testing/fstest"
)

// Version and Digest stand in for a Ky app's release pin in Unpinned's copy. Version is
// newer than any min_from or no_skip, so the upgrade rules stay valid.
const (
	Version = "999.0.0"
	Digest  = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
)

var (
	versionLine = regexp.MustCompile(`(?m)^version: (\S+)$`)
	// A whole images: entry, "  key: ghcr.io/busnes-app/name:version@digest".
	kyImage = regexp.MustCompile(`(?m)^( +[a-z0-9-]+: )ghcr\.io/busnes-app/([a-z0-9-]+):([0-9.]+)@sha256:[0-9a-f]{64}$`)
)

// Unpinned copies fsys with every Ky app's release pin (its version and the
// ghcr.io/busnes-app image tagged with it) replaced by Version and Digest, so golden tests survive the automated pin bumps
// (scripts/catalog-follow) and still catch every other change to what an app deploys.
// Other apps are copied unchanged.
func Unpinned(fsys fs.FS) fs.FS {
	out := fstest.MapFS{}
	fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		if path.Base(p) == "manifest.yaml" {
			b = unpin(b)
		}
		out[p] = &fstest.MapFile{Data: b}
		return nil
	})
	return out
}

func unpin(b []byte) []byte {
	m := versionLine.FindSubmatch(b)
	if m == nil {
		return b
	}
	version := string(m[1])
	pinned := false
	s := kyImage.ReplaceAllStringFunc(string(b), func(ref string) string {
		m := kyImage.FindStringSubmatch(ref)
		if m[3] != version {
			return ref
		}
		pinned = true
		return m[1] + "ghcr.io/busnes-app/" + m[2] + ":" + Version + "@" + Digest
	})
	if !pinned {
		return b
	}
	return []byte(versionLine.ReplaceAllString(s, "version: "+Version))
}
