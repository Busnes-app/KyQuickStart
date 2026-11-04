package dockerhost

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// fromVolumesStep (<app>.volumes) moves an app off the Compose named volumes an earlier
// version kept (its manifest's from_volumes) and onto ./<name> bind directories, which
// kyquickstart backup can read (#151). Before deploy, each volume kyq-<app>_<name> whose bind
// directory is missing is copied into it once, with the app stopped: the copy is renamed into
// place whole, and only deploy creates a bind directory otherwise, so an existing one (even
// empty) is done. The volume is kept, so nothing is lost if the new layout fails; the owner
// removes it by hand. A restore skips the step: it restores the directories it backs up, and
// deploy creates the rest empty.
type fromVolumesStep struct {
	a    App
	note *string
}

func (s fromVolumesStep) ID() string        { return s.a.Name + ".volumes" }
func (s fromVolumesStep) InputHash() string { return strings.Join(s.a.Catalog.FromVolumes, " ") }
func (s fromVolumesStep) Note() string      { return *s.note }

func (s fromVolumesStep) volume(name string) string { return "kyq-" + s.a.Name + "_" + name }

// pending lists the from_volumes still to copy: a volume exists and its bind directory is
// missing.
func (s fromVolumesStep) pending(ctx context.Context) ([]string, error) {
	cmd := "vols=$(docker volume ls -q) || exit 1\nd=" + remote.Quote(s.a.dir()) + "\nfor v in " + quoteAll(s.a.Catalog.FromVolumes) + `; do
  printf '%s\n' "$vols" | grep -Fqx "kyq-` + s.a.Name + `_$v" || continue
  if [ -e "$d/$v" ] || [ -L "$d/$v" ]; then continue; fi
  echo "$v"
done`
	out, err := s.a.Runner.Run(ctx, cmd, nil)
	if err != nil {
		return nil, fmt.Errorf("list Docker volumes: %w", err)
	}
	return strings.Fields(string(out)), nil
}

func (s fromVolumesStep) Inspect(ctx context.Context) (bool, error) {
	if s.a.Restoring {
		return true, nil
	}
	p, err := s.pending(ctx)
	return len(p) == 0, err
}

// copyContainer is the bounded, offline container both ends of a copy run in: the pinned
// restic image for its tar, read-only root, no network, no capabilities but those added.
const copyContainer = "docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges --memory 512m --memory-swap 512m --cpus 1 --pids-limit 64"

func (s fromVolumesStep) Apply(ctx context.Context) error {
	if err := refuseUnfinishedRestore(ctx, s.a); err != nil {
		return err
	}
	names, err := s.pending(ctx)
	if err != nil || len(names) == 0 {
		return err
	}
	if err := trustedDir(ctx, s.a.Runner, s.a.dir()); err != nil {
		return err
	}
	// The earlier deployment's own files stop it with its stop_grace_period.
	stop := "if [ -e " + remote.Quote(path.Join(s.a.dir(), "compose.yaml")) + " ]; then " + s.a.compose("stop") + "; fi"
	if _, err := s.a.Runner.Run(ctx, stop, nil); err != nil {
		return fmt.Errorf("stop %s before copying its volumes: %w", s.a.Name, err)
	}
	if out, err := s.a.Runner.Run(ctx, "docker ps -q --filter "+remote.Quote("label=com.docker.compose.project=kyq-"+s.a.Name)+" --filter status=running --filter status=restarting", nil); err != nil || strings.TrimSpace(string(out)) != "" {
		return fmt.Errorf("a container of %s still runs, so its volumes are not copied: %w", s.a.Name, errors.Join(err, errors.New("stop it and rerun apply")))
	}
	for _, v := range names {
		if err := s.copyVolume(ctx, v); err != nil {
			return err
		}
	}
	vols := make([]string, len(names))
	for i, v := range names {
		vols[i] = s.volume(v)
	}
	*s.note = fmt.Sprintf("copied Docker volumes %s into %s's bind directories under %s; the volumes are kept: once %s runs on the copies, remove them with docker volume rm %s", strings.Join(vols, ", "), s.a.Name, s.a.dir(), s.a.Name, strings.Join(vols, " "))
	return nil
}

// copyVolume copies volume v into the bind directory v through a private temporary directory,
// renamed into place only when both ends succeeded and every entry arrived. The volume is
// read as root with only CAP_DAC_READ_SEARCH (the earlier image user owns it 0700) and
// mounted read-only; the copy is written as the target user, who then owns every file. Each
// tar runs in its own offline container, so an archive entry can reach nothing but /to.
func (s fromVolumesStep) copyVolume(ctx context.Context, v string) error {
	src := copyContainer + " --user 0:0 --cap-add DAC_READ_SEARCH -v " + remote.Quote(s.volume(v)+":/from:ro") + " --entrypoint "
	dst := copyContainer + ` -i --user "$(id -u):$(id -g)" -v "$PWD/$t:/to" --entrypoint tar ` + ResticImage + " -C /to -xpf -"
	cmd := "umask 077; cd " + remote.Quote(s.a.dir()) + " || exit 1\nv=" + remote.Quote(v) + "; t=.$v.from-volume\n" + `if [ -L "$v" ] || { [ -e "$v" ] && [ ! -d "$v" ]; }; then echo "$v is not a directory; inspect it by hand" >&2; exit 1; fi
rm -rf "$t" && mkdir "$t" || exit 1
r=$( { { ` + src + `tar ` + ResticImage + ` -C /from -cf - . ; echo "src=$?" >&3; } | ` + dst + ` >&2; echo "dst=$?" >&3; } 3>&1 )
case $r in *src=0*dst=0*|*dst=0*src=0*) ;; *) echo "copying volume $v failed ($r)" >&2; rm -rf "$t"; exit 1;; esac
n=$(` + src + `find ` + ResticImage + ` /from | wc -l) || exit 1
m=$(find "$t" | wc -l) || exit 1
[ "$n" = "$m" ] || { echo "volume $v has $n entries, its copy $m" >&2; rm -rf "$t"; exit 1; }
chmod 700 "$t" || exit 1
if [ -d "$v" ]; then rmdir "$v" || exit 1; fi
mv "$t" "$v"`
	if _, err := s.a.Runner.Run(ctx, cmd, nil); err != nil {
		return fmt.Errorf("copy Docker volume %s into %s: %w", s.volume(v), path.Join(s.a.dir(), v), err)
	}
	return trustedDir(ctx, s.a.Runner, path.Join(s.a.dir(), v))
}

func (s fromVolumesStep) Verify(ctx context.Context) error {
	if s.a.Restoring {
		return nil
	}
	p, err := s.pending(ctx)
	if err == nil && len(p) > 0 {
		err = fmt.Errorf("%s's volumes %s are still not in its bind directories", s.a.Name, strings.Join(p, ", "))
	}
	return err
}
