package immich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type nativeReactivationReply struct {
	Revoked           bool
	RevokedAt         *string
	APIKeys, Sessions int64
	Reactivation      *struct {
		Epoch         int64
		ReactivatedAt string
	}
}

// qualifyNativeReactivation runs on a revoked everyday account. It proves the module
// route sets authority before clearing the fence, restores no credential, admits a fresh
// sign-in on the same UUID and leaves revocation repeatable. c and service are distinct
// administrator API keys (owner and service account); old holds the account's
// pre-revocation session tokens and oldKey its personal API key.
func qualifyNativeReactivation(t *testing.T, ctx context.Context, c, service *Client, daily, owner Binding, setupID, ownerSession string, old []string, oldKey string, administrator bool, signin func() (id string, admin bool, token string)) {
	t.Helper()
	send := func(token string, apiKey bool, method, path string, body any) (int, []byte) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		r, _ := http.NewRequestWithContext(ctx, method, c.origin+"/api"+path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if apiKey {
			r.Header.Set("X-Api-Key", token)
		} else {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := c.http.Do(r)
		if err != nil {
			t.Fatal("native reactivation request did not complete")
		}
		defer response.Body.Close()
		reply, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
		return response.StatusCode, reply
	}
	reactivate := func(token string, apiKey bool, id string, body map[string]any) (int, nativeReactivationReply) {
		t.Helper()
		status, raw := send(token, apiKey, http.MethodPost, "/admin/users/"+id+"/kyquickstart/reactivate", body)
		var reply nativeReactivationReply
		if status == http.StatusOK && json.Unmarshal(raw, &reply) != nil {
			t.Fatal("native reactivation reply malformed")
		}
		return status, reply
	}
	dead := func(stage string) {
		t.Helper()
		for _, token := range old {
			if status, _ := send(token, false, http.MethodGet, "/users/me", nil); status != http.StatusUnauthorized {
				t.Fatal("pre-revocation session works after "+stage, status)
			}
		}
		if status, _ := send(oldKey, true, http.MethodGet, "/users/me", nil); status != http.StatusUnauthorized {
			t.Fatal("pre-revocation API key works after "+stage, status)
		}
		if nativeSocketSubscribes(t, ctx, c, oldKey) {
			t.Fatal("pre-revocation API key subscribed a socket after " + stage)
		}
	}
	revoked, err := c.InspectAccess(ctx, daily, setupID)
	if err != nil || !revoked.Revoked {
		t.Fatal("native reactivation precondition is not a revoked account", err)
	}
	profile := map[string]any{"name": "daily", "email": "daily@example.test", "administrator": administrator}
	body := func(changes map[string]any) map[string]any {
		b := map[string]any{"setupId": setupID, "issuer": daily.Issuer, "clientId": daily.ClientID, "subject": daily.Subject, "epoch": 2, "profile": profile}
		for k, v := range changes {
			b[k] = v
		}
		return b
	}
	refusals := []struct {
		token   string
		apiKey  bool
		id      string
		changes map[string]any
		status  int
	}{
		{c.token, true, daily.ID, map[string]any{"epoch": 1}, http.StatusBadRequest},
		{c.token, true, daily.ID, map[string]any{"epoch": 0}, http.StatusBadRequest},
		{c.token, true, daily.ID, map[string]any{"epoch": 1 << 31}, http.StatusBadRequest},
		{c.token, true, daily.ID, map[string]any{"restore": true}, http.StatusBadRequest},
		{c.token, true, daily.ID, map[string]any{"profile": map[string]any{"name": "daily", "email": "Daily@example.test", "administrator": administrator}}, http.StatusBadRequest},
		{c.token, true, daily.ID, map[string]any{"subject": "foreign-subject"}, http.StatusForbidden},
		{c.token, true, daily.ID, map[string]any{"issuer": "https://foreign.example.test"}, http.StatusForbidden},
		{c.token, true, daily.ID, map[string]any{"setupId": "371333e2-c221-4601-b249-563b3d828530"}, http.StatusConflict},
		{c.token, true, daily.ID, map[string]any{"profile": map[string]any{"name": "daily", "email": "owner@example.test", "administrator": administrator}}, http.StatusConflict},
		{ownerSession, false, daily.ID, nil, http.StatusForbidden},
		{c.token, true, owner.ID, map[string]any{"subject": owner.Subject, "profile": map[string]any{"name": "owner", "email": "owner@example.test", "administrator": true}}, http.StatusForbidden},
		{service.token, true, owner.ID, map[string]any{"subject": owner.Subject, "profile": map[string]any{"name": "owner", "email": "owner@example.test", "administrator": true}}, http.StatusConflict},
	}
	for i, refusal := range refusals {
		if status, _ := reactivate(refusal.token, refusal.apiKey, refusal.id, body(refusal.changes)); status != refusal.status {
			t.Fatalf("native reactivation refusal %d answered HTTP %d", i, status)
		}
		if after, err := c.InspectAccess(ctx, daily, setupID); err != nil || after != revoked {
			t.Fatalf("native reactivation refusal %d changed the fence", i)
		}
	}
	status, reply := reactivate(c.token, true, daily.ID, body(nil))
	if status != http.StatusOK || reply.Revoked || reply.RevokedAt != nil || reply.Reactivation == nil || reply.Reactivation.Epoch != 2 || reply.APIKeys != 0 || reply.Sessions != 0 {
		t.Fatal("native reactivation did not clear the fence with zero credentials", status)
	}
	if _, err := time.Parse(time.RFC3339Nano, reply.Reactivation.ReactivatedAt); err != nil {
		t.Fatal("native reactivation time unavailable")
	}
	if repeat, again := reactivate(c.token, true, daily.ID, body(nil)); repeat != http.StatusOK || again.Reactivation == nil || *again.Reactivation != *reply.Reactivation {
		t.Fatal("native reactivation repeat was not idempotent", repeat)
	}
	changed := map[string]any{"name": "daily", "email": "daily@example.test", "administrator": !administrator}
	for _, conflict := range []map[string]any{{"profile": changed}, {"epoch": 4}} {
		if status, _ := reactivate(c.token, true, daily.ID, body(conflict)); status != http.StatusConflict {
			t.Fatal("native reactivation accepted a conflicting repeat", status)
		}
	}
	directory, err := c.InspectDirectory(ctx, daily, setupID)
	if err != nil || directory.Revoked || directory.Profile != (Profile{"daily", "daily@example.test", administrator}) {
		t.Fatal("native reactivation directory receipt does not match the profile", err)
	}
	dead("reactivation")
	id, admin, token := signin()
	if id != daily.ID || admin != administrator {
		t.Fatal("fresh sign-in after reactivation changed identity or authority")
	}
	expected := http.StatusForbidden
	if administrator {
		expected = http.StatusOK
	}
	if status, _ := send(token, false, http.MethodGet, "/admin/users", nil); status != expected {
		t.Fatal("reactivated authority does not follow the profile", status)
	}
	// Positive control: a credential issued after reactivation works and subscribes.
	status, raw := send(token, false, http.MethodPost, "/api-keys", map[string]any{"name": "post-reactivation", "permissions": []string{"all"}})
	var fresh struct{ Secret string }
	if status != http.StatusCreated || json.Unmarshal(raw, &fresh) != nil || !nativeSocketSubscribes(t, ctx, c, fresh.Secret) {
		t.Fatal("post-reactivation credential cannot subscribe; the dead-socket probe is not qualified", status)
	}
	dead("fresh sign-in")
	if c.RevokeAccess(ctx, daily, setupID) != nil {
		t.Fatal("native revocation after reactivation failed")
	}
	again, err := c.InspectAccess(ctx, daily, setupID)
	if err != nil || !again.Revoked || again.RevokedAt == revoked.RevokedAt || again.APIKeys != 0 || again.Sessions != 0 {
		t.Fatal("native revocation after reactivation did not write a new fence", err)
	}
	if status, _ := send(token, false, http.MethodGet, "/users/me", nil); status != http.StatusUnauthorized {
		t.Fatal("post-reactivation session survived the second revocation", status)
	}
	if status, _ := reactivate(c.token, true, daily.ID, body(nil)); status != http.StatusConflict {
		t.Fatal("native reactivation accepted a stale epoch", status)
	}
	status, reply = reactivate(service.token, true, daily.ID, body(map[string]any{"epoch": 4}))
	if status != http.StatusOK || reply.Revoked || reply.Reactivation == nil || reply.Reactivation.Epoch != 4 {
		t.Fatal("native service administrator could not reactivate at the next epoch", status)
	}
	old = append(old, token)
	dead("second reactivation")
	if status, _ := send(fresh.Secret, true, http.MethodGet, "/users/me", nil); status != http.StatusUnauthorized {
		t.Fatal("post-reactivation API key survived the second revocation", status)
	}
	t.Log("native reactivation sets authority before clearing the fence, restores no session/key/socket, admits a fresh sign-in on the same UUID, refuses stale/conflicting epochs and keeps revocation repeatable")
}

// nativeSocketSubscribes reports whether key completes a socket.io subscription.
func nativeSocketSubscribes(t *testing.T, ctx context.Context, c *Client, key string) bool {
	t.Helper()
	dialer := websocket.Dialer{TLSClientConfig: c.http.Transport.(*http.Transport).TLSClientConfig.Clone(), HandshakeTimeout: 5 * time.Second}
	socket, _, err := dialer.DialContext(ctx, strings.Replace(c.origin, "https://", "wss://", 1)+"/api/socket.io/?EIO=4&transport=websocket", http.Header{"X-Api-Key": []string{key}})
	if err != nil {
		return false
	}
	defer socket.Close()
	socket.SetReadLimit(8192)
	socket.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, frame, err := socket.ReadMessage(); err != nil || !bytes.HasPrefix(frame, []byte("0{")) || socket.WriteMessage(websocket.TextMessage, []byte("40")) != nil {
		return false
	}
	for {
		_, frame, err := socket.ReadMessage()
		if err != nil {
			return false
		}
		if bytes.HasPrefix(frame, []byte("40{")) {
			// A subscription must survive the server's post-join recheck.
			socket.SetReadDeadline(time.Now().Add(2 * time.Second))
			for {
				_, frame, err := socket.ReadMessage()
				var timeout net.Error
				if err != nil {
					return errors.As(err, &timeout) && timeout.Timeout()
				}
				if bytes.HasPrefix(frame, []byte("41")) || bytes.HasPrefix(frame, []byte("44")) {
					return false
				}
			}
		}
		if bytes.HasPrefix(frame, []byte("44")) || bytes.HasPrefix(frame, []byte("41")) {
			return false
		}
	}
}
