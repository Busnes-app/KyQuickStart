package forgejo

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Profile carries directory-owned display information, never login or authority.
// An empty Name explicitly clears the display name. Email must remain present.
type Profile struct {
	Name  string `json:"full_name"`
	Email string `json:"email"`
}

// ErrDirectoryUncertain means a native write may still be in flight or did not
// verify. A bridge must fence later directory writes; reads alone do not settle it.
var ErrDirectoryUncertain = errors.New("Forgejo directory write outcome is unresolved")

func (c *Client) InspectDirectory(ctx context.Context, b Binding) (Account, error) {
	return c.inspect(ctx, b, true)
}

// ApplyDirectory applies an authenticated, app-scoped directory role projection
// alongside the profile. It never activates an account. Disabled accounts cannot
// receive new administrator authority through directory synchronization.
func (c *Client) ApplyDirectory(ctx context.Context, b Binding, profile Profile, administrator bool) error {
	return c.updateProfile(ctx, b, profile, &administrator)
}

// UpdateProfile updates only a previously pinned native account. It verifies the
// resulting profile and preserves disabled state, administrator grants and the
// authentication source. Retrying an uncertain write inspects before mutating.
func (c *Client) UpdateProfile(ctx context.Context, b Binding, profile Profile) error {
	return c.updateProfile(ctx, b, profile, nil)
}

func (c *Client) updateProfile(ctx context.Context, b Binding, profile Profile, administrator *bool) error {
	if validate(b) != nil || !validProfile(profile) {
		return errors.New("invalid Forgejo directory profile")
	}
	before, err := c.inspect(ctx, b, true)
	if err != nil {
		return err
	}
	if administrator != nil && (!before.Active || before.LoginProhibited) {
		return errors.New("Forgejo directory updates refuse retained disabled accounts")
	}
	admin := before.Admin
	if administrator != nil {
		admin = *administrator
	}
	if before.Name == profile.Name && before.Email == profile.Email && before.Admin == admin {
		return nil
	}
	body := struct {
		Profile
		Admin *bool `json:"admin,omitempty"`
	}{profile, administrator}
	if err := c.call(ctx, http.MethodPatch, "/admin/users/"+url.PathEscape(b.Username), body, nil); err != nil {
		if administrator != nil {
			var rejected *apiError
			if !errors.As(err, &rejected) || rejected.status < 400 || rejected.status >= 500 || rejected.status == http.StatusRequestTimeout {
				return ErrDirectoryUncertain
			}
		}
		return err
	}
	after, err := c.inspect(ctx, b, true)
	if err != nil {
		if administrator != nil {
			return ErrDirectoryUncertain
		}
		return err
	}
	if after.Name != profile.Name || after.Email != profile.Email || after.Admin != admin ||
		after.Active != before.Active || after.LoginProhibited != before.LoginProhibited {
		if administrator != nil {
			return ErrDirectoryUncertain
		}
		return errors.New("Forgejo did not preserve authority and apply the directory profile")
	}
	return nil
}

func validProfile(profile Profile) bool {
	address, err := mail.ParseAddress(profile.Email)
	return utf8.ValidString(profile.Name) && len(profile.Name) <= 100 && strings.IndexFunc(profile.Name, unicode.IsControl) < 0 && err == nil && address.Name == "" && address.Address == profile.Email && len(profile.Email) <= 254
}
