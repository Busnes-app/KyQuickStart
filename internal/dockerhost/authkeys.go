package dockerhost

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// RunnerKeyComment starts the comment of the suite runner's authorized_keys line.
const RunnerKeyComment = "kyquickstart-runner "

var runnerKeyRE = regexp.MustCompile(`^restrict ssh-ed25519 [A-Za-z0-9+/]+=* kyquickstart-runner [0-9a-f-]{36}$`)

// keysScript replaces every runner line in the SSH user's authorized_keys with $1 (none when
// empty), keeping all other lines, and publishes the result as a private file by rename.
const keysScript = `set -e
umask 077
d="$HOME/.ssh"
f="$d/authorized_keys"
uid=$(id -u)
mine() { test -n "$(find "$1" -prune -user "$uid" ! -perm -020 ! -perm -002)"; }
if [ -L "$d" ] || { [ -e "$d" ] && ! { [ -d "$d" ] && mine "$d"; }; }; then
  echo "$d is a symlink, not a directory, or not owned and writable only by the SSH user" >&2; exit 1
fi
[ -d "$d" ] || mkdir -m 700 "$d"
if [ -L "$f" ] || { [ -e "$f" ] && ! { [ -f "$f" ] && mine "$f"; }; }; then
  echo "$f is a symlink, not a regular file, or not owned and writable only by the SSH user" >&2; exit 1
fi
t=$(mktemp "$d/.authorized_keys.XXXXXX")
trap 'rm -f "$t"' EXIT
if [ -e "$f" ]; then grep -v -E ' kyquickstart-runner [0-9a-f-]+$' "$f" > "$t" || [ $? -eq 1 ]; fi
[ -z "$1" ] || printf '%s\n' "$1" >> "$t"
if [ -e "$f" ] && cmp -s "$t" "$f"; then exit 0; fi
mv -f "$t" "$f"`

// AuthorizeRunner makes line, a restricted ed25519 key commented "kyquickstart-runner <id>",
// the only runner key the SSH user accepts. An empty line removes every runner key.
func AuthorizeRunner(ctx context.Context, r remote.Runner, line string) error {
	if line != "" && !runnerKeyRE.MatchString(line) {
		return errors.New("runner key line is malformed")
	}
	if _, err := r.Run(ctx, "set -- "+remote.Quote(line)+"\n"+keysScript, nil); err != nil {
		return fmt.Errorf("authorized_keys: %w", err)
	}
	return nil
}
