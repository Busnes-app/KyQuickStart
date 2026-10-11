package forgejo

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TeamBinding is a protected native receipt, not a name-based adoption request.
// Permissions pins each repository unit's authority; membership never edits it.
type TeamBinding struct {
	ID, OrganizationID, SourceID int64
	Name, Organization           string
	Permission                   string
	Permissions                  map[string]string
}

var ErrTeamUncertain = errors.New("Forgejo team mutation outcome is unknown; retain the intent and reconcile before another write")
var ErrTeamConflict = errors.New("Forgejo team already exists without an owned receipt")

// CreateTeam creates only a missing team in the exact retained organization.
// The caller must persist intent before this call and the returned receipt before
// using membership APIs. An existing name never authorizes adoption.
func (c *Client) CreateTeam(ctx context.Context, spec TeamBinding) (TeamBinding, error) {
	if err := c.TeamAbsent(ctx, spec); err != nil {
		return TeamBinding{}, err
	}
	units := make([]string, 0, len(spec.Permissions))
	for unit := range spec.Permissions {
		units = append(units, unit)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := c.call(ctx, http.MethodPost, "/orgs/"+url.PathEscape(spec.Organization)+"/teams", map[string]any{"name": spec.Name, "permission": spec.Permission, "units": units, "units_map": spec.Permissions, "includes_all_repositories": false, "can_create_org_repo": false}, &created); err != nil {
		var refused *apiError
		if errors.As(err, &refused) && refused.status >= 400 && refused.status < 500 && refused.status != http.StatusRequestTimeout {
			return TeamBinding{}, err
		}
		return TeamBinding{}, ErrTeamUncertain
	}
	b := spec
	b.ID = created.ID
	b.Permissions = maps.Clone(spec.Permissions)
	if err := c.InspectTeam(ctx, b); err != nil {
		return TeamBinding{}, ErrTeamUncertain
	}
	return b, nil
}

// TeamAbsent proves exact organization authority and complete native name absence.
func (c *Client) TeamAbsent(ctx context.Context, spec TeamBinding) error {
	b := spec
	b.ID = 1
	if spec.ID != 0 || !validTeam(b) {
		return errors.New("invalid Forgejo new-team specification")
	}
	var org struct {
		ID           int64  `json:"id"`
		Name         string `json:"name"`
		ChangeAccess *bool  `json:"repo_admin_change_team_access"`
	}
	if err := c.call(ctx, http.MethodGet, "/orgs/"+url.PathEscape(spec.Organization), nil, &org); err != nil {
		return err
	}
	if org.ID != spec.OrganizationID || org.Name != spec.Organization || org.ChangeAccess == nil || *org.ChangeAccess {
		return errors.New("Forgejo organization authority conflicts with its retained receipt")
	}
	var teams []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	var headers http.Header
	if err := c.call(ctx, http.MethodGet, "/orgs/"+url.PathEscape(spec.Organization)+"/teams?limit=100&page=1", nil, &teams, &headers); err != nil {
		return err
	}
	counts := headers.Values("X-Total-Count")
	if teams == nil || len(teams) > 100 || len(counts) != 1 || counts[0] != strconv.Itoa(len(teams)) {
		return errors.New("Forgejo team absence is not proven")
	}
	seen := make(map[int64]bool, len(teams))
	for _, team := range teams {
		if strings.EqualFold(team.Name, spec.Name) {
			return ErrTeamConflict
		}
		if team.ID <= 0 || team.Name == "" || seen[team.ID] {
			return errors.New("Forgejo team has conflicting identity")
		}
		seen[team.ID] = true
	}
	return nil
}

func validTeam(b TeamBinding) bool {
	if b.ID <= 0 || b.OrganizationID <= 0 || b.SourceID <= 0 || !username.MatchString(b.Name) || !username.MatchString(b.Organization) || (b.Permission != "read" && b.Permission != "write") || len(b.Permissions) == 0 || len(b.Permissions) > 10 {
		return false
	}
	for unit, permission := range b.Permissions {
		switch unit {
		case "repo.actions", "repo.code", "repo.issues", "repo.ext_issues", "repo.wiki", "repo.ext_wiki", "repo.pulls", "repo.releases", "repo.projects", "repo.packages":
		default:
			return false
		}
		if permission != "none" && permission != "read" && permission != "write" {
			return false
		}
	}
	return true
}

// InspectTeam refuses owner/admin teams, implicit all-repository grants, repository
// creation authority and any change to the retained organization, team or units.
func (c *Client) InspectTeam(ctx context.Context, b TeamBinding) error {
	_, err := c.inspectTeam(ctx, b, false)
	return err
}

func (c *Client) inspectTeam(ctx context.Context, b TeamBinding, label bool) (string, error) {
	if !validTeam(b) {
		return "", errors.New("Forgejo requires a pinned non-administrative team binding")
	}
	var wire struct {
		ID           int64   `json:"id"`
		Name         string  `json:"name"`
		Description  *string `json:"description"`
		Organization struct {
			ID           int64  `json:"id"`
			Name         string `json:"name"`
			ChangeAccess *bool  `json:"repo_admin_change_team_access"`
		} `json:"organization"`
		Permission  string            `json:"permission"`
		Permissions map[string]string `json:"units_map"`
		Units       []string          `json:"units"`
		All         *bool             `json:"includes_all_repositories"`
		Create      *bool             `json:"can_create_org_repo"`
	}
	if err := c.call(ctx, http.MethodGet, "/teams/"+strconv.FormatInt(b.ID, 10), nil, &wire); err != nil {
		return "", err
	}
	if wire.ID != b.ID || wire.Name != b.Name || wire.Organization.ID != b.OrganizationID || wire.Organization.Name != b.Organization || wire.Organization.ChangeAccess == nil || *wire.Organization.ChangeAccess || wire.Permission != b.Permission || wire.All == nil || *wire.All || wire.Create == nil || *wire.Create || !maps.Equal(wire.Permissions, b.Permissions) || len(wire.Units) != len(b.Permissions) {
		return "", errors.New("Forgejo team authority conflicts with its retained receipt")
	}
	seen := make(map[string]bool, len(wire.Units))
	for _, unit := range wire.Units {
		if _, ok := b.Permissions[unit]; !ok || seen[unit] {
			return "", errors.New("Forgejo team unit authority is incomplete")
		}
		seen[unit] = true
	}
	if label {
		if wire.Description == nil || !validTeamLabel(*wire.Description) {
			return "", errors.New("Forgejo team label is incomplete")
		}
		return *wire.Description, nil
	}
	return "", nil
}

func validTeamLabel(label string) bool {
	return len(label) <= 100 && utf8.ValidString(label) && strings.IndexFunc(label, unicode.IsControl) < 0
}

// TeamLabel is human-readable description metadata; native IDs/names stay pinned.
func (c *Client) TeamLabel(ctx context.Context, b TeamBinding) (string, error) {
	return c.inspectTeam(ctx, b, true)
}

func (c *Client) UpdateTeamLabel(ctx context.Context, b TeamBinding, label string) error {
	if !validTeamLabel(label) {
		return errors.New("invalid Forgejo team label")
	}
	current, err := c.TeamLabel(ctx, b)
	if err != nil || current == label {
		return err
	}
	if err := c.call(ctx, http.MethodPatch, "/teams/"+strconv.FormatInt(b.ID, 10), map[string]string{"description": label}, nil); err != nil {
		return ErrTeamUncertain
	}
	current, err = c.TeamLabel(ctx, b)
	if err != nil || current != label {
		return ErrTeamUncertain
	}
	return nil
}

func (c *Client) InspectTeamMembers(ctx context.Context, b TeamBinding, owned []Binding) ([]Binding, error) {
	if !validTeam(b) || len(owned) > 100 {
		return nil, errors.New("invalid Forgejo team member scope")
	}
	scope := make(map[int64]Binding, len(owned))
	logins := make(map[string]bool, len(owned))
	for _, account := range owned {
		if validate(account) != nil || account.SourceID != b.SourceID || scope[account.ID].ID != 0 || logins[account.Username] {
			return nil, errors.New("invalid Forgejo owned team account")
		}
		scope[account.ID], logins[account.Username] = account, true
	}
	if err := c.InspectTeam(ctx, b); err != nil {
		return nil, err
	}
	members, err := c.teamMembers(ctx, b, scope)
	if err != nil {
		return nil, err
	}
	result := []Binding{}
	for _, account := range owned {
		if members[account.ID] {
			result = append(result, account)
		}
	}
	return result, nil
}

func (c *Client) teamMembers(ctx context.Context, b TeamBinding, owned map[int64]Binding) (map[int64]bool, error) {
	var wire []struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	var headers http.Header
	if err := c.call(ctx, http.MethodGet, "/teams/"+strconv.FormatInt(b.ID, 10)+"/members?limit=100&page=1", nil, &wire, &headers); err != nil {
		return nil, err
	}
	counts := headers.Values("X-Total-Count")
	if wire == nil || len(wire) > 100 || len(counts) != 1 || counts[0] != strconv.Itoa(len(wire)) {
		return nil, errors.New("Forgejo team membership listing is incomplete")
	}
	members := make(map[int64]bool, len(wire))
	for _, member := range wire {
		account, ok := owned[member.ID]
		if !ok || account.Username != member.Login || members[member.ID] {
			return nil, errors.New("Forgejo team contains foreign or conflicting members")
		}
		if _, err := c.Inspect(ctx, account); err != nil {
			return nil, err
		}
		members[member.ID] = true
	}
	return members, nil
}

// ReplaceTeamMembers requires caller-owned serialization and durable intent before
// calling. Unknown outcomes must remain held by the caller, including across restart.
// All current members must belong to owned; foreign members are never removed.
func (c *Client) ReplaceTeamMembers(ctx context.Context, b TeamBinding, owned, desired []Binding) error {
	if !validTeam(b) || len(owned) > 100 || len(desired) > 100 {
		return errors.New("invalid Forgejo team membership scope")
	}
	scope := make(map[int64]Binding, len(owned))
	logins := make(map[string]bool, len(owned))
	for _, account := range owned {
		if validate(account) != nil || account.SourceID != b.SourceID || scope[account.ID].ID != 0 || logins[account.Username] {
			return errors.New("invalid Forgejo owned team account")
		}
		scope[account.ID], logins[account.Username] = account, true
	}
	want := make(map[int64]bool, len(desired))
	for _, account := range desired {
		if validate(account) != nil || scope[account.ID] != account || want[account.ID] {
			return errors.New("Forgejo desired member is not uniquely owned")
		}
		want[account.ID] = true
	}
	if err := c.InspectTeam(ctx, b); err != nil {
		return err
	}
	current, err := c.teamMembers(ctx, b, scope)
	if err != nil {
		return err
	}
	for _, account := range desired {
		state, err := c.Inspect(ctx, account)
		if err != nil {
			return err
		}
		if state.Admin || !state.Active || state.LoginProhibited {
			return errors.New("Forgejo team access requires active ordinary accounts")
		}
	}
	wrote := false
	// Remove before adding so a partial replacement never retains withdrawn access.
	for _, adding := range []bool{false, true} {
		for _, account := range owned {
			if current[account.ID] == want[account.ID] {
				continue
			}
			if want[account.ID] != adding {
				continue
			}
			method := http.MethodDelete
			if want[account.ID] {
				method = http.MethodPut
			}
			path := "/teams/" + strconv.FormatInt(b.ID, 10) + "/members/" + url.PathEscape(account.Username)
			if err := c.call(ctx, method, path, nil, nil); err != nil {
				var refused *apiError
				if !wrote && errors.As(err, &refused) && refused.status >= 400 && refused.status < 500 && refused.status != http.StatusRequestTimeout {
					return err
				}
				return ErrTeamUncertain
			}
			wrote = true
		}
	}
	if err := c.InspectTeam(ctx, b); err != nil {
		if wrote {
			return ErrTeamUncertain
		}
		return err
	}
	final, err := c.teamMembers(ctx, b, scope)
	if err != nil || !maps.Equal(final, want) {
		if wrote {
			return ErrTeamUncertain
		}
		return errors.New("Forgejo team membership verification failed")
	}
	for _, account := range desired {
		state, err := c.Inspect(ctx, account)
		if err != nil || state.Admin || !state.Active || state.LoginProhibited {
			if wrote {
				return ErrTeamUncertain
			}
			return errors.New("Forgejo desired team member authority changed")
		}
	}
	return nil
}
