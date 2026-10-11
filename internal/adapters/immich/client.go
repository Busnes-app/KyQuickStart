// Package immich uses the pinned native administration API without deleting content.
package immich

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Binding comes from protected provisioning receipts, never an email lookup.
type Binding struct{ ID, Issuer, ClientID, Subject string }
type Account struct {
	Binding
	Administrator bool
	Name, Email   string
}
type Client struct {
	origin, token string
	http          *http.Client
}

var ErrUncertain = errors.New("Immich mutation requires outcome reconciliation")

type requestRefused struct{ status int }

func (requestRefused) Error() string { return "Immich request refused" }

var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var installationUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func visible(s string, limit int) bool {
	return s != "" && len(s) <= limit && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}
func https(s string, origin bool) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Opaque == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && !strings.Contains(s, "#") && (!origin || u.Path == "") && visible(s, 2048)
}
func New(origin, token string) (*Client, error) {
	if !https(origin, true) || !visible(token, 16384) || strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return nil, errors.New("Immich requires a verified HTTPS origin and private API key")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	transport.TLSClientConfig.InsecureSkipVerify = false
	transport.ResponseHeaderTimeout = 10 * time.Second
	transport.MaxResponseHeaderBytes = 8192
	return &Client{origin: origin, token: token, http: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}}, nil
}

func (c *Client) call(ctx context.Context, method, path string, body any, out any) error {
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return errors.New("invalid Immich request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+"/api"+path, bytes.NewReader(raw))
	if err != nil {
		return errors.New("invalid Immich request")
	}
	req.Header.Set("x-api-key", c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return errors.New("Immich request did not complete")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return requestRefused{response.StatusCode}
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if err != nil || len(raw) > 256<<10 || !utf8.Valid(raw) {
		return errors.New("invalid Immich response")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid Immich response")
	}
	return nil
}

type nativeAccount struct {
	ID, OAuthID, Name, Email, Status string
	IsAdmin                          *bool
	DeletedAt                        json.RawMessage
}

// boundAccount checks only the native UUID/subject binding. Revocation uses it so a
// disabled or rotated provider, or an inactive/soft-deleted account, can still be fenced.
func (c *Client) boundAccount(ctx context.Context, b Binding) (nativeAccount, error) {
	if !uuid.MatchString(b.ID) || !https(b.Issuer, false) || !visible(b.ClientID, 255) || strings.IndexFunc(b.ClientID, unicode.IsSpace) >= 0 || !visible(b.Subject, 255) {
		return nativeAccount{}, errors.New("Immich requires exact native/provider/subject receipts")
	}
	if err := c.qualifiedVersion(ctx); err != nil {
		return nativeAccount{}, err
	}
	var native nativeAccount
	if c.call(ctx, http.MethodGet, "/admin/users/"+b.ID, nil, &native) != nil || native.ID != b.ID || native.OAuthID != b.Subject {
		return nativeAccount{}, errors.New("Immich native account receipt conflicts")
	}
	return native, nil
}

func (c *Client) Inspect(ctx context.Context, b Binding) (Account, error) {
	native, err := c.boundAccount(ctx, b)
	if err != nil {
		return Account{}, err
	}
	var config struct {
		OAuth struct {
			Enabled, AllowInsecureRequests *bool
			IssuerURL, ClientID            string
		}
	}
	if c.call(ctx, http.MethodGet, "/system-config", nil, &config) != nil || config.OAuth.Enabled == nil || !*config.OAuth.Enabled || config.OAuth.AllowInsecureRequests == nil || *config.OAuth.AllowInsecureRequests || config.OAuth.IssuerURL != b.Issuer || config.OAuth.ClientID != b.ClientID {
		return Account{}, errors.New("Immich provider receipt conflicts")
	}
	if native.IsAdmin == nil || native.Status != "active" || string(native.DeletedAt) != "null" || !visible(native.Name, 255) || !visible(native.Email, 254) {
		return Account{}, errors.New("Immich native account receipt conflicts")
	}
	return Account{Binding: b, Administrator: *native.IsAdmin, Name: native.Name, Email: native.Email}, nil
}

// SetAdministrator projects an explicit protected assignment. Callers serialize
// the account and persist intent before calling; an uncertain result holds later writes.
func (c *Client) SetAdministrator(ctx context.Context, b Binding, administrator bool) error {
	account, err := c.Inspect(ctx, b)
	if err != nil {
		return err
	}
	if account.Administrator == administrator {
		return nil
	}
	var ignored json.RawMessage
	if c.call(ctx, http.MethodPut, "/admin/users/"+b.ID, map[string]bool{"isAdmin": administrator}, &ignored) != nil {
		return ErrUncertain
	}
	verified, err := c.Inspect(ctx, b)
	if err != nil || verified.Administrator != administrator || verified.Name != account.Name || verified.Email != account.Email {
		return ErrUncertain
	}
	return nil
}

func (c *Client) qualifiedVersion(ctx context.Context) error {
	var version struct{ Major, Minor, Patch *int }
	if c.call(ctx, http.MethodGet, "/server/version", nil, &version) != nil || version.Major == nil || version.Minor == nil || version.Patch == nil || *version.Major != 3 || *version.Minor != 2 || *version.Patch != 0 {
		return errors.New("unqualified Immich version")
	}
	return nil
}
