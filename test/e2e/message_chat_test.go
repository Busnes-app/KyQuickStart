//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/kube"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// messageChat is the stack's chat as its people use it: the owner's everyday account (an
// installer-set address), member (an address verified by mail), their encrypted room and the
// owner's admins on KyIdentity and the console.
type messageChat struct {
	t         *testing.T
	ctx       context.Context
	transport *http.Transport
	connected *kube.Client
	msg       messageTarget
	mail      mailpit
	mas       *mas
	admin     *identityAdmin
	console   *http.Client
	owner     *matrixUser
	member    *matrixUser
	memberID  string
	memberPW  string
	chatOnly  *masTokens // a person assigned to the chat client alone, while signed in
	chatSince time.Time
	room      id.RoomID
	nonce     string // the owner's, sent before the backup
	event     id.EventID
	secrets   map[string]string
}

const (
	memberAddress   = "member@" + mailDomain
	chatOnlyAddress = "chatonly@" + mailDomain
	leaverAddress   = "leaver@" + mailDomain
	earlyAddress    = "early@" + mailDomain
)

func mxid(localpart string) string { return "@" + localpart + ":" + messageHosts["server_name"] }

// consoleGet reads path as the console's admin.
func (c *messageChat) consoleGet(path string, v any) {
	c.t.Helper()
	r, err := c.console.Get("https://" + messageHosts[""] + ":30443" + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if r.StatusCode != 200 || json.Unmarshal(b, v) != nil {
		c.t.Fatalf("console %s: %d %.300s", path, r.StatusCode, b)
	}
}

// chatUsers is MAS's users as the console's admin sees them: username → status.
func (c *messageChat) chatUsers() map[string]string {
	c.t.Helper()
	var list struct {
		Users []struct{ Username, Status string }
	}
	c.consoleGet("/api/admin/matrix/users?limit=100", &list)
	out := map[string]string{}
	for _, u := range list.Users {
		out[u.Username] = u.Status
	}
	return out
}

// directory is username's KyIdentity status in the console's directory, "" when MAS has no
// such user.
func (c *messageChat) directory(username string) string {
	c.t.Helper()
	var list struct {
		Users []struct {
			Username   string
			KyIdentity string `json:"kyidentity"`
		}
	}
	c.consoleGet("/api/admin/matrix/users?limit=100", &list)
	for _, u := range list.Users {
		if u.Username == username {
			return u.KyIdentity
		}
	}
	return ""
}

// audited waits until the console's audit has action for target and returns its newest
// row's time, zero when there is none.
func (c *messageChat) audited(action, target string, within time.Duration) time.Time {
	c.t.Helper()
	for deadline := time.Now().Add(within); ; time.Sleep(2 * time.Second) {
		var page struct {
			Records []struct {
				At                      time.Time
				Action, Target, Details string
			}
		}
		c.consoleGet("/api/admin/audit?kind=matrix&limit=100", &page)
		for _, r := range page.Records {
			if r.Action == action && r.Target == target {
				c.t.Logf("console audit: %s %s %s %s", r.At.Format(time.RFC3339), r.Action, r.Target, r.Details)
				return r.At
			}
		}
		if time.Now().After(deadline) {
			return time.Time{}
		}
	}
}

// diagnose logs KyIdentity's offboarding record of user and the console's sweep state.
func (c *messageChat) diagnose(user string) {
	c.t.Helper()
	_, b := c.admin.send("GET", "/api/admin/users/"+user+"/offboarding", nil, "")
	c.t.Logf("KyIdentity offboarding of %s: %.1500s", user, b)
	var systems struct{ Systems []struct{ ID, Name string } }
	_, b = c.admin.send("GET", "/api/admin/systems", nil, "")
	json.Unmarshal(b, &systems)
	for _, sys := range systems.Systems {
		for _, sub := range []string{"/deliveries", "/provisioning"} {
			_, b := c.admin.send("GET", "/api/admin/systems/"+sys.ID+sub, nil, "")
			c.t.Logf("KyIdentity system %s %s: %.3000s", sys.Name, sub, b)
		}
	}
	if len(systems.Systems) == 0 {
		c.t.Logf("KyIdentity systems: %.1000s", b)
	}
	for _, path := range []string{"/api/admin/matrix/sync-status", "/api/admin/matrix/users?limit=100", "/api/admin/audit?limit=30"} {
		if r, err := c.console.Get("https://" + messageHosts[""] + ":30443" + path); err == nil {
			b, _ := io.ReadAll(io.LimitReader(r.Body, 8<<10))
			r.Body.Close()
			c.t.Logf("console %s: %d %.3000s", path, r.StatusCode, b)
		}
	}
}

func (c *messageChat) everyday() []byte {
	return ownerPassword(c.t, c.ctx, c.connected, "everyday.password")
}

func (c *messageChat) device() string { return "KYQE2E" + strings.ToUpper(randomPassword()[:8]) }

// signIns proves chat sign-in by both verified-address paths (AC18, AC19).
func (c *messageChat) signIns() {
	t := c.t
	// AC18: the owner's everyday account; KyIdentity set its address, verified, at setup.
	c.owner = signInMatrix(t, c.ctx, c.mas, "prepared-user", c.everyday())
	if c.owner.c.UserID.String() != mxid("prepared-user") {
		t.Fatalf("owner's Matrix ID %s", c.owner.c.UserID)
	}
	if v := c.verifiedAt("prepared-user"); v == "" {
		t.Fatal("KyIdentity reports prepared-user's installer-set address unverified")
	} else {
		t.Logf("AC18: prepared-user is %s, its installer-set address verified at %s, no mail sent", c.owner.c.UserID, v)
	}

	// AC19: member, created and assigned to both clients through the admin API.
	c.memberPW = randomPassword()
	c.memberID = c.admin.createPerson("member", memberAddress, c.memberPW)
	for _, client := range []string{"kymessage", "kymessage-chat"} {
		c.admin.assign(c.admin.app(client), c.memberID)
	}
	c.secrets["member's first password"] = c.memberPW
	tok, stop := c.mas.signIn("member", []byte(c.memberPW), c.device())
	if tok != nil || strings.HasPrefix(stop, "kyidentity") {
		t.Fatalf("member before verification: tokens %v, stopped at %q", tok != nil, stop)
	}
	t.Logf("AC19: member, address unverified, signed in to KyIdentity; MAS stopped at %s", stop)
	if s, ok := c.chatUsers()["member"]; ok || c.verifiedAt("member") != "" {
		t.Fatalf("member before verification: MAS user %q, KyIdentity verified %q", s, c.verifiedAt("member"))
	}
	c.memberPW = c.verify("member", c.memberID, memberAddress)
	c.secrets["member's password"] = c.memberPW
	c.member = signInMatrix(t, c.ctx, c.mas, "member", []byte(c.memberPW))
	if c.member.c.UserID.String() != mxid("member") {
		t.Fatalf("member's Matrix ID %s", c.member.c.UserID)
	}
	t.Logf("AC19: member's address verified at %s by the mailed link; chat ID %s", c.verifiedAt("member"), c.member.c.UserID)
	// member is not the setup's; the sweeps after this one (AC22, AC25) find it still active.
	if s := c.chatUsers()["member"]; s != "active" {
		t.Fatalf("member in MAS is %q after its sign-in", s)
	}
}

// verify mails username a KyIdentity link through Mailpit and redeems it with a new password,
// which verifies its address.
func (c *messageChat) verify(username, userID, address string) string {
	t := c.t
	t.Helper()
	var link struct{ Delivery string }
	if json.Unmarshal(c.admin.change("POST", "/api/admin/users/"+userID+"/reset-link", map[string]bool{"send": true}), &link) != nil || link.Delivery != "email" {
		t.Fatalf("reset link delivery %q", link.Delivery)
	}
	token := c.mail.resetLink(t, address)
	pw := randomPassword()
	if code, b := c.identityPost("/api/auth/password/reset", map[string]string{"token": token, "password": pw}); code != 200 {
		t.Fatalf("redeem the mailed link: %d %s", code, b)
	}
	if c.verifiedAt(username) == "" {
		t.Fatalf("KyIdentity did not verify %s's address from the mailed link", username)
	}
	return pw
}

// chatOnlySignIn: a person assigned to "KyMessages chat" alone, of whom the console's directory
// never hears, signs in to chat. chatOnlyReached later proves the sweep reaches them.
func (c *messageChat) chatOnlySignIn() {
	t := c.t
	pw := randomPassword()
	uid := c.admin.createPerson("chatonly", chatOnlyAddress, pw)
	c.secrets["chatonly's first password"] = pw
	c.admin.assign(c.admin.app("kymessage-chat"), uid)
	pw = c.verify("chatonly", uid, chatOnlyAddress)
	c.secrets["chatonly's password"] = pw
	tok, stop := c.mas.signIn("chatonly", []byte(pw), c.device())
	if tok == nil {
		t.Logf("chat-only: chatonly, assigned to KyMessages chat alone, was refused chat: %.200s", stop)
		return
	}
	c.chatOnly, c.chatSince = tok, time.Now()
	code, _, user := whoami(t, c.transport, tok.Access)
	t.Logf("chat-only: chatonly, assigned to KyMessages chat alone and verified, signed in to chat: whoami %d %s, MAS status %q", code, user, c.chatUsers()["chatonly"])
}

// chatOnlyReached proves the console's sweep locks chatonly, whom it does not know, and ends
// its session: at its 5-minute run, or at the first accepted directory event (the owner's
// disable, t0, wakes one).
func (c *messageChat) chatOnlyReached(t0 time.Time) {
	t := c.t
	if c.chatOnly == nil {
		return
	}
	target := mxid("chatonly")
	for deadline := c.chatSince.Add(6 * time.Minute); ; time.Sleep(time.Second) {
		code, errcode, _ := whoami(t, c.transport, c.chatOnly.Access)
		if code == 401 && errcode == "M_UNKNOWN_TOKEN" {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("chat-only upstream: chatonly's token still answers %d %s %s after its sign-in, past the sweep's 5-minute run", code, errcode, time.Since(c.chatSince).Round(time.Second))
			return
		}
	}
	if code, _, b := c.mas.refresh(c.chatOnly.Refresh); code == 200 {
		t.Error("MAS refreshed chatonly's ended session")
	} else {
		t.Logf("chat-only: MAS refuses chatonly's refresh token: %d %.120s", code, b)
	}
	locked := c.audited("matrix.lock", target, 30*time.Second)
	if locked.IsZero() || c.audited("matrix.session_end", target, 0).IsZero() {
		t.Fatal("no matrix.lock and matrix.session_end for chatonly")
	}
	if tok, stop := c.mas.signIn("chatonly", []byte(c.secrets["chatonly's password"]), c.device()); tok != nil {
		t.Error("the locked chatonly signed in to chat again")
	} else {
		t.Logf("chat-only: chatonly's new sign-in stopped at %.120s", stop)
	}
	t.Logf("chat-only: the sweep locked chatonly and ended its session %s after its sign-in (%s relative to the owner's disable, t0); its token is 401",
		locked.Sub(c.chatSince).Round(time.Second), locked.Sub(t0).Round(time.Second))
}

// verifiedAt is KyIdentity's emailVerifiedAt for username, "" when unverified.
func (c *messageChat) verifiedAt(username string) string {
	c.t.Helper()
	var list struct {
		Users []struct {
			Username        string
			EmailVerifiedAt string `json:"emailVerifiedAt"`
		}
	}
	c.admin.get("/api/admin/users", &list)
	for _, u := range list.Users {
		if u.Username == username {
			return u.EmailVerifiedAt
		}
	}
	c.t.Fatalf("KyIdentity has no user %s", username)
	return ""
}

// identityPost posts body to KyIdentity anonymously, with a fresh CSRF pair.
func (c *messageChat) identityPost(path string, body any) (int, []byte) {
	c.t.Helper()
	anon := newAnonIdentity(c.t, c.ctx, c.transport)
	raw, _ := json.Marshal(body)
	return anon.send("POST", path, raw, "")
}

func newAnonIdentity(t *testing.T, ctx context.Context, transport http.RoundTripper) *identityAdmin {
	t.Helper()
	a := &identityAdmin{t: t, ctx: ctx, c: newJarClient(transport)}
	if code, b := a.send("GET", "/api/auth/csrf", nil, ""); code != 200 {
		t.Fatalf("KyIdentity CSRF: %d %s", code, b)
	}
	return a
}

// encryptedRoom proves end-to-end encryption between the two (AC20).
func (c *messageChat) encryptedRoom() {
	t, ctx := c.t, c.ctx
	sk := ""
	resp, err := c.owner.c.CreateRoom(ctx, &mautrix.ReqCreateRoom{Preset: "private_chat", Name: "e2e", Invite: []id.UserID{c.member.c.UserID},
		InitialState: []*event.Event{{Type: event.StateEncryption, StateKey: &sk, Content: event.Content{Parsed: &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}}}}})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	c.room = resp.RoomID
	if _, err := c.member.c.JoinRoomByID(ctx, c.room); err != nil {
		t.Fatalf("member joins: %v", err)
	}
	var enc event.EncryptionEventContent
	if err := c.member.c.StateEvent(ctx, c.room, event.StateEncryption, "", &enc); err != nil || enc.Algorithm != id.AlgorithmMegolmV1 {
		t.Fatalf("room encryption state: %v %+v", err, enc)
	}
	members := []id.UserID{c.owner.c.UserID, c.member.c.UserID}
	for _, u := range []*matrixUser{c.owner, c.member} {
		u.encrypted(c.room, members...)
		u.sync()
	}
	c.nonce = nonce()
	c.event = c.owner.send(c.room, members, c.nonce)
	if got := c.member.read(c.room, c.event); got != c.nonce {
		t.Fatalf("member decrypted %q, want the owner's nonce", got)
	}
	back := nonce()
	ev := c.member.send(c.room, members, back)
	if got := c.owner.read(c.room, ev); got != back {
		t.Fatalf("owner decrypted %q, want member's nonce", got)
	}
	t.Logf("AC20: room %s (%s); each read the other's m.room.encrypted event and decrypted the exact nonce", c.room, enc.Algorithm)
	dump := docker(t, "exec", "kyq-kymessage-postgres-1", "pg_dumpall", "-U", "postgres")
	for _, n := range []string{c.nonce, back} {
		if strings.Contains(dump, n) {
			t.Fatal("a nonce is in pg_dumpall's output")
		}
		g := exec.Command("docker", "exec", c.msg.name, "grep", "-rqF", "--", n, c.msg.root+"/kymessage/media")
		if err := g.Run(); g.ProcessState == nil || g.ProcessState.ExitCode() != 1 {
			t.Fatalf("grep the media directory for a nonce: %v", err)
		}
	}
	t.Logf("AC20: neither nonce in pg_dumpall (%d bytes) nor under media", len(dump))

	tok, stop := c.mas.signIn("prepared-admin", ownerPassword(t, ctx, c.connected, "admin.password"), c.device())
	if tok != nil || stop != "kyidentity:access_denied" {
		t.Fatalf("the dedicated admin's chat sign-in: tokens %v, stopped at %q", tok != nil, stop)
	}
	if s, ok := c.chatUsers()["prepared-admin"]; ok {
		t.Fatalf("MAS has the dedicated admin (%s)", s)
	}
	t.Log("AC20: KyIdentity answered MAS access_denied for the dedicated admin; MAS has no such user")
}

// calls proves call authorisation through the console and LiveKit's signalling (AC21).
func (c *messageChat) calls() *mautrix.RespOpenIDToken {
	t := c.t
	openID, err := c.owner.c.RequestOpenIDToken(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	code, grant := sfuGet(t, c.transport, c.room, openID, c.owner.c.DeviceID.String())
	if rtc := "wss://" + messageHosts["rtc"]; code != 200 || grant["jwt"] == "" || grant["url"] != rtc+":30443" && grant["url"] != rtc {
		t.Fatalf("sfu/get: %d url %q jwt %v", code, grant["url"], grant["jwt"] != "")
	}
	r, err := (&http.Client{Transport: c.transport, Timeout: 30 * time.Second}).Get("https://" + messageHosts["rtc"] + ":30443/rtc/validate?access_token=" + url.QueryEscape(grant["jwt"]))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("LiveKit /rtc/validate through the edge: %d", r.StatusCode)
	}
	bogus := &mautrix.RespOpenIDToken{AccessToken: "not-a-token", MatrixServerName: messageHosts["server_name"]}
	if code, out := sfuGet(t, c.transport, c.room, bogus, "X"); code == 200 {
		t.Fatalf("sfu/get without a valid OpenID token: %d %v", code, out)
	} else {
		t.Logf("AC21: grant for %s (url %s), validated by LiveKit through the edge; a forged OpenID token gets %d %s", c.owner.c.UserID, grant["url"], code, out["errcode"])
	}
	return openID
}

// cut measures, from t0, when Synapse refuses token (401 M_UNKNOWN_TOKEN) and MAS refuses
// refresh; both must be within 60 s (AC22).
func (c *messageChat) cut(who, userID string, t0 time.Time, token, refresh, event string) time.Duration {
	t := c.t
	t.Helper()
	var denied, refused time.Duration
	for logged := false; denied == 0 || refused == 0; time.Sleep(250 * time.Millisecond) {
		if denied == 0 {
			if code, errcode, _ := whoami(t, c.transport, token); code == 401 && errcode == "M_UNKNOWN_TOKEN" {
				denied = time.Since(t0)
			}
		}
		if refused == 0 {
			if code, tok, _ := c.mas.refresh(refresh); code == 200 {
				refresh = tok.Refresh
			} else if code >= 400 && code < 500 {
				refused = time.Since(t0)
			}
		}
		if !logged && time.Since(t0) > 60*time.Second {
			c.diagnose(userID)
			logged = true
		}
		if time.Since(t0) > 3*time.Minute {
			t.Fatalf("%s: 3 minutes after %s, token refused %v, refresh refused %v", who, event, denied != 0, refused != 0)
		}
	}
	t.Logf("AC22: %s's access token got 401 M_UNKNOWN_TOKEN %s after %s; MAS refused its refresh token %s after", who,
		denied.Round(100*time.Millisecond), event, refused.Round(100*time.Millisecond))
	if denied > 60*time.Second || refused > 60*time.Second {
		t.Errorf("AC22: %s's chat outlived the 60 s bound", who)
	}
	target := mxid(who)
	if c.audited("matrix.lock", target, 30*time.Second).IsZero() || c.audited("matrix.session_end", target, 0).IsZero() {
		t.Errorf("AC22: no matrix.lock and matrix.session_end in the console's audit for %s", who)
	}
	return max(denied, refused)
}

// offboarding disables the owner's everyday account in KyIdentity and measures what ends its
// chat (AC22), then re-enables it.
func (c *messageChat) offboarding(openID *mautrix.RespOpenIDToken) {
	t := c.t
	c.owner.renew()
	token := c.owner.c.AccessToken
	renewed := time.Now()
	ownerID := c.admin.findUser("prepared-user")
	target := mxid("prepared-user")
	t0 := time.Now()
	c.admin.change("PUT", "/api/admin/users/"+ownerID, map[string]string{"status": "disabled"})
	c.cut("prepared-user", ownerID, t0, token, c.owner.refresh, "the disable")
	if tok, stop := c.mas.signIn("prepared-user", c.everyday(), c.device()); tok != nil {
		t.Fatal("the disabled owner signed in to chat")
	} else {
		t.Logf("AC22: the disabled owner's new chat sign-in stopped at %.120s", stop)
	}
	if code, out := sfuGet(t, c.transport, c.room, openID, c.owner.c.DeviceID.String()); code == 200 {
		t.Fatal("sfu/get granted the disabled owner a call")
	} else {
		t.Logf("AC22: sfu/get with the owner's last OpenID token: %d %s %s", code, out["errcode"], out["error"])
	}
	// member, whom setup did not create, kept chat through the sweep the disable woke.
	c.member.renew()
	if code, _, user := whoami(t, c.transport, c.member.c.AccessToken); code != 200 || user != mxid("member") {
		t.Fatalf("member's token after the owner's disable: %d %s, MAS status %q", code, user, c.chatUsers()["member"])
	}
	if s := c.chatUsers()["member"]; s != "active" {
		t.Fatalf("member in MAS is %q after the owner's offboarding", s)
	}
	t.Log("AC22: member's token still works and member is active in MAS")
	c.chatOnlyReached(t0)

	c.admin.change("PUT", "/api/admin/users/"+ownerID, map[string]string{"status": "active"})
	if c.audited("matrix.unlock", target, 90*time.Second).IsZero() {
		t.Fatal("no matrix.unlock for the re-enabled owner")
	}
	c.owner.resume("prepared-user", c.everyday())
	if code, _, user := whoami(t, c.transport, c.owner.c.AccessToken); code != 200 || user != target {
		t.Fatalf("the re-enabled owner's fresh sign-in: %d %s", code, user)
	}
	// Past Synapse's 2-minute introspection cache, inside the token's 5-minute life, though the
	// fresh sign-in reuses the old device ID.
	reenabled := time.Now()
	for {
		if code, errcode, _ := whoami(t, c.transport, token); code != 401 {
			t.Errorf("AC22: the owner's old access token answers %d %s %s after the re-enable", code, errcode, time.Since(reenabled).Round(time.Second))
			break
		} else if time.Since(reenabled) > 130*time.Second || time.Since(renewed) > 290*time.Second {
			t.Logf("AC22: the owner's old access token stayed 401 %s for %s after the re-enable (issued %s before)", errcode, time.Since(reenabled).Round(time.Second), time.Since(renewed).Round(time.Second))
			break
		}
		time.Sleep(2 * time.Second)
	}
}

// unassign measures what removing someone's assignments ends: "KyMessages chat" alone stops
// only their new chat sign-ins, their open session keeps working; removing "KyMessages" too
// reaches the console's sweep, which ends it.
func (c *messageChat) unassign() {
	t := c.t
	pw := randomPassword()
	uid := c.admin.createPerson("leaver", leaverAddress, pw)
	c.secrets["leaver's first password"] = pw
	for _, client := range []string{"kymessage", "kymessage-chat"} {
		c.admin.assign(c.admin.app(client), uid)
	}
	pw = c.verify("leaver", uid, leaverAddress)
	c.secrets["leaver's password"] = pw
	tok, stop := c.mas.signIn("leaver", []byte(pw), c.device())
	if tok == nil {
		t.Fatalf("leaver's chat sign-in stopped at %.200s", stop)
	}
	// KyIdentity delivers an assignment about a second later; the console must know leaver
	// before its removal can reach it.
	for deadline := time.Now().Add(60 * time.Second); c.directory("leaver") != "active"; time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatalf("the console's directory has leaver %q 60 s after its assignment", c.directory("leaver"))
		}
	}
	unassign := func(client string) {
		t.Helper()
		c.admin.change("DELETE", "/api/admin/app-registry/"+c.admin.app(client)+"/assignments/users/"+uid, nil)
	}
	unassign("kymessage-chat")
	at := time.Now()
	if again, stop := c.mas.signIn("leaver", []byte(pw), c.device()); again != nil {
		t.Fatal("leaver signed in to chat again after its chat assignment was removed")
	} else {
		t.Logf("unassign: leaver's new chat sign-in without its chat assignment stopped at %.120s", stop)
	}
	code, next, _ := c.mas.refresh(tok.Refresh)
	if code != 200 {
		t.Fatalf("unassign: MAS refused leaver's refresh %d after its chat assignment was removed", code)
	}
	if wc, _, user := whoami(t, c.transport, next.Access); wc != 200 || user != mxid("leaver") || c.chatUsers()["leaver"] != "active" {
		t.Fatalf("unassign: leaver after its chat assignment was removed: whoami %d %s, MAS %q", wc, user, c.chatUsers()["leaver"])
	}
	t.Logf("unassign: %s after removing leaver's \"KyMessages chat\" assignment alone, its open session still refreshes and whoami answers 200; MAS has it active", time.Since(at).Round(time.Second))
	t1 := time.Now()
	unassign("kymessage")
	took := c.cut("leaver", uid, t1, next.Access, next.Refresh, `its removal from "KyMessages"`)
	t.Logf("unassign: removing leaver from \"KyMessages\" too ended its chat %s after", took.Round(100*time.Millisecond))
}

// earlyRemoval removes a person before KyIdentity has delivered them to the console (AC22,
// KyIdentity 0.4.6). early signs in to chat while the console is stopped, so MAS knows early
// and the console does not; the console's start locks early, failing closed. Then, with the
// setup's directory secret unreadable so the console refuses deliveries cleanly (503, never
// an uncertain write, which KyIdentity would fence), early is assigned to "KyMessages", that
// create fails, and early is disabled. KyIdentity 0.4.5 dropped such a removal with the undelivered create
// and its resync could not replay it; 0.4.6 sends it, so the console records early inactive.
func (c *messageChat) earlyRemoval() {
	t := c.t
	pw := randomPassword()
	uid := c.admin.createPerson("early", earlyAddress, pw)
	c.secrets["early's first password"] = pw
	pw = c.verify("early", uid, earlyAddress)
	c.secrets["early's password"] = pw
	docker(t, "stop", "kyq-kymessage-app-1")
	c.admin.assign(c.admin.app("kymessage-chat"), uid)
	tok, stop := c.mas.signIn("early", []byte(pw), c.device())
	if tok == nil {
		t.Fatalf("early's chat sign-in with the console stopped: %.200s", stop)
	}
	if code, _, user := whoami(t, c.transport, tok.Access); code != 200 || user != mxid("early") {
		t.Fatalf("early's chat with the console stopped: whoami %d %s", code, user)
	}
	docker(t, "start", "kyq-kymessage-app-1")
	t0 := time.Now()
	stackHealthy(t)
	c.cut("early", uid, t0, tok.Access, tok.Refresh, "the console's start")

	secret := c.msg.root + "/kymessage/data/installer/" + path.Base(messageSetupDir) + "/directory.secret"
	docker(t, "exec", c.msg.name, "chmod", "000", secret)
	c.admin.assign(c.admin.app("kymessage"), uid)
	sys := c.connector("KyMessages")
	// KyIdentity's view: Acknowledged is a delivered create, Blocked an attempt in flight.
	var state struct {
		Acknowledged, Blocked bool
		LastEvent             struct {
			Type, Status string
			Attempts     int
		}
	}
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(time.Second) {
		var page struct {
			Users []json.RawMessage
		}
		c.admin.get("/api/admin/systems/"+sys+"/provisioning?limit=100", &page)
		for _, raw := range page.Users {
			var u struct{ UserID string }
			if json.Unmarshal(raw, &u) == nil && u.UserID == uid {
				json.Unmarshal(raw, &state)
			}
		}
		if state.LastEvent.Type == "user.created" && state.LastEvent.Attempts > 0 && !state.Blocked {
			break
		}
		if time.Now().After(deadline) {
			c.diagnose(uid)
			t.Fatalf("early removal: KyIdentity's create of early did not fail cleanly with the console's directory secret unreadable: %+v", state)
		}
	}
	if state.Acknowledged {
		c.diagnose(uid)
		t.Fatalf("early removal: the console acknowledged early's create with its directory secret unreadable: %+v", state)
	}
	t.Logf("early removal: early's create to the console failed (%s after %d attempts) and is not acknowledged; disabling early", state.LastEvent.Status, state.LastEvent.Attempts)
	c.admin.change("PUT", "/api/admin/users/"+uid, map[string]string{"status": "disabled"})
	docker(t, "exec", c.msg.name, "chmod", "600", secret)

	ns, pod, container, err := c.connected.AppPod(c.ctx, "kyidentity")
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.connected.Exec(c.ctx, ns, pod, container, []string{"/usr/local/bin/kyidentity", "resync-system", "--client", "kymessage", "--wait", "2m"})
	var rep struct{ Status string }
	if err != nil || json.Unmarshal(bytes.TrimSpace(out), &rep) != nil || rep.Status != "delivered" {
		c.diagnose(uid)
		t.Fatalf("early removal: resync-system: %v %s", err, out)
	}
	if s, d := c.chatUsers()["early"], c.directory("early"); s != "locked" || d != "inactive" || c.audited("matrix.lock", mxid("early"), 0).IsZero() {
		c.diagnose(uid)
		t.Fatalf("early removal: after resync-system delivered, MAS has early %q and the console's directory %q (want locked, inactive)", s, d)
	}
	if again, _ := c.mas.signIn("early", []byte(pw), c.device()); again != nil {
		t.Fatal("the disabled early signed in to chat")
	}
	t.Log("early removal: resync-system delivered; the console's directory has early inactive and MAS keeps early locked")
}

// connector is the ID of KyIdentity's paired system name.
func (c *messageChat) connector(name string) string {
	c.t.Helper()
	var systems struct{ Systems []struct{ ID, Name string } }
	c.admin.get("/api/admin/systems", &systems)
	for _, s := range systems.Systems {
		if s.Name == name {
			return s.ID
		}
	}
	c.t.Fatalf("KyIdentity has no connector %s", name)
	return ""
}

// stackHealthy waits until every KyMessages container is running and, with a healthcheck,
// healthy.
func stackHealthy(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Minute); ; time.Sleep(2 * time.Second) {
		out := docker(t, "ps", "-a", "--filter", "label=com.docker.compose.project=kyq-kymessage", "--format", "{{.Names}} {{.State}} {{.Status}}")
		ready := strings.Count(out, "\n")+1 == 6
		for _, line := range strings.Split(out, "\n") {
			if f := strings.Fields(line); len(f) < 2 || f[1] != "running" || strings.Contains(line, "(health: starting)") || strings.Contains(line, "(unhealthy)") {
				ready = false
			}
		}
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the KyMessages stack is not healthy:\n%s", out)
		}
	}
}

// memorySampler records the peak sum of the KyMessages containers' memory, every few seconds.
type memorySampler struct {
	stop chan struct{}
	done sync.WaitGroup
	peak float64 // MiB
	at   string
}

var memUsage = regexp.MustCompile(`^([0-9.]+)([KMG]i?B|B)`)

func sampleMemory() *memorySampler {
	s := &memorySampler{stop: make(chan struct{})}
	s.done.Add(1)
	go func() {
		defer s.done.Done()
		for {
			out, _ := exec.Command("docker", "stats", "--no-stream", "--format", "{{.Name}} {{.MemUsage}}").Output()
			sum, lines := 0.0, []string{}
			for _, l := range strings.Split(string(out), "\n") {
				f := strings.Fields(l)
				if len(f) < 2 || !strings.HasPrefix(f[0], "kyq-kymessage-") {
					continue
				}
				if m := memUsage.FindStringSubmatch(f[1]); m != nil {
					v, _ := strconv.ParseFloat(m[1], 64)
					sum += v * map[string]float64{"B": 1.0 / (1 << 20), "KiB": 1.0 / 1024, "kB": 1.0 / 1024, "MiB": 1, "MB": 1, "GiB": 1024, "GB": 1024}[m[2]]
					lines = append(lines, f[0]+" "+f[1])
				}
			}
			if sum > s.peak {
				s.peak, s.at = sum, strings.Join(lines, ", ")
			}
			select {
			case <-s.stop:
				return
			case <-time.After(3 * time.Second):
			}
		}
	}()
	return s
}

func (s *memorySampler) end() (float64, string) {
	close(s.stop)
	s.done.Wait()
	return s.peak, s.at
}

var messageSnapshot = regexp.MustCompile(`(?m)^kymessage: snapshot ([0-9a-f]{64}) in `)

// stackContainers is each KyMessages container's ID and health.
func stackContainers(t *testing.T) string {
	t.Helper()
	ids := strings.Fields(docker(t, "ps", "-q", "--no-trunc", "--filter", "label=com.docker.compose.project=kyq-kymessage"))
	slices.Sort(ids)
	var out []string
	for _, i := range ids {
		out = append(out, i+" "+docker(t, "inspect", i, "--format", "{{.Name}} {{.State.Health.Status}}"))
	}
	return strings.Join(out, "\n")
}

// sweepSecrets is the AC27 list: every value at least 16 bytes long.
func sweepSecrets(secrets map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range secrets {
		if len(v) >= 16 {
			out[k] = v
		}
	}
	return out
}

// sameMembers compares two member lists as sets.
func sameMembers(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// history finds eventID in the room's history, newest first.
func (u *matrixUser) history(room id.RoomID, eventID id.EventID) bool {
	u.t.Helper()
	resp, err := u.c.Messages(u.ctx, room, "", "", mautrix.DirectionBackward, nil, 100)
	if err != nil {
		u.t.Fatalf("room history: %v", err)
	}
	return slices.ContainsFunc(resp.Chunk, func(e *event.Event) bool { return e.ID == eventID })
}

// sessionWatch follows a person's pre-backup session while a restore runs: each second it
// refreshes it at MAS and asks Synapse whoami with the new access token. It records the first
// time that answers 200 and the first refusal, by MAS (a 4xx to the refresh) or Synapse (401).
type sessionWatch struct {
	stop, finished chan struct{}
	ok, denied     time.Time
	last           string
}

func watchMatrixSession(t *testing.T, m *mas, refresh string) *sessionWatch {
	w := &sessionWatch{stop: make(chan struct{}), finished: make(chan struct{})}
	go func() {
		defer close(w.finished)
		for {
			select {
			case <-w.stop:
				return
			case <-time.After(time.Second):
			}
			code, b := m.grant(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
			var tok masTokens
			if code != 200 || json.Unmarshal(b, &tok) != nil || tok.Refresh == "" {
				if code >= 400 && code < 500 {
					w.denied, w.last = time.Now(), "MAS refused the refresh: "+strconv.Itoa(code)+" "+string(bytes.TrimSpace(b))
					return
				}
				continue
			}
			refresh = tok.Refresh
			switch wc, _, _ := whoami(t, m.transport, tok.Access); wc {
			case 200:
				if w.ok.IsZero() {
					w.ok = time.Now()
				}
			case 401:
				w.denied, w.last = time.Now(), "Synapse answered 401"
				return
			}
		}
	}()
	return w
}

func (w *sessionWatch) end() (ok, denied time.Time) {
	close(w.stop)
	<-w.finished
	return w.ok, w.denied
}

// messageOps runs kyquickstart against the stack's plan.
type messageOps struct {
	work, state, catalogDir, plan string
	trust                         []string
	apply                         func(label string) string
	outputs                       *[]string
	checks                        func() // the read-only setup checks: console and KyIdentity
	skipped                       func(label, out string)
}

func (o messageOps) run(t *testing.T, ctx context.Context, stdin string, args ...string) string {
	t.Helper()
	out, err := kyq(t, ctx, o.state, o.catalogDir, o.trust, stdin, args...)
	*o.outputs = append(*o.outputs, out)
	if err != nil {
		t.Fatalf("%s: %v", strings.Join(args, " "), err)
	}
	return out
}

// backupRestore backs the running stack up through job 008's command (AC25), offboards member
// after it, destroys the stack's data and restores it with the replay of removals (AC26).
func (c *messageChat) backupRestore(o messageOps) {
	t, ctx := c.t, c.ctx
	st := mustStack(t, o.plan)
	srv := startRestServer(t, o.work, "nas")
	files := strings.TrimSuffix(c.msg.root, "/root") + "/backup-files"
	dest := srv.destination(t, c.msg.name, files, "nas")
	docker(t, "exec", c.msg.name, "chown", "-R", "kyq", files) // the SSH user's, as preflight wants
	for i := range st.Targets {
		if st.Targets[i].Name == "nas" {
			st.Targets[i].Backup = dest
		}
	}
	savePlan(t, o.plan, st)
	handover := o.apply("backup destination")
	nas := nasTarget(t, o.plan)
	checkBackupHandover(t, handover, "kymessage", nas)
	o.skipped("backup destination", handover)
	c.secrets["the rest-server password"] = srv.passwords["nas"]

	// member's session as the snapshot holds it: active, never locked.
	c.member.renew()
	memberRefresh := c.member.refresh
	if code, _, _ := whoami(t, c.transport, c.member.c.AccessToken); code != 200 || c.chatUsers()["member"] != "active" || !c.audited("matrix.lock", mxid("member"), 0).IsZero() {
		t.Fatalf("member before the backup: whoami %d, MAS %q, or a matrix.lock in the audit", code, c.chatUsers()["member"])
	}

	// AC25: hot, with the dump streamed from postgres; the same containers, healthy.
	before := stackContainers(t)
	w := watchRestic()
	start := time.Now()
	out := o.run(t, ctx, "", "backup", "kymessage")
	inspect, _ := w.end(t)
	m := messageSnapshot.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no snapshot line:\n%s", out)
	}
	id := m[1]
	if after := stackContainers(t); after != before || strings.Count(after, " healthy") != 6 {
		t.Fatalf("the stack across the backup:\n%s\nwas:\n%s", after, before)
	}
	t.Logf("AC25: kyquickstart backup kymessage took %s, the stack running and healthy throughout", time.Since(start).Round(time.Second))
	pw := c.msg.root + "/kymessage/secrets/restic_password"
	password := handoverPassword(t, ctx, o.state, handover, nas)
	c.secrets["restic_password"] = password
	checkResticInspect(t, inspect, sweepSecrets(c.secrets))
	snaps := snapshots(t, dest, "kymessage", pw)
	inst := installationID(t, o.plan)
	if len(snaps) != 1 || snaps[0].ID != id || !slices.Contains(snaps[0].Tags, "app=kymessage") || !slices.Contains(snaps[0].Tags, "installation="+inst) {
		t.Fatalf("restic snapshots: %+v", snaps)
	}
	if got := strings.Join(snaps[0].Paths, " "); got != "/kyq/dump /kyq/secrets/session_secret /kyq/volumes/backups /kyq/volumes/data /kyq/volumes/matrix /kyq/volumes/media" {
		t.Fatalf("snapshot paths: %s", got)
	}
	ls, err := restic(dest, "kymessage", pw, "ls", id)
	if err != nil {
		t.Fatalf("restic ls: %v\n%s", err, ls)
	}
	listed := strings.Split(ls, "\n")
	for _, want := range []string{"/kyq/dump", "/kyq/volumes/matrix/secrets/upstream_provider_id", "/kyq/volumes/matrix/secrets/kyidentity_client_secret",
		"/kyq/volumes/matrix/synapse/signing.key", "/kyq/volumes/data/installer/directory_secret", "/kyq/volumes/media", "/kyq/volumes/backups"} {
		if !slices.Contains(listed, want) {
			t.Errorf("snapshot lacks %s", want)
		}
	}
	if strings.Contains(ls, "pgdata") {
		t.Error("snapshot holds the raw Postgres data directory")
	}
	if out, err := restic(dest, "kymessage", pw, "check"); err != nil {
		t.Fatalf("restic check: %v\n%s", err, out)
	}
	t.Logf("AC25: snapshot %s tagged %v, %d paths, restic check passes", id[:8], snaps[0].Tags, len(listed))

	// AC26: what must come back unchanged.
	members := c.owner.joined(c.room)
	hashes := func() string {
		return docker(t, "exec", c.msg.name, "sh", "-c", `cd "$1" && sha256sum matrix/synapse/signing.key matrix/secrets/upstream_provider_id`, "sh", c.msg.root+"/kymessage")
	}
	keys := hashes()

	// member, a person setup did not create, is offboarded after the snapshot (AC22).
	t1 := time.Now()
	c.admin.change("PUT", "/api/admin/users/"+c.memberID, map[string]string{"status": "disabled"})
	c.cut("member", c.memberID, t1, c.member.c.AccessToken, memberRefresh, "the disable")
	if s := c.chatUsers()["member"]; s != "locked" {
		t.Fatalf("the disabled member in MAS is %q", s)
	}
	docker(t, "compose", "-p", "kyq-kymessage", "down")
	docker(t, "exec", c.msg.name, "rm", "-rf", c.msg.root+"/kymessage")
	watch := watchMatrixSession(t, c.mas, memberRefresh)
	start = time.Now()
	out = o.run(t, ctx, password+"\n", "restore", "kymessage", "--snapshot", id, "--yes")
	restored := time.Now()
	for _, want := range []string{"kymessage: dump restored into postgres.", "kymessage: KyIdentity replayed its kymessage connector's state, removals included, and delivered it (", "kymessage: restored from snapshot " + id} {
		if !strings.Contains(out, want) {
			t.Fatalf("restore output lacks %q", want)
		}
	}
	t.Logf("AC26: restore took %s", restored.Sub(start).Round(time.Second))
	select {
	case <-watch.finished:
	case <-time.After(60 * time.Second):
	}
	ok, denied := watch.end()
	synapse, _ := time.Parse(time.RFC3339Nano, docker(t, "inspect", "kyq-kymessage-synapse-1", "--format", "{{.State.StartedAt}}"))
	if denied.IsZero() {
		t.Fatal("member's pre-backup session still works 60 s after the restore")
	}
	window := "never seen working"
	if !ok.IsZero() {
		window = "worked from " + ok.Format(time.RFC3339) + " (" + ok.Sub(synapse).Round(time.Second).String() + " after synapse started)"
	}
	t.Logf("AC26: member's pre-backup session (restore_order postgres, mas, app, synapse, element, livekit; replay after app) %s; refused (%s) at %s, %s after synapse started (%s), %s after the restore returned",
		window, watch.last, denied.Format(time.RFC3339Nano), denied.Sub(synapse).Round(100*time.Millisecond), synapse.Format(time.RFC3339Nano), denied.Sub(restored).Round(100*time.Millisecond))
	// The replay ran before Synapse started, so the restored session never reached a homeserver.
	if !ok.IsZero() {
		t.Errorf("AC26: member's pre-backup session worked after the restore (%s)", window)
	}
	if denied.Sub(synapse) > 60*time.Second {
		t.Errorf("AC26: member's session outlived synapse's start by over 60 s")
	}
	// The snapshot had member active with no lock, so these rows are the replay's sweep.
	for _, action := range []string{"matrix.lock", "matrix.session_end"} {
		if at := c.audited(action, mxid("member"), 30*time.Second); at.IsZero() {
			t.Errorf("AC26: no %s for member after the restore", action)
		} else {
			t.Logf("AC26: %s for member %s after synapse started", action, at.Sub(synapse).Round(time.Second))
		}
	}
	if tok, stop := c.mas.signIn("member", []byte(c.memberPW), c.device()); tok != nil {
		t.Fatal("the offboarded member signed in to chat after the restore")
	} else {
		t.Logf("AC26: member's new chat sign-in stopped at %.120s", stop)
	}

	// The owner's original device reads the pre-backup history.
	c.owner.renew()
	if !c.owner.history(c.room, c.event) {
		t.Fatal("the owner's pre-backup message is not in the room's history")
	}
	if got := c.owner.read(c.room, c.event); got != c.nonce {
		t.Fatalf("the original device decrypted %q, want the pre-backup nonce", got)
	}
	if code, _, user := whoami(t, c.transport, c.owner.c.AccessToken); code != 200 || user != mxid("prepared-user") {
		t.Fatalf("owner after the restore: %d %s", code, user)
	}
	if got := c.owner.joined(c.room); !sameMembers(got, members) {
		t.Fatalf("room members %v, were %v", got, members)
	}
	if got := hashes(); got != keys {
		t.Fatalf("signing key or provider ID changed:\n%s\nwas:\n%s", got, keys)
	}
	t.Logf("AC26: the original device decrypted the pre-backup nonce; members %v and the signing key and provider ID hashes unchanged", members)
	o.checks()
	o.skipped("after the restore", o.apply("after the restore"))
}
