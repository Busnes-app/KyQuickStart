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

const profileContract = "ky.bookstack-profile/1"
const maxProfileRevision = int64(9007199254740990)

// ProfileScope is an already provisioned account, never a profile-based adoption.
type ProfileScope struct {
	Binding
	SetupID, ClientID string
}

type Profile struct{ Name, Email string }
type ProfileState struct {
	Profile
	Revision int64
	Disabled bool
}

func profileSpec(scope ProfileScope, profile Profile) AccountSpec {
	return AccountSpec{SetupID: scope.SetupID, Issuer: scope.Issuer, ClientID: scope.ClientID,
		Subject: scope.Subject, Name: profile.Name, Email: profile.Email}
}

func profileBindingDigest(scope ProfileScope) string {
	sum := sha256.Sum256([]byte(scope.Issuer + "\x00" + scope.ClientID + "\x00" + scope.Subject))
	return hex.EncodeToString(sum[:])
}

func (c *Client) profile(ctx context.Context, scope ProfileScope, desired Profile, action string, revision int64) (ProfileState, error) {
	if validate(scope.Binding) != nil || validateSpec(profileSpec(scope, desired)) != nil || revision < 0 || revision > maxProfileRevision {
		return ProfileState{}, errors.New("invalid BookStack owned profile request")
	}
	body, err := json.Marshal(struct {
		Contract string `json:"contract"`
		Action   string `json:"action"`
		SetupID  string `json:"setup_id"`
		ID       int64  `json:"user_id"`
		Issuer   string `json:"issuer"`
		ClientID string `json:"client_id"`
		Subject  string `json:"subject"`
		Name     string `json:"name"`
		Email    string `json:"email"`
		Revision int64  `json:"expected_revision"`
	}{profileContract, action, scope.SetupID, scope.ID, scope.Issuer, scope.ClientID, scope.Subject, desired.Name, desired.Email, revision})
	if err != nil || len(body) > 4096 {
		return ProfileState{}, errors.New("invalid BookStack profile request")
	}
	raw, err := c.execute(ctx, "kyquickstart:profile", body)
	if err != nil {
		return ProfileState{}, err
	}
	return readProfileReport(raw, scope, desired, action, revision)
}

func readProfileReport(raw []byte, scope ProfileScope, desired Profile, action string, revision int64) (ProfileState, error) {
	var wire struct {
		Contract string  `json:"contract"`
		Version  string  `json:"module_version"`
		SetupID  string  `json:"setup_id"`
		ID       int64   `json:"user_id"`
		Digest   string  `json:"binding_digest"`
		Name     *string `json:"name"`
		Email    *string `json:"email"`
		Revision *int64  `json:"revision"`
		Disabled *bool   `json:"disabled"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || wire.Contract != profileContract || wire.Version != moduleVersion ||
		wire.SetupID != scope.SetupID || wire.ID != scope.ID || wire.Digest != profileBindingDigest(scope) || wire.Name == nil || wire.Email == nil || wire.Revision == nil || wire.Disabled == nil ||
		*wire.Revision < 0 || *wire.Revision > maxProfileRevision+1 {
		return ProfileState{}, errors.New("invalid BookStack profile ownership report")
	}
	current := Profile{*wire.Name, *wire.Email}
	if validateSpec(profileSpec(scope, current)) != nil || (action != "check" && (*wire.Disabled || current != desired ||
		(action == "initialize" && *wire.Revision != revision) || (action == "apply" && *wire.Revision != revision && *wire.Revision != revision+1))) {
		return ProfileState{}, errors.New("BookStack profile outcome unverified; inspect before retrying")
	}
	return ProfileState{Profile: current, Revision: *wire.Revision, Disabled: *wire.Disabled}, nil
}

// InitializeProfile explicitly pins the exact current owned profile. It neither
// adopts foreign accounts nor changes a profile to make initialization succeed.
func (c *Client) InitializeProfile(ctx context.Context, scope ProfileScope, current Profile) (ProfileState, error) {
	return c.profile(ctx, scope, current, "initialize", 0)
}

// InspectProfile is read-only and also verifies the native revision audit proof.
func (c *Client) InspectProfile(ctx context.Context, scope ProfileScope) (ProfileState, error) {
	return c.profile(ctx, scope, Profile{"inspection", "inspection@example.test"}, "check", 0)
}

// ApplyProfile uses the last inspected revision. A lost outcome is inspected on
// retry; exact desired repeats retain the revision and never reset authority.
func (c *Client) ApplyProfile(ctx context.Context, scope ProfileScope, desired Profile) (ProfileState, error) {
	if validateSpec(profileSpec(scope, desired)) != nil {
		return ProfileState{}, errors.New("invalid BookStack profile")
	}
	before, err := c.InspectProfile(ctx, scope)
	if err != nil {
		return ProfileState{}, err
	}
	if before.Disabled {
		return ProfileState{}, errors.New("BookStack profile is permanently revoked")
	}
	if before.Profile == desired {
		return before, nil
	}
	after, err := c.profile(ctx, scope, desired, "apply", before.Revision)
	if err != nil {
		return ProfileState{}, err
	}
	if after.Revision != before.Revision+1 {
		return ProfileState{}, errors.New("BookStack profile revision unverified")
	}
	checked, err := c.InspectProfile(ctx, scope)
	if err != nil || checked != after {
		return ProfileState{}, errors.New("BookStack profile readback unverified; inspect before retrying")
	}
	return checked, nil
}
