// Package catalog loads app manifests and renders their Compose files.
package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
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
	Secrets     []string          `yaml:"secrets"`
	DependsOn   []string          `yaml:"depends_on"`
	Health      Health            `yaml:"health"`
	OIDC        *OIDC             `yaml:"oidc"`
	Backup      *Backup           `yaml:"backup"`
	Upgrade     Upgrade           `yaml:"upgrade"`
	Kubernetes  *Kubernetes       `yaml:"kubernetes"`
	// PodSecurity is the app namespace's enforced level: restricted (the default) or baseline.
	PodSecurity string `yaml:"pod_security"`
}

// OIDC is the app's client in KyIdentity. Paths are relative to the app's public origin.
type OIDC struct {
	RedirectPaths         []string `yaml:"redirect_paths"`
	LogoutPaths           []string `yaml:"logout_paths"`
	BackchannelLogoutPath string   `yaml:"backchannel_logout_path"`
	Roles                 []string `yaml:"roles"`  // must include admin and user
	Public                bool     `yaml:"public"` // PKCE without a client secret
}

// Backup is what restic takes from the app. Volumes name Compose volumes and, when the
// app runs on Kubernetes, kubernetes.volumes; Skip entries are volume/path.
type Backup struct {
	Service        string   `yaml:"service"` // Compose service the command runs in
	Command        []string `yaml:"command"` // stdout is streamed into restic
	Volumes        []string `yaml:"volumes"`
	Skip           []string `yaml:"skip"`
	CapsuleSecrets []string `yaml:"capsule_secrets"` // sealed to KyRecovery; a subset of secrets
	RestoreOrder   []string `yaml:"restore_order"`   // Compose services
}

type Upgrade struct {
	MinFrom      string   `yaml:"min_from"` // oldest installed version that may upgrade to this one
	NoSkip       []string `yaml:"no_skip"`  // versions an upgrade must stop at on the way
	MigratesData bool     `yaml:"migrates_data"`
}

type Health struct {
	TimeoutSeconds int `yaml:"timeout_seconds"`
}

// Kubernetes is how a Ky product or edge component runs in a cluster: one container.
type Kubernetes struct {
	Image         string            `yaml:"image"` // a key of Images
	Args          []string          `yaml:"args"`
	Env           map[string]string `yaml:"env"` // never secrets: those are files in /run/secrets; may hold HostnameRef
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
}

// Resources are both the requests and the limits.
type Resources struct {
	CPU    string `yaml:"cpu"`
	Memory string `yaml:"memory"`
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
}

type Catalog map[string]App

var (
	keyRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	imageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
	cpuRE   = regexp.MustCompile(`^[1-9][0-9]{0,5}m?$`)
	sizeRE  = regexp.MustCompile(`^[1-9][0-9]{0,5}(Mi|Gi)$`)
	volRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	mountRE = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)
	envRE   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	roleRE  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	// No segment starts with a dot, so no . or .. components.
	urlPathRE = regexp.MustCompile(`^/([A-Za-z0-9_~-][A-Za-z0-9._~-]*/?)*$`)
	skipRE    = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*(/[A-Za-z0-9_-][A-Za-z0-9._-]*)+$`)
	versionRE = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)
)

// HostnameRef in a kubernetes.env value stands for the app's route hostname in the plan.
const HostnameRef = "{{hostname}}"

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
		if err := m.validateBackup(nil); err != nil {
			return App{}, fmt.Errorf("manifest.yaml: backup: %w", err)
		}
		return App{Manifest: m}, nil
	case err != nil:
		return App{}, err
	}
	t, err := template.New(dir).Option("missingkey=error").Parse(string(text))
	if err != nil {
		return App{}, fmt.Errorf("compose.yaml.tmpl: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, m); err != nil {
		return App{}, fmt.Errorf("compose.yaml.tmpl: %w", err)
	}
	c, err := checkCompose(buf.Bytes(), m.Images)
	if err != nil {
		return App{}, fmt.Errorf("compose.yaml.tmpl: %w", err)
	}
	if err := m.validateBackup(&c); err != nil {
		return App{}, fmt.Errorf("manifest.yaml: backup: %w", err)
	}
	return App{Manifest: m, Compose: buf.Bytes(), Services: c.services}, nil
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
	seen := map[string]bool{}
	for _, s := range m.Secrets {
		if !keyRE.MatchString(s) || seen[s] {
			return fmt.Errorf("secret %q must be unique and match %s", s, keyRE)
		}
		seen[s] = true
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
		if err := m.OIDC.validate(); err != nil {
			return fmt.Errorf("oidc.%w", err)
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
		if rest := strings.ReplaceAll(v, HostnameRef, ""); strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
			return fmt.Errorf("env: %s: the only reference is %s", key, HostnameRef)
		}
	}
	switch {
	case k.RunAs < 0 || k.RunAs > 2147483647 || k.RunAs == 0 && !baseline:
		return fmt.Errorf("run_as %d must be a non-root uid (root needs pod_security baseline)", k.RunAs)
	case (len(k.Capabilities) > 0 || k.WritableRoot) && !baseline:
		return fmt.Errorf("capabilities and writable_root need pod_security baseline")
	case len(k.NodePorts) > 0 && !edge:
		return fmt.Errorf("node_ports: only edge components publish ports; apps are reached through the edge")
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
	if !sizeRE.MatchString(k.Resources.Memory) {
		return fmt.Errorf("resources.memory %q must look like 64Mi or 1Gi", k.Resources.Memory)
	}
	mounts := map[string]bool{"/tmp": true, "/run/secrets": true}
	for _, v := range k.Volumes {
		switch {
		case !volRE.MatchString(v.Name):
			return fmt.Errorf("volumes: name %q must match %s", v.Name, volRE)
		case !mountRE.MatchString(v.Mount) || mounts[v.Mount]:
			return fmt.Errorf("volumes: mount %q must be a unique absolute path, not /tmp or /run/secrets", v.Mount)
		case !sizeRE.MatchString(v.Size):
			return fmt.Errorf("volumes: size %q must look like 1Gi", v.Size)
		}
		mounts[v.Mount] = true
	}
	return nil
}

// UsesHostname reports whether an env value references the route hostname.
func (k Kubernetes) UsesHostname() bool {
	for _, v := range k.Env {
		if strings.Contains(v, HostnameRef) {
			return true
		}
	}
	return false
}

// WithHostname returns a copy whose env has every HostnameRef replaced by host.
func (k Kubernetes) WithHostname(host string) *Kubernetes {
	env := make(map[string]string, len(k.Env))
	for key, v := range k.Env {
		env[key] = strings.ReplaceAll(v, HostnameRef, host)
	}
	k.Env = env
	return &k
}

func (o OIDC) validate() error {
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
	if err := check(o.Roles, roleRE.MatchString); err != nil {
		return fmt.Errorf("roles: %w must be unique and match %s", err, roleRE)
	}
	if !slices.Contains(o.Roles, "admin") || !slices.Contains(o.Roles, "user") {
		return fmt.Errorf("roles must include admin and user")
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
	if slices.Contains(b.Command, "") {
		return fmt.Errorf("command has an empty argument")
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
	services []string // sorted
	volumes  map[string]bool
}

// checkCompose returns the sorted service names and the named volumes, refusing builds
// and any image the manifest does not pin.
func checkCompose(b []byte, images map[string]string) (composeFile, error) {
	var c struct {
		Services map[string]struct {
			Image string `yaml:"image"`
			Build any    `yaml:"build"`
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
	var names []string
	for name, s := range c.Services {
		if s.Build != nil {
			return composeFile{}, fmt.Errorf("service %s: build is not allowed", name)
		}
		if !pinned[s.Image] {
			return composeFile{}, fmt.Errorf("service %s: image %q is not one of the manifest's pinned images", name, s.Image)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	volumes := map[string]bool{}
	for v := range c.Volumes {
		volumes[v] = true
	}
	return composeFile{services: names, volumes: volumes}, nil
}
