package bookstack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"testing"
	"time"
)

func TestNativeManagedAccountCreation(t *testing.T) {
	container := os.Getenv("KYQ_TEST_BOOKSTACK_OIDC_CONTAINER")
	if os.Getenv("KYQ_TEST_BOOKSTACK_CREATE") != "1" || container == "" {
		t.Skip("run owned native creation fixture")
	}
	owner := os.Getenv("KYQ_TEST_BOOKSTACK_FIXTURE_OWNER")
	if !regexp.MustCompile(`^[a-f0-9]{12}$`).MatchString(owner) || container != "kyq-bookstack-"+owner+"-app" {
		t.Fatal("unowned creation fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Config}}", container).Output()
	var cfg struct {
		Image  string
		Labels map[string]string
	}
	image := os.Getenv("KYQ_TEST_BOOKSTACK_IMAGE")
	if err != nil || json.Unmarshal(raw, &cfg) != nil || cfg.Image != image || !regexp.MustCompile(`^\S+@sha256:[a-f0-9]{64}$`).MatchString(image) || cfg.Labels["kyq.fixture-owner"] != owner || cfg.Labels["kyq.fixture"] != "bookstack" {
		t.Fatal("native ownership not verified")
	}
	c, err := New(func(callCtx context.Context, args []string, stdin []byte) ([]byte, error) {
		command := exec.CommandContext(callCtx, "docker", append([]string{"exec", "-i", "-u", "abc", container}, args...)...)
		command.Stdin = bytes.NewReader(stdin)
		return command.Output()
	}, "/config", "/app/www/artisan")
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	if json.Unmarshal([]byte(os.Getenv("KYQ_TEST_BOOKSTACK_ORDINARY_ROLE_IDS")), &ids) != nil {
		t.Fatal("role scope missing")
	}
	spec := CreationSpec{InstallationID: "271333e2-c221-4601-b249-563b3d828530", Account: AccountSpec{SetupID: "57a9c424-6499-4bfe-8f66-458b1c1136f0", Issuer: "https://kyq-bookstack-" + owner + "-gateway:8443/fixture-idp", ClientID: "fixture-client", Subject: os.Getenv("KYQ_TEST_BOOKSTACK_NEW_SUBJECT"), Name: "Created 雪", Email: "unassigned@example.test"}, EverydayRole: "editor", OrdinaryRoleIDs: ids}
	create, check := c.CreateManagedAccount, c.CheckCreatedAccount
	var h *CreationHTTPClient
	if os.Getenv("KYQ_TEST_BOOKSTACK_CREATE_HTTP") == "1" {
		d := fixtureDirectoryHTTP(t, ctx, container, owner)
		raw, err := exec.CommandContext(ctx, "docker", "exec", "-u", "abc", container, "cat", "/config/kyquickstart-bridge/creation.json").Output()
		var credential struct {
			Token string `json:"token"`
		}
		if err != nil || json.Unmarshal(raw, &credential) != nil {
			t.Fatal("creation credential unavailable")
		}
		h, err = NewCreationHTTP(d.client.origin, credential.Token)
		if err != nil {
			t.Fatal(err)
		}
		h.client.http.Transport = d.client.http.Transport
		create, check = h.CreateManagedAccount, h.CheckCreatedAccount
	}
	nativeScope := CreationScope{InstallationID: spec.InstallationID, AccountSetupID: spec.Account.SetupID, Issuer: spec.Account.Issuer, ClientID: spec.Account.ClientID, Subject: spec.Account.Subject, EverydayRole: spec.EverydayRole, OrdinaryRoleIDs: spec.OrdinaryRoleIDs}
	if os.Getenv("KYQ_TEST_BOOKSTACK_CREATE_PHASE") == "retained" {
		prior, err := check(ctx, spec)
		if err != nil || !prior.Account.OIDCVerified || prior.Account.Disabled {
			t.Fatal("new signed ordinary identity unverified", err)
		}
		if err := c.Deprovision(ctx, prior.Account.Binding); err != nil {
			t.Fatal(err)
		}
		after, err := create(ctx, spec)
		if err != nil || after.Account.Created || !after.Account.Disabled || after.Account.ID != prior.Account.ID || after.Directory.Profile.Profile != prior.Directory.Profile.Profile {
			t.Fatal("retained creation changed identity/lifecycle", err)
		}
		t.Log("signed ordinary creation and retained revocation receipts verified")
		return
	}
	faultKey := "kyquickstart-creation:" + spec.Account.SetupID + ":audit"
	faultNativeSetting(t, ctx, container, faultKey, "collision", false)
	if h != nil {
		absent, err := h.InspectSubjectAbsence(ctx, nativeScope)
		if err != nil || absent {
			t.Fatal("orphan creation audit became subject absence", err)
		}
		if _, err := h.ResolveCreatedScope(ctx, nativeScope); err == nil {
			t.Fatal("orphan audit adopted an account")
		}
	}
	if _, err := create(ctx, spec); err == nil || (h != nil && !errors.Is(err, ErrCreationUncertain)) {
		t.Fatal("database failure did not retain unknown HTTP outcome", err)
	}
	if _, err := c.CheckAccount(ctx, spec.Account); err == nil {
		t.Fatal("account escaped creation transaction")
	}
	faultNativeSetting(t, ctx, container, faultKey, "", true)
	if h != nil {
		absent, err := h.InspectSubjectAbsence(ctx, nativeScope)
		if err != nil || !absent {
			t.Fatal("rolled back birth still owns a subject", err)
		}
	}
	bad := spec
	bad.OrdinaryRoleIDs = []int64{1}
	if _, err := create(ctx, bad); err == nil {
		t.Fatal("management role granted at birth")
	}
	if _, err := c.CheckAccount(ctx, spec.Account); err == nil {
		t.Fatal("role-policy refusal left an account")
	}
	lost := *c
	lostReply := true
	lost.exec = func(callCtx context.Context, args []string, stdin []byte) ([]byte, error) {
		out, err := c.exec(callCtx, args, stdin)
		var input struct{ Contract, Action string }
		_ = json.Unmarshal(stdin, &input)
		if err == nil && input.Contract == creationContract && input.Action == "create" && lostReply {
			lostReply = false
			return nil, errors.New("lost creation response")
		}
		return out, err
	}
	lostCreate := lost.CreateManagedAccount
	if h != nil {
		unknown := *h
		private := *h.client
		unknown.client = &private
		private.http = &http.Client{Timeout: h.client.http.Timeout, CheckRedirect: h.client.http.CheckRedirect}
		transport := h.client.http.Transport
		private.http.Transport = fixtureRoundTrip(func(request *http.Request) (*http.Response, error) {
			body, err := request.GetBody()
			if err != nil {
				return nil, err
			}
			var intent struct{ Action string }
			err = json.NewDecoder(body).Decode(&intent)
			body.Close()
			if err != nil {
				return nil, err
			}
			response, err := transport.RoundTrip(request)
			if err == nil && intent.Action == "create" && lostReply {
				lostReply = false
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				return nil, errors.New("lost native creation HTTP reply")
			}
			return response, err
		})
		lostCreate = unknown.CreateManagedAccount
	}
	if _, err := lostCreate(ctx, spec); err == nil || (h != nil && !errors.Is(err, ErrCreationUncertain)) {
		t.Fatal("lost mutation claimed success or bypassed uncertainty hold", err)
	}

	created, err := check(ctx, spec)
	if err != nil || created.Account.Created || created.Account.Administrator || created.Account.Disabled || created.Directory.Roles.Revision != 0 || created.Directory.Profile.Revision != 0 {
		t.Fatal("joined creation receipts missing", err)
	}
	if h != nil {
		resolved, err := h.ResolveCreatedScope(ctx, nativeScope)
		if err != nil || resolved.ID != created.Account.ID || resolved.SetupID != spec.Account.SetupID {
			t.Fatal("joined native scope did not resolve", err)
		}
		absent, err := h.InspectSubjectAbsence(ctx, nativeScope)
		if err != nil || absent {
			t.Fatal("created account became subject absence", err)
		}
		foreign := nativeScope
		foreign.AccountSetupID = "e4707fa6-340e-4e2f-ad6c-f7b2fb803d8b"
		if _, err := h.ResolveCreatedScope(ctx, foreign); err == nil {
			t.Fatal("foreign setup pin resolved native identity")
		}
	}
	repeated, err := create(ctx, spec)
	if err != nil || repeated != created {
		t.Fatal("creation repeated or reset", err)
	}
	conflict := spec
	conflict.InstallationID = "2c676879-9c71-4913-b8a7-995521d9ed20"
	if _, err := create(ctx, conflict); err == nil {
		t.Fatal("foreign installation accepted")
	}
	conflict = spec
	conflict.Account.SetupID = "e4707fa6-340e-4e2f-ad6c-f7b2fb803d8b"
	if _, err := create(ctx, conflict); err == nil {
		t.Fatal("subject collision adopted")
	}
	existing := spec
	existing.Account = AccountSpec{SetupID: "43f864f0-991c-43bf-8b94-d93c2916a98f", Issuer: spec.Account.Issuer, ClientID: spec.Account.ClientID, Subject: os.Getenv("KYQ_TEST_BOOKSTACK_DAILY_SUBJECT"), Name: "Everyday", Email: "daily@example.test"}
	prior, err := c.CheckAccount(ctx, existing.Account)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := create(ctx, existing); err == nil {
		t.Fatal("prior setup account adopted without a creation receipt")
	}
	if h != nil {
		legacyScope := nativeScope
		legacyScope.AccountSetupID = existing.Account.SetupID
		legacyScope.Subject = existing.Account.Subject
		if _, err := h.ResolveCreatedScope(ctx, legacyScope); err == nil {
			t.Fatal("prior setup account resolved without creation ownership")
		}
		absent, err := h.InspectSubjectAbsence(ctx, legacyScope)
		if err != nil || absent {
			t.Fatal("prior setup became subject absence", err)
		}
	}
	retained, err := c.CheckAccount(ctx, existing.Account)
	if err != nil || retained != prior {
		t.Fatal("refused adoption changed prior setup")
	}
	_, err = c.ApplyDirectory(ctx, creationScope(spec, created.Account.ID), Profile{"Managed created 雪", spec.Account.Email}, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := create(ctx, spec)
	if err != nil || after.Account.Created || after.Account.Name != "Managed created 雪" || after.Directory.Profile.Revision != 1 {
		t.Fatal("repeat reset owned projection", err)
	}
	t.Log("atomic ordinary creation, policy/audit rollback, lost result, no adoption and immutable resume verified")
}
