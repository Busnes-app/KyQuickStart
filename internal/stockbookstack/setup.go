package stockbookstack

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Check proves the token works and belongs to the service account, which holds the admin role.
func Check(ctx context.Context, a API) error {
	roles, err := a.Roles(ctx)
	if err != nil {
		return err
	}
	admin, err := AdminRole(roles)
	if err != nil {
		return err
	}
	_, err = a.ServiceAccount(ctx, admin)
	return err
}

// claimNames are the role claims BookStack's sign-in matches r against: its external_auth_id
// list, or without one its display name, lowercased with spaces as dashes.
func claimNames(r Role) []string {
	if r.ExternalAuthID == "" {
		return []string{strings.ReplaceAll(strings.TrimSpace(strings.ToLower(r.DisplayName)), " ", "-")}
	}
	var out []string
	for _, id := range strings.Split(strings.ToLower(r.ExternalAuthID), ",") {
		out = append(out, strings.TrimSpace(id))
	}
	return out
}

// MapRoles maps the admin role, and only it, to the adminClaim sign-in claim, and refuses a
// role that adminClaim or userClaim would also grant. check only compares.
func MapRoles(ctx context.Context, a API, adminClaim, userClaim string, check bool) error {
	roles, err := a.Roles(ctx)
	if err != nil {
		return err
	}
	admin, err := AdminRole(roles)
	if err != nil {
		return err
	}
	claims := []string{strings.ToLower(adminClaim), strings.ToLower(userClaim)}
	for _, r := range roles {
		if r.ID == admin {
			if r.ExternalAuthID == adminClaim {
				continue
			}
			if check {
				return fmt.Errorf("BookStack's admin role is mapped to %q, not %s", r.ExternalAuthID, adminClaim)
			}
			if err := a.call(ctx, "PUT", fmt.Sprint("roles/", r.ID), map[string]string{"external_auth_id": adminClaim}, nil); err != nil {
				return err
			}
			continue
		}
		for _, n := range claimNames(r) {
			if slices.Contains(claims, n) {
				return fmt.Errorf("BookStack role %q would also be granted by the KyIdentity role %s; rename it or change its external authentication ID", r.DisplayName, n)
			}
		}
	}
	if check {
		return nil
	}
	return MapRoles(ctx, a, adminClaim, userClaim, true)
}
