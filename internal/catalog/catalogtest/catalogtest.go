// Package catalogtest is the catalog tests install from: the embedded one plus the entries in
// ../pending, whose Busnes images are not published yet (H1). Only tests import it.
package catalogtest

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing/fstest"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
)

// Digest stands in for a pending image's sha256:pending; no image has it.
const Digest = "sha256:00000000000000000000000000000000000000000000000000000000000000fa"

// FS is the embedded catalog plus pending/, each sha256:pending replaced by Digest. Once
// immich moves to apps/ with its attested pins, this is catalog.Embedded().
func FS() fs.FS {
	m := fstest.MapFS{}
	copyTree := func(fsys fs.FS, pending bool) {
		err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := fs.ReadFile(fsys, p)
			if pending {
				b = []byte(strings.ReplaceAll(string(b), "@sha256:pending", "@"+Digest))
			}
			m[p] = &fstest.MapFile{Data: b}
			return err
		})
		if err != nil {
			panic(err)
		}
	}
	copyTree(catalog.Embedded(), false)
	_, self, _, _ := runtime.Caller(0) // pending/ is source, not embedded
	copyTree(os.DirFS(filepath.Join(filepath.Dir(self), "..", "pending")), true)
	return m
}
