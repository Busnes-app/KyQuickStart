package forgejo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestConflictingOrIncompleteIdentityCannotMutate(t *testing.T) {
	for _, field := range []string{"id", "login", "source_id", "login_name", "is_admin", "active", "prohibit_login"} {
		t.Run(field, func(t *testing.T) {
			var writes atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					writes.Add(1)
				}
				account := map[string]any{"id": 2, "login": "everyday", "source_id": 1, "login_name": "subject", "is_admin": false, "active": true, "prohibit_login": false}
				delete(account, field)
				_ = json.NewEncoder(w).Encode(account)
			}))
			defer server.Close()
			c, err := New(server.URL, "fixture-token")
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = server.Client().Transport
			if err := c.Deprovision(context.Background(), Binding{ID: 2, Username: "everyday", SourceID: 1, Subject: "subject"}); err == nil || writes.Load() != 0 {
				t.Fatal("incomplete identity authorized a mutation")
			}
		})
	}
}

func TestRedirectCannotForwardCredential(t *testing.T) {
	var reached atomic.Bool
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
	}))
	defer destination.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	c, err := New(source.URL, "fixture-private-token")
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = source.Client().Transport
	_, err = c.Inspect(context.Background(), Binding{ID: 2, Username: "everyday", SourceID: 1, Subject: "subject"})
	if err == nil || reached.Load() || strings.Contains(err.Error(), "fixture-private-token") {
		t.Fatal("redirect or error exposed the credential")
	}
}

func TestInvalidNativeTokenListingCannotDelete(t *testing.T) {
	for _, listing := range []string{"null", `[{"id":0}]`, `[{"id":1},{"id":1}]`} {
		t.Run(listing, func(t *testing.T) {
			var deletes atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					deletes.Add(1)
				}
				if strings.Contains(r.URL.Path, "/tokens") {
					_, _ = w.Write([]byte(listing))
					return
				}
				_, _ = w.Write([]byte(`{"id":2,"login":"everyday","source_id":1,"login_name":"subject","is_admin":false,"active":false,"prohibit_login":true}`))
			}))
			defer server.Close()
			c, err := New(server.URL, "fixture-token")
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = server.Client().Transport
			if err := c.Deprovision(context.Background(), Binding{ID: 2, Username: "everyday", SourceID: 1, Subject: "subject"}); err == nil || deletes.Load() != 0 {
				t.Fatal("invalid listing authorized token deletion")
			}
		})
	}
}

func TestNativeFormRefusesAmbiguousAuthority(t *testing.T) {
	for _, page := range []string{
		`<form><input name="oauth2_key" value="one"><input name="oauth2_key" value="two"></form>`,
		`<form><input name="oauth2_key" value="one"></form><form><input name="oauth2_key" value="one"></form>`,
		`<form><input name="other" value="one"></form>`,
	} {
		if _, err := nativeForm([]byte(page), "oauth2_key"); err == nil {
			t.Fatal("ambiguous or missing source authority was accepted")
		}
	}
}

func TestNativeFormRedirectCannotForwardBootstrapPassword(t *testing.T) {
	var reached atomic.Bool
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer destination.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/user/login", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := *server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	source := &SourceClient{origin: server.URL, http: &client}
	_, _, err := source.request(context.Background(), http.MethodPost, "/user/login", url.Values{"password": {"private-fixture-password"}})
	if err == nil || reached.Load() || strings.Contains(err.Error(), "private-fixture-password") {
		t.Fatal("native form redirect or error exposed bootstrap credential")
	}
}
