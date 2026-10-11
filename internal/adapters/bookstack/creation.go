package bookstack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

const creationContract = "ky.bookstack-creation/1"

// Installation ownership and per-account setup UUIDs are separate pins.
type CreationSpec struct {
	InstallationID  string
	Account         AccountSpec
	EverydayRole    string
	OrdinaryRoleIDs []int64
}
type CreatedAccount struct {
	Account   ProvisionedAccount
	Directory DirectoryState
}

func creationScope(spec CreationSpec, id int64) RoleScope {
	return RoleScope{ProfileScope: ProfileScope{Binding: Binding{ID: id, Issuer: spec.Account.Issuer, Subject: spec.Account.Subject}, SetupID: spec.Account.SetupID, ClientID: spec.Account.ClientID}, EverydayRole: spec.EverydayRole, OrdinaryRoleIDs: spec.OrdinaryRoleIDs}
}
func validCreationSpec(spec CreationSpec) bool {
	return setupID.MatchString(spec.InstallationID) && !spec.Account.Administrator && validateSpec(spec.Account) == nil && len(spec.OrdinaryRoleIDs) > 0 && validateRoleScope(creationScope(spec, 1)) == nil
}
func creationIntent(spec CreationSpec) string {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode([]any{spec.InstallationID, intentDigest(spec.Account), spec.EverydayRole, spec.OrdinaryRoleIDs})
	sum := sha256.Sum256(bytes.TrimSuffix(out.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:])
}
func (c *Client) creation(ctx context.Context, spec CreationSpec, action string) (CreatedAccount, error) {
	if !validCreationSpec(spec) {
		return CreatedAccount{}, errors.New("invalid BookStack ordinary creation")
	}
	body, err := creationRequest(spec, action)
	if err != nil {
		return CreatedAccount{}, err
	}
	raw, err := c.execute(ctx, "kyquickstart:create", body)
	if err != nil {
		return CreatedAccount{}, err
	}
	return readCreationReport(raw, spec, action == "check")
}
func creationRequest(spec CreationSpec, action string) ([]byte, error) {
	account, err := accountRequest(spec.Account, action)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{"contract": creationContract, "action": action, "installation_id": spec.InstallationID, "account": json.RawMessage(account), "everyday_role": spec.EverydayRole, "ordinary_role_ids": spec.OrdinaryRoleIDs})
	if err != nil || len(body) > 4096 {
		return nil, errors.New("invalid BookStack creation request")
	}

	return body, nil
}

func readCreationReport(raw []byte, spec CreationSpec, check bool) (CreatedAccount, error) {
	var wire struct {
		Contract       string          `json:"contract"`
		Version        string          `json:"module_version"`
		InstallationID string          `json:"installation_id"`
		IntentDigest   string          `json:"intent_digest"`
		Account        json.RawMessage `json:"account"`
		Directory      json.RawMessage `json:"directory"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 16384 || decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || wire.Contract != creationContract || wire.Version != moduleVersion || wire.InstallationID != spec.InstallationID || wire.IntentDigest != creationIntent(spec) {
		return CreatedAccount{}, errors.New("invalid BookStack creation ownership report")
	}
	account, err := readAccountReport(wire.Account, spec.Account, check, true)
	if err != nil {
		return CreatedAccount{}, err
	}
	directory, err := readDirectoryReport(wire.Directory, creationScope(spec, account.ID), Profile{account.Name, account.Email}, false, "check", 0, 0)
	if err != nil || directory.Profile.Disabled != account.Disabled || directory.Roles.Administrator != account.Administrator || directory.Profile.Profile != (Profile{account.Name, account.Email}) || account.Created && (directory.Profile.Revision != 0 || directory.Roles.Revision != 0 || directory.Roles.Administrator || directory.Profile.Disabled) {
		return CreatedAccount{}, errors.New("BookStack creation outcome unverified; inspect before retrying")
	}
	return CreatedAccount{account, directory}, nil
}

// CreateManagedAccount never adopts prior setup accounts or adds management roles.
// A lost response is inspected with the identical immutable spec before retry.
func (c *Client) CreateManagedAccount(ctx context.Context, spec CreationSpec) (CreatedAccount, error) {
	if !validCreationSpec(spec) {
		return CreatedAccount{}, errors.New("invalid BookStack ordinary creation")
	}
	if err := c.probeAccountSource(ctx, spec.Account); err != nil {
		return CreatedAccount{}, err
	}
	return c.creation(ctx, spec, "create")
}
func (c *Client) CheckCreatedAccount(ctx context.Context, spec CreationSpec) (CreatedAccount, error) {
	return c.creation(ctx, spec, "check")
}
