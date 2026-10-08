package dockerhost

import (
	"context"
	"fmt"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// trustedDir creates dir if missing and fails unless only this user can change it or its
// path: a directory another user controls lets them swap compose.yaml before `docker
// compose up`. Ancestors are checked for writability only; another user who owns an
// ancestor without being able to write to it is out of scope.
func trustedDir(ctx context.Context, r remote.Runner, dir string) error {
	cmd := fmt.Sprintf(`d=%s; umask 077; mkdir -p "$d" || exit 1
test -n "$(find "$d" -prune -type d -user "$(id -u)" ! -perm -020 ! -perm -002)" || { echo "$d is not a real directory owned by this user and closed to group and others" >&2; exit 1; }
p=$(cd "$d" && pwd -P) || exit 1
while [ "$p" != / ]; do
  p=$(dirname "$p")
  test -n "$(find "$p" -prune \( \( ! -perm -020 ! -perm -002 \) -o -perm -1000 \))" || { echo "$p is writable by others (no sticky bit), so $d is not safe" >&2; exit 1; }
done`, remote.Quote(dir))
	if _, err := r.Run(ctx, cmd, nil); err != nil {
		return fmt.Errorf("untrusted directory: %w", err)
	}
	return nil
}
