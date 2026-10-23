package catalog

import (
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"
)

const digest = "traefik/whoami:v1.11.0@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab"

func TestLoadFixture(t *testing.T) {
	cat, err := Load(os.DirFS("testdata/apps"))
	if err != nil {
		t.Fatal(err)
	}
	app, ok := cat["hello"]
	if !ok {
		t.Fatal("hello missing")
	}
	if len(app.Services) != 1 || app.Services[0] != "whoami" {
		t.Errorf("services = %v", app.Services)
	}
	if !strings.Contains(string(app.Compose), "image: "+digest) {
		t.Errorf("compose not rendered:\n%s", app.Compose)
	}
	if app.Health.TimeoutSeconds != 120 || len(app.Secrets) != 1 {
		t.Errorf("manifest = %+v", app.Manifest)
	}
	if app.Category != "ky" || app.Kubernetes == nil || app.Kubernetes.Ports[0] != 8080 || len(app.Kubernetes.Volumes) != 1 {
		t.Errorf("kubernetes = %+v", app.Kubernetes)
	}
}

func TestEmbeddedLoads(t *testing.T) {
	if _, err := Load(Embedded()); err != nil {
		t.Fatal(err)
	}
}

// kyPin reports whether a's image key is its Ky release pin: repo tagged with a.Version, by
// digest. scripts/catalog-follow moves pins, so the entry tests do not name one.
func kyPin(a App, key, repo string) bool {
	return regexp.MustCompile(`^ghcr\.io/busnes-app/` + regexp.QuoteMeta(repo) + `:` + regexp.QuoteMeta(a.Version) + `@sha256:[0-9a-f]{64}$`).MatchString(a.Images[key])
}

func TestKyIdentityEntry(t *testing.T) {
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := cat["kyidentity"]
	k := a.Kubernetes
	if a.Category != "ky" || !VersionAtLeast(a.Version, "0.4.6") || a.PodSecurity != "restricted" || a.OIDC != nil || len(a.Compose) == 0 || len(a.Secrets) != 0 {
		t.Fatalf("manifest = %+v", a.Manifest)
	}
	if !kyPin(a, k.Image, "kyidentity-server") || k.RunAs != 100 || k.WritableRoot || len(k.Capabilities) != 0 {
		t.Fatalf("kubernetes = %+v", k)
	}
	if k.Env["KYIDENTITY_ISSUER_URL"] != "https://"+HostRef || k.ReadinessPath != "/readyz" || k.Ports[0] != 5867 || k.Volumes[0].Mount != k.Env["KYIDENTITY_DATA_DIR"] {
		t.Fatalf("env %v, probe %s, volumes %+v", k.Env, k.ReadinessPath, k.Volumes)
	}
	// 0.4.4 and later rewrite no data: an upgrade from 0.4.3 needs only an acknowledgement.
	if a.Upgrade.Allows("0.4.5", a.Version) != nil || a.Upgrade.Allows("0.4.3", a.Version) != nil {
		t.Fatalf("0.4.3 or 0.4.5 cannot upgrade to %s", a.Version)
	}
	if a.Upgrade.MigratesData || a.Upgrade.MinFrom != "0.4.0" || a.Backup.Volumes[0] != "data" {
		t.Fatalf("upgrade %+v, backup %+v", a.Upgrade, a.Backup)
	}
}

func TestKyRecoveryEntry(t *testing.T) {
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := cat["kyrecovery"]
	k, o := a.Kubernetes, a.OIDC
	if a.Category != "ky" || !VersionAtLeast(a.Version, "0.4.2") || a.PodSecurity != "restricted" || len(a.Compose) == 0 || len(a.Secrets) != 0 || len(a.DependsOn) != 1 || a.DependsOn[0] != "kyidentity" {
		t.Fatalf("manifest = %+v", a.Manifest)
	}
	if !kyPin(a, k.Image, "kyrecovery-server") || k.RunAs != 10001 || k.ReadinessPath != "/healthz" || k.Ports[0] != 8095 || k.Volumes[0].Mount != k.Env["KYRECOVERY_DATA_DIR"] || k.UsesHost() {
		t.Fatalf("kubernetes = %+v", k)
	}
	// Admin-only, with the signed directory KyRecovery's setup requires.
	if o.AdminRole != "kyrecovery.admin" || o.UserRole != "" || o.RedirectPaths[0] != "/api/auth/callback" || o.DirectoryPath != "/api/installer/directory" || len(o.LogoutPaths) != 0 || o.BackchannelLogoutPath != "" || o.Public {
		t.Fatalf("oidc = %+v", o)
	}
	if !a.Upgrade.MigratesData || a.Backup.Volumes[0] != "data" {
		t.Fatalf("upgrade %+v, backup %+v", a.Upgrade, a.Backup)
	}
}

func TestKyPulseEntry(t *testing.T) {
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := cat["kypulse"]
	k, o := a.Kubernetes, a.OIDC
	if a.Category != "ky" || !VersionAtLeast(a.Version, "0.4.2") || a.PodSecurity != "restricted" || len(a.Compose) == 0 || len(a.Secrets) != 0 || len(a.DependsOn) != 1 || a.DependsOn[0] != "kyidentity" {
		t.Fatalf("manifest = %+v", a.Manifest)
	}
	// It binds loopback unless both are set.
	if k.Env["KYPULSE_ENV"] != "production" || k.Env["KYPULSE_HOST"] != "0.0.0.0" || k.Env["KYPULSE_APP_URL"] != "https://{{host}}" || k.Env["KYPULSE_SSO_AUTO_PROVISION"] != "false" {
		t.Fatalf("env = %v", k.Env)
	}
	if !kyPin(a, k.Image, "kypulse-server") || k.RunAs != 10001 || k.ReadinessPath != "/healthz" || k.Ports[0] != 8080 || k.Volumes[0].Mount != k.Env["KYPULSE_DATA_DIR"] || k.Volumes[1].Mount+"/audit.key" != k.Env["KYPULSE_AUDIT_KEY_FILE"] {
		t.Fatalf("kubernetes = %+v", k)
	}
	if o.AdminRole != "kypulse.admin" || o.UserRole != "" || o.RedirectPaths[0] != "/api/sso/kysignon/callback" || o.DirectoryPath != "/api/sso/kysignon/sync" || len(o.LogoutPaths) != 0 || o.BackchannelLogoutPath != "" || o.Public {
		t.Fatalf("oidc = %+v", o)
	}
	if !a.Upgrade.MigratesData || !slices.Equal(a.Backup.Volumes, []string{"data", "secrets"}) {
		t.Fatalf("upgrade %+v, backup %+v", a.Upgrade, a.Backup)
	}
}

// AC2: KyNotes generates its own keys, takes the signed directory instead of SCIM, and its
// stop drains apply-setup.
func TestKyNotesEntry(t *testing.T) {
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := cat["kynotes"]
	k, o := a.Kubernetes, a.OIDC
	if a.Category != "ky" || !VersionAtLeast(a.Version, "0.4.2") || a.PodSecurity != "restricted" || len(a.Compose) == 0 || len(a.Secrets) != 0 || !slices.Equal(a.DependsOn, []string{"kyidentity"}) {
		t.Fatalf("manifest = %+v", a.Manifest)
	}
	if !kyPin(a, k.Image, "kynotes-server") ||
		k.RunAs != 65532 || k.ReadinessPath != "/healthz" || !slices.Equal(k.Ports, []int{8080}) || k.TerminationGraceSeconds != 1020 || k.UsesHost() {
		t.Fatalf("kubernetes = %+v", k)
	}
	if k.Env["KYNOTES_DATA_DIR"] != k.Volumes[0].Mount || k.Env["KYNOTES_BACKUP_DIR"] != k.Volumes[1].Mount || k.Env["PORT"] != "8080" || k.Env["TRUSTED_PROXY_CIDRS"] != "" || k.Env["BOOTSTRAP_ADMIN_PASS"] != "" {
		t.Fatalf("env = %v", k.Env)
	}
	if o.Name != "KyNotes" || o.AdminRole != "kynotes.admin" || o.UserRole != "kynotes.user" || !slices.Equal(o.RedirectPaths, []string{"/api/v1/auth/oidc/callback"}) ||
		o.DirectoryPath != "/api/v1/sync/events" || o.SCIMTokenFile != "" || o.BackchannelLogoutPath != "" || o.Public {
		t.Fatalf("oidc = %+v", o)
	}
	if a.Backup == nil || !slices.Equal(a.Backup.Volumes, []string{"data", "backups"}) || a.Upgrade.MinFrom != "0.4.2" || !a.Upgrade.MigratesData {
		t.Fatalf("upgrade %+v, backup %+v", a.Upgrade, a.Backup)
	}
}

func TestKyCalendarEntry(t *testing.T) {
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := cat["kycalendar"]
	k, o := a.Kubernetes, a.OIDC
	if a.Category != "ky" || !VersionAtLeast(a.Version, "0.4.2") || a.PodSecurity != "restricted" || len(a.Compose) == 0 || len(a.Secrets) != 0 || !slices.Equal(a.DependsOn, []string{"kyidentity"}) {
		t.Fatalf("manifest = %+v", a.Manifest)
	}
	if !kyPin(a, k.Image, "kycalendar") ||
		k.RunAs != 10001 || k.ReadinessPath != "/api/installer/network" || !slices.Equal(k.Ports, []int{8080}) || k.TerminationGraceSeconds != 1200 || !k.UsesHost() {
		t.Fatalf("kubernetes = %+v", k)
	}
	// Setup refuses SCIM, automatic provisioning and a set admin password; forwarded headers stay untrusted.
	for _, name := range []string{"KY_ADMIN_PASSWORD", "KY_SCIM_TOKEN", "KY_SCIM_ENABLED", "KY_TRUSTED_PROXIES"} {
		if _, ok := k.Env[name]; ok {
			t.Errorf("env sets %s", name)
		}
	}
	if k.Env["KY_SSO_AUTO_PROVISION"] != "false" || k.Env["KY_SSO_ENABLED"] != "true" || k.Env["KY_APP_URL"] != "https://{{host}}" ||
		k.Env["KY_DATA_DIR"] != k.Volumes[0].Mount || k.Env["KYCALENDAR_BACKUP_DIR"] != k.Volumes[1].Mount {
		t.Fatalf("env = %v", k.Env)
	}
	if o.Name != "KyCalendar" || o.AdminRole != "kycalendar.admin" || o.UserRole != "kycalendar.user" || !slices.Equal(o.RedirectPaths, []string{"/api/sso/kysignon/callback"}) ||
		o.DirectoryPath != "/api/sso/kysignon/sync" || o.SCIMTokenFile != "" || o.BackchannelLogoutPath != "" || o.Public {
		t.Fatalf("oidc = %+v", o)
	}
	if a.Backup == nil || !slices.Equal(a.Backup.Volumes, []string{"data", "backups"}) || a.Upgrade.MinFrom != "0.4.2" || !a.Upgrade.MigratesData {
		t.Fatalf("upgrade %+v, backup %+v", a.Upgrade, a.Backup)
	}
}

// AC2: KyDNS on Docker only, admin-only sign-in, and a hardened Compose file that publishes on
// the route's address alone and trusts exactly its edge's egress address.
func TestKyDNSEntry(t *testing.T) {
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := cat["kydns"]
	if a.Category != "ky" || !VersionAtLeast(a.Version, "0.4.2") || a.Kubernetes != nil || len(a.Secrets) != 0 || !slices.Equal(a.DependsOn, []string{"kyidentity"}) ||
		!kyPin(a, "kydns", "kydns-server") {
		t.Fatalf("manifest = %+v", a.Manifest)
	}
	if o := a.OIDC; o.Name != "KyDNS" || o.AdminRole != "kydns.admin" || o.UserRole != "" || o.SCIMTokenFile != "" || o.Public || o.BackchannelLogoutPath != "" ||
		!slices.Equal(o.RedirectPaths, []string{"/auth/sso/callback"}) || o.DirectoryPath != "/api/v1/installer/directory" {
		t.Fatalf("oidc = %+v", o)
	}
	if a.Backup == nil || !slices.Equal(a.Backup.Volumes, []string{"data"}) || a.Upgrade.MinFrom != "0.4.2" || !a.Upgrade.MigratesData {
		t.Fatalf("upgrade %+v, backup %+v", a.Upgrade, a.Backup)
	}
	if !a.UsesRoute() || !a.UsesEdgeAddress() || !slices.Equal(a.Binds, []string{"data"}) {
		t.Fatal("compose takes no route or edge address")
	}
	for _, c := range []struct{ forward, addr, edge string }{
		{"192.168.1.53:18053", "192.168.1.53", "192.168.1.20"},
		{"[fd00::53]:18053", "[fd00::53]", "fd00::20"},
	} {
		raw := a.WithRoute(Route{Host: "dns.example.com", Forward: c.forward, ForwardAddr: c.addr, EdgeAddress: c.edge}).Compose
		var f struct {
			Services map[string]struct {
				User        string            `yaml:"user"`
				CapDrop     []string          `yaml:"cap_drop"`
				SecurityOpt []string          `yaml:"security_opt"`
				ReadOnly    bool              `yaml:"read_only"`
				Sysctls     map[string]string `yaml:"sysctls"`
				Tmpfs       []string          `yaml:"tmpfs"`
				Volumes     []string          `yaml:"volumes"`
				Ports       []string          `yaml:"ports"`
				Environment map[string]string `yaml:"environment"`
				Healthcheck any               `yaml:"healthcheck"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal(raw, &f); err != nil || len(f.Services) != 1 {
			t.Fatalf("compose: %v\n%s", err, raw)
		}
		s := f.Services["app"]
		if s.User != "${KYQ_UID:?}:${KYQ_GID:?}" || !slices.Equal(s.CapDrop, []string{"ALL"}) || !slices.Equal(s.SecurityOpt, []string{"no-new-privileges:true"}) || !s.ReadOnly ||
			len(s.Sysctls) != 1 || s.Sysctls["net.ipv4.ip_unprivileged_port_start"] != "0" || !slices.Contains(s.Tmpfs, "/var/lib/kydns:size=1m") ||
			!slices.Equal(s.Volumes, []string{"./data:/data"}) || s.Healthcheck != nil {
			t.Fatalf("service = %+v", s)
		}
		if want := []string{c.forward + ":8053", c.addr + ":53:53/udp", c.addr + ":53:53/tcp"}; !slices.Equal(s.Ports, want) {
			t.Fatalf("ports = %v, want %v", s.Ports, want)
		}
		if s.Environment["KYDNS_TRUSTED_PROXIES"] != c.edge || s.Environment["KYDNS_DATA_DIR"] != "/data" || s.Environment["KYDNS_BACKUP_ALLOW_PRIVATE_RECOVERY"] != "false" {
			t.Fatalf("env = %v", s.Environment)
		}
		if _, ok := s.Environment["KYDNS_ADMIN_PASSWORD"]; ok || strings.Contains(string(raw), "0.0.0.0") || strings.Contains(string(raw), "{{") {
			t.Fatalf("compose:\n%s", raw)
		}
	}
}

// runnerKube completes kube for suite_runner, except for run_as.
const runnerKube = "  suite_runner: true\n  env: {KY_SUITE_RUNNER_SOCKET: /run/kyquickstart/sock/runner.sock}\n"

// AC2: KyYard is admin-only, refuses SCIM, backs up its data claim and runs the suite runner.
func TestKyYardEntry(t *testing.T) {
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := cat["kyyard"]
	k, o := a.Kubernetes, a.OIDC
	if a.Category != "ky" || !VersionAtLeast(a.Version, "0.4.3") || a.PodSecurity != "restricted" || len(a.Compose) == 0 || len(a.Secrets) != 0 || !slices.Equal(a.DependsOn, []string{"kyidentity"}) {
		t.Fatalf("manifest = %+v", a.Manifest)
	}
	if !kyPin(a, k.Image, "kyyard") ||
		k.RunAs != 10001 || k.ReadinessPath != "/health/ready" || !slices.Equal(k.Ports, []int{9273}) || k.TerminationGraceSeconds != 1200 ||
		!k.SuiteRunner || k.Resources.CPURequest != "50m" || k.Resources.Memory != "512Mi" ||
		len(k.Volumes) != 1 || k.Volumes[0] != (Volume{Name: "data", Mount: "/data", Size: "10Gi"}) {
		t.Fatalf("kubernetes = %+v", k)
	}
	want := map[string]string{
		"KY_HOST": "0.0.0.0", "KY_PORT": "9273", "KY_ALLOW_PLAINTEXT_BIND": "true", "KY_TRUSTED_PROXIES": "127.0.0.1", "KY_APP_NAME": "KyYard",
		"KY_APP_URL": "https://{{host}}", "KY_DB_DRIVER": "sqlite", "KY_DATA_DIR": "/data", "KY_DOCKER_SOCKET": "",
		"KY_SSO_ENABLED": "false", "KY_SCIM_ENABLED": "false", "KY_BACKUP_DIR": "/data/backups", "KY_BACKUP_KEEP": "7",
		"KY_BACKUP_DEPOSIT_INTERVAL": "24h", "KY_SUITE_RUNNER_SOCKET": "/run/kyquickstart/sock/runner.sock", "GOMEMLIMIT": "384MiB",
	}
	// Unset: a fixed admin password, the agent image, an external database and the keys KyYard
	// generates into /data. The only trusted proxy is the pod's loopback.
	if !maps.Equal(k.Env, want) {
		t.Fatalf("env = %v", k.Env)
	}
	if o.Name != "KyYard" || o.AdminRole != "kyyard.admin" || o.UserRole != "" || !slices.Equal(o.RedirectPaths, []string{"/api/sso/kyidentity/callback"}) ||
		o.DirectoryPath != "/api/installer/directory" || o.SCIMTokenFile != "" || o.BackchannelLogoutPath != "" || o.Public {
		t.Fatalf("oidc = %+v", o)
	}
	if a.Backup == nil || !slices.Equal(a.Backup.Volumes, []string{"data"}) || a.Upgrade.MinFrom != "0.4.3" || !a.Upgrade.MigratesData {
		t.Fatalf("upgrade %+v, backup %+v", a.Upgrade, a.Backup)
	}
	if !Pinned(a.Images["kyyard"]) || Pinned("ghcr.io/busnes-app/kyquickstart-runner:stable") {
		t.Fatal("Pinned disagrees with the catalog's rule")
	}
}

func TestKyDriveEntry(t *testing.T) {
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := cat["kydrive"]
	o := a.OIDC
	if a.Category != "ky" || !VersionAtLeast(a.Version, "0.4.2") || a.Kubernetes != nil || !slices.Equal(a.Services, []string{"app"}) || !slices.Equal(a.DependsOn, []string{"kyidentity"}) {
		t.Fatalf("manifest = %+v", a.Manifest)
	}
	if !kyPin(a, "kydrive", "kydrive-server") {
		t.Fatalf("image = %s", a.Images["kydrive"])
	}
	// KyDrive refuses a directory webhook and requires its SCIM token in setup.
	if o.RedirectPaths[0] != "/api/sso/kysignon/callback" || o.AdminRole != "kydrive.admin" || o.UserRole != "kydrive.user" || o.DirectoryPath != "" || o.SCIMTokenFile != "/run/secrets/scim_token" || o.Public {
		t.Fatalf("oidc = %+v", o)
	}
	if !slices.Equal(a.Secrets, []string{"session_secret", "scim_token"}) || !slices.Equal(a.Binds, []string{"backups", "data"}) || !slices.Equal(a.Backup.Volumes, []string{"data", "backups"}) || !a.Upgrade.MigratesData {
		t.Fatalf("secrets %v, binds %v, backup %+v", a.Secrets, a.Binds, a.Backup)
	}
	// No dump command: a cold backup, carrying both secrets in the snapshot.
	if b := a.Backup; len(b.Command) != 0 || !slices.Equal(b.CapsuleSecrets, []string{"session_secret", "scim_token"}) {
		t.Fatalf("backup %+v", b)
	}
	c := string(a.WithRoute(Route{Host: "drive.example.com", Forward: "192.168.1.91:18443"}).Compose)
	for _, want := range []string{`user: "${KYQ_UID:?}:${KYQ_GID:?}"`, "read_only: true", "cap_drop: [ALL]", "no-new-privileges:true", "stop_grace_period: 20m",
		`ports: ["192.168.1.91:18443:8080"]`, `KY_APP_URL: "https://drive.example.com"`, "KY_ENV: production", "KY_SSO_AUTO_PROVISION: \"false\"", "KY_SCIM_TOKEN_FILE: /run/secrets/scim_token",
		"KY_DATA_DIR: /data", "/healthz"} {
		if !strings.Contains(c, want) {
			t.Errorf("compose lacks %s", want)
		}
	}
	// No secret value, editor or bulk repository in the Compose file.
	for _, unwanted := range []string{"KY_SESSION_SECRET:", "KY_SCIM_TOKEN:", "KY_KYSIGNON", "KYDRIVE_EDITOR", "KYDRIVE_BULK", "KY_ADMIN_PASSWORD", "container_name", "0.0.0.0:"} {
		if strings.Contains(c, unwanted) {
			t.Errorf("compose has %s", unwanted)
		}
	}
}

// Euro-Office is KyDrive's editor on Kubernetes only: the pinned Euro-Office image, root under
// baseline with its measured capabilities, seeded claims, private-address fetches allowed only
// because its egress reaches nothing but DNS and the edge.
func TestEuroOfficeEntry(t *testing.T) {
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := cat["euro-office"]
	k := a.Kubernetes
	if a.Compose != nil || k == nil || !slices.Equal(a.DependsOn, []string{"kydrive"}) || a.OIDC != nil || a.Backup != nil || a.PodSecurity != "baseline" || !a.Upgrade.MigratesData {
		t.Fatalf("manifest = %+v", a.Manifest)
	}
	if a.Images["documentserver"] != "ghcr.io/euro-office/documentserver@sha256:beca380debb9b4eadb7e4c662d112a325c5df0f2179453a4affd8e6ba5d00562" {
		t.Fatalf("image = %s", a.Images["documentserver"])
	}
	if !slices.Equal(a.Secrets, []string{"jwt_secret", "secure_link_secret"}) || k.SecretsDir != "/var/www/euro-office/Data/.private" {
		t.Fatalf("secrets %v in %s", a.Secrets, k.SecretsDir)
	}
	if k.RunAs != 0 || !k.WritableRoot || !k.LowPorts || !slices.Equal(k.Capabilities, []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID", "KILL"}) {
		t.Fatalf("security = %+v", k)
	}
	if k.Env["JWT_ENABLED"] != "true" || k.Env["ALLOW_PRIVATE_IP_ADDRESS"] != "true" || k.Env["ALLOW_META_IP_ADDRESS"] != "false" || k.Env["USE_UNAUTHORIZED_STORAGE"] != "false" || k.Egress != "edge" {
		t.Fatalf("env %v, egress %q", k.Env, k.Egress)
	}
	if !slices.Equal(a.DenyPaths, []string{"^/(?i)(info|internal|ai-proxy|admin|example|welcome)", "^/[0-9]+[.][0-9]+[.][0-9]+[.-][A-Za-z0-9_]+/(?i)(info|internal|ai-proxy)"}) {
		t.Fatalf("deny_paths = %q", a.DenyPaths)
	}
	if k.ReadinessPath != "/healthcheck" || !slices.Equal(k.Ports, []int{80}) || k.Resources.Memory != "4Gi" || len(k.Volumes) != 2 || !k.Volumes[0].Seed || !k.Volumes[1].Seed {
		t.Fatalf("kubernetes = %+v", k)
	}
}

// KyPost runs on Docker only, without root or capabilities, publishing only its HTTP port on
// the forward address, with no mail stack and no secret, proxy trust or break-glass in env.
func TestKyPostEntry(t *testing.T) {
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := cat["kypost"]
	o := a.OIDC
	if a.Category != "ky" || !VersionAtLeast(a.Version, "0.4.2") || a.Kubernetes != nil || len(a.Secrets) != 0 || len(a.Binds) != 0 || !slices.Equal(a.Services, []string{"app"}) || !slices.Equal(a.DependsOn, []string{"kyidentity"}) || a.Health.TimeoutSeconds != 300 {
		t.Fatalf("manifest = %+v", a.Manifest)
	}
	if !kyPin(a, "kypost", "kypost-server") {
		t.Fatalf("image = %s", a.Images["kypost"])
	}
	if o.Name != "KyPost" || !slices.Equal(o.RedirectPaths, []string{"/api/auth/oidc/callback"}) || o.AdminRole != "kypost.admin" || o.UserRole != "kypost.user" ||
		o.DirectoryPath != "" || o.SCIMTokenFile != "" || o.BackchannelLogoutPath != "" || o.Public || len(o.LogoutPaths) != 0 {
		t.Fatalf("oidc = %+v", o)
	}
	if a.Backup == nil || !slices.Equal(a.Backup.Volumes, []string{"config", "private", "state"}) || a.Upgrade.MinFrom != "0.4.2" || !a.Upgrade.MigratesData {
		t.Fatalf("upgrade %+v, backup %+v", a.Upgrade, a.Backup)
	}
	type service struct {
		Image           string
		Platform        string
		User            string
		StopGracePeriod string   `yaml:"stop_grace_period"`
		ReadOnly        bool     `yaml:"read_only"`
		CapDrop         []string `yaml:"cap_drop"`
		CapAdd          []string `yaml:"cap_add"`
		SecurityOpt     []string `yaml:"security_opt"`
		PidsLimit       int      `yaml:"pids_limit"`
		MemLimit        string   `yaml:"mem_limit"`
		Ports           []string
		Entrypoint      []string
		Environment     map[string]string
		Secrets         []string
	}
	var c struct{ Services map[string]service }
	if err := yaml.Unmarshal(a.WithRoute(Route{Host: "post.example.com", Forward: "192.168.1.91:18866"}).Compose, &c); err != nil {
		t.Fatal(err)
	}
	s, ok := c.Services["app"]
	if len(c.Services) != 1 || !ok {
		t.Fatalf("services = %v", c.Services)
	}
	if s.Image != a.Images["kypost"] || s.Platform != "linux/amd64" || s.User != "kypost" || !s.ReadOnly || !slices.Equal(s.CapDrop, []string{"ALL"}) || s.CapAdd != nil ||
		!slices.Equal(s.SecurityOpt, []string{"no-new-privileges:true"}) || s.StopGracePeriod != "36m" || s.PidsLimit != 512 || s.MemLimit != "8g" || s.Secrets != nil {
		t.Fatalf("service = %+v", s)
	}
	if !slices.Equal(s.Ports, []string{"192.168.1.91:18866:5866"}) ||
		!slices.Equal(s.Entrypoint, []string{"/bin/sh", "-c", "kypost-server --mode bootstrap-admin && kypost-server migrate-native && exec supervisord -c /etc/supervisord.conf"}) {
		t.Fatalf("ports %v, entrypoint %v", s.Ports, s.Entrypoint)
	}
	e := s.Environment
	if e["SERVER_BASE_URL"] != "https://post.example.com" || e["KYPOST_NATIVE_MAIL"] != "false" || e["KYPOST_NATIVE_RECEIVING"] != "false" || e["KYPOST_NATIVE_RECEIVER"] != "false" || e["WEB_PORT"] != "5866" {
		t.Fatalf("env = %v", e)
	}
	for k := range e {
		for _, forbidden := range []string{"TRUSTED_PROXY_CIDRS", "PAIRING_SECRET", "POW_SECRET", "BOOTSTRAP_ADMIN_", "ALLOW_INSECURE_HTTP", "TLS_", "SANDBOX_PRIVATE_HOSTS", "SMTP_", "CAPTCHA_",
			"KYPOST_BACKUP_ALLOW_PRIVATE_RECOVERY", "KYPOST_BULK_BACKUP_REPOSITORY", "KYPOST_RECEIVING_"} {
			if strings.HasPrefix(k, forbidden) {
				t.Errorf("env sets %s", k)
			}
		}
	}
}

const manifest = "name: a\ncategory: ky\nversion: 2.4.0\nimages:\n  web: " + digest + "\nhealth:\n  timeout_seconds: 30\n"

const oidc = "oidc:\n  name: App\n  redirect_paths: [/auth/callback]\n  logout_paths: [/]\n  admin_role: admin\n  user_role: user\n"

const backup = "secrets: [db]\nbackup:\n  service: web\n  command: [pg_dump, -Fc]\n  restore_command: [pg_restore]\n  volumes: [data]\n  skip: [data/cache]\n  capsule_secrets: [db]\n  restore_order: [web]\n"

const composeVol = compose + "volumes:\n  data: {}\n"

// composeTwo has a second service, which restore_order lists first.
const composeTwo = compose + "  db:\n    image: {{ .Images.web }}\nvolumes:\n  data: {}\n"

const kube = "kubernetes:\n  image: web\n  ports: [8080]\n  readiness_path: /\n  run_as: 65532\n  resources:\n    cpu: 50m\n    memory: 32Mi\n"

const compose = "services:\n  web:\n    image: {{ .Images.web }}\n"

func TestLoadRejects(t *testing.T) {
	cases := map[string]struct{ manifest, compose, want string }{
		"unknown key":           {manifest + "port: 80\n", compose, "field port not found"},
		"name mismatch":         {strings.Replace(manifest, "name: a", "name: b", 1), compose, "must match its directory"},
		"tag not digest":        {strings.Replace(manifest, digest, "traefik/whoami:latest", 1), compose, "not pinned by digest"},
		"missing key":           {manifest, strings.Replace(compose, ".Images.web", ".Images.nope", 1), "nope"},
		"foreign image":         {manifest, strings.Replace(compose, "{{ .Images.web }}", "nginx@sha256:"+strings.Repeat("0", 64), 1), "not one of the manifest's pinned images"},
		"build":                 {manifest, compose + "    build: .\n", "build is not allowed"},
		"no services":           {manifest, "services: {}\n", "no services"},
		"zero timeout":          {strings.Replace(manifest, "timeout_seconds: 30", "timeout_seconds: 0", 1), compose, "timeout_seconds"},
		"bad secret name":       {manifest + "secrets: [Token]\n", compose, "secret"},
		"self dependency":       {manifest + "depends_on: [a]\n", compose, "depends_on"},
		"no category":           {strings.Replace(manifest, "category: ky\n", "", 1), compose, "category"},
		"third-party on k8s":    {strings.Replace(manifest, "category: ky", "category: third-party", 1) + oidc + kube, compose, "Docker hosts only"},
		"k8s unknown image":     {manifest + strings.Replace(kube, "image: web", "image: nope", 1), compose, "kubernetes.image"},
		"readiness no ports":    {manifest + strings.Replace(kube, "ports: [8080]", "ports: []", 1), compose, "readiness_path"},
		"root user":             {manifest + strings.Replace(kube, "run_as: 65532", "run_as: 0", 1), compose, "run_as"},
		"bad memory":            {manifest + strings.Replace(kube, "32Mi", "32MB", 1), compose, "memory"},
		"cpu_request above cpu": {manifest + strings.Replace(kube, "cpu: 50m\n", "cpu: 50m\n    cpu_request: 2\n", 1), compose, "cpu_request"},
		"bad cpu_request":       {manifest + strings.Replace(kube, "cpu: 50m\n", "cpu: 50m\n    cpu_request: lots\n", 1), compose, "cpu_request"},
		"bad volume size":       {manifest + kube + "  volumes:\n    - name: data\n      mount: /data\n      size: lots\n", compose, "size"},
		"bad env name":          {manifest + kube + "  env:\n    lower: x\n", compose, "env"},
		"other env template":    {manifest + kube + "  env:\n    URL: https://{{.Name}}\n", compose, "the only reference"},
		"split env template":    {manifest + kube + "  env:\n    URL: https://{{host}}name}}\n", compose, "the only reference"},

		"no version":            {strings.Replace(manifest, "version: 2.4.0\n", "", 1), compose, "version"},
		"v-prefixed version":    {strings.Replace(manifest, "2.4.0", "v2.4.0", 1), compose, "MAJOR.MINOR.PATCH"},
		"fourth part zero":      {strings.Replace(manifest, "2.4.0", "2.4.0.0", 1), compose, "MAJOR.MINOR.PATCH.N"},
		"fourth part leading 0": {strings.Replace(manifest, "2.4.0", "2.4.0.01", 1), compose, "MAJOR.MINOR.PATCH.N"},
		"five parts":            {strings.Replace(manifest, "2.4.0", "2.4.0.1.1", 1), compose, "MAJOR.MINOR.PATCH.N"},
		"min_from newer .N":     {manifest + "upgrade:\n  min_from: 2.4.0.1\n", compose, "min_from"},
		"no_skip bad .N":        {manifest + "upgrade:\n  no_skip: [2.3.0.0]\n", compose, "no_skip"},
		"min_from newer":        {manifest + "upgrade:\n  min_from: 2.5.0\n", compose, "min_from"},
		"no_skip at target":     {manifest + "upgrade:\n  no_skip: [2.4.0]\n", compose, "no_skip"},
		"no_skip before min":    {manifest + "upgrade:\n  min_from: 2.0.0\n  no_skip: [1.9.0]\n", compose, "no_skip"},
		"no_skip repeated":      {manifest + "upgrade:\n  no_skip: [2.1.0, 2.1.0]\n", compose, "no_skip"},
		"third-party no oidc":   {strings.Replace(manifest, "category: ky", "category: third-party", 1), compose, "sign in through KyIdentity"},
		"edge with oidc":        {strings.Replace(manifest, "category: ky", "category: edge", 1) + oidc, compose, "edge components"},
		"no redirect":           {manifest + strings.Replace(oidc, "[/auth/callback]", "[]", 1), compose, "redirect_paths"},
		"absolute redirect url": {manifest + strings.Replace(oidc, "/auth/callback", "https://evil.example/cb", 1), compose, "redirect_paths"},
		"dot-dot redirect":      {manifest + strings.Replace(oidc, "/auth/callback", "/a/../b", 1), compose, "redirect_paths"},
		"bad backchannel":       {manifest + oidc + "  backchannel_logout_path: logout\n", compose, "backchannel_logout_path"},
		"no client name":        {manifest + strings.Replace(oidc, "  name: App\n", "", 1), compose, "oidc.name"},
		"no admin role":         {manifest + strings.Replace(oidc, "  admin_role: admin\n", "", 1), compose, "admin_role"},
		"bad admin role":        {manifest + strings.Replace(oidc, "admin_role: admin", "admin_role: .admin", 1), compose, "admin_role"},
		"same roles":            {manifest + strings.Replace(oidc, "user_role: user", "user_role: Admin", 1), compose, "differ from admin_role"},
		"third-party no user":   {strings.Replace(manifest, "category: ky", "category: third-party", 1) + strings.Replace(oidc, "  user_role: user\n", "", 1), compose, "everyday role"},
		"bad directory path":    {manifest + oidc + "  directory_path: api\n", compose, "directory_path"},
		"negative grace":        {manifest + kube + "  termination_grace_seconds: -1\n", compose, "termination_grace_seconds"},
		"absurd grace":          {manifest + kube + "  termination_grace_seconds: 86400\n", compose, "termination_grace_seconds"},
		"empty backup":          {manifest + "backup: {}\n", compose, "command or volumes"},
		"backup bad service":    {manifest + strings.Replace(backup, "service: web", "service: db", 1), composeVol, "not a Compose service"},
		"backup no service":     {manifest + strings.Replace(backup, "  service: web\n", "", 1), composeVol, "not a Compose service"},
		"backup empty arg":      {manifest + strings.Replace(backup, "-Fc", `""`, 1), composeVol, "empty argument"},
		"backup unknown volume": {manifest + backup, compose, "volumes"},
		"skip outside backup":   {manifest + strings.Replace(backup, "data/cache", "other/cache", 1), composeVol, "skip"},
		"skip whole volume":     {manifest + strings.Replace(backup, "data/cache", "data", 1), composeVol, "skip"},
		"skip dot-dot":          {manifest + strings.Replace(backup, "data/cache", "data/../x", 1), composeVol, "skip"},
		"capsule not a secret":  {manifest + strings.Replace(backup, "capsule_secrets: [db]", "capsule_secrets: [other]", 1), composeVol, "capsule_secrets"},
		"restore unknown":       {manifest + strings.Replace(backup, "restore_order: [web]", "restore_order: [db]", 1), composeVol, "restore_order"},
		"backup volume not k8s": {manifest + kube + backup, composeVol, "declared by every deployment"},
		"dump without restore":  {manifest + strings.Replace(backup, "  restore_command: [pg_restore]\n", "", 1), composeVol, "come together"},
		"restore without dump":  {manifest + strings.Replace(backup, "  command: [pg_dump, -Fc]\n", "", 1), composeVol, "come together"},
		"restore empty arg":     {manifest + strings.Replace(backup, "[pg_restore]", `[pg_restore, ""]`, 1), composeVol, "empty argument"},
		"restore order first":   {manifest + strings.Replace(backup, "restore_order: [web]", "restore_order: [db, web]", 1), composeTwo, "must start with service"},
		"replay after unknown":  {manifest + oidc + backup + "  replay_after: db\n", composeVol, "replay_after"},
		"replay without oidc":   {manifest + backup + "  replay_after: web\n", composeVol, "replay_after"},
		"restic_password":       {manifest + "secrets: [restic_password]\n", compose, "reserved"},

		"pod_security unknown":    {manifest + "pod_security: privileged\n", compose, "pod_security"},
		"pod_security case":       {manifest + "pod_security: Baseline\n", compose, "pod_security"},
		"caps under restricted":   {manifest + kube + "  capabilities: [CHOWN]\n", compose, "need pod_security baseline"},
		"writable under restrict": {manifest + kube + "  writable_root: true\n", compose, "need pod_security baseline"},
		"cap outside baseline":    {manifest + "pod_security: baseline\n" + kube + "  capabilities: [NET_ADMIN]\n", compose, "capabilities"},
		"cap repeated":            {manifest + "pod_security: baseline\n" + kube + "  capabilities: [KILL, KILL]\n", compose, "capabilities"},
		"node_ports not edge":     {manifest + kube + "  node_ports: {8080: 30080}\n", compose, "only edge components"},
		"node_port admin":         {edgeKube("ports: [81, 80]", "{81: 30081}"), compose, "admin API port"},
		"node_port unknown port":  {edgeKube("ports: [81, 80]", "{443: 30443}"), compose, "not one of ports"},
		"node_port out of range":  {edgeKube("ports: [81, 80]", "{80: 8080}"), compose, "30000-32767"},
		"node_port repeated":      {edgeKube("ports: [81, 80, 443]", "{80: 30080, 443: 30080}"), compose, "unique"},

		"dot-dot bind":         {manifest, compose + "    volumes: [./a/../b:/b]\n", "must be ./<name>"},
		"wildcard publication": {manifest, compose + "    ports: [\"0.0.0.0:80:80\"]\n", "must publish on"},
		"bare publication":     {manifest, compose + "    ports: [\"7881:7881\"]\n", "must publish on"},
		"parent bind":          {manifest, compose + "    volumes: [../x:/x]\n", "must be ./<name>"},
		"long volume form":     {manifest, compose + "    volumes:\n      - {type: bind, source: ./x, target: /x}\n", "short source:target"},
		"bind is volume":       {manifest, compose + "    volumes: [./data:/data]\nvolumes:\n  data:\n", "also a named volume"},
		"other compose func":   {manifest, compose + "    environment: {A: '{{ print 1 }}{{ nope }}'}\n", "nope"},
		"scim not secret":      {manifest + oidc + "  scim_token_file: /run/secrets/scim\n", compose, "scim_token_file"},
		"scim outside secret":  {manifest + "secrets: [scim]\n" + oidc + "  scim_token_file: /data/scim\n", compose, "scim_token_file"},

		"runner other uid":     {manifest + kube + runnerKube, compose, "suite_runner needs run_as 10001"},
		"runner no socket":     {manifest + strings.Replace(kube, "65532", "10001", 1) + "  suite_runner: true\n", compose, "KY_SUITE_RUNNER_SOCKET"},
		"runner mount taken":   {manifest + strings.Replace(kube, "65532", "10001", 1) + runnerKube + "  volumes:\n    - {name: x, mount: /run/kyquickstart, size: 1Gi}\n", compose, "the runner's"},
		"runner no kubernetes": {manifest + "suite_runner: true\n", compose, "field suite_runner not found"},

		"seed non-root":         {manifest + kube + "  volumes:\n    - {name: db, mount: /db, size: 1Gi, seed: true}\n", compose, "only a root app"},
		"secrets_dir relative":  {manifest + kube + "  secrets_dir: run/secrets\n", compose, "secrets_dir"},
		"secrets_dir tmp":       {manifest + kube + "  secrets_dir: /tmp\n", compose, "secrets_dir"},
		"volume on secrets":     {manifest + kube + "  secrets_dir: /keys\n  volumes:\n    - {name: k, mount: /keys, size: 1Gi}\n", compose, "the secrets'"},
		"egress unknown":        {manifest + kube + "  egress: none\n", compose, "egress"},
		"deny_paths unanchored": {manifest + "deny_paths: [/info]\n", compose, "deny_paths"},
		"deny_paths bad regexp": {manifest + "deny_paths: [\"^/(info\"]\n", compose, "deny_paths"},
		"egress on edge":        {edgeKube("ports: [81, 80]", "{80: 30080}") + "  egress: edge\n", compose, "egress"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"a/manifest.yaml":     {Data: []byte(c.manifest)},
				"a/compose.yaml.tmpl": {Data: []byte(c.compose)},
			}
			_, err := Load(fsys)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

// edgeKube is a baseline edge manifest with the given ports and node_ports.
func edgeKube(ports, nodePorts string) string {
	m := strings.Replace(manifest, "category: ky", "category: edge", 1) + "pod_security: baseline\n"
	return m + strings.Replace(kube, "ports: [8080]", ports, 1) + "  node_ports: " + nodePorts + "\n"
}

func TestPodSecurity(t *testing.T) {
	load := func(m string) (App, error) {
		cat, err := Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(m)}, "a/compose.yaml.tmpl": {Data: []byte(compose)}})
		return cat["a"], err
	}
	a, err := load(manifest + kube)
	if err != nil || a.PodSecurity != "restricted" {
		t.Fatalf("default = %q, %v", a.PodSecurity, err)
	}
	m := edgeKube("ports: [81, 80, 443]", "{80: 30080, 443: 30443}")
	m = strings.Replace(m, "run_as: 65532", "run_as: 0", 1) + "  capabilities: [CHOWN, KILL]\n  writable_root: true\n  low_ports: true\n"
	a, err = load(m)
	if err != nil {
		t.Fatal(err)
	}
	k := a.Kubernetes
	if a.PodSecurity != "baseline" || k.RunAs != 0 || len(k.Capabilities) != 2 || !k.WritableRoot || !k.LowPorts || k.NodePorts[443] != 30443 {
		t.Fatalf("baseline app = %+v %+v", a.Manifest, k)
	}
}

func TestFullManifest(t *testing.T) {
	m := strings.Replace(manifest, "category: ky", "category: third-party", 1) + oidc +
		"  backchannel_logout_path: /auth/backchannel\n  public: true\n" + backup +
		"upgrade:\n  min_from: 2.0.0\n  no_skip: [2.2.0, 2.3.10]\n  migrates_data: true\n"
	cat, err := Load(fstest.MapFS{
		"a/manifest.yaml":     {Data: []byte(m)},
		"a/compose.yaml.tmpl": {Data: []byte(composeVol)},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := cat["a"]
	if a.Version != "2.4.0" || !a.OIDC.Public || a.OIDC.BackchannelLogoutPath != "/auth/backchannel" {
		t.Errorf("oidc = %+v", a.OIDC)
	}
	if a.Backup.Service != "web" || a.Backup.RestoreCommand[0] != "pg_restore" || a.Backup.Skip[0] != "data/cache" || a.Backup.CapsuleSecrets[0] != "db" {
		t.Errorf("backup = %+v", a.Backup)
	}
	if a.Upgrade.MinFrom != "2.0.0" || len(a.Upgrade.NoSkip) != 2 || !a.Upgrade.MigratesData {
		t.Errorf("upgrade = %+v", a.Upgrade)
	}
}

func TestKubernetesBackup(t *testing.T) {
	m := manifest + kube + "  env:\n    LOG_LEVEL: info\n  volumes:\n    - name: data\n      mount: /data\n      size: 1Gi\n" +
		"backup:\n  command: [sqlite3, /data/app.db, .dump]\n  restore_command: [sqlite3, /data/app.db]\n  volumes: [data]\n"
	cat, err := Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(m)}})
	if err != nil {
		t.Fatal(err)
	}
	if a := cat["a"]; a.Kubernetes.Env["LOG_LEVEL"] != "info" || a.Backup.Volumes[0] != "data" {
		t.Fatalf("app = %+v", a)
	}
	_, err = Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(m + "  restore_order: [app]\n")}})
	if err == nil || !strings.Contains(err.Error(), "need compose.yaml.tmpl") {
		t.Fatalf("restore_order without Compose: err = %v", err)
	}
}

func TestWithHost(t *testing.T) {
	m := manifest + kube + "  env:\n    URL: https://{{host}}/a\n    MODE: x\n"
	cat, err := Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(m)}})
	if err != nil {
		t.Fatal(err)
	}
	k := cat["a"].Kubernetes
	got := k.WithHost("id.example.com")
	if !k.UsesHost() || got.UsesHost() || got.Env["URL"] != "https://id.example.com/a" || got.Env["MODE"] != "x" {
		t.Fatalf("env = %v", got.Env)
	}
	if k.Env["URL"] != "https://{{host}}/a" {
		t.Fatal("WithHost changed the catalog's env")
	}
}

func TestComposeRoute(t *testing.T) {
	tmpl := compose + "    environment: {URL: 'https://{{host}}/a'}\n    ports: ['{{forward}}:8080']\n    volumes: [./data:/data, ./data:/again, ./backups:/backups, cache:/cache, /anonymous]\nvolumes:\n  cache:\n"
	m := manifest + "secrets: [scim]\n" + oidc + "  scim_token_file: /run/secrets/scim\nbackup:\n  volumes: [data, cache]\n"
	cat, err := Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(m)}, "a/compose.yaml.tmpl": {Data: []byte(tmpl)}})
	if err != nil {
		t.Fatal(err)
	}
	a := cat["a"]
	if !a.UsesRoute() || !slices.Equal(a.Binds, []string{"backups", "data"}) || a.OIDC.SCIMTokenFile != "/run/secrets/scim" {
		t.Fatalf("app = %+v", a)
	}
	got := a.WithRoute(Route{Host: "drive.example.com:30443", Forward: "192.168.1.91:18443"})
	if got.UsesRoute() || !strings.Contains(string(got.Compose), "URL: 'https://drive.example.com:30443/a'") || !strings.Contains(string(got.Compose), "ports: ['192.168.1.91:18443:8080']") {
		t.Fatalf("compose:\n%s", got.Compose)
	}
	if !a.UsesRoute() {
		t.Fatal("WithRoute changed the catalog's Compose file")
	}
	if a, _ := Load(os.DirFS("testdata/apps")); a["hello"].UsesRoute() || a["hello"].Binds != nil {
		t.Fatal("hello takes a route or binds")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"1.2.3", "1.2.3", 0}, {"1.10.0", "1.9.9", 1}, {"0.9.0", "1.0.0", -1}, {"2.0.10", "2.0.2", 1},
		{"1.2.3.1", "1.2.3", 1}, {"1.2.3", "1.2.3.1", -1}, {"1.2.3.10", "1.2.3.9", 1}, {"1.2.3.4", "1.2.4", -1}, {"1.2.3.2", "1.2.3.2", 0}} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// A Ky product's automated dependency release adds a fourth number: X.Y.Z.N pins, upgrades
// from X.Y.Z and is gated by three-part min_from and no_skip.
func TestFourPartVersion(t *testing.T) {
	m := strings.Replace(manifest, "version: 2.4.0", "version: 2.4.0.2", 1) +
		"upgrade:\n  min_from: 2.0.0.1\n  no_skip: [2.2.0, 2.4.0.1]\n"
	cat, err := Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(m)}, "a/compose.yaml.tmpl": {Data: []byte(compose)}})
	if err != nil {
		t.Fatal(err)
	}
	u := cat["a"].Upgrade
	for installed, want := range map[string]string{
		"2.4.0.1": "",
		"2.4.0":   "would skip 2.4.0.1",
		"2.0.0":   "min_from 2.0.0.1",
		"2.4.0.3": "downgrades",
	} {
		err := u.Allows(installed, cat["a"].Version)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: err = %v, want %q", installed, err, want)
		}
	}
	if !VersionAtLeast("0.4.7.1", "0.4.7") || VersionAtLeast("0.4.6.9", "0.4.7") || VersionAtLeast("0.4.7.0", "0.4.7") {
		t.Error("VersionAtLeast does not order the fourth part")
	}
}

func TestKubernetesOnlyApp(t *testing.T) {
	fsys := fstest.MapFS{"a/manifest.yaml": {Data: []byte(manifest + kube)}}
	cat, err := Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	a := cat["a"]
	if a.Compose != nil || a.Services != nil || a.Kubernetes == nil || a.Kubernetes.RunAs != 65532 {
		t.Fatalf("app = %+v", a)
	}
}

func TestNoDeploymentAtAll(t *testing.T) {
	_, err := Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(manifest)}})
	if err == nil || !strings.Contains(err.Error(), "neither compose.yaml.tmpl nor kubernetes") {
		t.Fatalf("err = %v", err)
	}
}

func TestThirdPartyNeedsCompose(t *testing.T) {
	m := strings.Replace(manifest, "category: ky", "category: third-party", 1) + oidc
	_, err := Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(m)}})
	if err == nil || !strings.Contains(err.Error(), "compose.yaml.tmpl") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpgradeAllows(t *testing.T) {
	u := Upgrade{MinFrom: "2.0.0", NoSkip: []string{"2.2.0"}}
	for installed, want := range map[string]string{
		"2.2.0":    "",
		"2.2.5":    "",
		"2.4.0":    "",
		"2.1.9":    "would skip 2.2.0",
		"1.9.0":    "min_from 2.0.0",
		"2.5.0":    "downgrades",
		"2.0":      "not MAJOR.MINOR.PATCH",
		"2.10.0":   "downgrades",
		"2.2.0.3":  "",
		"2.1.9.9":  "would skip 2.2.0",
		"2.4.0.1":  "downgrades",
		"2.4.0.0":  "not MAJOR.MINOR.PATCH",
		"2.3.0.01": "not MAJOR.MINOR.PATCH",
	} {
		err := u.Allows(installed, "2.4.0")
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: err = %v, want %q", installed, err, want)
		}
	}
}
