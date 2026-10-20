package dockerhost

import (
	"context"
	"errors"
	"fmt"
	"path"
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
	dir := d.a.dir()
	if !d.a.Restoring {
		if err := refuseUnfinishedRestore(ctx, d.a); err != nil {
			return err
		}
	}
	hash := remote.Quote(dir + "/.input-hash")
	// Bind directories too: Docker would create a missing one as root.
	for _, p := range append([]string{dir}, d.a.binds()...) {
		if err := trustedDir(ctx, d.a.Runner, p); err != nil {
			return err
		}
	}
	// Removed first and written last: a deploy that dies halfway is a hash mismatch next
	// run, so check() never reaches `docker compose ps` on a half-written compose.yaml.
	if _, err := d.a.Runner.Run(ctx, "rm -f "+hash, nil); err != nil {
		return fmt.Errorf("remove input hash: %w", err)
	}
	for _, f := range []struct {
		name string
		body []byte
	}{{"compose.yaml", d.a.Catalog.Compose}, {"compose.kyq.yaml", d.override}} {
		cmd := "umask 077; mkdir -p " + remote.Quote(dir) + " && cat > " + remote.Quote(dir+"/"+f.name)
		if _, err := d.a.Runner.Run(ctx, cmd, f.body); err != nil {
			return fmt.Errorf("write %s: %w", f.name, err)
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
	_, err := d.a.Runner.Run(ctx, "umask 077; printf %s "+remote.Quote(d.hash)+" > "+hash, nil)
	return err
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
