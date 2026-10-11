package offboard

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const directoryBody = `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"externalId":"exact-native-subject","userName":"day","displayName":"Updated 名","active":true,"emails":[{"value":"day@example.test","type":"work","primary":true}],"roles":[{"value":"user"}]}`

func directoryRequest(receiver *Receiver, id, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, "https://bridge.example.test"+receiver.base+"/Users/"+id, strings.NewReader(body))
	r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
	r.Header.Set("Authorization", "Bearer "+receiver.token)
	r.Header.Set("Content-Type", "application/scim+json")
	w := httptest.NewRecorder()
	receiver.ServeHTTP(w, r)
	return w
}

func TestSCIMDirectoryBoundaryAndNativeRoleProjection(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	b := directoryBinding()
	id, err := s.Bind(b)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	current := DirectoryProfile{Email: "old@example.test"}
	target := directoryTarget(b, func(_ context.Context, retained Binding, profile DirectoryProfile) error {
		if retained != b {
			t.Error("request selected a different native account")
		}
		calls.Add(1)
		current = profile
		return nil
	})
	target.directoryRead = func(context.Context, Binding) (DirectoryProfile, error) { return current, nil }
	receiver, err := NewReceiver(s, target, strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(directoryBody, "exact-native-subject", "foreign-subject", 1),
		strings.Replace(directoryBody, `"userName":"day"`, `"userName":"renamed"`, 1),
		strings.Replace(directoryBody, `"active":true`, `"active":false`, 1),
		strings.Replace(directoryBody, `"active":true`, `"active":null`, 1),
		strings.Replace(directoryBody, `"active":true`, `"active":true,"Active":true`, 1),
		strings.Replace(directoryBody, `"roles":[{"value":"user"}]`, `"roles":null`, 1),
		strings.Replace(directoryBody, `,"roles":[{"value":"user"}]`, "", 1),
		strings.Replace(directoryBody, `"value":"user"`, `"value":"foreign-role"`, 1),
		strings.Replace(directoryBody, `"roles":[{"value":"user"}]`, `"roles":[{"value":"admin"},{"value":"admin"}]`, 1),
		strings.Replace(directoryBody, `"value":"user"`, `"value":"admin","type":"hidden"`, 1),
		strings.Replace(directoryBody, "day@example.test", "invalid", 1),
		strings.Replace(directoryBody, `"displayName":"Updated 名"`, `"displayName":"Updated\n"`, 1),
		strings.Replace(directoryBody, `"active":true`, `"active":true,"issuer":"https://foreign.test"`, 1),
		strings.Replace(directoryBody, `"active":true`, `"active":true,"id":"foreign-id"`, 1),
		directoryBody + `{}`,
		strings.Replace(directoryBody, "Updated 名", string([]byte{0xff}), 1),
	} {
		w := directoryRequest(receiver, id, invalid)
		if w.Code != http.StatusBadRequest || calls.Load() != 0 || strings.Contains(w.Body.String(), receiver.token) {
			t.Fatal("invalid directory input crossed the mutation boundary", w.Code)
		}
	}
	for _, admin := range []bool{true, false} {
		body := strings.Replace(directoryBody, `"value":"user"`, `"value":"user","primary":true`, 1)
		if admin {
			body = strings.Replace(body, `"value":"user"`, `"value":"admin"`, 1)
		}
		if w := directoryRequest(receiver, id, body); w.Code != http.StatusNoContent || current.Administrator != admin || current.Name != "Updated 名" || current.Email != "day@example.test" {
			t.Fatal("directory profile/role projection failed", w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "https://bridge.example.test"+receiver.base+"/Users/"+id, nil)
	r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
	r.Header.Set("Authorization", "Bearer "+receiver.token)
	w := httptest.NewRecorder()
	receiver.ServeHTTP(w, r)
	var resource struct {
		UserName, DisplayName string
		Roles                 []directoryValue
	}
	if json.Unmarshal(w.Body.Bytes(), &resource) != nil || w.Code != 200 || resource.UserName != b.Username || resource.DisplayName != current.Name || len(resource.Roles) != 1 || resource.Roles[0].Value != "user" {
		t.Fatal("directory read did not report native profile and role")
	}
	if _, err := s.Request(id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if w := directoryRequest(receiver, id, directoryBody); w.Code != http.StatusConflict || calls.Load() != 2 {
		t.Fatal("active PUT reset revocation", w.Code)
	}
}

func TestSCIMDirectoryUnknownMutationIsNotPendingAcceptance(t *testing.T) {
	s := openStore(t, stateDir(t))
	defer s.Close()
	b := directoryBinding()
	id, _ := s.Bind(b)
	var calls atomic.Int32
	target := directoryTarget(b, func(context.Context, Binding, DirectoryProfile) error {
		calls.Add(1)
		return ErrDirectoryUncertain
	})
	target.directoryRead = func(context.Context, Binding) (DirectoryProfile, error) {
		return DirectoryProfile{Email: "day@example.test"}, nil
	}
	receiver, err := NewReceiver(s, target, strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if w := directoryRequest(receiver, id, directoryBody); w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "" {
			t.Fatal("unknown native write released the sender's uncertainty fence", w.Code)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("unknown native mutation was retried")
	}
}
