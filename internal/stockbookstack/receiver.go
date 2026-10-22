package stockbookstack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"
)

// DirectoryPath is the only path the receiver answers.
const DirectoryPath = "/kyq/directory"

// MaxBody bounds a directory event, read before its signature is checked.
const MaxBody = 64 << 10

// Deadline bounds the BookStack calls of one event. KyIdentity holds a person's later changes
// once a delivery outlasts its 5 s client timeout, until an administrator resumes it; a 503
// before then is retried instead.
var Deadline = 3 * time.Second

// Receiver takes KyIdentity's signed suite-webhook events for BookStack's client and only ever
// removes roles: a removed person loses every role, a demoted one the admin role. It never
// grants a role, deletes an account or touches the service account. Each action is idempotent,
// so KyIdentity's retries and a replay inside the signature window can only repeat a removal.
type Receiver struct {
	API        API
	Secret     []byte // KyIdentity's directory signing secret for the client
	AdminClaim string // the KyIdentity role that maps to BookStack's admin role
	Log        io.Writer
	Now        func() time.Time // nil: time.Now
	admin      atomic.Int64     // the admin role's ID once read; it never changes
}

// event is the part of KyIdentity's SCIM user payload the receiver reads.
type event struct {
	ExternalID string  `json:"externalId"`
	Active     bool    `json:"active"`
	Roles      []claim `json:"roles"`
}

type claim struct {
	Value string `json:"value"`
}

func (rc *Receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != DirectoryPath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil || len(body) > MaxBody {
		rc.log("-", "-", "refused: body over 64 KiB", 0)
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	ev, err := syncauth.Verify(rc.Secret, syncauth.FromRequest(r), body, syncauth.Options{Now: rc.Now})
	if err != nil {
		rc.log("-", "-", "refused: "+strings.TrimPrefix(err.Error(), "syncauth: "), 0)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var e event
	if json.Unmarshal(body, &e) != nil {
		rc.log(ev.ID, ev.Type, "refused: unreadable body", 0)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), Deadline)
	defer cancel()
	status, user, outcome := rc.handle(ctx, ev.Type, e)
	rc.log(ev.ID, ev.Type, outcome, user)
	w.WriteHeader(status)
}

func (rc *Receiver) handle(ctx context.Context, kind string, e event) (status, user int, outcome string) {
	removal := kind == "user.deleted" || kind == "user.updated" && !e.Active
	switch {
	case kind == "user.created" || kind == "user.mfa_reset":
		return http.StatusNoContent, 0, "ignored: roles come at sign-in"
	case kind != "user.updated" && kind != "user.deleted":
		return http.StatusBadRequest, 0, "refused: unknown event type"
	}
	u, status, outcome := rc.find(ctx, e.ExternalID)
	switch {
	case status == http.StatusNotFound && !removal:
		return http.StatusNoContent, 0, "no account: nothing to change"
	case status != 0:
		return status, u.ID, outcome
	}
	if removal {
		if err := rc.API.SetRoles(ctx, u.ID, nil); err != nil {
			return unavailable(err)
		}
		return http.StatusNoContent, u.ID, "removed every role"
	}
	if slices.ContainsFunc(e.Roles, func(c claim) bool { return strings.EqualFold(strings.TrimSpace(c.Value), rc.AdminClaim) }) {
		return http.StatusNoContent, u.ID, "unchanged: holds the admin claim"
	}
	admin, err := rc.adminRole(ctx)
	if err != nil {
		return unavailable(err)
	}
	full, err := rc.API.User(ctx, u.ID)
	if err != nil {
		return unavailable(err)
	}
	var keep []int
	for _, r := range full.Roles {
		if r.ID != admin {
			keep = append(keep, r.ID)
		}
	}
	if len(keep) == len(full.Roles) {
		return http.StatusNoContent, u.ID, "unchanged: no admin role"
	}
	if err := rc.API.SetRoles(ctx, u.ID, keep); err != nil {
		return unavailable(err)
	}
	return http.StatusNoContent, u.ID, "removed the admin role"
}

// find is the one account bound to KyIdentity ID external, exactly. BookStack's filter
// compares case-insensitively, so a match differing in case is ambiguous, as are two
// matches, the service account and an empty ID.
func (rc *Receiver) find(ctx context.Context, external string) (User, int, string) {
	if external == "" {
		return User{}, http.StatusConflict, "refused: event names no externalId"
	}
	users, err := rc.API.Users(ctx, "external_auth_id", external)
	if err != nil {
		s, _, o := unavailable(err)
		return User{}, s, o
	}
	if len(users) == 0 {
		return User{}, http.StatusNotFound, "no account"
	}
	if len(users) != 1 || users[0].ExternalAuthID != external || users[0].Email == ServiceEmail {
		return User{}, http.StatusConflict, fmt.Sprintf("refused: %d accounts match, not exactly one other than the service account", len(users))
	}
	return users[0], 0, ""
}

func (rc *Receiver) adminRole(ctx context.Context) (int, error) {
	if id := rc.admin.Load(); id != 0 {
		return int(id), nil
	}
	roles, err := rc.API.Roles(ctx)
	if err != nil {
		return 0, err
	}
	id, err := AdminRole(roles)
	if err == nil {
		rc.admin.Store(int64(id))
	}
	return id, err
}

// unavailable makes KyIdentity retry: BookStack down, or refusing the receiver's token.
func unavailable(err error) (int, int, string) {
	if errors.Is(err, ErrUnavailable) {
		return http.StatusServiceUnavailable, 0, "BookStack unavailable: retry"
	}
	return http.StatusServiceUnavailable, 0, "BookStack refused: " + err.Error()
}

// log writes one line per request: event ID and type, outcome and BookStack user, never a
// token, a secret or the body.
func (rc *Receiver) log(id, kind, outcome string, user int) {
	if rc.Log != nil {
		fmt.Fprintf(rc.Log, "event=%q type=%q user=%d outcome=%q\n", id, kind, user, outcome)
	}
}
