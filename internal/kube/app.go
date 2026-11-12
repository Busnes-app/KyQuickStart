package kube

import (
	"context"
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
	// Edges names the edge apps on this cluster, whose pods may reach this app's ports. More
	// than one only while an NPM is being replaced by the nginx edge.
	Edges []string
	// Upstreams are the addresses off the cluster the edge app's routes forward to, which its
	// egress admits; edge app only.
	Upstreams []netip.AddrPort
	// TLSSecrets are the Secrets the nginx edge's pods mount certificates from; edge_proxy only.
	TLSSecrets []string
	// PublicEgress lets an app with egress "edge" also reach public addresses on 443: behind a
	// tunnel edge, NPM answers external routes only to the connector, so the app reaches them
	// the way a browser does. Private, shared, link-local and loopback ranges stay refused.
	PublicEgress bool
	// RunnerImage is the suite runner sidecar's image, for an app with kubernetes.suite_runner.
	RunnerImage string
	// SecretFrom reads secrets copied from another target rather than generated here, by key.
	SecretFrom map[string]func(context.Context) ([]byte, error)
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
