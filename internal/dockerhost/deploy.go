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

// override is compose.kyq.yaml: labels on every service.
func override(services []string, labels map[string]string) []byte {
	svcs := map[string]any{}
	for _, s := range services {
		svcs[s] = map[string]any{"labels": labels}
	}
	b, err := yaml.Marshal(map[string]any{"services": svcs})
	if err != nil {
		panic(err) // maps of strings always marshal
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
	return nil
}

// RunInit runs the app's one-shot Compose service svc (a profile service) with env and stdin,
// removing its container afterwards. It writes the app's Compose files first.
func RunInit(ctx context.Context, a App, svc string, env []string, stdin []byte) ([]byte, error) {
	if !slices.Contains(a.Catalog.Init, svc) {
		return nil, fmt.Errorf("%s is not a one-shot service of %s", svc, a.Name)
	}
	if err := a.writeCompose(ctx, override(a.Catalog.Services, labels(a.ReleaseSet, a.Catalog.Version))); err != nil {
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
