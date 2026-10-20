package immich

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"regexp"
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
	Service     *struct{ ID string }
	NativeUsers []struct {
		ID            string
		Administrator bool
	}
}

var nativeServiceKey = regexp.MustCompile(`^[A-Za-z0-9]{40,64}$`)

// Every command targets the parent's label-verified disposable container. The
// bundle is created inside that container; credentials never appear in argv.
// The fixture's bootstrap administrator must exist on entry: it proves initial
// setup refuses a non-blank installation, then is removed so setup can own it.
// The returned service key is the setup-created service administrator's key.
func qualifyNativeSetup(t *testing.T, ctx context.Context, c *Client, container string, provider ProviderSpec, subjects map[string]string) (nativeSetupReport, string) {
	t.Helper()
	const path = "/usr/src/app/kyquickstart/setup.json"
	const keyPath = "/usr/src/app/kyquickstart/service.key"
	mobile := c.origin + "/api/oauth/mobile-redirect"
	spec := map[string]any{
		"schema": "immich.setup/1", "setupId": "271333e2-c221-4601-b249-563b3d828530",
		"provider":       map[string]string{"issuer": provider.Issuer, "clientId": provider.ClientID, "clientSecret": provider.ClientSecret, "rolesClaim": provider.RolesClaim, "mobileRedirectUri": mobile},
		"serviceKeyFile": keyPath,
		"administrator":  map[string]string{"subject": subjects["owner"], "email": "owner@example.test", "name": "owner"},
		"everyday":       map[string]string{"subject": subjects["daily"], "email": "daily@example.test", "name": "daily"},
	}
	providerSpec := func() map[string]string { return spec["provider"].(map[string]string) }
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
	keyFile := func() string {
		t.Helper()
		raw, err := exec.CommandContext(ctx, "docker", "exec", container, "sh", "-c", "cat "+keyPath+" 2>/dev/null || true").Output()
		if err != nil {
			t.Fatal("native service key file read failed")
		}
		return string(raw)
	}
	run := func(wantSuccess bool, flags ...string) nativeSetupReport {
		t.Helper()
		args := append([]string{"exec", "-e", "NODE_OPTIONS=--max-old-space-size=512", container, "immich-admin", "apply-setup", "--file", path}, flags...)
		output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		key := keyFile()
		if len(output) > 65536 || strings.Contains(string(output), provider.ClientSecret) || (key != "" && strings.Contains(string(output), key)) || (err == nil) != wantSuccess {
			t.Fatal("native setup result unsafe or unexpected")
		}
		var report nativeSetupReport
		if wantSuccess && (json.Unmarshal(output, &report) != nil || report.Schema != "immich.setup-report/1" || report.SetupID != spec["setupId"]) {
			t.Fatal("native setup report unavailable")
		}
		return report
	}
	database := strings.TrimSuffix(container, "-server") + "-db"
	query := func(statement string) string {
		t.Helper()
		raw, err := exec.CommandContext(ctx, "docker", "exec", database, "psql", "-U", "postgres", "-d", "immich", "-v", "ON_ERROR_STOP=1", "-tAc", statement).Output()
		if err != nil {
			t.Fatal("native fixture database query failed")
		}
		return strings.TrimSpace(string(raw))
	}
	// Raw stored configuration, read without an API key once the bootstrap is gone.
	stored := func() string {
		return query(`SELECT coalesce((SELECT value::text FROM system_metadata WHERE key = 'system-config'), '') || '|' || (SELECT count(*) FROM system_metadata WHERE key = 'kyquickstart-setup')`)
	}
	before, err := c.InspectProviderConfiguration(ctx)
	if err != nil {
		t.Fatal("native initial provider proof missing")
	}
	write()
	run(false, "--resume")
	// The bootstrap administrator makes this a fourth native user and an extra administrator.
	run(false, "--check")
	if keyFile() != "" {
		t.Fatal("native setup plan wrote a service key")
	}
	run(false)
	if after, err := c.InspectProviderConfiguration(ctx); err != nil || after != before {
		t.Fatal("refused initial setup changed provider")
	}
	// The key file is written before the commit; a refused or crashed run leaves it for reuse.
	pending := keyFile()
	if !nativeServiceKey.MatchString(pending) || !strings.HasSuffix(stored(), "|0") {
		t.Fatal("refused initial setup committed or lost its pre-commit service key")
	}
	spec["setupId"] = "271333E2-C221-4601-B249-563B3D828530"
	write()
	run(false)
	if after, err := c.InspectProviderConfiguration(ctx); err != nil || after != before {
		t.Fatal("unsupported installation UUID changed provider")
	}
	spec["setupId"] = "271333e2-c221-4601-b249-563b3d828530"
	write()
	query(`DELETE FROM "user" WHERE email = 'bootstrap@example.test'`)
	blank := stored()
	checked := run(true, "--check")
	if !checked.Check || len(checked.Planned) != 4 || len(checked.Created) != 0 || len(checked.Accounts) != 0 || checked.Service != nil || len(checked.NativeUsers) != 0 {
		t.Fatal("native initial check did not remain a plan")
	}
	if stored() != blank || keyFile() != pending {
		t.Fatal("native check mutated configuration or the service key")
	}
	for _, uri := range []string{c.origin + "/auth/login", strings.Replace(mobile, "https://", "http://", 1), mobile + "?next=1", mobile + "#fragment", strings.Replace(mobile, "https://", "https://user@", 1), mobile + "/"} {
		providerSpec()["mobileRedirectUri"] = uri
		write()
		run(false, "--check")
	}
	providerSpec()["mobileRedirectUri"] = mobile
	for _, file := range []string{"kyquickstart/service.key", "/usr/src/app/kyquickstart/../kyquickstart/service.key", "/usr/src/app/kyquickstart/", path} {
		spec["serviceKeyFile"] = file
		write()
		run(false, "--check")
	}
	spec["serviceKeyFile"] = keyPath
	write()
	if stored() != blank {
		t.Fatal("invalid bundle mutated configuration")
	}
	var created nativeSetupReport
	viaInstaller := os.Getenv("KYQ_TEST_IMMICH_INSTALLER") == "1"
	if viaInstaller {
		qualifyNativeInstaller(t, ctx, container, c.origin, provider.ClientSecret, subjects)
		created = run(true, "--check")
	} else {
		created = run(true)
	}
	if (viaInstaller && (len(created.Created) != 0 || !created.Check)) ||
		(!viaInstaller && (len(created.Created) != 4 || created.Check)) || len(created.Planned) != 0 || len(created.Accounts) != 2 ||
		created.Service == nil || !uuid.MatchString(created.Service.ID) || len(created.NativeUsers) != 3 {
		t.Fatal("native setup creation proofs incomplete")
	}
	if !viaInstaller && keyFile() != pending {
		t.Fatal("initial setup did not reuse its pre-commit service key file")
	}
	serviceKey := keyFile()
	administrators := map[string]bool{created.Accounts[0].ID: true, created.Service.ID: true}
	for i, user := range created.NativeUsers {
		if user.Administrator != administrators[user.ID] || (!administrators[user.ID] && user.ID != created.Accounts[1].ID) || (i > 0 && created.NativeUsers[i-1].ID >= user.ID) {
			t.Fatal("native users are not exactly owner, everyday and service administrator")
		}
	}
	uid, _ := exec.CommandContext(ctx, "docker", "exec", container, "id", "-u").Output()
	mode, _ := exec.CommandContext(ctx, "docker", "exec", container, "stat", "-c", "%a:%u:%F", keyPath).Output()
	if len(uid) == 0 || strings.TrimSpace(string(mode)) != "600:"+strings.TrimSpace(string(uid))+":regular file" || query(`SELECT count(*) FROM api_key WHERE "userId" = '`+created.Service.ID+`'`) != "1" {
		t.Fatal("native service key file is not private or the service account holds other keys")
	}
	if query(`SELECT value->'oauth'->>'mobileOverrideEnabled' || '|' || (value->'oauth'->>'mobileRedirectUri') FROM system_metadata WHERE key = 'system-config'`) != "true|"+mobile {
		t.Fatal("native setup did not write the phone redirect override")
	}
	svc, err := New(c.origin, serviceKey)
	if err != nil {
		t.Fatal("native service key unusable")
	}
	svc.http = c.http
	var listed []struct{ ID string }
	var ignored json.RawMessage
	if svc.call(ctx, http.MethodGet, "/admin/users", nil, &listed) != nil || len(listed) != 3 {
		t.Fatal("native service administrator cannot read accounts")
	}
	var config map[string]any
	if svc.call(ctx, http.MethodGet, "/system-config", nil, &config) != nil ||
		svc.call(ctx, http.MethodPut, "/system-config", config, &ignored) == nil ||
		svc.call(ctx, http.MethodPost, "/api-keys", map[string]any{"name": "escalation", "permissions": []string{"all"}}, &ignored) == nil {
		t.Fatal("native service key exceeds the bridge's permissions")
	}
	for _, password := range []string{"", "disposable-bootstrap-password"} {
		raw, _ := json.Marshal(map[string]string{"email": "kyquickstart-service@kyquickstart.invalid", "password": password})
		login, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.origin+"/api/auth/login", bytes.NewReader(raw))
		login.Header.Set("Content-Type", "application/json")
		response, err := c.http.Do(login)
		if err != nil {
			t.Fatal("native service password probe failed")
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusBadRequest {
			t.Fatal("native service administrator password login was not refused", response.StatusCode)
		}
	}
	for _, account := range created.Accounts {
		if !uuid.MatchString(account.ID) || account.Administrator != (account.Subject == subjects["owner"]) {
			t.Fatal("native setup account binding invalid")
		}
		if _, err := svc.Inspect(ctx, Binding{ID: account.ID, Issuer: provider.Issuer, ClientID: provider.ClientID, Subject: account.Subject}); err != nil {
			t.Fatal("native setup account not independently verified")
		}
	}
	same := func(r nativeSetupReport) bool {
		return len(r.Accounts) == 2 && r.Accounts[0] == created.Accounts[0] && r.Accounts[1] == created.Accounts[1] &&
			r.Service != nil && r.Service.ID == created.Service.ID && len(r.NativeUsers) == 3
	}
	resumed := run(true, "--resume")
	checked = run(true, "--check")
	if len(resumed.Created) != 0 || len(checked.Planned) != 0 || len(checked.Created) != 0 || !checked.Check || !same(resumed) || !same(checked) ||
		keyFile() != serviceKey || query(`SELECT count(*) FROM api_key WHERE "userId" = '`+created.Service.ID+`'`) != "1" {
		t.Fatal("native setup repeat changed owned accounts or minted a service key")
	}
	configured, err := svc.InspectProviderConfiguration(ctx)
	if err != nil {
		t.Fatal("native configured provider proof missing")
	}
	// After commit the file must hold the stored key: a changed or missing file refuses.
	replace := func(content string) {
		t.Helper()
		command := exec.CommandContext(ctx, "docker", "exec", "-i", container, "sh", "-c", "cat > "+keyPath)
		command.Stdin = strings.NewReader(content)
		if command.Run() != nil {
			t.Fatal("native service key fixture failed")
		}
	}
	replace(strings.Repeat("A", 43))
	run(false, "--resume")
	run(false, "--check")
	replace(serviceKey)
	if exec.CommandContext(ctx, "docker", "exec", container, "mv", keyPath, keyPath+".moved").Run() != nil {
		t.Fatal("native service key fixture failed")
	}
	run(false, "--resume")
	if exec.CommandContext(ctx, "docker", "exec", container, "mv", keyPath+".moved", keyPath).Run() != nil {
		t.Fatal("native service key restore failed")
	}
	if query(`SELECT count(*) FROM api_key WHERE "userId" = '`+created.Service.ID+`'`) != "1" {
		t.Fatal("native service key reconciliation minted a key")
	}
	spec["setupId"] = "371333e2-c221-4601-b249-563b3d828530"
	write()
	run(false)
	if after, err := svc.InspectProviderConfiguration(ctx); err != nil || after != configured {
		t.Fatal("foreign installation changed provider")
	}
	spec["setupId"] = created.SetupID
	providerSpec()["clientId"] = "foreign-client"
	write()
	run(false, "--resume")
	providerSpec()["clientId"] = provider.ClientID
	providerSpec()["mobileRedirectUri"] = "https://foreign.example.test/api/oauth/mobile-redirect"
	write()
	run(false, "--resume")
	if after, err := svc.InspectProviderConfiguration(ctx); err != nil || after != configured {
		t.Fatal("foreign provider intent changed native configuration")
	}
	providerSpec()["mobileRedirectUri"] = mobile
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
	if len(final.Created) != 0 || !same(final) {
		t.Fatal("native refusals damaged owned setup")
	}
	t.Log("native product-owned setup verified private bundle, blank-installation refusal, read-only plan, phone redirect override, immutable UUID receipts, exact owner/everyday/service-administrator creation, a private reused pre-commit service key, no second key and resume")
	return created, serviceKey
}
