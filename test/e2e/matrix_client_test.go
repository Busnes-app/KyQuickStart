//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Test-only Matrix client. maunium.net/go/mautrix (MPL-2.0), imported only under the e2e tag,
// with its pure-Go goolm crypto (build tag goolm, CGO_ENABLED=0) and an in-memory store. It
// signs in as Element does: MAS's OAuth 2.0 authorization code flow with PKCE, a dynamically
// registered public client and a loopback redirect that is never listened on, MAS sending the
// browser to KyIdentity and back.

const loopbackRedirect = "http://127.0.0.1/callback"

// mas is MAS's OAuth endpoints and the client the test registered there.
type mas struct {
	t                          *testing.T
	ctx                        context.Context
	transport                  http.RoundTripper
	authorize, token, clientID string
}

func newMAS(t *testing.T, ctx context.Context, transport http.RoundTripper) *mas {
	t.Helper()
	c := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	var disc struct {
		Authorization string `json:"authorization_endpoint"`
		Token         string `json:"token_endpoint"`
		Registration  string `json:"registration_endpoint"`
	}
	r, err := c.Get("https://" + messageHosts["auth"] + ":30443/.well-known/openid-configuration")
	if err != nil || json.NewDecoder(r.Body).Decode(&disc) != nil || disc.Registration == "" {
		t.Fatalf("MAS discovery: %v", err)
	}
	r.Body.Close()
	reg, _ := json.Marshal(map[string]any{"client_name": "kyquickstart e2e", "client_uri": "https://" + messageHosts["chat"] + "/",
		"application_type": "native", "redirect_uris": []string{loopbackRedirect}, "token_endpoint_auth_method": "none",
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}})
	r, err = c.Post(disc.Registration, "application/json", bytes.NewReader(reg))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var client struct {
		ClientID string `json:"client_id"`
	}
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 201 || json.Unmarshal(b, &client) != nil || client.ClientID == "" {
		t.Fatalf("MAS client registration: %d %s", r.StatusCode, b)
	}
	return &mas{t: t, ctx: ctx, transport: transport, authorize: disc.Authorization, token: disc.Token, clientID: client.ClientID}
}

type masTokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Expires int    `json:"expires_in"`
}

// grant posts form to the token endpoint and returns its status and body.
func (m *mas) grant(form url.Values) (int, []byte) {
	m.t.Helper()
	form.Set("client_id", m.clientID)
	c := &http.Client{Transport: m.transport, Timeout: 30 * time.Second}
	r, err := c.PostForm(m.token, form)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	return r.StatusCode, b
}

// refresh exchanges a refresh token; status 200 with tokens, or MAS's refusal.
func (m *mas) refresh(refresh string) (int, masTokens, []byte) {
	m.t.Helper()
	code, b := m.grant(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	var tok masTokens
	if code == 200 && (json.Unmarshal(b, &tok) != nil || tok.Access == "") {
		m.t.Fatalf("MAS refresh: %s", b)
	}
	return code, tok, b
}

var (
	upstreamHref = regexp.MustCompile(`href="(/upstream/authorize/[^"]+)"`)
	csrfInput    = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
	usernameIn   = regexp.MustCompile(`name="username"[^>]*value="([^"]*)"`)
)

// signIn signs username in to KyIdentity with password, then runs MAS's authorization flow for
// deviceID. It returns the tokens, or where the flow stopped: "kyidentity:<error>" when
// KyIdentity answered MAS's callback with an OAuth error, otherwise the page MAS ended on.
func (m *mas) signIn(username string, password []byte, deviceID string) (*masTokens, string) {
	m.t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Transport: m.transport, Jar: jar, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, endpoint string, body io.Reader, contentType, csrf string) (*http.Response, []byte) {
		m.t.Helper()
		req, _ := http.NewRequestWithContext(m.ctx, method, endpoint, body)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		r, err := c.Do(req)
		if err != nil {
			m.t.Fatalf("%s %s: %v", method, endpoint, err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		return r, b
	}
	identity := "https://" + catalogIdentityURLHost
	_, b := do("GET", identity+"/api/auth/csrf", nil, "", "")
	var csrf struct {
		CSRFToken string `json:"csrfToken"`
	}
	json.Unmarshal(b, &csrf)
	payload, _ := json.Marshal(map[string]string{"username": username, "password": strings.TrimSpace(string(password))})
	clear(password)
	if r, b := do("POST", identity+"/api/auth/login", bytes.NewReader(payload), "application/json", csrf.CSRFToken); r.StatusCode != 200 {
		return nil, "kyidentity sign-in " + r.Status + ": " + string(b)
	}

	verifier := base64.RawURLEncoding.EncodeToString(id16()) + base64.RawURLEncoding.EncodeToString(id16())
	challenge := sha256.Sum256([]byte(verifier))
	state := hex.EncodeToString(id16())
	q := url.Values{"response_type": {"code"}, "client_id": {m.clientID}, "redirect_uri": {loopbackRedirect}, "state": {state},
		"scope":          {"openid urn:matrix:org.matrix.msc2967.client:api:* urn:matrix:org.matrix.msc2967.client:device:" + deviceID},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	next, _ := url.Parse(m.authorize + "?" + q.Encode())
	method, form := "GET", url.Values(nil)
	for range 20 {
		var body io.Reader
		contentType := ""
		if form != nil {
			body, contentType = strings.NewReader(form.Encode()), "application/x-www-form-urlencoded"
		}
		r, b := do(method, next.String(), body, contentType, "")
		method, form = "GET", nil
		if loc, err := r.Location(); err == nil && r.StatusCode/100 == 3 {
			if e := loc.Query().Get("error"); e != "" && strings.HasPrefix(loc.Path, "/upstream/callback/") {
				return nil, "kyidentity:" + e
			}
			if loc.Host == "127.0.0.1" {
				if loc.Query().Get("state") != state || loc.Query().Get("code") == "" {
					return nil, "MAS redirect " + loc.String()
				}
				code, b := m.grant(url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "redirect_uri": {loopbackRedirect}, "code_verifier": {verifier}})
				var tok masTokens
				if code != 200 || json.Unmarshal(b, &tok) != nil || tok.Access == "" || tok.Refresh == "" {
					m.t.Fatalf("MAS code exchange: %d %s", code, b)
				}
				return &tok, ""
			}
			next = loc
			continue
		}
		page := string(b)
		switch h := upstreamHref.FindStringSubmatch(page); {
		case r.StatusCode == 200 && h != nil:
			next, _ = next.Parse(html.UnescapeString(h[1]))
		case r.StatusCode == 200 && csrfInput.MatchString(page):
			// The upstream registration (the imported localpart) or the consent page.
			form = url.Values{"csrf": {csrfInput.FindStringSubmatch(page)[1]}}
			if strings.Contains(page, `name="action" value="register"`) {
				if strings.Contains(page, "cpd-form-error-message") {
					return nil, next.Path + " registration refused: " + pageText(page)
				}
				form.Set("action", "register")
				form.Set("import_email", "on")
				form.Set("import_display_name", "on")
				if u := usernameIn.FindStringSubmatch(page); u != nil {
					form.Set("username", html.UnescapeString(u[1]))
				}
			}
			method = "POST"
		default:
			return nil, next.Path + " " + r.Status + ": " + pageText(page)
		}
	}
	return nil, "no end to MAS's redirects"
}

var tags = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<[^>]+>`)

// pageText is a page's visible text, squeezed, for a failure message.
func pageText(page string) string {
	s := strings.Join(strings.Fields(html.UnescapeString(tags.ReplaceAllString(page, " "))), " ")
	if len(s) > 400 {
		s = s[:400]
	}
	return s
}

// matrixUser is one person's device: a Matrix client and its Olm machine, held in memory so
// the same device can decrypt history after a restore.
type matrixUser struct {
	t       *testing.T
	ctx     context.Context
	mas     *mas
	c       *mautrix.Client
	state   mautrix.StateStore
	mach    *crypto.OlmMachine
	refresh string
	since   string
}

// signInMatrix signs username in through MAS as a new device and uploads its device keys.
func signInMatrix(t *testing.T, ctx context.Context, m *mas, username string, password []byte) *matrixUser {
	t.Helper()
	device := "KYQE2E" + strings.ToUpper(hex.EncodeToString(id16()[:5]))
	tok, stop := m.signIn(username, password, device)
	if tok == nil {
		t.Fatalf("%s's MAS sign-in stopped at %s", username, stop)
	}
	c, err := mautrix.NewClient("https://"+messageHosts["matrix"]+":30443", "", tok.Access)
	if err != nil {
		t.Fatal(err)
	}
	c.Client = &http.Client{Transport: m.transport, Timeout: 60 * time.Second}
	who, err := c.Whoami(ctx)
	if err != nil || who.DeviceID != id.DeviceID(device) {
		t.Fatalf("%s whoami: %v %+v", username, err, who)
	}
	c.UserID, c.DeviceID = who.UserID, who.DeviceID
	u := &matrixUser{t: t, ctx: ctx, mas: m, c: c, state: mautrix.NewMemoryStateStore(), refresh: tok.Refresh}
	u.mach = crypto.NewOlmMachine(c, nil, crypto.NewMemoryStore(nil), u.state.(crypto.StateStore))
	if err := u.mach.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := u.mach.ShareKeys(ctx, -1); err != nil {
		t.Fatalf("%s device keys: %v", username, err)
	}
	return u
}

// resume gives the same device (and its keys) a fresh sign-in of username.
func (u *matrixUser) resume(username string, password []byte) {
	u.t.Helper()
	tok, stop := u.mas.signIn(username, password, u.c.DeviceID.String())
	if tok == nil {
		u.t.Fatalf("%s's MAS sign-in stopped at %s", username, stop)
	}
	u.c.AccessToken, u.refresh = tok.Access, tok.Refresh
}

// renew refreshes the device's tokens: MAS access tokens live minutes.
func (u *matrixUser) renew() {
	u.t.Helper()
	code, tok, b := u.mas.refresh(u.refresh)
	if code != 200 {
		u.t.Fatalf("%s refresh: %d %s", u.c.UserID, code, b)
	}
	u.c.AccessToken, u.refresh = tok.Access, tok.Refresh
}

// whoami asks Synapse who token is: the status, its errcode and the user ID.
func whoami(t *testing.T, transport http.RoundTripper, token string) (int, string, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", "https://"+messageHosts["matrix"]+":30443/_matrix/client/v3/account/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r, err := (&http.Client{Transport: transport, Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, err.Error(), ""
	}
	defer r.Body.Close()
	var out struct {
		UserID  string `json:"user_id"`
		ErrCode string `json:"errcode"`
	}
	json.NewDecoder(r.Body).Decode(&out)
	return r.StatusCode, out.ErrCode, out.UserID
}

// sync runs one /sync and feeds its to-device events and device lists to the Olm machine.
func (u *matrixUser) sync() *mautrix.RespSync {
	u.t.Helper()
	resp, err := u.c.SyncRequest(u.ctx, 0, u.since, "", false, event.PresenceOffline)
	if err != nil {
		u.t.Fatalf("%s sync: %v", u.c.UserID, err)
	}
	u.mach.ProcessSyncResponse(u.ctx, resp, u.since)
	u.since = resp.NextBatch
	return resp
}

// encrypted records room as encrypted with members joined, as a sync's state would.
func (u *matrixUser) encrypted(room id.RoomID, members ...id.UserID) {
	u.state.SetEncryptionEvent(u.ctx, room, &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1})
	for _, m := range members {
		u.state.SetMembership(u.ctx, room, m, event.MembershipJoin)
	}
}

// send encrypts text with the room's Megolm session, shared first with every member's devices.
func (u *matrixUser) send(room id.RoomID, members []id.UserID, text string) id.EventID {
	u.t.Helper()
	if err := u.mach.ShareGroupSession(u.ctx, room, members); err != nil {
		u.t.Fatalf("share room key: %v", err)
	}
	content, err := u.mach.EncryptMegolmEvent(u.ctx, room, event.EventMessage, &event.MessageEventContent{MsgType: event.MsgText, Body: text})
	if err != nil {
		u.t.Fatal(err)
	}
	resp, err := u.c.SendMessageEvent(u.ctx, room, event.EventEncrypted, content)
	if err != nil {
		u.t.Fatalf("send: %v", err)
	}
	return resp.EventID
}

// read fetches eventID raw, wants it m.room.encrypted with the Megolm algorithm, and returns
// the text goolm decrypts it to, syncing until the room key has arrived.
func (u *matrixUser) read(room id.RoomID, eventID id.EventID) string {
	u.t.Helper()
	evt, err := u.c.GetEvent(u.ctx, room, eventID)
	if err != nil {
		u.t.Fatalf("%s get event: %v", u.c.UserID, err)
	}
	if evt.Type != event.EventEncrypted || !strings.Contains(string(evt.Content.VeryRaw), string(id.AlgorithmMegolmV1)) || strings.Contains(string(evt.Content.VeryRaw), `"body"`) {
		u.t.Fatalf("event %s as stored: %s %s", eventID, evt.Type.Type, evt.Content.VeryRaw)
	}
	evt.RoomID = room
	if err := evt.Content.ParseRaw(evt.Type); err != nil {
		u.t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		dec, err := u.mach.DecryptMegolmEvent(u.ctx, evt)
		if err == nil {
			msg, ok := dec.Content.Parsed.(*event.MessageEventContent)
			if !ok {
				u.t.Fatalf("decrypted %s is %T", eventID, dec.Content.Parsed)
			}
			return msg.Body
		}
		if time.Now().After(deadline) {
			u.t.Fatalf("%s cannot decrypt %s: %v", u.c.UserID, eventID, err)
		}
		u.sync()
	}
}

// joined is the room's joined members as Synapse lists them.
func (u *matrixUser) joined(room id.RoomID) []string {
	u.t.Helper()
	resp, err := u.c.JoinedMembers(u.ctx, room)
	if err != nil {
		u.t.Fatalf("joined members: %v", err)
	}
	var out []string
	for m := range resp.Joined {
		out = append(out, m.String())
	}
	return out
}

// sfuGet asks the console for a LiveKit grant with a Matrix OpenID token, as Element Call
// does through the livekit_service_url.
func sfuGet(t *testing.T, transport http.RoundTripper, room id.RoomID, openID *mautrix.RespOpenIDToken, device string) (int, map[string]string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"room": room, "device_id": device, "openid_token": openID})
	r, err := (&http.Client{Transport: transport, Timeout: 30 * time.Second}).Post("https://"+messageHosts[""]+":30443/api/matrix/rtc/sfu/get", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	out := map[string]string{}
	json.NewDecoder(r.Body).Decode(&out)
	return r.StatusCode, out
}

func nonce() string {
	b := make([]byte, 24)
	rand.Read(b)
	return "nonce-" + hex.EncodeToString(b)
}
