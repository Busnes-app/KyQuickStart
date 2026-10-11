package forgejo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestProfileRetryAfterLostReplyPreservesBindingAndAuthority(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "disabled"}[disabled], func(t *testing.T) {
			var mu sync.Mutex
			writes := 0
			account := map[string]any{"id": 2, "login": "everyday", "source_id": 1, "login_name": "subject", "is_admin": true,
				"active": !disabled, "prohibit_login": disabled, "full_name": "Old name", "email": "old@example.test"}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Method == http.MethodPatch {
					var update map[string]string
					if json.NewDecoder(r.Body).Decode(&update) != nil || len(update) != 2 || update["full_name"] != "Updated 名" || update["email"] != "new@example.test" {
						t.Error("profile write included unexpected fields")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					account["full_name"], account["email"] = update["full_name"], update["email"]
					writes++
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = connection.Close()
					return
				}
				_ = json.NewEncoder(w).Encode(account)
			}))
			defer server.Close()
			client, _ := New(server.URL, "private-fixture-token")
			client.http.Transport = server.Client().Transport
			binding := Binding{ID: 2, Username: "everyday", SourceID: 1, Subject: "subject"}
			profile := Profile{Name: "Updated 名", Email: "new@example.test"}
			if err := client.UpdateProfile(context.Background(), binding, profile); err == nil || strings.Contains(err.Error(), "private-fixture-token") {
				t.Fatal("lost reply was reported as completed or exposed credential")
			}
			if err := client.UpdateProfile(context.Background(), binding, profile); err != nil {
				t.Fatal(err)
			}
			after, err := client.Inspect(context.Background(), binding)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || writes != 1 || after.Name != profile.Name || after.Email != profile.Email || !after.Admin || after.Active == disabled || after.LoginProhibited != disabled {
				t.Fatal("retry replayed mutation or changed retained authority/profile")
			}
		})
	}
}

func TestProfileBoundaryRefusesBeforeMutation(t *testing.T) {
	for _, invalid := range []string{"binding", "name", "name-control", "name-utf8", "email", "email-display", "source", "missing-name", "missing-email"} {
		t.Run(invalid, func(t *testing.T) {
			var mu sync.Mutex
			writes := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Method != http.MethodGet {
					writes++
				}
				account := map[string]any{"id": 2, "login": "everyday", "source_id": 1, "login_name": "subject", "is_admin": false,
					"active": true, "prohibit_login": false, "full_name": "Old", "email": "old@example.test"}
				switch invalid {
				case "source":
					account["source_id"] = 10
				case "missing-name":
					delete(account, "full_name")
				case "missing-email":
					delete(account, "email")
				}
				_ = json.NewEncoder(w).Encode(account)
			}))
			defer server.Close()
			client, _ := New(server.URL, "fixture-token")
			client.http.Transport = server.Client().Transport
			binding := Binding{ID: 2, Username: "everyday", SourceID: 1, Subject: "subject"}
			profile := Profile{Name: "New", Email: "new@example.test"}
			switch invalid {
			case "binding":
				binding.ID = 0
			case "name":
				profile.Name = strings.Repeat("x", 101)
			case "name-control":
				profile.Name = "private\nvalue"
			case "name-utf8":
				profile.Name = string([]byte{0xff})
			case "email":
				profile.Email = "invalid-address"
			case "email-display":
				profile.Email = "Name <new@example.test>"
			}
			if err := client.UpdateProfile(context.Background(), binding, profile); err == nil {
				t.Fatal("invalid profile/native identity accepted")
			}
			mu.Lock()
			defer mu.Unlock()
			if writes != 0 {
				t.Fatal("invalid boundary authorized a mutation")
			}
		})
	}
}

func TestProfileVerificationRefusesNativeDrift(t *testing.T) {
	for _, drift := range []string{"full_name", "email", "source_id", "is_admin", "active", "prohibit_login"} {
		t.Run(drift, func(t *testing.T) {
			var mu sync.Mutex
			updated := false
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Method == http.MethodPatch {
					updated = true
					w.WriteHeader(http.StatusOK)
					return
				}
				account := map[string]any{"id": 2, "login": "everyday", "source_id": 1, "login_name": "subject", "is_admin": false,
					"active": true, "prohibit_login": false, "full_name": "Old", "email": "old@example.test"}
				if updated {
					account["full_name"], account["email"] = "", "new@example.test"
					account[drift] = map[string]any{"full_name": "Wrong", "email": "wrong@example.test", "source_id": 99, "is_admin": true, "active": false, "prohibit_login": true}[drift]
				}
				_ = json.NewEncoder(w).Encode(account)
			}))
			defer server.Close()
			client, _ := New(server.URL, "fixture-token")
			client.http.Transport = server.Client().Transport
			if err := client.UpdateProfile(context.Background(), Binding{ID: 2, Username: "everyday", SourceID: 1, Subject: "subject"}, Profile{Email: "new@example.test"}); err == nil {
				t.Fatal("native drift reported as successful profile synchronization")
			}
		})
	}
}
