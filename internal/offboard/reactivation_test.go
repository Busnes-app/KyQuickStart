package offboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/scim"
)

// kyIdentityPut is KyIdentity v0.4.3's re-assignment body: scimUserPayloadWithRoles,
// then deliverSCIM clears id/meta and sets externalId before the full PUT.
func kyIdentityPut(subject, username, name, email, role string) string {
	roles := []scim.MultiValue{}
	if role != "" {
		roles = []scim.MultiValue{{Value: role, Primary: true}}
	}
	user := scim.User{Schemas: []string{scim.UserSchema}, ExternalID: subject, UserName: username, DisplayName: name,
		Name: &scim.Name{Formatted: name}, Emails: []scim.MultiValue{{Value: email, Type: "work", Primary: true}}, Active: true}
	raw, _ := json.Marshal(struct {
		scim.User
		Roles []scim.MultiValue `json:"roles"`
	}{user, roles})
	return string(raw)
}

func userRequest(r *Receiver, method, id, body string) *httptest.ResponseRecorder {
	return groupRequest(r, method, r.base+"/Users/"+id, body)
}

func reportedActive(t *testing.T, r *Receiver, id string) bool {
	t.Helper()
	w := userRequest(r, "GET", id, "")
	var resource struct{ Active *bool }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &resource) != nil || resource.Active == nil {
		t.Fatal("account read failed", w.Code)
	}
	return *resource.Active
}

// epochFiles snapshots every revocation/reactivation job and audit file.
func epochFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if name := e.Name(); strings.HasSuffix(name, ".job") || strings.Contains(name, ".audit-") || strings.Contains(name, ".epoch-") {
			if files[name], err = os.ReadFile(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return files
}

func unchanged(t *testing.T, dir string, before map[string][]byte) {
	t.Helper()
	after := epochFiles(t, dir)
	for name, raw := range before {
		if !bytes.Equal(after[name], raw) {
			t.Fatal("epoch file rewritten or lost:", name)
		}
	}
}

func lifecycleTarget(b Binding) (*Target, *atomic.Int32, *atomic.Int32, *atomic.Pointer[DirectoryProfile]) {
	var revokes, reactivations atomic.Int32
	var projected atomic.Pointer[DirectoryProfile]
	target := directoryTarget(b, func(context.Context, Binding, DirectoryProfile) error { return nil })
	target.revoke = func(context.Context, Binding) error { revokes.Add(1); return nil }
	target.reactivate = func(_ context.Context, _ Binding, p *DirectoryProfile) error {
		reactivations.Add(1)
		projected.Store(p)
		return nil
	}
	target.directoryRead = func(context.Context, Binding) (DirectoryProfile, error) {
		return DirectoryProfile{Email: "day@example.test"}, nil
	}
	return target, &revokes, &reactivations, &projected
}

func TestReactivationLifecycleKeepsEveryEpochImmutable(t *testing.T) {
	dir := stateDir(t)
	s := openStore(t, dir)
	defer func() { s.Close() }()
	b := directoryBinding()
	id, err := s.Bind(b)
	if err != nil {
		t.Fatal(err)
	}
	target, revokes, reactivations, projected := lifecycleTarget(b)
	r, err := NewReceiver(s, target, strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	put := kyIdentityPut(b.Subject, b.Username, "Day 名", "day@example.test", "admin")
	if w := userRequest(r, "PATCH", id, deactivateBody); w.Code != 204 || revokes.Load() != 1 || reportedActive(t, r, id) {
		t.Fatal("revocation epoch failed", w.Code)
	}
	first := epochFiles(t, dir)
	if w := userRequest(r, "PUT", id, put); w.Code != 204 || reactivations.Load() != 1 || !reportedActive(t, r, id) {
		t.Fatal("re-assignment did not reactivate", w.Code)
	}
	if p := projected.Load(); p == nil || *p != (DirectoryProfile{Name: "Day 名", Email: "day@example.test", Administrator: true}) {
		t.Fatal("reactivation did not carry the PUT's profile and roles")
	}
	unchanged(t, dir, first)
	if w := userRequest(r, "PUT", id, put); w.Code != 204 || reactivations.Load() != 1 {
		t.Fatal("repeated PUT replayed reactivation", w.Code)
	}
	second := epochFiles(t, dir)
	for range 2 {
		if w := userRequest(r, "PATCH", id, deactivateBody); w.Code != 204 || revokes.Load() != 2 || reportedActive(t, r, id) {
			t.Fatal("second revocation epoch failed", w.Code)
		}
	}
	unchanged(t, dir, second)
	for _, epoch := range []uint32{1, 2, 3} {
		for _, phase := range []string{"started", "result"} {
			if _, err := s.epochAudit(id, epoch, 1, phase); err != nil {
				t.Fatal("epoch audit missing", epoch, phase)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, epochName(id, 4))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("repeated PATCH started another epoch")
	}
	s.Close()
	s = openStore(t, dir)
	if j, epoch, active, err := s.latest(id); err != nil || epoch != 3 || j.Status != "complete" || active {
		t.Fatal("restart lost epoch state", err)
	}
	// Missing proof for an earlier epoch fails closed.
	if err := os.Remove(filepath.Join(dir, epochAuditName(id, 2, 1, "result"))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Inspect(id); !errors.Is(err, ErrState) {
		t.Fatal("missing reactivation proof accepted")
	}
}

func TestReactivationWaitsForPendingOrAttemptingRevocation(t *testing.T) {
	dir := stateDir(t)
	s := openStore(t, dir)
	defer s.Close()
	b := directoryBinding()
	id, _ := s.Bind(b)
	target, _, reactivations, _ := lifecycleTarget(b)
	target.revoke = func(context.Context, Binding) error { return errors.New("native refusal") }
	r, _ := NewReceiver(s, target, strings.Repeat("cd", 32))
	put := kyIdentityPut(b.Subject, b.Username, "Day", "day@example.test", "user")
	if w := userRequest(r, "PATCH", id, deactivateBody); w.Code != 429 {
		t.Fatal("failed revocation not pending", w.Code)
	}
	pending := epochFiles(t, dir)
	if w := userRequest(r, "PUT", id, put); w.Code != 429 || w.Header().Get("Retry-After") == "" || reactivations.Load() != 0 {
		t.Fatal("PUT during pending revocation", w.Code)
	}
	unchanged(t, dir, pending)
	job, _, _ := s.Inspect(id)
	entered, release := make(chan struct{}), make(chan struct{})
	target.revoke = func(context.Context, Binding) error { close(entered); <-release; return nil }
	done := make(chan error)
	go func() { done <- s.Deliver(context.Background(), target, id, job.Next) }()
	<-entered
	attempting := epochFiles(t, dir)
	if j, _, _ := s.Inspect(id); j.Status != "attempting" {
		t.Fatal("revocation not attempting")
	}
	if w := userRequest(r, "PUT", id, put); w.Code != 429 || reactivations.Load() != 0 {
		t.Fatal("PUT during attempting revocation", w.Code)
	}
	unchanged(t, dir, attempting)
	if len(epochFiles(t, dir)) != len(attempting) {
		t.Fatal("PUT published state during revocation")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if w := userRequest(r, "PUT", id, put); w.Code != 204 || reactivations.Load() != 1 {
		t.Fatal("reactivation after completion", w.Code)
	}
}

// Bytes exactly as origin/master's Run and Request wrote them.
func TestOriginStoreLoadsUnchangedAsEpochOne(t *testing.T) {
	for _, status := range []string{"complete", "pending"} {
		t.Run(status, func(t *testing.T) {
			dir := stateDir(t)
			s := openStore(t, dir)
			defer s.Close()
			b := directoryBinding()
			b.Username = "legacy"
			id, _ := s.Bind(b)
			legacy := map[string]string{
				id + ".job": `{"schema":"ky.offboarding-job/1","binding":"` + id + `","status":"pending","requested":"2026-10-09T12:00:00Z","attempts":0,"next":"2026-10-09T12:00:00Z"}`,
			}
			if status == "complete" {
				legacy = map[string]string{
					id + ".job":                      `{"schema":"ky.offboarding-job/1","binding":"` + id + `","status":"complete","requested":"2026-10-09T12:00:00Z","attempts":1,"next":"2026-10-09T12:00:31Z"}`,
					id + ".audit-0000000001-started": `{"schema":"ky.offboarding-audit/1","binding":"` + id + `","attempt":1,"time":"2026-10-09T12:00:00Z","outcome":"started"}`,
					id + ".audit-0000000001-result":  `{"schema":"ky.offboarding-audit/1","binding":"` + id + `","attempt":1,"time":"2026-10-09T12:00:01Z","outcome":"success"}`,
				}
			}
			for name, raw := range legacy {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			target, revokes, reactivations, _ := lifecycleTarget(b)
			r, _ := NewReceiver(s, target, strings.Repeat("cd", 32))
			if j, epoch, active, err := s.latest(id); err != nil || epoch != 1 || j.Status != status || active != (status == "pending") {
				t.Fatal("origin store not read as epoch 1", err)
			}
			if err := s.Deliver(context.Background(), target, id, time.Now()); err != nil || revokes.Load() != map[string]int32{"complete": 0, "pending": 1}[status] {
				t.Fatal("origin epoch replayed or not resumed", err)
			}
			if status == "pending" {
				raw, _ := os.ReadFile(filepath.Join(dir, id+".job"))
				if bytes.Contains(raw, []byte("epoch")) || !strings.HasPrefix(string(raw), `{"schema":"ky.offboarding-job/1","binding":"`) {
					t.Fatal("origin job file changed format")
				}
			}
			before := epochFiles(t, dir)
			if w := userRequest(r, "PUT", id, kyIdentityPut(b.Subject, b.Username, "", "day@example.test", "user")); w.Code != 204 || reactivations.Load() != 1 {
				t.Fatal("origin store could not reactivate", w.Code)
			}
			unchanged(t, dir, before)
		})
	}
}

func TestRetiredGroupAndUserDeleteStayPermanentAcrossReactivation(t *testing.T) {
	s, target, r, account := groupReceiverFixture(t)
	target.reactivate = func(context.Context, Binding, *DirectoryProfile) error { return nil }
	collection := r.base + "/Groups"
	w := groupRequest(r, "POST", collection, groupBody("Engineering", account))
	var created struct{ ID string }
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatal("group not created", w.Code)
	}
	path := collection + "/" + created.ID
	if w := groupRequest(r, "DELETE", path, ""); w.Code != 204 {
		t.Fatal("group not retired", w.Code)
	}
	b, _ := s.Binding(account)
	if userRequest(r, "PATCH", account, deactivateBody).Code != 204 || userRequest(r, "PUT", account, kyIdentityPut(b.Subject, b.Username, "", "day@example.test", "user")).Code != 204 {
		t.Fatal("member lifecycle failed")
	}
	if w := groupRequest(r, "GET", path, ""); w.Code != 404 {
		t.Fatal("retired group revived", w.Code)
	}
	if w := groupRequest(r, "PUT", path, groupBody("Revived", account)); w.Code != 409 {
		t.Fatal("retired group accepted PUT", w.Code)
	}
	if w := userRequest(r, "DELETE", account, ""); w.Code != 405 {
		t.Fatal("user DELETE accepted", w.Code)
	}
}

func TestCreationStaysRefusedForRetainedSubject(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	var creates atomic.Int32
	target, scope, p := creationFixture(t, s, func(_ context.Context, b Binding, _ DirectoryProfile) (Binding, error) {
		creates.Add(1)
		b.NativeID = 31
		return b, nil
	})
	target.reactivate = func(context.Context, Binding, *DirectoryProfile) error { return nil }
	r, _ := NewReceiver(s, target, strings.Repeat("cd", 32))
	post := kyIdentityPut(scope.Subject, scope.Username, p.Name, p.Email, "user")
	if w := groupRequest(r, "POST", r.base+"/Users", post); w.Code != 201 {
		t.Fatal("account not created", w.Code)
	}
	id := bindingID(scope)
	for _, step := range []struct{ method, body string }{{"PATCH", deactivateBody}, {"POST", post}, {"PUT", post}, {"POST", post}} {
		w := groupRequest(r, step.method, r.base+"/Users", step.body)
		if step.method != "POST" {
			w = userRequest(r, step.method, id, step.body)
		}
		if want := map[string]int{"POST": 409, "PATCH": 204, "PUT": 204}[step.method]; w.Code != want {
			t.Fatal(step.method, "for retained subject:", w.Code)
		}
	}
	if creates.Load() != 1 {
		t.Fatal("retained subject created again")
	}
	if _, err := s.CreateAccount(context.Background(), target, scope, p, time.Now()); !errors.Is(err, ErrRevoked) {
		t.Fatal("creation accepted a retained subject", err)
	}
}

func TestReactivationUnsupportedForBookStackAndImmich(t *testing.T) {
	for _, b := range []Binding{fixtureBinding(), immichBinding()} {
		t.Run(b.Kind, func(t *testing.T) {
			dir := stateDir(t)
			s := openStore(t, dir)
			defer s.Close()
			id, _ := s.Bind(b)
			var revokes atomic.Int32
			target := &Target{spec: TargetSpec{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, ClientID: b.ClientID},
				revoke: func(context.Context, Binding) error { revokes.Add(1); return nil }}
			r, _ := NewReceiver(s, target, strings.Repeat("cd", 32))
			put := kyIdentityPut(b.Subject, b.Subject, "", "day@example.test", "user")
			if userRequest(r, "PATCH", id, deactivateBody).Code != 204 {
				t.Fatal("revocation failed")
			}
			for attempt := uint32(1); attempt <= 2; attempt++ {
				w := userRequest(r, "PUT", id, put)
				var e struct {
					Schemas []string
					Status  string
				}
				if w.Code != 409 || json.Unmarshal(w.Body.Bytes(), &e) != nil || len(e.Schemas) != 1 || e.Schemas[0] != errorSchema || e.Status != "409" || reportedActive(t, r, id) {
					t.Fatal("unsupported reactivation", w.Code, w.Body.String())
				}
				if a, err := s.epochAudit(id, 2, attempt, "result"); err != nil || a.Outcome != "reactivation_unsupported" {
					t.Fatal("refusal not audited", err)
				}
			}
			if j, _, _ := s.Inspect(id); j.Status != "refused" || j.Attempts != 2 {
				t.Fatal("refused epoch state", j.Status)
			}
			if err := s.Deliver(context.Background(), target, id, time.Now()); !errors.Is(err, ErrReactivationUnsupported) {
				t.Fatal("refused epoch retried as success", err)
			}
			if userRequest(r, "PATCH", id, deactivateBody).Code != 204 || revokes.Load() != 2 {
				t.Fatal("revocation after refused reactivation")
			}
		})
	}
}

func TestForgejoReactivationUnsupportedWithoutNativeWrite(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	b := directoryBinding()
	var writes atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": b.NativeID, "login": b.Username, "source_id": b.SourceID, "login_name": b.Subject,
			"is_admin": false, "active": false, "prohibit_login": true, "full_name": "", "email": "day@example.test"})
	}))
	defer server.Close()
	trustFixture(t, server)
	b.Origin = server.URL
	target, err := NewTarget(TargetSpec{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer, SourceID: b.SourceID, AdministratorRole: "admin", EverydayRole: "user"}, strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := s.Bind(b)
	if _, err := s.Request(id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(context.Background(), id, time.Now(), func(context.Context, Binding) error { return nil }); err != nil {
		t.Fatal(err)
	}
	r, _ := NewReceiver(s, target, strings.Repeat("cd", 32))
	w := userRequest(r, "PUT", id, kyIdentityPut(b.Subject, b.Username, "Day", "day@example.test", "user"))
	if a, err := s.epochAudit(id, 2, 1, "result"); w.Code != 409 || writes.Load() != 0 || err != nil || a.Outcome != "reactivation_unsupported" {
		t.Fatal("Forgejo reactivated without session revocation", w.Code)
	}
}

// No product reactivates natively yet; this proves the generic path a future one uses.
func TestReactivationAdministrationFollowsRoles(t *testing.T) {
	for _, mode := range []string{"revocation-only", "administrator", "everyday"} {
		t.Run(mode, func(t *testing.T) {
			s := openStore(t, stateDir(t))
			defer s.Close()
			b := directoryBinding()
			id, _ := s.Bind(b)
			target, _, _, _ := lifecycleTarget(b)
			var projected *DirectoryProfile
			target.reactivate = func(_ context.Context, _ Binding, p *DirectoryProfile) error {
				// The projection is durable intent before any native write.
				j, err := s.epochJob(id, 2)
				if _, startErr := s.epochAudit(id, 2, j.Attempts, "started"); err != nil || startErr != nil || j.Status != "attempting" || (p != nil && *j.Profile != *p) {
					t.Error("native reactivation preceded durable intent")
				}
				projected = p
				if p == nil {
					return ErrReactivationRefused // A native administrator without a role mapping.
				}
				return nil
			}
			role := "admin"
			if mode == "revocation-only" {
				target.directory, target.directoryRead, target.spec.AdministratorRole, target.spec.EverydayRole = nil, nil, "", ""
			} else if mode == "everyday" {
				role = "user"
			}
			r, _ := NewReceiver(s, target, strings.Repeat("cd", 32))
			if userRequest(r, "PATCH", id, deactivateBody).Code != 204 {
				t.Fatal("revocation failed")
			}
			w := userRequest(r, "PUT", id, kyIdentityPut(b.Subject, b.Username, "Day", "day@example.test", role))
			if mode == "revocation-only" {
				if a, err := s.epochAudit(id, 2, 1, "result"); w.Code != 409 || projected != nil || err != nil || a.Outcome != "administrator_refused" || reportedActive(t, r, id) {
					t.Fatal("administrator reactivated without a role mapping", w.Code)
				}
				return
			}
			if w.Code != 204 || projected == nil || projected.Administrator != (mode == "administrator") {
				t.Fatal("native administration does not follow the PUT's roles", w.Code)
			}
		})
	}
}

func TestReactivationRestartDoesNotRepeatVerifiedResult(t *testing.T) {
	dir := stateDir(t)
	s := openStore(t, dir)
	defer func() { s.Close() }()
	b := directoryBinding()
	id, _ := s.Bind(b)
	target, _, reactivations, _ := lifecycleTarget(b)
	r, _ := NewReceiver(s, target, strings.Repeat("cd", 32))
	put := kyIdentityPut(b.Subject, b.Username, "Day", "day@example.test", "user")
	if userRequest(r, "PATCH", id, deactivateBody).Code != 204 || userRequest(r, "PUT", id, put).Code != 204 {
		t.Fatal("lifecycle failed")
	}
	// Crash after the verified result audit, before the job marker.
	j, _ := s.epochJob(id, 2)
	j.Status = "attempting"
	if s.write(epochName(id, 2), j) != nil {
		t.Fatal("cannot simulate interrupted publication")
	}
	s.Close()
	s = openStore(t, dir)
	r, _ = NewReceiver(s, target, strings.Repeat("cd", 32))
	if w := userRequest(r, "PUT", id, put); w.Code != 204 || reactivations.Load() != 1 {
		t.Fatal("verified result repeated the native call", w.Code)
	}
	if j, _ := s.epochJob(id, 2); j.Status != "complete" || j.Attempts != 1 {
		t.Fatal("result not settled", j.Status)
	}
	// Crash during the native call: no result, so the attempt resumes after backoff.
	j.Status = "attempting"
	j.Next = time.Now().Add(time.Minute)
	if s.write(epochName(id, 2), j) != nil || os.Remove(filepath.Join(dir, epochAuditName(id, 2, 1, "result"))) != nil {
		t.Fatal("cannot simulate interrupted call")
	}
	if w := userRequest(r, "PUT", id, put); w.Code != 429 || reactivations.Load() != 1 {
		t.Fatal("interrupted attempt ignored backoff", w.Code)
	}
	if err := s.Deliver(context.Background(), target, id, j.Next); err != nil || reactivations.Load() != 2 {
		t.Fatal("interrupted reactivation did not resume", err)
	}
}

func TestReactivationDiagnosticsAndSupersedingRevocation(t *testing.T) {
	dir := stateDir(t)
	s := openStore(t, dir)
	defer s.Close()
	b := directoryBinding()
	id, _ := s.Bind(b)
	target, revokes, _, _ := lifecycleTarget(b)
	token := strings.Repeat("cd", 32)
	target.reactivate = func(context.Context, Binding, *DirectoryProfile) error {
		return errors.New("PRIVATE-NATIVE-BODY " + token)
	}
	r, _ := NewReceiver(s, target, token)
	if userRequest(r, "PATCH", id, deactivateBody).Code != 204 {
		t.Fatal("revocation failed")
	}
	w := userRequest(r, "PUT", id, kyIdentityPut(b.Subject, b.Username, "Day", "day@example.test", "user"))
	if w.Code != 429 || strings.Contains(w.Body.String(), "PRIVATE") || strings.Contains(w.Body.String(), token) {
		t.Fatal("failed reactivation unsafe or acknowledged", w.Code)
	}
	if a, err := s.epochAudit(id, 2, 1, "result"); err != nil || a.Outcome != "failed" || reportedActive(t, r, id) {
		t.Fatal("failure not audited", err)
	}
	// Revocation wins over an unfinished reactivation.
	if userRequest(r, "PATCH", id, deactivateBody).Code != 204 || revokes.Load() != 2 {
		t.Fatal("revocation did not supersede reactivation")
	}
	if j, epoch, active, err := s.latest(id); err != nil || epoch != 3 || j.Status != "complete" || active {
		t.Fatal("superseding epoch", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		if bytes.Contains(raw, []byte("PRIVATE")) || bytes.Contains(raw, []byte(token)) || strings.HasPrefix(e.Name(), "tmp-") {
			t.Fatal("native diagnostic or staging file in state", e.Name())
		}
	}
}
