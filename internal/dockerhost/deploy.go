package dockerhost

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"go.yaml.in/yaml/v3"
)

const (
	ManagedLabel    = "ky.managed-by"
	ManagedValue    = "kyquickstart"
	ReleaseSetLabel = "ky.release-set"
)

// override is compose.kyq.yaml: the managed labels on every service.
func override(services []string, releaseSet string) []byte {
	labels := map[string]string{ManagedLabel: ManagedValue, ReleaseSetLabel: releaseSet}
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
	return notRunning(cs, d.a.Catalog.Services), nil
}

func (d deployStep) Inspect(ctx context.Context) (bool, error) {
	why, err := d.check(ctx)
	return why == "", err
}

func (d deployStep) Apply(ctx context.Context) error {
	dir := d.a.dir()
	hash := remote.Quote(dir + "/.input-hash")
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
	if _, err := d.a.Runner.Run(ctx, d.a.compose("up -d --remove-orphans"), nil); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}
	_, err := d.a.Runner.Run(ctx, "umask 077; printf %s "+remote.Quote(d.hash)+" > "+hash, nil)
	return err
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
