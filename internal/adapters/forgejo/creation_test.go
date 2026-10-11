package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDirectoryCreationChecksSubjectOwnershipAndUnknownWrites(t *testing.T) {
	for _, kind := range []string{"foreign-subject", "incomplete-absence", "uncertain-write", "complete"} {
		t.Run(kind, func(t *testing.T) {
			writes := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/admin/users" && r.Method == http.MethodGet {
					if r.URL.Query().Get("source_id") != "7" || r.URL.Query().Get("login_name") != "subject" || r.URL.Query().Get("limit") != "2" {
						t.Error("native exact-subject lookup missing")
					}
					if kind == "foreign-subject" {
						w.Header().Set("X-Total-Count", "1")
						w.Write([]byte(`[{"id":9}]`))
						return
					}
					if kind != "incomplete-absence" {
						w.Header().Set("X-Total-Count", "0")
					}
					w.Write([]byte(`[]`))
					return
				}
				if r.Method == http.MethodPost {
					writes++
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["full_name"] != "New day" || body["source_id"] != float64(7) || body["login_name"] != "subject" || body["password"] != nil || body["admin"] != nil {
						t.Error("unexpected creation authority/password")
					}
					if kind == "uncertain-write" {
						w.WriteHeader(503)
						return
					}
					w.Write([]byte(`{"id":31}`))
					return
				}
				if writes == 0 {
					w.WriteHeader(404)
					return
				}
				w.Write([]byte(`{"id":31,"login":"day","source_id":7,"login_name":"subject","is_admin":false,"active":true,"prohibit_login":false,"full_name":"New day","email":"day@example.test"}`))
			}))
			defer server.Close()
			c, err := New(server.URL, "fixture-token")
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = server.Client().Transport
			b, err := c.CreateDirectoryAccount(context.Background(), AccountSpec{Username: "day", Name: "New day", Email: "day@example.test", SourceID: 7, Subject: "subject"})
			switch kind {
			case "foreign-subject", "incomplete-absence":
				if err == nil || writes != 0 {
					t.Fatal("unproved subject absence authorized creation")
				}
			case "uncertain-write":
				if !errors.Is(err, ErrCreateUncertain) || writes != 1 {
					t.Fatal("unknown native create not distinguished", err)
				}
			case "complete":
				if err != nil || b.ID != 31 || writes != 1 {
					t.Fatal("native creation receipt missing", err)
				}
			}
		})
	}
}
