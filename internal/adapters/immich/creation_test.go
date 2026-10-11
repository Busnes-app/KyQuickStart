package immich

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreationRequiresScopedAbsenceAndHoldsUnverifiedWrites(t *testing.T) {
	for _, mode := range []string{"missing-absence", "foreign-client", "present", "contradictory-absence", "known-refusal", "lost-write", "foreign-receipt", "missing-digest", "missing-user"} {
		t.Run(mode, func(t *testing.T) {
			scope := Binding{Issuer: "https://identity.example.test", ClientID: "photos", Subject: "new-subject"}
			const setupID = "271333e2-c221-4601-b249-563b3d828530"
			writes := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/server/version" {
					json.NewEncoder(w).Encode(map[string]int{"major": 3, "minor": 2, "patch": 0})
					return
				}
				if r.URL.Path != "/api/admin/users/kyquickstart/creation" || r.Header.Get("x-api-key") != "private-fixture-key" {
					t.Error("unexpected native creation path or authority")
					w.WriteHeader(403)
					return
				}
				proof := map[string]any{"contract": creationContract, "setupId": setupID, "issuer": scope.Issuer, "clientId": scope.ClientID, "subject": scope.Subject, "absent": true}
				if r.Method == http.MethodGet {
					if r.URL.Query().Get("setupId") != setupID || r.URL.Query().Get("subject") != scope.Subject || r.URL.Query().Get("issuer") != scope.Issuer || r.URL.Query().Get("clientId") != scope.ClientID {
						t.Error("native absence inspection lost scope")
					}
					switch mode {
					case "missing-absence":
						delete(proof, "absent")
					case "foreign-client":
						proof["clientId"] = "foreign"
					case "present":
						proof["absent"] = false
					case "contradictory-absence":
						proof["userId"] = setupID
					}
				} else {
					writes++
					var body struct {
						SetupID, Issuer, ClientID, Subject string
						Profile                            map[string]string
					}
					if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body.SetupID != setupID || body.Issuer != scope.Issuer || body.ClientID != scope.ClientID || body.Subject != scope.Subject || len(body.Profile) != 2 || body.Profile["name"] != "Daily" || body.Profile["email"] != "daily@example.test" {
						t.Error("native creation request lost ordinary authority or immutable scope")
					}
					if mode == "lost-write" {
						w.WriteHeader(503)
						return
					}
					if mode == "known-refusal" {
						w.WriteHeader(http.StatusConflict)
						return
					}
					proof["absent"], proof["userId"], proof["intentDigest"] = false, setupID, strings.Repeat("a", 64)
					proof["profile"] = map[string]string{"name": "Daily", "email": "daily@example.test"}
					switch mode {
					case "foreign-receipt":
						proof["subject"] = "foreign"
					case "missing-digest":
						delete(proof, "intentDigest")
					case "missing-user":
						delete(proof, "userId")
					}
				}
				json.NewEncoder(w).Encode(proof)
			}))
			defer server.Close()
			c, err := New(server.URL, "private-fixture-key")
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = server.Client().Transport
			b, err := c.CreateDirectoryAccount(context.Background(), scope, setupID, Profile{"Daily", "daily@example.test", false})
			if err == nil || b != (Binding{}) {
				t.Fatal("unverified creation produced a native binding")
			}
			expectedWrites := 1
			switch mode {
			case "missing-absence", "foreign-client", "present", "contradictory-absence":
				expectedWrites = 0
			}
			if writes != expectedWrites {
				t.Fatal("native creation mutation boundary was not exercised", writes)
			}
			if writes == 1 && mode != "known-refusal" && !errors.Is(err, ErrUncertain) {
				t.Fatal("unverified write did not hold reconciliation", err)
			}
			if mode == "known-refusal" && !errors.Is(err, ErrAccountConflict) {
				t.Fatal("known transaction refusal incorrectly held as uncertain", err)
			}
			if mode == "present" && (!errors.Is(err, ErrAccountConflict) || writes != 0) {
				t.Fatal("retained subject authorized adoption", err)
			}
		})
	}
}
