package offboard

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const deactivateBody = `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}`

func TestSCIMAuthenticationScopeAndInputBoundary(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	b := fixtureBinding()
	id, err := s.Bind(b)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	target := &Target{spec: TargetSpec{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer}, revoke: func(context.Context, Binding) error { calls.Add(1); return nil }}
	token := strings.Repeat("cd", 32)
	receiver, err := NewReceiver(s, target, token)
	if err != nil {
		t.Fatal(err)
	}
	path := receiver.base + "/Users/" + id
	for _, test := range []struct {
		name, method, path, body string
		change                   func(*http.Request)
		status                   int
	}{
		{"plain TLS claim", "PATCH", path, deactivateBody, func(r *http.Request) { r.TLS = nil; r.Header.Set("X-Forwarded-Proto", "https") }, 401},
		{"missing bearer", "PATCH", path, deactivateBody, func(r *http.Request) {
			r.Header.Del("Authorization")
			r.AddCookie(&http.Cookie{Name: "token", Value: token})
		}, 401},
		{"wrong bearer", "PATCH", path, deactivateBody, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+strings.Repeat("ab", 32)) }, 401},
		{"duplicate bearer", "PATCH", path, deactivateBody, func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+token) }, 401},
		{"browser origin", "PATCH", path, deactivateBody, func(r *http.Request) { r.Header.Set("Origin", "https://foreign.test") }, 403},
		{"other app", "PATCH", strings.Replace(path, "/wiki/", "/other/", 1), deactivateBody, nil, 403},
		{"unknown mapping", "PATCH", receiver.base + "/Users/" + strings.Repeat("0", 64), deactivateBody, nil, 409},
		{"reactivate", "PATCH", path, strings.Replace(deactivateBody, "false", "true", 1), nil, 400},
		{"grant role", "PATCH", path, strings.Replace(deactivateBody, `"active"`, `"roles"`, 1), nil, 400},
		{"duplicate value", "PATCH", path, strings.Replace(deactivateBody, `"value":false`, `"value":true,"value":false`, 1), nil, 400},
		{"case duplicate", "PATCH", path, strings.Replace(deactivateBody, `"value":false`, `"Value":true,"value":false`, 1), nil, 400},
		{"missing value", "PATCH", path, strings.Replace(deactivateBody, `,"value":false`, "", 1), nil, 400},
		{"null value", "PATCH", path, strings.Replace(deactivateBody, "false", "null", 1), nil, 400},
		{"extra authority", "PATCH", path, strings.Replace(deactivateBody, `"value":false`, `"value":false,"issuer":"https://foreign.test"`, 1), nil, 400},
		{"suffix", "PATCH", path, deactivateBody + `{}`, nil, 400},
		{"oversize", "PATCH", path, strings.Repeat(" ", 4097), nil, 413},
		{"encoded body", "PATCH", path, deactivateBody, func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }, 415},
		{"wrong content", "PATCH", path, deactivateBody, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
		{"profile write", "PUT", path, `{"active":true}`, nil, 405},
		{"delete content", "DELETE", path, "", nil, 405},
		{"token query", "PATCH", path + "?token=" + token, deactivateBody, nil, 400},
		{"unfiltered enumeration", "GET", receiver.base + "/Users", "", nil, 400},
		{"wrong pagination", "GET", receiver.base + `/Users?filter=externalId+eq+%22exact-native-subject%22&startIndex=1&count=100`, "", nil, 400},
		{"unknown subject", "GET", receiver.base + `/Users?filter=externalId+eq+%22unknown%22&startIndex=1&count=2`, "", nil, 409},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, "https://bridge.example.test"+test.path, strings.NewReader(test.body))
			r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Content-Type", "application/scim+json")
			if test.change != nil {
				test.change(r)
			}
			w := httptest.NewRecorder()
			receiver.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatal("wrong refusal", w.Code)
			}
			if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), token) || strings.Contains(w.Body.String(), "foreign.test") {
				t.Fatal("unsafe diagnostics")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("refused input invoked product")
	}
	if _, present, err := s.Inspect(id); err != nil || present {
		t.Fatal("refused input published intent", err)
	}
	if _, err := NewReceiver(s, target, "invalid-private-token"); !errors.Is(err, ErrState) {
		t.Fatal("invalid credential accepted")
	}
	if _, err := NewReceiver(s, &Target{}, token); !errors.Is(err, ErrState) {
		t.Fatal("unconfigured native target accepted")
	}
}

func TestSCIMPendingCompletionAndRetainedScope(t *testing.T) {
	dir := stateDir(t)
	s := openStore(t, dir)
	defer func() { s.Close() }()
	b := fixtureBinding()
	id, err := s.Bind(b)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	target := &Target{spec: TargetSpec{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer}, revoke: func(_ context.Context, retained Binding) error {
		if retained != b {
			t.Error("native binding changed")
		}
		if calls.Add(1) == 1 {
			return errors.New("PRIVATE-NATIVE-TOKEN")
		}
		return nil
	}}
	token := strings.Repeat("cd", 32)
	receiver, err := NewReceiver(s, target, token)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(receiver)
	defer server.Close()
	request := func(method, path, body string) (int, http.Header, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/scim+json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, res.Header, data
	}
	path := receiver.base + "/Users/" + id
	status, headers, body := request("PATCH", path, deactivateBody)
	if status != 429 || headers.Get("Retry-After") == "" || bytes.Contains(body, []byte("PRIVATE-NATIVE-TOKEN")) {
		t.Fatal("pending revocation acknowledged or leaked", status)
	}
	status, _, body = request("GET", path, "")
	var read map[string]any
	if json.Unmarshal(body, &read) != nil || status != 200 || read["externalId"] != b.Subject {
		t.Fatal("invalid resource")
	}
	if read["active"] != true {
		t.Fatal("pending resource claims inactive")
	}
	status, _, _ = request("PATCH", path, deactivateBody)
	if status != 429 || calls.Load() != 1 {
		t.Fatal("pending retry bypassed backoff", status)
	}
	job, _, err := s.Inspect(id)
	if err != nil {
		t.Fatal(err)
	}
	// Advance only the test's due time, preserving its durable intent and audits.
	job.Next = time.Now().Add(-time.Second)
	if err := s.write(id+".job", job); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openStore(t, dir)
	receiver.store = s
	status, _, _ = request("PATCH", path, deactivateBody)
	if status != 204 || calls.Load() != 2 {
		t.Fatal("resumed proof not completed", status)
	}
	status, _, body = request("GET", path, "")
	read = map[string]any{}
	if json.Unmarshal(body, &read) != nil || status != 200 || read["active"] != false {
		t.Fatal("completed inactive proof absent")
	}
	filter := url.Values{"filter": {`externalId eq "` + b.Subject + `"`}, "startIndex": {"1"}, "count": {"2"}}
	status, _, body = request("GET", receiver.base+"/Users?"+filter.Encode(), "")
	var list struct {
		Total     int `json:"totalResults"`
		Resources []struct {
			ID, ExternalID string
			Active         *bool
		}
	}
	if json.Unmarshal(body, &list) != nil || status != 200 || list.Total != 1 || len(list.Resources) != 1 || list.Resources[0].ID != id || list.Resources[0].ExternalID != b.Subject || list.Resources[0].Active == nil || *list.Resources[0].Active {
		t.Fatal("bad filtered identity/completion")
	}
	status, _, _ = request("PATCH", path, deactivateBody)
	if status != 204 || calls.Load() != 2 {
		t.Fatal("completed request replayed native call")
	}
	other := b
	other.App = "other"
	otherID, err := s.Bind(other)
	if err != nil {
		t.Fatal(err)
	}
	status, _, _ = request("PATCH", receiver.base+"/Users/"+otherID, deactivateBody)
	if status != 409 || calls.Load() != 2 {
		t.Fatal("app-scoped bearer crossed resource scope")
	}
	job, _, err = s.Inspect(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, auditName(id, job.Attempts, "result"))); err != nil {
		t.Fatal(err)
	}
	status, _, _ = request("GET", path, "")
	if status != 503 {
		t.Fatal("missing completion audit reported inactive", status)
	}
	status, _, _ = request("PATCH", path, deactivateBody)
	if status != 503 || calls.Load() != 2 {
		t.Fatal("corrupt completion acknowledged or retried native mutation", status)
	}
}

// Real-product fixture helper: retry over verified TLS without editing the due
// time. Native harnesses prove actual session/token denial after this returns.
func resumeThroughSCIM(t *testing.T, store *Store, target *Target, id string, next time.Time) {
	t.Helper()
	token := strings.Repeat("cd", 32) // Disposable receiver credential, separate from product authority.
	receiver, err := NewReceiver(store, target, token)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(receiver)
	defer server.Close()
	request := func(method, body string) (int, http.Header, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+receiver.base+"/Users/"+id, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/scim+json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal("native SCIM request failed")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, response.Header, data
	}
	status, _, data := request("GET", "")
	var resource map[string]any
	if status != 200 || json.Unmarshal(data, &resource) != nil || resource["active"] != true {
		t.Fatal("pending native state reported inactive")
	}
	status, headers, _ := request("PATCH", deactivateBody)
	if status != 429 || headers.Get("Retry-After") == "" {
		t.Fatal("native backoff not preserved", status)
	}
	delay := time.Until(next)
	if delay < 0 || delay > 35*time.Second {
		t.Fatal("unexpected native retry deadline")
	}
	timer := time.NewTimer(delay + 10*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	status, _, _ = request("PATCH", deactivateBody)
	if status != 204 {
		t.Fatal("native SCIM completion not verified", status)
	}
	status, _, data = request("GET", "")
	resource = map[string]any{}
	if status != 200 || json.Unmarshal(data, &resource) != nil || resource["active"] != false {
		t.Fatal("native completion proof not represented")
	}
	status, _, _ = request("PATCH", deactivateBody)
	if status != 204 {
		t.Fatal("completed SCIM retry failed", status)
	}
}

func TestSCIMNativeDeadline(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	b := fixtureBinding()
	id, err := s.Bind(b)
	if err != nil {
		t.Fatal(err)
	}
	target := &Target{spec: TargetSpec{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer}, revoke: func(ctx context.Context, _ Binding) error { <-ctx.Done(); return ctx.Err() }}
	token := strings.Repeat("cd", 32)
	receiver, err := NewReceiver(s, target, token)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PATCH", "https://bridge.example.test"+receiver.base+"/Users/"+id, strings.NewReader(deactivateBody))
	r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/scim+json")
	w := httptest.NewRecorder()
	began := time.Now()
	receiver.ServeHTTP(w, r)
	if w.Code != 429 || time.Since(began) > 7*time.Second {
		t.Fatal("native deadline exceeded sender budget", w.Code)
	}
	job, _, err := s.Inspect(id)
	if err != nil || job.Status != "pending" || job.Attempts != 1 {
		t.Fatal("deadline lost durable intent", err)
	}
}

func TestSCIMBoundedNativeCallsPreserveQueuedIntent(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	b := fixtureBinding()
	ids := make([]string, 5)
	for i := range ids {
		account := b
		account.Subject = "subject-" + string(rune('a'+i))
		account.NativeID += int64(i)
		var err error
		ids[i], err = s.Bind(account)
		if err != nil {
			t.Fatal(err)
		}
	}
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	target := &Target{spec: TargetSpec{App: b.App, Kind: b.Kind, Origin: b.Origin, Issuer: b.Issuer}, revoke: func(ctx context.Context, _ Binding) error {
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	token := strings.Repeat("cd", 32)
	receiver, err := NewReceiver(s, target, token)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(receiver)
	defer server.Close()
	request := func(id string) int {
		req, _ := http.NewRequest("PATCH", server.URL+receiver.base+"/Users/"+id, strings.NewReader(deactivateBody))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/scim+json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Error("TLS fixture request failed")
			return 0
		}
		defer res.Body.Close()
		io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}
	results := make(chan int, 4)
	for _, id := range ids[:4] {
		go func(id string) { results <- request(id) }(id)
	}
	for range 4 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("native slots did not start")
		}
	}
	status := request(ids[4])
	if status != 429 {
		close(release)
		t.Fatal("native concurrency exceeded bound", status)
	}
	job, present, err := s.Inspect(ids[4])
	if err != nil || !present || job.Status != "pending" || job.Attempts != 0 {
		close(release)
		t.Fatal("busy receiver lost intent", job, err)
	}
	close(release)
	for range 4 {
		if status := <-results; status != 204 {
			t.Fatal("native completion failed", status)
		}
	}
	if status := request(ids[4]); status != 204 {
		t.Fatal("queued unattempted request did not resume", status)
	}
}
