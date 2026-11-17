package dockerhost

import (
	"context"
	"fmt"
	"path"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// MaxTLSChain bounds a copied certificate chain.
const MaxTLSChain = 64 << 10

// tlsChain is the public chain file below an app's directory, in the ./tls bind directory
// its Compose file mounts read-only.
func tlsChain(root, app string) string { return path.Join(root, app, "tls", "fullchain.pem") }

// ReadTLSChain returns app's copied chain, nil when there is none. A symlink or anything but
// a regular file is refused, and at most MaxTLSChain+1 bytes are read.
func ReadTLSChain(ctx context.Context, r remote.Runner, root, app string) ([]byte, error) {
	f := remote.Quote(tlsChain(root, app))
	cmd := "f=" + f + `; if [ -L "$f" ] || { [ -e "$f" ] && [ ! -f "$f" ]; }; then echo "$f is not a regular file" >&2; exit 1; fi; [ -e "$f" ] || exit 0; head -c ` + fmt.Sprint(MaxTLSChain+1) + ` "$f"`
	b, err := r.Run(ctx, cmd, nil)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", tlsChain(root, app), err)
	}
	if len(b) == 0 {
		return nil, nil
	}
	return b, nil
}

// WriteTLSChain replaces app's copied chain with chain, readable by all (0644) in the app's
// private ./tls directory: a temporary file in that directory renamed over the old one, so
// a run that dies halfway leaves the old file or the new one. The directory is checked as
// every bind directory is, and Docker sees the new file through the directory bind.
func WriteTLSChain(ctx context.Context, r remote.Runner, root, app string, chain []byte) error {
	file := tlsChain(root, app)
	if err := trustedDir(ctx, r, path.Dir(file)); err != nil {
		return err
	}
	cmd := "umask 022; t=$(mktemp " + remote.Quote(path.Join(path.Dir(file), ".kyq.XXXXXX")) + ") && { cat > \"$t\" && chmod 644 \"$t\" && mv -f \"$t\" " + remote.Quote(file) + "; s=$?; rm -f \"$t\"; exit $s; }"
	if _, err := r.Run(ctx, cmd, chain); err != nil {
		return fmt.Errorf("write %s: %w", file, err)
	}
	return nil
}
