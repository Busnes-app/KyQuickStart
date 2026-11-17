//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// seedCalendarUpgrade uses the old product's ordinary API, then checks the same
// event through a freshly signed-in candidate session. No attendees or mail are sent.
func seedCalendarUpgrade(t *testing.T, ctx context.Context, old *http.Client) (func(*http.Client), func(*http.Client)) {
	t.Helper()
	origin := "https://" + catalogCalendarURLHost
	send := func(client *http.Client, method, path, body, etag string, want int, out any) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, origin+path, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		u, _ := url.Parse(origin)
		for _, cookie := range client.Jar.Cookies(u) {
			if cookie.Name == "ky_csrf" {
				req.Header.Set("X-CSRF-Token", cookie.Value)
			}
		}
		if etag != "" {
			req.Header.Set("If-Match", `"`+etag+`"`)
		}
		r, err := client.Do(req)
		if err != nil {
			t.Fatal("calendar content request failed", err)
		}
		defer r.Body.Close()
		b, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil || r.StatusCode != want {
			t.Fatalf("calendar content %s %s: HTTP %d, read error %v", method, path, r.StatusCode, err)
		}
		if out != nil {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatal("calendar content response invalid", err)
			}
		}
	}
	var cal struct{ ID string }
	send(old, "POST", "/api/calendars", `{"name":"Major upgrade calendar","color":"#d05b20"}`, "", http.StatusCreated, &cal)
	if cal.ID == "" {
		t.Fatal("old Calendar created no calendar ID")
	}
	const event = `{"title":"Major migration — café","location":"Room 7","description":"Retain this ordinary event.\nSecond line.","start":"2026-11-18T09:00:00Z","end":"2026-11-18T10:00:00Z","zone":"UTC"}`
	var made struct {
		CalendarID string `json:"calendar_id"`
		UID, ETag  string
	}
	send(old, "POST", "/api/calendars/"+cal.ID+"/events", event, "", http.StatusCreated, &made)
	if made.CalendarID != cal.ID || made.UID == "" || made.ETag == "" {
		t.Fatal("old Calendar created no event identity/ETag")
	}
	type eventView struct {
		CalendarID                                          string `json:"calendar_id"`
		UID, ETag, Title, Location, Description, Start, End string
		Editable                                            bool
	}
	query := "/api/events?start=2026-11-18T00:00:00Z&end=2026-11-19T00:00:00Z&tz=UTC&calendar=" + url.QueryEscape(cal.ID)
	read := func(client *http.Client) eventView {
		t.Helper()
		var events []eventView
		send(client, "GET", query, "", "", http.StatusOK, &events)
		if len(events) != 1 {
			t.Fatalf("calendar content: want one seeded event, got %d", len(events))
		}
		return events[0]
	}
	before := read(old)
	if before.CalendarID != cal.ID || before.UID != made.UID || before.ETag != made.ETag || before.Title != "Major migration — café" || before.Location != "Room 7" || before.Description != "Retain this ordinary event.\nSecond line." || before.Start != "2026-11-18T09:00:00Z" || before.End != "2026-11-18T10:00:00Z" || !before.Editable {
		t.Fatal("old Calendar did not read back the seeded event exactly")
	}
	const edited = `{"title":"Edited after candidate upgrade","location":"Room 7","description":"Retain this ordinary event.\nSecond line.","start":"2026-11-18T09:00:00Z","end":"2026-11-18T10:00:00Z","zone":"UTC"}`
	var afterUpgrade eventView
	verifyUpgrade := func(candidate *http.Client) {
		t.Helper()
		if after := read(candidate); after != before {
			t.Fatal("candidate Calendar changed existing event content, identity, ETag or access")
		}
		var changed struct{ ETag string }
		send(candidate, "PUT", "/api/events/"+cal.ID+"/"+made.UID, edited, made.ETag, http.StatusOK, &changed)
		after := read(candidate)
		if changed.ETag == "" || changed.ETag == made.ETag || after.ETag != changed.ETag || after.Title != "Edited after candidate upgrade" {
			t.Fatal("candidate Calendar could not edit preserved event with its old ETag")
		}
		afterUpgrade = after
		after.Title, after.ETag = before.Title, before.ETag
		if after != before {
			t.Fatal("candidate Calendar edit changed unrelated event content or access")
		}
		t.Log("real previous-image Calendar event retained text/times/IDs/ETag and accepted a candidate If-Match edit")
	}
	verifyRestore := func(restored *http.Client) {
		t.Helper()
		got := read(restored)
		if afterUpgrade.ETag == "" || got.ETag == "" {
			t.Fatal("no upgraded/restored Calendar ETag")
		}
		etag := got.ETag
		// Native restore intentionally rotates the sync epoch; content/identity
		// must survive even if an epoch-derived ETag changes.
		got.ETag = afterUpgrade.ETag
		if got != afterUpgrade {
			t.Fatal("native restore changed the everyday event, identifiers or edit access")
		}
		var changed struct{ ETag string }
		body := strings.Replace(edited, "Edited after candidate upgrade", "Edited after native capsule restore", 1)
		send(restored, "PUT", "/api/events/"+cal.ID+"/"+made.UID, body, etag, http.StatusOK, &changed)
		got = read(restored)
		if got.Title != "Edited after native capsule restore" || changed.ETag == "" || changed.ETag == etag || got.ETag != changed.ETag {
			t.Fatal("restored event did not accept an If-Match edit")
		}
		got.Title, got.ETag = afterUpgrade.Title, afterUpgrade.ETag
		if got != afterUpgrade {
			t.Fatal("restored Calendar edit changed unrelated event fields")
		}
		t.Log("native Calendar restore retained everyday event content/IDs/access and accepted a fresh If-Match edit")
	}
	return verifyUpgrade, verifyRestore

}
