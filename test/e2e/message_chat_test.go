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
	room      id.RoomID
	nonce     string // the owner's, sent before the backup
	event     id.EventID
	secrets   map[string]string
}

const memberAddress = "member@" + mailDomain

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

// audited waits until the console's audit has action for target and returns its rows.
func (c *messageChat) audited(action, target string, within time.Duration) bool {
	c.t.Helper()
	for deadline := time.Now().Add(within); ; time.Sleep(2 * time.Second) {
		var page struct {
			Records []struct{ Action, Target, Details string }
		}
		c.consoleGet("/api/admin/audit?kind=matrix&limit=100", &page)
		for _, r := range page.Records {
			if r.Action == action && r.Target == target {
				c.t.Logf("console audit: %s %s %s", r.Action, r.Target, r.Details)
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
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

// waitChatStatus waits until MAS has username in status, as the console reports it.
func (c *messageChat) waitChatStatus(username, status string) time.Duration {
	c.t.Helper()
	start := time.Now()
	for deadline := start.Add(90 * time.Second); ; time.Sleep(time.Second) {
		if got := c.chatUsers()[username]; got == status {
			return time.Since(start)
		} else if time.Now().After(deadline) {
			c.t.Fatalf("%s in MAS is %q, want %s", username, got, status)
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
	var link struct{ Delivery string }
	if json.Unmarshal(c.admin.change("POST", "/api/admin/users/"+c.memberID+"/reset-link", map[string]bool{"send": true}), &link) != nil || link.Delivery != "email" {
		t.Fatalf("reset link delivery %q", link.Delivery)
	}
	token := c.mail.resetLink(t, memberAddress)
	c.memberPW = randomPassword()
	c.secrets["member's password"] = c.memberPW
	if code, b := c.identityPost("/api/auth/password/reset", map[string]string{"token": token, "password": c.memberPW}); code != 200 {
		t.Fatalf("redeem the mailed link: %d %s", code, b)
	}
	v := c.verifiedAt("member")
	if v == "" {
		t.Fatal("KyIdentity did not verify member's address from the mailed link")
	}
	c.member = signInMatrix(t, c.ctx, c.mas, "member", []byte(c.memberPW))
	if c.member.c.UserID.String() != mxid("member") {
		t.Fatalf("member's Matrix ID %s", c.member.c.UserID)
	}
	t.Logf("AC19: member's address verified at %s by the mailed link; chat ID %s", v, c.member.c.UserID)
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

// offboarding disables the owner's everyday account in KyIdentity and measures what ends its
// chat (AC22), then re-enables it.
func (c *messageChat) offboarding(openID *mautrix.RespOpenIDToken) {
	t := c.t
	c.owner.renew()
	token, refresh := c.owner.c.AccessToken, c.owner.refresh
	renewed := time.Now()
	ownerID := c.admin.findUser("prepared-user")
	t0 := time.Now()
	c.admin.change("PUT", "/api/admin/users/"+ownerID, map[string]string{"status": "disabled"})
	for logged := false; ; time.Sleep(500 * time.Millisecond) {
		code, errcode, _ := whoami(t, c.transport, token)
		if code == 401 && errcode == "M_UNKNOWN_TOKEN" {
			break
		}
		if !logged && time.Since(t0) > 60*time.Second {
			c.diagnose(ownerID)
			logged = true
		}
		if time.Since(t0) > 6*time.Minute {
			t.Fatalf("the disabled owner's Matrix token still answers %d %s", code, errcode)
		}
	}
	denied := time.Since(t0)
	t.Logf("AC22: the owner's access token got 401 M_UNKNOWN_TOKEN %s after the disable", denied.Round(100*time.Millisecond))
	// The sweep locks at once; Synapse keeps MAS's introspection of a token for 2 minutes.
	if denied > 60*time.Second {
		t.Logf("AC22 upstream: over 60 s: the sweep locked the owner in MAS at once and ended no session, and Synapse answers from its cached introspection until it expires")
	}
	if denied > 150*time.Second {
		t.Errorf("AC22: token revoked after %s, past Synapse's 2-minute introspection cache", denied)
	}
	code, tok, b := c.mas.refresh(refresh)
	if code == 200 {
		wc, we, _ := whoami(t, c.transport, tok.Access)
		t.Logf("AC22 upstream: MAS 1.26 still refreshes the locked owner's session %s after the disable (200); the new access token gets %d %s", time.Since(t0).Round(time.Second), wc, we)
		if wc != 401 {
			t.Errorf("the locked owner's refreshed token answers %d", wc)
		}
	} else {
		t.Logf("AC22: MAS refused the owner's refresh token %s after the disable: %d %.200s", time.Since(t0).Round(time.Second), code, b)
	}
	target := mxid("prepared-user")
	if !c.audited("matrix.lock", target, 60*time.Second) {
		t.Error("no matrix.lock in the console's audit for the owner")
	}
	if c.audited("matrix.session_end", target, 0) {
		t.Log("AC22: matrix.session_end audited for the owner")
	} else {
		t.Log("AC22 upstream: no matrix.session_end for the owner: KyMessages' sweep locks a person in MAS and ends no session")
	}
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
	c.member.renew()
	if code, _, user := whoami(t, c.transport, c.member.c.AccessToken); code == 200 && user == mxid("member") {
		t.Log("AC22: member's token still works")
	} else if s := c.chatUsers()["member"]; s == "locked" {
		// The console's managed directory takes only its setup's identities (422 for member), so
		// its sweep locks member as "not known to KyMessages" at its first run after member's
		// sign-in: here the one the owner's offboarding woke.
		t.Logf("AC22 upstream: member's token gets %d: the sweep locked member, unknown to the console, whose directory refuses people its setup did not create", code)
	} else {
		t.Fatalf("member's token after the owner's disable: %d %s, MAS status %q", code, user, s)
	}

	c.admin.change("PUT", "/api/admin/users/"+ownerID, map[string]string{"status": "active"})
	if !c.audited("matrix.unlock", target, 90*time.Second) {
		t.Fatal("no matrix.unlock for the re-enabled owner")
	}
	c.owner.resume("prepared-user", c.everyday())
	if code, _, user := whoami(t, c.transport, c.owner.c.AccessToken); code != 200 || user != target {
		t.Fatalf("the re-enabled owner's fresh sign-in: %d %s", code, user)
	}
	// Past Synapse's 2-minute introspection cache, inside the token's 5-minute life.
	reenabled := time.Now()
	for {
		code, errcode, _ := whoami(t, c.transport, token)
		if code != 401 {
			t.Logf("AC22 upstream: the owner's old access token answers %d again %s after the re-enable (issued %s before): locking ended no session", code, time.Since(reenabled).Round(time.Second), time.Since(renewed).Round(time.Second))
			break
		}
		if time.Since(reenabled) > 130*time.Second || time.Since(renewed) > 290*time.Second {
			t.Logf("AC22: the owner's old access token stayed 401 %s for %s after the re-enable (issued %s before)", errcode, time.Since(reenabled).Round(time.Second), time.Since(renewed).Round(time.Second))
			break
		}
		time.Sleep(2 * time.Second)
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
// time that answers 200 and the first 401.
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
				if code != 0 && code < 500 {
					w.last = strconv.Itoa(code) + " " + string(bytes.TrimSpace(b))
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
				w.denied = time.Now()
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

	// member's session as the snapshot holds it.
	c.member.renew()
	memberRefresh := c.member.refresh

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

	// member is offboarded after the snapshot.
	c.admin.change("PUT", "/api/admin/users/"+c.memberID, map[string]string{"status": "disabled"})
	c.waitChatStatus("member", "locked")
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
		t.Fatalf("member's pre-backup session still works 60 s after the restore (last refusal %s)", watch.last)
	}
	window := "never seen working"
	if !ok.IsZero() {
		window = "worked from " + ok.Format(time.RFC3339) + " (" + ok.Sub(synapse).Round(time.Second).String() + " after synapse started)"
	}
	t.Logf("AC26: member's pre-backup session (restore_order postgres, mas, app, synapse, element, livekit) %s; 401 at %s, %s after synapse started (%s), %s after the restore returned",
		window, denied.Format(time.RFC3339), denied.Sub(synapse).Round(time.Second), synapse.Format(time.RFC3339), denied.Sub(restored).Round(time.Second))
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
