package edge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

const certPath = "/api/nginx/certificates"

// NPM 2.16 takes the Let's Encrypt account email from the requesting NPM user and refuses
// unknown certificate meta. So each email gets a passwordless NPM user carrying this
// installation's mark, and a certificate is ours only when that user owns it.
type npmUser struct {
	ID       int      `json:"id"`
	Email    string   `json:"email"`
	Nickname string   `json:"nickname"`
	Roles    []string `json:"roles"`
}

type certificate struct {
	ID       int      `json:"id"`
	Owner    int      `json:"owner_user_id"`
	Provider string   `json:"provider"`
	Domains  []string `json:"domain_names"`
	Expires  string   `json:"expires_on"`
}

// dnsCredentials maps an allowed dns_provider to its certbot credentials key.
var dnsCredentials = map[string]string{"cloudflare": "dns_cloudflare_api_token"}

// cloudflareToken also keeps the value from adding lines to certbot's credentials file.
var cloudflareToken = regexp.MustCompile(`^[A-Za-z0-9_-]{20,200}$`)

func (n *NPM) acmeMark(e stack.Edge) string { return "kyquickstart:" + n.installation + ":" + e.Target }

// acmeUser returns this installation's NPM user for email, nil if absent. Any other user with
// that email is refused: NPM signs in by email, so a second one could shadow the operator's.
func (n *NPM) acmeUser(ctx context.Context, e stack.Edge, email string) (*npmUser, error) {
	var users []npmUser
	if err := n.request(ctx, http.MethodGet, "/api/users", nil, &users); err != nil {
		return nil, err
	}
	var found *npmUser
	for i, u := range users {
		if !strings.EqualFold(u.Email, email) {
			continue
		}
		if u.Nickname != n.acmeMark(e) || len(u.Roles) != 0 || found != nil {
			return nil, fmt.Errorf("NPM user %s exists and is not this installation's certificate user; give the certificate another email, such as a +acme address", email)
		}
		found = &users[i]
	}
	return found, nil
}

// ownedCert returns c's certificate in NPM, nil if absent. Certificates of other owners are
// never considered.
func (n *NPM) ownedCert(ctx context.Context, e stack.Edge, c stack.Certificate) (*certificate, error) {
	u, err := n.acmeUser(ctx, e, c.Email)
	if err != nil || u == nil {
		return nil, err
	}
	var certs []certificate
	if err := n.request(ctx, http.MethodGet, certPath, nil, &certs); err != nil {
		return nil, err
	}
	var found *certificate
	for i, cert := range certs {
		if cert.Owner != u.ID || cert.Provider != "letsencrypt" || !sameSet(cert.Domains, c.Domains) {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("certificate %s has more than one NPM certificate; delete the extra one in NPM", c.Name)
		}
		found = &certs[i]
	}
	return found, nil
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// certificateID resolves a route's certificate to its NPM ID.
func (n *NPM) certificateID(ctx context.Context, e stack.Edge, r stack.Route) (int, error) {
	i := slices.IndexFunc(e.Certificates, func(c stack.Certificate) bool { return c.Name == r.Certificate })
	if i < 0 {
		return r.CertificateID, nil
	}
	cert, err := n.ownedCert(ctx, e, e.Certificates[i])
	if err == nil && cert == nil {
		err = fmt.Errorf("certificate %s is not issued yet; run kyquickstart apply", r.Certificate)
	}
	if err != nil {
		return 0, err
	}
	return cert.ID, nil
}

// dnsCredential reads c's DNS API token from the target and registers it for redaction.
func (n *NPM) dnsCredential(ctx context.Context, c stack.Certificate) (string, error) {
	b, err := n.Secrets.EdgeSecret(ctx, c.Credentials.Secret, c.Credentials.Key)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	n.Redact.Add(token)
	if !cloudflareToken.MatchString(token) {
		return "", fmt.Errorf("secret kyq-edge/%s key %s is not a Cloudflare API token", c.Credentials.Secret, c.Credentials.Key)
	}
	return token, nil
}

// acmeToken creates or repairs the certificate user for email and signs in as it.
func (n *NPM) acmeToken(ctx context.Context, e stack.Edge, email string) (string, error) {
	u, err := n.acmeUser(ctx, e, email)
	if err != nil {
		return "", err
	}
	if u == nil {
		// No auth: it has no password, so only an administrator's "sign in as" reaches it.
		u = &npmUser{}
		body := map[string]any{"name": "KyQuickStart certificates", "nickname": n.acmeMark(e), "email": email, "roles": []string{}, "is_disabled": false}
		if err := n.request(ctx, http.MethodPost, "/api/users", body, u); err != nil {
			return "", err
		}
	}
	// Every run, so a user created by an interrupted run never keeps NPM's manage-all default.
	perms := map[string]string{"visibility": "user", "certificates": "manage", "proxy_hosts": "hidden",
		"redirection_hosts": "hidden", "dead_hosts": "hidden", "streams": "hidden", "access_lists": "hidden"}
	if err := n.request(ctx, http.MethodPut, fmt.Sprintf("/api/users/%d/permissions", u.ID), perms, nil); err != nil {
		return "", err
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := n.request(ctx, http.MethodPost, fmt.Sprintf("/api/users/%d/login", u.ID), nil, &login); err != nil {
		return "", err
	}
	n.Redact.Add(login.Token)
	if login.Token == "" {
		return "", errors.New("NPM issued no token for the certificate user")
	}
	return login.Token, nil
}

// certStep has NPM request c and checks it stays valid. Renewal is NPM's job.
type certStep struct {
	n *NPM
	e stack.Edge
	c stack.Certificate
}

func (s certStep) ID() string { return s.e.Target + ".edge.cert." + s.c.Name }
func (s certStep) InputHash() string {
	b, _ := json.Marshal(struct {
		API, Installation string
		Certificate       stack.Certificate // references only, never the credential
	}{s.n.base, s.n.installation, s.c})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// check returns the owned certificate, nil if absent, and refuses one that is not valid now.
func (s certStep) check(ctx context.Context) (*certificate, error) {
	cert, err := s.n.ownedCert(ctx, s.e, s.c)
	if err != nil || cert == nil {
		return nil, err
	}
	expires, err := expiry(cert.Expires)
	switch {
	case err != nil:
		return nil, fmt.Errorf("NPM certificate %s (#%d) has no expiry yet: issuance is in progress or failed; rerun apply later", s.c.Name, cert.ID)
	case !expires.After(time.Now()):
		return nil, fmt.Errorf("NPM certificate %s (#%d) expired on %s; NPM renews it, so check its renewal log and the DNS credential", s.c.Name, cert.ID, cert.Expires)
	}
	return cert, nil
}

func (s certStep) Inspect(ctx context.Context) (bool, error) {
	cert, err := s.check(ctx)
	return cert != nil, err
}

func (s certStep) Apply(ctx context.Context) error {
	token, err := s.n.dnsCredential(ctx, s.c)
	if err != nil {
		return err
	}
	acme, err := s.n.acmeToken(ctx, s.e, s.c.Email)
	if err != nil {
		return err
	}
	body := map[string]any{"provider": "letsencrypt", "domain_names": s.c.Domains, "meta": map[string]any{
		"dns_challenge": true, "dns_provider": s.c.DNSProvider,
		"dns_provider_credentials": dnsCredentials[s.c.DNSProvider] + " = " + token,
	}}
	// NPM answers once certbot finishes, allowing it 15 minutes.
	slow := *s.n.http
	slow.Timeout = 16 * time.Minute
	if _, err := call(ctx, &slow, s.n.base, acme, http.MethodPost, certPath, body, &certificate{}); err != nil {
		// Never retried within a run: each attempt counts against Let's Encrypt's rate limits.
		return fmt.Errorf("NPM did not issue certificate %s (%s); see /data/logs/letsencrypt.log in NPM", s.c.Name, err.Error())
	}
	return nil
}

func (s certStep) Verify(ctx context.Context) error {
	cert, err := s.check(ctx)
	if err == nil && cert == nil {
		err = fmt.Errorf("NPM certificate %s is absent", s.c.Name)
	}
	return err
}
