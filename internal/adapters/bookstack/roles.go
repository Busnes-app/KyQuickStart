package bookstack

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
)

const roleContract = "ky.bookstack-roles/1"

var roleAlias = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type RoleScope struct {
	ProfileScope
	EverydayRole    string
	OrdinaryRoleIDs []int64
}

type RoleState struct {
	Revision                int64
	Administrator, Disabled bool
	AdministratorRoleID     int64
	PolicyDigest            string
}

func validateRoleScope(scope RoleScope) error {
	if validate(scope.Binding) != nil || validateSpec(profileSpec(scope.ProfileScope, Profile{"Role inspection", "inspection@example.test"})) != nil ||
		!roleAlias.MatchString(scope.EverydayRole) || scope.EverydayRole == "admin" || scope.OrdinaryRoleIDs == nil || len(scope.OrdinaryRoleIDs) > 32 {
		return errors.New("invalid BookStack owned role scope")
	}
	previous := int64(0)
	for _, id := range scope.OrdinaryRoleIDs {
		if id <= previous {
			return errors.New("invalid BookStack ordinary role IDs")
		}
		previous = id
	}
	return nil
}

func (c *Client) roles(ctx context.Context, scope RoleScope, administrator bool, action string, revision int64) (RoleState, error) {
	if validateRoleScope(scope) != nil || revision < 0 || revision > maxProfileRevision {
		return RoleState{}, errors.New("invalid BookStack role request")
	}
	body, err := json.Marshal(struct {
		Contract      string  `json:"contract"`
		Action        string  `json:"action"`
		SetupID       string  `json:"setup_id"`
		ID            int64   `json:"user_id"`
		Issuer        string  `json:"issuer"`
		ClientID      string  `json:"client_id"`
		Subject       string  `json:"subject"`
		EverydayRole  string  `json:"everyday_role"`
		OrdinaryIDs   []int64 `json:"ordinary_role_ids"`
		Administrator bool    `json:"administrator"`
		Revision      int64   `json:"expected_revision"`
	}{roleContract, action, scope.SetupID, scope.ID, scope.Issuer, scope.ClientID, scope.Subject, scope.EverydayRole, scope.OrdinaryRoleIDs, administrator, revision})
	if err != nil || len(body) > 4096 {
		return RoleState{}, errors.New("invalid BookStack role request")
	}
	raw, err := c.execute(ctx, "kyquickstart:roles", body)
	if err != nil {
		return RoleState{}, err
	}
	return readRoleReport(raw, scope, administrator, action, revision)
}

func readRoleReport(raw []byte, scope RoleScope, administrator bool, action string, revision int64) (RoleState, error) {
	var wire struct {
		Contract        string  `json:"contract"`
		Version         string  `json:"module_version"`
		SetupID         string  `json:"setup_id"`
		ID              int64   `json:"user_id"`
		BindingDigest   string  `json:"binding_digest"`
		Revision        *int64  `json:"revision"`
		Administrator   *bool   `json:"administrator"`
		Disabled        *bool   `json:"disabled"`
		EverydayRole    string  `json:"everyday_role"`
		OrdinaryIDs     []int64 `json:"ordinary_role_ids"`
		RoleIDs         []int64 `json:"role_ids"`
		AdministratorID int64   `json:"administrator_role_id"`
		PolicyDigest    string  `json:"policy_digest"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || wire.Contract != roleContract || wire.Version != moduleVersion || wire.SetupID != scope.SetupID ||
		wire.OrdinaryIDs == nil || wire.RoleIDs == nil || wire.ID != scope.ID || wire.BindingDigest != profileBindingDigest(scope.ProfileScope) || wire.EverydayRole != scope.EverydayRole || !slices.Equal(wire.OrdinaryIDs, scope.OrdinaryRoleIDs) ||
		wire.AdministratorID < 1 || slices.Contains(scope.OrdinaryRoleIDs, wire.AdministratorID) || wire.Revision == nil || *wire.Revision < 0 || *wire.Revision > maxProfileRevision+1 ||
		wire.Administrator == nil || wire.Disabled == nil {
		return RoleState{}, errors.New("invalid BookStack role ownership report")
	}
	fingerprint, err := hex.DecodeString(wire.PolicyDigest)
	if err != nil || len(fingerprint) != 32 || hex.EncodeToString(fingerprint) != wire.PolicyDigest {
		return RoleState{}, errors.New("invalid BookStack native role policy proof")
	}
	expected := slices.Clone(scope.OrdinaryRoleIDs)
	if *wire.Administrator {
		expected = append(expected, wire.AdministratorID)
		slices.Sort(expected)
	}
	if !slices.Equal(expected, wire.RoleIDs) || (action != "check" && (*wire.Disabled || *wire.Administrator != administrator ||
		(action == "initialize" && *wire.Revision != revision) || (action == "apply" && *wire.Revision != revision && *wire.Revision != revision+1))) {
		return RoleState{}, errors.New("BookStack role outcome unverified; inspect before retrying")
	}
	return RoleState{*wire.Revision, *wire.Administrator, *wire.Disabled, wire.AdministratorID, wire.PolicyDigest}, nil
}

// InitializeRoles pins exact already-established native roles; it does not grant.
func (c *Client) InitializeRoles(ctx context.Context, scope RoleScope, administrator bool) (RoleState, error) {
	return c.roles(ctx, scope, administrator, "initialize", 0)
}
func (c *Client) InspectRoles(ctx context.Context, scope RoleScope) (RoleState, error) {
	return c.roles(ctx, scope, false, "check", 0)
}

// ApplyRoles projects only the native administrator role, preserving the pinned
// ordinary roles. A verified native audit and readback are required for success.
func (c *Client) ApplyRoles(ctx context.Context, scope RoleScope, administrator bool) (RoleState, error) {
	before, err := c.InspectRoles(ctx, scope)
	if err != nil {
		return RoleState{}, err
	}
	if before.Disabled {
		return RoleState{}, errors.New("BookStack roles are permanently revoked")
	}
	if before.Administrator == administrator {
		return before, nil
	}
	after, err := c.roles(ctx, scope, administrator, "apply", before.Revision)
	if err != nil {
		return RoleState{}, err
	}
	if after.Revision != before.Revision+1 || after.PolicyDigest != before.PolicyDigest || after.AdministratorRoleID != before.AdministratorRoleID {
		return RoleState{}, errors.New("BookStack role revision/policy unverified")
	}
	checked, err := c.InspectRoles(ctx, scope)
	if err != nil || checked != after {
		return RoleState{}, errors.New("BookStack role readback unverified; inspect before retrying")
	}
	return checked, nil
}
