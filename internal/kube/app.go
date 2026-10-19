package kube

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"

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
	// StorageClass is the target's storage_class; empty leaves claims to the cluster default.
	StorageClass string
	// Edge names the edge app on this cluster, whose pods may reach this app's ports; empty for none.
	Edge string
	// Upstreams are the addresses off the cluster the edge app's routes forward to, which its
	// egress admits; edge app only.
	Upstreams []netip.AddrPort
	// RunnerImage is the suite runner sidecar's image, for an app with kubernetes.suite_runner.
	RunnerImage string
}

func hashOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Steps returns the app's install steps: secrets, deploy, health, and for NPM its bootstrap.
func Steps(a App) []engine.Step {
	hash := render(a).hash()
	steps := []engine.Step{secretsStep{a}, deployStep{a: a, hash: hash}, healthStep{a: a, hash: hash}}
	if a.Name == "npm" && a.Catalog.Category == "edge" {
		steps = append(steps, npmBootstrapStep{a})
	}
	return steps
}
