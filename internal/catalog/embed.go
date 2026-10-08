package catalog

import (
	"embed"
	"io/fs"
)

//go:embed all:apps
var embedded embed.FS

// Embedded is the catalog shipped in this binary: one directory per app.
func Embedded() fs.FS {
	sub, err := fs.Sub(embedded, "apps")
	if err != nil {
		panic(err) // "apps" is a valid path; fs.Sub fails only on invalid ones
	}
	return sub
}
