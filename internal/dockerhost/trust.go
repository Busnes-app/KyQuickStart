package dockerhost

import (
	"context"
	"fmt"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// trustedDir creates dir if missing and fails unless only this user can change it or its
// path: a directory another user controls lets them swap compose.yaml before `docker
// compose up`. Any symlink in the path is refused, since every hop's directory would need
// checking and could be re-pointed after the check. Every ancestor must be owned by root or
// this user (its owner can always replace entries) and closed to group and others unless
// sticky.
func trustedDir(ctx context.Context, r remote.Runner, dir string) error {
	cmd := fmt.Sprintf(`d=%s; umask 077; mkdir -p "$d" || exit 1
uid=$(id -u)
real=$(cd "$d" && pwd -P) || exit 1
[ "$real" = "$d" ] || { echo "$d goes through a symlink; configure the real path $real" >&2; exit 1; }
test -n "$(find "$d" -prune -type d -user "$uid" ! -perm -020 ! -perm -002)" || { echo "$d is not a directory owned by this user and closed to group and others" >&2; exit 1; }
p=$d
while [ "$p" != / ]; do
  p=$(dirname "$p")
  test -n "$(find "$p" -prune \( -user 0 -o -user "$uid" \) \( \( ! -perm -020 ! -perm -002 \) -o -perm -1000 \))" || { echo "$p is owned by another user or writable by others, so $d is not safe" >&2; exit 1; }
done`, remote.Quote(dir))
	if _, err := r.Run(ctx, cmd, nil); err != nil {
		return fmt.Errorf("untrusted directory: %w", err)
	}
	return nil
}
