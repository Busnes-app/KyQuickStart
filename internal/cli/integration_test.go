package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

func TestIntegrateStackValidatesAllAdaptersAndCancellationBeforeConnecting(t *testing.T) {
	for _, mode := range []string{"unsupported", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			plan := "version: 1\ntargets:\n- name: nas\n  host: 192.0.2.1\n  user: kyq\nexisting:\n- product: kyidentity\n  target: nas\n  url: https://identity.example.test\n"
			if mode == "unsupported" {
				plan += "- product: kyvault\n  target: nas\n  url: https://vault.example.test\n"
			}
			plan += "integrations:\n- product: kyidentity\n  target: nas\n  container: identity\n  bundle: /data/setup.json\n"
			if mode == "unsupported" {
				plan += "- product: kyvault\n  target: nas\n  container: vault\n  bundle: /data/setup.json\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(plan), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			var out bytes.Buffer
			err := Run(ctx, []string{"integrate-stack", "--state", dir}, Options{Catalog: catalog.Embedded(), Out: &out})
			if mode == "unsupported" && (err == nil || !strings.Contains(err.Error(), "kyvault has no supported setup adapter")) {
				t.Fatal("unsupported later adapter did not stop before identity", err)
			}
			if mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancelled plan proceeded", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "known_hosts")); !os.IsNotExist(err) {
				t.Fatal("validation reached target connection")
			}
			if out.Len() != 0 {
				t.Fatal("unstarted integration emitted success")
			}
		})
	}
}

func TestIntegrationOrderAndUnsupportedPrevalidation(t *testing.T) {
	st := &stack.Stack{Integrations: []stack.Integration{{Product: "kypulse"}, {Product: "kyidentity"}, {Product: "kyrecovery"}}}
	ordered, err := integrationOrder(st)
	if err != nil || ordered[0].Product != "kyidentity" || ordered[1].Product != "kypulse" || ordered[2].Product != "kyrecovery" || st.Integrations[0].Product != "kypulse" {
		t.Fatal("identity ordering changed plan or lost saved order", err)
	}
	st.Integrations = append(st.Integrations, stack.Integration{Product: "kyvault"})
	if _, err := integrationOrder(st); err == nil {
		t.Fatal("unsupported legacy password adapter accepted")
	}
	if _, err := integrationOrder(&stack.Stack{}); err == nil {
		t.Fatal("references implied integration")
	}
}

func TestGuidedResumePreservesPreparationAndDisclosesItsScope(t *testing.T) {
	st := &stack.Stack{Integrations: []stack.Integration{{Product: "kynotes", Target: "nas", Container: "notes", Bundle: "/data/installer/setup.json", Notes: &stack.NotesPreparation{Version: 1, Backup: &stack.NotesBackup{Keep: 7}}}}, Targets: []stack.Target{{Name: "nas", Host: "nas.example.test", User: "kyq"}}}
	answers := map[string]string{}
	seedAnswers(answers, st)
	var out bytes.Buffer
	draft := filepath.Join(t.TempDir(), "answers.json")
	p := &prompts{ctx: context.Background(), in: bufio.NewReader(strings.NewReader("\n\n\n")), out: &out, answers: answers, draft: draft}
	next := &stack.Stack{Targets: st.Targets}
	if err := guidedIntegration(p, next, stack.Existing{Product: "kynotes", Target: "nas"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.Integrations, st.Integrations) {
		t.Fatal("guided resume lost preparation")
	}
	showPlan(&out, next, catalog.Catalog{})
	if !strings.Contains(out.String(), "Prepare native bundle from saved secret-free settings") || !strings.Contains(out.String(), "credential files must already exist") {
		t.Fatal("plan overstates preparation", out.String())
	}
}

func TestGuidedExplicitIntegrationResumeAndDecline(t *testing.T) {
	dir := t.TempDir()
	answers := "no\n1\nnas\ndocker\n192.0.2.1\nkyq\n22\n/opt/test\nskip\nskip\n1\nkypulse\nnas\nhttps://pulse.example.test\nyes\npulse\n/data/installer/setup.json\nyes\nfinish\n"
	var out bytes.Buffer
	o := Options{Catalog: catalog.Embedded(), In: strings.NewReader(answers), Out: &out}
	if err := Run(context.Background(), []string{"plan", "--state", dir}, o); err != nil {
		t.Fatal(err, out.String())
	}
	st, err := stack.Load(filepath.Join(dir, "stack.yaml"))
	if err != nil || len(st.Integrations) != 1 || st.Integrations[0].Bundle != "/data/installer/setup.json" {
		t.Fatal("explicit integration missing", err)
	}
	if !strings.Contains(out.String(), "Selected setup kypulse") || !strings.Contains(out.String(), "integrate-stack") {
		t.Fatal("plan did not disclose selected mutation")
	}
	// Reconstruct prompts from the durable plan, independent of saved draft answers.
	if err := os.Remove(filepath.Join(dir, "plan-answers.json")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	o.In = strings.NewReader(strings.Repeat("\n", 19))
	if err := Run(context.Background(), []string{"plan", "--state", dir}, o); err != nil {
		t.Fatal(err, out.String())
	}
	after, err := stack.Load(filepath.Join(dir, "stack.yaml"))
	if err != nil || !reflect.DeepEqual(st.Integrations, after.Integrations) {
		t.Fatal("resume lost selected setup", err)
	}
	if st.InstallationID == "" || after.InstallationID != st.InstallationID {
		t.Fatalf("installation id %q not written once (now %q)", st.InstallationID, after.InstallationID)
	}
	// Declining integration removes intent without deleting an existing installation.
	out.Reset()
	o.In = strings.NewReader(strings.Repeat("\n", 14) + "no\nyes\nfinish\n")
	if err := Run(context.Background(), []string{"plan", "--state", dir}, o); err != nil {
		t.Fatal(err, out.String())
	}
	after, err = stack.Load(filepath.Join(dir, "stack.yaml"))
	if err != nil || len(after.Integrations) != 0 || len(after.Existing) != 1 {
		t.Fatal("decline lost inventory or retained mutation", err)
	}
	if err := Run(context.Background(), []string{"integrate-stack", "--state", dir}, Options{Catalog: catalog.Embedded(), Out: &out}); err == nil || !strings.Contains(err.Error(), "no product integrations") {
		t.Fatal("reference-only plan reached integration", err)
	}
}

func TestGuidedResumeKeepsHandEditedPreparation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "stack.yaml")
	first := "no\n1\nnas\ndocker\n192.0.2.1\nkyq\n22\n/opt/test\nskip\nskip\n2\n" +
		"kyidentity\nnas\nhttps://identity.example.test\nyes\nidentity\n/data/installer/271333e2-c221-4601-b249-563b3d828530/setup.json\n" +
		"kynotes\nnas\nhttps://notes.example.test\nyes\nnotes\n/data/installer/setup.json\nyes\nfinish\n"
	var out bytes.Buffer
	o := Options{Catalog: catalog.Embedded(), In: strings.NewReader(first), Out: &out}
	if err := Run(context.Background(), []string{"plan", "--state", dir}, o); err != nil {
		t.Fatal(err, out.String())
	}
	// Hand-edit the published plan; the saved draft still holds the original answers.
	st, err := stack.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	source, destination, _ := transferFixture()
	identity := *source.Identity
	identity.Schema, identity.Admin.Email = "kyidentity.setup/1", "admin@example.test"
	identity.Everyday = stack.IdentityAccount{Username: "owner", Email: "owner@example.test"}
	identity.Clients[0].Name = "KyNotes"
	st.Integrations[0].Identity = &identity
	st.Integrations[1].Notes, st.Integrations[1].IdentityClient = destination.Notes, destination.IdentityClient
	edited, err := yaml.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.Parse(edited); err != nil {
		t.Fatal("fixture edit invalid", err)
	}
	if err := os.WriteFile(file, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	// Change only the SSH login; Enter keeps every other answer, including identity-client.
	out.Reset()
	o.In = strings.NewReader("\n\n\n\n\nops\n" + strings.Repeat("\n", 18) + "yes\nfinish\n")
	if err := Run(context.Background(), []string{"plan", "--state", dir}, o); err != nil {
		t.Fatal(err, out.String())
	}
	after, err := stack.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if after.Targets[0].User != "ops" {
		t.Fatal("wizard answer not applied", after.Targets[0].User)
	}
	for i := range st.Integrations {
		want, _ := yaml.Marshal(st.Integrations[i])
		got, _ := yaml.Marshal(after.Integrations[i])
		if !bytes.Equal(want, got) {
			t.Fatalf("resume lost hand-edited preparation:\nwant %s\ngot %s", want, got)
		}
	}
}
