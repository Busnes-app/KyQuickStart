package offboard

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/Busnes-app/kyquickstart/internal/adapters/bookstack"
)

// Account setup UUIDs belong to BookStack provisioning, independently of the
// bridge installation UUID. This protected map never comes from SCIM input.
type BookStackAccount struct {
	ID      int64  `json:"native_id"`
	SetupID string `json:"account_setup_id"`
	// nil uses the app policy; an explicit empty array preserves no ordinary roles.
	OrdinaryRoleIDs []int64 `json:"ordinary_role_ids"`
}

type BookStackDirectoryConfig struct {
	ClientID        string                      `json:"client_id"`
	OrdinaryRoleIDs []int64                     `json:"ordinary_role_ids"`
	Accounts        map[string]BookStackAccount `json:"accounts"`
}

func validBookStackDirectory(p *BookStackDirectoryConfig) bool {
	if p == nil || !validClientID(p.ClientID) || len(p.OrdinaryRoleIDs) < 1 || len(p.OrdinaryRoleIDs) > 32 || len(p.Accounts) < 1 || len(p.Accounts) > 128 {
		return false
	}
	if !validBookStackRoleIDs(p.OrdinaryRoleIDs) {
		return false
	}
	ids, setups := map[int64]bool{}, map[string]bool{}
	for subject, account := range p.Accounts {
		if !validSubject(subject) || account.ID < 1 || !setupID.MatchString(account.SetupID) || ids[account.ID] || setups[account.SetupID] || (account.OrdinaryRoleIDs != nil && !validBookStackRoleIDs(account.OrdinaryRoleIDs)) {
			return false
		}
		ids[account.ID], setups[account.SetupID] = true, true
	}
	return true
}

func validBookStackRoleIDs(ids []int64) bool {
	if ids == nil || len(ids) > 32 {
		return false
	}
	previous := int64(0)
	for _, id := range ids {
		if id <= previous {
			return false
		}
		previous = id
	}
	return true
}

func (t *Target) enableBookStackDirectory(token, creationToken string) error {
	p := t.spec.BookStackDirectory
	if !validBookStackDirectory(p) || t.spec.AdministratorRole != "admin" || !validDirectoryRoles(t.spec.AdministratorRole, t.spec.EverydayRole) || t.spec.Groups != nil {
		return ErrState
	}
	c, err := bookstack.NewDirectoryHTTP(t.spec.Origin, token)
	if err != nil {
		return ErrState
	}
	var creation *bookstack.CreationHTTPClient
	if creationToken != "" {
		creation, err = bookstack.NewCreationHTTP(t.spec.Origin, creationToken)
		if err != nil {
			return ErrState
		}
	}
	owned := *p
	owned.Accounts, owned.OrdinaryRoleIDs = maps.Clone(p.Accounts), slices.Clone(p.OrdinaryRoleIDs)
	for subject, account := range owned.Accounts {
		account.OrdinaryRoleIDs = slices.Clone(account.OrdinaryRoleIDs)
		owned.Accounts[subject] = account
	}
	t.spec.BookStackDirectory = &owned
	scope := func(ctx context.Context, b Binding) (bookstack.RoleScope, error) {
		account, ok := owned.Accounts[b.Subject]
		if !ok && creation != nil {
			resolved, err := creation.ResolveCreatedScope(ctx, t.bookStackCreationScope(b.Subject))
			if err != nil || resolved.ID != b.NativeID {
				return bookstack.RoleScope{}, ErrState
			}
			return resolved, nil
		}
		if !ok || account.ID != b.NativeID {
			return bookstack.RoleScope{}, ErrState
		}
		roles := account.OrdinaryRoleIDs
		if roles == nil {
			roles = owned.OrdinaryRoleIDs
		}
		return bookstack.RoleScope{ProfileScope: bookstack.ProfileScope{Binding: bookstack.Binding{ID: b.NativeID, Issuer: b.Issuer, Subject: b.Subject},
			SetupID: account.SetupID, ClientID: owned.ClientID}, EverydayRole: t.spec.EverydayRole, OrdinaryRoleIDs: roles}, nil
	}
	inspectAccess := t.inspect
	t.inspect = func(ctx context.Context, b Binding) error {
		s, err := scope(ctx, b)
		if err != nil {
			return err
		}
		if err := inspectAccess(ctx, b); err != nil {
			return err
		}
		_, err = c.InspectDirectory(ctx, s)
		return err
	}
	t.directoryRead = func(ctx context.Context, b Binding) (DirectoryProfile, error) {
		s, err := scope(ctx, b)
		if err != nil {
			return DirectoryProfile{}, err
		}
		state, err := c.InspectDirectory(ctx, s)
		return DirectoryProfile{Name: state.Profile.Name, Email: state.Profile.Email, Administrator: state.Roles.Administrator}, err
	}
	t.directory = func(ctx context.Context, b Binding, profile DirectoryProfile) error {
		s, err := scope(ctx, b)
		if err != nil {
			return err
		}
		_, err = c.ApplyDirectory(ctx, s, bookstack.Profile{Name: profile.Name, Email: profile.Email}, profile.Administrator)
		if errors.Is(err, bookstack.ErrDirectoryUncertain) {
			return ErrDirectoryUncertain
		}
		return err
	}
	if t.spec.CreateAccounts {
		t.absent = func(ctx context.Context, subject string) error {
			absent, err := creation.InspectSubjectAbsence(ctx, t.bookStackCreationScope(subject))
			if err != nil {
				return err
			}
			if !absent {
				return ErrAccountConflict
			}
			return nil
		}
		t.create = func(ctx context.Context, b Binding, profile DirectoryProfile) (Binding, error) {
			spec := bookstack.CreationSpec{InstallationID: b.SetupID, Account: bookstack.AccountSpec{SetupID: bookStackAccountSetup(t.spec, b.Subject),
				Issuer: b.Issuer, ClientID: owned.ClientID, Subject: b.Subject, Name: profile.Name, Email: profile.Email}, EverydayRole: t.spec.EverydayRole, OrdinaryRoleIDs: owned.OrdinaryRoleIDs}
			state, err := creation.CreateManagedAccount(ctx, spec)
			if errors.Is(err, bookstack.ErrCreationUncertain) {
				return Binding{}, ErrCreationUncertain
			}
			if errors.Is(err, bookstack.ErrAccountConflict) {
				return Binding{}, ErrAccountConflict
			}
			if err != nil {
				return Binding{}, err
			}
			if state.Account.Disabled || state.Account.Administrator || state.Account.ActivationPending || state.Directory.Profile.Name != profile.Name || state.Directory.Profile.Email != profile.Email {
				return Binding{}, ErrCreationUncertain
			}
			b.NativeID = state.Account.ID
			return b, nil
		}
	}
	return nil
}

// A stable version-8 UUID keeps native account intent separate from installation state.
// Native resolution must still prove the exact immutable creation receipt; a hash is no adoption authority.
func bookStackAccountSetup(spec TargetSpec, subject string) string {
	sum := sha256.Sum256([]byte("ky.bookstack-account/1\x00" + spec.SetupID + "\x00" + spec.App + "\x00" + spec.Issuer + "\x00" + spec.BookStackDirectory.ClientID + "\x00" + subject))
	sum[6] = (sum[6] & 0x0f) | 0x80
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}
func (t *Target) bookStackCreationScope(subject string) bookstack.CreationScope {
	p := t.spec.BookStackDirectory
	return bookstack.CreationScope{InstallationID: t.spec.SetupID, AccountSetupID: bookStackAccountSetup(t.spec, subject), Issuer: t.spec.Issuer, ClientID: p.ClientID, Subject: subject, EverydayRole: t.spec.EverydayRole, OrdinaryRoleIDs: p.OrdinaryRoleIDs}
}
