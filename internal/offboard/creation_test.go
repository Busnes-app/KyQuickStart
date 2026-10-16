package offboard

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func creationFixture(t *testing.T, s *Store, create func(context.Context, Binding, DirectoryProfile) (Binding, error)) (*Target, Binding, DirectoryProfile) {
	t.Helper()
	b := directoryBinding()
	b.NativeID = 0
	p := DirectoryProfile{Name: "Directory day", Email: "day@example.test"}
	target := directoryTarget(b, func(context.Context, Binding, DirectoryProfile) error { return nil })
	target.spec.CreateAccounts = true
	target.create = create
	target.inspect = func(context.Context, Binding) error { return nil }
	target.absent = func(context.Context, string) error { return nil }
	target.directoryRead = func(context.Context, Binding) (DirectoryProfile, error) { return p, nil }
	return target, b, p
}
func TestCreationUnknownOutcomeIsNotReplayedAfterRestart(t *testing.T) {
	path := stateDir(t)
	s := openStore(t, path)
	defer func() { s.Close() }()
	calls := 0
	target, scope, p := creationFixture(t, s, func(context.Context, Binding, DirectoryProfile) (Binding, error) {
		calls++
		return Binding{}, ErrCreationUncertain
	})
	id := bindingID(scope)
	if _, err := s.CreateAccount(context.Background(), target, scope, p, time.Now()); !errors.Is(err, ErrCreationUncertain) {
		t.Fatal(err)
	}
	if _, err := s.Binding(id); err == nil {
		t.Fatal("unverified native identity was published")
	}
	s.Close()
	s = openStore(t, path)
	for range 2 {
		if _, err := s.CreateAccount(context.Background(), target, scope, p, time.Now()); !errors.Is(err, ErrCreationUncertain) {
			t.Fatal("creation hold lost", err)
		}
	}
	if calls != 1 {
		t.Fatal("unknown create repeated")
	}
	p.Name = "Later directory profile"
	if _, err := s.CreateAccount(context.Background(), target, scope, p, time.Now()); !errors.Is(err, ErrCreationUncertain) {
		t.Fatal("new profile released an unresolved creation fence", err)
	}
}
func TestCreationProofPublicationAndInitialization(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	calls := 0
	var target *Target
	var scope Binding
	var profile DirectoryProfile
	target, scope, profile = creationFixture(t, s, func(_ context.Context, b Binding, p DirectoryProfile) (Binding, error) {
		calls++
		job, err := s.creationJob(bindingID(b))
		var audit creationAudit
		if err != nil || job.Status != "attempting" || s.read(creationAuditName(bindingID(b), job.Attempt, "started"), &audit) != nil {
			t.Error("native write preceded durable intent/audit")
		}
		if calls == 1 {
			return Binding{}, errors.New("native refusal with a secret body")
		}
		b.NativeID = 31
		return b, nil
	})
	if _, err := s.CreateAccount(context.Background(), target, scope, profile, time.Now()); err == nil {
		t.Fatal("known refusal accepted")
	}
	b, err := s.CreateAccount(context.Background(), target, scope, profile, time.Now())
	if err != nil || b.NativeID != 31 {
		t.Fatal("known refusal could not retry", err)
	}
	id := bindingID(scope)
	job, err := s.creationJob(id)
	if err != nil || job.Status != "created" || job.Attempt != 2 {
		t.Fatal("verified native receipt not retained", err)
	}
	if err := s.completeCreation(id); err == nil {
		t.Fatal("uninitialized account acknowledged")
	}
	if err := s.ApplyDirectory(context.Background(), target, id, profile, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.completeCreation(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccount(context.Background(), target, scope, profile, time.Now()); err != nil || calls != 2 {
		t.Fatal("completed creation was replayed", err)
	}
	complete, err := s.creationJob(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.root.Name(), id+".creation")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Binding(id); !errors.Is(err, ErrState) {
		t.Fatal("created binding lost its required creation proof")
	}
	if _, err := s.Bind(b); !errors.Is(err, ErrState) {
		t.Fatal("registration bypassed missing creation proof")
	}
	if err := s.write(id+".creation", complete); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Request(id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccount(context.Background(), target, scope, profile, time.Now()); !errors.Is(err, ErrRevoked) {
		t.Fatal("creation reactivated revocation", err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.root.Name(), creationAuditName(id, 1, "result")))
	if strings.Contains(string(raw), "secret") {
		t.Fatal("native diagnostic entered receipt")
	}
}
func TestCreationRefusalRetriesCorrectedProfileOnly(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	calls := 0
	var target *Target
	var scope Binding
	var refused DirectoryProfile
	target, scope, refused = creationFixture(t, s, func(_ context.Context, b Binding, p DirectoryProfile) (Binding, error) {
		calls++
		if calls == 1 {
			return Binding{}, errors.New("native refusal")
		}
		// The job is attempting and the delivery lock is held: no concurrent overwrite.
		other := p
		other.Name = "Concurrent profile"
		if _, err := s.CreateAccount(context.Background(), target, scope, other, time.Now()); !errors.Is(err, ErrPending) {
			t.Error("concurrent retry entered an in-progress creation", err)
		}
		b.NativeID = 31
		return b, nil
	})
	id := bindingID(scope)
	if _, err := s.CreateAccount(context.Background(), target, scope, refused, time.Now()); err == nil {
		t.Fatal("known refusal accepted")
	}
	foreign := scope
	foreign.Username = "night"
	corrected := refused
	corrected.Name = "Corrected profile"
	if _, err := s.CreateAccount(context.Background(), target, foreign, corrected, time.Now()); !errors.Is(err, ErrAccountConflict) || calls != 1 {
		t.Fatal("refusal released a different scope", err)
	}
	if _, err := s.CreateAccount(context.Background(), target, scope, corrected, time.Now()); err != nil || calls != 2 {
		t.Fatal("refused creation could not retry a corrected profile", err)
	}
	job, err := s.creationJob(id)
	if err != nil || job.Status != "created" || job.Attempt != 2 || job.Profile != corrected {
		t.Fatal("corrected attempt not retained", err, job)
	}
	var first creationAudit
	if s.read(creationAuditName(id, 1, "result"), &first) != nil || first.Outcome != "failed" || first.Digest != creationDigest(scope, refused) {
		t.Fatal("refused attempt audit lost")
	}
	later := corrected
	later.Name = "Later profile"
	if _, err := s.CreateAccount(context.Background(), target, scope, later, time.Now()); !errors.Is(err, ErrAccountConflict) {
		t.Fatal("created account accepted a different digest", err)
	}
	if err := s.ApplyDirectory(context.Background(), target, id, corrected, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.completeCreation(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccount(context.Background(), target, scope, later, time.Now()); !errors.Is(err, ErrAccountConflict) || calls != 2 {
		t.Fatal("complete account accepted a different digest", err)
	}
}
func TestCreationAttemptingHoldsCorrectedProfile(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	calls := 0
	target, scope, p := creationFixture(t, s, func(context.Context, Binding, DirectoryProfile) (Binding, error) {
		calls++
		return Binding{}, errors.New("native refusal")
	})
	if _, err := s.CreateAccount(context.Background(), target, scope, p, time.Now()); err == nil {
		t.Fatal("known refusal accepted")
	}
	// An interrupted attempt is durable as attempting.
	id := bindingID(scope)
	job, err := s.creationJob(id)
	if err != nil {
		t.Fatal(err)
	}
	job.Status = "attempting"
	if err := s.write(id+".creation", job); err != nil {
		t.Fatal(err)
	}
	p.Name = "Corrected profile"
	if _, err := s.CreateAccount(context.Background(), target, scope, p, time.Now()); !errors.Is(err, ErrCreationUncertain) || calls != 1 {
		t.Fatal("corrected profile replaced an interrupted creation", err)
	}
}
func TestCreationReceiptBindingPublishFailureDoesNotRecreate(t *testing.T) {
	path := stateDir(t)
	s := openStore(t, path)
	defer func() { s.Close() }()
	calls := 0
	target, scope, p := creationFixture(t, s, func(_ context.Context, b Binding, _ DirectoryProfile) (Binding, error) {
		calls++
		if err := os.Mkdir(filepath.Join(path, bindingID(b)+".binding"), 0700); err != nil {
			t.Fatal(err)
		}
		b.NativeID = 31
		return b, nil
	})
	if _, err := s.CreateAccount(context.Background(), target, scope, p, time.Now()); !errors.Is(err, ErrState) {
		t.Fatal("partial publication not refused", err)
	}
	if err := os.Remove(filepath.Join(path, bindingID(scope)+".binding")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openStore(t, path)
	if _, err := s.CreateAccount(context.Background(), target, scope, p, time.Now()); err != nil || calls != 1 {
		t.Fatal("verified receipt caused another create", err)
	}
}
func TestSCIMCreationOptInAndUninitializedReadFence(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	calls := 0
	target, scope, p := creationFixture(t, s, func(_ context.Context, b Binding, _ DirectoryProfile) (Binding, error) {
		calls++
		b.NativeID = 31
		return b, nil
	})
	receiver, err := NewReceiver(s, target, strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://bridge.example.test"+path, strings.NewReader(body))
		r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
		r.Header.Set("Authorization", "Bearer "+receiver.token)
		r.Header.Set("Content-Type", "application/scim+json")
		w := httptest.NewRecorder()
		receiver.ServeHTTP(w, r)
		return w
	}
	id := bindingID(scope)
	if _, err := s.CreateAccount(context.Background(), target, scope, p, time.Now()); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, receiver.base+"/Users/"+id, ""); w.Code != 503 {
		t.Fatal("unfinished creation exposed as ready", w.Code)
	}
	// Repeat the exact native creation intent; it must initialize the retained
	// receipt, not issue a second native POST or infer readiness from a read.
	body := strings.Replace(directoryBody, "Updated 名", p.Name, 1)
	if w := request(http.MethodPost, receiver.base+"/Users", body); w.Code != 201 || calls != 1 {
		t.Fatal("SCIM create did not finish receipt", w.Code)
	}
	if w := request(http.MethodPost, receiver.base+"/Users", body); w.Code != 201 || calls != 1 {
		t.Fatal("SCIM create repeat mutated native creation", w.Code)
	}
	if w := request(http.MethodGet, receiver.base+"/Users/"+id, ""); w.Code != 200 {
		t.Fatal("proved creation not readable", w.Code)
	}
}

func TestCreationAdministratorGrantFollowsVerifiedReceipt(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	target, scope, p := creationFixture(t, s, func(_ context.Context, b Binding, _ DirectoryProfile) (Binding, error) {
		b.NativeID = 31
		return b, nil
	})
	p.Administrator = true
	id := bindingID(scope)
	granted := false
	target.directory = func(_ context.Context, b Binding, profile DirectoryProfile) error {
		receipt, err := s.creationJob(id)
		retained, bindErr := s.Binding(id)
		if err != nil || bindErr != nil || receipt.Status != "created" || retained != b || !profile.Administrator {
			t.Fatal("grant preceded owned receipt")
		}
		granted = true
		return nil
	}
	receiver, err := NewReceiver(s, target, strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(strings.Replace(directoryBody, "Updated 名", p.Name, 1), `"value":"user"`, `"value":"admin"`, 1)
	r := httptest.NewRequest(http.MethodPost, "https://bridge.example.test"+receiver.base+"/Users", strings.NewReader(body))
	r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
	r.Header.Set("Authorization", "Bearer "+receiver.token)
	r.Header.Set("Content-Type", "application/scim+json")
	w := httptest.NewRecorder()
	receiver.ServeHTTP(w, r)
	if w.Code != 201 || !granted {
		t.Fatal("authenticated administrator creation not completed", w.Code)
	}
}

func TestSCIMCreationRefusesForeignSubjectsAndDefaultTargets(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	calls := 0
	target, _, _ := creationFixture(t, s, func(_ context.Context, b Binding, _ DirectoryProfile) (Binding, error) {
		calls++
		b.NativeID = 31
		return b, nil
	})
	target.absent = func(context.Context, string) error { return ErrAccountConflict }
	receiver, err := NewReceiver(s, target, strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "https://bridge.example.test"+receiver.base+"/Users?filter=externalId%20eq%20%22foreign-subject%22&startIndex=1&count=2", nil)
	r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
	r.Header.Set("Authorization", "Bearer "+receiver.token)
	w := httptest.NewRecorder()
	receiver.ServeHTTP(w, r)
	if w.Code != 409 || calls != 0 {
		t.Fatal("foreign subject was projected as absent", w.Code)
	}
	target.create = nil
	r = httptest.NewRequest(http.MethodPost, "https://bridge.example.test"+receiver.base+"/Users", strings.NewReader(directoryBody))
	r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
	r.Header.Set("Authorization", "Bearer "+receiver.token)
	r.Header.Set("Content-Type", "application/scim+json")
	w = httptest.NewRecorder()
	receiver.ServeHTTP(w, r)
	if w.Code < 400 || calls != 0 {
		t.Fatal("default receiver accepted account creation", w.Code)
	}
}

func TestUUIDCreationReceiptsSurviveRestartAndInitializeBeforeAcknowledgment(t *testing.T) {
	path := stateDir(t)
	s := openStore(t, path)
	defer func() { s.Close() }()
	native := immichBinding()
	scope := creationScope(native)
	p := DirectoryProfile{Name: "Daily", Email: "day@example.test", Administrator: true}
	target := directoryTarget(native, func(context.Context, Binding, DirectoryProfile) error { return nil })
	target.spec.ClientID, target.spec.CreateAccounts = native.ClientID, true
	target.inspect = func(_ context.Context, b Binding) error {
		if b != native {
			return ErrState
		}
		return nil
	}
	target.directoryRead = func(context.Context, Binding) (DirectoryProfile, error) { return p, nil }
	calls := 0
	target.create = func(_ context.Context, b Binding, _ DirectoryProfile) (Binding, error) {
		calls++
		if b != scope {
			t.Fatal("SCIM metadata changed immutable creation scope")
		}
		job, err := s.creationJob(bindingID(scope))
		if err != nil || job.Status != "attempting" {
			t.Fatal("native creation preceded durable intent", err)
		}
		// Simulate failure after a valid native receipt but before binding publication.
		if err := os.Mkdir(filepath.Join(path, bindingID(scope)+".binding"), 0700); err != nil {
			t.Fatal(err)
		}
		return native, nil
	}
	if _, err := s.CreateAccount(context.Background(), target, scope, p, time.Now()); !errors.Is(err, ErrState) {
		t.Fatal("incomplete binding publication accepted", err)
	}
	if err := os.Remove(filepath.Join(path, bindingID(scope)+".binding")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openStore(t, path)
	if b, err := s.CreateAccount(context.Background(), target, scope, p, time.Now()); err != nil || b != native || calls != 1 {
		t.Fatal("verified UUID receipt could not repair publication without replay", err)
	}
	receiver, err := NewReceiver(s, target, strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	body := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"externalId":"` + scope.Subject + `","userName":"informational@example.test","displayName":"Daily","active":true,"emails":[{"value":"day@example.test","primary":true}],"roles":[{"value":"admin"}]}`
	r := httptest.NewRequest(http.MethodPost, "https://bridge.example.test"+receiver.base+"/Users", strings.NewReader(body))
	r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
	r.Header.Set("Authorization", "Bearer "+receiver.token)
	r.Header.Set("Content-Type", "application/scim+json")
	w := httptest.NewRecorder()
	receiver.ServeHTTP(w, r)
	if w.Code != http.StatusCreated || calls != 1 || !strings.Contains(w.Body.String(), `"userName":"`+scope.Subject+`"`) {
		t.Fatal("UUID creation failed to initialize or returned metadata as identity", w.Code)
	}
	s.Close()
	s = openStore(t, path)
	j, err := s.creationJob(bindingID(scope))
	if err != nil || j.Status != "complete" || j.DirectoryAttempt == 0 {
		t.Fatal("completed UUID initialization proof lost across restart", err)
	}
	if b, err := s.Binding(bindingID(scope)); err != nil || b != native {
		t.Fatal("native UUID/client receipt lost", err)
	}
	if err := os.Remove(filepath.Join(path, creationAuditName(bindingID(scope), j.Attempt, "result"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Binding(bindingID(scope)); !errors.Is(err, ErrState) {
		t.Fatal("UUID binding survived missing creation proof", err)
	}
}

func TestUUIDCreationForeignAndUnknownReceiptsHoldAcrossRestart(t *testing.T) {
	for _, mutation := range []string{"unknown", "foreign-client", "mixed-id", "missing-uuid"} {
		t.Run(mutation, func(t *testing.T) {
			path := stateDir(t)
			s := openStore(t, path)
			defer func() { s.Close() }()
			native := immichBinding()
			scope := creationScope(native)
			target := directoryTarget(native, func(context.Context, Binding, DirectoryProfile) error { return nil })
			target.spec.ClientID = native.ClientID
			calls := 0
			target.create = func(context.Context, Binding, DirectoryProfile) (Binding, error) {
				calls++
				switch mutation {
				case "unknown":
					return Binding{}, ErrCreationUncertain
				case "foreign-client":
					native.ClientID = "foreign-client"
				case "mixed-id":
					native.NativeID = 31
				case "missing-uuid":
					native.NativeUUID = ""
				}
				return native, nil
			}
			p := DirectoryProfile{Name: "Daily", Email: "day@example.test"}
			if _, err := s.CreateAccount(context.Background(), target, scope, p, time.Now()); !errors.Is(err, ErrCreationUncertain) {
				t.Fatal("unverified UUID creation acknowledged", err)
			}
			s.Close()
			s = openStore(t, path)
			p.Name = "Later profile"
			if _, err := s.CreateAccount(context.Background(), target, scope, p, time.Now()); !errors.Is(err, ErrCreationUncertain) || calls != 1 {
				t.Fatal("restart or later profile replayed held UUID creation", err)
			}
			if _, err := s.Binding(bindingID(scope)); err == nil {
				t.Fatal("unverified UUID became a binding")
			}
		})
	}
}

func TestCreationScopesNeverAcceptSuppliedNativeIdentities(t *testing.T) {
	for _, scope := range []Binding{creationScope(immichBinding()), creationScope(directoryBinding())} {
		if !validCreationScope(scope) {
			t.Fatal("valid uncreated scope refused")
		}
		for _, mutate := range []func(*Binding){
			func(b *Binding) { b.NativeID = 31 },
			func(b *Binding) { b.NativeUUID = "271333e2-c221-4601-b249-563b3d828530" },
			func(b *Binding) { b.Kind = "bookstack" },
			func(b *Binding) { b.ClientID = "foreign client" },
		} {
			b := scope
			mutate(&b)
			if validCreationScope(b) {
				t.Fatal("creation accepted supplied native identity or malformed scope")
			}
		}
	}
}
