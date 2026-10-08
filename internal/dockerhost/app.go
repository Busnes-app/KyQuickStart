// Package dockerhost installs apps on a Docker host over a remote.Runner.
package dockerhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// Steps returns the app's install steps: secrets, deploy, health.
func Steps(a App) []engine.Step {
	ov := override(a.Catalog.Services, a.ReleaseSet)
	hash := hashOf(string(a.Catalog.Compose), string(ov))
	return []engine.Step{secretsStep{a}, deployStep{a: a, override: ov, hash: hash}, healthStep{a: a, hash: hash}}
}

// compose runs docker compose for this app's project from its directory.
func (a App) compose(args string) string {
	return "cd " + remote.Quote(a.dir()) + " && docker compose -p " + remote.Quote("kyq-"+a.Name) +
		" -f compose.yaml -f compose.kyq.yaml " + args
}

func (a App) ps(ctx context.Context) ([]container, error) {
	out, err := a.Runner.Run(ctx, a.compose("ps --all --format json"), nil)
	if err != nil {
		return nil, fmt.Errorf("docker compose ps: %w", err)
	}
	return parsePS(out)
}
