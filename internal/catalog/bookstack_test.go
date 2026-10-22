package catalog

import (
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Busnes-app/kyquickstart/internal/stockbookstack"
	"go.yaml.in/yaml/v3"
)

var bookstackRoute = Route{
	Host: "wiki.example.com:30443", Hostname: "wiki.example.com", Forward: "192.168.1.91:18080", ForwardAddr: "192.168.1.91",
	EdgeAddress: "192.168.1.10",
	Roles:       map[string]RoleAddress{"directory": {"wiki-directory.example.com:30443", "wiki-directory.example.com", "192.168.1.91:18081"}},
}

// bookstackFS is the embedded catalog with BookStack's manifest edited by edit.
func bookstackFS(t *testing.T, edit func(string) string) fs.FS {
	t.Helper()
	sub := fstest.MapFS{}
	fs.WalkDir(Embedded(), ".", func(p string, d fs.DirEntry, err error) error {
		if !d.IsDir() {
			b, _ := fs.ReadFile(Embedded(), p)
			if p == "bookstack/manifest.yaml" {
				b = []byte(edit(string(b)))
			}
			sub[p] = &fstest.MapFile{Data: b}
		}
		return err
	})
	return sub
}

// AC2: the entry's routes, directory route, backup and upgrade.
func TestBookStackEntry(t *testing.T) {
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := cat["bookstack"]
	if a.Category != "third-party" || a.Version != "26.9.2" || a.Kubernetes != nil || !slices.Equal(a.DependsOn, []string{"kyidentity"}) {
		t.Fatalf("entry: %+v", a.Manifest)
	}
	for k, ref := range a.Images {
		if !Pinned(ref) {
			t.Errorf("image %s %s is not pinned", k, ref)
		}
	}
	if len(a.Images)+len(a.PendingImages) != 3 || a.Images["bookstack"] == "" || a.Images["mariadb"] == "" {
		t.Errorf("images %v, pending %v", a.Images, a.PendingImages)
	}
	if len(a.Routes) != 2 || a.Primary() != "app" || a.Routes[0].Service != "bookstack" || a.Routes[0].Port != 80 ||
		a.Routes[1].Role != "directory" || a.Routes[1].Service != "directory" || !slices.Equal(a.Routes[1].Paths, []string{"/kyq/directory"}) || !a.Routes[1].Webhook || a.Routes[0].Webhook {
		t.Fatalf("routes %+v", a.Routes)
	}
	deny := func(path string) bool {
		return slices.ContainsFunc(a.Routes[0].DenyPaths, func(re string) bool { return regexp.MustCompile(re).MatchString(path) })
	}
	for _, p := range []string{"/my-account", "/my-account/", "/my-account/delete", "/index.php/my-account", "/index.php/my-account/delete/"} {
		if !deny(p) {
			t.Errorf("the edge serves self-delete at %s", p)
		}
	}
	for _, p := range []string{"/my-account/profile", "/my-account/auth", "/books", "/"} {
		if deny(p) {
			t.Errorf("the edge closes %s", p)
		}
	}
	o := a.OIDC
	if o.DirectoryRole != "directory" || o.DirectoryPath != "/kyq/directory" || o.AdminRole != "bookstack.admin" || o.UserRole != "bookstack.user" || !slices.Equal(o.RedirectPaths, []string{"/oidc/callback"}) {
		t.Fatalf("oidc %+v", o)
	}
	b := a.Backup
	if b.Service != "mariadb" || b.ReplayAfter != "directory" || !slices.Equal(b.RestoreOrder, []string{"mariadb", "bookstack", "directory"}) ||
		!slices.Equal(b.CapsuleSecrets, a.Secrets) || !slices.Equal(b.Volumes, []string{"config", "data", "token"}) {
		t.Fatalf("backup %+v", b)
	}
	for _, c := range [][]string{b.Command, b.RestoreCommand} {
		if strings.Contains(strings.Join(c, " "), "-p") || !strings.Contains(c[2], "--defaults-extra-file") {
			t.Errorf("the dump's password must come from a private file, not argv: %q", c)
		}
	}
	if !a.Upgrade.MigratesData || a.Upgrade.MinFrom != "26.9.2" || !strings.Contains(a.Upgrade.Note, "back it up") {
		t.Fatalf("upgrade %+v", a.Upgrade)
	}
	if a.SecretFormats["app_key"] != "laravel" {
		t.Fatalf("app_key format %q", a.SecretFormats["app_key"])
	}
}

// AC2: directory_role names a route that serves directory_path.
func TestDirectoryRoleRefused(t *testing.T) {
	for _, c := range []struct{ name, from, to, want string }{
		{"missing route", "directory_role: directory", "directory_role: hooks", `oidc.directory_role "hooks"`},
		{"paths lack directory_path", "paths: [/kyq/directory]", "paths: [/kyq/other]", `oidc.directory_role "directory"`},
		{"primary named", "directory_role: directory", "directory_role: app", `oidc.directory_role "app"`},
		{"unpinned pending image", "pending_images: [kyoffboard]", "pending_images: [mariadb]", "pending_images"},
		{"webhook without paths", "deny_paths: [", "webhook: true, deny_paths: [", "webhook needs paths"},
		{"unknown secret format", "secret_formats: {app_key: laravel}", "secret_formats: {app_key: hex}", "secret_formats"},
	} {
		_, err := Load(bookstackFS(t, func(s string) string {
			if !strings.Contains(s, c.from) {
				t.Fatalf("%s: manifest lacks %q", c.name, c.from)
			}
			return strings.Replace(s, c.from, c.to, 1)
		}))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// AC3: the rendered Compose file's hardening, networks, publications and secrets.
func TestBookStackComposeHygiene(t *testing.T) {
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := cat["bookstack"].WithRoute(bookstackRoute)
	if strings.Contains(string(a.Compose), "{{") {
		t.Fatalf("unreplaced reference:\n%s", a.Compose)
	}
	var c struct {
		Services map[string]struct {
			composeService `yaml:",inline"`
			CPUs           float64  `yaml:"cpus"`
			Tmpfs          []string `yaml:"tmpfs"`
			Command        []string `yaml:"command"`
			Secrets        []string `yaml:"secrets"`
		} `yaml:"services"`
		Networks map[string]map[string]bool `yaml:"networks"`
	}
	if err := yaml.Unmarshal(a.Compose, &c); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.Services, []string{"bookstack", "directory", "mariadb"}) || !slices.Equal(a.Init, []string{"bootstrap", "standard"}) {
		t.Fatalf("services %v, one-shots %v", a.Services, a.Init)
	}
	mib := func(v string) int {
		n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(v, "m"), "g"))
		if strings.HasSuffix(v, "g") {
			n *= 1024
		}
		return n
	}
	total := 0
	var published []string
	for name, s := range c.Services {
		// Pinned: the target user, no capability, the stricter of the two modes the spec allows.
		if s.User != "${KYQ_UID:?}:${KYQ_GID:?}" || !slices.Equal(s.CapDrop, []string{"ALL"}) || len(s.CapAdd) > 0 ||
			!slices.Equal(s.SecurityOpt, []string{"no-new-privileges:true"}) || !s.ReadOnly || s.Privileged || s.NetworkMode != "" || s.Build != nil {
			t.Errorf("%s: user %q cap_drop %v cap_add %v security_opt %v read_only %v", name, s.User, s.CapDrop, s.CapAdd, s.SecurityOpt, s.ReadOnly)
		}
		if mib(s.MemLimit) == 0 || s.MemLimit != s.MemSwap || s.PidsLimit <= 0 || s.CPUs <= 0 {
			t.Errorf("%s: mem_limit %q memswap_limit %q pids_limit %d cpus %v", name, s.MemLimit, s.MemSwap, s.PidsLimit, s.CPUs)
		}
		if len(s.Profiles) == 0 {
			total += mib(s.MemLimit)
			if s.Healthcheck == nil {
				t.Errorf("%s has no healthcheck", name)
			}
		}
		for k, v := range s.Environment {
			if strings.ContainsAny(k, "_") && regexp.MustCompile(`PASSWORD|SECRET|KEY|TOKEN`).MatchString(k) &&
				!strings.HasPrefix(k, "FILE__") && !strings.HasSuffix(k, "_FILE") {
				t.Errorf("%s: %s=%q is a secret in the environment", name, k, v)
			}
		}
		published = append(published, s.Ports...)
		if name == "bookstack" || name == "standard" {
			if !slices.Contains(s.Tmpfs, "/run:uid=${KYQ_UID},gid=${KYQ_GID},mode=0755,exec") {
				t.Errorf("%s: tmpfs %v", name, s.Tmpfs)
			}
			if s.Environment["DOCKER_MODS"] != "" {
				t.Errorf("%s sets DOCKER_MODS", name)
			}
		}
	}
	t.Logf("AC3: long-running services' mem_limit sum %d MiB", total)
	if total > 1536 {
		t.Errorf("mem_limit sum %d MiB is over 1.5 GiB", total)
	}
	slices.Sort(published)
	if !slices.Equal(published, []string{"192.168.1.91:18080:80", "192.168.1.91:18081:8080"}) {
		t.Errorf("publications %v", published)
	}
	if !c.Networks["db"]["internal"] || !c.Networks["bootstrap"]["internal"] {
		t.Errorf("networks %v", c.Networks)
	}
	nets := func(name string) []string {
		var out []string
		yaml.Unmarshal(must(yaml.Marshal(c.Services[name].Networks)), &out)
		return out
	}
	for name, want := range map[string][]string{"mariadb": {"db"}, "bookstack": {"default", "db"}, "standard": {"db", "bootstrap"}, "bootstrap": {"bootstrap"}, "directory": nil} {
		if got := nets(name); !slices.Equal(got, want) {
			t.Errorf("%s networks %v, want %v", name, got, want)
		}
	}
	env := c.Services["bookstack"].Environment
	for k, v := range map[string]string{"AUTH_METHOD": "oidc", "APP_URL": "https://wiki.example.com:30443", "APP_PROXIES": "192.168.1.10",
		"STORAGE_TYPE": "local_secure_restricted", "SESSION_LIFETIME": "60", "OIDC_GROUPS_CLAIM": "roles", "OIDC_REMOVE_FROM_GROUPS": "true",
		"OIDC_USER_TO_GROUPS": "true", "OIDC_FETCH_AVATAR": "false", "OIDC_DUMP_USER_DETAILS": "false", "OIDC_ISSUER_DISCOVER": "true"} {
		if env[k] != v {
			t.Errorf("bookstack %s = %q, want %q", k, env[k], v)
		}
	}
	if !slices.Equal(a.Nested, []string{"./data/oidc_client_id", "./data/oidc_client_secret", "./data/oidc_issuer"}) {
		t.Errorf("BookStack mounts %v of data, want its three sign-in files", a.Nested)
	}
	if c.Services["standard"].Environment["AUTH_METHOD"] != "standard" || len(c.Services["standard"].Ports) != 0 {
		t.Error("the bootstrap BookStack must be standard-auth with no published port")
	}
	for k := range env {
		if strings.HasPrefix(k, "MAIL_") {
			t.Errorf("bookstack sets %s", k)
		}
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// The receiver's built-in claims are the catalog client's roles.
func TestBookStackReceiverClaims(t *testing.T) {
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	if o := cat["bookstack"].OIDC; o.AdminRole != stockbookstack.AdminClaim || o.UserRole != stockbookstack.UserClaim || o.DirectoryPath != stockbookstack.DirectoryPath {
		t.Fatalf("oidc %+v", o)
	}
}
