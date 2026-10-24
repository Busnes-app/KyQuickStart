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

// AddressCheck passes when addr, an IPv4 address the plan publishes media on, is one of the
// host's own addresses: Docker refuses to publish on any other, and a wrong one is a dead port.
func AddressCheck(ctx context.Context, r remote.Runner, app, addr string) Finding {
	re := strings.ReplaceAll(addr, ".", `\.`)
	cmd := "{ ip -o -4 addr show 2>/dev/null || ifconfig 2>/dev/null; } | grep -Eq " + remote.Quote(`[[:space:]]inet (addr:)?`+re+`([/[:space:]]|$)`)
	if _, err := r.Run(ctx, cmd, nil); err != nil {
		return Finding{"media address", false, app + ": media_address " + addr + " is not an address of this host; set media_address to one of its LAN IPv4 addresses (ip -4 addr)"}
	}
	return Finding{"media address", true, app + ": " + addr + " is this host's"}
}

// ReceiverChecks are the read-only checks of a native-mail receiver's inputs on the host: a
// Linux x86_64 host (KyPost's only qualified engine), smtp_address one of its addresses, the
// operator's Maddy executable exactly the pinned one, and the TLS directory as KyPost's
// receiver reads it as the container's kypost (uid 1000). None follows a symlink.
func ReceiverChecks(ctx context.Context, r remote.Runner, app string, rc stack.MailReceiver, maddySHA256 string) []Finding {
	f := []Finding{}
	if out, err := r.Run(ctx, "uname -sm", nil); err != nil || strings.TrimSpace(string(out)) != "Linux x86_64" {
		f = append(f, Finding{"mail receiver", false, app + ": its receiver runs on Linux x86_64 only"})
	} else {
		f = append(f, Finding{"mail receiver", true, app + ": Linux x86_64"})
	}
	addr := AddressCheck(ctx, r, app, rc.SMTPAddress)
	addr.Check = "smtp address"
	if !addr.OK {
		addr.Detail = app + ": smtp_address " + rc.SMTPAddress + " is not an address of this host; set it to the one whose TCP 25 the internet reaches (ip -4 addr)"
	}
	f = append(f, addr)
	bin := remote.Quote(rc.MaddyBinary)
	cmd := "f=" + bin + `; [ -f "$f" ] && [ ! -L "$f" ] && [ -x "$f" ] && test -n "$(find "$f" -prune ! -perm -020 ! -perm -002)" && [ "$(sha256sum < "$f" | cut -d ' ' -f 1)" = ` + remote.Quote(maddySHA256) + " ]"
	if _, err := r.Run(ctx, cmd, nil); err != nil {
		f = append(f, Finding{"maddy binary", false, app + ": want " + rc.MaddyBinary + " a regular executable file, not a symlink, writable by neither group nor others, with SHA-256 " + maddySHA256 + " (Maddy 0.9.5, maddy-0.9.5-x86_64-linux-musl; see docs/guided-setup.md)"})
	} else {
		f = append(f, Finding{"maddy binary", true, app + ": " + rc.MaddyBinary + " is the pinned Maddy 0.9.5"})
	}
	// uid 1000 traverses the directory and reads both files; the key is its own, owner-only.
	cmd = "d=" + remote.Quote(rc.TLSDir) + `; [ -d "$d" ] && [ ! -L "$d" ] && [ -f "$d/fullchain.pem" ] && [ ! -L "$d/fullchain.pem" ] && [ -f "$d/privkey.pem" ] && [ ! -L "$d/privkey.pem" ] || exit 1
set -- $(stat -c '%u %a' "$d" "$d/fullchain.pem" "$d/privkey.pem") || exit 1
[ "$1" = 1000 ] || [ $(( 0$2 & 1 )) = 1 ] || exit 1
[ "$3" = 1000 ] || [ $(( 0$4 & 4 )) = 4 ] || exit 1
[ "$5" = 1000 ] && case $6 in 600|400) ;; *) exit 1;; esac`
	if _, err := r.Run(ctx, cmd, nil); err != nil {
		f = append(f, Finding{"receiver tls", false, app + ": want " + rc.TLSDir + " a directory (not a symlink) holding regular files fullchain.pem, readable by uid 1000, and privkey.pem, owned by uid 1000 with mode 0600; the certificate must name " + rc.Hostname})
	} else {
		f = append(f, Finding{"receiver tls", true, app + ": " + rc.TLSDir + " holds fullchain.pem and uid 1000's private privkey.pem"})
	}
	return f
}
