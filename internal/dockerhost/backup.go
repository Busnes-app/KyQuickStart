package dockerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// ResticImage is restic 0.19.1's linux/amd64 manifest (index sha256:136600b6ff68…), from
// `docker buildx imagetools inspect docker.io/restic/restic:0.19.1` on 2026-10-19. Its only
// provenance is an unsigned BuildKit SLSA attestation.
const ResticImage = "docker.io/restic/restic:0.19.1@sha256:08916bcda4a4435f9d9828ebb4e91bb7ada3d2c8a53699788930e0ae1bd4fa67"

// Paths inside the restic container. Every snapshot path is under ctrData.
const (
	ctrRest     = "/run/kyq/rest_password"
	ctrPassword = "/run/kyq/restic_password"
	ctrCA       = "/run/kyq/ca.pem"
	ctrData     = "/kyq"
	ctrRestore  = "/restore"
)

// resticWrapper gives restic the rest-server password from its file inside the container,
// so it is in no argv and no Config.Env.
const resticWrapper = `p=$(cat ` + ctrRest + `) || exit 1; RESTIC_REST_PASSWORD=$p; export RESTIC_REST_PASSWORD; unset p; exec restic --no-cache "$@"`

// maxResticJSON bounds restic's JSON output.
const maxResticJSON = 4 << 20

var (
	snapshotIDRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// ValidSnapshotID is what restore accepts: a short or full lowercase snapshot ID.
	ValidSnapshotID = regexp.MustCompile(`^([0-9a-f]{8}|[0-9a-f]{64})$`).MatchString
)

// Store is one app's restic repository, <repository>/<app>, on its target's rest-server.
type Store struct {
	App          App
	Dest         stack.BackupDestination
	Host         string // the target's name, restic's --host
	Installation string
	Version      string // the deployed catalog version
}

type bind struct {
	src, dst string
	rw       bool
}

func (s Store) repo() string { return s.Dest.Repository + "/" + s.App.Name }

// PasswordFile is the repository password on the target, beside the app's secrets.
func (s Store) PasswordFile() string {
	return path.Join(s.App.dir(), "secrets", catalog.ResticPassword)
}

func (s Store) scratch() string  { return path.Join(s.App.dir(), ".backup") }
func (s Store) staging() string  { return path.Join(s.App.dir(), ".restore") }
func (s Store) received() string { return path.Join(s.App.dir(), ".restore-password") }

// Tags bind a snapshot to its app and installation.
func (s Store) Tags() []string {
	return []string{"kyquickstart", "app=" + s.App.Name, "installation=" + s.Installation}
}

// resticCmd runs restic in the pinned image as the target user: read-only root, no
// capabilities, the default bridge, no cache, bounded, and only the given binds.
// password is the target path of the repository password file.
func (s Store) resticCmd(password string, binds []bind, args ...string) string {
	all := append([]bind{{src: s.Dest.CredentialFile, dst: ctrRest}, {src: password, dst: ctrPassword}}, binds...)
	if s.Dest.CAFile != "" {
		all = append(all, bind{src: s.Dest.CAFile, dst: ctrCA})
		args = append([]string{"--cacert", ctrCA}, args...)
	}
	var b strings.Builder
	b.WriteString(`docker run --rm --user "$(id -u):$(id -g)" --read-only --cap-drop ALL --security-opt no-new-privileges --network bridge --tmpfs /tmp:size=512m --memory 1g --memory-swap 1g --cpus 1 --pids-limit 128`)
	for _, e := range []string{"RESTIC_REPOSITORY=" + s.repo(), "RESTIC_PASSWORD_FILE=" + ctrPassword, "RESTIC_REST_USERNAME=" + s.Dest.RestUser()} {
		b.WriteString(" -e " + remote.Quote(e))
	}
	for _, m := range all {
		mode := ":ro"
		if m.rw {
			mode = ":rw"
		}
		b.WriteString(" -v " + remote.Quote(m.src+":"+m.dst+mode))
	}
	b.WriteString(" --entrypoint /bin/sh " + ResticImage + " -c " + remote.Quote(resticWrapper) + " restic")
	for _, a := range args {
		b.WriteString(" " + remote.Quote(a))
	}
	return b.String()
}

func (s Store) restic(ctx context.Context, password string, binds []bind, args ...string) ([]byte, error) {
	out, err := s.App.Runner.Run(ctx, s.resticCmd(password, binds, args...), nil)
	if err != nil {
		return out, fmt.Errorf("restic %s: %w", args[0], err)
	}
	return out, nil
}

// HasPassword reports whether the target holds the repository password: a private regular
// file of the generator's length. It is never read onto the workstation.
func (s Store) HasPassword(ctx context.Context) (bool, error) {
	out, err := s.App.Runner.Run(ctx, `f=`+remote.Quote(s.PasswordFile())+`; if test -e "$f" || test -L "$f"; then test -n "$(find "$f" -prune -type f -user "$(id -u)" -perm 600 -size 43c)" && echo yes || echo bad; fi`, nil)
	switch strings.TrimSpace(string(out)) {
	case "yes":
		return true, err
	case "bad":
		return false, fmt.Errorf("%s is not a private 43-byte file of this user; inspect it by hand", s.PasswordFile())
	}
	return false, err
}

// ensureRepository creates the password once, on the target, and the repository only when
// restic finds none (exit 10). It reports whether this run created the password.
func (s Store) ensureRepository(ctx context.Context) (bool, error) {
	for _, d := range []string{s.App.dir(), path.Dir(s.PasswordFile())} {
		if err := trustedDir(ctx, s.App.Runner, d); err != nil {
			return false, err
		}
	}
	has, err := s.HasPassword(ctx)
	if err != nil {
		return false, err
	}
	if !has {
		if err := generateSecret(ctx, s.App.Runner, s.PasswordFile()); err != nil {
			return false, fmt.Errorf("generate %s: %w", catalog.ResticPassword, err)
		}
	}
	_, err = s.restic(ctx, s.PasswordFile(), nil, "cat", "config")
	var ee *remote.ExitError
	if errors.As(err, &ee) && ee.Code == 10 {
		_, err = s.restic(ctx, s.PasswordFile(), nil, "init")
	}
	return !has, err
}

// Backup takes one snapshot of the app: its dump or, without a dump command, its stopped
// volumes; then its capsule_secrets. created reports that this run generated the password.
func (s Store) Backup(ctx context.Context) (id string, created bool, err error) {
	b := s.App.Catalog.Backup
	if err := refuseUnfinishedRestore(ctx, s.App); err != nil {
		return "", false, err
	}
	if created, err = s.ensureRepository(ctx); err != nil {
		return "", created, err
	}
	scratch := remote.Quote(s.scratch())
	if _, err := s.App.Runner.Run(ctx, "rm -rf "+scratch, nil); err != nil {
		return "", created, fmt.Errorf("remove the previous scratch dump: %w", err)
	}
	var binds []bind
	var paths []string
	add := func(src, dst string) { binds, paths = append(binds, bind{src: src, dst: dst}), append(paths, dst) }
	if len(b.Command) > 0 {
		// The dump is plaintext: it never outlives this run, whatever fails.
		defer func() {
			if _, rerr := s.App.Runner.Run(context.WithoutCancel(ctx), "rm -rf "+scratch, nil); rerr != nil {
				err = errors.Join(err, fmt.Errorf("remove the scratch dump: %w", rerr))
			}
		}()
		if err := trustedDir(ctx, s.App.Runner, s.scratch()); err != nil {
			return "", created, err
		}
		dump := path.Join(s.scratch(), "dump")
		cmd := "umask 077; " + s.App.compose("exec -T "+remote.Quote(b.Service)+" "+quoteAll(b.Command)) + " > " + remote.Quote(dump) + " && test -s " + remote.Quote(dump)
		if _, err := s.App.Runner.Run(ctx, cmd, nil); err != nil {
			return "", created, fmt.Errorf("dump command failed or wrote nothing; no snapshot taken: %w", err)
		}
		add(dump, ctrData+"/dump")
	} else {
		// Cold: SQLite and friends are consistent only at rest. Started again whatever happens.
		if _, err := s.App.Runner.Run(ctx, s.App.compose("stop"), nil); err != nil {
			err = fmt.Errorf("docker compose stop: %w", err)
			return "", created, errors.Join(err, s.restart(ctx))
		}
		defer func() { err = errors.Join(err, s.restart(ctx)) }()
	}
	for _, v := range b.Volumes {
		add(path.Join(s.App.dir(), v), ctrData+"/volumes/"+v)
	}
	for _, n := range b.CapsuleSecrets {
		add(path.Join(s.App.dir(), "secrets", n), ctrData+"/secrets/"+n)
	}
	args := []string{"backup", "--json", "--host", s.Host}
	for _, t := range append(s.Tags(), "version="+s.Version) {
		args = append(args, "--tag", t)
	}
	for _, x := range b.Skip {
		args = append(args, "--exclude", ctrData+"/volumes/"+x)
	}
	out, err := s.restic(ctx, s.PasswordFile(), binds, append(args, paths...)...)
	if err != nil {
		return "", created, err
	}
	if id, err = summaryID(out); err != nil {
		return "", created, err
	}
	if _, err := s.restic(ctx, s.PasswordFile(), nil, "check"); err != nil {
		return id, created, err
	}
	return id, created, nil
}

// restart starts the stopped project and waits for its health, even after ctx is cancelled.
func (s Store) restart(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(s.App.Catalog.Health.TimeoutSeconds)*time.Second+time.Minute)
	defer cancel()
	if _, err := s.App.Runner.Run(ctx, s.App.compose("start"), nil); err != nil {
		return fmt.Errorf("docker compose start: %w", err)
	}
	return healthStep{a: s.App}.Apply(ctx)
}

// summaryID reads the snapshot ID from restic backup --json's summary line.
func summaryID(out []byte) (string, error) {
	if len(out) > maxResticJSON {
		return "", errors.New("restic backup output is too large")
	}
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		var m struct {
			Type string `json:"message_type"`
			ID   string `json:"snapshot_id"`
		}
		if json.Unmarshal(line, &m) == nil && m.Type == "summary" && snapshotIDRE.MatchString(m.ID) {
			return m.ID, nil
		}
	}
	return "", errors.New("restic backup reported no snapshot")
}

// Snapshot is one restic snapshot of an app.
type Snapshot struct {
	ID    string    `json:"id"`
	Short string    `json:"short_id"`
	Time  time.Time `json:"time"`
	Host  string    `json:"hostname"`
	Paths []string  `json:"paths"`
	Tags  []string  `json:"tags"`
}

// Bound reports whether the snapshot carries this app's and installation's tags.
func (s Store) Bound(snap Snapshot) bool {
	return !slices.ContainsFunc(s.Tags(), func(t string) bool { return !slices.Contains(snap.Tags, t) })
}

// Snapshots lists the app's snapshots, or the one named, with password at its target path.
func (s Store) Snapshots(ctx context.Context, password, id string) ([]Snapshot, error) {
	args := []string{"snapshots", "--json"}
	if id != "" {
		args = append(args, id)
	} else {
		args = append(args, "--tag", strings.Join(s.Tags(), ","))
	}
	out, err := s.restic(ctx, password, nil, args...)
	if err != nil {
		return nil, err
	}
	if len(out) > maxResticJSON {
		return nil, errors.New("restic snapshots output is too large")
	}
	var list []Snapshot
	dec := json.NewDecoder(bytes.NewReader(out))
	if err := dec.Decode(&list); err != nil {
		return nil, fmt.Errorf("restic snapshots output: %w", err)
	}
	for _, sn := range list {
		if !snapshotIDRE.MatchString(sn.ID) {
			return nil, errors.New("restic snapshots output has an invalid ID")
		}
	}
	return list, nil
}

// Running reports whether any container of the app's project is running.
func (s Store) Running(ctx context.Context) (bool, error) {
	out, err := s.App.Runner.Run(ctx, "docker ps -q --filter "+remote.Quote("label=com.docker.compose.project=kyq-"+s.App.Name)+" --filter status=running", nil)
	return strings.TrimSpace(string(out)) != "", err
}

// Occupied lists the app's non-empty bind directories and present secret files: what
// restore would overwrite.
func (s Store) Occupied(ctx context.Context) ([]string, error) {
	var names []string
	for _, v := range s.App.Catalog.Binds {
		names = append(names, v+"/")
	}
	for _, n := range s.App.Catalog.Secrets {
		names = append(names, "secrets/"+n)
	}
	cmd := "cd " + remote.Quote(s.App.dir()) + " 2>/dev/null || exit 0\nfor p in " + quoteAll(names) + `; do
  case $p in
  */) [ -d "$p" ] && [ -n "$(ls -A "$p")" ] && echo "$p" ;;
  *) { [ -e "$p" ] || [ -L "$p" ]; } && echo "$p" ;;
  esac
done; exit 0`
	out, err := s.App.Runner.Run(ctx, cmd, nil)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

// MoveAside renames the bind directories and the declared secrets into
// <app>/.replaced-<stamp>/ and returns that path. Nothing is deleted; restic_password stays.
func (s Store) MoveAside(ctx context.Context, stamp string) (string, error) {
	dest := path.Join(s.App.dir(), ".replaced-"+stamp)
	for _, d := range []string{dest, path.Join(dest, "secrets")} {
		if err := trustedDir(ctx, s.App.Runner, d); err != nil {
			return "", err
		}
	}
	var names []string
	for _, n := range s.App.Catalog.Secrets {
		names = append(names, "secrets/"+n)
	}
	cmd := "cd " + remote.Quote(s.App.dir()) + " || exit 1\nfor p in " + quoteAll(append(slices.Clone(s.App.Catalog.Binds), names...)) + `; do
  if [ -e "$p" ] || [ -L "$p" ]; then mv "$p" ` + remote.Quote(dest) + `/"$p" || exit 1; fi
done`
	if _, err := s.App.Runner.Run(ctx, cmd, nil); err != nil {
		return "", fmt.Errorf("move the current data aside: %w", err)
	}
	return dest, nil
}

// ReceivePassword writes a password given on the workstation to a private file in the app
// directory for restic to read; PublishPassword makes it the app's.
func (s Store) ReceivePassword(ctx context.Context, password []byte) (string, error) {
	if err := trustedDir(ctx, s.App.Runner, s.App.dir()); err != nil {
		return "", err
	}
	f := remote.Quote(s.received())
	if _, err := s.App.Runner.Run(ctx, "umask 077; rm -f "+f+" && cat > "+f, password); err != nil {
		return "", fmt.Errorf("write the received password: %w", err)
	}
	return s.received(), nil
}

// PublishPassword moves the received password to the app's secrets, never replacing one.
func (s Store) PublishPassword(ctx context.Context) error {
	if err := trustedDir(ctx, s.App.Runner, path.Dir(s.PasswordFile())); err != nil {
		return err
	}
	cmd := "ln " + remote.Quote(s.received()) + " " + remote.Quote(s.PasswordFile()) + " && rm " + remote.Quote(s.received())
	if _, err := s.App.Runner.Run(ctx, cmd, nil); err != nil {
		return fmt.Errorf("save %s: %w", catalog.ResticPassword, err)
	}
	return nil
}

// RestoreFiles restores the snapshot into a private staging directory and moves its volumes
// and secrets into place, owned by the target user; secrets end 0600. The dump, if any,
// stays staged for RestoreDump.
func (s Store) RestoreFiles(ctx context.Context, password, id string) error {
	for _, d := range []string{s.App.dir(), path.Join(s.App.dir(), "secrets")} {
		if err := trustedDir(ctx, s.App.Runner, d); err != nil {
			return err
		}
	}
	if _, err := s.App.Runner.Run(ctx, "rm -rf "+remote.Quote(s.staging()), nil); err != nil {
		return fmt.Errorf("remove a previous staging directory: %w", err)
	}
	if err := trustedDir(ctx, s.App.Runner, s.staging()); err != nil {
		return err
	}
	if _, err := s.restic(ctx, password, []bind{{src: s.staging(), dst: ctrRestore, rw: true}}, "restore", id, "--target", ctrRestore); err != nil {
		return err
	}
	b := s.App.Catalog.Backup
	from := path.Join(s.staging(), strings.TrimPrefix(ctrData, "/"))
	// A snapshot may come from a compromised target: a symlinked volume or secret would point
	// the app's bind mount, or chmod, at another host path. find -prune does not follow links.
	// Every directory between the trusted staging directory and an entry is checked too: a
	// symlinked kyq, volumes or secrets would redirect the whole tree. All entries are checked
	// before any moves.
	var parents []string
	if len(b.Volumes) > 0 {
		parents = append(parents, "volumes")
	}
	if len(b.CapsuleSecrets) > 0 {
		parents = append(parents, "secrets")
	}
	dumpCheck := ""
	if len(b.Command) > 0 {
		dumpCheck = `[ ! -L "$f/dump" ] && [ -n "$(find "$f/dump" -prune -type f)" ] || { echo "the snapshot has no dump file" >&2; exit 1; }`
	}
	cmd := "umask 077; cd " + remote.Quote(s.App.dir()) + " || exit 1\nf=" + remote.Quote(from) + `
for p in "$f" ` + quoteAll(parents) + `; do
  [ "$p" = "$f" ] || p="$f/$p"
  [ ! -L "$p" ] && [ -n "$(find "$p" -prune -type d)" ] || { echo "the snapshot's $p is not a directory" >&2; exit 1; }
done
` + dumpCheck + `
for v in ` + quoteAll(b.Volumes) + `; do
  [ ! -L "$f/volumes/$v" ] && [ -n "$(find "$f/volumes/$v" -prune -type d)" ] || { echo "the snapshot has no volume directory $v" >&2; exit 1; }
done
for n in ` + quoteAll(b.CapsuleSecrets) + `; do
  [ ! -L "$f/secrets/$n" ] && [ -n "$(find "$f/secrets/$n" -prune -type f)" ] || { echo "the snapshot has no secret file $n" >&2; exit 1; }
done
for v in ` + quoteAll(b.Volumes) + `; do
  if [ -d "$v" ]; then rmdir "$v" || exit 1; fi
  mv "$f/volumes/$v" "$v" || exit 1
done
for n in ` + quoteAll(b.CapsuleSecrets) + `; do
  chmod 600 "$f/secrets/$n" && ln "$f/secrets/$n" secrets/"$n" || exit 1
done`
	if _, err := s.App.Runner.Run(ctx, cmd, nil); err != nil {
		return fmt.Errorf("move the restored files into place: %w", err)
	}
	return nil
}

// RestoreDump streams the staged dump into restore_command in the running service.
func (s Store) RestoreDump(ctx context.Context) error {
	b := s.App.Catalog.Backup
	dump := path.Join(s.staging(), strings.TrimPrefix(ctrData, "/"), "dump")
	d, f := remote.Quote(dump), remote.Quote(path.Dir(dump))
	cmd := "{ [ ! -L " + f + " ] && [ -n \"$(find " + f + " -prune -type d)\" ] && [ ! -L " + d + " ] && [ -n \"$(find " + d + " -prune -type f)\" ]; } || { echo 'the snapshot has no dump file' >&2; exit 1; }\n" +
		s.App.compose("exec -T "+remote.Quote(b.Service)+" "+quoteAll(b.RestoreCommand)) + " < " + d
	if _, err := s.App.Runner.Run(ctx, cmd, nil); err != nil {
		return fmt.Errorf("restore_command: %w", err)
	}
	return nil
}

func (s Store) pending() string { return path.Join(s.App.dir(), ".restore-pending") }

// Pending reads the restore in progress: its snapshot and last finished stage, "" if none.
func (s Store) Pending(ctx context.Context) (id, stage string, err error) {
	f := remote.Quote(s.pending())
	out, err := s.App.Runner.Run(ctx, "if test -e "+f+"; then head -c 256 "+f+"; fi", nil)
	if err != nil || len(out) == 0 {
		return "", "", err
	}
	id, stage, ok := strings.Cut(strings.TrimSpace(string(out)), " ")
	if !ok || !snapshotIDRE.MatchString(id) || stage != "files" && stage != "dump" {
		return "", "", fmt.Errorf("%s is not a restore record; inspect it by hand", s.pending())
	}
	return id, stage, nil
}

// refuseUnfinishedRestore refuses every operation on the app but restore itself while a
// restore is unfinished: its removal replay has not been delivered, so starting the app,
// backing it up or handing it over could readmit someone removed after the snapshot.
func refuseUnfinishedRestore(ctx context.Context, a App) error {
	id, _, err := Store{App: a}.Pending(ctx)
	if err == nil && id != "" {
		err = fmt.Errorf("a restore of %s is unfinished and its removal replay not delivered; finish it with kyquickstart restore %s", a.Name, a.Name)
	}
	return err
}

// SetPending records the restore's last finished stage.
func (s Store) SetPending(ctx context.Context, id, stage string) error {
	_, err := s.App.Runner.Run(ctx, "umask 077; printf '%s %s\\n' "+remote.Quote(id)+" "+remote.Quote(stage)+" > "+remote.Quote(s.pending()), nil)
	return err
}

// Finish removes the restore record, the staging directory and an unpublished password.
func (s Store) Finish(ctx context.Context) error {
	_, err := s.App.Runner.Run(ctx, "rm -rf "+remote.Quote(s.staging())+" && rm -f "+remote.Quote(s.pending())+" "+remote.Quote(s.received()), nil)
	return err
}

// Forget removes a password received for a run that restored nothing.
func (s Store) Forget(ctx context.Context) error {
	_, err := s.App.Runner.Run(ctx, "rm -f "+remote.Quote(s.received()), nil)
	return err
}

// Stop stops the app's project, keeping its containers.
func (s Store) Stop(ctx context.Context) error {
	if _, err := s.App.Runner.Run(ctx, s.App.compose("stop"), nil); err != nil {
		return fmt.Errorf("docker compose stop: %w", err)
	}
	return nil
}

// Start starts one service of the deployed project and waits until it is healthy.
func (s Store) Start(ctx context.Context, service string) error {
	if _, err := s.App.Runner.Run(ctx, s.App.compose("up -d "+remote.Quote(service)), nil); err != nil {
		return fmt.Errorf("docker compose up %s: %w", service, err)
	}
	a := s.App
	a.Catalog.Services = []string{service}
	return healthStep{a: a}.Apply(ctx)
}

func quoteAll(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = remote.Quote(a)
	}
	return strings.Join(q, " ")
}
