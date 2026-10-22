package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
)

// bookstackIdentity is the oldest KyIdentity BookStack signs in through: 0.4.6 sends a removal
// made before a person's first delivery, and BookStack keeps every account a sign-in made.
const bookstackIdentity = "0.4.6"

// pendingPlan refuses, before connecting, an app whose image is not published yet.
func pendingPlan(st *stack.Stack, cat catalog.Catalog) error {
	for _, a := range st.Apps {
		if p := cat[a.Name].PendingImages; len(p) > 0 {
			return fmt.Errorf("app %q cannot be installed yet: its %s image has no published release. Nothing was changed; remove %s from the plan until a release of this installer can install it", a.Name, strings.Join(p, ", "), a.Name)
		}
	}
	return nil
}

// bookstackPlan refuses, before connecting, a BookStack without its KyIdentity client or its
// edge's egress_address, or whose KyIdentity is older than bookstackIdentity or unknown.
func bookstackPlan(st *stack.Stack, cat catalog.Catalog) error {
	k := slices.IndexFunc(st.Apps, func(a stack.App) bool { return a.Name == "bookstack" })
	switch {
	case k < 0:
		return nil
	case slices.ContainsFunc(st.Edges, func(e stack.Edge) bool {
		return e.EgressAddress == "" && slices.ContainsFunc(e.Routes, func(r stack.Route) bool { return r.App == "bookstack" })
	}):
		return errors.New(`app "bookstack" trusts exactly the address its edge connects from (APP_PROXIES): set egress_address on its edge to the edge's own address as its target sees it (for NPM on a cluster, the IP of the node running NPM)`)
	case !slices.ContainsFunc(st.Integrations, func(i stack.Integration) bool { return i.Product == "bookstack" && i.Installed(st) }):
		return fmt.Errorf("bookstack signs in through KyIdentity alone and its receiver needs KyIdentity's client: add {product: bookstack, target: %s, bundle: /data/installer/<setup UUID>/setup.json, identity_client: bookstack} to integrations", st.Apps[k].Target)
	case slices.ContainsFunc(st.Existing, func(e stack.Existing) bool { return e.Product == "kyidentity" }):
		return identityTooOld("unknown (an existing KyIdentity this plan does not deploy)")
	case !catalog.VersionAtLeast(cat["kyidentity"].Version, bookstackIdentity):
		return identityTooOld(cat["kyidentity"].Version + ", the version this plan installs")
	}
	return nil
}

func identityTooOld(found string) error {
	return suite.Errorf(suite.Dependency, "bookstack needs KyIdentity v%s or later, which sends a removal made before a person's first sign-in; found: %s. Upgrade KyIdentity first; nothing was changed", bookstackIdentity, found)
}

// refuseOldIdentity refuses BookStack when this run leaves an installed KyIdentity older than
// bookstackIdentity in place: one it does not redeploy, whose deployment record is older.
func (s *session) refuseOldIdentity(ctx context.Context) error {
	if !slices.Contains(s.order, "bookstack") || slices.Contains(s.order, "kyidentity") {
		return nil
	}
	d, err := s.drivers[s.placed["kyidentity"].Name].deployed(ctx, "kyidentity")
	if err != nil {
		return fmt.Errorf("kyidentity: %w", err)
	}
	if !catalog.VersionAtLeast(d.Version, bookstackIdentity) {
		found := d.Version
		if !d.Found || found == "" {
			found = "unknown (no deployment record)"
		}
		return identityTooOld(found)
	}
	return nil
}

// bookstackSteps are the catalog BookStack's steps on its Docker target: bookstack.setup before
// its deploy, bookstack.bootstrap and bookstack.roles after its health.
func (s *session) bookstackSteps(d driver, i stack.Integration) (before, after []engine.Step, err error) {
	host, ok := d.(sshDriver)
	if !ok {
		return nil, nil, fmt.Errorf("%s runs on a Docker target only", i.Product)
	}
	source, export, err := s.clientSource(i)
	if err != nil {
		return nil, nil, err
	}
	app := s.cat[i.Product]
	a := host.app(i.Product, app, s.o.ReleaseSet)
	receiver := func(ctx context.Context, args ...string) error {
		_, err := host.c.Run(ctx, dockerServiceContainer(i.Product, "directory")+`docker exec "$c" kyoffboard `+strings.Join(args, " "), nil)
		return err
	}
	setup := bookstackSetupStep{app: i.Product, target: host.t.Name, client: i.IdentityClient, source: source,
		issuer: s.cat["kyidentity"].Kubernetes.Env[issuerEnv], export: export, file: hostFile(host.c, path.Join(host.t.Root, i.Product)), redact: s.redact}
	boot := bootstrapStep{app: i.Product, target: host.t.Name, check: func(ctx context.Context) error { return receiver(ctx, "bookstack-check") },
		run: func(ctx context.Context, cmd string) error { _, err := host.c.Run(ctx, cmd, nil); return err }, compose: a.Compose}
	roles := rolesStep{app: i.Product, admin: app.OIDC.AdminRole, user: app.OIDC.UserRole, receiver: receiver}
	return []engine.Step{setup}, []engine.Step{boot, roles}, nil
}

// clientSource is KyIdentity's integration that carries app i's client (its first setup, or
// the amendment that added the client) and its apply-setup, which exports the client.
func (s *session) clientSource(i stack.Integration) (stack.Integration, identityExec, error) {
	source := s.firstIdentity()
	if a := slices.IndexFunc(s.amendments, func(a identityAmendment) bool { return a.app == i.Product }); a >= 0 {
		source = s.amendments[a].source
	}
	export, _, err := nativeProductExec(s.drivers[s.placed["kyidentity"].Name], source, productAdapters["kyidentity"])
	return source, export, err
}

// bookstackSetupStep (bookstack.setup) places, before BookStack first starts, what its
// environment reads from files: KyIdentity's issuer, the client's ID and secret, and the
// directory signing secret the receiver verifies. Each is written once, from KyIdentity's
// export in memory, and never replaced.
type bookstackSetupStep struct {
	app, target, client, issuer string
	source                      stack.Integration
	export                      identityExec
	file                        func(ctx context.Context, mode, rel string, data []byte) error
	redact                      *engine.Redactor
}

func (s bookstackSetupStep) ID() string { return s.app + ".setup" }
func (s bookstackSetupStep) InputHash() string {
	sum := sha256.Sum256([]byte(s.client + "\x00" + s.issuer + "\x00" + s.source.Bundle))
	return hex.EncodeToString(sum[:])
}
func (s bookstackSetupStep) Inspect(ctx context.Context) (bool, error) {
	return s.Verify(ctx) == nil, nil
}
func (s bookstackSetupStep) Apply(ctx context.Context) error  { return s.place(ctx, "write") }
func (s bookstackSetupStep) Verify(ctx context.Context) error { return s.place(ctx, "check") }

func (s bookstackSetupStep) place(ctx context.Context, mode string) error {
	if err := identityCapabilities(ctx, s.export, true, "client-credential-export"); err != nil {
		return err
	}
	raw, err := s.export(ctx, []string{"apply-setup", "--file", s.source.Bundle, "--export-client", s.client})
	frame, ferr := readClientCredentials(raw, s.source, s.client, s.issuer)
	clear(raw)
	if err != nil || ferr != nil || frame.DirectorySecret == "" {
		return fmt.Errorf("KyIdentity's private export of client %s failed or does not match this plan", s.client)
	}
	s.redact.Add(frame.ClientSecret)
	s.redact.Add(frame.DirectorySecret)
	for _, f := range []struct{ rel, value string }{
		{"data/oidc_issuer", s.issuer}, {"data/oidc_client_id", s.client},
		{"data/oidc_client_secret", frame.ClientSecret}, {"data/directory_secret", frame.DirectorySecret},
	} {
		if err := s.file(ctx, mode, f.rel, []byte(f.value)); err != nil {
			return fmt.Errorf("%s on %s is missing, unsafe or differs from KyIdentity's current client; retained files are never replaced", f.rel, s.target)
		}
	}
	return nil
}

// bookstackService is the service account's address: BookStack's seeded admin keeps it, so
// create-admin --initial re-keys that account on every bootstrap.
const bookstackService = "admin@admin.com"

// bootstrapContainer is the one-off standard-auth BookStack, by a name the bootstrap client
// reaches it at.
const bootstrapContainer = "kyq-bookstack-standard"

// bootstrapPoll spaces the checks for the one-off BookStack's readiness.
var bootstrapPoll = 3 * time.Second

// bootstrapStep (bookstack.bootstrap) gives the receiver a working token of BookStack's
// service account, the seeded admin@admin.com. It is done while that token works for an account
// that holds the admin role. Otherwise it stops BookStack, starts a one-off standard-auth
// BookStack with no published port, re-keys the service account with create-admin and pipes
// its one-time password straight into the receiver image's sign-in client, which replaces
// the account's tokens, sets Editor as the default role and writes the token; the one-off is
// removed and BookStack started again. The password is never stored, and under oidc no
// password signs in.
type bootstrapStep struct {
	app, target string
	check       func(context.Context) error
	run         func(ctx context.Context, cmd string) error
	compose     func(args string) string
}

func (s bootstrapStep) ID() string                                { return s.app + ".bootstrap" }
func (s bootstrapStep) InputHash() string                         { return "bootstrap" }
func (s bootstrapStep) Inspect(ctx context.Context) (bool, error) { return s.check(ctx) == nil, nil }

func (s bootstrapStep) Verify(ctx context.Context) error {
	if s.check(ctx) != nil {
		return fmt.Errorf("the receiver's BookStack token on %s is refused, or not the service account's; rerun apply, which bootstraps it again", s.target)
	}
	return nil
}

func (s bootstrapStep) Apply(ctx context.Context) (err error) {
	remove := "docker rm -f " + bootstrapContainer + " >/dev/null 2>&1; true"
	// A run that died left the one-off: remove it first, and always after.
	defer func() {
		err = errors.Join(err, finish(ctx, func(ctx context.Context) error { return s.run(ctx, remove) }))
	}()
	stages := []struct{ what, cmd string }{
		{"stop BookStack", s.compose("stop bookstack")},
		{"remove a leftover one-off BookStack", remove},
		{"start the one-off standard-auth BookStack", s.compose("run -d --name "+bootstrapContainer+" standard") + " >/dev/null"},
	}
	for _, st := range stages {
		if err := s.run(ctx, st.cmd); err != nil {
			return fmt.Errorf("%s on %s: %w", st.what, s.target, err)
		}
	}
	for deadline := time.Now().Add(10 * time.Minute); s.run(ctx, "docker exec "+bootstrapContainer+" curl -fsS -o /dev/null http://127.0.0.1/status") != nil; {
		if time.Now().After(deadline) {
			return fmt.Errorf("the one-off BookStack on %s did not answer within 10 minutes; see docker logs %s", s.target, bootstrapContainer)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(bootstrapPoll):
		}
	}
	rekey := "docker exec " + bootstrapContainer + " /usr/bin/with-contenv php /app/www/artisan bookstack:create-admin --initial --email=" +
		bookstackService + " --name='KyQuickStart service' --generate-password 2>/dev/null | (" + s.compose("run --rm -T bootstrap") + ")"
	if err := s.run(ctx, rekey); err != nil {
		return fmt.Errorf("re-key BookStack's service account on %s and mint its token: %w", s.target, err)
	}
	if err := s.run(ctx, remove); err != nil {
		return err
	}
	if err := s.run(ctx, s.compose("up -d --wait --wait-timeout 600 bookstack")); err != nil {
		return fmt.Errorf("start BookStack again on %s: %w", s.target, err)
	}
	return nil
}

// rolesStep (bookstack.roles) maps BookStack's admin role, and only it, to the admin claim,
// through the receiver with the service account's token, and refuses another role that
// either claim would grant.
type rolesStep struct {
	app, admin, user string
	receiver         func(ctx context.Context, args ...string) error
}

func (s rolesStep) ID() string                                { return s.app + ".roles" }
func (s rolesStep) InputHash() string                         { return s.admin + " " + s.user }
func (s rolesStep) Inspect(ctx context.Context) (bool, error) { return s.Verify(ctx) == nil, nil }
func (s rolesStep) Apply(ctx context.Context) error           { return s.do(ctx, false) }
func (s rolesStep) Verify(ctx context.Context) error          { return s.do(ctx, true) }

func (s rolesStep) do(ctx context.Context, check bool) error {
	args := []string{"bookstack-roles", "--admin-claim", remote.Quote(s.admin), "--user-claim", remote.Quote(s.user)}
	if check {
		args = append(args, "--check")
	}
	if s.receiver(ctx, args...) != nil {
		return fmt.Errorf("BookStack's admin role is not mapped to %s alone, or another role would also be granted by %s or %s: in BookStack's Settings > Roles, rename that role or change its external authentication ID, then rerun apply", s.admin, s.admin, s.user)
	}
	return nil
}

// bookstackResidual is what the handover and docs/guided-setup.md say about offboarding.
const bookstackResidual = `Offboarding (disabling, deleting or unassigning the person in KyIdentity) removes every
  role from their BookStack account within seconds (if BookStack is down then, KyIdentity
  retries until it is back): their API tokens are refused and they can read and edit nothing. Their account and authorship stay. BookStack cannot end one person's
  web session: one already open stays signed in, with no roles, until it has been idle for
  60 minutes, and meanwhile they can still change their own name, email, language and profile
  image. The edge refuses self-delete; from a LAN host that reaches BookStack's published port
  directly, that open session could still delete its own account (blanking the authorship of
  its pages) until it idles.`

// bookstackHandover is printed after every apply that installs BookStack.
func bookstackHandover(host, admin, everyday string) string {
	return fmt.Sprintf(`
BookStack at https://%[1]s/: sign in through KyIdentity as %[2]s (BookStack's Admin role) or %[3]s
  (Editor). Anyone else KyIdentity assigns to BookStack gets Editor at first sign-in; a second
  administrator needs KyIdentity's bookstack.admin role, which installer reruns then refuse as an
  extra role holder. Its service account (admin@admin.com, Admin) holds the receiver's API
  token; it cannot sign in, and every bootstrap gives it a new password nobody keeps.
  %[4]s
  After a restore, someone removed after the snapshot has their roles and API token back until
  KyIdentity's replay is delivered, seconds after BookStack starts; if it is not, restore
  stops BookStack again.
  All residuals: docs/guided-setup.md, "BookStack: provenance, measurements and residuals".
`, host, admin, everyday, bookstackResidual)
}
