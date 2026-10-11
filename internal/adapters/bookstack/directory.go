package bookstack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
)

const directoryContract = "ky.bookstack-directory/1"

type DirectoryState struct {
	Profile ProfileState
	Roles   RoleState
}

func (c *Client) directory(ctx context.Context, scope RoleScope, desired Profile, administrator bool, action string, profileRevision, roleRevision int64) (DirectoryState, error) {
	return directoryCall(ctx, scope, desired, administrator, action, profileRevision, roleRevision,
		func(ctx context.Context, body []byte) ([]byte, error) {
			return c.execute(ctx, "kyquickstart:directory", body)
		})
}

func directoryCall(ctx context.Context, scope RoleScope, desired Profile, administrator bool, action string, profileRevision, roleRevision int64, send func(context.Context, []byte) ([]byte, error)) (DirectoryState, error) {
	if validateRoleScope(scope) != nil || validateSpec(profileSpec(scope.ProfileScope, desired)) != nil ||
		profileRevision < 0 || profileRevision > maxProfileRevision || roleRevision < 0 || roleRevision > maxProfileRevision {
		return DirectoryState{}, errors.New("invalid BookStack directory request")
	}
	profile := map[string]any{"contract": profileContract, "action": action, "setup_id": scope.SetupID,
		"user_id": scope.ID, "issuer": scope.Issuer, "client_id": scope.ClientID, "subject": scope.Subject,
		"name": desired.Name, "email": desired.Email, "expected_revision": profileRevision}
	roles := map[string]any{"contract": roleContract, "action": action, "setup_id": scope.SetupID,
		"user_id": scope.ID, "issuer": scope.Issuer, "client_id": scope.ClientID, "subject": scope.Subject,
		"everyday_role": scope.EverydayRole, "ordinary_role_ids": scope.OrdinaryRoleIDs,
		"administrator": administrator, "expected_revision": roleRevision}
	body, err := json.Marshal(map[string]any{"contract": directoryContract, "profile": profile, "roles": roles})
	if err != nil || len(body) > 4096 {
		return DirectoryState{}, errors.New("invalid BookStack directory request")
	}
	raw, err := send(ctx, body)
	if err != nil {
		return DirectoryState{}, err
	}
	return readDirectoryReport(raw, scope, desired, administrator, action, profileRevision, roleRevision)
}

func readDirectoryReport(raw []byte, scope RoleScope, desired Profile, administrator bool, action string, profileRevision, roleRevision int64) (DirectoryState, error) {
	var wire struct {
		Contract string          `json:"contract"`
		Version  string          `json:"module_version"`
		Profile  json.RawMessage `json:"profile"`
		Roles    json.RawMessage `json:"roles"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || wire.Contract != directoryContract || wire.Version != moduleVersion {
		return DirectoryState{}, errors.New("invalid BookStack directory report")
	}
	p, err := readProfileReport(wire.Profile, scope.ProfileScope, desired, action, profileRevision)
	if err != nil {
		return DirectoryState{}, err
	}
	r, err := readRoleReport(wire.Roles, scope, administrator, action, roleRevision)
	if err != nil || p.Disabled != r.Disabled {
		return DirectoryState{}, errors.New("invalid BookStack directory authority report")
	}
	return DirectoryState{p, r}, nil
}

// InitializeDirectory pins the exact current profile and native authority together.
func (c *Client) InitializeDirectory(ctx context.Context, scope RoleScope, current Profile, administrator bool) (DirectoryState, error) {
	return c.directory(ctx, scope, current, administrator, "initialize", 0, 0)
}

// InspectDirectory observes both receipts in one native transaction.
func (c *Client) InspectDirectory(ctx context.Context, scope RoleScope) (DirectoryState, error) {
	return c.directory(ctx, scope, Profile{"inspection", "inspection@example.test"}, false, "check", 0, 0)
}

// ApplyDirectory updates profile and authority atomically. Lost responses resume
// by inspecting both native receipts before another mutation is attempted.
func (c *Client) ApplyDirectory(ctx context.Context, scope RoleScope, desired Profile, administrator bool) (DirectoryState, error) {
	return applyDirectory(ctx, scope, desired, administrator, c.directory)
}

func applyDirectory(ctx context.Context, scope RoleScope, desired Profile, administrator bool,
	call func(context.Context, RoleScope, Profile, bool, string, int64, int64) (DirectoryState, error)) (DirectoryState, error) {
	if validateRoleScope(scope) != nil || validateSpec(profileSpec(scope.ProfileScope, desired)) != nil {
		return DirectoryState{}, errors.New("invalid BookStack directory intent")
	}
	before, err := call(ctx, scope, Profile{"inspection", "inspection@example.test"}, false, "check", 0, 0)
	if err != nil {
		return DirectoryState{}, err
	}
	if before.Profile.Disabled {
		return DirectoryState{}, errors.New("BookStack directory identity is permanently revoked")
	}
	if before.Profile.Profile == desired && before.Roles.Administrator == administrator {
		return before, nil
	}
	after, err := call(ctx, scope, desired, administrator, "apply", before.Profile.Revision, before.Roles.Revision)
	if err != nil {
		return DirectoryState{}, err
	}
	profileRevision, roleRevision := before.Profile.Revision, before.Roles.Revision
	if before.Profile.Profile != desired {
		profileRevision++
	}
	if before.Roles.Administrator != administrator {
		roleRevision++
	}
	if after.Profile.Revision != profileRevision || after.Roles.Revision != roleRevision ||
		after.Roles.PolicyDigest != before.Roles.PolicyDigest || after.Roles.AdministratorRoleID != before.Roles.AdministratorRoleID {
		return DirectoryState{}, errors.New("BookStack directory revisions/policy unverified")
	}
	checked, err := call(ctx, scope, Profile{"inspection", "inspection@example.test"}, false, "check", 0, 0)
	if err != nil || checked != after {
		return DirectoryState{}, errors.New("BookStack directory readback unverified; inspect before retrying")
	}
	return checked, nil
}
