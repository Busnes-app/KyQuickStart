package immich

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
)

const directoryContract = "ky.immich-directory/1"
const maxDirectoryRevision = int64(1<<53 - 1)

type Profile struct {
	Name          string `json:"name"`
	Email         string `json:"email"`
	Administrator bool   `json:"administrator"`
}
type DirectoryState struct {
	Profile
	Revision int64
	Revoked  bool
}

func validProfile(p Profile) bool {
	address, err := mail.ParseAddress(p.Email)
	return visible(p.Name, 255) && visible(p.Email, 254) && err == nil && address.Address == p.Email && p.Email == strings.ToLower(p.Email)
}

// InspectDirectory verifies a product-owned installation/account receipt and its
// current native profile. Missing metadata and unreceipted edits are conflicts.
func (c *Client) InspectDirectory(ctx context.Context, b Binding, setupID string) (DirectoryState, error) {
	if !installationUUID.MatchString(setupID) {
		return DirectoryState{}, errors.New("Immich directory requires an installation receipt")
	}
	account, err := c.Inspect(ctx, b)
	if err != nil {
		return DirectoryState{}, err
	}
	var proof struct {
		Contract, SetupID, UserID, Issuer, ClientID, Subject string
		Revision                                             *int64
		Revoked                                              *bool
		Profile                                              *struct {
			Name, Email   string
			Administrator *bool
		}
	}
	if c.call(ctx, http.MethodGet, "/admin/users/"+b.ID+"/kyquickstart/directory?setupId="+url.QueryEscape(setupID), nil, &proof) != nil ||
		proof.Contract != directoryContract || proof.SetupID != setupID || proof.UserID != b.ID || proof.Issuer != b.Issuer || proof.ClientID != b.ClientID || proof.Subject != b.Subject ||
		proof.Revision == nil || *proof.Revision < 0 || *proof.Revision > maxDirectoryRevision || proof.Revoked == nil || proof.Profile == nil || proof.Profile.Administrator == nil {
		return DirectoryState{}, errors.New("Immich directory ownership proof conflicts")
	}
	profile := Profile{proof.Profile.Name, proof.Profile.Email, *proof.Profile.Administrator}
	if !validProfile(profile) || profile.Name != account.Name || profile.Email != account.Email || profile.Administrator != account.Administrator {
		return DirectoryState{}, errors.New("Immich directory profile conflicts")
	}
	return DirectoryState{profile, *proof.Revision, *proof.Revoked}, nil
}

// ApplyDirectory commits only name/email/administrator plus the native revision
// receipt. The product compares revisions under the account lock and refuses
// revoked accounts. Callers persist intent and hold uncertain outcomes.
func (c *Client) ApplyDirectory(ctx context.Context, b Binding, setupID string, profile Profile) error {
	if !validProfile(profile) {
		return errors.New("invalid Immich directory profile")
	}
	before, err := c.InspectDirectory(ctx, b, setupID)
	if err != nil {
		return err
	}
	if before.Revoked {
		return errors.New("Immich directory access is permanently revoked")
	}
	if before.Profile == profile {
		return nil
	}
	if before.Revision == maxDirectoryRevision {
		return errors.New("Immich directory revision requires reconciliation")
	}
	body := struct {
		SetupID          string  `json:"setupId"`
		Issuer           string  `json:"issuer"`
		ClientID         string  `json:"clientId"`
		Subject          string  `json:"subject"`
		ExpectedRevision int64   `json:"expectedRevision"`
		Profile          Profile `json:"profile"`
	}{setupID, b.Issuer, b.ClientID, b.Subject, before.Revision, profile}
	var ignored json.RawMessage
	if c.call(ctx, http.MethodPost, "/admin/users/"+b.ID+"/kyquickstart/directory", body, &ignored) != nil {
		return ErrUncertain
	}
	after, err := c.InspectDirectory(ctx, b, setupID)
	if err != nil || after.Revoked || after.Profile != profile || after.Revision != before.Revision+1 {
		return ErrUncertain
	}
	return nil
}
