package offboard

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"
)

func (s *Receiver) findUncreated(w http.ResponseWriter, r *http.Request, id, subject string) {
	var retained bindingRecord
	if err := s.store.read(id+".binding", &retained); !errors.Is(err, os.ErrNotExist) {
		scimError(w, http.StatusServiceUnavailable)
		return
	}
	job, err := s.store.creationJob(id)
	if err == nil {
		if !s.target.matches(creationProbe(job.Scope)) {
			scimError(w, http.StatusServiceUnavailable)
			return
		}
	}
	if !errors.Is(err, os.ErrNotExist) && (err != nil || job.Status != "refused") {
		scimError(w, http.StatusServiceUnavailable)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		w.Header().Set("Retry-After", "5")
		scimError(w, http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	err = s.target.absent(ctx, subject)
	cancel()
	if err != nil {
		if errors.Is(err, ErrAccountConflict) {
			scimError(w, http.StatusConflict)
		} else {
			scimError(w, http.StatusServiceUnavailable)
		}
		return
	}
	scimReply(w, http.StatusOK, map[string]any{"schemas": []string{listSchema}, "totalResults": 0, "startIndex": 1, "itemsPerPage": 0, "Resources": []any{}})
}

func (s *Receiver) createAccount(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		scimError(w, http.StatusBadRequest)
		return
	}
	user, ok := s.parseDirectoryUser(w, r)
	if !ok {
		return
	}
	spec := s.target.spec
	scope := Binding{SetupID: spec.SetupID, App: spec.App, Kind: spec.Kind, Origin: spec.Origin, Issuer: spec.Issuer, SourceID: spec.SourceID, Subject: user.Subject, Username: user.Username}
	if spec.Kind == "immich" {
		scope.ClientID = spec.ClientID
	}
	if spec.Kind != "forgejo" {
		scope.Username = ""
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		w.Header().Set("Retry-After", "5")
		scimError(w, http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	b, err := s.store.CreateAccount(ctx, s.target, scope, user.Profile, time.Now())
	id := bindingID(scope)
	if err == nil {
		job, readErr := s.store.creationJob(id)
		if readErr != nil {
			err = ErrState
		} else if job.Status != "complete" {
			err = s.store.ApplyDirectory(ctx, s.target, id, user.Profile, time.Now())
			if err == nil {
				err = s.store.completeCreation(id)
			}
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, ErrPending):
			w.Header().Set("Retry-After", "5")
			scimError(w, http.StatusTooManyRequests)
		case errors.Is(err, ErrState), errors.Is(err, ErrCreationUncertain), errors.Is(err, ErrDirectoryUncertain):
			scimError(w, http.StatusServiceUnavailable)
		default:
			scimError(w, http.StatusConflict)
		}
		return
	}
	// Creation already verified the native identity/profile and durable initialization
	// proofs. Return the canonical resource, without another fallible native call
	// after a committed mutation; later GET still inspects actual native state.
	w.Header().Set("Location", s.base+"/Users/"+id)
	username := b.Username
	if b.Kind != "forgejo" {
		username = b.Subject
	}
	scimReply(w, http.StatusCreated, map[string]any{"schemas": []string{userSchema}, "id": id, "externalId": b.Subject, "userName": username, "active": true})
}
