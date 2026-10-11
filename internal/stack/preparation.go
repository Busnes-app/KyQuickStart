package stack

import (
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"
)

// NotesPreparation contains only native metadata and target-local credential references.
// Actual credential values have no field in the saved plan or preparation wire format.
type NotesPreparation struct {
	Version int          `yaml:"version" json:"version"`
	SSO     *NotesSSO    `yaml:"sso,omitempty" json:"sso,omitempty"`
	Admins  []NotesAdmin `yaml:"admins,omitempty" json:"admins,omitempty"`
	Backup  *NotesBackup `yaml:"backup,omitempty" json:"backup,omitempty"`
}
type NotesSSO struct {
	IssuerURL               string `yaml:"issuer_url" json:"issuerUrl"`
	ClientID                string `yaml:"client_id" json:"clientId"`
	ClientSecretFile        string `yaml:"client_secret_file" json:"clientSecretFile"`
	RedirectURI             string `yaml:"redirect_uri" json:"redirectUri"`
	DirectoryHMACSecretFile string `yaml:"directory_hmac_secret_file,omitempty" json:"directoryHmacSecretFile,omitempty"`
}
type NotesAdmin struct {
	Issuer   string `yaml:"issuer" json:"issuer"`
	Subject  string `yaml:"subject" json:"subject"`
	Username string `yaml:"username" json:"username"`
}
type NotesBackup struct {
	Dir             string         `yaml:"dir,omitempty" json:"dir,omitempty"`
	Keep            int            `yaml:"keep,omitempty" json:"keep,omitempty"`
	DepositInterval string         `yaml:"deposit_interval,omitempty" json:"depositInterval,omitempty"`
	Recovery        *NotesRecovery `yaml:"recovery,omitempty" json:"recovery,omitempty"`
}
type NotesRecovery struct {
	URL             string `yaml:"url" json:"url"`
	PairingCodeFile string `yaml:"pairing_code_file" json:"pairingCodeFile"`
}

func preparationPath(s string) bool {
	return path.IsAbs(s) && path.Clean(s) == s && len(s) <= 4096 && !strings.ContainsFunc(s, unicode.IsControl)
}
func preparationIdentifier(s string) bool {
	return s != "" && len(s) <= 256 && strings.TrimSpace(s) == s && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
}
func preparationURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && len(s) <= 2048 && !strings.ContainsAny(s, "# \t\r\n\x00")
}

func (n *NotesPreparation) validate(bundle string) error {
	bad := errors.New("invalid secret-free KyNotes preparation metadata")
	if n.Version != 1 || len(n.Admins) > 16 || n.SSO == nil && len(n.Admins) == 0 && n.Backup == nil {
		return bad
	}
	paths := []string{}
	if s := n.SSO; s != nil {
		u, _ := url.Parse(s.RedirectURI)
		if !preparationURL(s.IssuerURL) || !preparationIdentifier(s.ClientID) || !preparationURL(s.RedirectURI) || u.Path != "/api/v1/auth/oidc/callback" {
			return bad
		}
		paths = append(paths, s.ClientSecretFile)
		if s.DirectoryHMACSecretFile != "" {
			paths = append(paths, s.DirectoryHMACSecretFile)
		}
	}
	names, subjects := map[string]bool{}, map[[2]string]bool{}
	for _, a := range n.Admins {
		key, name := [2]string{a.Issuer, a.Subject}, strings.ToLower(a.Username)
		if !preparationURL(a.Issuer) || !preparationIdentifier(a.Subject) || !preparationIdentifier(a.Username) || names[name] || subjects[key] || n.SSO != nil && a.Issuer != n.SSO.IssuerURL {
			return bad
		}
		names[name], subjects[key] = true, true
	}
	if b := n.Backup; b != nil {
		if b.Dir != "" && !preparationPath(b.Dir) || b.Keep < 0 {
			return bad
		}
		if b.DepositInterval != "" {
			d, err := time.ParseDuration(b.DepositInterval)
			if err != nil || d < 0 || d%time.Second != 0 || d != 0 && (d < 15*time.Minute || d > 366*24*time.Hour) {
				return bad
			}
		}
		if r := b.Recovery; r != nil {
			if !preparationURL(r.URL) {
				return bad
			}
			paths = append(paths, r.PairingCodeFile)
		}
	}
	for _, file := range paths {
		if !preparationPath(file) || file == bundle {
			return bad
		}
	}
	raw, err := json.Marshal(n)
	if err != nil || len(raw) > 64<<10 {
		return bad
	}
	return nil
}
