package bookstack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/mail"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const accountContract = "ky.bookstack-account/1"
const accountReportContract = "ky.bookstack-account-report/2"

// AccountSpec pins one account's immutable provisioning intent to its own receipt
// key. SetupID is a stable UUID retained on the target; its receipt and the native
// user are committed together inside BookStack. No password is supplied or returned.
type AccountSpec struct {
	SetupID, Issuer, ClientID, Subject, Name, Email string
	Administrator                                   bool
}

type ProvisionedAccount struct {
	Binding
	SetupID, Name, Email                         string
	Created, Disabled, Administrator             bool
	OIDCVerified                                 bool
	ActivationPending, AdministratorGrantPending bool
}

var setupID = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

func validateSpec(spec AccountSpec) error {
	address, err := mail.ParseAddress(spec.Email)
	if validate(Binding{ID: 1, Issuer: spec.Issuer, Subject: spec.Subject}) != nil || !setupID.MatchString(spec.SetupID) ||
		spec.ClientID == "" || len(spec.ClientID) > 255 || !utf8.ValidString(spec.ClientID) || strings.IndexFunc(spec.ClientID, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 ||
		spec.Name == "" || len(spec.Name) > 100 || !utf8.ValidString(spec.Name) || strings.IndexFunc(spec.Name, unicode.IsControl) >= 0 ||
		err != nil || address.Name != "" || address.Address != spec.Email || len(spec.Email) > 254 {
		return errors.New("invalid BookStack account provisioning specification")
	}
	return nil
}

func intentDigest(spec AccountSpec) string {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode([]any{spec.SetupID, spec.Issuer, spec.ClientID, spec.Subject, spec.Name, spec.Email, spec.Administrator})
	sum := sha256.Sum256(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

func accountRequest(spec AccountSpec, action string) ([]byte, error) {
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	request, err := json.Marshal(struct {
		Contract      string `json:"contract"`
		Action        string `json:"action"`
		SetupID       string `json:"setup_id"`
		Issuer        string `json:"issuer"`
		ClientID      string `json:"client_id"`
		Subject       string `json:"subject"`
		Name          string `json:"name"`
		Email         string `json:"email"`
		Administrator bool   `json:"administrator"`
	}{accountContract, action, spec.SetupID, spec.Issuer, spec.ClientID, spec.Subject, spec.Name, spec.Email, spec.Administrator})
	if err != nil || len(request) > 4096 {
		return nil, errors.New("invalid BookStack account request")
	}
	return request, nil
}

func (c *Client) account(ctx context.Context, spec AccountSpec, check bool) (ProvisionedAccount, error) {
	action := "create"
	if check {
		action = "check"
	}
	request, err := accountRequest(spec, action)
	if err != nil {
		return ProvisionedAccount{}, err
	}
	data, err := c.execute(ctx, "kyquickstart:account", request)
	if err != nil {
		return ProvisionedAccount{}, err
	}
	return readAccountReport(data, spec, check, false)
}

func readAccountReport(data []byte, spec AccountSpec, check, projectedAuthority bool) (ProvisionedAccount, error) {
	var wire struct {
		Contract      string  `json:"contract"`
		Version       string  `json:"module_version"`
		SetupID       string  `json:"setup_id"`
		Intent        string  `json:"intent_digest"`
		ID            int64   `json:"user_id"`
		Name          *string `json:"name"`
		Email         *string `json:"email"`
		BindingDigest string  `json:"binding_digest"`
		Created       *bool   `json:"created"`
		Disabled      *bool   `json:"disabled"`
		Administrator *bool   `json:"administrator"`
		OIDCVerified  *bool   `json:"oidc_verified"`
		Activation    *bool   `json:"activation_pending"`
		AdminPending  *bool   `json:"admin_grant_pending"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	b := Binding{ID: 1, Issuer: spec.Issuer, Subject: spec.Subject}
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || wire.Contract != accountReportContract || wire.Version != moduleVersion ||
		wire.SetupID != spec.SetupID || wire.Intent != intentDigest(spec) || wire.ID <= 0 || wire.BindingDigest != digest(b) ||
		wire.Name == nil || wire.Email == nil || wire.Created == nil || wire.Disabled == nil || wire.Administrator == nil || wire.OIDCVerified == nil || wire.Activation == nil || wire.AdminPending == nil ||
		(check && *wire.Created) || (!spec.Administrator && *wire.Administrator && !projectedAuthority) ||
		(*wire.AdminPending != (spec.Administrator && (!*wire.Administrator || !*wire.OIDCVerified))) || (*wire.Created && (*wire.Administrator || *wire.Disabled || *wire.OIDCVerified)) {
		return ProvisionedAccount{}, errors.New("invalid BookStack provisioning authority report")
	}
	profile := spec
	profile.Name, profile.Email = *wire.Name, *wire.Email
	if validateSpec(profile) != nil || (*wire.Created && (profile.Name != spec.Name || profile.Email != spec.Email)) {
		return ProvisionedAccount{}, errors.New("invalid BookStack native profile report")
	}
	b.ID = wire.ID
	return ProvisionedAccount{Binding: b, SetupID: wire.SetupID, Name: *wire.Name, Email: *wire.Email, Created: *wire.Created, Disabled: *wire.Disabled,
		Administrator: *wire.Administrator, OIDCVerified: *wire.OIDCVerified, ActivationPending: *wire.Activation, AdministratorGrantPending: *wire.AdminPending}, nil
}

// Apply creates a non-administrator account or inspects the receipt-owned retained
// binding. It never adopts a collision, changes retained roles or reactivates users.
func (c *Client) probeAccountSource(ctx context.Context, spec AccountSpec) error {
	request, err := accountRequest(spec, "probe")
	if err != nil {
		return err
	}
	data, err := c.execute(ctx, "kyquickstart:account", request)
	if err != nil {
		return err
	}
	var source struct {
		Contract string `json:"contract"`
		Version  string `json:"module_version"`
		Digest   string `json:"source_digest"`
	}
	sum := sha256.Sum256([]byte(spec.Issuer + "\x00" + spec.ClientID))
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&source) != nil || decoder.Decode(new(any)) != io.EOF || source.Contract != "ky.bookstack-account-source/1" ||
		source.Version != moduleVersion || source.Digest != hex.EncodeToString(sum[:]) {
		return errors.New("BookStack account source or module contract is unverified")
	}
	return nil
}

func (c *Client) Apply(ctx context.Context, spec AccountSpec) (ProvisionedAccount, error) {
	if err := c.probeAccountSource(ctx, spec); err != nil {
		return ProvisionedAccount{}, err
	}
	return c.account(ctx, spec, false)
}

func (c *Client) CheckAccount(ctx context.Context, spec AccountSpec) (ProvisionedAccount, error) {
	return c.account(ctx, spec, true)
}

// VerifyAdmin checks the native OIDC grant; Apply never grants it directly.
func (c *Client) VerifyAdmin(ctx context.Context, spec AccountSpec) error {
	if !spec.Administrator {
		return errors.New("BookStack administrator verification requires dedicated administrator intent")
	}
	account, err := c.CheckAccount(ctx, spec)
	if err != nil {
		return err
	}
	if account.Disabled || !account.Administrator || !account.OIDCVerified || account.ActivationPending || account.AdministratorGrantPending {
		return errors.New("BookStack native OIDC administrator grant is not verified")
	}
	return nil
}
