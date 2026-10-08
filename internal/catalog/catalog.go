// Package catalog loads app manifests and renders their Compose files.
package catalog

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"text/template"

	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

type Manifest struct {
	Name      string            `yaml:"name"`
	Images    map[string]string `yaml:"images"`
	Secrets   []string          `yaml:"secrets"`
	DependsOn []string          `yaml:"depends_on"`
	Health    Health            `yaml:"health"`
}

type Health struct {
	TimeoutSeconds int `yaml:"timeout_seconds"`
}

// App is a validated manifest with its rendered Compose file.
type App struct {
	Manifest
	Compose  []byte
	Services []string // sorted
}

type Catalog map[string]App

var (
	keyRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	imageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
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
	if err != nil {
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
