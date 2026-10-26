//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

// The product source has its own CI. An explicit local binary enables the cross-repo
// integration fixture without replacing pinned production images or touching live hosts.
func testIdentityIntegration(t *testing.T, ctx context.Context, state, hostFP string) {
	binary := os.Getenv("KYQ_TEST_IDENTITY_BINARY")
	if binary == "" {
		return
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("KYQ_TEST_IDENTITY_BINARY must be absolute")
	}
	data := t.TempDir()
	installer := filepath.Join(data, "installer")
	if err := os.Mkdir(installer, 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"admin.password": "fixture-administrator-password", "everyday.password": "fixture-everyday-password", "drive.secret": strings.Repeat("q", 48), "drive.scim": strings.Repeat("s", 48), "notes.secret": strings.Repeat("n", 48), "notes.webhook": strings.Repeat("w", 64)} {
		if err := os.WriteFile(filepath.Join(installer, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bundle := `{"schema":"kyidentity.setup/1","setupId":"271333e2-c221-4601-b249-563b3d828530","admin":{"username":"owner-admin","email":"admin@example.test","passwordFile":"/data/installer/admin.password"},"everyday":{"username":"owner","email":"owner@example.test","passwordFile":"/data/installer/everyday.password"},"groups":[{"name":"Suite users","members":["owner"]}],"clients":[{"id":"drive","name":"Drive","secretFile":"/data/installer/drive.secret","redirectUris":["https://drive.example.test/callback"],"postLogoutUris":[],"administratorRole":"admin","everydayRole":"user","groups":["Suite users"],"scim":{"url":"https://unreachable.example.test/scim/v2","tokenFile":"/data/installer/drive.scim","groups":true}},{"id":"notes","name":"KyNotes","secretFile":"/data/installer/notes.secret","redirectUris":["https://notes.example.test/api/v1/auth/oidc/callback"],"postLogoutUris":[],"administratorRole":"kynotes.admin","everydayRole":"kynotes.user","suiteWebhook":{"url":"https://unreachable.example.test/api/v1/sync/events","signingSecretFile":"/data/installer/notes.webhook"}}]}`
	if err := os.WriteFile(filepath.Join(installer, "bundle.json"), []byte(bundle), 0600); err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	name := "kyq-identity-" + hex.EncodeToString(random)
	docker(t, "run", "-d", "--name", name, "--memory", "768m", "--memory-swap", "768m", "--cpus", "1", "--pids-limit", "128", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "-v", binary+":/usr/local/bin/kyidentity:ro", "-v", data+":/data", "-e", "KYIDENTITY_DATA_DIR=/data", "-e", "GOMAXPROCS=2", "-e", "GOMEMLIMIT=512MiB", "-e", "KYIDENTITY_ISSUER_URL=https://identity.example.test", "-e", relaysOffEnv[0], "-e", relaysOffEnv[1], "--entrypoint", "/usr/local/bin/kyidentity", "kyq-e2e-sshd")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		b, err := exec.Command("docker", "exec", name, "wget", "-q", "-O-", "http://127.0.0.1:5867/readyz").Output()
		if err == nil && len(b) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disposable KyIdentity did not become ready")
		}
		time.Sleep(250 * time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		args := []string{"integrate-identity", "--state", state, "--target", "e2e", "--container", name, "--bundle", "/data/installer/bundle.json", "--trust-host-key", "e2e=" + hostFP}
		if i == 1 {
			args = append(args, "--check")
		}
		var out bytes.Buffer
		if err := cli.Run(ctx, args, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "e2e", Out: &out}); err != nil {
			t.Fatal(err)
		}
		var r struct {
			Schema          string
			Created, Reused []json.RawMessage
		}
		if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.Schema != "kyidentity.setup/1" {
			t.Fatalf("setup report %q: %v", out.String(), err)
		}
		if i == 0 && len(r.Created) != 11 || i > 0 && (len(r.Created) != 0 || len(r.Reused) != 11) {
			t.Fatalf("setup was not idempotent: %s", out.String())
		}
		for _, s := range []string{"fixture-administrator-password", "fixture-everyday-password", strings.Repeat("q", 48), strings.Repeat("s", 48), strings.Repeat("n", 48), strings.Repeat("w", 64)} {
			if strings.Contains(out.String(), s) {
				t.Fatal("setup exposed credentials")
			}
		}
	}
	st, err := stack.Load(filepath.Join(state, "stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	st.Existing = []stack.Existing{{Product: "kyidentity", Target: "e2e", URL: "https://identity.example.test"}}
	n := identityFixturePreparation()
	st.Integrations = []stack.Integration{{Product: "kyidentity", Target: "e2e", Container: name, Bundle: "/data/installer/" + n.SetupID + "/setup.json", Identity: n}}
	runPreparedIdentityStack(t, ctx, st, []string{"--trust-host-key", "e2e=" + hostFP})
	password := filepath.Join(data, "installer", n.SetupID, "admin.password")
	if err := os.Remove(password); err != nil {
		t.Fatal(err)
	}
	preparedState := t.TempDir()
	raw, err := yaml.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(preparedState, "stack.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := cli.Run(ctx, []string{"integrate-stack", "--state", preparedState, "--trust-host-key", "e2e=" + hostFP}, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "e2e", Out: &out}); err == nil || out.Len() != 0 {
		t.Fatal("lost native credential accepted or reported ready", err)
	}
	if _, err := os.Stat(password); !os.IsNotExist(err) {
		t.Fatal("lost native owner password regenerated")
	}
	t.Log("real KyIdentity setup through SSH: create, read-only check, preparation/resume and lost-credential refusal passed")
}

func identityFixturePreparation() *stack.IdentityPreparation {
	return &stack.IdentityPreparation{Schema: "kyidentity.setup/1", SetupID: "bb715aba-9bde-4a8c-bde1-24a2eea0af86", Admin: stack.IdentityAccount{Username: "prepared-admin", Email: "prepared-admin@example.test"}, Everyday: stack.IdentityAccount{Username: "prepared-user", Email: "prepared-user@example.test"}, Clients: []stack.IdentityClient{{ID: "prepared-notes", Name: "Prepared KyNotes", RedirectURIs: []string{"https://notes.example.test/api/v1/auth/oidc/callback"}, AdministratorRole: "kynotes.admin", EverydayRole: "kynotes.user", SuiteWebhook: &stack.IdentityWebhook{URL: "https://unreachable.example.test/api/v1/sync/events"}}}}
}

func runPreparedIdentityStack(t *testing.T, ctx context.Context, st *stack.Stack, trust []string) {
	t.Helper()
	state := t.TempDir()
	save := func() {
		raw, err := yaml.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "stack.yaml"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	args := append([]string{"integrate-stack", "--state", state}, trust...)
	run := func(check bool) ([]byte, error) {
		current := append([]string{}, args...)
		if check {
			current = append(current, "--check")
		}
		var out bytes.Buffer
		err := cli.Run(ctx, current, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "e2e", Out: &out})
		return out.Bytes(), err
	}
	if out, err := run(true); err == nil || len(out) != 0 {
		t.Fatal("missing Identity preparation reported ready", err)
	}
	for _, check := range []bool{false, true, false} {
		out, err := run(check)
		if err != nil {
			t.Fatal("native Identity preparation", err)
		}
		if !bytes.Contains(out, []byte(`"product":"kyidentity"`)) || !bytes.Contains(out, []byte(`"schema":"kyidentity.setup/1"`)) {
			t.Fatal("prepared Identity native report missing")
		}
	}
	original := st.Integrations[0].Identity.Clients[0].Name
	st.Integrations[0].Identity.Clients[0].Name = "Changed intent"
	save()
	for _, check := range []bool{true, false} {
		if out, err := run(check); err == nil || len(out) != 0 {
			t.Fatal("changed Identity intent accepted or reported ready", err)
		}
	}
	st.Integrations[0].Identity.Clients[0].Name = original
	save()
	if _, err := run(true); err != nil {
		t.Fatal("changed intent damaged retained setup", err)
	}
	t.Log("real Identity preparation: missing inspection, target generation, apply/check/resume and changed-intent preservation passed")
}
