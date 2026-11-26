package edge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// Releasing hands an app's routes on an edge to the operator as they serve: its NPM hosts and
// nginx-edge files lose this installation's marks and keep their configuration and state,
// and its LAN DNS records leave the ledger. Nothing is disabled, renamed or deleted, and no
// later apply touches them: they are foreign from then on (unmanage of an app the catalog
// lacks).

// releasedLine replaces a released host's or file's marker line, so its origin stays legible.
func releasedLine(installation, app string) string {
	return "# released by kyquickstart installation " + installation + " (" + app + "): managed by hand from now on\n"
}

// appHostnames is the set of app's route hostnames on e.
func appHostnames(e stack.Edge, app string) map[string]bool {
	out := map[string]bool{}
	for _, r := range e.Routes {
		if r.App == app {
			out[r.Hostname] = true
		}
	}
	return out
}

func releaseHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

// Release returns the steps that hand app's routes on e to the operator.
func (n *NPM) Release(e stack.Edge, app string) []engine.Step {
	out := []engine.Step{npmRelease{n: n, e: e, app: app}}
	if e.LANDNS != nil {
		out = append(out, dnsRelease{n: n.dns(), e: e, app: app})
	}
	return out
}

type npmRelease struct {
	n   *NPM
	e   stack.Edge
	app string
}

func (s npmRelease) ID() string { return s.e.Target + ".edge.release." + s.app }
func (s npmRelease) InputHash() string {
	return releaseHash(append([]string{"npm", s.n.base, s.n.installation}, slices.Sorted(maps.Keys(appHostnames(s.e, s.app)))...)...)
}

// pending lists the app's hosts still carrying this installation's marks, or this target's
// legacy ones.
func (s npmRelease) pending(ctx context.Context) ([]host, error) {
	var hosts []host
	if err := s.n.request(ctx, http.MethodGet, hostPath, nil, &hosts); err != nil {
		return nil, err
	}
	names := appHostnames(s.e, s.app)
	var out []host
	for _, h := range hosts {
		if len(h.Domains) == 1 && names[h.Domains[0]] && s.n.claim(h, s.e, s.app+":") != foreign {
			out = append(out, h)
		}
	}
	return out, nil
}

func (s npmRelease) Inspect(ctx context.Context) (bool, error) {
	hosts, err := s.pending(ctx)
	return len(hosts) == 0, err
}

// Apply rewrites only the marker line and the ownership meta; NPM keeps the host enabled or
// disabled as it was, with the same forward fields, certificate and locations.
func (s npmRelease) Apply(ctx context.Context) error {
	hosts, err := s.pending(ctx)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		_, rest, _ := strings.Cut(h.Advanced, "\n")
		meta := maps.Clone(h.Meta)
		if meta == nil {
			meta = map[string]any{}
		}
		// Empty, not absent: NPM may merge meta on update, and an empty value is never ours.
		for _, k := range []string{"ky.managed-by", "ky.target", "ky.installation"} {
			meta[k] = ""
		}
		release := map[string]any{"advanced_config": releasedLine(s.n.installation, s.app) + rest, "meta": meta}
		if err := s.n.request(ctx, http.MethodPut, fmt.Sprintf("%s/%d", hostPath, h.ID), release, nil); err != nil {
			return fmt.Errorf("release %s: %w", h.Domains[0], err)
		}
	}
	return nil
}

func (s npmRelease) Verify(ctx context.Context) error {
	hosts, err := s.pending(ctx)
	if err == nil && len(hosts) > 0 {
		err = fmt.Errorf("NPM host %s still carries this installation's marks", hosts[0].Domains[0])
	}
	return err
}

// Release returns the steps that hand app's routes on e to the operator.
func (n *Nginx) Release(e stack.Edge, app string) []engine.Step {
	out := []engine.Step{nginxRelease{n: n, e: e, app: app}}
	if e.LANDNS != nil {
		out = append(out, dnsRelease{n: n.dns(), e: e, app: app})
	}
	return out
}

type nginxRelease struct {
	n   *Nginx
	e   stack.Edge
	app string
}

func (s nginxRelease) ID() string { return s.e.Target + ".edge.release." + s.app }
func (s nginxRelease) InputHash() string {
	return releaseHash(append([]string{"nginx", s.n.installation}, slices.Sorted(maps.Keys(appHostnames(s.e, s.app)))...)...)
}

// pending lists the app's files, enabled or disabled, that still start with this
// installation's marker.
func (s nginxRelease) pending(files map[string]string) []string {
	mark := s.n.prefix(s.e) + s.app + ":"
	var out []string
	for name := range appHostnames(s.e, s.app) {
		for _, ext := range []string{enabledExt, disabledExt} {
			if f := routeKey(name) + ext; strings.HasPrefix(files[f], mark) {
				out = append(out, f)
			}
		}
	}
	slices.Sort(out)
	return out
}

func (s nginxRelease) Inspect(ctx context.Context) (bool, error) {
	files, _, err := s.n.c.EdgeFiles(ctx)
	return len(s.pending(files)) == 0, err
}

// Apply replaces each file's first line and keeps its name, so an enabled route stays
// enabled; the pods load it by reload, as for any change.
func (s nginxRelease) Apply(ctx context.Context) error {
	files, version, err := s.n.c.EdgeFiles(ctx)
	if err != nil {
		return err
	}
	pending := s.pending(files)
	if len(pending) == 0 {
		return nil
	}
	for _, f := range pending {
		_, rest, _ := strings.Cut(files[f], "\n")
		files[f] = releasedLine(s.n.installation, s.app) + rest
	}
	if err := s.n.c.SetEdgeFiles(ctx, files, version); err != nil {
		return err
	}
	return s.n.await(ctx, files)
}

func (s nginxRelease) Verify(ctx context.Context) error {
	files, _, err := s.n.c.EdgeFiles(ctx)
	if err != nil {
		return err
	}
	if left := s.pending(files); len(left) > 0 {
		return fmt.Errorf("nginx edge file %s still carries this installation's marker", left[0])
	}
	return nil
}

// dnsRelease drops the app's records from the LAN DNS ledger; KyDNS itself is not called, so
// the records stay exactly as they are and become the operator's.
type dnsRelease struct {
	n   lanDNS
	e   stack.Edge
	app string
}

func (s dnsRelease) ID() string { return s.e.Target + ".edge.lan-dns.release." + s.app }
func (s dnsRelease) InputHash() string {
	return releaseHash(append([]string{"lan-dns", s.n.installation}, slices.Sorted(maps.Keys(appHostnames(s.e, s.app)))...)...)
}

// ledger reads the ledger and splits off the app's entries.
func (s dnsRelease) ledger(ctx context.Context) (keep, release []ledgerEntry, err error) {
	stored, err := s.n.Secrets.LANDNSLedger(ctx)
	if err != nil {
		return nil, nil, err
	}
	var ledger []ledgerEntry
	if len(stored) > 0 && json.Unmarshal(stored, &ledger) != nil {
		return nil, nil, errors.New("the LAN DNS ledger in kyq-edge/kyq-credentials is unreadable; fix or remove its kydns_records key")
	}
	names := map[string]bool{}
	for h := range appHostnames(s.e, s.app) {
		names[fqdn(h)] = true
	}
	for _, en := range ledger {
		if names[en.Name] {
			release = append(release, en)
		} else {
			keep = append(keep, en)
		}
	}
	return keep, release, nil
}

func (s dnsRelease) Inspect(ctx context.Context) (bool, error) {
	_, release, err := s.ledger(ctx)
	return len(release) == 0, err
}

func (s dnsRelease) Apply(ctx context.Context) error {
	keep, release, err := s.ledger(ctx)
	if err != nil || len(release) == 0 {
		return err
	}
	if keep == nil {
		keep = []ledgerEntry{}
	}
	b, _ := json.Marshal(keep)
	return s.n.Secrets.SetLANDNSLedger(ctx, b)
}

func (s dnsRelease) Verify(ctx context.Context) error {
	_, release, err := s.ledger(ctx)
	if err == nil && len(release) > 0 {
		err = fmt.Errorf("the LAN DNS ledger still lists %s", strings.TrimSuffix(release[0].Name, "."))
	}
	return err
}
