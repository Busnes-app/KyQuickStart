package offboard

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const userSchema = "urn:ietf:params:scim:schemas:core:2.0:User"
const patchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
const listSchema = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
const errorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"

// Receiver projects app-scoped revocation and explicitly configured directory writes.
// Mount it without stripping its /scim/<app> prefix, on a direct TLS listener.
type Receiver struct {
	store  *Store
	target *Target
	token  string
	base   string
	slots  chan struct{}
}

func NewReceiver(store *Store, target *Target, token string) (*Receiver, error) {
	raw, err := hex.DecodeString(token)
	if store == nil || target == nil || target.revoke == nil || !appName.MatchString(target.spec.App) || err != nil || len(raw) != 32 || strings.ToLower(token) != token {
		return nil, ErrState
	}
	if target.spec.Groups != nil && (target.groupAbsent == nil || target.groupRead == nil || target.groupLabel == nil || target.groupCreate == nil || target.groupReplace == nil) {
		return nil, ErrState
	}
	return &Receiver{store: store, target: target, token: token, base: "/scim/" + target.spec.App, slots: make(chan struct{}, 4)}, nil
}

func scimReply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}
func scimError(w http.ResponseWriter, status int) {
	scimReply(w, status, map[string]any{"schemas": []string{errorSchema}, "status": strconv.Itoa(status), "detail": "request refused; check bridge configuration and revocation status"})
}

func (s *Receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	auth := r.Header.Values("Authorization")
	if r.TLS == nil || r.TLS.Version < tls.VersionTLS12 || len(auth) != 1 || subtle.ConstantTimeCompare([]byte(auth[0]), []byte("Bearer "+s.token)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		scimError(w, http.StatusUnauthorized)
		return
	}
	// No browser/session authority or forwarded TLS claims are accepted.
	if len(r.Header.Values("Origin")) != 0 || r.URL.RawPath != "" || len(r.URL.RawQuery) > 2048 {
		scimError(w, http.StatusForbidden)
		return
	}
	collection := s.base + "/Users"
	groups := s.base + "/Groups"
	if r.URL.Path == groups || strings.HasPrefix(r.URL.Path, groups+"/") {
		if s.target.spec.Groups == nil {
			scimError(w, http.StatusForbidden)
			return
		}
		s.groups(w, r, groups)
		return
	}
	if r.URL.Path == collection && r.Method == http.MethodPost && s.target.create != nil {
		s.createAccount(w, r)
		return
	}
	if r.URL.Path == collection && r.Method == http.MethodGet {
		s.find(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, collection+"/") || !recordID.MatchString(strings.TrimPrefix(r.URL.Path, collection+"/")) {
		scimError(w, http.StatusForbidden)
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		scimError(w, http.StatusBadRequest)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, collection+"/")
	b, err := s.store.Binding(id)
	if err != nil || !s.target.matches(b) {
		// A missing binding is not proof that an account is gone. In particular,
		// never return a 404 that a SCIM sender could treat as completed disable.
		scimError(w, http.StatusConflict)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.read(w, r, id, b, false)
	case http.MethodPut:
		if s.target.create != nil {
			creation, err := s.store.creationJob(id)
			if !errors.Is(err, os.ErrNotExist) && (err != nil || creation.Status != "complete") {
				scimError(w, http.StatusServiceUnavailable)
				return
			}
		}
		if s.target.directory == nil {
			w.Header().Set("Allow", "GET, PATCH")
			scimError(w, http.StatusMethodNotAllowed)
			return
		}
		s.replace(w, r, id, b)
	case http.MethodPatch:
		if !deactivationPatch(w, r) {
			return
		}
		s.deactivate(w, r, id)
	default:
		w.Header().Set("Allow", "GET, PATCH")
		if s.target.directory != nil {
			w.Header().Set("Allow", "GET, PATCH, PUT")
		}
		scimError(w, http.StatusMethodNotAllowed)
	}
}

func (s *Receiver) find(w http.ResponseWriter, r *http.Request) {
	subject, ok := externalFilter(r)
	if !ok {
		scimError(w, http.StatusBadRequest)
		return
	}
	id := bindingID(Binding{App: s.target.spec.App, Issuer: s.target.spec.Issuer, Subject: subject})
	b, err := s.store.Binding(id)
	if err != nil || !s.target.matches(b) {
		if s.target.create != nil {
			s.findUncreated(w, r, id, subject)
			return
		}
		scimError(w, http.StatusConflict)
		return
	}
	s.read(w, r, id, b, true)
}

func externalFilter(r *http.Request) (string, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 3 || len(query["filter"]) != 1 || len(query["startIndex"]) != 1 || len(query["count"]) != 1 || query.Get("startIndex") != "1" || query.Get("count") != "2" {
		return "", false
	}
	filter := query.Get("filter")
	var subject string
	if !utf8.ValidString(filter) || !strings.HasPrefix(filter, "externalId eq ") || json.Unmarshal([]byte(strings.TrimPrefix(filter, "externalId eq ")), &subject) != nil || !validSubject(subject) {
		return "", false
	}
	return subject, true
}

func (s *Receiver) read(w http.ResponseWriter, r *http.Request, id string, b Binding, list bool) {
	if s.target.create != nil {
		creation, err := s.store.creationJob(id)
		if !errors.Is(err, os.ErrNotExist) && (err != nil || creation.Status != "complete") {
			scimError(w, http.StatusServiceUnavailable)
			return
		}
	}
	j, present, err := s.store.Inspect(id)
	if err != nil {
		scimError(w, http.StatusServiceUnavailable)
		return
	}
	resource := map[string]any{"schemas": []string{userSchema}, "id": id, "externalId": b.Subject, "userName": b.Subject, "active": true}
	// This directory's names are opaque subjects, not native profile promises.
	// Conservatively report possible retained access until revocation is proved.
	// Always include active: clients with a bool field may decode omission as false.
	if present && j.Status == "complete" {
		resource["active"] = false
	}
	if s.target.directoryRead != nil {
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		default:
			w.Header().Set("Retry-After", "5")
			scimError(w, http.StatusTooManyRequests)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		profile, err := s.target.directoryRead(ctx, b)
		cancel()
		if err != nil || !validDirectoryProfile(profile) {
			scimError(w, http.StatusServiceUnavailable)
			return
		}
		role := s.target.spec.EverydayRole
		if profile.Administrator {
			role = s.target.spec.AdministratorRole
		}
		if b.Kind == "forgejo" {
			resource["userName"] = b.Username
		}
		resource["displayName"] = profile.Name
		resource["name"] = map[string]string{"formatted": profile.Name}
		resource["emails"] = []any{map[string]any{"value": profile.Email, "type": "work", "primary": true}}
		resource["roles"] = []any{map[string]string{"value": role}}
	}
	if list {
		scimReply(w, http.StatusOK, map[string]any{"schemas": []string{listSchema}, "totalResults": 1, "startIndex": 1, "itemsPerPage": 1, "Resources": []any{resource}})
	} else {
		scimReply(w, http.StatusOK, resource)
	}
}

func deactivationPatch(w http.ResponseWriter, r *http.Request) bool {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (kind != "application/scim+json" && kind != "application/json") || r.Header.Get("Content-Encoding") != "" {
		scimError(w, http.StatusUnsupportedMediaType)
		return false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		scimError(w, http.StatusRequestEntityTooLarge)
		return false
	}
	var body struct {
		Schemas    []string `json:"schemas"`
		Operations []struct {
			Op    string `json:"op"`
			Path  string `json:"path"`
			Value *bool  `json:"value"`
		} `json:"Operations"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if !uniqueJSON(data) || d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || len(body.Schemas) != 1 || body.Schemas[0] != patchSchema || len(body.Operations) != 1 || body.Operations[0].Op != "replace" || body.Operations[0].Path != "active" || body.Operations[0].Value == nil || *body.Operations[0].Value {
		scimError(w, http.StatusBadRequest)
		return false
	}
	return true
}

// Reject ambiguous duplicate fields before the typed decoder narrows the request.
func uniqueJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 16 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		open, compound := token.(json.Delim)
		if !compound {
			return true
		}
		seen := map[string]bool{}
		for d.More() {
			if open == '{' {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[strings.ToLower(name)] {
					return false
				}
				seen[strings.ToLower(name)] = true
			}
			if !value(depth + 1) {
				return false
			}
		}
		end, err := d.Token()
		return err == nil && ((open == '{' && end == json.Delim('}')) || (open == '[' && end == json.Delim(']')))
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func (s *Receiver) deactivate(w http.ResponseWriter, r *http.Request, id string) {
	now := time.Now()
	if _, err := s.store.Request(id, now); err != nil {
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
	// KyIdentity's delivery timeout is 20s. Bound this synchronous attempt well
	// inside it; incomplete native work stays in the durable job, never a 202.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	err := s.store.Deliver(ctx, s.target, id, now)
	cancel()
	if err == nil {
		scimReply(w, http.StatusNoContent, nil)
		return
	}
	if errors.Is(err, ErrPending) {
		job, _, readErr := s.store.Inspect(id)
		if readErr == nil {
			seconds := max(1, min(3600, int(time.Until(job.Next).Seconds())+1))
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			scimError(w, http.StatusTooManyRequests)
			return
		}
	}
	scimError(w, http.StatusServiceUnavailable)
}
