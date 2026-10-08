// Package stack reads stack.yaml, the secret-free plan every command consumes.
package stack

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

const DefaultRoot = "/opt/kyquickstart"

type Stack struct {
	Version int      `yaml:"version"`
	Targets []Target `yaml:"targets"`
	Apps    []App    `yaml:"apps"`
}

type Target struct {
	Name string `yaml:"name"`
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	User string `yaml:"user"`
	Root string `yaml:"root"`
}

type App struct {
	Name   string `yaml:"name"`
	Target string `yaml:"target"`
}

var (
	nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	hostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	userRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	rootRE = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)
)

// ValidName reports whether s is a usable target or app name. App names become step IDs,
// file names and Compose project names.
func ValidName(s string) bool { return nameRE.MatchString(s) }

func Load(path string) (*Stack, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

func Parse(b []byte) (*Stack, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var s Stack
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("stack.yaml: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("stack.yaml: must hold exactly one document")
	}
	for i := range s.Targets {
		if s.Targets[i].Port == 0 {
			s.Targets[i].Port = 22
		}
		if s.Targets[i].Root == "" {
			s.Targets[i].Root = DefaultRoot
		}
	}
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("stack.yaml: %w", err)
	}
	return &s, nil
}

func (s *Stack) Target(name string) (Target, bool) {
	for _, t := range s.Targets {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}

func (s *Stack) validate() error {
	if s.Version != 1 {
		return fmt.Errorf("version %d: only version 1 is supported", s.Version)
	}
	if len(s.Targets) == 0 {
		return errors.New("no targets")
	}
	seen := map[string]bool{}
	for _, t := range s.Targets {
		switch {
		case !ValidName(t.Name):
			return fmt.Errorf("target %q: name must match %s", t.Name, nameRE)
		case seen[t.Name]:
			return fmt.Errorf("duplicate target %q", t.Name)
		case !hostRE.MatchString(t.Host) && net.ParseIP(t.Host) == nil:
			return fmt.Errorf("target %q: host %q is not a hostname or IP address", t.Name, t.Host)
		case t.Port < 1 || t.Port > 65535:
			return fmt.Errorf("target %q: port %d is out of range", t.Name, t.Port)
		case !userRE.MatchString(t.User):
			return fmt.Errorf("target %q: user %q must match %s", t.Name, t.User, userRE)
		case !validRoot(t.Root):
			return fmt.Errorf("target %q: root %q must be an absolute path without . or ..", t.Name, t.Root)
		}
		seen[t.Name] = true
	}
	apps := map[string]bool{}
	for _, a := range s.Apps {
		switch {
		case !ValidName(a.Name):
			return fmt.Errorf("app %q: name must match %s", a.Name, nameRE)
		case apps[a.Name]:
			return fmt.Errorf("duplicate app %q", a.Name)
		case !seen[a.Target]:
			return fmt.Errorf("app %q: unknown target %q", a.Name, a.Target)
		}
		apps[a.Name] = true
	}
	return nil
}

func validRoot(r string) bool {
	if !rootRE.MatchString(r) {
		return false
	}
	for _, seg := range strings.Split(r[1:], "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}
