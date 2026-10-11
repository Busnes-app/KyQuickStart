package offboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const groupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"

type directoryGroup struct {
	ExternalID, Label string
	Members           []string
}

func parseGroup(w http.ResponseWriter, r *http.Request) (directoryGroup, bool) {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (kind != "application/scim+json" && kind != "application/json") || r.Header.Get("Content-Encoding") != "" {
		scimError(w, http.StatusUnsupportedMediaType)
		return directoryGroup{}, false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
	if err != nil {
		scimError(w, http.StatusRequestEntityTooLarge)
		return directoryGroup{}, false
	}
	var wire struct {
		Schemas    []string          `json:"schemas"`
		ExternalID string            `json:"externalId"`
		Label      string            `json:"displayName"`
		Members    *[]directoryValue `json:"members"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !utf8.Valid(raw) || !uniqueJSON(raw) || d.Decode(&wire) != nil || d.Decode(new(any)) != io.EOF || len(wire.Schemas) != 1 || wire.Schemas[0] != groupSchema || !validSubject(wire.ExternalID) || wire.Label == "" || len(wire.Label) > 100 || strings.IndexFunc(wire.Label, unicode.IsControl) >= 0 || wire.Members == nil || len(*wire.Members) > 100 {
		scimError(w, http.StatusBadRequest)
		return directoryGroup{}, false
	}
	group := directoryGroup{ExternalID: wire.ExternalID, Label: wire.Label, Members: []string{}}
	seen := map[string]bool{}
	for _, member := range *wire.Members {
		if !recordID.MatchString(member.Value) || seen[member.Value] || member.Type != "" || member.Display != "" || member.Primary {
			scimError(w, http.StatusBadRequest)
			return directoryGroup{}, false
		}
		seen[member.Value] = true
		group.Members = append(group.Members, member.Value)
	}
	slices.Sort(group.Members)
	return group, true
}

func groupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrPending):
		w.Header().Set("Retry-After", "5")
		scimError(w, http.StatusTooManyRequests)
	case errors.Is(err, ErrState), errors.Is(err, ErrGroupUncertain), errors.Is(err, ErrGroupMembershipUncertain), errors.Is(err, ErrDirectoryUncertain), errors.Is(err, ErrCreationUncertain):
		scimError(w, http.StatusServiceUnavailable)
	default:
		scimError(w, http.StatusConflict)
	}
}

func (s *Receiver) groups(w http.ResponseWriter, r *http.Request, collection string) {
	var input directoryGroup
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		var ok bool
		input, ok = parseGroup(w, r)
		if !ok {
			return
		}
	}
	if r.URL.Path != collection && (!strings.HasPrefix(r.URL.Path, collection+"/") || !recordID.MatchString(strings.TrimPrefix(r.URL.Path, collection+"/"))) {
		scimError(w, http.StatusForbidden)
		return
	}
	if (r.URL.Path != collection || r.Method != http.MethodGet) && (r.URL.RawQuery != "" || r.URL.ForceQuery) {
		scimError(w, http.StatusBadRequest)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		groupError(w, ErrPending)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if r.URL.Path == collection {
		switch r.Method {
		case http.MethodGet:
			external, ok := externalFilter(r)
			if !ok {
				scimError(w, http.StatusBadRequest)
				return
			}
			scope := s.target.groupScope(external)
			id := groupID(scope)
			g, err := s.store.GroupBinding(id)
			if err == nil {
				resource, deleted, err := s.readGroup(ctx, id, g)
				if err != nil {
					groupError(w, err)
					return
				}
				resources := []any{}
				if !deleted {
					resources = append(resources, resource)
				}
				scimReply(w, http.StatusOK, map[string]any{"schemas": []string{listSchema}, "totalResults": len(resources), "startIndex": 1, "itemsPerPage": len(resources), "Resources": resources})
				return
			}
			var raw json.RawMessage
			if err := s.store.read(id+".group-binding", &raw); !errors.Is(err, os.ErrNotExist) {
				groupError(w, ErrState)
				return
			}
			j, _, err := s.store.groupCreation(id)
			if !errors.Is(err, os.ErrNotExist) && (err != nil || j.Status != "refused" || !sameGroup(j.Scope, scope)) {
				groupError(w, ErrState)
				return
			}
			if err := s.target.groupAbsent(ctx, scope); err != nil {
				if errors.Is(err, ErrGroupConflict) {
					groupError(w, err)
				} else {
					groupError(w, ErrState)
				}
				return
			}
			scimReply(w, http.StatusOK, map[string]any{"schemas": []string{listSchema}, "totalResults": 0, "startIndex": 1, "itemsPerPage": 0, "Resources": []any{}})
		case http.MethodPost:
			scope := s.target.groupScope(input.ExternalID)
			if err := s.validateGroupMembers(input.Members); err != nil {
				groupError(w, err)
				return
			}
			g, err := s.store.CreateGroup(ctx, s.target, scope, time.Now())
			if err != nil {
				groupError(w, err)
				return
			}
			id := groupID(g)
			if err := s.writeGroup(ctx, id, input, false); err != nil {
				groupError(w, err)
				return
			}
			w.Header().Set("Location", collection+"/"+id)
			scimReply(w, http.StatusCreated, groupResource(id, g.ExternalID, input.Label, input.Members))
		default:
			w.Header().Set("Allow", "GET, POST")
			scimError(w, http.StatusMethodNotAllowed)
		}
		return
	}
	id := strings.TrimPrefix(r.URL.Path, collection+"/")
	g, err := s.store.GroupBinding(id)
	if err != nil || !s.target.matchesGroup(g) {
		groupError(w, ErrState)
		return
	}
	switch r.Method {
	case http.MethodGet:
		resource, deleted, err := s.readGroup(ctx, id, g)
		if err != nil {
			groupError(w, err)
			return
		}
		if deleted {
			scimError(w, http.StatusNotFound)
			return
		}
		scimReply(w, http.StatusOK, resource)
	case http.MethodPut:
		if input.ExternalID != g.ExternalID {
			scimError(w, http.StatusBadRequest)
			return
		}
		if err := s.writeGroup(ctx, id, input, false); err != nil {
			groupError(w, err)
			return
		}
		scimReply(w, http.StatusNoContent, nil)
	case http.MethodDelete:
		if r.ContentLength != 0 {
			scimError(w, http.StatusBadRequest)
			return
		}
		if err := s.writeGroup(ctx, id, directoryGroup{ExternalID: g.ExternalID, Members: []string{}}, true); err != nil {
			groupError(w, err)
			return
		}
		scimReply(w, http.StatusNoContent, nil)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		scimError(w, http.StatusMethodNotAllowed)
	}
}

func groupResource(id, external, label string, members []string) map[string]any {
	values := []any{}
	for _, id := range members {
		values = append(values, map[string]string{"value": id})
	}
	return map[string]any{"schemas": []string{groupSchema}, "id": id, "externalId": external, "displayName": label, "members": values}
}

func (s *Receiver) validateGroupMembers(ids []string) error {
	for _, id := range ids {
		b, err := s.store.Binding(id)
		if err != nil || !s.target.matches(b) {
			return ErrGroupConflict
		}
		if err := s.store.membershipReady(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Receiver) writeGroup(ctx context.Context, id string, input directoryGroup, deleted bool) error {
	if err := s.validateGroupMembers(input.Members); err != nil {
		return err
	}
	return s.store.applyGroupMembership(ctx, s.target, id, GroupMembership{Owned: append([]string{}, input.Members...), Members: input.Members, Label: input.Label, Deleted: deleted}, time.Now(), true)
}

func (s *Receiver) readGroup(ctx context.Context, id string, g GroupBinding) (map[string]any, bool, error) {
	if !s.target.matchesGroup(g) {
		return nil, false, ErrState
	}
	lock := &s.store.account("group:" + id).delivery
	if !lock.TryLock() {
		return nil, false, ErrPending
	}
	defer lock.Unlock()
	j, err := s.store.membershipJob(id)
	if !errors.Is(err, os.ErrNotExist) && (err != nil || j.Status == "attempting" || j.Status == "uncertain") {
		return nil, false, ErrGroupMembershipUncertain
	}
	owned := []Binding{}
	for _, id := range j.Scope.Owned {
		b, err := s.store.Binding(id)
		if err != nil || !s.target.matches(b) {
			return nil, false, ErrState
		}
		owned = append(owned, b)
	}
	label, members, err := s.target.groupRead(ctx, g, owned)
	if err != nil {
		return nil, false, ErrState
	}
	ids := []string{}
	for _, b := range members {
		ids = append(ids, bindingID(b))
	}
	slices.Sort(ids)
	if j.Status == "complete" && !slices.Equal(ids, j.Scope.Members) {
		return nil, false, ErrState
	}
	return groupResource(id, g.ExternalID, label, ids), j.Status == "complete" && j.Scope.Deleted, nil
}
