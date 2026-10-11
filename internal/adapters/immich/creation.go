package immich

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var ErrAccountConflict = errors.New("Immich account creation conflicts with retained ownership")
var intentDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

const creationContract = "ky.immich-creation/1"

type creationProof struct {
	Contract, SetupID, Issuer, ClientID, Subject, UserID, IntentDigest string
	Absent                                                             *bool
	Profile                                                            *struct{ Name, Email string }
}

func validCreationScope(b Binding, setupID string) bool {
	return b.ID == "" && installationUUID.MatchString(setupID) && https(b.Issuer, false) &&
		visible(b.ClientID, 255) && visible(b.Subject, 255) &&
		strings.IndexFunc(b.ClientID+b.Subject, func(r rune) bool { return r < 33 || r > 126 }) < 0
}

func matchesCreation(proof creationProof, b Binding, setupID string) bool {
	return proof.Contract == creationContract && proof.SetupID == setupID && proof.Issuer == b.Issuer &&
		proof.ClientID == b.ClientID && proof.Subject == b.Subject && proof.Absent != nil
}

// SubjectAbsent requires the product's installation/provider-scoped proof. A
// matching account or retained receipt is a conflict, never an adoption candidate.
func (c *Client) SubjectAbsent(ctx context.Context, b Binding, setupID string) error {
	if !validCreationScope(b, setupID) {
		return errors.New("invalid Immich creation scope")
	}
	if err := c.qualifiedVersion(ctx); err != nil {
		return err
	}
	query := url.Values{"setupId": {setupID}, "issuer": {b.Issuer}, "clientId": {b.ClientID}, "subject": {b.Subject}}
	var proof creationProof
	if c.call(ctx, http.MethodGet, "/admin/users/kyquickstart/creation?"+query.Encode(), nil, &proof) != nil || !matchesCreation(proof, b, setupID) {
		return errors.New("Immich native absence proof is unavailable")
	}
	if !*proof.Absent {
		return ErrAccountConflict
	}
	if proof.UserID != "" || proof.IntentDigest != "" || proof.Profile != nil {
		return errors.New("Immich native absence proof conflicts")
	}
	return nil
}

// CreateDirectoryAccount creates only ordinary authority. The caller persists
// intent before this call and publishes the verified UUID before projecting roles.
// Unknown writes/readback require reconciliation, never another creation request.
func (c *Client) CreateDirectoryAccount(ctx context.Context, b Binding, setupID string, profile Profile) (Binding, error) {
	if !validCreationScope(b, setupID) || !validProfile(profile) || profile.Administrator {
		return Binding{}, errors.New("invalid Immich ordinary account creation")
	}
	if err := c.SubjectAbsent(ctx, b, setupID); err != nil {
		return Binding{}, err
	}
	body := struct {
		SetupID  string `json:"setupId"`
		Issuer   string `json:"issuer"`
		ClientID string `json:"clientId"`
		Subject  string `json:"subject"`
		Profile  struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"profile"`
	}{SetupID: setupID, Issuer: b.Issuer, ClientID: b.ClientID, Subject: b.Subject}
	body.Profile.Name, body.Profile.Email = profile.Name, profile.Email
	var proof creationProof
	if err := c.call(ctx, http.MethodPost, "/admin/users/kyquickstart/creation", body, &proof); err != nil {
		var refusal requestRefused
		if errors.As(err, &refusal) && (refusal.status == http.StatusBadRequest || refusal.status == http.StatusForbidden || refusal.status == http.StatusConflict) {
			return Binding{}, ErrAccountConflict
		}
		return Binding{}, ErrUncertain
	}
	if !matchesCreation(proof, b, setupID) || *proof.Absent || !uuid.MatchString(proof.UserID) ||
		!intentDigest.MatchString(proof.IntentDigest) || proof.Profile == nil ||
		proof.Profile.Name != profile.Name || proof.Profile.Email != profile.Email {
		return Binding{}, ErrUncertain
	}
	b.ID = proof.UserID
	state, err := c.InspectDirectory(ctx, b, setupID)
	if err != nil || state.Revoked || state.Revision != 0 || state.Profile != profile {
		return Binding{}, ErrUncertain
	}
	if _, err := c.InspectAccess(ctx, b, setupID); err != nil {
		return Binding{}, ErrUncertain
	}
	return b, nil
}
