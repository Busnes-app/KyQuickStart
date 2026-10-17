package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	"golang.org/x/crypto/ssh"
)

const runnerID = "0b6c1f3e-2a1d-4f6e-9c3b-5d7e8f9a0b1c"

// fakeRunner answers serve-keygen and serve-export, and keeps what serve-import received.
type fakeRunner struct {
	key      string
	imported map[string]string
	export   string
}

func newFakeRunner(t *testing.T, f *fixture) *fakeRunner {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sp, _ := ssh.NewPublicKey(pub)
	r := &fakeRunner{key: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))}
	f.h.exec = func(cmd string, stdin []byte) ([]byte, error) {
		if !strings.HasPrefix(cmd, "docker exec -i 'kyquickstart-runner' kyquickstart ") || !strings.HasSuffix(cmd, " --state /state") {
			t.Errorf("unexpected exec %q", cmd)
		}
		switch {
		case strings.Contains(cmd, "serve-keygen"):
			return []byte(r.key + " kyquickstart-runner " + runnerID + "\n"), nil
		case strings.Contains(cmd, "serve-export"):
			return []byte(r.export), nil
		}
		r.imported = map[string]string{}
		tr := tar.NewReader(bytes.NewReader(stdin))
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(tr)
			r.imported[h.Name] = string(b)
		}
		return nil, nil
	}
	return r
}

func TestHandoffOwnershipAndRevoke(t *testing.T) {
	f, cat := suiteFixture(t)
	writeApp(t, cat, "extra", "version: 1.0.0\n")
	home := t.TempDir()
	t.Setenv("HOME", home)
	r := newFakeRunner(t, f)
	if err := f.run("install", "web", "--target", "nas"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(f.state, "known_hosts"), []byte("[192.0.2.1]:22 ssh-ed25519 AAAA\n"), 0o600)

	for range 2 { // a rerun reconciles
		if err := f.run("handoff", "--kyyard-target", "nas"); err != nil {
			t.Fatal(err, f.out.String())
		}
	}
	if f.stack(t).Revision != 3 || f.marker(t) != `{"revision":3,"owner":"runner","runner_id":"`+runnerID+`"}` {
		t.Fatalf("revision %d, marker %s", f.stack(t).Revision, f.marker(t))
	}
	keys, _ := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys"))
	if string(keys) != "restrict "+r.key+" kyquickstart-runner "+runnerID+"\n" {
		t.Errorf("authorized_keys = %q", keys)
	}
	if st, err := stack.Parse([]byte(r.imported["stack.yaml"])); err != nil || st.Revision != 3 {
		t.Errorf("imported plan: %v", err)
	}
	if r.imported["known_hosts"] == "" || r.imported["results/web.deploy.json"] == "" {
		t.Errorf("imported %v", r.imported)
	}

	// The CLI may not change a runner-owned plan, locally or on its targets.
	f.h.docker = nil
	for _, args := range [][]string{{"install", "extra", "--target", "nas"}, {"uninstall", "web"}, {"unmanage", "web"}} {
		if err := f.run(args...); code(err) != suite.OwnedByRunner || !strings.Contains(err.Error(), "Suite page") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if f.ran(" down") || f.ran(" up ") || f.stack(t).Revision != 3 {
		t.Error("a refused command changed a target or the plan")
	}
	f.o.In = strings.NewReader("")
	if err := f.run("plan"); code(err) != suite.OwnedByRunner {
		t.Errorf("plan: %v", err)
	}
	f.o.In = nil
	// Plan-preserving commands run at the runner's revision.
	if err := f.run("upgrade"); err != nil {
		t.Errorf("upgrade at the runner's revision: %v", err)
	}
	if err := f.run("status"); err != nil {
		t.Errorf("status: %v", err)
	}

	// The runner moved on: the CLI must pull first.
	os.WriteFile(filepath.Join(f.h.root, ".plan"), []byte(`{"revision":5,"owner":"runner","runner_id":"`+runnerID+`"}`), 0o600)
	if err := f.run("apply"); code(err) != suite.PlanStale {
		t.Fatalf("apply behind the runner: %v", err)
	}
	if err := f.run("status"); code(err) != suite.PlanStale {
		t.Fatalf("status behind the runner: %v", err)
	}
	r.export = strings.Replace(r.imported["stack.yaml"], "revision: 3", "revision: 5", 1)
	if err := f.run("pull-plan", "--kyyard-target", "nas"); err != nil {
		t.Fatal(err)
	}
	if bak, _ := stack.Load(filepath.Join(f.state, "stack.yaml.bak")); f.stack(t).Revision != 5 || bak == nil || bak.Revision != 3 {
		t.Fatalf("pulled revision %d", f.stack(t).Revision)
	}
	if err := f.run("apply"); err != nil {
		t.Fatalf("apply after pull-plan: %v", err)
	}
	r.export = strings.Replace(r.export, "revision: 5", "revision: 4", 1)
	if err := f.run("pull-plan", "--kyyard-target", "nas"); code(err) != suite.PlanStale || f.stack(t).Revision != 5 {
		t.Fatalf("pulled an older plan: %v", err)
	}

	if err := f.run("handoff", "--revoke"); err != nil {
		t.Fatal(err, f.out.String())
	}
	if f.marker(t) != `{"revision":6,"owner":"cli","runner_id":""}` {
		t.Errorf("marker after revoke = %s", f.marker(t))
	}
	if keys, _ := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys")); len(keys) != 0 {
		t.Errorf("runner key left: %q", keys)
	}
	if err := f.run("install", "extra", "--target", "nas"); err != nil {
		t.Errorf("install after revoke: %v", err)
	}
	if err := f.run("uninstall", "web"); err != nil {
		t.Errorf("uninstall after revoke: %v", err)
	}
}

func TestRunnerActsOnlyOnItsOwnTargets(t *testing.T) {
	f, _ := suiteFixture(t)
	f.o.RunnerID = runnerID
	if err := f.run("install", "db", "--target", "nas"); code(err) != suite.OwnedByCLI {
		t.Fatalf("no marker: %v", err)
	}
	os.WriteFile(filepath.Join(f.h.root, ".plan"), []byte(`{"revision":0,"owner":"runner","runner_id":"9b6c1f3e-2a1d-4f6e-9c3b-5d7e8f9a0b1c"}`), 0o600)
	if err := f.run("install", "db", "--target", "nas"); code(err) != suite.OwnedByRunner {
		t.Fatalf("another runner's target: %v", err)
	}
	os.WriteFile(filepath.Join(f.h.root, ".plan"), []byte(`{"revision":0,"owner":"runner","runner_id":"`+runnerID+`"}`), 0o600)
	if err := f.run("install", "db", "--target", "nas"); err != nil {
		t.Fatal(err)
	}
	if f.marker(t) != `{"revision":1,"owner":"runner","runner_id":"`+runnerID+`"}` {
		t.Errorf("marker = %s", f.marker(t))
	}
	if _, err := Status(context.Background(), f.state, f.o); err != nil {
		t.Errorf("status: %v", err)
	}
}

func TestHandoffRefusesBadKeygenOutput(t *testing.T) {
	for _, out := range []string{
		"",
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC kyquickstart-runner " + runnerID,
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA operator@laptop",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA kyquickstart-runner " + runnerID + "\nssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA x",
	} {
		if _, _, err := runnerKey([]byte(out)); err == nil {
			t.Errorf("accepted %q", out)
		}
	}
}
