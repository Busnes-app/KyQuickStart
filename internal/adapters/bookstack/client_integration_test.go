package bookstack

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The Python qualification owns the native service and all credentials. This
// bridge receives only a labeled disposable target and its non-secret binding.
func TestNativeOffboardingAdapter(t *testing.T) {
	container := os.Getenv("KYQ_TEST_BOOKSTACK_CONTAINER")
	if container == "" {
		t.Skip("run the disposable qualification with KYQ_TEST_BOOKSTACK_ADAPTER=1")
	}
	owner := os.Getenv("KYQ_TEST_BOOKSTACK_FIXTURE_OWNER")
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(owner) || container != "kyq-bookstack-"+owner+"-app" {
		t.Fatal("refusing a target outside this disposable fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	docker := func(args ...string) []byte {
		t.Helper()
		data, err := exec.CommandContext(ctx, "docker", args...).Output()
		if err != nil {
			t.Fatalf("owned fixture operation %s failed", args[0])
		}
		return data
	}
	var labels map[string]string
	if json.Unmarshal(docker("inspect", "--format", "{{json .Config.Labels}}", container), &labels) != nil || labels["kyq.fixture-owner"] != owner || labels["kyq.fixture"] != "bookstack" {
		t.Fatal("fixture ownership is not verified")
	}
	image := os.Getenv("KYQ_TEST_BOOKSTACK_IMAGE")
	if !regexp.MustCompile(`^\S+@sha256:[0-9a-f]{64}$`).MatchString(image) || strings.TrimSpace(string(docker("inspect", "--format", "{{.Config.Image}}", container))) != image {
		t.Fatal("fixture image pin mismatch")
	}
	id, err := strconv.ParseInt(os.Getenv("KYQ_TEST_BOOKSTACK_USER_ID"), 10, 64)
	if err != nil {
		t.Fatal("fixture native ID invalid")
	}
	b := Binding{ID: id, Issuer: "https://identity.example.test", Subject: "disposable-bound-subject"}
	c, err := New(func(callCtx context.Context, args []string, stdin []byte) ([]byte, error) {
		command := exec.CommandContext(callCtx, "docker", append([]string{"exec", "-i", "-u", "abc", container}, args...)...)
		command.Stdin = bytes.NewReader(stdin)
		return command.Output()
	}, "/config", "/app/www/artisan")
	if err != nil {
		t.Fatal(err)
	}
	ownerSpec := AccountSpec{SetupID: "2c676879-9c71-4913-b8a7-995521d9ed20", Issuer: b.Issuer,
		ClientID: "fixture-client", Subject: "setup-owner-subject", Name: "Owner 雪 & <test>", Email: "setup-owner@example.test", Administrator: true}
	if os.Getenv("KYQ_TEST_BOOKSTACK_PROFILE") == "1" {
		account, err := c.CheckAccount(ctx, ownerSpec)
		if err != nil || account.Name != "Retained owner 雪" || account.Email != "retained-owner@example.test" {
			t.Fatal("retained owned profile unavailable", err)
		}
		scope := ProfileScope{Binding: account.Binding, SetupID: ownerSpec.SetupID, ClientID: ownerSpec.ClientID}
		current := Profile{account.Name, account.Email}
		state, err := c.InitializeProfile(ctx, scope, current)
		if err != nil || state.Profile != current || state.Revision != 0 || state.Disabled {
			t.Fatal("native profile initialization failed", err)
		}
		desired := Profile{"Projected 雪", "projected-owner@example.test"}
		state, err = c.ApplyProfile(ctx, scope, desired)
		if err != nil || state.Profile != desired || state.Revision != 1 {
			t.Fatal("native projection failed", err)
		}
		if _, err := c.profile(ctx, scope, Profile{"Stale", "stale@example.test"}, "apply", 0); err == nil {
			t.Fatal("stale profile revision accepted")
		}
		repeated, err := c.ApplyProfile(ctx, scope, desired)
		if err != nil || repeated != state {
			t.Fatal("profile repeat changed revision", err)
		}
		foreign := scope
		foreign.Subject = "wrong-subject"
		if _, err := c.ApplyProfile(ctx, foreign, current); err == nil {
			t.Fatal("foreign profile adopted")
		}
		if _, err := c.ApplyProfile(ctx, scope, Profile{"Collision", "admin@admin.com"}); err == nil {
			t.Fatal("native email collision accepted")
		}
		if repeated, err := c.InspectProfile(ctx, scope); err != nil || repeated != state {
			t.Fatal("refusal changed profile", err)
		}
		lost := true
		unknown := *c
		unknown.exec = func(callCtx context.Context, args []string, stdin []byte) ([]byte, error) {
			raw, err := c.exec(callCtx, args, stdin)
			var request struct {
				Action string `json:"action"`
			}
			_ = json.Unmarshal(stdin, &request)
			if err == nil && request.Action == "apply" && lost {
				lost = false
				return nil, errors.New("lost profile reply")
			}
			return raw, err
		}
		desired = Profile{"After lost reply", "lost-reply@example.test"}
		if _, err := unknown.ApplyProfile(ctx, scope, desired); err == nil {
			t.Fatal("lost profile outcome reported success")
		}
		state, err = c.ApplyProfile(ctx, scope, desired)
		if err != nil || state.Profile != desired || state.Revision != 2 {
			t.Fatal("lost outcome replayed or failed readback", err)
		}
		// Fault injection uses only this already verified disposable native target.
		// An occupied next audit key must roll back the preceding profile write.
		auditKey := "kyquickstart-profile:" + strconv.FormatInt(scope.ID, 10) + ":audit:3"
		fault := func(key, value string, remove bool) {
			faultNativeSetting(t, ctx, container, key, value, remove)
		}
		fault(auditKey, "occupied future proof", false)
		if _, err := c.ApplyProfile(ctx, scope, Profile{"Must roll back", "rollback@example.test"}); err == nil {
			t.Fatal("failed audit publication reported success")
		}
		if retained, err := c.InspectProfile(ctx, scope); err != nil || retained != state {
			t.Fatal("audit failure changed native profile", err)
		}
		fault(auditKey, "", true)
		if err := c.Deprovision(ctx, account.Binding); err != nil {
			t.Fatal("profile owner revocation failed", err)
		}
		retained, err := c.InspectProfile(ctx, scope)
		if err != nil || !retained.Disabled || retained.Profile != desired || retained.Revision != 2 {
			t.Fatal("revoked profile not retained", err)
		}
		if _, err := c.ApplyProfile(ctx, scope, current); err == nil {
			t.Fatal("profile update reactivated revoked account")
		}
		fault("kyquickstart-profile:"+strconv.FormatInt(scope.ID, 10)+":audit:2", "", true)
		if _, err := c.InspectProfile(ctx, scope); err == nil {
			t.Fatal("missing durable native audit proof accepted")
		}
		t.Log("native owned profile revision/audit, repeat, collision/foreign refusal, lost-result resume and permanent retained revocation verified")
		return
	}
	ownerAccount, err := c.Apply(ctx, ownerSpec)
	if err != nil || !ownerAccount.Created || ownerAccount.Name != ownerSpec.Name || ownerAccount.Email != ownerSpec.Email || ownerAccount.Administrator || ownerAccount.Disabled || !ownerAccount.ActivationPending || !ownerAccount.AdministratorGrantPending {
		t.Fatal("native owner account creation or pending authority failed")
	}
	resumed, err := c.Apply(ctx, ownerSpec)
	if err != nil || resumed.Created || resumed.Binding != ownerAccount.Binding {
		t.Fatal("native target receipt did not resume its owned account")
	}
	if err := c.VerifyAdmin(ctx, ownerSpec); err == nil {
		t.Fatal("owner pending OIDC grant was treated as administrator readiness")
	}
	changed := ownerSpec
	changed.Administrator = false
	if _, err := c.Apply(ctx, changed); err == nil {
		t.Fatal("immutable provisioning intent was overwritten")
	}
	unowned := ownerSpec
	unowned.SetupID = "c30ecafb-6337-446f-982a-aac50b56b753"
	if _, err := c.Apply(ctx, unowned); err == nil {
		t.Fatal("existing source-bound account was adopted under another receipt")
	}
	wrongSource := unowned
	wrongSource.ClientID = "unconfigured-client"
	if _, err := c.Apply(ctx, wrongSource); err == nil {
		t.Fatal("account source mismatch accepted")
	}
	dailySpec := ownerSpec
	dailySpec.SetupID, dailySpec.Subject, dailySpec.Name, dailySpec.Email, dailySpec.Administrator =
		"43f864f0-991c-43bf-8b94-d93c2916a98f", "setup-everyday-subject", "Everyday setup", "setup-everyday@example.test", false
	// Lose the result only after the real native create committed. Retry must find
	// its own receipt and native ID without profile-based adoption or a second user.
	lostReply := true
	unknown := *c
	unknown.exec = func(callCtx context.Context, args []string, stdin []byte) ([]byte, error) {
		data, err := c.exec(callCtx, args, stdin)
		var input struct {
			Action string `json:"action"`
		}
		json.Unmarshal(stdin, &input)
		if err == nil && input.Action == "create" && lostReply {
			lostReply = false
			return nil, errors.New("simulated lost native reply")
		}
		return data, err
	}
	if _, err := unknown.Apply(ctx, dailySpec); err == nil {
		t.Fatal("unknown mutation outcome reported success")
	}
	everyday, err := c.Apply(ctx, dailySpec)
	if err != nil || everyday.Created || everyday.Administrator || everyday.AdministratorGrantPending || everyday.Disabled || !everyday.ActivationPending {
		t.Fatal("native account/receipt transaction did not recover the lost outcome")
	}
	if err := c.Deprovision(ctx, everyday.Binding); err != nil {
		t.Fatal(err)
	}
	offboarded, err := c.Apply(ctx, dailySpec)
	if err != nil || offboarded.Created || !offboarded.Disabled || offboarded.Administrator || offboarded.ID != everyday.ID {
		t.Fatal("account resume reactivated or replaced a retained identity")
	}
	before, err := c.Inspect(ctx, b)
	if err != nil || before.Disabled || before.RevocationComplete || before.TokensRemaining != 1 {
		t.Fatal("initial native inspection failed")
	}
	wrong := b
	wrong.Subject = "wrong-subject"
	if _, err := c.Inspect(ctx, wrong); err == nil {
		t.Fatal("native binding mismatch accepted")
	}
	if err := c.Deprovision(ctx, b); err != nil {
		t.Fatal(err)
	}
	// Reproduce a crash between independent fence publications when no token was
	// present. Disabled alone must not suppress completion of the subject fence.
	docker("exec", "-u", "abc", container, "rm", "/config/kyquickstart-revocations/subject-"+digest(b)+".json")
	partial, err := c.Inspect(ctx, b)
	if err != nil || !partial.Disabled || partial.RevocationComplete || partial.TokensRemaining != 0 {
		t.Fatal("partial native revocation not represented")
	}
	if err := c.Deprovision(ctx, b); err != nil {
		t.Fatal(err)
	}
	after, err := c.Inspect(ctx, b)
	if err != nil || !after.Disabled || !after.RevocationComplete || after.TokensRemaining != 0 {
		t.Fatal("native revocation not fully repaired")
	}
	if err := c.Deprovision(ctx, b); err != nil {
		t.Fatal(err)
	}
	remaining := docker("exec", "-u", "abc", container, "find", "/config", "-maxdepth", "1", "-name", "kyquickstart-offboard.*", "-print")
	if len(bytes.TrimSpace(remaining)) != 0 {
		t.Fatal("native request staging directory leaked")
	}
}

func TestNativeHTTPOffboardingAdapter(t *testing.T) {
	container := os.Getenv("KYQ_TEST_BOOKSTACK_HTTP_CONTAINER")
	if container == "" {
		t.Skip("run the disposable signed OIDC qualification")
	}
	owner := os.Getenv("KYQ_TEST_BOOKSTACK_FIXTURE_OWNER")
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(owner) || container != "kyq-bookstack-"+owner+"-app" {
		t.Fatal("refusing an unowned fixture target")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	docker := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).Output()
		if err != nil {
			t.Fatal("owned fixture operation failed")
		}
		return out
	}
	var config struct {
		Image  string
		Labels map[string]string
	}
	image := os.Getenv("KYQ_TEST_BOOKSTACK_IMAGE")
	if json.Unmarshal(docker("inspect", "--format", "{{json .Config}}", container), &config) != nil || config.Labels["kyq.fixture"] != "bookstack" || config.Labels["kyq.fixture-owner"] != owner || !regexp.MustCompile(`^\S+@sha256:[0-9a-f]{64}$`).MatchString(image) || config.Image != image {
		t.Fatal("fixture ownership/image not verified")
	}
	hostPort := strings.TrimSpace(string(docker("port", "kyq-bookstack-"+owner+"-gateway", "8443")))
	if !strings.HasPrefix(hostPort, "127.0.0.1:") || strings.Contains(hostPort, "\n") {
		t.Fatal("fixture gateway is not loopback-only")
	}
	var credential struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(docker("exec", "-u", "abc", container, "cat", "/config/kyquickstart-bridge/token.json"), &credential) != nil {
		t.Fatal("fixture credential unreadable")
	}
	client, err := NewHTTP("https://"+hostPort, credential.Token)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(docker("exec", container, "cat", "/fixture/ca.pem")) {
		t.Fatal("fixture public CA invalid")
	}
	transport := client.http.Transport.(*http.Transport)
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	id, err := strconv.ParseInt(os.Getenv("KYQ_TEST_BOOKSTACK_USER_ID"), 10, 64)
	if err != nil {
		t.Fatal("fixture native ID invalid")
	}
	binding := Binding{ID: id, Issuer: "https://kyq-bookstack-" + owner + "-gateway:8443/fixture-idp", Subject: os.Getenv("KYQ_TEST_BOOKSTACK_DAILY_SUBJECT")}
	before, err := client.Inspect(ctx, binding)
	producer := os.Getenv("KYQ_TEST_BOOKSTACK_PRODUCER") == "1"
	expectedTokens := int64(1)
	if producer {
		expectedTokens = 0
	}
	if err != nil || before.Disabled != producer || before.RevocationComplete != producer || before.TokensRemaining != expectedTokens {
		t.Fatal("initial scoped HTTP inspection failed", err)
	}
	wrong := binding
	wrong.Subject = "wrong-subject"
	if _, err := client.Inspect(ctx, wrong); err == nil {
		t.Fatal("HTTP endpoint accepted a rebound subject")
	}
	if err := client.Deprovision(ctx, binding); err != nil {
		t.Fatal(err)
	}
	docker("exec", "-u", "abc", container, "rm", "/config/kyquickstart-revocations/subject-"+digest(binding)+".json")
	partial, err := client.Inspect(ctx, binding)
	if err != nil || !partial.Disabled || partial.RevocationComplete || partial.TokensRemaining != 0 {
		t.Fatal("HTTP partial publication not represented", err)
	}
	if err := client.Deprovision(ctx, binding); err != nil {
		t.Fatal("HTTP publication repair failed", err)
	}
	if err := client.Deprovision(ctx, binding); err != nil {
		t.Fatal("HTTP completed resume failed", err)
	}
}

func TestNativeOIDCAccountVerification(t *testing.T) {
	container := os.Getenv("KYQ_TEST_BOOKSTACK_OIDC_CONTAINER")
	if container == "" {
		t.Skip("run the disposable signed OIDC qualification")
	}
	owner := os.Getenv("KYQ_TEST_BOOKSTACK_FIXTURE_OWNER")
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(owner) || container != "kyq-bookstack-"+owner+"-app" {
		t.Fatal("refusing a target outside this disposable fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	inspect, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Config}}", container).Output()
	var config struct {
		Image  string
		Labels map[string]string
	}
	image := os.Getenv("KYQ_TEST_BOOKSTACK_IMAGE")
	if err != nil || json.Unmarshal(inspect, &config) != nil || config.Labels["kyq.fixture"] != "bookstack" ||
		config.Labels["kyq.fixture-owner"] != owner || !regexp.MustCompile(`^\S+@sha256:[0-9a-f]{64}$`).MatchString(image) || config.Image != image {
		t.Fatal("fixture ownership/image not verified")
	}
	issuer := "https://kyq-bookstack-" + owner + "-gateway:8443/fixture-idp"
	c, err := New(func(callCtx context.Context, args []string, stdin []byte) ([]byte, error) {
		command := exec.CommandContext(callCtx, "docker", append([]string{"exec", "-i", "-u", "abc", container}, args...)...)
		command.Stdin = bytes.NewReader(stdin)
		return command.Output()
	}, "/config", "/app/www/artisan")
	if err != nil {
		t.Fatal(err)
	}
	ownerSpec := AccountSpec{SetupID: "2c676879-9c71-4913-b8a7-995521d9ed20", Issuer: issuer,
		ClientID: "fixture-client", Subject: "owner-subject", Name: "Owner", Email: "owner@example.test", Administrator: true}
	if subject := os.Getenv("KYQ_TEST_BOOKSTACK_OWNER_SUBJECT"); subject != "" {
		ownerSpec.Subject = subject
	}
	if err := c.VerifyAdmin(ctx, ownerSpec); err != nil {
		t.Fatal("actual signed OIDC owner grant not verified by installer client")
	}
	dailySpec := AccountSpec{SetupID: "43f864f0-991c-43bf-8b94-d93c2916a98f", Issuer: issuer,
		ClientID: "fixture-client", Subject: "daily-subject", Name: "Everyday", Email: "daily@example.test"}
	if subject := os.Getenv("KYQ_TEST_BOOKSTACK_DAILY_SUBJECT"); subject != "" {
		dailySpec.Subject = subject
	}
	if phase := os.Getenv("KYQ_TEST_BOOKSTACK_ROLES"); phase != "" {
		id, err := strconv.ParseInt(os.Getenv("KYQ_TEST_BOOKSTACK_USER_ID"), 10, 64)
		if err != nil || id < 1 {
			t.Fatal("native role fixture binding invalid")
		}
		daily := Binding{ID: id, Issuer: dailySpec.Issuer, Subject: dailySpec.Subject}
		var ordinary []int64
		if json.Unmarshal([]byte(os.Getenv("KYQ_TEST_BOOKSTACK_ORDINARY_ROLE_IDS")), &ordinary) != nil {
			t.Fatal("ordinary role policy invalid")
		}
		scope := RoleScope{ProfileScope: ProfileScope{Binding: daily, SetupID: dailySpec.SetupID, ClientID: dailySpec.ClientID}, EverydayRole: "editor", OrdinaryRoleIDs: ordinary}
		switch phase {
		case "grant":
			state, err := c.InitializeRoles(ctx, scope, false)
			if err != nil || state.Administrator || state.Disabled || state.Revision != 0 {
				t.Fatal("native ordinary role initialization failed", err)
			}
			h := fixtureDirectoryHTTP(t, ctx, container, owner)
			before, err := h.InspectDirectory(ctx, scope)
			if err != nil || before.Roles.Administrator || before.Roles.Revision != 0 || before.Profile.Revision != 1 {
				t.Fatal("native directory unavailable before HTTPS grant", err)
			}
			granted, err := h.ApplyDirectory(ctx, scope, before.Profile.Profile, true)
			if err != nil || !granted.Roles.Administrator || granted.Roles.Revision != 1 || granted.Profile != before.Profile {
				t.Fatal("HTTPS administrator projection failed", err)
			}
			state = granted.Roles
			if repeat, err := c.ApplyRoles(ctx, scope, true); err != nil || repeat != state {
				t.Fatal("native role repeat changed state", err)
			}
			if _, err := c.roles(ctx, scope, false, "apply", 0); err == nil {
				t.Fatal("stale native role revision accepted")
			}
			if checked, err := c.InspectRoles(ctx, scope); err != nil || checked != state {
				t.Fatal("role refusal changed authority", err)
			}
			if check, apply := nativeRoleLocks(t, ctx, container, scope.ID); check != 0 || apply == 0 {
				t.Fatal("per-request role check takes row locks", check, apply)
			}
		case "demote":
			h := fixtureDirectoryHTTP(t, ctx, container, owner)
			before, err := h.InspectDirectory(ctx, scope)
			if err != nil || !before.Roles.Administrator || before.Roles.Revision != 1 || before.Profile.Revision != 1 {
				t.Fatal("native directory state unavailable before audit failure", err)
			}
			desired := Profile{"Atomic directory everyday", "atomic-directory@example.test"}
			if _, err := h.directory(ctx, scope, desired, false, "apply", before.Profile.Revision, 0); err == nil {
				t.Fatal("stale directory authority accepted after profile write")
			}
			if checked, err := h.InspectDirectory(ctx, scope); err != nil || checked != before {
				t.Fatal("stale role refusal did not roll back profile", err)
			}
			key := "kyquickstart-roles:" + strconv.FormatInt(scope.ID, 10) + ":audit:2"
			faultNativeSetting(t, ctx, container, key, "occupied future audit", false)
			if _, err := h.ApplyDirectory(ctx, scope, desired, false); err == nil {
				t.Fatal("failed directory role audit publication reported success")
			}
			if checked, err := h.InspectDirectory(ctx, scope); err != nil || checked != before {
				t.Fatal("role audit failure did not roll back profile and authority", err)
			}
			faultNativeSetting(t, ctx, container, key, "", true)
			unknown := *h
			clientCopy := *h.client
			httpCopy := *h.client.http
			unknown.client = &clientCopy
			unknown.client.http = &httpCopy
			lost := true
			transport := h.client.http.Transport
			unknown.client.http.Transport = fixtureRoundTrip(func(request *http.Request) (*http.Response, error) {
				body, err := request.GetBody()
				if err != nil {
					return nil, err
				}
				var intent struct {
					Profile struct {
						Action string `json:"action"`
					} `json:"profile"`
				}
				err = json.NewDecoder(body).Decode(&intent)
				body.Close()
				if err != nil {
					return nil, err
				}
				response, err := transport.RoundTrip(request)
				if err == nil && intent.Profile.Action == "apply" && lost {
					lost = false
					response.Body.Close()
					return nil, errors.New("lost native directory HTTP reply")
				}
				return response, err
			})
			if _, err := unknown.ApplyDirectory(ctx, scope, desired, false); err == nil {
				t.Fatal("lost directory outcome reported success")
			}
			state, err := h.ApplyDirectory(ctx, scope, desired, false)
			if err != nil || state.Roles.Administrator || state.Roles.Disabled || state.Roles.Revision != 2 ||
				state.Profile.Profile != desired || state.Profile.Revision != 2 {
				t.Fatal("native atomic directory resume failed", err)
			}
			if repeat, err := h.ApplyDirectory(ctx, scope, desired, false); err != nil || repeat != state {
				t.Fatal("directory repeat changed receipts", err)
			}
		case "retained":
			expectedRevision := int64(2)
			if os.Getenv("KYQ_TEST_BOOKSTACK_DIRECTORY") == "1" || os.Getenv("KYQ_TEST_BOOKSTACK_PRODUCER") == "1" {
				expectedRevision = 4
			}
			state, err := c.InspectRoles(ctx, scope)
			if err != nil || !state.Disabled || state.Administrator || state.Revision != expectedRevision {
				t.Fatal("revoked roles not retained", err)
			}
			if _, err := c.ApplyDirectory(ctx, scope, Profile{"Revoked", "revoked@example.test"}, true); err == nil {
				t.Fatal("revoked directory projection accepted")
			}
			faultNativeSetting(t, ctx, container, "kyquickstart-roles:"+strconv.FormatInt(scope.ID, 10)+":audit:"+strconv.FormatInt(expectedRevision, 10), "", true)
			if _, err := c.InspectRoles(ctx, scope); err == nil {
				t.Fatal("missing native role audit accepted")
			}
		default:
			t.Fatal("unknown owned role fixture phase")
		}
		t.Log("native owned role projection/readback phase verified:", phase)
		return
	}
	daily, err := c.CheckAccount(ctx, dailySpec)
	if err != nil || !daily.Disabled || daily.Administrator || !daily.OIDCVerified || daily.Created || daily.ActivationPending {
		t.Fatal("retained signed OIDC everyday account receipt not verified")
	}
	if err := c.VerifyAdmin(ctx, dailySpec); err == nil {
		t.Fatal("everyday identity accepted as owner administrator")
	}
}

// Called only after the native helper has verified its disposable container and pin.
func faultNativeSetting(t *testing.T, ctx context.Context, container, key, value string, remove bool) {
	t.Helper()
	keyJSON, _ := json.Marshal(key)
	valueJSON, _ := json.Marshal(value)
	code := `require '/app/www/vendor/autoload.php'; $app=require '/app/www/bootstrap/app.php'; $app->make(\Illuminate\Contracts\Console\Kernel::class)->bootstrap();`
	if remove {
		code += ` \BookStack\Settings\Setting::query()->where('setting_key',` + string(keyJSON) + `)->delete();`
	} else {
		code += ` \BookStack\Settings\Setting::query()->insert(['setting_key'=>` + string(keyJSON) + `,'type'=>'string','value'=>` + string(valueJSON) + `]);`
	}
	if exec.CommandContext(ctx, "docker", "exec", "-u", "abc", container, "php", "-r", code).Run() != nil {
		t.Fatal("owned native fault injection failed")
	}
}

// Counts FOR UPDATE queries in the per-request check and, as a control, a no-op apply repeat.
// Called only after the native helper has verified its disposable container and pin.
func nativeRoleLocks(t *testing.T, ctx context.Context, container string, id int64) (int, int) {
	t.Helper()
	code := `require '/app/www/vendor/autoload.php'; $app=require '/app/www/bootstrap/app.php'; $app->make(\Illuminate\Contracts\Console\Kernel::class)->bootstrap();
use Illuminate\Support\Facades\DB; use KyQuickStart\BookStack\Roles;
$locks = function (callable $run): int { DB::flushQueryLog(); DB::enableQueryLog(); $run(); DB::disableQueryLog();
    return count(array_filter(DB::getQueryLog(), fn ($q): bool => stripos($q['query'], 'for update') !== false)); };
$user = \BookStack\Users\Models\User::findOrFail(` + strconv.FormatInt(id, 10) + `);
$r = Roles::inspectUser($user);
$check = $locks(fn () => Roles::inspectUser($user));
$apply = $locks(fn () => Roles::apply(Roles::validate(['contract' => Roles::CONTRACT, 'action' => 'apply', 'user_id' => $user->id,
    'setup_id' => $r['setup_id'], 'issuer' => (string) config('oidc.issuer'), 'client_id' => (string) config('oidc.client_id'),
    'subject' => (string) $user->external_auth_id, 'ordinary_role_ids' => $r['ordinary_role_ids'],
    'everyday_role' => $r['everyday_role'], 'administrator' => $r['administrator'], 'expected_revision' => $r['revision']])));
echo json_encode([$check, $apply]);`
	out, err := exec.CommandContext(ctx, "docker", "exec", "-u", "abc", container, "php", "-r", code).Output()
	var counts [2]int
	if err != nil || json.Unmarshal(out, &counts) != nil {
		t.Fatal("native role lock probe failed")
	}
	return counts[0], counts[1]
}

// Called only after the parent verifies this nonce-owned container and pinned image.
func fixtureDirectoryHTTP(t *testing.T, ctx context.Context, container, owner string) *DirectoryHTTPClient {
	t.Helper()
	docker := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).Output()
		if err != nil {
			t.Fatal("owned directory fixture operation failed")
		}
		return out
	}
	hostPort := strings.TrimSpace(string(docker("port", "kyq-bookstack-"+owner+"-gateway", "8443")))
	if !strings.HasPrefix(hostPort, "127.0.0.1:") || strings.Contains(hostPort, "\n") {
		t.Fatal("directory gateway is not loopback-only")
	}
	var credential struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(docker("exec", "-u", "abc", container, "cat", "/config/kyquickstart-bridge/directory.json"), &credential) != nil {
		t.Fatal("directory fixture credential unreadable")
	}
	client, err := NewDirectoryHTTP("https://"+hostPort, credential.Token)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(docker("exec", container, "cat", "/fixture/ca.pem")) {
		t.Fatal("directory fixture public CA invalid")
	}
	client.client.http.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return client
}

type fixtureRoundTrip func(*http.Request) (*http.Response, error)

func (f fixtureRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
