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
	f := []Finding{
		// 25.0: healthcheck start_interval (API 1.44), which shortens a restored app's first health.
		versionCheck(ctx, r, "docker engine", "docker version --format '{{.Server.Version}}'", 25, 0),
		versionCheck(ctx, r, "docker compose", "docker compose version --short", 2, 21),
		rootCheck(ctx, r, t.Root),
		secretToolsCheck(ctx, r),
	}
	if t.Backup != nil {
		f = append(f, BackupFilesCheck(ctx, r, *t.Backup))
	}
	return f
}

// BackupFilesCheck passes when the rest-server credential is a 0600 regular file and the CA,
// if any, a regular file, both owned by this user. find does not follow a symlink.
func BackupFilesCheck(ctx context.Context, r remote.Runner, b stack.BackupDestination) Finding {
	cmd := "uid=$(id -u); test -n \"$(find " + remote.Quote(b.CredentialFile) + " -prune -type f -user \"$uid\" -perm 600)\""
	detail := b.CredentialFile + " is a mode 0600 regular file owned by this user"
	if b.CAFile != "" {
		cmd += " && test -n \"$(find " + remote.Quote(b.CAFile) + " -prune -type f -user \"$uid\")\""
		detail += "; " + b.CAFile + " is a regular file owned by this user"
	}
	if _, err := r.Run(ctx, cmd, nil); err != nil {
		return Finding{"backup files", false, "want: " + detail + " (not a symlink)"}
	}
	return Finding{"backup files", true, detail}
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

// The target, rather than the workstation, must create private random credentials.
// Check the actual command prerequisites before any deployment mutation.
func secretToolsCheck(ctx context.Context, r remote.Runner) Finding {
	const requirements = "dd base64 tr mktemp wc ln rm find id dirname"
	cmd := `test -c /dev/urandom && test -r /dev/urandom || exit 1
for tool in ` + requirements + `; do command -v "$tool" >/dev/null 2>&1 || exit 1; done`
	if _, err := r.Run(ctx, cmd, nil); err != nil {
		return Finding{"target secrets", false, "requires readable /dev/urandom and " + requirements}
	}
	return Finding{"target secrets", true, "target random source and private-file tools available"}
}
