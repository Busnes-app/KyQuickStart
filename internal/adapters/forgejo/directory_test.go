package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestDirectoryNativeRoleMutationAndOutcomeClassification(t *testing.T) {
	for _, mode := range []string{"demote", "grant", "disabled", "rejected", "timeout", "server-error", "verification-drift"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			writes := 0
			account := map[string]any{"id": 2, "login": "everyday", "source_id": 1, "login_name": "subject", "is_admin": mode != "grant",
				"active": mode != "disabled", "prohibit_login": mode == "disabled", "full_name": "Before", "email": "old@example.test"}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Method == http.MethodPatch {
					writes++
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 3 || body["full_name"] != "After" || body["email"] != "new@example.test" || body["admin"] != (mode == "grant") {
						t.Error("directory write changed fields outside profile and mapped administrator grant")
					}
					switch mode {
					case "rejected":
						w.WriteHeader(http.StatusBadRequest)
						return
					case "timeout":
						w.WriteHeader(http.StatusRequestTimeout)
						return
					case "server-error":
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					account["full_name"], account["email"], account["is_admin"] = body["full_name"], body["email"], body["admin"]
					if mode == "verification-drift" {
						account["active"] = false
					}
					w.WriteHeader(http.StatusOK)
					return
				}
				_ = json.NewEncoder(w).Encode(account)
			}))
			defer server.Close()
			client, _ := New(server.URL, "fixture-token")
			client.http.Transport = server.Client().Transport
			err := client.ApplyDirectory(context.Background(), Binding{ID: 2, Username: "everyday", SourceID: 1, Subject: "subject"},
				Profile{Name: "After", Email: "new@example.test"}, mode == "grant")
			uncertain := mode == "timeout" || mode == "server-error" || mode == "verification-drift"
			if errors.Is(err, ErrDirectoryUncertain) != uncertain || ((mode == "demote" || mode == "grant") != (err == nil)) {
				t.Fatal("incorrect native role write outcome", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if (mode == "disabled" && writes != 0) || (mode != "disabled" && writes != 1) {
				t.Fatal("disabled account was mutated or a directory write was replayed")
			}
		})
	}
}
