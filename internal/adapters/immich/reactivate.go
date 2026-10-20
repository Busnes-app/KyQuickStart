package immich

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// ErrRefused is a definite native refusal: nothing changed, and retrying the same request cannot succeed.
var ErrRefused = errors.New("Immich refused the reactivation")

// reactivation reads the module's latest reactivation receipt; nil means never reactivated.
// A module without the field cannot reactivate.
func (c *Client) reactivation(ctx context.Context, b Binding) (*uint32, error) {
	var state struct {
		Contract, UserID string
		Reactivation     json.RawMessage
	}
	if c.call(ctx, http.MethodGet, "/admin/users/"+b.ID+"/kyquickstart/access", nil, &state) != nil || state.Contract != accessContract || state.UserID != b.ID {
		return nil, errors.New("Immich access proof conflicts or is unavailable")
	}
	if len(state.Reactivation) == 0 {
		return nil, ErrRefused
	}
	if string(state.Reactivation) == "null" {
		return nil, nil
	}
	var receipt struct {
		Epoch         *uint32
		ReactivatedAt string
	}
	if json.Unmarshal(state.Reactivation, &receipt) != nil || receipt.Epoch == nil {
		return nil, errors.New("Immich reactivation proof conflicts")
	}
	if _, err := time.Parse(time.RFC3339Nano, receipt.ReactivatedAt); err != nil {
		return nil, errors.New("Immich reactivation proof conflicts")
	}
	return receipt.Epoch, nil
}

// Reactivate lifts the revocation fence for bridge epoch epoch. The module sets the
// profile and administrator flag before re-enabling sign-in, in one transaction,
// and restores no session or key. Lost or unverified outcomes return ErrUncertain.
func (c *Client) Reactivate(ctx context.Context, b Binding, setupID string, epoch uint32, p Profile) error {
	if epoch < 2 || epoch%2 != 0 || !validProfile(p) {
		return errors.New("invalid Immich reactivation")
	}
	if _, err := c.Inspect(ctx, b); err != nil {
		return err
	}
	// The bridge holds the account's delivery lock, so these reads do not race a revocation.
	before, err := c.accessProof(ctx, b, setupID)
	if err != nil {
		return err
	}
	done, err := c.reactivation(ctx, b)
	if err != nil {
		return err
	}
	posted := before.Revoked
	if posted {
		body := struct {
			SetupID  string  `json:"setupId"`
			Issuer   string  `json:"issuer"`
			ClientID string  `json:"clientId"`
			Subject  string  `json:"subject"`
			Epoch    uint32  `json:"epoch"`
			Profile  Profile `json:"profile"`
		}{setupID, b.Issuer, b.ClientID, b.Subject, epoch, p}
		var reply json.RawMessage
		if err := c.call(ctx, http.MethodPost, "/admin/users/"+b.ID+"/kyquickstart/reactivate", body, &reply); err != nil {
			if refused, ok := err.(requestRefused); ok && refused.status == http.StatusConflict {
				return ErrRefused
			}
			return ErrUncertain
		}
	} else if done == nil || *done != epoch {
		return ErrRefused // Never revoked, or reactivated by another epoch.
	}
	after, err := c.accessProof(ctx, b, setupID)
	if err != nil || after.Revoked || (posted && (after.APIKeys != 0 || after.Sessions != 0)) {
		return ErrUncertain
	}
	if done, err = c.reactivation(ctx, b); err != nil || done == nil || *done != epoch {
		return ErrUncertain
	}
	directory, err := c.InspectDirectory(ctx, b, setupID)
	if err != nil || directory.Revoked {
		return ErrUncertain
	}
	if directory.Profile != p {
		if posted {
			return ErrUncertain
		}
		// This epoch committed earlier; a later PUT changed the desired profile.
		return c.ApplyDirectory(ctx, b, setupID, p)
	}
	return nil
}
