package bookstack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
)

var ErrCreationUncertain = errors.New("BookStack creation outcome requires reconciliation")
var ErrAccountConflict = errors.New("BookStack ordinary creation conflicts with retained ownership")

type CreationHTTPClient struct{ client *HTTPClient }

// CreationScope identifies a native creation lane without supplying a profile or native ID.
type CreationScope struct {
	InstallationID, AccountSetupID, Issuer, ClientID, Subject, EverydayRole string
	OrdinaryRoleIDs                                                         []int64
}

type creationScopeProof struct {
	SetupID         string  `json:"account_setup_id"`
	Contract        string  `json:"contract"`
	Version         string  `json:"module_version"`
	InstallationID  string  `json:"installation_id"`
	Source          string  `json:"source_digest"`
	Binding         string  `json:"binding_digest"`
	EverydayRole    string  `json:"everyday_role"`
	OrdinaryRoleIDs []int64 `json:"ordinary_role_ids"`
}

func (p creationScopeProof) matches(scope CreationScope) bool {
	source := sha256.Sum256([]byte(scope.Issuer + "\x00" + scope.ClientID))
	return p.Version == moduleVersion && p.InstallationID == scope.InstallationID && p.SetupID == scope.AccountSetupID && p.Source == hex.EncodeToString(source[:]) &&
		p.Binding == digest(Binding{Issuer: scope.Issuer, Subject: scope.Subject}) && p.EverydayRole == scope.EverydayRole && slices.Equal(p.OrdinaryRoleIDs, scope.OrdinaryRoleIDs)
}

func (c *CreationHTTPClient) scope(ctx context.Context, scope CreationScope, action string) ([]byte, error) {
	if !setupID.MatchString(scope.AccountSetupID) || len(scope.OrdinaryRoleIDs) == 0 || validateRoleScope(RoleScope{ProfileScope: ProfileScope{Binding: Binding{ID: 1, Issuer: scope.Issuer, Subject: scope.Subject}, SetupID: scope.InstallationID, ClientID: scope.ClientID}, EverydayRole: scope.EverydayRole, OrdinaryRoleIDs: scope.OrdinaryRoleIDs}) != nil {
		return nil, errors.New("invalid BookStack creation source scope")
	}
	body, err := json.Marshal(map[string]any{"contract": "ky.bookstack-creation-scope/1", "action": action, "installation_id": scope.InstallationID, "account_setup_id": scope.AccountSetupID,
		"issuer": scope.Issuer, "client_id": scope.ClientID, "subject": scope.Subject, "everyday_role": scope.EverydayRole, "ordinary_role_ids": scope.OrdinaryRoleIDs})
	if err != nil || len(body) > 4096 {
		return nil, errors.New("invalid BookStack scope request")
	}
	return c.client.send(ctx, "/api/kyquickstart/creation", body)
}

// InspectSubjectAbsence is only an existence check. Birth separately checks email and intent.
func (c *CreationHTTPClient) InspectSubjectAbsence(ctx context.Context, scope CreationScope) (bool, error) {
	raw, err := c.scope(ctx, scope, "subject-probe")
	if err != nil {
		return false, err
	}
	var wire struct {
		creationScopeProof
		Absent *bool `json:"absent"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || wire.Contract != "ky.bookstack-creation-subject-absence/1" || !wire.matches(scope) || wire.Absent == nil {
		return false, errors.New("invalid BookStack subject absence proof")
	}
	return *wire.Absent, nil
}

// ResolveCreatedScope requires native creation/account/assignment and current directory proofs.
// It never resolves a prior setup account without the immutable creation receipt.
func (c *CreationHTTPClient) ResolveCreatedScope(ctx context.Context, scope CreationScope) (RoleScope, error) {
	raw, err := c.scope(ctx, scope, "resolve")
	if err != nil {
		return RoleScope{}, err
	}
	var wire struct {
		creationScopeProof
		ID             int64  `json:"user_id"`
		AccountIntent  string `json:"account_intent_digest"`
		CreationIntent string `json:"creation_intent_digest"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || wire.Contract != "ky.bookstack-creation-scope/1" || !wire.matches(scope) || !setupID.MatchString(wire.SetupID) {
		return RoleScope{}, errors.New("invalid BookStack native scope proof")
	}
	accountDigest, err := hex.DecodeString(wire.AccountIntent)
	intentBytes, _ := json.Marshal([]any{scope.InstallationID, wire.AccountIntent, scope.EverydayRole, scope.OrdinaryRoleIDs})
	intent := sha256.Sum256(intentBytes)
	if err != nil || len(accountDigest) != 32 || hex.EncodeToString(accountDigest) != wire.AccountIntent || wire.CreationIntent != hex.EncodeToString(intent[:]) {
		return RoleScope{}, errors.New("invalid BookStack joined creation intent")
	}
	resolved := RoleScope{ProfileScope: ProfileScope{Binding: Binding{ID: wire.ID, Issuer: scope.Issuer, Subject: scope.Subject}, SetupID: wire.SetupID, ClientID: scope.ClientID}, EverydayRole: scope.EverydayRole, OrdinaryRoleIDs: slices.Clone(scope.OrdinaryRoleIDs)}
	if validateRoleScope(resolved) != nil {
		return RoleScope{}, errors.New("invalid BookStack resolved identity")
	}
	return resolved, nil
}

func NewCreationHTTP(origin, token string) (*CreationHTTPClient, error) {
	client, err := NewHTTP(origin, token)
	if err != nil {
		return nil, err
	}
	return &CreationHTTPClient{client}, nil
}
func (c *CreationHTTPClient) SubjectAbsent(ctx context.Context, spec CreationSpec) (bool, error) {
	if !validCreationSpec(spec) {
		return false, errors.New("invalid BookStack creation scope")
	}
	body, err := creationRequest(spec, "probe")
	if err != nil {
		return false, err
	}
	raw, err := c.client.send(ctx, "/api/kyquickstart/creation", body)
	if err != nil {
		return false, err
	}
	var wire struct {
		Contract        string  `json:"contract"`
		Version         string  `json:"module_version"`
		InstallationID  string  `json:"installation_id"`
		SetupID         string  `json:"account_setup_id"`
		Source          string  `json:"source_digest"`
		Binding         string  `json:"binding_digest"`
		EverydayRole    string  `json:"everyday_role"`
		OrdinaryRoleIDs []int64 `json:"ordinary_role_ids"`
		Absent          *bool   `json:"absent"`
	}
	sum := sha256.Sum256([]byte(spec.Account.Issuer + "\x00" + spec.Account.ClientID))
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || wire.Contract != "ky.bookstack-creation-absence/1" || wire.Version != moduleVersion || wire.InstallationID != spec.InstallationID || wire.SetupID != spec.Account.SetupID || wire.Source != hex.EncodeToString(sum[:]) || wire.Binding != digest(Binding{Issuer: spec.Account.Issuer, Subject: spec.Account.Subject}) || wire.EverydayRole != spec.EverydayRole || !slices.Equal(wire.OrdinaryRoleIDs, spec.OrdinaryRoleIDs) || wire.Absent == nil {
		return false, errors.New("invalid BookStack native absence proof")
	}
	return *wire.Absent, nil
}
func (c *CreationHTTPClient) creation(ctx context.Context, spec CreationSpec, action string) (CreatedAccount, error) {
	if !validCreationSpec(spec) {
		return CreatedAccount{}, errors.New("invalid BookStack ordinary creation")
	}
	body, err := creationRequest(spec, action)
	if err != nil {
		return CreatedAccount{}, err
	}
	raw, err := c.client.send(ctx, "/api/kyquickstart/creation", body)
	if err == nil {
		var state CreatedAccount
		state, err = readCreationReport(raw, spec, action == "check")
		if err == nil {
			return state, nil
		}
	}
	if action == "create" && !errors.Is(err, errHTTPRefused) {
		return CreatedAccount{}, errors.Join(ErrCreationUncertain, err)
	}
	return CreatedAccount{}, err
}
func (c *CreationHTTPClient) CheckCreatedAccount(ctx context.Context, spec CreationSpec) (CreatedAccount, error) {
	return c.creation(ctx, spec, "check")
}
func (c *CreationHTTPClient) CreateManagedAccount(ctx context.Context, spec CreationSpec) (CreatedAccount, error) {
	absent, err := c.SubjectAbsent(ctx, spec)
	if err != nil {
		return CreatedAccount{}, err
	}
	if !absent {
		state, err := c.CheckCreatedAccount(ctx, spec)
		if errors.Is(err, errHTTPRefused) {
			return CreatedAccount{}, ErrAccountConflict
		}
		return state, err
	}
	state, err := c.creation(ctx, spec, "create")
	if errors.Is(err, errHTTPRefused) {
		return CreatedAccount{}, ErrAccountConflict
	}
	return state, err
}
