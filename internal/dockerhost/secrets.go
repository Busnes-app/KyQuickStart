package dockerhost

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// secretLen is 32 random bytes in unpadded base64url.
const secretLen = 43

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
	if !s.a.Restoring {
		if err := refuseUnfinishedRestore(ctx, s.a); err != nil {
			return false, err
		}
	}
	for _, n := range s.a.Catalog.Secrets {
		if ok, err := s.exists(ctx, n); err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (s secretsStep) Apply(ctx context.Context) error {
	for _, d := range []string{s.a.dir(), path.Join(s.a.dir(), "secrets")} {
		if err := trustedDir(ctx, s.a.Runner, d); err != nil {
			return err
		}
	}
	for _, n := range s.a.Catalog.Secrets {
		ok, err := s.exists(ctx, n)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		if err := generateSecret(ctx, s.a.Runner, s.file(n)); err != nil {
			return fmt.Errorf("generate target secret %s: %w", n, err)
		}
	}
	return nil
}

// generateSecret writes 32 random bytes from the target's /dev/urandom to file as unpadded
// base64url, once: it publishes only a complete private file, and ln refuses replacement.
// The trap removes both random and encoded scratch files.
func generateSecret(ctx context.Context, r remote.Runner, f string) error {
	dir := path.Dir(f)
	cmd := fmt.Sprintf(`umask 077
f=%s
t=$(mktemp %s) || exit 1
raw=
trap 'rm -f "$t"; [ -z "$raw" ] || rm -f "$raw"' 0
trap 'exit 1' HUP INT TERM
raw=$(mktemp %s) || exit 1
dd if=/dev/urandom of="$raw" bs=32 count=1 2>/dev/null || exit 1
[ "$(wc -c < "$raw")" -eq 32 ] || exit 1
base64 < "$raw" > "$t" || exit 1
tr '+/' '-_' < "$t" > "$raw" || exit 1
tr -d '=\n' < "$raw" > "$t" || exit 1
[ "$(wc -c < "$t")" -eq 43 ] || exit 1
ln "$t" "$f"`, remote.Quote(f), remote.Quote(path.Join(dir, ".tmp.XXXXXX")), remote.Quote(path.Join(dir, ".random.XXXXXX")))
	if _, err := r.Run(ctx, cmd, nil); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("target refused creation")
	}
	return nil
}

// Verify reads every secret back, which also registers it for redaction on re-runs.
func (s secretsStep) Verify(ctx context.Context) error {
	for _, n := range s.a.Catalog.Secrets {
		out, err := remote.ReadSecret(ctx, s.a.Runner, s.file(n))
		if err != nil {
			return fmt.Errorf("secret %s is missing or not mode 600: %w", n, err)
		}
		s.a.Redact.Add(string(out))
		if len(out) != secretLen {
			return fmt.Errorf("secret %s is %d bytes, want %d", n, len(out), secretLen)
		}
		if _, err := base64.RawURLEncoding.Strict().DecodeString(string(out)); err != nil {
			return fmt.Errorf("secret %s is not canonical unpadded base64url", n)
		}
	}
	return nil
}
