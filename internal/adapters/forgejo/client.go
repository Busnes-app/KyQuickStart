// Package forgejo uses the application's native administration API.
package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Binding pins the native account and authentication source to an OIDC subject.
// Its values come from verified provisioning, never a username-only directory event.
type Binding struct {
	ID       int64
	Username string
	SourceID int64
	Subject  string
}

type Account struct {
	Binding
	Admin, Active, LoginProhibited bool
	Name, Email                    string
}

// AccountSpec identifies a new account or a previously pinned native account.
// SourceID must name the installer-owned, verified KyIdentity authentication source.
type AccountSpec struct {
	Username, Email, Subject string
	Name                     string
	SourceID, ExistingID     int64
}

type apiError struct{ status int }

func (e *apiError) Error() string { return fmt.Sprintf("Forgejo refused request (%d)", e.status) }

type Client struct {
	origin, token string
	http          *http.Client
}

func New(origin, token string) (*Client, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("Forgejo requires a verified HTTPS origin")
	}
	if token == "" || len(token) > 16384 || strings.ContainsAny(token, "\r\n\x00") {
		return nil, errors.New("Forgejo credential is missing or invalid")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{origin: origin, token: token, http: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}}, nil
}

var username = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,39}$`)

func validate(b Binding) error {
	if b.ID <= 0 || b.SourceID <= 0 || !username.MatchString(b.Username) || b.Subject == "" || len(b.Subject) > 255 || strings.ContainsAny(b.Subject, "\x00\r\n") {
		return errors.New("Forgejo requires an exact account/source/subject binding")
	}
	return nil
}

func (c *Client) call(ctx context.Context, method, path string, body, out any, headers ...*http.Header) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return errors.New("invalid Forgejo request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+"/api/v1"+path, bytes.NewReader(data))
	if err != nil {
		return errors.New("invalid Forgejo request")
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return errors.New("Forgejo request did not complete; inspect state before retrying")
	}
	defer response.Body.Close()
	if method == http.MethodDelete && response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &apiError{status: response.StatusCode}
	}
	if len(headers) > 0 {
		*headers[0] = response.Header.Clone()
	}
	if out == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, out) != nil {
		return errors.New("invalid or oversized Forgejo response")
	}
	return nil
}

// Apply provisions through the native API or verifies a retained exact binding.
// A name collision is a conflict even if its profile appears to match. Persist the
// returned native ID on the target before a repeat run; this method never adopts,
// reactivates or changes the password/authority of an existing account.
func (c *Client) Apply(ctx context.Context, spec AccountSpec) (Binding, error) {
	b := Binding{ID: spec.ExistingID, Username: spec.Username, SourceID: spec.SourceID, Subject: spec.Subject}
	validationBinding := b
	validationBinding.ID = 1
	if !validProfile(Profile{Name: spec.Name, Email: spec.Email}) || validate(validationBinding) != nil || spec.ExistingID < 0 {
		return Binding{}, errors.New("Forgejo account specification is invalid")
	}
	if spec.ExistingID > 0 {
		if _, err := c.Inspect(ctx, b); err != nil {
			return Binding{}, err
		}
		return b, nil
	}
	var existing map[string]any
	err := c.call(ctx, http.MethodGet, "/users/"+url.PathEscape(spec.Username), nil, &existing)
	var refused *apiError
	if err == nil {
		return Binding{}, errors.New("Forgejo account already exists without a pinned native ID")
	}
	if !errors.As(err, &refused) || refused.status != http.StatusNotFound {
		return Binding{}, err
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := c.call(ctx, http.MethodPost, "/admin/users", map[string]any{"username": spec.Username, "email": spec.Email, "source_id": spec.SourceID, "login_name": spec.Subject, "full_name": spec.Name, "must_change_password": false}, &created); err != nil {
		var refused *apiError
		if errors.As(err, &refused) && refused.status >= 400 && refused.status < 500 && refused.status != http.StatusRequestTimeout {
			return Binding{}, err
		}
		return Binding{}, ErrCreateUncertain
	}
	b.ID = created.ID
	account, err := c.inspect(ctx, b, true)
	if err != nil {
		return Binding{}, ErrCreateUncertain
	}
	if account.Admin || !account.Active || account.LoginProhibited || account.Name != spec.Name || account.Email != spec.Email {
		return Binding{}, ErrCreateUncertain
	}
	return b, nil
}

func (c *Client) Inspect(ctx context.Context, b Binding) (Account, error) {
	return c.inspect(ctx, b, false)
}

func (c *Client) inspect(ctx context.Context, b Binding, profile bool) (Account, error) {
	if err := validate(b); err != nil {
		return Account{}, err
	}
	var wire struct {
		ID         int64   `json:"id"`
		Login      string  `json:"login"`
		SourceID   int64   `json:"source_id"`
		Subject    string  `json:"login_name"`
		Admin      *bool   `json:"is_admin"`
		Active     *bool   `json:"active"`
		Prohibited *bool   `json:"prohibit_login"`
		Name       *string `json:"full_name"`
		Email      *string `json:"email"`
	}
	if err := c.call(ctx, http.MethodGet, "/users/"+url.PathEscape(b.Username), nil, &wire); err != nil {
		return Account{}, err
	}
	if wire.ID != b.ID || wire.Login != b.Username || wire.SourceID != b.SourceID || wire.Subject != b.Subject || wire.Admin == nil || wire.Active == nil || wire.Prohibited == nil {
		return Account{}, errors.New("Forgejo account authority conflicts with the pinned binding")
	}
	if profile && (wire.Name == nil || wire.Email == nil) {
		return Account{}, errors.New("Forgejo profile response is incomplete")
	}
	a := Account{Binding: b, Admin: *wire.Admin, Active: *wire.Active, LoginProhibited: *wire.Prohibited}
	if wire.Name != nil {
		a.Name = *wire.Name
	}
	if wire.Email != nil {
		a.Email = *wire.Email
	}
	return a, nil
}

// GrantAdmin verifies the active grant on the pinned native account. Initial OIDC
// setup and authenticated directory projection establish it; this method never
// promotes an account by matching a display name or email.
func (c *Client) GrantAdmin(ctx context.Context, b Binding) error {
	account, err := c.Inspect(ctx, b)
	if err != nil {
		return err
	}
	if !account.Admin || !account.Active || account.LoginProhibited {
		return errors.New("Forgejo administrator grant is not active; verify native OIDC role assignment")
	}
	return nil
}

// Deprovision disables sign-in before removing native personal access tokens.
// Repositories, issues, keys and role assignments are retained. An interrupted run
// resumes by inspecting current state; it never reactivates the account.
func (c *Client) Deprovision(ctx context.Context, b Binding) error {
	before, err := c.Inspect(ctx, b)
	if err != nil {
		return err
	}
	path := "/admin/users/" + url.PathEscape(b.Username)
	if before.Active || !before.LoginProhibited {
		if err = c.call(ctx, http.MethodPatch, path, map[string]bool{"active": false, "prohibit_login": true}, nil); err != nil {
			return err
		}
	}
	account, err := c.Inspect(ctx, b)
	if err != nil {
		return err
	}
	if account.Active || !account.LoginProhibited || account.Admin != before.Admin {
		return errors.New("Forgejo did not preserve authority and disable access")
	}
	// Always take the first page: deleting entries shifts later pages forward.
	for batch := 0; batch < 256; batch++ {
		var tokens []struct {
			ID int64 `json:"id"`
		}
		if err = c.call(ctx, http.MethodGet, path+"/tokens?limit=100&page=1", nil, &tokens); err != nil {
			return err
		}
		if tokens == nil || len(tokens) > 100 {
			return errors.New("invalid Forgejo token listing")
		}
		if len(tokens) == 0 {
			account, err = c.Inspect(ctx, b)
			if err != nil {
				return err
			}
			if account.Active || !account.LoginProhibited || account.Admin != before.Admin {
				return errors.New("Forgejo account authority changed during offboarding")
			}
			return nil
		}
		seen := map[int64]bool{}
		for _, token := range tokens {
			if token.ID <= 0 || seen[token.ID] {
				return errors.New("invalid Forgejo token identity")
			}
			seen[token.ID] = true
		}
		for _, token := range tokens {
			if err = c.call(ctx, http.MethodDelete, path+"/tokens/"+strconv.FormatInt(token.ID, 10), nil, nil); err != nil {
				return err
			}
		}
	}
	return errors.New("Forgejo token revocation exceeded its bounded run; inspect and resume")
}
