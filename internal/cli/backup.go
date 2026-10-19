package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	"golang.org/x/term"
)

// replayVersion is the first KyIdentity whose resync replays removals (KyIdentity-server#119).
const replayVersion = "0.4.4"

// resyncWait bounds restore's wait for KyIdentity's connector to deliver the replay.
var resyncWait = 10 * time.Minute

// backupHandover is apply's handover for a Docker app whose target has a backup destination.
const backupHandover = `
%[1]s backups: kyquickstart backup %[1]s takes an encrypted snapshot to %[2]s/%[1]s; nothing runs on a schedule.
  After the first backup, read its repository password once into your printed record (a lost
  target cannot be restored without it): ssh -p %[3]d %[4]s@%[5]s cat %[6]s
  Restore: kyquickstart restore %[1]s lists its snapshots; add --snapshot ID to restore one.
`

// backupLines is backupHandover for app on t, "" when kyquickstart backup does not cover it.
func backupLines(name string, t stack.Target, app catalog.App) string {
	if t.Kubernetes() || t.Backup == nil || app.Backup == nil || namedVolume(app) != "" {
		return ""
	}
	return fmt.Sprintf(backupHandover, name, t.Backup.Repository, t.Port, t.User, t.Host, path.Join(t.Root, name, "secrets", catalog.ResticPassword))
}

// namedVolume names a backup volume that is not a ./<name> bind directory, "" if none.
func namedVolume(app catalog.App) string {
	for _, v := range app.Backup.Volumes {
		if !slices.Contains(app.Binds, v) {
			return v
		}
	}
	return ""
}

// backupCommand parses `backup APP` and `restore APP [--snapshot ID] [--replace] [--yes]`.
func backupCommand(ctx context.Context, cmd string, args []string, o Options) error {
	fl := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fl.SetOutput(o.Out)
	state := fl.String("state", ".", "state directory holding stack.yaml")
	trust := trustFlag{}
	fl.Var(trust, "trust-host-key", "accept an unknown host key: <target>=SHA256:<fingerprint>")
	var r restoreRequest
	if cmd == "restore" {
		fl.StringVar(&r.snapshot, "snapshot", "", "restore this snapshot (8 or 64 lowercase hex)")
		fl.BoolVar(&r.replace, "replace", false, "move the app's current data and secrets aside first")
		fl.BoolVar(&r.yes, "yes", false, "skip the typed confirmation")
	}
	var apps []string
	for rest := args; ; rest = fl.Args()[1:] {
		if err := fl.Parse(rest); err != nil {
			return err
		}
		if fl.NArg() == 0 {
			break
		}
		apps = append(apps, fl.Arg(0))
	}
	if len(apps) != 1 {
		return ErrUsage
	}
	if cmd == "backup" {
		return backup(ctx, *state, apps[0], trust, o)
	}
	if r.snapshot == "" && (r.replace || r.yes) {
		return suite.Errorf(suite.InvalidRequest, "--replace and --yes restore a snapshot: add --snapshot ID (kyquickstart restore %s lists them)", apps[0])
	}
	r.app = apps[0]
	return restore(ctx, *state, r, trust, o)
}

// backupSession loads the plan and refuses, before connecting, an app backup cannot cover.
func backupSession(state, name string, trust trustFlag, o Options) (*session, stack.Target, error) {
	s, err := load(state, trust, o)
	if err != nil {
		return nil, stack.Target{}, err
	}
	t, err := s.installed(name)
	if err == nil {
		app := s.cat[name]
		switch {
		case t.Kubernetes():
			err = suite.Errorf(suite.InvalidRequest, "%s runs on Kubernetes; kyquickstart backup and restore cover Docker apps", name)
		case app.Backup == nil:
			err = suite.Errorf(suite.BackupUnconfigured, "%s's catalog entry has no backup block", name)
		case t.Backup == nil:
			err = suite.Errorf(suite.BackupUnconfigured, "target %s has no backup destination; add its rest-server with kyquickstart plan", t.Name)
		case namedVolume(app) != "":
			err = suite.Errorf(suite.BackupUnsupported, "%s keeps %s in a named Docker volume; kyquickstart backup takes ./<name> bind directories only", name, namedVolume(app))
		case s.installation == "":
			err = suite.Errorf(suite.InvalidRequest, "the plan has no installation_id yet; apply it first")
		}
	}
	if err != nil {
		s.close()
		return nil, t, err
	}
	// Refused on a runner-owned target like any change.
	s.changesPlan, s.edges = true, nil
	return s, t, nil
}

// store is the app's repository on its target, for the deployed version. Restore may find
// no deployment (lost) but never an unmanaged one.
func (s *session) store(ctx context.Context, name string, t stack.Target, restore bool) (dockerhost.Store, error) {
	d := s.drivers[t.Name].(sshDriver)
	if f := dockerhost.BackupFilesCheck(ctx, d.c, *t.Backup); !f.OK {
		return dockerhost.Store{}, suite.Errorf(suite.BackupUnconfigured, "%s: %s", t.Name, f.Detail)
	}
	dep, err := d.deployed(ctx, name)
	if err != nil {
		return dockerhost.Store{}, err
	}
	if dep.Found && !dep.Managed || !dep.Found && !restore {
		return dockerhost.Store{}, suite.Errorf(suite.NotInstalled, "%s is not deployed by kyquickstart on %s", name, t.Name)
	}
	a := d.app(name, s.cat[name], s.o.ReleaseSet)
	a.Redact = s.redact
	return dockerhost.Store{App: a, Dest: *t.Backup, Host: t.Name, Installation: s.installation, Version: dep.Version}, nil
}

// locked runs f holding the locks of s.targets; their markers record this plan after.
func (s *session) locked(ctx context.Context, f func(context.Context) error) (err error) {
	done, err := s.lock(ctx, holderName(), s.targets)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, finish(ctx, done)) }()
	return f(ctx)
}

func backup(ctx context.Context, state, name string, trust trustFlag, o Options) error {
	s, t, err := backupSession(state, name, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
	s.targets = []stack.Target{t}
	if err := s.connect(ctx, trust); err != nil {
		return err
	}
	return s.locked(ctx, func(ctx context.Context) error {
		st, err := s.store(ctx, name, t, false)
		if err != nil {
			return err
		}
		cold := len(s.cat[name].Backup.Command) == 0
		if cold {
			fmt.Fprintf(s.o.Out, "%s: stopping for a cold backup; it starts again when the snapshot ends.\n", name)
		}
		start := time.Now()
		id, created, err := st.Backup(ctx)
		if err != nil {
			err = fmt.Errorf("%s: backup: %s", name, s.redact.Redact(err.Error()))
			if cold {
				if up, rerr := st.Running(ctx); rerr == nil && up {
					fmt.Fprintf(s.o.Out, "%s is running again.\n", name)
				} else {
					fmt.Fprintf(s.o.Out, "%s is NOT running: start it with kyquickstart apply, or on %s: docker compose -p kyq-%s start\n", name, t.Name, name)
				}
			}
			return err
		}
		fmt.Fprintf(s.o.Out, "%s: snapshot %s in %s/%s (restic check passed, %s)\n", name, id, t.Backup.Repository, name, time.Since(start).Round(time.Second))
		if created {
			fmt.Fprintf(s.o.Out, "This run created %s's repository password. Read it once into your printed record:\n  ssh -p %d %s@%s cat %s\n", name, t.Port, t.User, t.Host, st.PasswordFile())
		}
		return nil
	})
}

type restoreRequest struct {
	app, snapshot string
	replace, yes  bool
}

// removalGate refuses to restore an app whose people come from KyIdentity unless the plan's
// KyIdentity replays removals on resync, so a restore cannot readmit someone removed after
// the snapshot. It trusts the installer's deployment record, not the running binary.
func removalGate(app catalog.App, existing bool, d managed.Deployed) error {
	switch {
	case app.OIDC == nil:
		return nil
	case existing:
		return suite.Errorf(suite.RestoreRefused, "restore needs KyIdentity v%s or later, which replays removals on resync; installed: unknown (an existing KyIdentity this plan does not deploy). Upgrade KyIdentity first", replayVersion)
	case catalog.VersionAtLeast(d.Version, replayVersion):
		return nil
	}
	installed := d.Version
	if !d.Found || installed == "" {
		installed = "unknown (no deployment record)"
	}
	return suite.Errorf(suite.RestoreRefused, "restore needs KyIdentity v%s or later, which replays removals on resync; installed: %s. Upgrade KyIdentity first", replayVersion, installed)
}

func restore(ctx context.Context, state string, r restoreRequest, trust trustFlag, o Options) error {
	if r.snapshot != "" && !dockerhost.ValidSnapshotID(r.snapshot) {
		return suite.Errorf(suite.InvalidRequest, "--snapshot must be 8 or 64 lowercase hex characters")
	}
	s, t, err := backupSession(state, r.app, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
	app := s.cat[r.app]
	s.targets = []stack.Target{t}
	identity, hasIdentity := s.placed["kyidentity"]
	if app.OIDC != nil {
		existing := slices.ContainsFunc(s.plan.Existing, func(e stack.Existing) bool { return e.Product == "kyidentity" })
		if existing || !hasIdentity {
			return removalGate(app, existing, managed.Deployed{})
		}
		if !identity.Kubernetes() {
			return suite.Errorf(suite.RestoreRefused, "restore replays removals with resync-system in KyIdentity's pod, which needs KyIdentity on Kubernetes; it is on Docker target %s. Nothing was changed", identity.Name)
		}
		if identity.Name != t.Name {
			s.targets = append(s.targets, identity)
		}
	}
	if err := s.connect(ctx, trust); err != nil {
		return err
	}
	in := bufio.NewReader(o.Stdin)
	return s.locked(ctx, func(ctx context.Context) error {
		if app.OIDC != nil {
			dep, err := s.drivers[identity.Name].deployed(ctx, "kyidentity")
			if err != nil {
				return err
			}
			if err := removalGate(app, false, dep); err != nil {
				return err
			}
		}
		st, err := s.store(ctx, r.app, t, true)
		if err != nil {
			return err
		}
		return s.restoreLocked(ctx, st, r, in)
	})
}

func (s *session) restoreLocked(ctx context.Context, st dockerhost.Store, r restoreRequest, in *bufio.Reader) (err error) {
	out := s.o.Out
	refuse := func(format string, a ...any) error { return suite.Errorf(suite.RestoreRefused, format, a...) }
	pendingID, stage, err := st.Pending(ctx)
	if err != nil {
		return err
	}
	if pendingID != "" {
		if r.snapshot != "" && !strings.HasPrefix(pendingID, r.snapshot) {
			return refuse("a restore of snapshot %s is in progress; rerun kyquickstart restore %s --snapshot %s to finish it", pendingID, r.app, pendingID)
		}
		fmt.Fprintf(out, "Resuming the restore of %s from snapshot %s (%s restored).\n", r.app, pendingID, stage)
		if err := s.confirm(in, r); err != nil {
			return err
		}
		return s.finishRestore(ctx, st, pendingID, stage)
	}
	// Refusals that need no repository come first: the server is not contacted for them.
	var occupied []string
	if r.snapshot != "" {
		running, err := st.Running(ctx)
		if err != nil {
			return err
		}
		if running {
			t := s.placed[r.app]
			return refuse("%s is running on %s; stop it first (ssh -p %d %s@%s docker compose -p kyq-%s stop) or uninstall it. Nothing was changed", r.app, st.Host, t.Port, t.User, t.Host, r.app)
		}
		if occupied, err = st.Occupied(ctx); err != nil {
			return err
		}
		if len(occupied) > 0 && !r.replace {
			return refuse("%s already has %s; rerun with --replace to move them aside (nothing is deleted). Nothing was changed", r.app, strings.Join(occupied, ", "))
		}
	}
	has, err := st.HasPassword(ctx)
	if err != nil {
		return err
	}
	password := st.PasswordFile()
	if !has {
		pw, err := readPassword(in, s.o.Out, s.o.Stdin, r.app)
		if err != nil {
			return err
		}
		s.redact.Add(string(pw))
		password, err = st.ReceivePassword(ctx, pw)
		clear(pw)
		if err != nil {
			return err
		}
		// Gone once published; otherwise asked for again on a rerun.
		defer func() { err = errors.Join(err, finish(ctx, st.Forget)) }()
	}
	list, err := st.Snapshots(ctx, password, r.snapshot)
	if err != nil {
		return fmt.Errorf("%s: %s", r.app, s.redact.Redact(err.Error()))
	}
	if r.snapshot == "" {
		fmt.Fprintf(out, "%s snapshots in %s/%s:\n", r.app, st.Dest.Repository, r.app)
		if len(list) == 0 {
			fmt.Fprintln(out, "  none")
			return nil
		}
		fmt.Fprintf(out, "  %-8s  %-20s  %s\n", "ID", "TIME (UTC)", "HOST")
		for _, sn := range list {
			fmt.Fprintf(out, "  %s  %s  %s\n", sn.Short, sn.Time.UTC().Format(time.RFC3339), sn.Host)
		}
		fmt.Fprintf(out, "Restore one with: kyquickstart restore %s --snapshot <ID>\n", r.app)
		return nil
	}
	if len(list) != 1 {
		return refuse("no single snapshot %s in %s/%s", r.snapshot, st.Dest.Repository, r.app)
	}
	snap := list[0]
	if !st.Bound(snap) {
		return refuse("snapshot %s is not tagged for %s of this installation (%s); nothing was changed", snap.Short, r.app, strings.Join(st.Tags(), ","))
	}
	fmt.Fprintf(out, "Restore %s from snapshot %s\n  taken %s on %s\n  paths %s\n", r.app, snap.ID, snap.Time.UTC().Format(time.RFC3339), snap.Host, strings.Join(snap.Paths, " "))
	if err := s.confirm(in, r); err != nil {
		return err
	}
	start := time.Now()
	if len(occupied) > 0 {
		aside, err := st.MoveAside(ctx, time.Now().UTC().Format("20060102T150405Z"))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Moved the current data and secrets aside to %s.\n", aside)
	}
	if err := st.RestoreFiles(ctx, password, snap.ID); err != nil {
		return fmt.Errorf("%s: %s", r.app, s.redact.Redact(err.Error()))
	}
	if !has {
		if err := st.PublishPassword(ctx); err != nil {
			return err
		}
	}
	if err := st.SetPending(ctx, snap.ID, "files"); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: files restored (%s).\n", r.app, time.Since(start).Round(time.Second))
	return s.finishRestore(ctx, st, snap.ID, "files")
}

// confirm requires the app name typed back, or --yes.
func (s *session) confirm(in *bufio.Reader, r restoreRequest) error {
	if r.yes {
		return nil
	}
	if s.o.Stdin == nil {
		return suite.Errorf(suite.RestoreRefused, "restore needs the app name typed back, or --yes when not interactive; nothing was changed")
	}
	fmt.Fprintf(s.o.Out, "Type %s to restore it: ", r.app)
	line, _ := in.ReadString('\n')
	if strings.TrimSpace(line) != r.app {
		return suite.Errorf(suite.RestoreRefused, "not confirmed: type %q exactly, or pass --yes; nothing was changed", r.app)
	}
	return nil
}

// readPassword reads the repository password from stdin: without echo on a terminal, one
// bounded line otherwise.
func readPassword(in *bufio.Reader, out io.Writer, stdin io.Reader, app string) ([]byte, error) {
	refuse := suite.Errorf(suite.RestoreRefused, "%s's repository password is not on its target; give the one you recorded on stdin: on its own line, then the app name on the next (or the password alone with --yes). Nothing was changed", app)
	if stdin == nil {
		return nil, refuse
	}
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(out, "%s's repository password is not on its target. Enter the one you recorded: ", app)
		pw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(out)
		if err != nil || len(pw) == 0 || len(pw) > 1024 {
			return nil, refuse
		}
		return pw, nil
	}
	line, err := in.ReadSlice('\n')
	pw := []byte(strings.TrimRight(string(line), "\r\n"))
	clear(line)
	if err != nil && !errors.Is(err, io.EOF) || len(pw) == 0 || len(pw) > 1024 {
		return nil, refuse
	}
	return pw, nil
}

// finishRestore deploys the restored app through apply's own steps, restore_order first and
// the dump between, checks its health and setup, and replays KyIdentity's removals.
func (s *session) finishRestore(ctx context.Context, st dockerhost.Store, id, stage string) (err error) {
	name, b := st.App.Name, st.App.Catalog.Backup
	// Until KyIdentity's replay is delivered, the restored app may readmit someone removed
	// after the snapshot: any failure before then leaves it stopped. A rerun resumes here.
	gated, replayed := st.App.Catalog.OIDC != nil, false
	defer func() {
		if err != nil && gated && !replayed {
			err = errors.Join(err, finish(ctx, st.Stop))
		}
	}()
	e := &engine.Engine{Dir: s.state + "/results", Out: s.o.Out, Redact: s.redact, Progress: s.o.Progress}
	order := b.RestoreOrder
	if len(b.Command) > 0 && len(order) == 0 {
		order = []string{b.Service}
	}
	if len(order) > 0 {
		if err := e.Run(ctx, dockerhost.RestoreSteps(st.App, order[0])); err != nil {
			return &suite.Error{Code: suite.StepFailed, Message: err.Error(), Err: err}
		}
		if err := st.Start(ctx, order[0]); err != nil {
			return err
		}
		if len(b.Command) > 0 && stage == "files" {
			if err := st.RestoreDump(ctx); err != nil {
				return fmt.Errorf("%s: %s", name, s.redact.Redact(err.Error()))
			}
			if err := st.SetPending(ctx, id, "dump"); err != nil {
				return err
			}
			fmt.Fprintf(s.o.Out, "%s: dump restored into %s.\n", name, b.Service)
		}
		for _, svc := range order[1:] {
			if err := st.Start(ctx, svc); err != nil {
				return err
			}
		}
	}
	steps := dockerhost.Steps(st.App)
	if err := e.Run(ctx, steps); err != nil {
		return &suite.Error{Code: suite.StepFailed, Message: err.Error(), Err: err}
	}
	// As soon as the app answers: its health is all the replay needs.
	if gated {
		if err := s.replayRemovals(ctx, name); err != nil {
			return err
		}
		replayed = true
	}
	for _, i := range s.integrations {
		if i.Product != name || i.Target != st.Host {
			continue
		}
		setup, connector, err := s.setupSteps(s.drivers[st.Host], i)
		if err != nil {
			return err
		}
		if connector != nil {
			setup = append(setup, connector)
		}
		for _, step := range setup {
			if err := step.Verify(ctx); err != nil {
				return fmt.Errorf("%s: read-only check after restore: %s", step.ID(), s.redact.Redact(err.Error()))
			}
		}
	}
	if err := st.Finish(ctx); err != nil {
		return err
	}
	fmt.Fprintf(s.o.Out, "%s: restored from snapshot %s and healthy.\n", name, id)
	return nil
}

// resyncReport is KyIdentity's `resync-system` result line.
type resyncReport struct {
	Schema   string `json:"schema"`
	ClientID string `json:"clientId"`
	SystemID string `json:"systemId"`
	Status   string `json:"status"`
	Pending  int    `json:"pending"`
	Failed   int    `json:"failed"`
	Blocked  string `json:"blocked"`
}

// replayRemovals has KyIdentity re-send the app's connector state, deactivations included,
// and waits until the connector's whole queue is delivered. Anything else is
// restore_incomplete: the app stays running for a rerun.
func (s *session) replayRemovals(ctx context.Context, name string) error {
	client := name
	if i := slices.IndexFunc(s.integrations, func(i stack.Integration) bool { return i.Product == name && i.IdentityClient != "" }); i >= 0 {
		client = s.integrations[i].IdentityClient
	}
	incomplete := func(detail string) error {
		return suite.Errorf(suite.RestoreIncomplete, "%s is restored but stopped: KyIdentity's %s connector did not deliver the replay of removals (%s), and until it does someone removed after the snapshot could sign in. Fix the connector, then rerun kyquickstart restore %s, which starts the app and replays again", name, client, detail, name)
	}
	k, ok := s.drivers[s.placed["kyidentity"].Name].(kubeDriver)
	if !ok {
		return incomplete("this build runs resync-system in a Kubernetes KyIdentity only")
	}
	ns, pod, container, err := k.c.AppPod(ctx, "kyidentity")
	if err != nil {
		return incomplete(err.Error())
	}
	start := time.Now()
	args := []string{productAdapters["kyidentity"].binary, "resync-system", "--client", client, "--wait", resyncWait.String()}
	out, err := k.c.ExecReport(ctx, ns, pod, container, args)
	var rep resyncReport
	line := strings.TrimSpace(string(out))
	if len(line) > 1024 || json.Unmarshal([]byte(line), &rep) != nil || rep.Schema != "kyidentity.resync-system/1" || rep.ClientID != client {
		detail := "no valid resync-system report"
		if err != nil {
			detail += ": " + s.redact.Redact(err.Error())
		}
		return incomplete(detail)
	}
	if rep.Status != "delivered" {
		detail := fmt.Sprintf("%s, %d pending, %d failed", rep.Status, rep.Pending, rep.Failed)
		if rep.Blocked != "" {
			detail += ", blocked: " + rep.Blocked
		}
		return incomplete(detail)
	}
	fmt.Fprintf(s.o.Out, "%s: KyIdentity replayed its %s connector's state, removals included, and delivered it (%s).\n", name, client, time.Since(start).Round(time.Second))
	return nil
}
