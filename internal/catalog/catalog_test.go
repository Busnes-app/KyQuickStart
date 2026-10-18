package catalog

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"
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

func TestKyIdentityEntry(t *testing.T) {
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := cat["kyidentity"]
	k := a.Kubernetes
	if a.Category != "ky" || a.Version != "0.4.2" || a.PodSecurity != "restricted" || a.OIDC != nil || a.Compose != nil || len(a.Secrets) != 0 {
		t.Fatalf("manifest = %+v", a.Manifest)
	}
	if !strings.HasPrefix(a.Images[k.Image], "ghcr.io/busnes-app/kyidentity-server:0.4.2@sha256:") || k.RunAs != 100 || k.WritableRoot || len(k.Capabilities) != 0 {
		t.Fatalf("kubernetes = %+v", k)
	}
	if k.Env["KYIDENTITY_ISSUER_URL"] != "https://"+HostRef || k.ReadinessPath != "/readyz" || k.Ports[0] != 5867 || k.Volumes[0].Mount != k.Env["KYIDENTITY_DATA_DIR"] {
		t.Fatalf("env %v, probe %s, volumes %+v", k.Env, k.ReadinessPath, k.Volumes)
	}
	if !a.Upgrade.MigratesData || a.Upgrade.MinFrom != "0.4.0" || a.Backup.Volumes[0] != "data" {
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
	if a.Category != "ky" || a.Version != "0.4.2" || a.PodSecurity != "restricted" || a.Compose != nil || len(a.Secrets) != 0 || len(a.DependsOn) != 1 || a.DependsOn[0] != "kyidentity" {
		t.Fatalf("manifest = %+v", a.Manifest)
	}
	if !strings.HasPrefix(a.Images[k.Image], "ghcr.io/busnes-app/kyrecovery-server:0.4.2@sha256:") || k.RunAs != 10001 || k.ReadinessPath != "/healthz" || k.Ports[0] != 8095 || k.Volumes[0].Mount != k.Env["KYRECOVERY_DATA_DIR"] || k.UsesHost() {
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

const manifest = "name: a\ncategory: ky\nversion: 2.4.0\nimages:\n  web: " + digest + "\nhealth:\n  timeout_seconds: 30\n"

const oidc = "oidc:\n  name: App\n  redirect_paths: [/auth/callback]\n  logout_paths: [/]\n  admin_role: admin\n  user_role: user\n"

const backup = "secrets: [db]\nbackup:\n  service: web\n  command: [pg_dump, -Fc]\n  volumes: [data]\n  skip: [data/cache]\n  capsule_secrets: [db]\n  restore_order: [web]\n"

const composeVol = compose + "volumes:\n  data: {}\n"

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
	if a.Backup.Service != "web" || a.Backup.Skip[0] != "data/cache" || a.Backup.CapsuleSecrets[0] != "db" {
		t.Errorf("backup = %+v", a.Backup)
	}
	if a.Upgrade.MinFrom != "2.0.0" || len(a.Upgrade.NoSkip) != 2 || !a.Upgrade.MigratesData {
		t.Errorf("upgrade = %+v", a.Upgrade)
	}
}

func TestKubernetesBackup(t *testing.T) {
	m := manifest + kube + "  env:\n    LOG_LEVEL: info\n  volumes:\n    - name: data\n      mount: /data\n      size: 1Gi\n" +
		"backup:\n  command: [sqlite3, /data/app.db, .dump]\n  volumes: [data]\n"
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

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"1.2.3", "1.2.3", 0}, {"1.10.0", "1.9.9", 1}, {"0.9.0", "1.0.0", -1}, {"2.0.10", "2.0.2", 1}} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
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
		"2.2.0":  "",
		"2.2.5":  "",
		"2.4.0":  "",
		"2.1.9":  "would skip 2.2.0",
		"1.9.0":  "min_from 2.0.0",
		"2.5.0":  "downgrades",
		"2.0":    "not MAJOR.MINOR.PATCH",
		"2.10.0": "downgrades",
	} {
		err := u.Allows(installed, "2.4.0")
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: err = %v, want %q", installed, err, want)
		}
	}
}
