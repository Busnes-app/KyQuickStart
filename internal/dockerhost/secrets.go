package dockerhost

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"path"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// secretsStep creates each declared secret once, on the target, and never overwrites it.
type secretsStep struct{ a App }

func (s secretsStep) ID() string        { return s.a.Name + ".secrets" }
func (s secretsStep) InputHash() string { return hashOf(s.a.Catalog.Secrets...) }

func (s secretsStep) file(name string) string { return path.Join(s.a.dir(), "secrets", name) }

func (s secretsStep) exists(ctx context.Context, name string) (bool, error) {
	out, err := s.a.Runner.Run(ctx, "if test -e "+remote.Quote(s.file(name))+"; then echo yes; fi", nil)
	return strings.TrimSpace(string(out)) == "yes", err
}

func (s secretsStep) Inspect(ctx context.Context) (bool, error) {
	for _, n := range s.a.Catalog.Secrets {
		if ok, err := s.exists(ctx, n); err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (s secretsStep) Apply(ctx context.Context) error {
	for _, n := range s.a.Catalog.Secrets {
		ok, err := s.exists(ctx, n)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		b := make([]byte, 32)
		rand.Read(b)
		v := base64.RawURLEncoding.EncodeToString(b)
		s.a.Redact.Add(v)
		f := s.file(n)
		// set -C: the write fails rather than replace a secret that appeared meanwhile.
		cmd := "umask 077; set -C; mkdir -p " + remote.Quote(path.Dir(f)) + " && cat > " + remote.Quote(f)
		if _, err := s.a.Runner.Run(ctx, cmd, []byte(v)); err != nil {
			return fmt.Errorf("write secret %s: %w", n, err)
		}
	}
	return nil
}

// Verify reads every secret back, which also registers it for redaction on re-runs.
func (s secretsStep) Verify(ctx context.Context) error {
	for _, n := range s.a.Catalog.Secrets {
		f := remote.Quote(s.file(n))
		out, err := s.a.Runner.Run(ctx, `test -n "$(find `+f+` -prune -type f -perm 600)" && cat `+f, nil)
		if err != nil {
			return fmt.Errorf("secret %s is missing or not mode 600: %w", n, err)
		}
		if len(out) == 0 {
			return fmt.Errorf("secret %s is empty", n)
		}
		s.a.Redact.Add(string(out))
	}
	return nil
}
