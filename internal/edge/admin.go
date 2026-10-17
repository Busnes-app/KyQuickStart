package edge

import (
	"context"
	"errors"
	"net/http"
)

// NPM's own administrator, which bootstrap replaces with one from a target-held secret. NPM
// 2.13+ starts with no user ("setup mode"); older data may still hold this default login.
const (
	DefaultEmail    = "admin@example.com"
	DefaultPassword = "changeme"
)

// Admin manages NPM's administrator account. Credentials and tokens stay in memory and never
// reach an error.
type Admin struct {
	base string
	http *http.Client
}

// NewAdmin reaches NPM's API at origin, an https:// or kube:// npm_url.
func NewAdmin(origin string, tunnel Dial) (*Admin, error) {
	base, transport, err := npmTransport(origin, tunnel)
	if err != nil {
		return nil, err
	}
	return &Admin{base: base, http: npmClient(transport)}, nil
}

func (a *Admin) Close() { a.http.CloseIdleConnections() }

// rejected is an answer that says the credential is wrong, not that NPM failed. NPM answers a
// bad login or token with 400 (its AuthError) and a disallowed one with 403.
func rejected(status int) bool { return status == 400 || status == 401 || status == 403 }

// Setup reports whether NPM has a user; without one, anyone reaching the API can create the
// first administrator.
func (a *Admin) Setup(ctx context.Context) (bool, error) {
	var res struct {
		Status string `json:"status"`
		Setup  bool   `json:"setup"`
	}
	if _, err := call(ctx, a.http, a.base, "", http.MethodGet, "/api/", nil, &res); err != nil {
		return false, err
	}
	if res.Status != "OK" {
		return false, errors.New("NPM API is not ready")
	}
	return res.Setup, nil
}

// CreateAdmin creates the first administrator; NPM accepts this only while it has no user.
func (a *Admin) CreateAdmin(ctx context.Context, email, password string) error {
	body := map[string]any{
		"name": "KyQuickStart", "nickname": "kyquickstart", "email": email, "roles": []string{"admin"},
		"is_disabled": false, "auth": map[string]string{"type": "password", "secret": password},
	}
	_, err := call(ctx, a.http, a.base, "", http.MethodPost, "/api/users", body, nil)
	return err
}

// Login returns a token, or "" when NPM rejects the credentials.
func (a *Admin) Login(ctx context.Context, email, password string) (string, error) {
	var res struct {
		Token       string `json:"token"`
		Requires2FA bool   `json:"requires_2fa"`
	}
	status, err := call(ctx, a.http, a.base, "", http.MethodPost, "/api/tokens", map[string]string{"identity": email, "secret": password}, &res)
	switch {
	case rejected(status):
		return "", nil
	case err != nil:
		return "", err
	case res.Requires2FA:
		return "", errors.New("NPM administrator has two-factor sign-in, which the installer cannot use")
	case res.Token == "":
		return "", errors.New("NPM issued no token")
	}
	return res.Token, nil
}

// Verify reports whether NPM still accepts token. Any answer but 2xx is a no: NPM rejects an
// expired token with 400 but a malformed one, or one signed by another key, with 500. Only a
// failure to reach NPM is an error.
func (a *Admin) Verify(ctx context.Context, token string) (bool, error) {
	status, err := call(ctx, a.http, a.base, token, http.MethodGet, "/api/users/me", nil, nil)
	if status != 0 {
		return err == nil, nil
	}
	return false, err
}

// Rotate moves the signed-in account (token, current password) to email and password. NPM
// then rejects every token issued before the change.
func (a *Admin) Rotate(ctx context.Context, token, current, email, password string) error {
	if _, err := call(ctx, a.http, a.base, token, http.MethodPut, "/api/users/me", map[string]string{"email": email}, nil); err != nil {
		return err
	}
	auth := map[string]string{"type": "password", "current": current, "secret": password}
	_, err := call(ctx, a.http, a.base, token, http.MethodPut, "/api/users/me/auth", auth, nil)
	return err
}
