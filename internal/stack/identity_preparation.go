package stack

import (
	"encoding/json"
	"errors"
	"net"
	"net/mail"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// IdentityPreparation carries intent only; the product derives credential paths
// and generates credentials on its target. Destination SCIM tokens are references.
type IdentityPreparation struct {
	Schema   string           `yaml:"schema" json:"schema"`
	SetupID  string           `yaml:"setup_id" json:"setupId"`
	Admin    IdentityAccount  `yaml:"admin" json:"admin"`
	Everyday IdentityAccount  `yaml:"everyday" json:"everyday"`
	Clients  []IdentityClient `yaml:"clients" json:"clients"`
	Groups   []IdentityGroup  `yaml:"groups,omitempty" json:"groups,omitempty"`
	Mail     *IdentityMail    `yaml:"mail,omitempty" json:"mail,omitempty"`
}

// IdentityMail is KyIdentity's SMTP relay, set by its first setup only. PasswordFile is a
// workstation file; apply sends its bytes on stdin to KyIdentity's copy, TargetFile.
type IdentityMail struct {
	Host         string `yaml:"host" json:"host"`
	Port         int    `yaml:"port" json:"port"`
	Security     string `yaml:"security" json:"security"` // tls or starttls; never plaintext
	From         string `yaml:"from" json:"from"`
	Username     string `yaml:"username,omitempty" json:"username,omitempty"`
	PasswordFile string `yaml:"password_file,omitempty" json:"-"`
	TargetFile   string `yaml:"-" json:"passwordFile,omitempty"`
}

func (m IdentityMail) validate() error {
	var domain string
	from, err := mail.ParseAddress(m.From)
	if err == nil {
		_, domain, _ = strings.Cut(from.Address, "@")
	}
	switch {
	case !Hostname(m.Host) && net.ParseIP(m.Host) == nil:
		return errors.New("identity.mail.host must be a lowercase DNS name or an IP address")
	case m.Port < 1 || m.Port > 65535:
		return errors.New("identity.mail.port must be 1-65535")
	case m.Security != "tls" && m.Security != "starttls":
		return errors.New("identity.mail.security must be tls or starttls; plaintext SMTP is never used")
	case err != nil || !Hostname(strings.ToLower(domain)) || strings.ContainsAny(m.From, "\r\n"):
		return errors.New("identity.mail.from must be a mail address, optionally with a name")
	case (m.Username == "") != (m.PasswordFile == ""):
		return errors.New("identity.mail.username and password_file come together, or neither for a relay without authentication")
	case m.Username != "" && (len(m.Username) > 256 || strings.ContainsFunc(m.Username, func(r rune) bool { return r < 0x21 || r == 0x7f })):
		return errors.New("identity.mail.username must be 1-256 printable characters")
	case m.PasswordFile != "" && !validRoot(m.PasswordFile):
		return errors.New("identity.mail.password_file must be an absolute path on this workstation")
	}
	return nil
}

type IdentityAccount struct {
	Username string `yaml:"username" json:"username"`
	Email    string `yaml:"email" json:"email"`
}
type IdentityClient struct {
	ID                string           `yaml:"id" json:"id"`
	Name              string           `yaml:"name" json:"name"`
	RedirectURIs      []string         `yaml:"redirect_uris" json:"redirectUris"`
	PostLogoutURIs    []string         `yaml:"post_logout_uris,omitempty" json:"postLogoutUris"`
	AdministratorRole string           `yaml:"administrator_role,omitempty" json:"administratorRole,omitempty"`
	EverydayRole      string           `yaml:"everyday_role,omitempty" json:"everydayRole,omitempty"`
	SCIM              *IdentitySCIM    `yaml:"scim,omitempty" json:"scim,omitempty"`
	SuiteWebhook      *IdentityWebhook `yaml:"suite_webhook,omitempty" json:"suiteWebhook,omitempty"`
	Groups            []string         `yaml:"groups,omitempty" json:"groups,omitempty"`
}
type IdentitySCIM struct {
	URL       string `yaml:"url" json:"url"`
	TokenFile string `yaml:"token_file" json:"tokenFile"`
	Groups    bool   `yaml:"groups,omitempty" json:"groups,omitempty"`
}
type IdentityWebhook struct {
	URL string `yaml:"url" json:"url"`
}
type IdentityGroup struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Members     []string `yaml:"members" json:"members"`
}

var identitySetupName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var identitySetupRole = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)
var identitySetupGroup = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,63}$`)

func (n *IdentityPreparation) validate(bundle string) error {
	bad := errors.New("invalid secret-free KyIdentity preparation metadata")
	id, err := uuid.Parse(n.SetupID)
	scope := path.Dir(bundle)
	if n.Schema != "kyidentity.setup/1" || err != nil || id.String() != n.SetupID || path.Base(bundle) != "setup.json" || path.Base(scope) != n.SetupID || path.Base(path.Dir(scope)) != "installer" || len(n.Clients) > 32 || len(n.Groups) > 32 {
		return bad
	}
	if strings.EqualFold(n.Admin.Username, n.Everyday.Username) || strings.EqualFold(n.Admin.Email, n.Everyday.Email) {
		return bad
	}
	for _, a := range []IdentityAccount{n.Admin, n.Everyday} {
		address, err := mail.ParseAddress(a.Email)
		if !identitySetupName.MatchString(a.Username) || err != nil || address.Address != a.Email || len(a.Email) > 254 {
			return bad
		}
	}
	groups, names := map[string]bool{}, map[string]bool{}
	for _, g := range n.Groups {
		if !identitySetupGroup.MatchString(g.Name) || strings.TrimSpace(g.Name) != g.Name || names[strings.ToLower(g.Name)] || len(g.Description) > 1024 || strings.ContainsAny(g.Description, "\x00\r\n") || len(g.Members) > 1 || len(g.Members) == 1 && g.Members[0] != n.Everyday.Username {
			return bad
		}
		groups[g.Name], names[strings.ToLower(g.Name)] = true, true
	}
	clients := map[string]bool{}
	for _, c := range n.Clients {
		if !identitySetupName.MatchString(c.ID) || clients[strings.ToLower(c.ID)] || len(c.Name) < 1 || len(c.Name) > 128 || strings.ContainsAny(c.Name, "\r\n\t") || len(c.RedirectURIs) == 0 || len(c.RedirectURIs) > 16 || len(c.PostLogoutURIs) > 16 || c.SCIM != nil && c.SuiteWebhook != nil {
			return bad
		}
		clients[strings.ToLower(c.ID)] = true
		if c.AdministratorRole != "" && !identitySetupRole.MatchString(c.AdministratorRole) || c.EverydayRole != "" && !identitySetupRole.MatchString(c.EverydayRole) || c.AdministratorRole != "" && strings.EqualFold(c.AdministratorRole, c.EverydayRole) {
			return bad
		}
		for _, uri := range append(append([]string{}, c.RedirectURIs...), c.PostLogoutURIs...) {
			u, err := url.Parse(uri)
			if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(uri) > 2048 || strings.ContainsAny(uri, "\r\n\t ") {
				return bad
			}
		}
		if len(c.Groups) > 32 || len(c.Groups) > 0 && c.AdministratorRole != "" && c.EverydayRole == "" {
			return bad
		}
		seen := map[string]bool{}
		for _, group := range c.Groups {
			if !groups[group] || seen[group] {
				return bad
			}
			seen[group] = true
		}
		if c.SuiteWebhook != nil && !preparationURL(c.SuiteWebhook.URL) {
			return bad
		}
		if c.SCIM != nil {
			if !preparationURL(c.SCIM.URL) || !preparationPath(c.SCIM.TokenFile) || c.SCIM.TokenFile == bundle || strings.HasPrefix(c.SCIM.TokenFile, scope+"/") || c.SCIM.Groups && len(c.Groups) == 0 {
				return bad
			}
		}
	}
	if n.Mail != nil {
		if err := n.Mail.validate(); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(n)
	if err != nil || len(raw) > 128<<10 {
		return bad
	}
	return nil
}
