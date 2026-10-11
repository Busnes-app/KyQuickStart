package remote

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

// ReadSecret reads an existing mode-0600 regular file, refusing symlinks and unsafe
// ancestors. It is read-only; error output and partial stdout never escape this boundary.
func ReadSecret(ctx context.Context, r Runner, file string) ([]byte, error) {
	if !path.IsAbs(file) || path.Clean(file) != file || strings.ContainsAny(file, "\x00\r\n") {
		return nil, errors.New("secret file must be a clean absolute path")
	}
	cmd := fmt.Sprintf(`f=%s
uid=$(id -u)
test -n "$(find "$f" -prune -type f -user "$uid" -perm 600)" || exit 1
d=$(dirname "$f")
real=$(cd "$d" && pwd -P) || exit 1
[ "$d" = "$real" ] || exit 1
while :; do
  test -n "$(find "$d" -prune -type d \( -user 0 -o -user "$uid" \) \( \( ! -perm -020 ! -perm -002 \) -o -perm -1000 \))" || exit 1
  [ "$d" = / ] && break
  d=$(dirname "$d")
done
test "$(wc -c < "$f")" -le 16384 || exit 1
cat "$f"`, Quote(file))
	b, err := r.Run(ctx, cmd, nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("target credential is missing, symlinked, not mode 600, or has an unsafe owner/path")
	}
	if len(b) == 0 || len(b) > 16384 {
		return nil, errors.New("target credential is empty or oversized")
	}
	return b, nil
}
