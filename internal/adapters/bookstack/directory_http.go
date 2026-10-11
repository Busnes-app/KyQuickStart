package bookstack

import (
	"context"
	"errors"
)

var ErrDirectoryUncertain = errors.New("BookStack directory outcome requires reconciliation")

// DirectoryHTTPClient has separately provisioned profile/role authority. It
// exposes neither initialization nor account creation, revocation or content APIs.
type DirectoryHTTPClient struct{ client *HTTPClient }

func NewDirectoryHTTP(origin, token string) (*DirectoryHTTPClient, error) {
	client, err := NewHTTP(origin, token)
	if err != nil {
		return nil, err
	}
	return &DirectoryHTTPClient{client: client}, nil
}

func (c *DirectoryHTTPClient) directory(ctx context.Context, scope RoleScope, desired Profile, administrator bool, action string, profileRevision, roleRevision int64) (DirectoryState, error) {
	state, err := directoryCall(ctx, scope, desired, administrator, action, profileRevision, roleRevision,
		func(ctx context.Context, body []byte) ([]byte, error) {
			return c.client.send(ctx, "/api/kyquickstart/directory", body)
		})
	if err != nil && action == "apply" && !errors.Is(err, errHTTPRefused) {
		return DirectoryState{}, errors.Join(ErrDirectoryUncertain, err)
	}
	return state, err
}

func (c *DirectoryHTTPClient) InspectDirectory(ctx context.Context, scope RoleScope) (DirectoryState, error) {
	return c.directory(ctx, scope, Profile{"inspection", "inspection@example.test"}, false, "check", 0, 0)
}

func (c *DirectoryHTTPClient) ApplyDirectory(ctx context.Context, scope RoleScope, desired Profile, administrator bool) (DirectoryState, error) {
	written := false
	state, err := applyDirectory(ctx, scope, desired, administrator,
		func(ctx context.Context, scope RoleScope, profile Profile, admin bool, action string, pr, rr int64) (DirectoryState, error) {
			if action == "apply" {
				written = true
			}
			return c.directory(ctx, scope, profile, admin, action, pr, rr)
		})
	if written && err != nil && !errors.Is(err, errHTTPRefused) {
		return DirectoryState{}, errors.Join(ErrDirectoryUncertain, err)
	}
	return state, err
}
