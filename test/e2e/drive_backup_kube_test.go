//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	"go.yaml.in/yaml/v3"
)

// driveBackup is KyDrive's backup destination on the nas target.
type driveBackup struct {
	srv  restServer
	dest *stack.BackupDestination
	pw   string // restic_password on the target, whose paths the host shares
	sshd string
}

// giveDriveDestination starts a rest-server with user nas and gives st's nas target its
// credential and CA, written on the target.
func giveDriveDestination(t *testing.T, work string, st *stack.Stack, d driveTarget) driveBackup {
	t.Helper()
	srv := startRestServer(t, work, "nas")
	dest := srv.destination(t, d.name, strings.TrimSuffix(d.root, "/root")+"/backup-files", "nas")
	for i := range st.Targets {
		if st.Targets[i].Name == "nas" {
			st.Targets[i].Backup = dest
		}
	}
	return driveBackup{srv: srv, dest: dest, pw: d.root + "/kydrive/secrets/restic_password", sshd: d.name}
}

func savePlan(t *testing.T, plan string, st *stack.Stack) {
	t.Helper()
	b, err := yaml.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func nasTarget(t *testing.T, plan string) stack.Target {
	t.Helper()
	st, err := stack.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range st.Targets {
		if target.Name == "nas" {
			return target
		}
	}
	t.Fatal("no nas target")
	return stack.Target{}
}

// kyq runs one kyquickstart command against state, stdin given to restore.
func kyq(t *testing.T, ctx context.Context, state, catalogDir string, trust []string, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	o := cli.Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "e2e", Out: &out}
	if stdin != "" {
		o.Stdin = strings.NewReader(stdin)
	}
	start := time.Now()
	err := cli.Run(ctx, append(append(args, "--state", state), trust...), o)
	t.Logf("%s (%s): %v\n%s", strings.Join(args, " "), time.Since(start).Round(time.Second), err, out.String())
	return out.String(), err
}

func driveContainer(t *testing.T) string {
	t.Helper()
	return docker(t, "ps", "-q", "--no-trunc", "--filter", "label=com.docker.compose.project=kyq-kydrive")
}

var snapshotLine = regexp.MustCompile(`(?m)^kydrive: snapshot ([0-9a-f]{64}) in `)

// backupDrive runs kyquickstart backup kydrive and proves the cold backup kept KyDrive's
// container, left it healthy, and that restic check passes. It returns the snapshot ID and
// the output, and checks the restic containers it ran held no secret.
func backupDrive(t *testing.T, ctx context.Context, state, catalogDir string, trust []string, b driveBackup, secrets map[string]string) (string, string) {
	t.Helper()
	before := driveContainer(t)
	w := watchRestic()
	start := time.Now()
	out, err := kyq(t, ctx, state, catalogDir, trust, "", "backup", "kydrive")
	took := time.Since(start)
	inspect, created := w.end(t)
	m := snapshotLine.FindStringSubmatch(out)
	if err != nil || m == nil {
		t.Fatalf("backup kydrive: %v", err)
	}
	if created == 0 {
		t.Fatal("docker events showed no restic container for a backup")
	}
	if after := driveContainer(t); after != before || after == "" {
		t.Fatalf("KyDrive's container changed across the backup: %s, was %s", after, before)
	}
	if h := docker(t, "inspect", before, "--format", "{{.State.Health.Status}}"); h != "healthy" {
		t.Fatalf("KyDrive is %s after the backup", h)
	}
	if out, err := restic(b.dest, "kydrive", b.pw, "check"); err != nil {
		t.Fatalf("restic check: %v\n%s", err, out)
	}
	if secrets != nil {
		secrets["restic_password"] = docker(t, "exec", b.sshd, "cat", b.pw)
		checkResticInspect(t, inspect, secrets)
	}
	t.Logf("kyquickstart backup kydrive took %s (cold)", took.Round(time.Second))
	return m[1], out
}

// refused asserts err is restore_refused naming want.
func refused(t *testing.T, err error, want string) {
	t.Helper()
	var se *suite.Error
	if !errors.As(err, &se) || se.Code != suite.RestoreRefused || !strings.Contains(se.Message, want) {
		t.Fatalf("restore: %v, want restore_refused naming %q", err, want)
	}
}

// refuseUntouched runs restore kydrive --snapshot id --yes and wants restore_refused naming
// want, KyDrive's directory unchanged and no restic container.
func refuseUntouched(t *testing.T, ctx context.Context, state, catalogDir string, trust []string, d driveTarget, id, want string) []string {
	t.Helper()
	listing := dirDigest(t, d.name, d.root+"/kydrive")
	w := watchRestic()
	out, err := kyq(t, ctx, state, catalogDir, trust, "", "restore", "kydrive", "--snapshot", id, "--yes")
	inspect, created := w.end(t)
	refused(t, err, want)
	if created != 0 {
		t.Fatalf("the refused restore ran %d restic containers: %v", created, inspect)
	}
	if after := dirDigest(t, d.name, d.root+"/kydrive"); after != listing {
		t.Fatalf("the refused restore changed KyDrive's directory:\n%s\nwas:\n%s", after, listing)
	}
	return []string{out, err.Error()}
}

// testDriveBackupGate gives the nas target a destination, asserts apply's backup lines for
// KyDrive, backs it up cold, and proves a restore of the running KyDrive is refused with its
// directory untouched. It returns the outputs and the secrets for the leak check.
func testDriveBackupGate(t *testing.T, ctx context.Context, work, state, catalogDir, plan string, d driveTarget, trust []string, apply func(string) string) ([]string, []string) {
	t.Helper()
	st, err := stack.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	b := giveDriveDestination(t, work, st, d)
	savePlan(t, plan, st)
	out := apply("backup destination")
	checkBackupHandover(t, out, "kydrive", nasTarget(t, plan))
	for _, id := range []string{"kydrive.deploy", "kydrive.health", "kydrive.setup"} {
		if status(out, id) != "skipped" {
			t.Errorf("backup destination: %s = %q", id, status(out, id))
		}
	}
	secrets := map[string]string{"the rest-server password": b.srv.passwords["nas"]}
	id, backupOut := backupDrive(t, ctx, state, catalogDir, trust, b, secrets)
	if !strings.Contains(backupOut, "This run created kydrive's repository password") {
		t.Fatal("the first backup did not print the password command")
	}
	refusal := refuseUntouched(t, ctx, state, catalogDir, trust, d, id[:8], "kydrive is running on nas")
	return append([]string{out, backupOut}, refusal...), []string{secrets["restic_password"], secrets["the rest-server password"]}
}
