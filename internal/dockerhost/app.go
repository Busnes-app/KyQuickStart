// Package dockerhost installs apps on a Docker host over a remote.Runner.
package dockerhost

import (
	"crypto/sha256"
	"encoding/hex"
	"path"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// App is one catalog app placed on one Docker host.
type App struct {
	Name       string
	Root       string // the target's installer root
	Runner     remote.Runner
	Catalog    catalog.App
	ReleaseSet string
	Redact     *engine.Redactor
}

func (a App) dir() string { return path.Join(a.Root, a.Name) }

func hashOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
