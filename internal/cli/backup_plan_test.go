package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

const restBlock = "    backup:\n      repository: rest:https://backup.lan:8000/nas\n      credential_file: /home/kyq/rest-password\n"

// AC2: review, plan --noninteractive and apply refuse each destination fault before any
// connection, and accept a valid block.
func TestBackupDestinationRefusedBeforeConnecting(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	dialTarget = func(context.Context, stack.Target, remote.SSHConfig, bool) (driver, error) {
		t.Fatal("connected")
		return nil, nil
	}
	faults := map[string]string{
		"scheme":     "rest:http://backup.lan:8000/nas",
		"userinfo":   "rest:https://nas:pw@backup.lan:8000/nas",
		"query":      "rest:https://backup.lan:8000/nas?x",
		"fragment":   "rest:https://backup.lan:8000/nas#x",
		"no user":    "rest:https://backup.lan:8000/",
		"relative":   "rest:https://backup.lan:8000/nas\n      ca_file: ca.pem",
		"ca dotdot":  "rest:https://backup.lan:8000/nas\n      ca_file: /a/../b",
		"kubernetes": "",
	}
	for name, repo := range faults {
		extra := "apps:\n  - name: hello\n    target: nas\n"
		block := strings.Replace(restBlock, "rest:https://backup.lan:8000/nas", repo, 1)
		if name == "kubernetes" {
			kc := filepath.Join(t.TempDir(), "kubeconfig")
			os.WriteFile(kc, nil, 0o600)
			block = ""
			extra = "  - name: k1\n    kubeconfig: " + kc + "\n" + restBlock + extra
		}
		f.plan(t, block+extra)
		for _, cmd := range [][]string{{"review"}, {"plan", "--noninteractive"}, {"apply"}} {
			if err := f.run(cmd...); err == nil || !strings.Contains(err.Error(), "backup") {
				t.Errorf("%s: %s: err = %v", name, cmd[0], err)
			}
		}
	}
	f.plan(t, restBlock+"apps:\n  - name: hello\n    target: nas\n")
	if err := f.run("review"); err != nil || !strings.Contains(f.out.String(), "Backups: rest:https://backup.lan:8000/nas/<app> (rest-server password in /home/kyq/rest-password, system trust); run by kyquickstart backup, never on a schedule") {
		t.Fatalf("review: %v\n%s", err, f.out.String())
	}
}

// The guided plan asks for a Docker target's destination, blank for none, and resumes it.
func TestGuidedBackupDestination(t *testing.T) {
	dir := t.TempDir()
	answers := "no\n1\nnas\ndocker\n192.0.2.1\nkyq\n22\n/opt/test\nrest:http://b.lan/nas\nrest:https://b.lan/nas\nrel\n/home/kyq/pw\n/home/kyq/ca.pem\n" + strings.Repeat("skip\n", 11) + "0\nyes\nfinish\n"
	var out bytes.Buffer
	o := Options{Catalog: catalog.Embedded(), In: strings.NewReader(answers), Out: &out}
	if err := Run(context.Background(), []string{"plan", "--state", dir}, o); err != nil {
		t.Fatal(err, out.String())
	}
	st, err := stack.Load(filepath.Join(dir, "stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := stack.BackupDestination{Repository: "rest:https://b.lan/nas", CredentialFile: "/home/kyq/pw", CAFile: "/home/kyq/ca.pem"}
	if b := st.Targets[0].Backup; b == nil || *b != want || strings.Count(out.String(), "Please enter a valid value") != 2 {
		t.Fatalf("backup = %+v\n%s", st.Targets[0].Backup, out.String())
	}
	if !strings.Contains(out.String(), "Backups: rest:https://b.lan/nas/<app> (rest-server password in /home/kyq/pw, CA /home/kyq/ca.pem)") {
		t.Fatal(out.String())
	}
	os.Remove(filepath.Join(dir, "plan-answers.json"))
	out.Reset()
	o.In = strings.NewReader(strings.Repeat("\n", 25))
	if err := Run(context.Background(), []string{"plan", "--state", dir}, o); err != nil {
		t.Fatal(err, out.String())
	}
	if st, _ = stack.Load(filepath.Join(dir, "stack.yaml")); st.Targets[0].Backup == nil || *st.Targets[0].Backup != want {
		t.Fatalf("resume lost the destination: %+v", st.Targets[0].Backup)
	}
}
