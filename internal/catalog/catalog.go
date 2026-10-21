// Package catalog loads app manifests and renders their Compose files.
package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

type Manifest struct {
	Description string            `yaml:"description,omitempty"`
	Name        string            `yaml:"name"`
	Category    string            `yaml:"category"` // ky, edge or third-party
	Version     string            `yaml:"version"`  // release the images pin, MAJOR.MINOR.PATCH
	Images      map[string]string `yaml:"images"`
	// PendingImages are images the app needs that are not published yet: its Compose file
	// renders, but no plan may install it.
	PendingImages []string `yaml:"pending_images"`
	Secrets       []string `yaml:"secrets"`
	// SecretFormats names a secret's format other than 32 random bytes in base64url:
	// laravel is "base64:" and 32 random bytes in standard base64, Laravel's APP_KEY.
	SecretFormats map[string]string `yaml:"secret_formats"`
	DependsOn     []string          `yaml:"depends_on"`
	Health        Health            `yaml:"health"`
	OIDC          *OIDC             `yaml:"oidc"`
	Backup        *Backup           `yaml:"backup"`
	Upgrade       Upgrade           `yaml:"upgrade"`
	Kubernetes    *Kubernetes       `yaml:"kubernetes"`
	// Routes are the edge routes of an app reached at several hostnames; the first is the
	// primary. None means one route to the app.
	Routes []RouteRole `yaml:"routes"`
	// Media are ports published on the plan's media_address, outside the edge.
	Media *Media `yaml:"media"`
	// ChatOIDC is a second, everyday-only KyIdentity client at one of the app's routes.
	ChatOIDC *ChatOIDC `yaml:"chat_oidc"`
	// PodSecurity is the app namespace's enforced level: restricted (the default) or baseline.
	PodSecurity string `yaml:"pod_security"`
}

// OIDC is the app's client in KyIdentity. Paths are relative to the app's public origin.
type OIDC struct {
	Name                  string   `yaml:"name"` // shown in KyIdentity
	RedirectPaths         []string `yaml:"redirect_paths"`
	LogoutPaths           []string `yaml:"logout_paths"`
	BackchannelLogoutPath string   `yaml:"backchannel_logout_path"`
	// The app roles KyIdentity issues to the owner's admin and everyday identities. No
	// user_role makes the client admin-only; third-party apps need both.
	AdminRole string `yaml:"admin_role"`
	UserRole  string `yaml:"user_role"`
	// DirectoryPath receives KyIdentity's signed suite directory events, at the host of route
	// DirectoryRole (the primary when empty), which must serve it.
	DirectoryPath string `yaml:"directory_path"`
	DirectoryRole string `yaml:"directory_role"`
	Public        bool   `yaml:"public"` // PKCE without a client secret
	// SCIMTokenFile is /run/secrets/<secret>: the app's inbound SCIM bearer token, which its
	// setup bundle names.
	SCIMTokenFile string `yaml:"scim_token_file"`
}

// RouteRole is one of an app's edge routes: a hostname role served by a Compose service's
// published port. Paths are the only exact paths served (everything else answers 404);
// DenyPaths are anchored regular expressions that answer 404.
type RouteRole struct {
	Role      string   `yaml:"role"`
	Service   string   `yaml:"service"`
	Port      int      `yaml:"port"`
	Paths     []string `yaml:"paths"`
	DenyPaths []string `yaml:"deny_paths"`
}

// Media are <port>/<tcp|udp> entries published on the plan's media_address.
type Media struct {
	Ports []string `yaml:"ports"`
}

// ChatOIDC is a client whose redirect is at route role Role; RedirectPath may hold
// {provider_id}. It has no administrator role: only the everyday role signs in.
type ChatOIDC struct {
	Name         string `yaml:"name"`
	Role         string `yaml:"role"`
	ClientSuffix string `yaml:"client_suffix"` // the client ID is <app>-<suffix>
	EverydayRole string `yaml:"everyday_role"`
	RedirectPath string `yaml:"redirect_path"`
}

// ProviderIDRef in a ChatOIDC redirect path stands for the derived provider ID.
const ProviderIDRef = "{provider_id}"

// Backup is what restic takes from the app. Volumes name Compose volumes or ./<name> bind
// directories and, when the app runs on Kubernetes, kubernetes.volumes; Skip entries are
// volume/path. Without a Command the backup is cold: the app stops while restic reads.
type Backup struct {
	Service        string   `yaml:"service"`         // Compose service both commands run in
	Command        []string `yaml:"command"`         // its stdout is the dump
	RestoreCommand []string `yaml:"restore_command"` // its stdin is the dump
	Volumes        []string `yaml:"volumes"`
	Skip           []string `yaml:"skip"`
	// CapsuleSecrets travel in the restic snapshot, encrypted by restic; a subset of secrets.
	CapsuleSecrets []string `yaml:"capsule_secrets"`
	RestoreOrder   []string `yaml:"restore_order"` // Compose services, service first
	// ReplayAfter is the restore_order service after which a restore replays KyIdentity's
	// removals; the services after it start only once the replay is delivered.
	ReplayAfter string `yaml:"replay_after"`
}

// ResticPassword is the installer's own secret beside an app's: no manifest may declare it.
const ResticPassword = "restic_password"

type Upgrade struct {
	MinFrom      string   `yaml:"min_from"` // oldest installed version that may upgrade to this one
	NoSkip       []string `yaml:"no_skip"`  // versions an upgrade must stop at on the way
	MigratesData bool     `yaml:"migrates_data"`
	Note         string   `yaml:"note"` // what the owner must check after upgrading to this version
}

type Health struct {
	TimeoutSeconds int `yaml:"timeout_seconds"`
}

// Kubernetes is how a Ky product or edge component runs in a cluster: one container.
type Kubernetes struct {
	Image         string            `yaml:"image"` // a key of Images
	Args          []string          `yaml:"args"`
	Env           map[string]string `yaml:"env"` // never secrets: those are files in /run/secrets; may hold HostRef
	Ports         []int             `yaml:"ports"`
	ReadinessPath string            `yaml:"readiness_path"`
	RunAs         int64             `yaml:"run_as"` // uid and gid; 0 only under baseline
	Resources     Resources         `yaml:"resources"`
	Volumes       []Volume          `yaml:"volumes"`
	// Baseline only: capabilities added back after dropping ALL, and a writable root filesystem.
	Capabilities []string `yaml:"capabilities"`
	WritableRoot bool     `yaml:"writable_root"`
	// LowPorts lets the pod's non-root processes bind ports below 1024 (a safe sysctl).
	LowPorts bool `yaml:"low_ports"`
	// NodePorts publishes container ports on every node, with their default NodePort; edge only.
	NodePorts map[int]int `yaml:"node_ports"`
	// TerminationGraceSeconds lets a stopping pod drain this long; 0 keeps Kubernetes' default.
	TerminationGraceSeconds int64 `yaml:"termination_grace_seconds"`
	// SuiteRunner adds the suite runner (kyquickstart serve) as a sidecar sharing only its
	// socket directory with the app, which runs as the runner's uid and is told the socket.
	SuiteRunner bool `yaml:"suite_runner"`
}

const (
	SuiteRunnerUID    = 10001                                  // the runner image's user
	SuiteRunnerMount  = "/run/kyquickstart"                    // in both containers
	SuiteRunnerSocket = SuiteRunnerMount + "/sock/runner.sock" // KY_SUITE_RUNNER_SOCKET
)

// Resources are both the requests and the limits, except that CPURequest, when set, is a
// lower CPU request: an app idle between short bursts need not reserve a whole limit.
type Resources struct {
	CPU        string `yaml:"cpu"`
	CPURequest string `yaml:"cpu_request,omitempty"`
	Memory     string `yaml:"memory"`
}

type Volume struct {
	Name  string `yaml:"name"`
	Mount string `yaml:"mount"`
	Size  string `yaml:"size"`
}

// App is a validated manifest with its rendered Compose file. Compose and Services are
// nil when the app ships no compose.yaml.tmpl.
type App struct {
	Manifest
	Compose  []byte
	Services []string // sorted
	// Binds are the ./<name> bind directories of its services, sorted; the Docker driver
	// creates them private before Compose would create them as root.
	Binds []string
	// Nested are bind sources inside a bind directory, ./<name>/<path>, sorted; they must
	// exist before Compose starts, or Docker would create them as root.
	Nested []string
	// Init are the services in a Compose profile: one-shot runs, never part of up.
	Init []string
}

type Catalog map[string]App

// Pinned reports whether an image reference is pinned by digest, as the catalog requires.
func Pinned(ref string) bool { return imageRE.MatchString(ref) }

var (
	keyRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	imageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
	cpuRE   = regexp.MustCompile(`^[1-9][0-9]{0,5}m?$`)
	sizeRE  = regexp.MustCompile(`^[1-9][0-9]{0,5}(Mi|Gi)$`)
	volRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	mountRE = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)
	envRE   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	roleRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`) // KyIdentity's app-role grammar
	// No segment starts with a dot, so no . or .. components.
	urlPathRE = regexp.MustCompile(`^/([A-Za-z0-9_~-][A-Za-z0-9._~-]*/?)*$`)
	skipRE    = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*(/[A-Za-z0-9_-][A-Za-z0-9._-]*)+$`)
	versionRE = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)
)

// HostRef in a kubernetes.env value stands for the URL host of the app's route: its hostname,
// plus the port when clients reach the edge on another than 443.
const HostRef = "{{host}}"

// ForwardRef in a Compose template stands for forward_host:forward_port of the app's route:
// the one address it publishes its port on, for an edge on another host.
const ForwardRef = "{{forward}}"

// ForwardAddrRef stands for the route's forward_host alone, bracketed if IPv6, for ports the
// app publishes on that address beside its forward_port.
const ForwardAddrRef = "{{forward_addr}}"

// EdgeAddressRef stands for the route's edge's egress_address: the one address the app sees
// the edge's requests from.
const EdgeAddressRef = "{{edge_address}}"

// MediaAddressRef stands for the plan's media_address of the app.
const MediaAddressRef = "{{media_address}}"

// HostnameRef stands for the route's hostname alone, without a client port.
const HostnameRef = "{{hostname}}"

// routeRefs are every route reference a Compose template may write; {{host:<role>}} and
// {{forward:<role>}} name a role of routes.
var routeRefs = []string{HostRef, ForwardRef, ForwardAddrRef, EdgeAddressRef, MediaAddressRef, HostnameRef, "{{host:", "{{forward:", "{{hostname:"}

// roleRef renders {{host "x"}} as {{host:x}}, and {{host}} as itself.
func roleRef(name string) func(...string) string {
	return func(role ...string) string {
		if len(role) == 0 {
			return "{{" + name + "}}"
		}
		return "{{" + name + ":" + strings.Join(role, ",") + "}}"
	}
}

// routeFuncs render a Compose template's route references as themselves; a session replaces
// them with the app's routes (WithRoute).
var routeFuncs = template.FuncMap{
	"host":          roleRef("host"),
	"forward":       roleRef("forward"),
	"hostname":      roleRef("hostname"),
	"forward_addr":  func() string { return ForwardAddrRef },
	"edge_address":  func() string { return EdgeAddressRef },
	"media_address": func() string { return MediaAddressRef },
}

// roleRefRE finds {{host:<role>}} and {{forward:<role>}} in a rendered Compose file.
var roleRefRE = regexp.MustCompile(`\{\{(?:host|forward|hostname):([^}]*)\}\}`)

// Route is what a Compose template's route references stand for. Roles holds the
// non-primary routes' addresses, by role.
type Route struct {
	Host, Forward, ForwardAddr, EdgeAddress string
	Hostname, MediaAddress                  string
	Roles                                   map[string]RoleAddress
}

// RoleAddress is one role's URL host, hostname and forward_host:forward_port.
type RoleAddress struct{ Host, Hostname, Forward string }

// AdminPort is NPM's admin API port. It is never published outside the cluster.
const AdminPort = 81

// baselineCaps are the capabilities Pod Security baseline lets a container add.
var baselineCaps = []string{"AUDIT_WRITE", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "MKNOD", "NET_BIND_SERVICE", "SETFCAP", "SETGID", "SETPCAP", "SETUID", "SYS_CHROOT"}

// Load reads every directory of fsys as one app.
func Load(fsys fs.FS) (Catalog, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	cat := Catalog{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		app, err := loadApp(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("app %s: %w", e.Name(), err)
		}
		cat[app.Name] = app
	}
	return cat, nil
}

func loadApp(fsys fs.FS, dir string) (App, error) {
	raw, err := fs.ReadFile(fsys, path.Join(dir, "manifest.yaml"))
	if err != nil {
		return App{}, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return App{}, fmt.Errorf("manifest.yaml: %w", err)
	}
	if m.PodSecurity == "" {
		m.PodSecurity = "restricted"
	}
	if err := m.validate(dir); err != nil {
		return App{}, fmt.Errorf("manifest.yaml: %w", err)
	}
	text, err := fs.ReadFile(fsys, path.Join(dir, "compose.yaml.tmpl"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if m.Category == "third-party" {
			return App{}, fmt.Errorf("third-party apps need compose.yaml.tmpl")
		}
		if m.Kubernetes == nil {
			return App{}, fmt.Errorf("neither compose.yaml.tmpl nor kubernetes")
		}
		if m.Routes != nil || m.Media != nil || m.ChatOIDC != nil {
			return App{}, fmt.Errorf("routes, media and chat_oidc need compose.yaml.tmpl")
		}
		if m.OIDC != nil && m.OIDC.DirectoryRole != "" {
			return App{}, fmt.Errorf("oidc.directory_role needs routes, which need compose.yaml.tmpl")
		}
		if err := m.validateBackup(nil); err != nil {
			return App{}, fmt.Errorf("manifest.yaml: backup: %w", err)
		}
		return App{Manifest: m}, nil
	case err != nil:
		return App{}, err
	}
	t, err := template.New(dir).Option("missingkey=error").Funcs(routeFuncs).Parse(string(text))
	if err != nil {
		return App{}, fmt.Errorf("compose.yaml.tmpl: %w", err)
	}
	// A pending image renders as a reference no registry serves; the plan refuses the app.
	render := m
	render.Images = maps.Clone(m.Images)
	for _, k := range m.PendingImages {
		render.Images[k] = "unpublished/" + k
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, render); err != nil {
		return App{}, fmt.Errorf("compose.yaml.tmpl: %w", err)
	}
	c, err := checkCompose(buf.Bytes(), render.Images)
	if err != nil {
		return App{}, fmt.Errorf("compose.yaml.tmpl: %w", err)
	}
	if err := m.validateBackup(&c); err != nil {
		return App{}, fmt.Errorf("manifest.yaml: backup: %w", err)
	}
	if err := m.validateRoutes(buf.Bytes(), c); err != nil {
		return App{}, fmt.Errorf("manifest.yaml: %w", err)
	}
	return App{Manifest: m, Compose: buf.Bytes(), Services: c.services, Binds: c.binds, Nested: c.nested, Init: c.init}, nil
}

// UsesRoute reports whether the app's Compose file takes anything from its route.
func (a App) UsesRoute() bool {
	return slices.ContainsFunc(routeRefs, func(ref string) bool { return bytes.Contains(a.Compose, []byte(ref)) })
}

// UsesEdgeAddress reports whether the app's Compose file takes its edge's egress_address.
func (a App) UsesEdgeAddress() bool { return bytes.Contains(a.Compose, []byte(EdgeAddressRef)) }

// WithRoute returns a copy whose Compose file has its route references replaced by r.
func (a App) WithRoute(r Route) App {
	pairs := []string{HostRef, r.Host, ForwardRef, r.Forward, ForwardAddrRef, r.ForwardAddr, EdgeAddressRef, r.EdgeAddress, MediaAddressRef, r.MediaAddress, HostnameRef, r.Hostname}
	for role, ra := range r.Roles {
		pairs = append(pairs, "{{host:"+role+"}}", ra.Host, "{{forward:"+role+"}}", ra.Forward, "{{hostname:"+role+"}}", ra.Hostname)
	}
	a.Compose = []byte(strings.NewReplacer(pairs...).Replace(string(a.Compose)))
	return a
}

// Primary is the role of the app's primary route, "" for an app with one route.
func (m Manifest) Primary() string {
	if len(m.Routes) == 0 {
		return ""
	}
	return m.Routes[0].Role
}

func (m Manifest) validate(dir string) error {
	if m.Name != dir || !stack.ValidName(m.Name) {
		return fmt.Errorf("name %q must match its directory %q and be a valid app name", m.Name, dir)
	}
	switch m.Category {
	case "ky", "edge", "third-party":
	default:
		return fmt.Errorf("category %q must be ky, edge or third-party", m.Category)
	}
	if len(m.Images) == 0 {
		return fmt.Errorf("no images")
	}
	for k, ref := range m.Images {
		if !keyRE.MatchString(k) {
			return fmt.Errorf("image key %q must match %s", k, keyRE)
		}
		if !imageRE.MatchString(ref) {
			return fmt.Errorf("image %s = %q is not pinned by digest", k, ref)
		}
	}
	for _, k := range m.PendingImages {
		if _, ok := m.Images[k]; ok || !keyRE.MatchString(k) {
			return fmt.Errorf("pending_images: %q must match %s and not be pinned in images", k, keyRE)
		}
	}
	seen := map[string]bool{}
	for _, s := range m.Secrets {
		if !keyRE.MatchString(s) || seen[s] || s == ResticPassword {
			return fmt.Errorf("secret %q must be unique, match %s and not be the reserved %s", s, keyRE, ResticPassword)
		}
		seen[s] = true
	}
	for s, f := range m.SecretFormats {
		if !seen[s] || f != "laravel" || m.Kubernetes != nil {
			return fmt.Errorf("secret_formats: %s must be one of secrets, laravel, on an app without kubernetes", s)
		}
	}
	for _, d := range m.DependsOn {
		if !stack.ValidName(d) || d == m.Name {
			return fmt.Errorf("depends_on %q is not another app", d)
		}
	}
	if m.Health.TimeoutSeconds < 1 || m.Health.TimeoutSeconds > 1800 {
		return fmt.Errorf("health.timeout_seconds %d must be 1-1800", m.Health.TimeoutSeconds)
	}
	if !versionRE.MatchString(m.Version) {
		return fmt.Errorf("version %q must be MAJOR.MINOR.PATCH", m.Version)
	}
	if err := m.Upgrade.validate(m.Version); err != nil {
		return fmt.Errorf("upgrade.%w", err)
	}
	if m.OIDC == nil && m.Category == "third-party" {
		return fmt.Errorf("oidc: third-party apps sign in through KyIdentity")
	}
	if m.OIDC != nil {
		if m.Category == "edge" {
			return fmt.Errorf("oidc: edge components have no sign-in")
		}
		if err := m.OIDC.validate(m.Category == "third-party"); err != nil {
			return fmt.Errorf("oidc.%w", err)
		}
		if f := m.OIDC.SCIMTokenFile; f != "" && !slices.Contains(m.Secrets, strings.TrimPrefix(f, "/run/secrets/")) {
			return fmt.Errorf("oidc.scim_token_file %q must be /run/secrets/<one of secrets>", f)
		}
	}
	if m.PodSecurity != "restricted" && m.PodSecurity != "baseline" {
		return fmt.Errorf("pod_security %q must be restricted or baseline", m.PodSecurity)
	}
	if m.Kubernetes != nil {
		if m.Category == "third-party" {
			return fmt.Errorf("kubernetes: third-party apps run on Docker hosts only")
		}
		if err := m.Kubernetes.validate(m.Images, m.PodSecurity == "baseline", m.Category == "edge"); err != nil {
			return fmt.Errorf("kubernetes.%w", err)
		}
	}
	return nil
}

func (k Kubernetes) validate(images map[string]string, baseline, edge bool) error {
	if _, ok := images[k.Image]; !ok {
		return fmt.Errorf("image %q is not a key of images", k.Image)
	}
	seen := map[int]bool{}
	for _, p := range k.Ports {
		if p < 1 || p > 65535 || seen[p] {
			return fmt.Errorf("ports: %d must be unique and 1-65535", p)
		}
		seen[p] = true
	}
	if k.ReadinessPath != "" && (len(k.Ports) == 0 || !strings.HasPrefix(k.ReadinessPath, "/")) {
		return fmt.Errorf("readiness_path %q needs a port and must start with /", k.ReadinessPath)
	}
	for key, v := range k.Env {
		if !envRE.MatchString(key) {
			return fmt.Errorf("env: name %q must match %s", key, envRE)
		}
		if rest := strings.ReplaceAll(v, HostRef, ""); strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
			return fmt.Errorf("env: %s: the only reference is %s", key, HostRef)
		}
	}
	switch {
	case k.RunAs < 0 || k.RunAs > 2147483647 || k.RunAs == 0 && !baseline:
		return fmt.Errorf("run_as %d must be a non-root uid (root needs pod_security baseline)", k.RunAs)
	case (len(k.Capabilities) > 0 || k.WritableRoot) && !baseline:
		return fmt.Errorf("capabilities and writable_root need pod_security baseline")
	case len(k.NodePorts) > 0 && !edge:
		return fmt.Errorf("node_ports: only edge components publish ports; apps are reached through the edge")
	case k.TerminationGraceSeconds < 0 || k.TerminationGraceSeconds > 3600:
		return fmt.Errorf("termination_grace_seconds %d must be 0-3600", k.TerminationGraceSeconds)
	case k.SuiteRunner && k.RunAs != SuiteRunnerUID:
		return fmt.Errorf("suite_runner needs run_as %d: the socket admits its own uid only", SuiteRunnerUID)
	case k.SuiteRunner && k.Env["KY_SUITE_RUNNER_SOCKET"] != SuiteRunnerSocket:
		return fmt.Errorf("suite_runner needs env KY_SUITE_RUNNER_SOCKET %s", SuiteRunnerSocket)
	}
	if err := check(k.Capabilities, func(c string) bool { return slices.Contains(baselineCaps, c) }); err != nil {
		return fmt.Errorf("capabilities: %w must be unique and one of %v", err, baselineCaps)
	}
	used := map[int]bool{}
	for p, n := range k.NodePorts {
		switch {
		case p == AdminPort:
			return fmt.Errorf("node_ports: %d is NPM's admin API port, which never leaves the cluster", p)
		case !seen[p]:
			return fmt.Errorf("node_ports: %d is not one of ports", p)
		case !stack.ValidNodePort(n) || used[n]:
			return fmt.Errorf("node_ports: %d must be unique and 30000-32767", n)
		}
		used[n] = true
	}
	if !cpuRE.MatchString(k.Resources.CPU) {
		return fmt.Errorf("resources.cpu %q must look like 100m or 2", k.Resources.CPU)
	}
	if r := k.Resources.CPURequest; r != "" && (!cpuRE.MatchString(r) || cpuMilli(r) > cpuMilli(k.Resources.CPU)) {
		return fmt.Errorf("resources.cpu_request %q must look like 100m and be at most resources.cpu", r)
	}
	if !sizeRE.MatchString(k.Resources.Memory) {
		return fmt.Errorf("resources.memory %q must look like 64Mi or 1Gi", k.Resources.Memory)
	}
	mounts := map[string]bool{"/tmp": true, "/run/secrets": true, SuiteRunnerMount: k.SuiteRunner}
	for _, v := range k.Volumes {
		switch {
		case !volRE.MatchString(v.Name):
			return fmt.Errorf("volumes: name %q must match %s", v.Name, volRE)
		case !mountRE.MatchString(v.Mount) || mounts[v.Mount]:
			return fmt.Errorf("volumes: mount %q must be a unique absolute path, not /tmp, /run/secrets or the runner's", v.Mount)
		case !sizeRE.MatchString(v.Size):
			return fmt.Errorf("volumes: size %q must look like 1Gi", v.Size)
		}
		mounts[v.Mount] = true
	}
	return nil
}

// UsesHost reports whether an env value references the route hostname.
func (k Kubernetes) UsesHost() bool {
	for _, v := range k.Env {
		if strings.Contains(v, HostRef) {
			return true
		}
	}
	return false
}

// WithHost returns a copy whose env has every HostRef replaced by host.
func (k Kubernetes) WithHost(host string) *Kubernetes {
	env := make(map[string]string, len(k.Env))
	for key, v := range k.Env {
		env[key] = strings.ReplaceAll(v, HostRef, host)
	}
	k.Env = env
	return &k
}

func (o OIDC) validate(thirdParty bool) error {
	if o.Name == "" || len(o.Name) > 128 || strings.ContainsAny(o.Name, "\r\n\t") {
		return fmt.Errorf("name %q must be 1-128 characters on one line", o.Name)
	}
	if len(o.RedirectPaths) == 0 {
		return fmt.Errorf("redirect_paths: at least one is required")
	}
	if err := check(o.RedirectPaths, urlPathRE.MatchString); err != nil {
		return fmt.Errorf("redirect_paths: %w must be a unique absolute path", err)
	}
	if err := check(o.LogoutPaths, urlPathRE.MatchString); err != nil {
		return fmt.Errorf("logout_paths: %w must be a unique absolute path", err)
	}
	if o.BackchannelLogoutPath != "" && !urlPathRE.MatchString(o.BackchannelLogoutPath) {
		return fmt.Errorf("backchannel_logout_path %q must be an absolute path", o.BackchannelLogoutPath)
	}
	if o.DirectoryPath != "" && !urlPathRE.MatchString(o.DirectoryPath) {
		return fmt.Errorf("directory_path %q must be an absolute path", o.DirectoryPath)
	}
	switch {
	case !roleRE.MatchString(o.AdminRole):
		return fmt.Errorf("admin_role %q must match %s", o.AdminRole, roleRE)
	case o.UserRole == "" && thirdParty:
		return fmt.Errorf("user_role: third-party apps need an everyday role")
	case o.UserRole != "" && (!roleRE.MatchString(o.UserRole) || strings.EqualFold(o.UserRole, o.AdminRole)):
		return fmt.Errorf("user_role %q must match %s and differ from admin_role", o.UserRole, roleRE)
	}
	return nil
}

func (u Upgrade) validate(version string) error {
	if u.MinFrom != "" && (!versionRE.MatchString(u.MinFrom) || compareVersions(u.MinFrom, version) > 0) {
		return fmt.Errorf("min_from %q must be a version no newer than %s", u.MinFrom, version)
	}
	err := check(u.NoSkip, func(v string) bool {
		return versionRE.MatchString(v) && compareVersions(v, version) < 0 &&
			(u.MinFrom == "" || compareVersions(v, u.MinFrom) > 0)
	})
	if err != nil {
		return fmt.Errorf("no_skip: %w must be a unique version after min_from and before %s", err, version)
	}
	return nil
}

// Allows checks an upgrade from the installed version to version against min_from and no_skip.
func (u Upgrade) Allows(installed, version string) error {
	switch {
	case !versionRE.MatchString(installed):
		return fmt.Errorf("installed version %q is not MAJOR.MINOR.PATCH", installed)
	case compareVersions(installed, version) > 0:
		return fmt.Errorf("installed %s is newer than %s; downgrades are refused", installed, version)
	case u.MinFrom != "" && compareVersions(installed, u.MinFrom) < 0:
		return fmt.Errorf("installed %s is older than min_from %s; upgrade through an earlier release set first", installed, u.MinFrom)
	}
	for _, v := range u.NoSkip {
		if compareVersions(installed, v) < 0 {
			return fmt.Errorf("upgrading %s to %s would skip %s (no_skip); upgrade through the release set that ships %s first", installed, version, v, v)
		}
	}
	return nil
}

// validateBackup checks the backup against what the app deploys; c is nil without Compose.
func (m Manifest) validateBackup(c *composeFile) error {
	b := m.Backup
	if b == nil {
		return nil
	}
	if len(b.Command) == 0 && len(b.Volumes) == 0 {
		return fmt.Errorf("needs a command or volumes")
	}
	if slices.Contains(b.Command, "") || slices.Contains(b.RestoreCommand, "") {
		return fmt.Errorf("command or restore_command has an empty argument")
	}
	if (len(b.Command) == 0) != (len(b.RestoreCommand) == 0) {
		return fmt.Errorf("command and restore_command come together")
	}
	isService := func(s string) bool { return c != nil && slices.Contains(c.services, s) }
	switch {
	case len(b.Command) == 0 && b.Service != "":
		return fmt.Errorf("service %q without a command", b.Service)
	case len(b.Command) > 0 && c != nil && !isService(b.Service):
		return fmt.Errorf("service %q is not a Compose service", b.Service)
	case c == nil && (b.Service != "" || len(b.RestoreOrder) > 0):
		return fmt.Errorf("service and restore_order need compose.yaml.tmpl")
	}
	isVolume := func(v string) bool {
		if c != nil && !c.volumes[v] {
			return false
		}
		return m.Kubernetes == nil || slices.ContainsFunc(m.Kubernetes.Volumes, func(k Volume) bool { return k.Name == v })
	}
	if err := check(b.Volumes, isVolume); err != nil {
		return fmt.Errorf("volumes: %w must be unique and declared by every deployment", err)
	}
	err := check(b.Skip, func(s string) bool {
		return skipRE.MatchString(s) && slices.Contains(b.Volumes, strings.SplitN(s, "/", 2)[0])
	})
	if err != nil {
		return fmt.Errorf("skip: %w must be a unique volume/path inside a backed-up volume", err)
	}
	if err := check(b.CapsuleSecrets, func(s string) bool { return slices.Contains(m.Secrets, s) }); err != nil {
		return fmt.Errorf("capsule_secrets: %w must be unique and one of secrets", err)
	}
	if err := check(b.RestoreOrder, isService); err != nil {
		return fmt.Errorf("restore_order: %w must be a unique Compose service", err)
	}
	if b.Service != "" && len(b.RestoreOrder) > 0 && b.RestoreOrder[0] != b.Service {
		return fmt.Errorf("restore_order must start with service %q", b.Service)
	}
	if b.ReplayAfter != "" && (m.OIDC == nil || !slices.Contains(b.RestoreOrder, b.ReplayAfter)) {
		return fmt.Errorf("replay_after must be a restore_order service of an app with oidc")
	}
	return nil
}

var (
	// exactPathRE is an exact request path nginx can match: no space, quote, brace or ;.
	exactPathRE = regexp.MustCompile(`^(/[A-Za-z0-9_.~-]+)+$`)
	denyRE      = regexp.MustCompile(`^\^/[A-Za-z0-9_./()|?*+\[\]-]*\$?$`) // a $ only at the end
	mediaRE     = regexp.MustCompile(`^[1-9][0-9]{0,4}/(tcp|udp)$`)
)

// validateRoutes checks routes, media and chat_oidc against the rendered Compose file:
// every role reference names a role, every role a service publishing its port.
func (m Manifest) validateRoutes(compose []byte, c composeFile) error {
	if m.Kubernetes != nil && (m.Routes != nil || m.Media != nil) {
		return fmt.Errorf("routes and media are for Docker: a Kubernetes app is one container")
	}
	roles := map[string]RouteRole{}
	for _, r := range m.Routes {
		switch {
		case !keyRE.MatchString(r.Role) || roles[r.Role].Role != "":
			return fmt.Errorf("routes: role %q must be unique and match %s", r.Role, keyRE)
		case !slices.Contains(c.ports[r.Service], r.Port):
			return fmt.Errorf("routes: role %s: service %q does not publish port %d", r.Role, r.Service, r.Port)
		case len(r.Paths) > 0 && len(r.DenyPaths) > 0:
			return fmt.Errorf("routes: role %s: paths and deny_paths exclude each other", r.Role)
		}
		if err := check(r.Paths, func(p string) bool { return exactPathRE.MatchString(p) && !strings.Contains(p, "/../") }); err != nil {
			return fmt.Errorf("routes: role %s: paths %w must be a unique exact absolute path", r.Role, err)
		}
		if err := check(r.DenyPaths, func(p string) bool { _, err := regexp.Compile(p); return denyRE.MatchString(p) && err == nil }); err != nil {
			return fmt.Errorf("routes: role %s: deny_paths %w must be a unique regular expression anchored at ^/", r.Role, err)
		}
		roles[r.Role] = r
	}
	if o := m.OIDC; o != nil && o.DirectoryRole != "" {
		if r, ok := roles[o.DirectoryRole]; !ok || !slices.Contains(r.Paths, o.DirectoryPath) {
			return fmt.Errorf("oidc.directory_role %q must be a role of routes whose paths include directory_path %q", o.DirectoryRole, o.DirectoryPath)
		}
	}
	for _, ref := range roleRefRE.FindAllSubmatch(compose, -1) {
		if _, ok := roles[string(ref[1])]; !ok || len(m.Routes) > 0 && string(ref[1]) == m.Primary() {
			return fmt.Errorf("compose.yaml.tmpl: %s names no non-primary role of routes", ref[0])
		}
	}
	usesMedia := bytes.Contains(compose, []byte(MediaAddressRef))
	if m.Media != nil {
		if len(m.Media.Ports) == 0 || !usesMedia {
			return fmt.Errorf("media: ports need a Compose publication on %s", MediaAddressRef)
		}
		if err := check(m.Media.Ports, mediaRE.MatchString); err != nil {
			return fmt.Errorf("media: port %w must be unique <port>/<tcp|udp>", err)
		}
	} else if usesMedia {
		return fmt.Errorf("compose.yaml.tmpl: %s needs media", MediaAddressRef)
	}
	if o := m.ChatOIDC; o != nil {
		r, ok := roles[o.Role]
		switch {
		case m.OIDC == nil:
			return fmt.Errorf("chat_oidc needs oidc: the console client")
		case !ok || o.Role == m.Primary():
			return fmt.Errorf("chat_oidc.role %q must be a non-primary role of routes", o.Role)
		case len(r.Paths) > 0:
			return fmt.Errorf("chat_oidc.role %s serves only its paths", o.Role)
		case !keyRE.MatchString(o.ClientSuffix):
			return fmt.Errorf("chat_oidc.client_suffix %q must match %s", o.ClientSuffix, keyRE)
		case !roleRE.MatchString(o.EverydayRole) || o.EverydayRole == m.OIDC.AdminRole || o.EverydayRole == m.OIDC.UserRole:
			return fmt.Errorf("chat_oidc.everyday_role %q must match %s and differ from oidc's roles", o.EverydayRole, roleRE)
		case !urlPathRE.MatchString(strings.ReplaceAll(o.RedirectPath, ProviderIDRef, "x")):
			return fmt.Errorf("chat_oidc.redirect_path %q must be an absolute path", o.RedirectPath)
		}
		if err := (OIDC{Name: o.Name, RedirectPaths: []string{"/"}, AdminRole: "x"}).validate(false); err != nil {
			return fmt.Errorf("chat_oidc.%w", err)
		}
	}
	return nil
}

// check names the first entry that repeats or fails ok.
func check(list []string, ok func(string) bool) error {
	seen := map[string]bool{}
	for _, s := range list {
		if seen[s] || !ok(s) {
			return fmt.Errorf("%q", s)
		}
		seen[s] = true
	}
	return nil
}

// VersionAtLeast reports whether v is a MAJOR.MINOR.PATCH version no older than min.
func VersionAtLeast(v, min string) bool {
	return versionRE.MatchString(v) && compareVersions(v, min) >= 0
}

// compareVersions orders two strings that match versionRE.
func compareVersions(a, b string) int {
	parts := func(v string) []int {
		m := versionRE.FindStringSubmatch(v)
		p := make([]int, 3)
		for i := range p {
			p[i], _ = strconv.Atoi(m[i+1])
		}
		return p
	}
	return slices.Compare(parts(a), parts(b))
}

type composeFile struct {
	services []string        // sorted, outside profiles
	init     []string        // sorted, in a profile
	volumes  map[string]bool // named volumes and bind directories
	binds    []string        // sorted
	nested   []string        // sorted
	ports    map[string][]int
}

// bindRE is a bind source beside compose.yaml: ./<name>, or a path inside it.
var bindRE = regexp.MustCompile(`^\./([a-z][a-z0-9-]{0,30})((/[A-Za-z0-9_][A-Za-z0-9._-]*)*)$`)

// publishRE is a ports entry: an explicit address reference, then host:container[/proto].
// A bare or wildcard publication would bypass the address the plan chose.
var publishRE = regexp.MustCompile(`^(\{\{forward(:[a-z][a-z0-9_]{0,31})?\}\}|(\{\{forward_addr\}\}|\{\{media_address\}\}|127\.0\.0\.1):[1-9][0-9]{0,4}):([1-9][0-9]{0,4})(/(tcp|udp))?$`)

// checkCompose returns the sorted service names, the named volumes and the bind directories,
// refusing builds, any image the manifest does not pin, any other relative bind source and
// any publication without an explicit address.
func checkCompose(b []byte, images map[string]string) (composeFile, error) {
	var c struct {
		Services map[string]struct {
			Image    string   `yaml:"image"`
			Build    any      `yaml:"build"`
			Volumes  []any    `yaml:"volumes"`
			Ports    []any    `yaml:"ports"`
			Profiles []string `yaml:"profiles"`
		} `yaml:"services"`
		Volumes map[string]any `yaml:"volumes"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return composeFile{}, err
	}
	if len(c.Services) == 0 {
		return composeFile{}, fmt.Errorf("no services")
	}
	pinned := map[string]bool{}
	for _, ref := range images {
		pinned[ref] = true
	}
	out := composeFile{volumes: map[string]bool{}, ports: map[string][]int{}}
	for v := range c.Volumes {
		out.volumes[v] = true
	}
	for name, s := range c.Services {
		if s.Build != nil {
			return composeFile{}, fmt.Errorf("service %s: build is not allowed", name)
		}
		if !pinned[s.Image] {
			return composeFile{}, fmt.Errorf("service %s: image %q is not one of the manifest's pinned images", name, s.Image)
		}
		if len(s.Profiles) > 0 {
			out.init = append(out.init, name)
		} else {
			out.services = append(out.services, name)
		}
		for _, p := range s.Ports {
			short, _ := p.(string)
			m := publishRE.FindStringSubmatch(short)
			if m == nil {
				return composeFile{}, fmt.Errorf("service %s: port %v must publish on {{forward}}, {{forward_addr}} or {{media_address}} as host:container[/proto]", name, p)
			}
			port, _ := strconv.Atoi(m[4])
			out.ports[name] = append(out.ports[name], port)
		}
		for _, v := range s.Volumes {
			short, ok := v.(string)
			if !ok {
				return composeFile{}, fmt.Errorf("service %s: write volumes in the short source:target form", name)
			}
			source, _, _ := strings.Cut(short, ":")
			if !strings.HasPrefix(source, ".") {
				continue
			}
			m := bindRE.FindStringSubmatch(source)
			if m == nil {
				return composeFile{}, fmt.Errorf("service %s: bind source %q must be ./<name> or a path inside it", name, source)
			}
			if _, named := c.Volumes[m[1]]; named {
				return composeFile{}, fmt.Errorf("bind directory %s is also a named volume", m[1])
			}
			if !slices.Contains(out.binds, m[1]) {
				out.volumes[m[1]] = true
				out.binds = append(out.binds, m[1])
			}
			if m[2] != "" && !slices.Contains(out.nested, source) {
				out.nested = append(out.nested, source)
			}
		}
	}
	for _, l := range [][]string{out.services, out.init, out.binds, out.nested} {
		sort.Strings(l)
	}
	if len(out.services) == 0 {
		return composeFile{}, fmt.Errorf("no services outside profiles")
	}
	return out, nil
}

// cpuMilli reads a quantity cpuRE accepted, such as 100m or 2, as millicores.
func cpuMilli(s string) int {
	if m, ok := strings.CutSuffix(s, "m"); ok {
		n, _ := strconv.Atoi(m)
		return n
	}
	n, _ := strconv.Atoi(s)
	return n * 1000
}
