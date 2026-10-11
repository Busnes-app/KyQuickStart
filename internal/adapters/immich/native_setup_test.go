package immich

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

type nativeSetupReport struct {
	Schema   string
	SetupID  string
	Check    bool
	Created  []string
	Planned  []string
	Accounts []struct {
		ID, Subject   string
		Administrator bool
	}
}

// Every command targets the parent's label-verified disposable container. The
// bundle is created inside that container; credentials never appear in argv.
func qualifyNativeSetup(t *testing.T, ctx context.Context, c *Client, container string, provider ProviderSpec, subjects map[string]string) nativeSetupReport {
	t.Helper()
	const path = "/usr/src/app/kyquickstart/setup.json"
	spec := map[string]any{
		"schema": "immich.setup/1", "setupId": "271333e2-c221-4601-b249-563b3d828530",
		"provider":      map[string]string{"issuer": provider.Issuer, "clientId": provider.ClientID, "clientSecret": provider.ClientSecret, "rolesClaim": provider.RolesClaim},
		"administrator": map[string]string{"subject": subjects["owner"], "email": "owner@example.test", "name": "owner"},
		"everyday":      map[string]string{"subject": subjects["daily"], "email": "daily@example.test", "name": "daily"},
	}
	write := func() {
		t.Helper()
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal("native setup input invalid")
		}
		command := exec.CommandContext(ctx, "docker", "exec", "-i", container, "sh", "-c", "umask 077; mkdir -p /usr/src/app/kyquickstart; chmod 700 /usr/src/app/kyquickstart; cat > /usr/src/app/kyquickstart/setup.json; chmod 600 /usr/src/app/kyquickstart/setup.json")
		command.Stdin = bytes.NewReader(raw)
		if command.Run() != nil {
			t.Fatal("private native bundle write failed")
		}
	}
	run := func(wantSuccess bool, flags ...string) nativeSetupReport {
		t.Helper()
		args := append([]string{"exec", "-e", "NODE_OPTIONS=--max-old-space-size=512", container, "immich-admin", "apply-setup", "--file", path}, flags...)
		output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if len(output) > 65536 || strings.Contains(string(output), provider.ClientSecret) || (err == nil) != wantSuccess {
			t.Fatal("native setup result unsafe or unexpected")
		}
		var report nativeSetupReport
		if wantSuccess && (json.Unmarshal(output, &report) != nil || report.Schema != "immich.setup-report/1" || report.SetupID != spec["setupId"]) {
			t.Fatal("native setup report unavailable")
		}
		return report
	}
	before, err := c.InspectProviderConfiguration(ctx)
	if err != nil {
		t.Fatal("native initial provider proof missing")
	}
	write()
	run(false, "--resume")
	checked := run(true, "--check")
	if !checked.Check || len(checked.Planned) != 3 || len(checked.Created) != 0 || len(checked.Accounts) != 0 {
		t.Fatal("native initial check did not remain a plan")
	}
	if after, err := c.InspectProviderConfiguration(ctx); err != nil || after != before {
		t.Fatal("native check mutated configuration")
	}
	spec["setupId"] = "271333E2-C221-4601-B249-563B3D828530"
	write()
	run(false)
	if after, err := c.InspectProviderConfiguration(ctx); err != nil || after != before {
		t.Fatal("unsupported installation UUID changed provider")
	}
	spec["setupId"] = "271333e2-c221-4601-b249-563b3d828530"
	// A foreign account in the second slot must roll back the entire setup.
	spec["everyday"] = map[string]string{"subject": subjects["daily"], "email": "bootstrap@example.test", "name": "daily"}
	write()
	run(false)
	if after, err := c.InspectProviderConfiguration(ctx); err != nil || after != before {
		t.Fatal("foreign account conflict changed provider")
	}
	spec["everyday"] = map[string]string{"subject": subjects["daily"], "email": "daily@example.test", "name": "daily"}
	write()
	var created nativeSetupReport
	viaInstaller := os.Getenv("KYQ_TEST_IMMICH_INSTALLER") == "1"
	if viaInstaller {
		qualifyNativeInstaller(t, ctx, container, c.origin, provider.ClientSecret, subjects)
		created = run(true, "--check")
	} else {
		created = run(true)
	}
	if (viaInstaller && (len(created.Created) != 0 || !created.Check)) ||
		(!viaInstaller && (len(created.Created) != 3 || created.Check)) || len(created.Planned) != 0 || len(created.Accounts) != 2 {
		t.Fatal("native setup creation proofs incomplete")
	}
	for _, account := range created.Accounts {
		if !uuid.MatchString(account.ID) || account.Administrator != (account.Subject == subjects["owner"]) {
			t.Fatal("native setup account binding invalid")
		}
		if _, err := c.Inspect(ctx, Binding{ID: account.ID, Issuer: provider.Issuer, ClientID: provider.ClientID, Subject: account.Subject}); err != nil {
			t.Fatal("native setup account not independently verified")
		}
	}
	resumed := run(true, "--resume")
	checked = run(true, "--check")
	if len(resumed.Created) != 0 || len(checked.Planned) != 0 || len(checked.Created) != 0 || !checked.Check ||
		len(resumed.Accounts) != 2 || resumed.Accounts[0] != created.Accounts[0] || resumed.Accounts[1] != created.Accounts[1] {
		t.Fatal("native setup repeat changed owned accounts")
	}
	configured, err := c.InspectProviderConfiguration(ctx)
	if err != nil {
		t.Fatal("native configured provider proof missing")
	}
	spec["setupId"] = "371333e2-c221-4601-b249-563b3d828530"
	write()
	run(false)
	if after, err := c.InspectProviderConfiguration(ctx); err != nil || after != configured {
		t.Fatal("foreign installation changed provider")
	}
	spec["setupId"] = created.SetupID
	spec["provider"].(map[string]string)["clientId"] = "foreign-client"
	write()
	run(false, "--resume")
	if after, err := c.InspectProviderConfiguration(ctx); err != nil || after != configured {
		t.Fatal("foreign provider intent changed native configuration")
	}
	spec["provider"].(map[string]string)["clientId"] = provider.ClientID
	write()
	if exec.CommandContext(ctx, "docker", "exec", container, "chmod", "644", path).Run() != nil {
		t.Fatal("native permission fixture failed")
	}
	run(false)
	write()
	if exec.CommandContext(ctx, "docker", "exec", container, "ln", "-s", path, path+".symlink").Run() != nil {
		t.Fatal("native symlink fixture failed")
	}
	run(false, "--file", path+".symlink")
	if exec.CommandContext(ctx, "docker", "exec", container, "chmod", "777", "/usr/src/app/kyquickstart").Run() != nil {
		t.Fatal("native parent permission fixture failed")
	}
	run(false)
	if exec.CommandContext(ctx, "docker", "exec", container, "chmod", "700", "/usr/src/app/kyquickstart").Run() != nil {
		t.Fatal("native parent permission restore failed")
	}
	final := run(true, "--resume")
	if len(final.Created) != 0 || len(final.Accounts) != 2 || final.Accounts[0] != created.Accounts[0] || final.Accounts[1] != created.Accounts[1] {
		t.Fatal("native refusals damaged owned setup")
	}
	t.Log("native product-owned setup verified private bundle, read-only plan, atomic conflict rollback, immutable UUID receipts, exact administrator/everyday creation and resume")
	return created
}
