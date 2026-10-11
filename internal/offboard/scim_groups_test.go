package offboard

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func groupReceiverFixture(t *testing.T) (*Store, *Target, *Receiver, string) {
	t.Helper()
	s := openStore(t, stateDir(t))
	t.Cleanup(func() { s.Close() })
	target, _ := groupFixture()
	target.spec.Groups = &GroupConfig{OrganizationID: 12, Organization: "owned-org", Permission: "read", Permissions: map[string]string{"repo.code": "read"}}
	current := []Binding{}
	label := ""
	target.groupAbsent = func(context.Context, GroupBinding) error { return nil }
	target.groupCreate = func(_ context.Context, g GroupBinding) (GroupBinding, error) { g.TeamID = 44; return g, nil }
	target.groupReplace = func(_ context.Context, _ GroupBinding, _, desired []Binding) error {
		current = append([]Binding{}, desired...)
		return nil
	}
	target.groupLabel = func(_ context.Context, _ GroupBinding, name string) error { label = name; return nil }
	target.groupRead = func(_ context.Context, _ GroupBinding, _ []Binding) (string, []Binding, error) {
		return label, append([]Binding{}, current...), nil
	}
	b := directoryBinding()
	id, err := s.Bind(b)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewReceiver(s, target, strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	return s, target, r, id
}
func groupRequest(r *Receiver, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/scim+json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
func groupBody(name, member string) string {
	members := []any{}
	if member != "" {
		members = append(members, map[string]string{"value": member})
	}
	body, _ := json.Marshal(map[string]any{"schemas": []string{groupSchema}, "externalId": "source-group", "displayName": name, "members": members})
	return string(body)
}

func TestSCIMGroupCreateRenameDeleteRetainsOwnedReceipts(t *testing.T) {
	s, target, r, account := groupReceiverFixture(t)
	collection := r.base + "/Groups"
	query := url.Values{"filter": {`externalId eq "source-group"`}, "startIndex": {"1"}, "count": {"2"}}
	if w := groupRequest(r, "GET", collection+"?"+query.Encode(), ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"totalResults":0`) {
		t.Fatal("native absence not projected", w.Code)
	}
	w := groupRequest(r, "POST", collection, groupBody("Engineering", account))
	var created struct {
		ID         string `json:"id"`
		ExternalID string `json:"externalId"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.ID != groupID(target.groupScope("source-group")) || created.ExternalID != "source-group" {
		t.Fatal("verified group not created", w.Code)
	}
	path := collection + "/" + created.ID
	if w := groupRequest(r, "GET", path, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Engineering") {
		t.Fatal("native group read failed", w.Code)
	}
	if w := groupRequest(r, "PUT", path, groupBody("Renamed engineering", account)); w.Code != 204 {
		t.Fatal("group rename failed", w.Code)
	}
	if w := groupRequest(r, "GET", path, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Renamed engineering") {
		t.Fatal("native label change not observed", w.Code)
	}
	if w := groupRequest(r, "DELETE", path, ""); w.Code != 204 {
		t.Fatal("group deletion not completed", w.Code)
	}
	if w := groupRequest(r, "GET", path, ""); w.Code != 404 {
		t.Fatal("retired group still active", w.Code)
	}
	if w := groupRequest(r, "PUT", path, groupBody("Resurrection", account)); w.Code != 409 {
		t.Fatal("retired group resurrected", w.Code)
	}
	if w := groupRequest(r, "DELETE", path, ""); w.Code != 204 {
		t.Fatal("retired group repeat failed", w.Code)
	}
	target.groupRead = func(_ context.Context, _ GroupBinding, owned []Binding) (string, []Binding, error) {
		return "", owned, nil
	}
	if w := groupRequest(r, "DELETE", path, ""); w.Code != 503 {
		t.Fatal("native member drift acknowledged as retirement", w.Code)
	}
	if _, err := s.GroupBinding(created.ID); err != nil {
		t.Fatal("logical deletion removed owned team receipt", err)
	}
	j, err := s.membershipJob(created.ID)
	if err != nil || !j.Scope.Deleted || len(j.Scope.Members) != 0 {
		t.Fatal("retirement not proved", err)
	}
}

func TestSCIMGroupInputAndUnknownOutcomeBoundaries(t *testing.T) {
	for _, mode := range []string{"default", "foreign-member", "missing-members", "duplicate-member", "extra-authority", "native-uncertain", "label-uncertain"} {
		t.Run(mode, func(t *testing.T) {
			s, target, r, account := groupReceiverFixture(t)
			body := groupBody("Engineering", account)
			expected := 400
			calls := 0
			original := target.groupReplace
			target.groupReplace = func(ctx context.Context, g GroupBinding, owned, desired []Binding) error {
				calls++
				if mode == "native-uncertain" {
					return ErrGroupMembershipUncertain
				}
				return original(ctx, g, owned, desired)
			}
			switch mode {
			case "default":
				target.spec.Groups = nil
				expected = 403
			case "foreign-member":
				body = groupBody("Engineering", strings.Repeat("f", 64))
				expected = 409
			case "missing-members":
				body = `{"schemas":["` + groupSchema + `"],"externalId":"source-group","displayName":"Engineering"}`
			case "duplicate-member":
				body = strings.Replace(body, `"members":[`, `"members":[{"value":"`+account+`"},`, 1)
			case "extra-authority":
				body = strings.TrimSuffix(body, "}") + `,"organization_id":99}`
			case "native-uncertain":
				expected = 503
			case "label-uncertain":
				expected = 503
				target.groupLabel = func(context.Context, GroupBinding, string) error { return errors.New("native label lost reply") }
			}
			w := groupRequest(r, "POST", r.base+"/Groups", body)
			if w.Code != expected {
				t.Fatal("group boundary status", w.Code, expected)
			}
			if expected != 503 && calls != 0 {
				t.Fatal("invalid input reached native membership")
			}
			if expected == 503 {
				id := groupID(target.groupScope("source-group"))
				j, err := s.membershipJob(id)
				if err != nil || j.Status != "uncertain" {
					t.Fatal("unknown group outcome not held", err)
				}
				if w := groupRequest(r, "PUT", r.base+"/Groups/"+id, groupBody("Later", account)); w.Code != 503 || calls != 1 {
					t.Fatal("new label released native hold", w.Code, calls)
				}
				if w := groupRequest(r, "GET", r.base+"/Groups/"+id, ""); w.Code != 503 {
					t.Fatal("read cleared native hold", w.Code)
				}
			}
		})
	}
}
