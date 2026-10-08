// Package catalog loads app manifests and renders their Compose files.
package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

type Manifest struct {
	Name       string            `yaml:"name"`
	Category   string            `yaml:"category"` // ky, edge or third-party
	Images     map[string]string `yaml:"images"`
	Secrets    []string          `yaml:"secrets"`
	DependsOn  []string          `yaml:"depends_on"`
	Health     Health            `yaml:"health"`
	Kubernetes *Kubernetes       `yaml:"kubernetes"`
}

type Health struct {
	TimeoutSeconds int `yaml:"timeout_seconds"`
}

// Kubernetes is how a Ky product or edge component runs in a cluster: one container.
type Kubernetes struct {
	Image         string    `yaml:"image"` // a key of Images
	Args          []string  `yaml:"args"`
	Ports         []int     `yaml:"ports"`
	ReadinessPath string    `yaml:"readiness_path"`
	RunAs         int64     `yaml:"run_as"` // uid and gid; never 0
	Resources     Resources `yaml:"resources"`
	Volumes       []Volume  `yaml:"volumes"`
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
)

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
	services, err := checkCompose(buf.Bytes(), m.Images)
	if err != nil {
		return App{}, fmt.Errorf("compose.yaml.tmpl: %w", err)
	}
	return App{Manifest: m, Compose: buf.Bytes(), Services: services}, nil
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
	if m.Kubernetes != nil {
		if m.Category == "third-party" {
			return fmt.Errorf("kubernetes: third-party apps run on Docker hosts only")
		}
		if err := m.Kubernetes.validate(m.Images); err != nil {
			return fmt.Errorf("kubernetes.%w", err)
		}
	}
	return nil
}

func (k Kubernetes) validate(images map[string]string) error {
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
	if k.RunAs < 1 || k.RunAs > 2147483647 {
		return fmt.Errorf("run_as %d must be a non-root uid", k.RunAs)
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

// checkCompose returns the sorted service names, refusing builds and any image the
// manifest does not pin.
func checkCompose(b []byte, images map[string]string) ([]string, error) {
	var c struct {
		Services map[string]struct {
			Image string `yaml:"image"`
			Build any    `yaml:"build"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if len(c.Services) == 0 {
		return nil, fmt.Errorf("no services")
	}
	pinned := map[string]bool{}
	for _, ref := range images {
		pinned[ref] = true
	}
	var names []string
	for name, s := range c.Services {
		if s.Build != nil {
			return nil, fmt.Errorf("service %s: build is not allowed", name)
		}
		if !pinned[s.Image] {
			return nil, fmt.Errorf("service %s: image %q is not one of the manifest's pinned images", name, s.Image)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
