package catalog

import (
	"flag"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/rendered.golden.yaml")

// messageRoute is a plan's routes for KyMessages behind a lan edge on client port 30443.
var messageRoute = Route{
	Host: "message.example.com:30443", Hostname: "message.example.com", Forward: "192.168.1.91:18080", ForwardAddr: "192.168.1.91",
	EdgeAddress: "192.168.1.10", MediaAddress: "192.168.1.91",
	Roles: map[string]RoleAddress{
		"server_name": {"example.com:30443", "example.com", "192.168.1.91:18080"},
		"matrix":      {"matrix.example.com:30443", "matrix.example.com", "192.168.1.91:18008"},
		"auth":        {"auth.example.com:30443", "auth.example.com", "192.168.1.91:18081"},
		"chat":        {"chat.example.com:30443", "chat.example.com", "192.168.1.91:18082"},
		"rtc":         {"rtc.example.com:30443", "rtc.example.com", "192.168.1.91:17880"},
	},
}

type composeService struct {
	Image       string            `yaml:"image"`
	User        string            `yaml:"user"`
	CapDrop     []string          `yaml:"cap_drop"`
	CapAdd      []string          `yaml:"cap_add"`
	SecurityOpt []string          `yaml:"security_opt"`
	Privileged  bool              `yaml:"privileged"`
	NetworkMode string            `yaml:"network_mode"`
	ReadOnly    bool              `yaml:"read_only"`
	MemLimit    string            `yaml:"mem_limit"`
	MemSwap     string            `yaml:"memswap_limit"`
	PidsLimit   int               `yaml:"pids_limit"`
	Profiles    []string          `yaml:"profiles"`
	Healthcheck map[string]any    `yaml:"healthcheck"`
	Environment map[string]string `yaml:"environment"`
	Ports       []string          `yaml:"ports"`
	Networks    any               `yaml:"networks"`
	Build       any               `yaml:"build"`
}

func messageCompose(t *testing.T) (App, map[string]composeService, map[string]map[string]bool) {
	t.Helper()
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := cat["kymessage"]
	var c struct {
		Services map[string]composeService  `yaml:"services"`
		Networks map[string]map[string]bool `yaml:"networks"`
	}
	r := a.WithRoute(messageRoute)
	if err := yaml.Unmarshal(r.Compose, &c); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(r.Compose), "{{") {
		t.Fatalf("unreplaced reference:\n%s", r.Compose)
	}
	return a, c.Services, c.Networks
}

// AC4: six roles, the console primary, the media ports and both clients.
func TestKyMessageEntry(t *testing.T) {
	a, _, _ := messageCompose(t)
	if a.Version != "0.4.4" || a.Images["kymessage"] != "ghcr.io/busnes-app/kymessage-server:0.4.4@sha256:3ecb7a412877bbdd782bc1a3ef0f0af7fbab859cd8a77abb7f787d67122d35b0" {
		t.Fatalf("version %s, image %s", a.Version, a.Images["kymessage"])
	}
	var roles []string
	for _, r := range a.Routes {
		roles = append(roles, r.Role)
	}
	if !slices.Equal(roles, []string{"console", "server_name", "matrix", "auth", "chat", "rtc"}) || a.Primary() != "console" {
		t.Fatalf("roles = %v", roles)
	}
	want := map[string]RouteRole{
		"server_name": {Role: "server_name", Service: "app", Port: 8080, Paths: []string{"/.well-known/matrix/client", "/.well-known/matrix/server"}},
		"matrix":      {Role: "matrix", Service: "synapse", Port: 8008, DenyPaths: []string{"^/_synapse/(admin|mas)/"}},
		"rtc":         {Role: "rtc", Service: "livekit", Port: 7880, DenyPaths: []string{"^/twirp/"}},
		"auth":        {Role: "auth", Service: "mas", Port: 8080},
		"chat":        {Role: "chat", Service: "element", Port: 8080},
	}
	for _, r := range a.Routes[1:] {
		if w := want[r.Role]; !slices.Equal(r.Paths, w.Paths) || !slices.Equal(r.DenyPaths, w.DenyPaths) || r.Service != w.Service || r.Port != w.Port {
			t.Errorf("route %s = %+v", r.Role, r)
		}
	}
	if !slices.Equal(a.Media.Ports, []string{"7881/tcp", "7882/udp"}) {
		t.Errorf("media = %v", a.Media)
	}
	o := a.OIDC
	if o.Name != "KyMessages" || !slices.Equal(o.RedirectPaths, []string{"/api/sso/kyidentity/callback"}) || o.AdminRole != "kymessage.admin" || o.UserRole != "kymessage.user" || o.DirectoryPath != "/api/sso/kyidentity/sync" || o.SCIMTokenFile != "" {
		t.Errorf("oidc = %+v", o)
	}
	if c := *a.ChatOIDC; c != (ChatOIDC{Name: "KyMessages chat", Role: "auth", ClientSuffix: "chat", EverydayRole: "kymessage.chat", RedirectPath: "/upstream/callback/{provider_id}"}) {
		t.Errorf("chat_oidc = %+v", c)
	}
	if a.Kubernetes != nil || !slices.Equal(a.Init, []string{"matrix-init"}) || !slices.Equal(a.Services, []string{"app", "element", "livekit", "mas", "postgres", "synapse"}) {
		t.Errorf("kubernetes %v, init %v, services %v", a.Kubernetes, a.Init, a.Services)
	}
	if !slices.Equal(a.Binds, []string{"backups", "data", "matrix", "media", "pgdata"}) || !slices.Contains(a.Nested, "./matrix/mas/config.yaml") {
		t.Errorf("binds %v nested %v", a.Binds, a.Nested)
	}
	b := a.Backup
	if b.Service != "postgres" || !slices.Equal(b.Volumes, []string{"data", "backups", "matrix", "media"}) || b.RestoreOrder[0] != "postgres" || b.ReplayAfter != "app" || a.Upgrade.MigratesData || a.Upgrade.MinFrom != "0.4.3" || a.Upgrade.Allows("0.4.3", a.Version) != nil {
		t.Errorf("backup %+v upgrade %+v", b, a.Upgrade)
	}
}

// AC5: hardened services, internal-only Postgres and MAS admin, no secret in environment,
// within 3 GiB.
func TestKyMessageComposeHygiene(t *testing.T) {
	_, services, networks := messageCompose(t)
	if !networks["matrix-db"]["internal"] || !networks["matrix-admin"]["internal"] {
		t.Fatalf("networks = %v", networks)
	}
	var total int
	for name, s := range services {
		if !slices.Equal(s.CapDrop, []string{"ALL"}) || !slices.Contains(s.SecurityOpt, "no-new-privileges:true") || s.User != "${KYQ_UID:?}:${KYQ_GID:?}" || !s.ReadOnly ||
			s.MemLimit == "" || s.MemLimit != s.MemSwap || s.PidsLimit == 0 || s.Privileged || len(s.CapAdd) > 0 || s.NetworkMode == "host" || s.Build != nil {
			t.Errorf("service %s is not hardened: %+v", name, s)
		}
		if s.Healthcheck == nil && !slices.Equal(s.Profiles, []string{"init"}) {
			t.Errorf("service %s has no healthcheck", name)
		}
		// Compose refuses start_interval without start_period.
		if s.Healthcheck["start_interval"] != nil && s.Healthcheck["start_period"] == nil {
			t.Errorf("service %s: start_interval without start_period", name)
		}
		total += memMiB(t, s.MemLimit)
		for k, v := range s.Environment {
			if strings.HasPrefix(k, "KY_KYIDENTITY_") || k == "KY_SCIM_TOKEN" || k == "KY_ADMIN_PASSWORD" || k == "KY_BACKUP_KEEP" || k == "KY_BACKUP_DEPOSIT_INTERVAL" || k == "KY_SESSION_SECRET" ||
				strings.HasPrefix(k, "KY_MATRIX_") && strings.HasSuffix(k, "SECRET") || strings.HasPrefix(k, "POSTGRES_PASSWORD") && k != "POSTGRES_PASSWORD_FILE" {
				t.Errorf("service %s sets %s=%q", name, k, v)
			}
		}
	}
	if services["app"].Environment["KY_SCIM_ENABLED"] != "false" || services["app"].Environment["KY_SSO_AUTO_PROVISION"] != "false" || services["app"].Environment["KY_ENV"] != "production" {
		t.Errorf("app environment = %v", services["app"].Environment)
	}
	if total > 3<<10 {
		t.Errorf("mem_limit sum %d MiB > 3 GiB", total)
	}
	for name, want := range map[string]any{"postgres": []any{"matrix-db"}} {
		if !slices.Equal(services[name].Networks.([]any), want.([]any)) {
			t.Errorf("%s networks = %v", name, services[name].Networks)
		}
	}
	// MAS's admin listener binds its alias on matrix-admin, which only it and the console join.
	for name, s := range services {
		if n, ok := s.Networks.(map[string]any); ok && n["matrix-admin"] != nil && name != "mas" || slices.Contains(asList(s.Networks), "matrix-admin") && name != "app" {
			t.Errorf("%s joins matrix-admin", name)
		}
	}
}

func asList(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			out = append(out, x.(string))
		}
	}
	return out
}

func memMiB(t *testing.T, s string) int {
	t.Helper()
	var n int
	var unit string
	for i, c := range s {
		if c < '0' || c > '9' {
			n, unit = atoi(s[:i]), s[i:]
			break
		}
	}
	switch unit {
	case "m":
		return n
	case "g":
		return n << 10
	}
	t.Fatalf("mem_limit %q", s)
	return 0
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// AC6: after WithRoute KyMessages publishes exactly its five forwards and the media ports.
func TestKyMessagePublishes(t *testing.T) {
	_, services, _ := messageCompose(t)
	var got []string
	for _, s := range services {
		got = append(got, s.Ports...)
	}
	sort.Strings(got)
	want := []string{"192.168.1.91:17880:7880", "192.168.1.91:18008:8008", "192.168.1.91:18080:8080", "192.168.1.91:18081:8080", "192.168.1.91:18082:8080",
		"192.168.1.91:7881:7881/tcp", "192.168.1.91:7882:7882/udp"}
	if !slices.Equal(got, want) {
		t.Fatalf("ports = %v", got)
	}
}

// AC2: every catalog service image is a manifest key, every manifest image a digest.
func TestNoTagOnlyImage(t *testing.T) {
	imageLine := regexp.MustCompile(`(?m)^\s+image:\s*(.*)$`)
	ref := regexp.MustCompile(`^\{\{ \.Images\.[a-z][a-z0-9_]* \}\}$`)
	for _, root := range []fs.FS{Embedded(), Demo()} {
		dirs, _ := fs.ReadDir(root, ".")
		for _, d := range dirs {
			m, _ := fs.ReadFile(root, d.Name()+"/manifest.yaml")
			var man Manifest
			if err := yaml.Unmarshal(m, &man); err != nil {
				t.Fatal(err)
			}
			for k, img := range man.Images {
				if !strings.Contains(img, "@sha256:") {
					t.Errorf("%s image %s = %s is tag-only", d.Name(), k, img)
				}
			}
			b, err := fs.ReadFile(root, d.Name()+"/compose.yaml.tmpl")
			if err != nil {
				continue
			}
			for _, l := range imageLine.FindAllStringSubmatch(string(b), -1) {
				if !ref.MatchString(strings.TrimSpace(l[1])) {
					t.Errorf("%s: image %q is not {{ .Images.<key> }}", d.Name(), l[1])
				}
			}
		}
	}
	// Load itself refuses a pinned-looking image the manifest does not declare.
	_, err := Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(manifest)}, "a/compose.yaml.tmpl": {Data: []byte(strings.Replace(compose, "{{ .Images.web }}", "traefik/whoami:v1.11.0", 1))}})
	if err == nil || !strings.Contains(err.Error(), "not one of the manifest's pinned images") {
		t.Fatalf("tag-only image: %v", err)
	}
}

// AC4: every other app renders byte for byte as on origin/master (testdata/rendered.golden.yaml
// was written by origin/master 890b571's catalog.go; KyIdentity's image line since follows its 0.4.6 pin).
// BookStack's own tests pin its rendering.
func TestRenderedGolden(t *testing.T) {
	sub := fstest.MapFS{}
	fs.WalkDir(Embedded(), ".", func(p string, d fs.DirEntry, err error) error {
		if !d.IsDir() && !strings.HasPrefix(p, "kymessage/") && !strings.HasPrefix(p, "bookstack/") {
			b, _ := fs.ReadFile(Embedded(), p)
			sub[p] = &fstest.MapFile{Data: b}
		}
		return err
	})
	cat, err := Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]string{}
	for name, a := range cat {
		k, _ := yaml.Marshal(a.Kubernetes)
		got[name] = map[string]string{"compose": string(a.WithRoute(Route{Host: "h.example.com", Forward: "192.0.2.9:1", ForwardAddr: "192.0.2.9", EdgeAddress: "192.0.2.8"}).Compose), "kubernetes": string(k)}
	}
	b, _ := yaml.Marshal(got)
	if *updateGolden {
		os.WriteFile("testdata/rendered.golden.yaml", b, 0o644)
	}
	want, _ := os.ReadFile("testdata/rendered.golden.yaml")
	if string(b) != string(want) {
		t.Fatal("rendered Compose or Kubernetes output differs from testdata/rendered.golden.yaml")
	}
}

const (
	routesManifest = manifest + oidc + "routes:\n  - {role: console, service: web, port: 8080}\n  - {role: chat, service: web, port: 9090}\n"
	routesCompose  = compose + "    ports: ['{{forward}}:8080', '{{forward \"chat\"}}:9090']\n    environment: {A: 'https://{{host \"chat\"}}'}\n"
)

// AC4: refusals of the new fields; each case is the mutation of a passing manifest.
func TestRoutesRejects(t *testing.T) {
	load := func(m, c string) error {
		_, err := Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(m)}, "a/compose.yaml.tmpl": {Data: []byte(c)}})
		return err
	}
	if err := load(routesManifest, routesCompose); err != nil {
		t.Fatalf("valid routes: %v", err)
	}
	media := "media:\n  ports: [7881/tcp]\n"
	mediaCompose := strings.Replace(routesCompose, "'{{forward \"chat\"}}:9090'", "'{{forward \"chat\"}}:9090', '{{media_address}}:7881:7881/tcp'", 1)
	if err := load(routesManifest+media, mediaCompose); err != nil {
		t.Fatalf("valid media: %v", err)
	}
	chat := "chat_oidc:\n  name: Chat\n  role: chat\n  client_suffix: chat\n  everyday_role: a.chat\n  redirect_path: /cb/{provider_id}\n"
	if err := load(routesManifest+chat, routesCompose); err != nil {
		t.Fatalf("valid chat_oidc: %v", err)
	}
	for name, c := range map[string]struct{ manifest, compose, want string }{
		"unknown role reference": {routesManifest, strings.Replace(routesCompose, `{{host "chat"}}`, `{{host "rtc"}}`, 1), "names no non-primary role"},
		"host of a missing role": {manifest + oidc, strings.Replace(routesCompose, `'{{forward "chat"}}:9090'`, "'{{forward_addr}}:9090:9090'", 1), "names no non-primary role"},
		"duplicate role":         {strings.Replace(routesManifest, "role: chat", "role: console", 1), routesCompose, "must be unique"},
		"paths and deny_paths":   {strings.Replace(routesManifest, "port: 9090}", "port: 9090, paths: [/a], deny_paths: ['^/b']}", 1), routesCompose, "exclude each other"},
		"unpublished port":       {strings.Replace(routesManifest, "port: 9090", "port: 9091", 1), routesCompose, "does not publish port 9091"},
		"no such service":        {strings.Replace(routesManifest, "service: web, port: 9090", "service: db, port: 9090", 1), routesCompose, "does not publish"},
		"media without ref":      {routesManifest + media, routesCompose, "media: ports need"},
		"ref without media":      {routesManifest, mediaCompose, "needs media"},
		"bad media port":         {routesManifest + strings.Replace(media, "tcp", "sctp", 1), mediaCompose, "media: port"},
		"unanchored deny":        {strings.Replace(routesManifest, "port: 9090}", "port: 9090, deny_paths: ['/admin']}", 1), routesCompose, "anchored"},
		"nginx-breaking path":    {strings.Replace(routesManifest, "port: 9090}", "port: 9090, paths: ['/a;b']}", 1), routesCompose, "exact absolute path"},
		"routes on kubernetes":   {routesManifest + kube, routesCompose, "Kubernetes app is one container"},
		"chat on primary":        {routesManifest + strings.Replace(chat, "role: chat", "role: console", 1), routesCompose, "non-primary role"},
		"chat with admin roles":  {routesManifest + strings.Replace(chat, "a.chat", "admin", 1), routesCompose, "differ from oidc's roles"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := load(c.manifest, c.compose); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// AC1: the five stock images are exactly the digests KyMessage's docker-compose.matrix.yml
// pins. The fixture is that file byte for byte (KyMessage-Server v0.4.4).
func TestKyMessageStockImages(t *testing.T) {
	b, err := os.ReadFile("testdata/kymessage-docker-compose.matrix.yml")
	if err != nil {
		t.Fatal(err)
	}
	var upstream []string
	for _, m := range regexp.MustCompile(`(?m)^\s+image: ([^$\s]+@sha256:[0-9a-f]{64})$`).FindAllStringSubmatch(string(b), -1) {
		if !slices.Contains(upstream, m[1]) {
			upstream = append(upstream, m[1])
		}
	}
	a, _, _ := messageCompose(t)
	var ours []string
	for k, img := range a.Images {
		if k != "kymessage" {
			ours = append(ours, img)
		}
	}
	sort.Strings(upstream)
	sort.Strings(ours)
	if len(upstream) != 5 || !slices.Equal(upstream, ours) {
		t.Fatalf("stock images %v, upstream %v", ours, upstream)
	}
}
