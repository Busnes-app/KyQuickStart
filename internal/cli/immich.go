package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/google/uuid"
)

var immichNativeID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var immichSubject = regexp.MustCompile(`^[\x21-\x7e]{1,255}$`)

const (
	// immichSetupRoot is the server's ./setup bind: the bundle and the bridge's service key
	// live there, never in the library.
	immichSetupRoot  = "/kyq-setup"
	immichServiceKey = immichSetupRoot + "/service.key"
	// immichRolesClaim is the claim KyIdentity always sends the app roles in; it sends role
	// only for legacy clients, which installer clients never are (decision 8).
	immichRolesClaim = "roles"
)

// immichFeatures are what apply's setup needs from the module; reactivation is the bridge's.
var immichFeatures = []string{"native-oidc", "owned-initial-accounts", "immutable-receipts", "check", "mobile-redirect", "service-administrator"}

// immichCreated is what the module's first setup creates.
var immichCreated = []string{"provider", "administrator", "everyday", "service"}

type immichSetupAccount struct {
	ID            string `json:"id"`
	Subject       string `json:"subject"`
	Administrator *bool  `json:"administrator"`
}
type immichNativeUser struct {
	ID            string `json:"id"`
	Administrator *bool  `json:"administrator"`
}
type immichSetupReport struct {
	Schema   string               `json:"schema"`
	SetupID  string               `json:"setupId"`
	Check    *bool                `json:"check"`
	Created  []string             `json:"created"`
	Planned  []string             `json:"planned"`
	Accounts []immichSetupAccount `json:"accounts"`
	Service  *struct {
		ID string `json:"id"`
	} `json:"service"`
	NativeUsers []immichNativeUser `json:"nativeUsers"`
}

func readImmichSetupReport(raw []byte, check bool) (immichSetupReport, error) {
	var r immichSetupReport
	if len(raw) > 128<<10 || json.Unmarshal(raw, &r) != nil || r.Schema != "immich.setup-report/1" ||
		!productSetupID.MatchString(r.SetupID) || r.Check == nil || *r.Check != check || r.Created == nil || r.Planned == nil ||
		len(r.Planned) != 0 || len(r.Accounts) != 2 || (check && len(r.Created) != 0) ||
		(len(r.Created) != 0 && !slices.Equal(r.Created, immichCreated)) {
		return r, errors.New("Immich setup is incomplete or its report is invalid")
	}
	for i, a := range r.Accounts {
		if !immichNativeID.MatchString(a.ID) || !immichSubject.MatchString(a.Subject) || a.Administrator == nil || *a.Administrator != (i == 0) {
			return r, errors.New("Immich setup lacks exact native account and administrator proofs")
		}
	}
	if r.Accounts[0].ID == r.Accounts[1].ID || r.Accounts[0].Subject == r.Accounts[1].Subject {
		return r, errors.New("Immich administrator and everyday accounts must be distinct")
	}
	if r.Service == nil || !immichNativeID.MatchString(r.Service.ID) || r.Service.ID == r.Accounts[0].ID || r.Service.ID == r.Accounts[1].ID {
		return r, errors.New("Immich setup lacks its service administrator")
	}
	// The owner's administrator, the everyday account and the service administrator, with
	// exactly those roles. Right after the first setup they are every native account; later
	// ones are KyIdentity's assignees, whatever their role.
	want := map[string]bool{r.Accounts[0].ID: true, r.Accounts[1].ID: false, r.Service.ID: true}
	seen := map[string]bool{}
	for _, u := range r.NativeUsers {
		if !immichNativeID.MatchString(u.ID) || u.Administrator == nil || seen[u.ID] {
			return r, errors.New("Immich reports an invalid native account list")
		}
		seen[u.ID] = true
		if admin, owned := want[u.ID]; owned && *u.Administrator != admin {
			return r, errors.New("an Immich setup account has the wrong administrator right")
		}
	}
	if len(r.NativeUsers) > 1000 || !seen[r.Accounts[0].ID] || !seen[r.Accounts[1].ID] || !seen[r.Service.ID] || len(r.Created) != 0 && len(r.NativeUsers) != 3 {
		return r, errors.New("Immich's native accounts are not exactly its setup's owner, everyday and service accounts")
	}
	return r, nil
}

// immichCapabilities requires the qualified module, before any file is placed or changed: a
// stock image has no apply-setup and fails here.
func immichCapabilities(ctx context.Context, exec identityExec) error {
	raw, err := exec(ctx, []string{"apply-setup", "--capabilities"})
	if err != nil {
		return err
	}
	var caps struct {
		Schema, Service, BundleSchema, NativeVersion, ModuleContract string
		Features                                                     []string
	}
	if len(raw) > 128<<10 || json.Unmarshal(raw, &caps) != nil || caps.Schema != "ky.setup-capabilities/1" ||
		caps.Service != "immich" || caps.BundleSchema != "immich.setup/1" || caps.NativeVersion != "3.2.0" || caps.ModuleContract != "ky.immich-setup/1" {
		return errors.New("Immich does not advertise the qualified native setup module")
	}
	for _, feature := range immichFeatures {
		if !slices.Contains(caps.Features, feature) {
			return errors.New("Immich native setup capability is missing")
		}
	}
	return nil
}

// immichApply applies bundle, or only checks it, and on apply confirms the result with the
// module's read-only check: same installation, accounts and service administrator.
func immichApply(ctx context.Context, exec identityExec, bundle string, check bool) (immichSetupReport, error) {
	args := []string{"apply-setup", "--file", bundle}
	if check {
		args = append(args, "--check")
	}
	raw, err := exec(ctx, args)
	if err != nil {
		return immichSetupReport{}, err
	}
	r, err := readImmichSetupReport(raw, check)
	if err != nil || check {
		return r, err
	}
	raw, err = exec(ctx, append(args, "--check"))
	if err != nil {
		return r, err
	}
	verified, err := readImmichSetupReport(raw, true)
	if err != nil {
		return r, err
	}
	if verified.SetupID != r.SetupID || verified.Service.ID != r.Service.ID {
		return r, errors.New("Immich installation ownership changed during verification")
	}
	for i, account := range verified.Accounts {
		if account.ID != r.Accounts[i].ID || account.Subject != r.Accounts[i].Subject || *account.Administrator != *r.Accounts[i].Administrator {
			return r, errors.New("Immich native account ownership changed during verification")
		}
	}
	return r, nil
}

// immichSetup is the module's setup as integrate-product runs it, on an existing Immich.
func immichSetup(ctx context.Context, exec identityExec, bundle string, check bool) ([]byte, error) {
	if err := immichCapabilities(ctx, exec); err != nil {
		return nil, err
	}
	r, err := immichApply(ctx, exec, bundle, check)
	if err != nil {
		return nil, err
	}
	// Emit fixed names and native UUIDs only; arbitrary product fields and OIDC
	// subject text remain inside validation and never enter workstation output.
	objects := []productObject{{Kind: "configuration", Name: "native-oidc"},
		{Kind: "identity", Name: "owner-administrator", ID: r.Accounts[0].ID},
		{Kind: "identity", Name: "everyday", ID: r.Accounts[1].ID},
		{Kind: "identity", Name: "service-administrator", ID: r.Service.ID}}
	out := productReport{Schema: "ky.product-setup-report/1", Service: "immich", SetupID: r.SetupID,
		Created: []productObject{}, Present: []productObject{}, Pending: []productObject{}}
	if len(r.Created) == 0 {
		out.Present = objects
	} else {
		out.Created = objects
	}
	return json.Marshal(out)
}

type immichAccount struct {
	Subject string `json:"subject"`
	Email   string `json:"email"`
	Name    string `json:"name"`
}

// immichBundle is the module's immich.setup/1 bundle. Its client secret is inline: the file
// is private to the server's user.
type immichBundle struct {
	Schema   string `json:"schema"`
	SetupID  string `json:"setupId"`
	Provider struct {
		Issuer            string `json:"issuer"`
		ClientID          string `json:"clientId"`
		ClientSecret      string `json:"clientSecret"`
		RolesClaim        string `json:"rolesClaim"`
		MobileRedirectURI string `json:"mobileRedirectUri"`
	} `json:"provider"`
	ServiceKeyFile string        `json:"serviceKeyFile"`
	Administrator  immichAccount `json:"administrator"`
	Everyday       immichAccount `json:"everyday"`
}

// immichSetupStep (immich.setup) is apply's setup of the catalog Immich: KyIdentity's client
// and the owner's two accounts go into the module's bundle, which creates them, the provider
// and the service administrator whose key the bridge uses.
type immichSetupStep struct {
	i               stack.Integration // Bundle is /kyq-setup/installer/<setup UUID>/setup.json
	source          stack.Integration // KyIdentity's with the client, or its amendment's
	first           string            // KyIdentity's first setup bundle, whose check names the everyday subject
	issuer, mobile  string
	admin, everyday stack.IdentityAccount
	export          identityExec // KyIdentity's apply-setup
	exec            identityExec // immich-admin in the server
	place           func(ctx context.Context, args []string, input []byte) error
	shell           identityExec // sh in the server, which probes a refused placement
	target, setup   string       // the target's name and its ./setup bind directory, for messages
}

// immichResult is what a verified setup proves: its report and KyIdentity's client.
type immichResult struct {
	report           immichSetupReport
	issuer, clientID string
}

func (s immichSetupStep) ID() string { return "immich.setup" }

func (s immichSetupStep) InputHash() string {
	b, err := json.Marshal([]any{s.i, s.source, s.first, s.issuer, s.mobile, s.admin, s.everyday})
	if err != nil {
		panic(err) // plain typed metadata always marshals
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s immichSetupStep) Inspect(ctx context.Context) (bool, error) { return s.Verify(ctx) == nil, nil }
func (s immichSetupStep) Apply(ctx context.Context) error {
	_, err := s.run(ctx, false)
	return err
}
func (s immichSetupStep) Verify(ctx context.Context) error {
	_, err := s.run(ctx, true)
	return err
}

// run checks both contracts before writing, places the bundle (never replacing a differing
// one), applies it and re-exports the client to confirm it did not change meanwhile.
func (s immichSetupStep) run(ctx context.Context, check bool) (immichResult, error) {
	if err := identityCapabilities(ctx, s.export, true, "client-credential-export"); err != nil {
		return immichResult{}, err
	}
	if err := immichCapabilities(ctx, s.exec); err != nil {
		return immichResult{}, err
	}
	exportArgs := []string{"apply-setup", "--file", s.source.Bundle, "--export-client", s.i.IdentityClient}
	raw, err := s.export(ctx, exportArgs)
	defer clear(raw)
	if err != nil {
		return immichResult{}, errors.New("private source credential export failed")
	}
	frame, err := readClientCredentials(raw, s.source, s.i.IdentityClient, s.issuer)
	if err != nil {
		return immichResult{}, err
	}
	everyday, err := everydaySubject(ctx, s.export, everydayUser{Bundle: s.first, Username: s.everyday.Username})
	if err != nil {
		return immichResult{}, err
	}
	var b immichBundle
	b.Schema, b.SetupID, b.ServiceKeyFile = "immich.setup/1", s.i.SetupID(), immichServiceKey
	b.Provider.Issuer, b.Provider.ClientID, b.Provider.ClientSecret = frame.IssuerURL, frame.ClientID, frame.ClientSecret
	b.Provider.RolesClaim, b.Provider.MobileRedirectURI = immichRolesClaim, s.mobile
	b.Administrator = immichAccount{Subject: frame.Administrator.Subject, Email: s.admin.Email, Name: frame.Administrator.Username}
	b.Everyday = immichAccount{Subject: everyday, Email: s.everyday.Email, Name: s.everyday.Username}
	bundle, err := json.Marshal(b)
	defer clear(bundle)
	if err != nil {
		return immichResult{}, err
	}
	mode := "write"
	if check {
		mode = "check"
	}
	if s.place(ctx, []string{mode, s.i.Bundle}, bundle) != nil {
		probe := func(ctx context.Context) ([]byte, error) {
			return s.shell(ctx, []string{"-c", probeScript, "sh", s.i.Bundle})
		}
		return immichResult{}, placeRefusal(ctx, probe, "Immich's setup bundle", path.Join(s.setup, strings.TrimPrefix(s.i.Bundle, immichSetupRoot)), s.target)
	}
	r, err := immichApply(ctx, s.exec, s.i.Bundle, check)
	if err != nil {
		return immichResult{}, err
	}
	if r.SetupID != b.SetupID || r.Accounts[0].Subject != b.Administrator.Subject || r.Accounts[1].Subject != b.Everyday.Subject {
		return immichResult{}, errors.New("Immich reports another installation or other owner accounts than its bundle names")
	}
	if !check {
		verified, err := s.export(ctx, exportArgs)
		defer clear(verified)
		if err != nil || !bytes.Equal(raw, verified) {
			return immichResult{}, errors.New("source credential ownership changed during transfer; destination files are retained")
		}
	}
	return immichResult{report: r, issuer: frame.IssuerURL, clientID: frame.ClientID}, nil
}

// immichRuntime is the bridge's ky.offboarding-runtime/1 configuration for Immich at origin.
// Its files are relative to /directory/config; it registers the owner's two accounts.
func immichRuntime(r immichResult, origin string) []byte {
	type account struct {
		NativeUUID string `json:"native_uuid"`
		Subject    string `json:"subject"`
	}
	type app struct {
		App              string `json:"app"`
		Kind             string `json:"kind"`
		Origin           string `json:"origin"`
		APIOrigin        string `json:"api_origin"`
		Issuer           string `json:"issuer"`
		ClientID         string `json:"client_id"`
		ProductTokenFile string `json:"product_token_file"`
		SCIMTokenFile    string `json:"scim_token_file"`
		Directory        struct {
			AdministratorRole string `json:"administrator_role"`
			EverydayRole      string `json:"everyday_role"`
			CreateAccounts    bool   `json:"create_accounts"`
		} `json:"directory"`
		Accounts []account `json:"accounts"`
	}
	a := app{App: "immich", Kind: "immich", Origin: origin, APIOrigin: "http://immich-server:2283", Issuer: r.issuer, ClientID: r.clientID,
		ProductTokenFile: "immich-token", SCIMTokenFile: "scim-token"}
	a.Directory.AdministratorRole, a.Directory.EverydayRole, a.Directory.CreateAccounts = "admin", "user", true
	for _, x := range r.report.Accounts {
		a.Accounts = append(a.Accounts, account{x.ID, x.Subject})
	}
	b, err := json.Marshal(struct {
		Schema   string `json:"schema"`
		SetupID  string `json:"setup_id"`
		Listen   string `json:"listen"`
		StateDir string `json:"state_dir"`
		CertFile string `json:"cert_file"`
		KeyFile  string `json:"key_file"`
		Apps     []app  `json:"apps"`
	}{"ky.offboarding-runtime/1", r.report.SetupID, "0.0.0.0:8443", immichBridgeState, "tls.crt", "tls.key", []app{a}})
	if err != nil {
		panic(err) // plain typed metadata always marshals
	}
	return append(b, '\n')
}

// The bridge's directories inside its container, ./directory on the target.
const (
	immichBridgeConfig = "/directory/config/runtime.json"
	immichBridgeState  = "/directory/state"
)

// immichDirectoryStep (immich.directory) configures and starts the bridge from Immich's
// verified setup: its tokens are copied on the target, never through the workstation, and
// never replace a file that differs; so is its configuration, so a different binding is
// refused. It creates the bridge's TLS on the target and waits for it to be healthy.
type immichDirectoryStep struct {
	setup  immichSetupStep // its read-only check names the accounts and client
	app    dockerhost.App  // Immich's deployment; its Redact takes the bridge's output
	origin string          // https://<route URL host>
}

func (s immichDirectoryStep) ID() string { return "immich.directory" }
func (s immichDirectoryStep) InputHash() string {
	sum := sha256.Sum256([]byte(s.setup.InputHash() + "\x00" + s.origin))
	return hex.EncodeToString(sum[:])
}
func (s immichDirectoryStep) Inspect(ctx context.Context) (bool, error) {
	return s.Verify(ctx) == nil, nil
}

// files places the bridge's tokens and configuration with placeScript's mode on the target.
func (s immichDirectoryStep) files(ctx context.Context, mode string) error {
	r, err := s.setup.run(ctx, true)
	if err != nil {
		return err
	}
	dir := path.Join(s.app.Root, s.app.Name)
	config := path.Join(dir, "directory/config")
	if mode == "write" { // a check creates nothing: placeScript's check fails on a missing file
		for _, d := range []string{config, path.Join(dir, "directory/state")} {
			if err := dockerhost.PrivateDir(ctx, s.app.Runner, d); err != nil {
				return err
			}
		}
	}
	place := func(f string) string {
		return "sh -c " + remote.Quote(placeScript) + " sh " + remote.Quote(mode) + " " + remote.Quote(path.Join(config, f))
	}
	refused := func(f, what string) error {
		where := path.Join(config, f)
		probe := func(ctx context.Context) ([]byte, error) {
			return s.app.Runner.Run(ctx, "sh -c "+remote.Quote(probeScript)+" sh "+remote.Quote(where), nil)
		}
		return placeRefusal(ctx, probe, what, where, s.setup.target)
	}
	// Each token is read into the shell (exit 3 if it cannot be) and handed to placeScript by
	// the printf builtin: in no argv, no output. The service key is read inside the server,
	// whose view of a symlink cannot reach the host; the SCIM token is the installer's own.
	scim := path.Join(dir, "secrets/scim_token")
	key := path.Join(s.setup.setup, strings.TrimPrefix(immichServiceKey, immichSetupRoot))
	for _, c := range []struct{ file, what, read, source string }{
		{"immich-token", "the bridge's copy of Immich's service key", `k=$(` + dockerAppContainer("immich") + `docker exec "$c" cat ` + remote.Quote(immichServiceKey) + `) || exit 3`,
			"Immich's service key " + key + " on " + s.setup.target + " cannot be read through its running server; inspect it and the server (immich.setup writes it)"},
		{"scim-token", "the bridge's copy of the SCIM token", `f=` + remote.Quote(scim) + `; { [ ! -L "$f" ] && test -n "$(find "$f" -prune -type f -user "$(id -u)" -perm 600)" && k=$(cat "$f"); } || exit 3`,
			"the SCIM token " + scim + " on " + s.setup.target + " is missing, a symlink or not a private file of the target user; inspect it (immich.secrets generates it)"},
	} {
		_, err := s.app.Runner.Run(ctx, c.read+`; [ -n "$k" ] || exit 3; printf %s "$k" | `+place(c.file), nil)
		var ee *remote.ExitError
		switch {
		case errors.As(err, &ee) && ee.Code == 3:
			return errors.New(c.source)
		case err != nil:
			return refused(c.file, c.what)
		}
	}
	if _, err := s.app.Runner.Run(ctx, place("runtime.json"), immichRuntime(r, s.origin)); err != nil {
		return refused("runtime.json", "the bridge's configuration")
	}
	return nil
}

// probeScript says, reading only, why placeScript refused $1: unsafe (a symlink or not a
// regular file), differs, or missing.
const probeScript = `f=$1
if [ -L "$f" ] || { [ -e "$f" ] && [ ! -f "$f" ]; }; then echo unsafe; elif [ -e "$f" ]; then echo differs; else echo missing; fi`

// placeRefusal is the error for a refused placement of what at where on target: which of
// missing, unsafe or differs it is (probe tells), and what the operator does.
func placeRefusal(ctx context.Context, probe func(context.Context) ([]byte, error), what, where, target string) error {
	out, err := probe(ctx)
	why := "could not be inspected"
	if err == nil {
		why = map[string]string{
			"missing": "is missing and could not be written (is its directory the target user's own and closed to others?)",
			"unsafe":  "is a symlink or not a regular file",
			"differs": "differs from what this apply writes (another installation, KyIdentity client or owner accounts) and is never replaced",
		}[strings.TrimSpace(string(out))]
	}
	if why == "" {
		why = "could not be inspected"
	}
	return fmt.Errorf("%s %s on %s %s. Inspect %s; move it aside only if you are sure it is stale, then rerun apply", what, where, target, why, where)
}

// bridge runs the bridge once with args, beside the running one. A failure carries the
// bridge's last output line, redacted, and where its logs are.
func (s immichDirectoryStep) bridge(ctx context.Context, args, failed string) error {
	out, err := s.app.Runner.Run(ctx, s.app.Compose("run --rm --no-deps -T directory --config "+immichBridgeConfig+" "+args), nil)
	if err == nil {
		return nil
	}
	var ee *remote.ExitError
	if errors.As(err, &ee) {
		out = append(out, ee.Stderr...)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	last := s.app.Redact.Redact(strings.TrimSpace(lines[len(lines)-1]))
	if len(last) > 300 {
		last = last[:300]
	}
	if last == "" {
		last = "no output"
	}
	return fmt.Errorf("%s (kyoffboard: %s). On %s, docker compose -p kyq-immich logs directory (run in %s) shows the running bridge's log", failed, last, s.setup.target, path.Join(s.app.Root, s.app.Name))
}

func (s immichDirectoryStep) Apply(ctx context.Context) error {
	if err := s.files(ctx, "write"); err != nil {
		return err
	}
	if err := s.bridge(ctx, "--init-tls", "the bridge could not create its TLS key and certificate in directory/config"); err != nil {
		return err
	}
	// Recreated: before its configuration existed it could only restart, failing.
	if _, err := s.app.Runner.Run(ctx, s.app.Compose("up -d --no-deps --force-recreate directory"), nil); err != nil {
		return fmt.Errorf("docker compose up directory: %w", err)
	}
	return dockerhost.WaitHealthy(ctx, s.app, "directory")
}

// Verify only reads: files compared, the bridge's own --check and its health.
func (s immichDirectoryStep) Verify(ctx context.Context) error {
	if err := s.files(ctx, "check"); err != nil {
		return err
	}
	if err := s.bridge(ctx, "--check", "the bridge refuses its configuration (kyoffboard --check)"); err != nil {
		return err
	}
	return dockerhost.Healthy(ctx, s.app, "directory")
}

// immichPlan refuses, before connecting, an Immich whose setup could not run or would fail
// mid-apply: without its integration its first visitor could make themselves its
// administrator; its bundle belongs in the server's ./setup bind, never the library; the
// module takes only lowercase email addresses.
func immichPlan(st *stack.Stack) error {
	at := slices.IndexFunc(st.Apps, func(a stack.App) bool { return a.Name == "immich" })
	if at < 0 {
		return nil
	}
	i := slices.IndexFunc(st.Integrations, func(i stack.Integration) bool { return i.Product == "immich" && i.Installed(st) })
	if i < 0 {
		return fmt.Errorf("Immich needs its setup in the plan, or its first visitor could make themselves its administrator. Add this to integrations (the UUID is fresh; any UUID will do): {product: immich, target: %s, bundle: %s, identity_client: immich}", st.Apps[at].Target, immichSetupBundle(""))
	}
	if b := st.Integrations[i]; !strings.HasPrefix(b.Bundle, immichSetupRoot+"/installer/") {
		return fmt.Errorf("Immich's setup bundle %s is outside %s, the bind directory only its setup uses. Change it to: bundle: %s", b.Bundle, immichSetupRoot, immichSetupBundle(b.SetupID()))
	}
	for _, k := range st.Integrations {
		if k.Product != "kyidentity" || k.Identity == nil || !k.Installed(st) {
			continue
		}
		for _, a := range [][2]string{{"admin", k.Identity.Admin.Email}, {"everyday", k.Identity.Everyday.Email}} {
			if a[1] != strings.ToLower(a[1]) {
				return fmt.Errorf("Immich takes only lowercase email addresses. Change identity.%s.email %q to %q in the kyidentity integration", a[0], a[1], strings.ToLower(a[1]))
			}
		}
	}
	return nil
}

// immichSetupBundle is the bundle path for setup id, a fresh one when id is "".
func immichSetupBundle(id string) string {
	if id == "" {
		id = uuid.NewString()
	}
	return immichSetupRoot + "/installer/" + id + "/setup.json"
}

// immichReassign says what assigning a removed person to Immich again does.
const immichReassign = "  Assigning them again restores their sign-in to the same account, with none of their old\n  sessions or keys.\n"

// immichHandover is printed after every apply that installs Immich at host, forwarded to
// forward:port and its bridge to forward:port+1; server is the catalog's server image.
func immichHandover(host, admin, everyday, forward string, port int, server string) string {
	return fmt.Sprintf(`
Immich at https://%[1]s/: sign in through KyIdentity as %[2]s (administers Immich) or %[3]s
  (your photos). Use %[3]s for photos: Immich lets its administrator keep photos too, and
  %[2]s should only administer.
  Phone app: enter the server URL https://%[1]s, then choose "Login with OAuth".
  Anyone you assign to Immich in KyIdentity later signs in the same way; their account is
  created for them.
  Removing someone from Immich in KyIdentity ends their Immich sessions and API keys and keeps
  their account and photos.
`, host, admin, everyday) + immichReassign + fmt.Sprintf(`  Its accounts have no local password: every sign-in goes through KyIdentity.

  Notes:
  - Ports %[1]s:%[2]d (Immich) and %[1]s:%[3]d (its KyIdentity bridge) are open to your whole
    LAN: restrict both to the edge host at your firewall, with a rule tested on that host
    (docs/guided-setup.md, Catalog Immich on Docker). The edge reaches the bridge over TLS
    whose self-signed certificate it does not verify.
  - Immich connects out to GitHub (version check), Hugging Face (machine learning models) and
    tiles.immich.cloud (map tiles).
  - Source of its KyQuickStart module (AGPL):
    https://github.com/Busnes-app/KyQuickStart/tree/%[4]s/internal/adapters/immich/module
`, forward, port, port+1, immichSourceTag(server))
}

// immichSourceTag is the repository tag the server image was built from: image tag 3.2.0-ky.1
// is tag immich-v3.2.0-ky.1.
func immichSourceTag(image string) string {
	ref, _, _ := strings.Cut(image, "@")
	return "immich-v" + ref[strings.LastIndex(ref, ":")+1:]
}

// immichRestored is printed after a restore of Immich: thumbnails are not backed up.
const immichRestored = "immich: thumbnails were not backed up; Immich makes them again at its nightly task, or at once from Administration → Jobs → Generate Thumbnails.\n"
