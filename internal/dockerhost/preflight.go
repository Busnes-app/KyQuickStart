package dockerhost

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// Finding is one read-only preflight check.
type Finding struct {
	Check  string
	OK     bool
	Detail string
}

// Preflight checks a Docker host and changes nothing.
func Preflight(ctx context.Context, r remote.Runner, t stack.Target) []Finding {
	return []Finding{
		versionCheck(ctx, r, "docker engine", "docker version --format '{{.Server.Version}}'", 24, 0),
		versionCheck(ctx, r, "docker compose", "docker compose version --short", 2, 21),
		rootCheck(ctx, r, t.Root),
	}
}

func versionCheck(ctx context.Context, r remote.Runner, check, cmd string, major, minor int) Finding {
	out, err := r.Run(ctx, cmd, nil)
	if err != nil {
		return Finding{check, false, err.Error()}
	}
	v := strings.TrimSpace(string(out))
	parts := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3)
	if len(parts) < 2 {
		return Finding{check, false, fmt.Sprintf("cannot read version %q", v)}
	}
	gotMajor, err1 := strconv.Atoi(parts[0])
	gotMinor, err2 := strconv.Atoi(parts[1])
	switch {
	case err1 != nil || err2 != nil:
		return Finding{check, false, fmt.Sprintf("cannot read version %q", v)}
	case gotMajor < major || gotMajor == major && gotMinor < minor:
		return Finding{check, false, fmt.Sprintf("%s is older than %d.%d", v, major, minor)}
	}
	return Finding{check, true, v}
}

// rootCheck passes when the nearest existing ancestor of root is a writable directory.
func rootCheck(ctx context.Context, r remote.Runner, root string) Finding {
	cmd := "d=" + remote.Quote(root) + `; while [ ! -e "$d" ]; do d=$(dirname "$d"); done; [ -d "$d" ] && [ -w "$d" ]`
	if _, err := r.Run(ctx, cmd, nil); err != nil {
		return Finding{"root", false, root + " cannot be created or written by this user"}
	}
	return Finding{"root", true, root}
}
