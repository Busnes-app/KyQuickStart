// Package stockbookstack talks to a stock BookStack over its own HTTP interfaces only: the
// REST API with the installer's token, and (bootstrap) its standard sign-in and settings forms.
// Nothing here runs inside BookStack, and nothing needs a module, a patch or its database.
package stockbookstack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The KyIdentity role claims of the catalog BookStack's client.
const (
	AdminClaim = "bookstack.admin"
	UserClaim  = "bookstack.user"
)

// ServiceEmail is the installer's service account: BookStack's seeded admin, kept at its
// seeded address so that create-admin --initial re-keys it on every bootstrap.
const ServiceEmail = "admin@admin.com"

// tokenRE is BookStack's id:secret, 32 random characters each.
var tokenRE = regexp.MustCompile(`^[A-Za-z0-9]{32}:[A-Za-z0-9]{32}$`)

// ErrUnavailable is BookStack unreachable or answering 5xx: the caller retries later.
var ErrUnavailable = errors.New("BookStack is unavailable")

// API is BookStack's REST API with the token in TokenFile, read per call so a re-keyed token
// is picked up without a restart, or with Token, which bootstrap holds before writing it.
type API struct {
	URL       string // http://bookstack
	TokenFile string
	Token     string
	Client    *http.Client // nil: 15 s timeout, no redirects
}

func (a API) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// ReadToken reads an id:secret token file.
func ReadToken(file string) (string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", errors.New("token file unreadable")
	}
	t := strings.TrimSpace(string(b))
	if !tokenRE.MatchString(t) {
		return "", errors.New("token file is not a BookStack id:secret token")
	}
	return t, nil
}

// call sends one API request and decodes a 2xx JSON answer into out. A non-2xx status other
// than 5xx is returned as statusError.
func (a API) call(ctx context.Context, method, path string, in, out any) error {
	token := a.Token
	if token == "" {
		var err error
		if token, err = ReadToken(a.TokenFile); err != nil {
			return err
		}
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.URL+"/api/"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Token "+token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.client().Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch {
	case err != nil || res.StatusCode >= 500:
		return ErrUnavailable
	case res.StatusCode/100 != 2:
		return statusError(res.StatusCode)
	case out == nil:
		return nil
	}
	if json.Unmarshal(b, out) != nil {
		return errors.New("BookStack answered " + method + " /api/" + path + " with unreadable JSON")
	}
	return nil
}

type statusError int

func (s statusError) Error() string { return fmt.Sprintf("BookStack answered %d", int(s)) }

// Status is the HTTP status of a refused API call, 0 for any other error.
func Status(err error) int {
	var s statusError
	if errors.As(err, &s) {
		return int(s)
	}
	return 0
}

type User struct {
	ID             int    `json:"id"`
	Email          string `json:"email"`
	ExternalAuthID string `json:"external_auth_id"`
	Roles          []Role `json:"roles"`
}

type Role struct {
	ID             int    `json:"id"`
	DisplayName    string `json:"display_name"`
	SystemName     string `json:"system_name"`
	ExternalAuthID string `json:"external_auth_id"`
}

// Users lists accounts whose field equals value: BookStack compares case-insensitively.
func (a API) Users(ctx context.Context, field, value string) ([]User, error) {
	var page struct{ Data []User }
	q := url.Values{"filter[" + field + "]": {value}, "count": {"20"}}
	if err := a.call(ctx, "GET", "users?"+q.Encode(), nil, &page); err != nil {
		return nil, err
	}
	return page.Data, nil
}

func (a API) User(ctx context.Context, id int) (User, error) {
	var u User
	err := a.call(ctx, "GET", fmt.Sprint("users/", id), nil, &u)
	return u, err
}

// SetRoles sets the account's roles to exactly ids and reads them back.
func (a API) SetRoles(ctx context.Context, id int, ids []int) error {
	if ids == nil {
		ids = []int{}
	}
	if err := a.call(ctx, "PUT", fmt.Sprint("users/", id), map[string]any{"roles": ids}, nil); err != nil {
		return err
	}
	u, err := a.User(ctx, id)
	if err != nil {
		return err
	}
	if len(u.Roles) != len(ids) {
		return fmt.Errorf("BookStack user %d holds %d roles after the update, want %d", id, len(u.Roles), len(ids))
	}
	for _, r := range u.Roles {
		if !slices.Contains(ids, r.ID) {
			return fmt.Errorf("BookStack user %d holds role %d after the update", id, r.ID)
		}
	}
	return nil
}

// Roles reads every role in full: only a role's own read names its system_name.
func (a API) Roles(ctx context.Context) ([]Role, error) {
	var page struct{ Data []Role }
	if err := a.call(ctx, "GET", "roles?count=100", nil, &page); err != nil {
		return nil, err
	}
	out := make([]Role, 0, len(page.Data))
	for _, r := range page.Data {
		var full Role
		if err := a.call(ctx, "GET", fmt.Sprint("roles/", r.ID), nil, &full); err != nil {
			return nil, err
		}
		out = append(out, full)
	}
	return out, nil
}

// AdminRole is the ID of BookStack's system admin role.
func AdminRole(roles []Role) (int, error) {
	for _, r := range roles {
		if r.SystemName == "admin" {
			return r.ID, nil
		}
	}
	return 0, errors.New("BookStack has no system admin role")
}

// ServiceAccount is the installer's service account, which must hold the admin role.
func (a API) ServiceAccount(ctx context.Context, admin int) (User, error) {
	users, err := a.Users(ctx, "email", ServiceEmail)
	if err != nil {
		return User{}, err
	}
	var found []User
	for _, u := range users {
		if u.Email == ServiceEmail {
			found = append(found, u)
		}
	}
	if len(found) != 1 {
		return User{}, fmt.Errorf("BookStack has %d accounts %s, want the one service account", len(found), ServiceEmail)
	}
	u, err := a.User(ctx, found[0].ID)
	if err != nil {
		return User{}, err
	}
	if !slices.ContainsFunc(u.Roles, func(r Role) bool { return r.ID == admin }) {
		return User{}, errors.New("BookStack's service account does not hold the admin role")
	}
	return u, nil
}
