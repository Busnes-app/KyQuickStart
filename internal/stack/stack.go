// Package stack reads stack.yaml, the secret-free plan every command consumes.
package stack

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/util/validation"
)

const DefaultRoot = "/opt/kyquickstart"

type Stack struct {
	Demo         bool          `yaml:"demo,omitempty"`
	Version      int           `yaml:"version"`
	Revision     int           `yaml:"revision,omitempty"` // +1 on every write; 0 only in files that predate it
	Targets      []Target      `yaml:"targets"`
	Apps         []App         `yaml:"apps"`
	Edges        []Edge        `yaml:"edges,omitempty"`
	Domains      *Domains      `yaml:"domains,omitempty"` // when set, keeps internal and external route names apart
	Existing     []Existing    `yaml:"existing,omitempty"`
	Integrations []Integration `yaml:"integrations,omitempty"`
	// InstallationID (UUID v4) marks this installation's NPM hosts; written once, by plan or apply.
	InstallationID string `yaml:"installation_id,omitempty"`
}

// Integration explicitly selects product-owned setup, independently of references.
// Bundle is a protected path inside the container, never bundle contents.
type Integration struct {
	Product        string               `yaml:"product"`
	Target         string               `yaml:"target"`
	Container      string               `yaml:"container"`
	Namespace      string               `yaml:"namespace,omitempty"`
	Pod            string               `yaml:"pod,omitempty"`
	Bundle         string               `yaml:"bundle"`
	Notes          *NotesPreparation    `yaml:"notes,omitempty"`
	Identity       *IdentityPreparation `yaml:"identity,omitempty"`
	IdentityClient string               `yaml:"identity_client,omitempty"`
	// IdentityAmendment is the KyIdentity setup amendment that added an installed app's client
	// after KyIdentity's first setup; apply records it once.
	IdentityAmendment string `yaml:"identity_amendment,omitempty"`
	// ChatClient is an installed app's second KyIdentity client (KyMessages' MAS), which comes
	// with identity_client in the same first setup or amendment.
	ChatClient string `yaml:"chat_client,omitempty" json:",omitempty"`
}

// Existing is a reference, never permission to adopt or change a workload.
type Existing struct {
	Product string `yaml:"product"`
	Target  string `yaml:"target"`
	URL     string `yaml:"url"`
}

type Target struct {
	Name       string `yaml:"name"`
	Host       string `yaml:"host"`
	Port       int    `yaml:"port"`
	User       string `yaml:"user"`
	Root       string `yaml:"root"`
	Kubeconfig string `yaml:"kubeconfig"` // set only for a Kubernetes target; a path, never contents
	Context    string `yaml:"context"`    // empty uses the kubeconfig's current context
	// StorageClass names the class for the target's claims; empty needs a cluster default.
	StorageClass string `yaml:"storage_class,omitempty"`
	// Backup is a Docker target's off-host restic rest-server; nil means none.
	Backup *BackupDestination `yaml:"backup,omitempty"`
}

// BackupDestination is a restic rest-server reached over TLS. Each app's repository is
// Repository/<app>; the server's --private-repos keeps the user inside its own path.
type BackupDestination struct {
	Repository     string `yaml:"repository"`        // rest:https://<host>[:port]/<user>
	CredentialFile string `yaml:"credential_file"`   // target path: the rest-server password for <user>
	CAFile         string `yaml:"ca_file,omitempty"` // target path: the server's CA; empty uses system trust
}

// restUserRE is the one path segment naming the rest-server user.
var restUserRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// RestUser returns the rest-server user, the repository's one path segment.
func (b BackupDestination) RestUser() string {
	u, _ := url.Parse(strings.TrimPrefix(b.Repository, "rest:"))
	return strings.Trim(u.Path, "/")
}

// ValidBackupRepository reports whether s is a rest:https repository URL kyquickstart accepts.
func ValidBackupRepository(s string) bool { return validRepository(s) == nil }

// ValidTargetPath reports whether s is a clean absolute path without . or .. segments.
func ValidTargetPath(s string) bool { return validRoot(s) }

func validRepository(repo string) error {
	raw, ok := strings.CutPrefix(repo, "rest:")
	u, err := url.Parse(raw)
	switch {
	case !ok || err != nil || u.Scheme != "https" || u.Opaque != "":
		return errors.New("backup.repository must be rest:https://<host>[:port]/<user>")
	case u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#"):
		return errors.New("backup.repository takes no credentials, query or fragment: the password is in credential_file")
	case !hostRE.MatchString(u.Hostname()) && net.ParseIP(u.Hostname()) == nil || !validPort(u.Port()):
		return errors.New("backup.repository needs a hostname or IP address and an optional port 1-65535")
	case !restUserRE.MatchString(strings.TrimPrefix(u.Path, "/")) || u.RawPath != "":
		return errors.New("backup.repository needs exactly one path segment, the rest-server user")
	}
	return nil
}

func (b BackupDestination) validate() error {
	if err := validRepository(b.Repository); err != nil {
		return err
	}
	switch {
	case !validRoot(b.CredentialFile):
		return errors.New("backup.credential_file must be an absolute path on the target")
	case b.CAFile != "" && !validRoot(b.CAFile):
		return errors.New("backup.ca_file must be an absolute path on the target")
	}
	return nil
}

// Kubernetes reports whether the target is a cluster reached through a kubeconfig.
func (t Target) Kubernetes() bool { return t.Kubeconfig != "" }

type App struct {
	Name   string `yaml:"name"`
	Target string `yaml:"target"`
	// NodePorts overrides an edge's published NodePorts: container port -> node port.
	NodePorts map[int]int `yaml:"node_ports,omitempty"`
	// MediaAddress is the one IPv4 address of a Docker target the app publishes call media on.
	MediaAddress string `yaml:"media_address,omitempty" json:",omitempty"`
	// Stopped is the operator's `kyquickstart stop`: commands that would start the app refuse.
	Stopped bool `yaml:"stopped,omitempty" json:",omitempty"`
}

// MediaAddress accepts one unicast IPv4 address a LAN client can reach.
func MediaAddress(s string) bool {
	ip, err := netip.ParseAddr(s)
	return err == nil && ip.Is4() && ip.String() == s && !ip.IsUnspecified() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!ip.IsMulticast() && ip != netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

var (
	nameRE    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	hostRE    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	userRE    = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	rootRE    = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)
	contextRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:/-]{0,252}$`)
)

// ValidName reports whether s is a usable target or app name. App names become step IDs,
// file names and Compose project names.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// ValidNodePort is in the API server's default NodePort range.
func ValidNodePort(n int) bool { return n >= 30000 && n <= 32767 }

// NewInstallationID returns a random UUID v4.
func NewInstallationID() string { return uuid.NewString() }

func validInstallationID(s string) bool {
	u, err := uuid.Parse(s)
	return err == nil && u.String() == s && u.Version() == 4 && u.Variant() == uuid.RFC4122
}

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
		if s.Targets[i].Kubernetes() {
			continue
		}
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
	if s.Revision < 0 {
		return errors.New("revision must not be negative")
	}
	if s.InstallationID != "" && !validInstallationID(s.InstallationID) {
		return errors.New("installation_id must be a lowercase UUID v4")
	}
	if len(s.Targets) == 0 {
		return errors.New("no targets")
	}
	seen := map[string]bool{}
	for _, t := range s.Targets {
		if t.Kubernetes() {
			switch {
			case !ValidName(t.Name):
				return fmt.Errorf("target %q: name must match %s", t.Name, nameRE)
			case seen[t.Name]:
				return fmt.Errorf("duplicate target %q", t.Name)
			case t.Host != "" || t.User != "" || t.Root != "" || t.Port != 0:
				return fmt.Errorf("target %q: a Kubernetes target takes no ssh fields (host, port, user, root)", t.Name)
			case strings.ContainsAny(t.Kubeconfig, "\x00\n\r"):
				return fmt.Errorf("target %q: kubeconfig path contains a control character", t.Name)
			case t.Context != "" && !contextRE.MatchString(t.Context):
				return fmt.Errorf("target %q: context %q must match %s", t.Name, t.Context, contextRE)
			case t.StorageClass != "" && len(validation.IsDNS1123Subdomain(t.StorageClass)) > 0:
				return fmt.Errorf("target %q: storage_class %q must be a DNS-1123 subdomain name", t.Name, t.StorageClass)
			case t.Backup != nil:
				return fmt.Errorf("target %q: backup destinations are for Docker targets; Kubernetes apps are not backed up by kyquickstart backup", t.Name)
			}
			seen[t.Name] = true
			continue
		}
		if t.Context != "" || t.StorageClass != "" {
			return fmt.Errorf("target %q: context and storage_class need kubeconfig", t.Name)
		}
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
		if t.Backup != nil {
			if err := t.Backup.validate(); err != nil {
				return fmt.Errorf("target %q: %w", t.Name, err)
			}
		}
		seen[t.Name] = true
	}
	apps, nodePorts := map[string]bool{}, map[int]bool{}
	for _, a := range s.Apps {
		switch {
		case !ValidName(a.Name):
			return fmt.Errorf("app %q: name must match %s", a.Name, nameRE)
		case apps[a.Name]:
			return fmt.Errorf("duplicate app %q", a.Name)
		case !seen[a.Target]:
			return fmt.Errorf("app %q: unknown target %q", a.Name, a.Target)
		}
		if a.MediaAddress != "" && !MediaAddress(a.MediaAddress) {
			return fmt.Errorf("app %q: media_address %q must be one IPv4 address of its target: not a hostname, IPv6, unspecified, loopback, link-local, multicast or broadcast", a.Name, a.MediaAddress)
		}
		apps[a.Name] = true
		for _, n := range a.NodePorts {
			if !ValidNodePort(n) || nodePorts[n] {
				return fmt.Errorf("app %q: node_ports %d must be unique and 30000-32767", a.Name, n)
			}
			nodePorts[n] = true
		}
	}
	references := map[string]bool{}
	for _, e := range s.Existing {
		if references[e.Product] || apps[e.Product] {
			return errors.New("an existing product must be unique and cannot also be selected for installation")
		}
		references[e.Product] = true
		if !ValidName(e.Product) || !seen[e.Target] {
			return errors.New("existing product needs a valid name and known target")
		}
		u, err := url.Parse(e.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("existing product URL must be HTTPS without credentials, query or fragment")
		}
	}
	if err := s.validateIntegrations(); err != nil {
		return err
	}
	return s.validateEdges()
}

func validPort(p string) bool {
	if p == "" {
		return true
	}
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
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
