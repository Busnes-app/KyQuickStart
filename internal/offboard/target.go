package offboard

import (
	"context"
	"errors"
	"maps"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/adapters/bookstack"
	"github.com/Busnes-app/kyquickstart/internal/adapters/forgejo"
	"github.com/Busnes-app/kyquickstart/internal/adapters/immich"
)

// TargetSpec comes from protected deployment configuration. For Forgejo, SourceID
// must already have been verified against this issuer by native source setup.
// Directory role mapping must likewise match that source and its assigned-only client.
// Directory events supply neither target configuration nor native account IDs.
type TargetSpec struct {
	App, Kind, Origin, Issuer       string
	SetupID                         string
	SourceID                        int64
	ClientID                        string
	AdministratorRole, EverydayRole string
	CreateAccounts                  bool
	Groups                          *GroupConfig
	BookStackDirectory              *BookStackDirectoryConfig
}

type Target struct {
	spec          TargetSpec
	inspect       func(context.Context, Binding) error
	revoke        func(context.Context, Binding) error
	reactivate    func(context.Context, Binding, *DirectoryProfile) error // nil: unsupported; no kind has one yet
	directory     func(context.Context, Binding, DirectoryProfile) error
	directoryRead func(context.Context, Binding) (DirectoryProfile, error)
	create        func(context.Context, Binding, DirectoryProfile) (Binding, error)
	absent        func(context.Context, string) error
	groupCreate   func(context.Context, GroupBinding) (GroupBinding, error)
	groupInspect  func(context.Context, GroupBinding) error
	groupReplace  func(context.Context, GroupBinding, []Binding, []Binding) error
	groupAbsent   func(context.Context, GroupBinding) error
	groupLabel    func(context.Context, GroupBinding, string) error
	groupRead     func(context.Context, GroupBinding, []Binding) (string, []Binding, error)
}

// NewTarget retains the credential only inside the native client's memory.
func NewTarget(spec TargetSpec, token string) (*Target, error) {
	return newTarget(spec, token, "", "")
}

func newTarget(spec TargetSpec, token, directoryToken, creationToken string) (*Target, error) {
	if !appName.MatchString(spec.App) || !https(spec.Origin, true) || !https(spec.Issuer, false) || (spec.SetupID != "" && !setupID.MatchString(spec.SetupID)) {
		return nil, ErrState
	}
	if spec.CreateAccounts && ((spec.Kind != "forgejo" && spec.Kind != "immich" && spec.Kind != "bookstack") || spec.AdministratorRole == "" || !setupID.MatchString(spec.SetupID)) {
		return nil, ErrState
	}
	if spec.Kind != "immich" && spec.ClientID != "" {
		return nil, ErrState
	}
	if (spec.Kind != "bookstack" && spec.BookStackDirectory != nil) ||
		(spec.BookStackDirectory == nil && directoryToken != "") ||
		(spec.BookStackDirectory != nil && (directoryToken == "" || directoryToken == token)) {
		return nil, ErrState
	}
	if creationToken != "" && (spec.Kind != "bookstack" || spec.BookStackDirectory == nil || creationToken == token || creationToken == directoryToken) ||
		(spec.Kind == "bookstack" && spec.CreateAccounts && creationToken == "") {
		return nil, ErrState
	}
	if spec.Groups != nil {
		if spec.Kind != "forgejo" || spec.AdministratorRole == "" || !setupID.MatchString(spec.SetupID) || !validGroupConfig(spec.Groups) {
			return nil, ErrState
		}
		groups := *spec.Groups
		groups.Permissions = maps.Clone(groups.Permissions)
		spec.Groups = &groups
	}
	t := &Target{spec: spec}
	switch spec.Kind {
	case "immich":
		if !setupID.MatchString(spec.SetupID) || !validClientID(spec.ClientID) || spec.SourceID != 0 || !validDirectoryRoles(spec.AdministratorRole, spec.EverydayRole) {
			return nil, ErrState
		}
		c, err := immich.New(spec.Origin, token)
		if err != nil {
			return nil, ErrState
		}
		binding := func(b Binding) immich.Binding {
			return immich.Binding{ID: b.NativeUUID, Issuer: b.Issuer, ClientID: b.ClientID, Subject: b.Subject}
		}
		t.inspect = func(ctx context.Context, b Binding) error {
			_, err := c.InspectAccess(ctx, binding(b), b.SetupID)
			return err
		}
		t.revoke = func(ctx context.Context, b Binding) error { return c.RevokeAccess(ctx, binding(b), b.SetupID) }
		if spec.CreateAccounts {
			t.absent = func(ctx context.Context, subject string) error {
				err := c.SubjectAbsent(ctx, immich.Binding{Issuer: spec.Issuer, ClientID: spec.ClientID, Subject: subject}, spec.SetupID)
				if errors.Is(err, immich.ErrAccountConflict) {
					return ErrAccountConflict
				}
				return err
			}
			t.create = func(ctx context.Context, scope Binding, p DirectoryProfile) (Binding, error) {
				native, err := c.CreateDirectoryAccount(ctx, binding(scope), scope.SetupID, immich.Profile{Name: p.Name, Email: p.Email})
				if errors.Is(err, immich.ErrUncertain) {
					return Binding{}, ErrCreationUncertain
				}
				if errors.Is(err, immich.ErrAccountConflict) {
					return Binding{}, ErrAccountConflict
				}
				if err != nil {
					return Binding{}, err
				}
				scope.NativeUUID = native.ID
				return scope, nil
			}
		}
		if spec.AdministratorRole != "" {
			inspectAccess := t.inspect
			t.inspect = func(ctx context.Context, b Binding) error {
				if err := inspectAccess(ctx, b); err != nil {
					return err
				}
				_, err := c.InspectDirectory(ctx, binding(b), b.SetupID)
				return err
			}
			t.directoryRead = func(ctx context.Context, b Binding) (DirectoryProfile, error) {
				state, err := c.InspectDirectory(ctx, binding(b), b.SetupID)
				return DirectoryProfile{Name: state.Name, Email: state.Email, Administrator: state.Administrator}, err
			}
			t.directory = func(ctx context.Context, b Binding, p DirectoryProfile) error {
				err := c.ApplyDirectory(ctx, binding(b), b.SetupID, immich.Profile{Name: p.Name, Email: p.Email, Administrator: p.Administrator})
				if errors.Is(err, immich.ErrUncertain) {
					return ErrDirectoryUncertain
				}
				return err
			}
		}

	case "bookstack":
		if spec.SourceID != 0 || (spec.BookStackDirectory == nil && (spec.AdministratorRole != "" || spec.EverydayRole != "")) {
			return nil, ErrState
		}
		c, err := bookstack.NewHTTP(spec.Origin, token)
		if err != nil {
			return nil, ErrState
		}
		binding := func(b Binding) bookstack.Binding {
			return bookstack.Binding{ID: b.NativeID, Issuer: b.Issuer, Subject: b.Subject}
		}
		t.inspect = func(ctx context.Context, b Binding) error { _, err := c.Inspect(ctx, binding(b)); return err }
		t.revoke = func(ctx context.Context, b Binding) error { return c.Deprovision(ctx, binding(b)) }
		if spec.BookStackDirectory != nil {
			if err := t.enableBookStackDirectory(directoryToken, creationToken); err != nil {
				return nil, err
			}
		}
	case "forgejo":
		if spec.SourceID <= 0 || !validDirectoryRoles(spec.AdministratorRole, spec.EverydayRole) {
			return nil, ErrState
		}
		c, err := forgejo.New(spec.Origin, token)
		if err != nil {
			return nil, ErrState
		}
		binding := func(b Binding) forgejo.Binding {
			return forgejo.Binding{ID: b.NativeID, Username: b.Username, SourceID: b.SourceID, Subject: b.Subject}
		}
		t.inspect = func(ctx context.Context, b Binding) error { _, err := c.Inspect(ctx, binding(b)); return err }
		t.revoke = func(ctx context.Context, b Binding) error { return c.Deprovision(ctx, binding(b)) }
		// No native reactivation: Forgejo 16.0.5 cannot revoke web sessions, so
		// re-enabling sign-in would revive every pre-revocation session.
		team := func(b GroupBinding) forgejo.TeamBinding {
			return forgejo.TeamBinding{ID: b.TeamID, OrganizationID: b.OrganizationID, SourceID: b.SourceID, Name: b.TeamName, Organization: b.Organization, Permission: b.Permission, Permissions: b.Permissions}
		}
		t.groupInspect = func(ctx context.Context, b GroupBinding) error { return c.InspectTeam(ctx, team(b)) }
		t.groupAbsent = func(ctx context.Context, b GroupBinding) error {
			err := c.TeamAbsent(ctx, team(b))
			if errors.Is(err, forgejo.ErrTeamConflict) {
				return ErrGroupConflict
			}
			return err
		}
		t.groupLabel = func(ctx context.Context, b GroupBinding, label string) error {
			return c.UpdateTeamLabel(ctx, team(b), label)
		}
		t.groupRead = func(ctx context.Context, g GroupBinding, owned []Binding) (string, []Binding, error) {
			label, err := c.TeamLabel(ctx, team(g))
			if err != nil {
				return "", nil, err
			}
			native := make([]forgejo.Binding, 0, len(owned))
			byID := make(map[int64]Binding, len(owned))
			for _, b := range owned {
				native = append(native, binding(b))
				byID[b.NativeID] = b
			}
			members, err := c.InspectTeamMembers(ctx, team(g), native)
			if err != nil {
				return "", nil, err
			}
			result := make([]Binding, 0, len(members))
			for _, b := range members {
				result = append(result, byID[b.ID])
			}
			return label, result, nil
		}
		t.groupReplace = func(ctx context.Context, g GroupBinding, owned, desired []Binding) error {
			convert := func(accounts []Binding) []forgejo.Binding {
				result := make([]forgejo.Binding, 0, len(accounts))
				for _, account := range accounts {
					result = append(result, binding(account))
				}
				return result
			}
			err := c.ReplaceTeamMembers(ctx, team(g), convert(owned), convert(desired))
			if errors.Is(err, forgejo.ErrTeamUncertain) {
				return ErrGroupMembershipUncertain
			}
			return err
		}
		t.groupCreate = func(ctx context.Context, b GroupBinding) (GroupBinding, error) {
			native, err := c.CreateTeam(ctx, team(b))
			if errors.Is(err, forgejo.ErrTeamUncertain) {
				return GroupBinding{}, ErrGroupUncertain
			}
			if err != nil {
				return GroupBinding{}, err
			}
			b.TeamID = native.ID
			return cloneGroup(b), nil
		}
		if spec.CreateAccounts {
			t.absent = func(ctx context.Context, subject string) error {
				err := c.SubjectAbsent(ctx, spec.SourceID, subject)
				if errors.Is(err, forgejo.ErrAccountConflict) {
					return ErrAccountConflict
				}
				return err
			}
			t.create = func(ctx context.Context, scope Binding, p DirectoryProfile) (Binding, error) {
				native, err := c.CreateDirectoryAccount(ctx, forgejo.AccountSpec{Username: scope.Username, Email: p.Email, Name: p.Name, Subject: scope.Subject, SourceID: scope.SourceID})
				if errors.Is(err, forgejo.ErrCreateUncertain) {
					return Binding{}, ErrCreationUncertain
				}
				if err != nil {
					return Binding{}, err
				}
				scope.NativeID = native.ID
				return scope, nil
			}
		}
		if spec.AdministratorRole != "" {
			t.directoryRead = func(ctx context.Context, b Binding) (DirectoryProfile, error) {
				account, err := c.InspectDirectory(ctx, binding(b))
				return DirectoryProfile{Name: account.Name, Email: account.Email, Administrator: account.Admin}, err
			}
			t.directory = func(ctx context.Context, b Binding, p DirectoryProfile) error {
				err := c.ApplyDirectory(ctx, binding(b), forgejo.Profile{Name: p.Name, Email: p.Email}, p.Administrator)
				if err == forgejo.ErrDirectoryUncertain {
					return ErrDirectoryUncertain
				}
				return err
			}
		}
	default:
		return nil, ErrState
	}
	return t, nil
}

func (t *Target) matchesGroup(b GroupBinding) bool {
	if t == nil || t.spec.Kind != "forgejo" || !setupID.MatchString(t.spec.SetupID) || t.spec.SetupID != b.SetupID || t.spec.App != b.App || t.spec.Origin != b.Origin || t.spec.Issuer != b.Issuer || t.spec.SourceID != b.SourceID {
		return false
	}
	if t.spec.Groups != nil {
		scope := b
		scope.TeamID = 0
		if !sameGroup(scope, t.groupScope(b.ExternalID)) {
			return false
		}
	}
	return true
}

func (t *Target) matches(b Binding) bool {
	return t != nil && validate(b) == nil && t.spec.App == b.App && t.spec.Kind == b.Kind &&
		t.spec.Origin == b.Origin && t.spec.Issuer == b.Issuer && t.spec.SourceID == b.SourceID && t.spec.ClientID == b.ClientID && (t.spec.SetupID == "" || t.spec.SetupID == b.SetupID)
}

// Register proves the native binding before publishing bridge state. It neither
// creates an account nor grants authority, including when resuming a revoked user.
func (s *Store) Register(ctx context.Context, t *Target, b Binding) (string, error) {
	if !t.matches(b) || t.inspect(ctx, b) != nil {
		return "", ErrState
	}
	return s.Bind(b)
}

// Deliver uses the retained binding, not request data, and records success only
// after the native adapter has verified the latest epoch's revocation or reactivation.
func (s *Store) Deliver(ctx context.Context, t *Target, id string, now time.Time) error {
	return s.deliver(ctx, t, id, now, 0)
}

func (s *Store) deliver(ctx context.Context, t *Target, id string, now time.Time, epoch uint32) error {
	b, err := s.Binding(id)
	if err != nil || !t.matches(b) {
		return ErrState
	}
	reactivate := t.reactivate
	if reactivate == nil {
		reactivate = func(context.Context, Binding, *DirectoryProfile) error { return ErrReactivationUnsupported }
	}
	return s.run(ctx, id, now, epoch, t.revoke, reactivate)
}
