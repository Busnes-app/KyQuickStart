package immich

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"time"
)

const accessContract = "ky.immich-access/1"

type AccessState struct {
	Revoked           bool
	APIKeys, Sessions int64
	RevokedAt         string
}

// InspectAccess requires both native ownership and the community module's exact
// installation-bound fence. A stock server or incomplete proof is refused.
func (c *Client) InspectAccess(ctx context.Context, b Binding, setupID string) (AccessState, error) {
	if _, err := c.Inspect(ctx, b); err != nil {
		return AccessState{}, err
	}
	return c.accessProof(ctx, b, setupID)
}

// accessProof reads the module fence; callers check the native account first.
func (c *Client) accessProof(ctx context.Context, b Binding, setupID string) (AccessState, error) {
	if !installationUUID.MatchString(setupID) {
		return AccessState{}, errors.New("Immich requires an installation receipt")
	}
	var state struct {
		Contract, UserID, Issuer, ClientID, Subject string
		SetupID, RevokedAt                          json.RawMessage
		Revoked                                     *bool
		APIKeys, Sessions                           *int64
	}
	if c.call(ctx, http.MethodGet, "/admin/users/"+b.ID+"/kyquickstart/access", nil, &state) != nil ||
		state.Contract != accessContract || state.UserID != b.ID || state.Issuer != b.Issuer ||
		state.ClientID != b.ClientID || state.Subject != b.Subject || state.Revoked == nil ||
		state.APIKeys == nil || *state.APIKeys < 0 || state.Sessions == nil || *state.Sessions < 0 {
		return AccessState{}, errors.New("Immich access proof conflicts or is unavailable")
	}
	result := AccessState{Revoked: *state.Revoked, APIKeys: *state.APIKeys, Sessions: *state.Sessions}
	if !result.Revoked {
		if string(state.SetupID) != "null" || string(state.RevokedAt) != "null" {
			return AccessState{}, errors.New("Immich access proof conflicts")
		}
		return result, nil
	}
	var receipt string
	if json.Unmarshal(state.SetupID, &receipt) != nil || receipt != setupID ||
		json.Unmarshal(state.RevokedAt, &result.RevokedAt) != nil {
		return AccessState{}, errors.New("Immich access ownership conflicts")
	}
	if _, err := time.Parse(time.RFC3339Nano, result.RevokedAt); err != nil {
		return AccessState{}, errors.New("Immich revocation time is unavailable")
	}
	return result, nil
}

// RevokeAccess is permanent and retains content. Callers persist intent before
// mutation and reconcile uncertain writes; repeating removes late credentials
// and disconnects subscribed sockets without changing the retained fence.
// It requires only the exact account binding, not live provider configuration or
// an active account: offboarding must not depend on the state it is cleaning up.
func (c *Client) RevokeAccess(ctx context.Context, b Binding, setupID string) error {
	before, err := c.boundAccount(ctx, b)
	if err != nil {
		return err
	}
	if _, err := c.accessProof(ctx, b, setupID); err != nil {
		return err
	}
	body := struct {
		SetupID  string `json:"setupId"`
		Issuer   string `json:"issuer"`
		ClientID string `json:"clientId"`
		Subject  string `json:"subject"`
	}{setupID, b.Issuer, b.ClientID, b.Subject}
	var reply json.RawMessage
	if c.call(ctx, http.MethodPost, "/admin/users/"+b.ID+"/kyquickstart/revoke", body, &reply) != nil {
		return ErrUncertain
	}
	state, err := c.accessProof(ctx, b, setupID)
	if err != nil || !state.Revoked || state.APIKeys != 0 || state.Sessions != 0 {
		return ErrUncertain
	}
	after, err := c.boundAccount(ctx, b)
	if err != nil || !reflect.DeepEqual(after, before) {
		return ErrUncertain
	}
	return nil
}
