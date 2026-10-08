package kube

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
)

// App is one catalog app placed on one cluster.
type App struct {
	Name       string
	Client     *Client
	Catalog    catalog.App
	ReleaseSet string
	Redact     *engine.Redactor
}

func hashOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Steps returns the app's install steps: secrets, deploy, health.
func Steps(a App) []engine.Step {
	hash := render(a.Name, a.Catalog, a.ReleaseSet).hash()
	return []engine.Step{secretsStep{a}, deployStep{a: a, hash: hash}, healthStep{a: a, hash: hash}}
}
