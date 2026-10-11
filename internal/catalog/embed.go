package catalog

import (
	"embed"
	"io/fs"
)

//go:embed all:apps
var embedded embed.FS

//go:embed demo/hello/*
var demo embed.FS

// Demo ships only the disposable whoami fixture, never suite product substitutes.
func Demo() fs.FS {
	sub, err := fs.Sub(demo, "demo")
	if err != nil {
		panic(err)
	}
	return sub
}

// Embedded is the catalog shipped in this binary: one directory per app.
func Embedded() fs.FS {
	sub, err := fs.Sub(embedded, "apps")
	if err != nil {
		panic(err) // "apps" is a valid path; fs.Sub fails only on invalid ones
	}
	return sub
}
