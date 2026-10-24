package dockerhost

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"go.yaml.in/yaml/v3"
)

// labels marks a deployment as ours, of this release set and catalog version.
func labels(releaseSet, version string) map[string]string {
	l := map[string]string{managed.Label: managed.Value, managed.ReleaseSetLabel: releaseSet}
	if version != "" {
		l[managed.VersionLabel] = version
	}
	return l
}

// EdgeAlias is the name a cloudflare-direct edge's cloudflared reaches app by on its network.
func EdgeAlias(app string) string { return "kyq-" + app }

// overrideFile is compose.kyq.yaml's whole shape: written by override, read back by
// readOverride and rewritten by unmanage, so nothing in it is lost on the way.
type overrideFile struct {
	Services map[string]overrideService `yaml:"services"`
	Networks map[string]edgeNetwork     `yaml:"networks,omitempty"`
}

type overrideService struct {
	Labels   map[string]string        `yaml:"labels"`
	Networks map[string]networkAttach `yaml:"networks,omitempty"`
}

type networkAttach struct {
	Aliases []string `yaml:"aliases,omitempty"`
}

type edgeNetwork struct {
	External bool   `yaml:"external"`
	Name     string `yaml:"name"`
}

// override is compose.kyq.yaml: labels on every service and, with an edge network, that
// network for service "app" beside its default one.
func (a App) override(labels map[string]string) []byte {
	o := overrideFile{Services: map[string]overrideService{}}
	for _, s := range a.Catalog.Services {
		o.Services[s] = overrideService{Labels: labels}
	}
	if a.EdgeNetwork != "" {
		o.Services["app"] = overrideService{Labels: labels, Networks: map[string]networkAttach{
			"default": {}, "kyq-edge": {Aliases: []string{EdgeAlias(a.Name)}}}}
		o.Networks = map[string]edgeNetwork{"kyq-edge": {External: true, Name: a.EdgeNetwork}}
	}
	b, err := yaml.Marshal(o)
	if err != nil {
		panic(err) // plain structs of strings always marshal
	}
	return b
}

type deployStep struct {
	a        App
	override []byte
	hash     string
}

func (d deployStep) ID() string        { return d.a.Name + ".deploy" }
func (d deployStep) InputHash() string { return d.hash }

// check returns why the deployment is not current, or "" if it is.
func (d deployStep) check(ctx context.Context) (string, error) {
	out, err := d.a.Runner.Run(ctx, "cat "+remote.Quote(d.a.dir()+"/.input-hash")+" 2>/dev/null || true", nil)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(out)) != d.hash {
		return "deployed files differ from this release", nil
	}
	cs, err := d.a.ps(ctx)
	if err != nil {
		return "", err
	}
	services := d.a.Catalog.Services
	if d.a.Only != "" {
		services = []string{d.a.Only}
	}
	return notRunning(cs, services), nil
}

func (d deployStep) Inspect(ctx context.Context) (bool, error) {
	why, err := d.check(ctx)
	return why == "", err
}

func (d deployStep) Apply(ctx context.Context) error {
	if !d.a.Restoring {
		if err := refuseUnfinishedRestore(ctx, d.a); err != nil {
			return err
		}
	}
	if err := d.a.writeCompose(ctx, d.override); err != nil {
		return err
	}
	// A missing bind source inside a bind directory would be created as root.
	for _, n := range d.a.Catalog.Nested {
		if _, err := d.a.Runner.Run(ctx, "test -e "+remote.Quote(path.Join(d.a.dir(), n)), nil); err != nil {
			return fmt.Errorf("bind source %s is missing; Docker would create it as root", n)
		}
	}
	if _, err := d.a.Runner.Run(ctx, d.a.compose("pull --quiet"), nil); err != nil {
		return fmt.Errorf("docker compose pull: %w: %w", engine.ErrTransient, err)
	}
	up := "up -d --remove-orphans"
	if d.a.Only != "" {
		up += " " + remote.Quote(d.a.Only)
	}
	if d.a.ForceRecreate {
		up += " --force-recreate"
	}
	if _, err := d.a.Runner.Run(ctx, d.a.compose(up), nil); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}
	_, err := d.a.Runner.Run(ctx, "umask 077; printf %s "+remote.Quote(d.hash)+" > "+remote.Quote(d.a.dir()+"/.input-hash"), nil)
	return err
}

// writeCompose writes compose.yaml and its override in the app's private directory, whose
// bind directories it creates private first: Docker would create a missing one as root.
// The input hash goes first, so a write that dies halfway is a mismatch next run and deploy
// never reaches docker compose ps on a half-written compose.yaml.
func (a App) writeCompose(ctx context.Context, override []byte) error {
	dir := a.dir()
	for _, p := range append([]string{dir}, a.binds()...) {
		if err := trustedDir(ctx, a.Runner, p); err != nil {
			return err
		}
	}
	if _, err := a.Runner.Run(ctx, "rm -f "+remote.Quote(dir+"/.input-hash"), nil); err != nil {
		return fmt.Errorf("remove input hash: %w", err)
	}
	for _, f := range []struct {
		name string
		body []byte
	}{{"compose.yaml", a.Catalog.Compose}, {"compose.kyq.yaml", override}} {
		cmd := "umask 077; mkdir -p " + remote.Quote(dir) + " && cat > " + remote.Quote(dir+"/"+f.name)
		if _, err := a.Runner.Run(ctx, cmd, f.body); err != nil {
			return fmt.Errorf("write %s: %w", f.name, err)
		}
	}
	// Catalog configuration a sidecar reads as its own uid through a file bind: readable by
	// all, inside the private bind directory, replaced atomically and never through a symlink.
	for _, f := range a.files() {
		file := path.Join(dir, f.name)
		if err := trustedDir(ctx, a.Runner, path.Dir(file)); err != nil {
			return err
		}
		cmd := "umask 022; t=$(mktemp " + remote.Quote(path.Join(path.Dir(file), ".kyq.XXXXXX")) + ") && { cat > \"$t\" && chmod 644 \"$t\" && mv -f \"$t\" " + remote.Quote(file) + "; s=$?; rm -f \"$t\"; exit $s; }"
		if _, err := a.Runner.Run(ctx, cmd, f.body); err != nil {
			return fmt.Errorf("write %s: %w", f.name, err)
		}
	}
	return nil
}

// RunInit runs the app's one-shot Compose service svc (a profile service) with env and stdin,
// removing its container afterwards. It writes the app's Compose files first.
func RunInit(ctx context.Context, a App, svc string, env []string, stdin []byte) ([]byte, error) {
	if !slices.Contains(a.Catalog.Init, svc) {
		return nil, fmt.Errorf("%s is not a one-shot service of %s", svc, a.Name)
	}
	if err := a.writeCompose(ctx, a.override(labels(a.ReleaseSet, a.Catalog.Version))); err != nil {
		return nil, err
	}
	args := "run --rm -T"
	for _, e := range env {
		args += " -e " + remote.Quote(e)
	}
	out, err := a.Runner.Run(ctx, a.compose(args+" "+remote.Quote(svc)), stdin)
	if err != nil {
		return out, fmt.Errorf("docker compose run %s: %w", svc, err)
	}
	return out, nil
}

// WriteConfig atomically publishes target-only configuration in an owned directory.
func WriteConfig(ctx context.Context, r remote.Runner, file string, body []byte) error {
	if !path.IsAbs(file) || path.Clean(file) != file || strings.ContainsAny(file, "\x00\r\n") {
		return errors.New("target config requires a clean absolute path")
	}
	if err := trustedDir(ctx, r, path.Dir(file)); err != nil {
		return err
	}
	cmd := "umask 077; rm -f " + remote.Quote(path.Join(path.Dir(file), ".input-hash")) +
		" && t=$(mktemp " + remote.Quote(path.Join(path.Dir(file), ".config.XXXXXX")) + ")" +
		" && { cat > \"$t\" && mv -f \"$t\" " + remote.Quote(file) + "; s=$?; rm -f \"$t\"; exit $s; }"
	if _, err := r.Run(ctx, cmd, body); err != nil {
		return fmt.Errorf("write target config: %w", err)
	}
	return nil
}

func (d deployStep) Verify(ctx context.Context) error {
	why, err := d.check(ctx)
	if err != nil {
		return err
	}
	if why != "" {
		return errors.New(why)
	}
	return nil
}
