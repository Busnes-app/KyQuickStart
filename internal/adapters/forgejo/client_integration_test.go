package forgejo

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeAccountInspectionAndOffboarding(t *testing.T) {
	image := os.Getenv("KYQ_TEST_FORGEJO_IMAGE")
	if image == "" {
		t.Skip("set KYQ_TEST_FORGEJO_IMAGE to a pinned disposable rootless image")
	}
	if !strings.Contains(image, "@sha256:") {
		t.Fatal("fixture image must be pinned")
	}
	if os.Getenv("KYQ_TEST_FORGEJO_GROUPS") == "1" && (os.Getenv("KYQ_TEST_FORGEJO_PRODUCER") != "1" || os.Getenv("KYQ_TEST_FORGEJO_TEAMS") != "1") {
		t.Fatal("group worker fixture requires producer and owned-team fixture")
	}
	reactivate := os.Getenv("KYQ_TEST_FORGEJO_REACTIVATE") == "1"
	if reactivate && (os.Getenv("KYQ_TEST_FORGEJO_QUEUE") != "1" || os.Getenv("KYQ_TEST_FORGEJO_SCIM") != "1" || os.Getenv("KYQ_TEST_FORGEJO_IDENTITY_BINARY") != "") {
		t.Fatal("reactivation fixture requires the queue and SCIM flags and the test IdP")
	}
	duration := 90 * time.Second
	if os.Getenv("KYQ_TEST_FORGEJO_PRODUCER") == "1" || reactivate {
		duration = 180 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).Output()
		if err != nil {
			t.Fatalf("disposable Docker operation %s failed", args[0])
		}
		return strings.TrimSpace(string(out))
	}
	name := fmt.Sprintf("kyq-forgejo-api-%d", time.Now().UnixNano())
	portListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := portListener.Addr().String()
	_, port, _ := net.SplitHostPort(address)
	portListener.Close()
	origin := "http://" + address
	backend, _ := url.Parse(origin)
	api := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(backend))
	defer api.Close()
	docker("run", "-d", "--name", name, "--network", "host", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--label", "kyq.fixture=forgejo", "--label", "kyq.fixture-owner="+name,
		"-e", "FORGEJO__server__HTTP_ADDR=127.0.0.1", "-e", "FORGEJO__server__HTTP_PORT="+port,
		"-e", "FORGEJO__server__ROOT_URL="+api.URL+"/", "-e", "SSL_CERT_FILE=/var/lib/gitea/fixture/ca.pem",
		"-e", "FORGEJO__oauth2_client__ACCOUNT_LINKING=disabled", "-e", "FORGEJO__oauth2_client__ENABLE_AUTO_REGISTRATION=false",
		"-e", "FORGEJO__security__INSTALL_LOCK=true", "-e", "FORGEJO__database__DB_TYPE=sqlite3", "-e", "FORGEJO__server__DISABLE_SSH=true",
		"-e", "FORGEJO__service__DISABLE_REGISTRATION=true", "-e", "FORGEJO__log__LEVEL=warn", image)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", name).Run() })
	readiness := &http.Client{Timeout: 2 * time.Second}
	waitReady := func() {
		for deadline := time.Now().Add(30 * time.Second); ; {
			response, err := readiness.Get(origin + "/api/v1/version")
			if err == nil {
				response.Body.Close()
				if response.StatusCode == 200 {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("disposable Forgejo did not start")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	waitReady()
	config := "/var/lib/gitea/custom/conf/app.ini"
	docker("exec", name, "forgejo", "admin", "user", "create", "--config", config, "--username", "fixture-admin", "--email", "fixture-admin@example.test", "--password", "disposable-fixture-password-only", "--admin", "--must-change-password=false")
	scopes := "write:admin,read:user,read:repository"
	if os.Getenv("KYQ_TEST_FORGEJO_TEAMS") == "1" {
		scopes = "write:admin,read:user,write:repository,write:organization"
	}
	token := docker("exec", name, "forgejo", "admin", "user", "generate-access-token", "--config", config, "--username", "fixture-admin", "--token-name", "fixture-adapter", "--raw", "--scopes", scopes)
	c, err := New(api.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = api.Client().Transport
	defer c.http.CloseIdleConnections()

	if os.Getenv("KYQ_TEST_FORGEJO_CREATE") == "1" && os.Getenv("KYQ_TEST_FORGEJO_PRODUCER") != "1" {
		t.Fatal("creation fixture requires actual producer mode")
	}
	var producer *nativeProducerPlan
	if os.Getenv("KYQ_TEST_FORGEJO_PRODUCER") == "1" {
		if os.Getenv("KYQ_TEST_FORGEJO_IDENTITY_BINARY") == "" {
			t.Fatal("producer mode requires actual identity binary")
		}
		producer = newNativeProducerPlan(t)
	}
	var idp *fixtureOIDC
	if binary := os.Getenv("KYQ_TEST_FORGEJO_IDENTITY_BINARY"); binary != "" {
		idp = newNativeFixtureOIDC(t, ctx, api.URL, image, binary, producer)
	} else {
		idp = newFixtureOIDC(t)
	}
	docker("exec", name, "mkdir", "-p", "/var/lib/gitea/fixture")
	docker("cp", idp.caFile, name+":/var/lib/gitea/fixture/ca.pem")
	sources, err := c.OpenSourceSession(ctx, "fixture-admin", "disposable-fixture-password-only")
	if err != nil {
		t.Fatal(err)
	}
	defer sources.Close()
	sourceSpec := SourceSpec{Name: "kyidentity-fixture", Issuer: idp.server.URL, ClientID: "fixture-client", AssignedClaim: "kyq_assigned", AssignedValue: "forgejo", RolesClaim: "roles", AdminRole: "admin"}
	if idp.native != nil {
		sourceSpec.AssignedClaim, sourceSpec.AssignedValue = "aud", "fixture-client"
	}
	source, err := sources.Apply(ctx, sourceSpec, "disposable-fixture-client-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sources.Apply(ctx, sourceSpec, "disposable-fixture-client-secret"); err == nil {
		t.Fatal("unpinned native source adopted")
	}
	if repeated, err := sources.Apply(ctx, source, "disposable-fixture-client-secret"); err != nil || repeated != source {
		t.Fatal("source repeat changed retained configuration")
	}
	if err := sources.Inspect(ctx, source, "wrong-fixture-secret"); err == nil {
		t.Fatal("conflicting source credential accepted")
	}
	conflictingSource := source
	conflictingSource.AdminRole = "wrong-role"
	if _, err := sources.Apply(ctx, conflictingSource, "disposable-fixture-client-secret"); err == nil {
		t.Fatal("conflicting source authority accepted")
	}
	if err := sources.Inspect(ctx, source, "disposable-fixture-client-secret"); err != nil {
		t.Fatal("conflicting source apply changed native configuration")
	}
	create := func(login, subject string) Binding {
		t.Helper()
		binding, err := c.Apply(ctx, AccountSpec{Username: login, Email: login + "@example.test", SourceID: source.ID, Subject: subject})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Apply(ctx, AccountSpec{Username: login, Email: login + "@example.test", SourceID: source.ID, Subject: subject}); err == nil {
			t.Fatal("existing unpinned account adopted")
		}
		retained, err := c.Apply(ctx, AccountSpec{Username: login, Email: login + "@example.test", SourceID: source.ID, Subject: subject, ExistingID: binding.ID})
		if err != nil || retained != binding {
			t.Fatal("pinned provisioning repeat changed binding")
		}
		return binding
	}
	ownerName, dailyName := "owner-admin", "everyday-user"
	if idp.native != nil {
		ownerName, dailyName = "owner", "daily"
	}
	owner := create(ownerName, idp.subject("owner"))
	daily := create(dailyName, idp.subject("daily"))
	unassigned := create("unassigned-user", idp.subject("unassigned"))
	ownerSession := idp.login(t, api, "owner")
	dailySession := idp.login(t, api, "daily")
	unassignedSession := idp.login(t, api, "unassigned")
	probeSession := func(client *http.Client, endpoint string, expected int) {
		t.Helper()
		probe := *client
		probe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := probe.Get(api.URL + endpoint)
		if err != nil {
			t.Fatal("native session probe failed")
		}
		response.Body.Close()
		if response.StatusCode != expected {
			t.Fatalf("native session authorization for %s: got %d, expected %d", endpoint, response.StatusCode, expected)
		}
	}
	probeSession(ownerSession, "/user/settings", http.StatusOK)
	probeSession(dailySession, "/user/settings", http.StatusOK)
	probeSession(unassignedSession, "/user/settings", http.StatusSeeOther)
	probeSession(ownerSession, "/admin", http.StatusOK)
	probeSession(dailySession, "/admin", http.StatusForbidden)
	probeSuspended := func(client *http.Client) {
		t.Helper()
		response, err := client.Get(api.URL + "/user/settings")
		if err != nil {
			t.Fatal("native suspended-session probe failed")
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		// Forgejo deliberately renders its suspended-account screen with HTTP 200.
		if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), `aria-label="Account is suspended"`) || !strings.Contains(string(body), "Your account has been suspended from interacting with the instance.") {
			t.Fatal("native session did not reach the suspended-account boundary")
		}
	}
	var repository struct {
		ID int64 `json:"id"`
	}
	if err = c.call(ctx, http.MethodPost, "/admin/users/"+daily.Username+"/repos", map[string]any{"name": "retained-content", "private": true, "auto_init": true, "description": "fixture retained repository"}, &repository); err != nil {
		t.Fatal(err)
	}
	if err = c.GrantAdmin(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err = c.GrantAdmin(ctx, daily); err == nil {
		t.Fatal("everyday identity treated as administrator")
	}
	if os.Getenv("KYQ_TEST_FORGEJO_TEAMS") == "1" {
		organizationID := qualifyNativeTeam(t, ctx, c, owner, daily, unassigned, name, image, source.Issuer, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}))
		if producer != nil && os.Getenv("KYQ_TEST_FORGEJO_GROUPS") == "1" {
			producer.OrganizationID = organizationID
			producer.GroupID = idp.native.groupID
		}
	}
	ownerRevision, dailyRevision := 0, 0
	if producer != nil {
		if os.Getenv("KYQ_TEST_FORGEJO_CREATE") == "1" {
			producer.NewSubject = idp.subject("new-daily")
		}
		stopProducer := startNativeProducerBridge(t, ctx, producer, name, image, token, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}), api.URL, source.Issuer, owner, daily)
		defer stopProducer()
		ownerRevision = idp.native.waitDelivery(t, ctx, ownerSession, idp.server.URL, "owner", true, 0)
		dailyRevision = idp.native.waitDelivery(t, ctx, ownerSession, idp.server.URL, "daily", true, 0)
		for _, binding := range []Binding{owner, daily} {
			account, err := c.Inspect(ctx, binding)
			if err != nil || account.Name != binding.Username || account.Email != binding.Username+"@example.test" || account.Admin != (binding == owner) {
				t.Fatal("native producer did not project its exact initial profile and authority")
			}
		}
		t.Log("actual identity worker acknowledged initial profiles and roles; unassigned identities excluded")
		if producer.GroupID != "" {
			qualifyNativeGroupProducer(t, ctx, c, producer, idp.native, ownerSession, idp.server.URL, daily)
		}
		profile := Profile{Name: "Native directory owner 名", Email: "native-owner-directory@example.test"}
		idp.native.changeProfile(t, ownerSession, idp.server.URL, "owner", profile)
		ownerRevision = idp.native.waitDelivery(t, ctx, ownerSession, idp.server.URL, "owner", true, ownerRevision)
		account, err := c.Inspect(ctx, owner)
		if err != nil || account.Name != profile.Name || account.Email != profile.Email || !account.Admin {
			t.Fatal("actual identity profile event did not reach Forgejo")
		}
		t.Log("actual identity profile change reached Forgejo without another sign-in")
		if producer.NewSubject != "" {
			idp.native.assignAccess(t, ownerSession, idp.server.URL, "new-daily")
			revision := idp.native.waitDelivery(t, ctx, ownerSession, idp.server.URL, "new-daily", true, 0)
			var native struct {
				ID int64 `json:"id"`
			}
			if c.call(ctx, http.MethodGet, "/users/new-daily", nil, &native) != nil || native.ID <= 0 {
				t.Fatal("worker did not create native account")
			}
			b := Binding{ID: native.ID, Username: "new-daily", Subject: producer.NewSubject, SourceID: source.ID}
			account, err := c.Inspect(ctx, b)
			if err != nil || !account.Active || account.Admin || account.Name != "new-daily" || account.Email != "new-daily@example.test" {
				t.Fatal("created native identity/profile incorrect")
			}
			session := idp.login(t, api, "new-daily")
			probeSession(session, "/user/settings", http.StatusOK)
			probeSession(session, "/admin", http.StatusForbidden)
			var repository struct {
				ID int64 `json:"id"`
			}
			if c.call(ctx, http.MethodPost, "/admin/users/new-daily/repos", map[string]any{"name": "created-retained", "private": true, "auto_init": true}, &repository) != nil {
				t.Fatal("new-account retained repository failed")
			}
			var nativeToken struct {
				Value string `json:"sha1"`
			}
			if c.call(ctx, http.MethodPost, "/admin/users/new-daily/tokens", map[string]any{"name": "created-token", "scopes": []string{"read:user"}}, &nativeToken) != nil {
				t.Fatal("new-account token failed")
			}
			idp.native.changeAccess(t, ownerSession, idp.server.URL, "new-daily", false)
			idp.native.waitDelivery(t, ctx, ownerSession, idp.server.URL, "new-daily", false, revision)
			probeSuspended(session)
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, api.URL+"/api/v1/user", nil)
			req.Header.Set("Authorization", "token "+nativeToken.Value)
			response, err := api.Client().Do(req)
			if err != nil {
				t.Fatal("created-account token probe failed")
			}
			response.Body.Close()
			if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
				t.Fatal("created-account token not revoked")
			}
			var retained struct {
				ID int64 `json:"id"`
			}
			if c.call(ctx, http.MethodGet, "/repos/new-daily/created-retained", nil, &retained) != nil || retained.ID != repository.ID {
				t.Fatal("new-account repository lost")
			}
			t.Log("actual identity worker created, signed in and revoked new native account while retaining content")
		}

	}
	for _, user := range []struct {
		binding Binding
		profile Profile
	}{{owner, Profile{Name: "Dedicated owner", Email: "owner-directory@example.test"}},
		{daily, Profile{Name: "Everyday 名", Email: "daily-directory@example.test"}}} {
		for i := 0; i < 2; i++ {
			if err = c.UpdateProfile(ctx, user.binding, user.profile); err != nil {
				t.Fatal(err)
			}
		}
		account, err := c.Inspect(ctx, user.binding)
		if err != nil || account.Name != user.profile.Name || account.Email != user.profile.Email || !account.Active || account.LoginProhibited || account.Admin != (user.binding == owner) {
			t.Fatal("native directory profile changed pinned identity or authority")
		}
	}
	var oldToken struct {
		Value string `json:"sha1"`
	}
	for i := 0; i < 101; i++ {
		if err = c.call(ctx, http.MethodPost, "/admin/users/"+daily.Username+"/tokens", map[string]any{"name": fmt.Sprintf("fixture-%d", i), "scopes": []string{"read:user"}}, &oldToken); err != nil {
			t.Fatal(err)
		}
	}
	checkToken := func(wantAllowed bool) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, api.URL+"/api/v1/user", nil)
		req.Header.Set("Authorization", "token "+oldToken.Value)
		response, err := api.Client().Do(req)
		if err != nil {
			t.Fatal("fixture token request failed")
		}
		response.Body.Close()
		if (wantAllowed && response.StatusCode != http.StatusOK) || (!wantAllowed && response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden) {
			t.Fatalf("old token authorization: %d", response.StatusCode)
		}
	}
	checkToken(true)
	if idp.native != nil {
		idp.native.changeAccess(t, ownerSession, idp.server.URL, "daily", false)
		denied := idp.login(t, api, "daily")
		probeSession(denied, "/user/settings", http.StatusSeeOther)
		if producer != nil {
			idp.native.waitDelivery(t, ctx, ownerSession, idp.server.URL, "daily", false, dailyRevision)
			checkToken(false)
			probeSuspended(dailySession)
		} else {
			account, err := c.Inspect(ctx, daily)
			if err != nil || !account.Active || account.LoginProhibited {
				t.Fatal("identity assignment removal silently mutated the native account")
			}
			checkToken(true)
			t.Log("native assignment loss refuses fresh OIDC issuance; retained product access still requires offboarding")
		}
	}
	wrong := daily
	wrong.Subject = "wrong-subject"
	if err = c.Deprovision(ctx, wrong); err == nil {
		t.Fatal("conflicting binding accepted")
	}
	checkToken(producer == nil)
	dailyBinding := map[string]any{"setup_id": "271333e2-c221-4601-b249-563b3d828530", "app": "forgejo", "kind": "forgejo", "origin": api.URL,
		"issuer": source.Issuer, "subject": daily.Subject, "native_id": daily.ID, "source_id": daily.SourceID, "username": daily.Username}
	// The reactivation phases reopen the bridge store the queue phase revoked.
	state := ""
	if reactivate {
		if state, err = filepath.EvalSymlinks(t.TempDir()); err != nil || os.Chmod(state, 0700) != nil {
			t.Fatal("bridge state directory unavailable")
		}
	}
	if os.Getenv("KYQ_TEST_FORGEJO_QUEUE") == "1" {
		// A separate test process avoids importing the bridge back into its adapter.
		// Fixture credentials and public trust stay in RAM, never argv or state files.
		input, err := json.Marshal(map[string]any{
			"container": name, "image": image, "token": token, "state": state,
			"ca":      pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}),
			"binding": dailyBinding,
		})
		if err != nil {
			t.Fatal("queue fixture encoding failed")
		}
		queue := exec.CommandContext(ctx, "go", "test", "-race", "-count=1", "-v", "./internal/offboard", "-run", "^TestNativeForgejoQueue$")
		queue.Dir = "../../.."
		queue.Env = append(os.Environ(), "KYQ_TEST_FORGEJO_QUEUE_JSON="+string(input))
		output, err := queue.Output()
		if err != nil {
			t.Fatal("native Forgejo queue qualification failed")
		}
		t.Log(strings.TrimSpace(string(output)))
	} else {
		for i := 0; i < 2; i++ {
			if err = c.Deprovision(ctx, daily); err != nil {
				t.Fatal(err)
			}
		}
	}
	checkToken(false)
	probeSuspended(dailySession)
	newSession := idp.login(t, api, "daily")
	if idp.native != nil {
		probeSession(newSession, "/user/settings", http.StatusSeeOther)
	} else {
		probeSuspended(newSession)
	}
	var retained struct {
		ID          int64  `json:"id"`
		Description string `json:"description"`
	}
	if err = c.call(ctx, http.MethodGet, "/repos/"+daily.Username+"/retained-content", nil, &retained); err != nil || retained.ID != repository.ID || retained.Description != "fixture retained repository" {
		t.Fatal("offboarding did not retain native repository")
	}

	account, err := c.Inspect(ctx, daily)
	if err != nil || account.Active || !account.LoginProhibited || account.Admin {
		t.Fatal("native account revocation failed")
	}
	if _, err := c.Apply(ctx, AccountSpec{Username: daily.Username, Email: daily.Username + "@example.test", SourceID: daily.SourceID, Subject: daily.Subject, ExistingID: daily.ID}); err != nil {
		t.Fatal("retained account inspection failed after offboarding")
	}
	account, err = c.Inspect(ctx, daily)
	if err != nil || account.Active || !account.LoginProhibited {
		t.Fatal("repeat provisioning reactivated an offboarded account")
	}
	profile := Profile{Email: "disabled-directory@example.test"}
	if err = c.UpdateProfile(ctx, daily, profile); err != nil {
		t.Fatal(err)
	}
	account, err = c.Inspect(ctx, daily)
	if err != nil || account.Name != "" || account.Email != profile.Email || account.Active || !account.LoginProhibited || account.Admin {
		t.Fatal("profile update reactivated or changed authority of retained disabled account")
	}
	checkToken(false)
	probeSuspended(dailySession)
	t.Log("native directory profiles and repeat verified for owner, everyday and retained disabled accounts")
	if err = c.GrantAdmin(ctx, owner); err != nil {
		t.Fatal("offboarding changed unrelated owner authority")
	}
	if reactivate {
		profile := Profile{Name: "Everyday reactivated", Email: daily.Username + "@example.test"}
		bridge := func(phase string) {
			t.Helper()
			input, err := json.Marshal(map[string]any{
				"container": name, "image": image, "token": token, "state": state, "phase": phase, "name": profile.Name, "email": profile.Email,
				"ca":      pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}),
				"binding": dailyBinding,
			})
			if err != nil {
				t.Fatal("reactivation fixture encoding failed")
			}
			command := exec.CommandContext(ctx, "go", "test", "-race", "-count=1", "-v", "./internal/offboard", "-run", "^TestNativeForgejoReactivation$")
			command.Dir = "../../.."
			command.Env = append(os.Environ(), "KYQ_TEST_FORGEJO_REACTIVATE_JSON="+string(input))
			output, err := command.Output()
			t.Log(strings.TrimSpace(string(output)))
			if err != nil {
				t.Fatal("native Forgejo reactivation phase failed: " + phase)
			}
		}
		// Forgejo 16.0.5 has no session revocation: re-enabling sign-in would revive
		// the pre-revocation session, so the bridge refuses reactivation.
		stillRevoked := func() {
			t.Helper()
			account, err := c.Inspect(ctx, daily)
			if err != nil || account.Active || !account.LoginProhibited || account.Admin {
				t.Fatal("refused reactivation changed the retained account")
			}
			checkToken(false)
			probeSuspended(dailySession)
			probeSuspended(idp.login(t, api, "daily"))
			if err = c.call(ctx, http.MethodGet, "/repos/"+daily.Username+"/retained-content", nil, &retained); err != nil || retained.ID != repository.ID {
				t.Fatal("retained repository lost")
			}
		}
		bridge("reactivate")
		stillRevoked()
		t.Log("re-assignment refused: same native account still revoked, old session/tokens and fresh sign-in denied, repository retained")
		bridge("revoke")
		stillRevoked()
		t.Log("second PATCH revoked again: account, session and token denial and repository retention unchanged")
	}
	if idp.native != nil {
		idp.native.changeAccess(t, ownerSession, idp.server.URL, "owner", true)
		if producer != nil {
			idp.native.waitDelivery(t, ctx, ownerSession, idp.server.URL, "owner", true, ownerRevision)
			probeSession(ownerSession, "/admin", http.StatusForbidden)
			if c.GrantAdmin(ctx, owner) == nil {
				t.Fatal("producer left stale administrator authority")
			}
			t.Log("actual identity worker removed old-session administrator authority before another OIDC login")
		} else if os.Getenv("KYQ_TEST_FORGEJO_DIRECTORY") == "1" {
			input, err := json.Marshal(map[string]any{
				"container": name, "image": image, "token": token,
				"ca": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}),
				"binding": map[string]any{"setup_id": "271333e2-c221-4601-b249-563b3d828530", "app": "forgejo", "kind": "forgejo", "origin": api.URL,
					"issuer": source.Issuer, "subject": owner.Subject, "native_id": owner.ID, "source_id": owner.SourceID, "username": owner.Username},
			})
			if err != nil {
				t.Fatal("directory fixture encoding failed")
			}
			command := exec.CommandContext(ctx, "go", "test", "-race", "-count=1", "-v", "./internal/offboard", "-run", "^TestNativeForgejoDirectory$")
			command.Dir = "../../.."
			command.Env = append(os.Environ(), "KYQ_TEST_FORGEJO_DIRECTORY_JSON="+string(input))
			output, err := command.Output()
			if err != nil {
				t.Log(strings.TrimSpace(string(output)))
				t.Fatal("native Forgejo directory runtime qualification failed")
			}
			t.Log(strings.TrimSpace(string(output)))
			probeSession(ownerSession, "/admin", http.StatusForbidden)
			if err := c.GrantAdmin(ctx, owner); err == nil {
				t.Fatal("SCIM role replacement left stale native administrator access")
			}
			t.Log("native directory role replacement denies old-session administration before another OIDC login")
		}
		changed := idp.login(t, api, "owner")
		probeSession(changed, "/user/settings", http.StatusOK)
		probeSession(changed, "/admin", http.StatusForbidden)
		probeSession(ownerSession, "/admin", http.StatusForbidden)
		if err := c.GrantAdmin(ctx, owner); err == nil {
			t.Fatal("native role loss left the administrator grant active")
		}
		t.Log("native app administrator role loss removes Forgejo administration after fresh OIDC, including the old product session")
	}
}
