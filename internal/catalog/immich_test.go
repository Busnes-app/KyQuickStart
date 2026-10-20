package catalog_test

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/catalog/catalogtest"
	"go.yaml.in/yaml/v3"
)

func immich(t *testing.T) catalog.App {
	t.Helper()
	cat, err := catalog.Load(catalogtest.FS())
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.Pinned("x@" + catalogtest.Digest) {
		t.Fatal("the stand-in digest is not a digest")
	}
	return cat["immich"]
}

// AC2, AC3: every field of the entry, every pin, and storage in ./<name> binds alone.
func TestImmichEntry(t *testing.T) {
	a := immich(t)
	m := a.Manifest
	if m.Name != "immich" || m.Category != "third-party" || m.Version != "3.2.0" || m.Kubernetes != nil || m.PodSecurity != "restricted" ||
		!slices.Equal(m.DependsOn, []string{"kyidentity"}) || !slices.Equal(m.Secrets, []string{"db_password", "scim_token"}) ||
		m.Health.TimeoutSeconds != 600 || !slices.Equal(m.Health.AfterSetup, []string{"directory"}) ||
		!slices.Equal(m.Components, []string{"server", "machine learning", "database (Postgres)", "cache (Valkey)", "bridge (kyoffboard)"}) {
		t.Fatalf("manifest = %+v", m)
	}
	want := map[string]string{
		"server":           "ghcr.io/busnes-app/kyq-immich-server:3.2.0-ky.1@" + catalogtest.Digest,
		"machine_learning": "ghcr.io/immich-app/immich-machine-learning:v3.2.0@sha256:30b81e0c6f68d80d6a8e6d0a8dddd7780e78d0c89e44d7701d9a4212ae8d90ca",
		"postgres":         "ghcr.io/immich-app/postgres:14-vectorchord0.4.3-pgvectors0.2.0@sha256:bcf63357191b76a916ae5eb93464d65c07511da41e3bf7a8416db519b40b1c23",
		"valkey":           "docker.io/valkey/valkey:9@sha256:70739f85ad2ee01a726a965584a0f94895f01b0c60b3cc8b0aeef11eaa6888cf",
		"directory":        "ghcr.io/busnes-app/kyoffboard:0.1.0@" + catalogtest.Digest,
	}
	if !maps.Equal(m.Images, want) {
		t.Fatalf("images = %v", m.Images)
	}
	for k, ref := range m.Images {
		if !catalog.Pinned(ref) {
			t.Errorf("image %s is not pinned: %s", k, ref)
		}
	}
	o := m.OIDC
	if o.Name != "Immich" || !slices.Equal(o.RedirectPaths, []string{"/auth/login", "/api/oauth/mobile-redirect"}) || o.AdminRole != "admin" || o.UserRole != "user" ||
		o.SCIMTokenFile != "/run/secrets/scim_token" || o.SCIMPath != "/scim/immich" || o.Public || len(o.LogoutPaths) != 0 ||
		o.DirectoryPath != "" || o.BackchannelLogoutPath != "" {
		t.Fatalf("oidc = %+v", o)
	}
	b := m.Backup
	if b.Service != "database" || !slices.Equal(b.Command, []string{"pg_dump", "-U", "postgres", "--clean", "--if-exists", "immich"}) ||
		len(b.RestoreCommand) != 3 || b.RestoreCommand[0] != "sh" || b.RestoreCommand[1] != "-c" ||
		!strings.Contains(b.RestoreCommand[2], "psql -U postgres -d immich --single-transaction -v ON_ERROR_STOP=1") ||
		!strings.Contains(b.RestoreCommand[2], "set_config('search_path', 'public, pg_catalog', true)") ||
		!strings.Contains(b.RestoreCommand[2], "COALESCE(value->'mountChecks', '{}') - 'thumbs' - 'encoded-video' - 'backups') WHERE key = 'system-flags';") ||
		!slices.Equal(b.Volumes, []string{"library", "setup", "directory"}) ||
		!slices.Equal(b.Skip, []string{"library/thumbs", "library/encoded-video", "library/backups"}) ||
		!slices.Equal(b.CapsuleSecrets, []string{"db_password", "scim_token"}) ||
		!slices.Equal(b.RestoreOrder, []string{"database", "redis", "immich-server", "immich-machine-learning", "directory"}) {
		t.Fatalf("backup = %+v", b)
	}
	if m.Upgrade.MinFrom != "3.2.0" || !m.Upgrade.MigratesData || len(m.Upgrade.NoSkip) != 0 {
		t.Fatalf("upgrade = %+v", m.Upgrade)
	}
	if !slices.Equal(a.Services, []string{"database", "directory", "immich-machine-learning", "immich-server", "redis"}) ||
		!slices.Equal(a.Binds, []string{"directory", "library", "model-cache", "postgres", "setup"}) || !a.UsesRoute() || !a.UsesBridge() {
		t.Fatalf("services %v, binds %v", a.Services, a.Binds)
	}
	var c struct {
		Services map[string]struct{ Volumes []string }
		Volumes  any
	}
	if err := yaml.Unmarshal(a.Compose, &c); err != nil {
		t.Fatal(err)
	}
	if c.Volumes != nil || strings.Contains(string(a.Compose), "\nvolumes:") {
		t.Fatal("compose declares named volumes")
	}
	bind := regexp.MustCompile(`^\./[a-z][a-z0-9-]*:/[a-z/-]+$`)
	for name, s := range c.Services {
		for _, v := range s.Volumes {
			if !bind.MatchString(v) {
				t.Errorf("service %s mounts %s, not a ./<name> bind", name, v)
			}
		}
	}
}

type immichService struct {
	Image       string
	User        string
	CapDrop     []string `yaml:"cap_drop"`
	CapAdd      []string `yaml:"cap_add"`
	SecurityOpt []string `yaml:"security_opt"`
	MemLimit    string   `yaml:"mem_limit"`
	Privileged  bool
	Ports       []string
	Networks    []string
	Environment map[string]string
	Healthcheck struct{ Test []string }
}

// AC4: on the rendered Compose file, only the server and the bridge publish, each on its own
// port of the forward address; the database and cache sit only on the internal network,
// machine learning off it; every service is non-root, capability-free and bounded.
func TestImmichComposeHardening(t *testing.T) {
	a := immich(t).WithRoute(catalog.Route{Host: "photos.example.com", Forward: "192.168.1.91:18443", ForwardAddr: "192.168.1.91", BridgeForward: "192.168.1.91:18444"})
	if a.UsesRoute() {
		t.Fatalf("unrendered route references:\n%s", a.Compose)
	}
	var c struct {
		Services map[string]immichService
		Networks map[string]struct{ Internal bool }
		Secrets  map[string]struct{ File string }
	}
	if err := yaml.Unmarshal(a.Compose, &c); err != nil {
		t.Fatal(err)
	}
	ports := map[string][]string{"immich-server": {"192.168.1.91:18443:2283"}, "directory": {"192.168.1.91:18444:8443"}}
	networks := map[string][]string{"immich-server": {"default", "internal"}, "directory": {"default", "internal"}, "immich-machine-learning": {"default"}, "database": {"internal"}, "redis": {"internal"}}
	if len(c.Services) != 5 || !c.Networks["internal"].Internal || len(c.Networks) != 1 {
		t.Fatalf("services %d, networks %+v", len(c.Services), c.Networks)
	}
	secretName := regexp.MustCompile(`PASSWORD|SECRET|TOKEN|KEY`)
	for name, s := range c.Services {
		if !slices.Equal(s.Ports, ports[name]) || !slices.Equal(s.Networks, networks[name]) {
			t.Errorf("%s: ports %v, networks %v", name, s.Ports, s.Networks)
		}
		if s.User != "${KYQ_UID:?}:${KYQ_GID:?}" || !slices.Equal(s.CapDrop, []string{"ALL"}) || s.CapAdd != nil || s.Privileged ||
			!slices.Equal(s.SecurityOpt, []string{"no-new-privileges:true"}) || s.MemLimit == "" || len(s.Healthcheck.Test) < 2 || s.Healthcheck.Test[0] != "CMD" {
			t.Errorf("%s: %+v", name, s)
		}
		for k, v := range s.Environment {
			if secretName.MatchString(k) && (!strings.HasSuffix(k, "_FILE") || !strings.HasPrefix(v, "/run/secrets/")) {
				t.Errorf("%s: env %s holds a secret, not a file", name, k)
			}
		}
	}
	if len(c.Secrets) != 1 || c.Secrets["db_password"].File != "./secrets/db_password" {
		t.Fatalf("secrets = %+v", c.Secrets)
	}
}
