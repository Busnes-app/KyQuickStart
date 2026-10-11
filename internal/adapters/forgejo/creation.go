package forgejo

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

var ErrCreateUncertain = errors.New("Forgejo account creation outcome is unresolved")
var ErrAccountConflict = errors.New("Forgejo account exists without an owned native receipt")

// SubjectAbsent checks the native exact source/login-name index. Never infer
// absence from a failed, truncated or unsupported collection response.
func (c *Client) SubjectAbsent(ctx context.Context, sourceID int64, subject string) error {
	if validate(Binding{ID: 1, SourceID: sourceID, Username: "probe", Subject: subject}) != nil {
		return ErrAccountConflict
	}
	query := url.Values{"source_id": {strconv.FormatInt(sourceID, 10)}, "login_name": {subject}, "limit": {"2"}, "page": {"1"}}
	var accounts []jsonAccount
	var headers http.Header
	if err := c.call(ctx, http.MethodGet, "/admin/users?"+query.Encode(), nil, &accounts, &headers); err != nil {
		return err
	}
	if len(headers.Values("X-Total-Count")) != 1 || headers.Get("X-Total-Count") != "0" || accounts == nil || len(accounts) != 0 {
		return ErrAccountConflict
	}
	return nil
}

type jsonAccount struct {
	ID int64 `json:"id"`
}

// CreateDirectoryAccount creates only a new ordinary account. The bridge retains
// this verified receipt before applying its separately audited app-role projection.
func (c *Client) CreateDirectoryAccount(ctx context.Context, spec AccountSpec) (Binding, error) {
	if spec.ExistingID != 0 || !validProfile(Profile{Name: spec.Name, Email: spec.Email}) || validate(Binding{ID: 1, SourceID: spec.SourceID, Subject: spec.Subject, Username: spec.Username}) != nil {
		return Binding{}, ErrAccountConflict
	}
	if err := c.SubjectAbsent(ctx, spec.SourceID, spec.Subject); err != nil {
		return Binding{}, err
	}
	return c.Apply(ctx, spec)
}
