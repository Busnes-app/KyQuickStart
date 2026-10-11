package offboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"
	"unicode/utf8"
)

// DirectoryRole names come only from protected target configuration. Full SCIM
// replacement cannot silently discard unknown roles or rename a pinned account.
type directoryValue struct {
	Value   string `json:"value"`
	Type    string `json:"type,omitempty"`
	Display string `json:"display,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

type directoryUser struct {
	Subject, Username string
	Profile           DirectoryProfile
}

func (s *Receiver) parseDirectoryUser(w http.ResponseWriter, r *http.Request) (directoryUser, bool) {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (kind != "application/scim+json" && kind != "application/json") || r.Header.Get("Content-Encoding") != "" {
		scimError(w, http.StatusUnsupportedMediaType)
		return directoryUser{}, false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if err != nil {
		scimError(w, http.StatusRequestEntityTooLarge)
		return directoryUser{}, false
	}
	var user struct {
		Schemas     []string `json:"schemas"`
		ExternalID  string   `json:"externalId"`
		UserName    string   `json:"userName"`
		DisplayName string   `json:"displayName,omitempty"`
		Name        *struct {
			Formatted  string `json:"formatted,omitempty"`
			GivenName  string `json:"givenName,omitempty"`
			FamilyName string `json:"familyName,omitempty"`
		} `json:"name,omitempty"`
		Active *bool             `json:"active"`
		Emails []directoryValue  `json:"emails"`
		Roles  *[]directoryValue `json:"roles"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if !utf8.Valid(data) || !uniqueJSON(data) || decoder.Decode(&user) != nil || decoder.Decode(new(any)) != io.EOF ||
		len(user.Schemas) != 1 || user.Schemas[0] != userSchema || !validSubject(user.ExternalID) ||
		(s.target.spec.Kind == "forgejo" && !nativeName.MatchString(user.UserName) || s.target.spec.Kind != "forgejo" && !validClientID(user.UserName)) || user.Active == nil || !*user.Active || len(user.Emails) != 1 ||
		(user.Emails[0].Type != "" && user.Emails[0].Type != "work") || user.Emails[0].Display != "" ||
		user.Roles == nil || len(*user.Roles) > 2 {
		scimError(w, http.StatusBadRequest)
		return directoryUser{}, false
	}
	profile := DirectoryProfile{Name: user.DisplayName, Email: user.Emails[0].Value}
	if user.Name != nil {
		if user.Name.GivenName != "" || user.Name.FamilyName != "" || (profile.Name != "" && user.Name.Formatted != "" && user.Name.Formatted != profile.Name) {
			scimError(w, http.StatusBadRequest)
			return directoryUser{}, false
		}
		if profile.Name == "" {
			profile.Name = user.Name.Formatted
		}
	}
	seen := map[string]bool{}
	// SCIM primary is selection metadata; only the pinned role values confer authority.
	for _, role := range *user.Roles {
		if seen[role.Value] || role.Type != "" || role.Display != "" || (role.Value != s.target.spec.AdministratorRole && role.Value != s.target.spec.EverydayRole) {
			scimError(w, http.StatusBadRequest)
			return directoryUser{}, false
		}
		seen[role.Value] = true
		if role.Value == s.target.spec.AdministratorRole {
			profile.Administrator = true
		}
	}
	if !validDirectoryProfile(profile) {
		scimError(w, http.StatusBadRequest)
		return directoryUser{}, false
	}
	return directoryUser{Subject: user.ExternalID, Username: user.UserName, Profile: profile}, true
}

func (s *Receiver) replace(w http.ResponseWriter, r *http.Request, id string, binding Binding) {
	user, ok := s.parseDirectoryUser(w, r)
	if !ok {
		return
	}
	if user.Subject != binding.Subject || (binding.Kind == "forgejo" && user.Username != binding.Username) {
		scimError(w, http.StatusBadRequest)
		return
	}
	profile := user.Profile
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		w.Header().Set("Retry-After", "5")
		scimError(w, http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	err := s.store.ApplyDirectory(ctx, s.target, id, profile, time.Now())
	cancel()
	switch {
	case err == nil:
		scimReply(w, http.StatusNoContent, nil)
	case errors.Is(err, ErrPending):
		w.Header().Set("Retry-After", "5")
		scimError(w, http.StatusTooManyRequests)
	case errors.Is(err, ErrDirectoryUncertain), errors.Is(err, ErrState):
		scimError(w, http.StatusServiceUnavailable)
	default:
		scimError(w, http.StatusConflict)
	}
}
