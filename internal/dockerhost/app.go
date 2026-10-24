// Package dockerhost installs apps on a Docker host over a remote.Runner.
package dockerhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// ComposeEnv precedes every docker compose command.
const ComposeEnv = "KYQ_UID=$(id -u) KYQ_GID=$(id -g)"

// App is one catalog app placed on one Docker host.
type App struct {
	Name          string
	Root          string // the target's installer root
	Runner        remote.Runner
	Catalog       catalog.App
	ReleaseSet    string
	Redact        *engine.Redactor
	ForceRecreate bool // connector credential replacement requires a fresh process
	// EdgeNetwork is a cloudflare-direct edge's Docker network, which service "app" joins.
	EdgeNetwork string
	// Only, when set, is the one service deploy starts: restore starts its services in order.
	Only string
	// Restoring is set by restore alone: deploy refuses an app with an unfinished restore
	// (.restore-pending), whose removal replay has not been delivered.
	Restoring bool
}

func (a App) dir() string { return path.Join(a.Root, a.Name) }

// binds are the paths of the app's bind directories.
func (a App) binds() []string {
	var out []string
	for _, b := range a.Catalog.Binds {
		out = append(out, path.Join(a.dir(), b))
	}
	return out
}

// catalogFile is one of the catalog's files/ that the app's Compose file binds.
type catalogFile struct {
	name string // below the app's directory
	body []byte
}

// files are the catalog files the app's Compose file binds, in Nested order.
func (a App) files() []catalogFile {
	var out []catalogFile
	for _, n := range a.Catalog.Nested {
		name := strings.TrimPrefix(n, "./")
		if b, ok := a.Catalog.Files[name]; ok {
			out = append(out, catalogFile{name, b})
		}
	}
	return out
}

func hashOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Steps returns the app's install steps: secrets (when it declares any), deploy, health.
func Steps(a App) []engine.Step {
	ov := a.override(labels(a.ReleaseSet, a.Catalog.Version))
	parts := []string{string(a.Catalog.Compose), string(ov)}
	for _, f := range a.files() {
		parts = append(parts, f.name, string(f.body))
	}
	hash := hashOf(parts...)
	steps := []engine.Step{deployStep{a: a, override: ov, hash: hash}, healthStep{a: a, hash: hash}}
	if len(a.Catalog.Secrets) > 0 {
		steps = append([]engine.Step{secretsStep{a}}, steps...)
	}
	return steps
}

// RestoreSteps are the app's secrets and a deploy that starts only service first.
func RestoreSteps(a App, first string) []engine.Step {
	a.Only, a.Restoring = first, true
	steps := Steps(a)
	return steps[:len(steps)-1]
}

// compose runs docker compose for this app's project from its directory. A Compose file
// may run a service as the target user, ${KYQ_UID}:${KYQ_GID}, who owns its secrets and
// bind directories.
func (a App) compose(args string) string {
	return "cd " + remote.Quote(a.dir()) + " && " + ComposeEnv + " docker compose -p " + remote.Quote("kyq-"+a.Name) +
		" -f compose.yaml -f compose.kyq.yaml " + args
}

func (a App) ps(ctx context.Context) ([]container, error) {
	out, err := a.Runner.Run(ctx, a.compose("ps --all --format json"), nil)
	if err != nil {
		return nil, fmt.Errorf("docker compose ps: %w", err)
	}
	return parsePS(out)
}
