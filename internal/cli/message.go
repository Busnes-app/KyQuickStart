package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	"go.yaml.in/yaml/v3"
)

// providerID is the MAS upstream provider ID of an installation's chat client: a ULID
// derived from installation_id, so KyIdentity's redirect URI is known before matrix-init runs
// and stays the same on every rerun. It is no secret.
func providerID(installation, app string) string {
	sum := sha256.Sum256([]byte(installation + "\x00" + app + "-mas"))
	n := new(big.Int).SetBytes(sum[:16])
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	b := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		b[i] = crockford[new(big.Int).And(n, big.NewInt(31)).Int64()]
		n.Rsh(n, 5)
	}
	return string(b)
}

// chatClientFor is the everyday-only client an app's chat_oidc describes at its route host.
func chatClientFor(id, host, provider string, o *catalog.ChatOIDC) stack.IdentityClient {
	return stack.IdentityClient{ID: id, Name: o.Name, EverydayRole: o.EverydayRole,
		RedirectURIs: []string{"https://" + host + strings.ReplaceAll(o.RedirectPath, catalog.ProviderIDRef, provider)}}
}

// roleHost is the URL host of app's route with role, the primary for "".
func roleHost(st *stack.Stack, app, role string) string {
	for _, e := range st.Edges {
		for _, r := range e.Routes {
			if r.App == app && r.Role == role {
				return e.URLHost(r)
			}
		}
	}
	return ""
}

// ownerUsername is the username rule KyMessages' and KyCalendar's setups apply.
func ownerUsername(u string) bool {
	return calendarUsername.MatchString(u) && !strings.EqualFold(u, "admin")
}

// messagePlan refuses, before any connection, a plan its chat stack could not install:
// no installation_id (the provider ID derives from it), a missing or other chat client, or an
// owner username KyMessages' setup refuses.
func messagePlan(st *stack.Stack, cat catalog.Catalog) error {
	for _, i := range st.Integrations {
		o := cat[i.Product].ChatOIDC
		switch {
		case !i.Installed(st) || i.Product == "kyidentity":
			continue
		case o == nil && i.ChatClient != "":
			return fmt.Errorf("integration %s: chat_client is only for an app with a chat client (KyMessages)", i.Product)
		case o == nil:
			continue
		case i.ChatClient != i.Product+"-"+o.ClientSuffix:
			return fmt.Errorf("integration %s: chat_client must be %s-%s, the KyIdentity client of %s", i.Product, i.Product, o.ClientSuffix, o.Name)
		case st.InstallationID == "":
			// apply would add one, but only after this check: say what to write instead.
			return fmt.Errorf("%s derives its chat provider ID from installation_id, which this plan lacks: add the line installation_id: %s to stack.yaml (a fresh one; kyquickstart plan also writes one)", i.Product, stack.NewInstallationID())
		}
		for _, k := range st.Integrations {
			if k.Product == "kyidentity" && k.Identity != nil && k.Installed(st) {
				for _, a := range [][2]string{{"admin", k.Identity.Admin.Username}, {"everyday", k.Identity.Everyday.Username}} {
					if !ownerUsername(a[1]) {
						return fmt.Errorf(`KyMessages cannot take the KyIdentity %s username %q: its usernames are 3 to 64 letters, digits, ".", "_" or "-", and "admin" is its own break-glass account. Change identity.%s.username in the kyidentity integration`, a[0], a[1], a[0])
					}
				}
			}
		}
	}
	return nil
}

// mailPasswordFiles checks each identity.mail.password_file before preflight or apply
// connects. review does not, so a plan can be reviewed before the file exists.
func mailPasswordFiles(st *stack.Stack) error {
	for _, i := range st.Integrations {
		if i.Identity != nil && i.Identity.Mail != nil && i.Identity.Mail.PasswordFile != "" {
			pw, err := readMailPassword(i.Identity.Mail.PasswordFile)
			clear(pw)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// withChatClients names each installed chat app's chat client, which has one possible name.
func withChatClients(st *stack.Stack, cat catalog.Catalog) {
	for k, i := range st.Integrations {
		if o := cat[i.Product].ChatOIDC; o != nil && i.IdentityClient != "" && i.Installed(st) {
			st.Integrations[k].ChatClient = i.Product + "-" + o.ClientSuffix
		}
	}
}

// readMailPassword reads the workstation's SMTP password file: a regular 0600 file of this
// user, not a symlink, one line of at most 1024 bytes. The caller clears the result.
func readMailPassword(file string) ([]byte, error) {
	bad := func(why string) error {
		return fmt.Errorf("identity.mail.password_file %s %s. It must be a regular file of yours with mode 0600, not a symlink, holding one line of at most 1024 bytes", file, why)
	}
	// The checks are on the opened file. O_NONBLOCK: a FIFO is refused below, not waited on.
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, bad("does not exist")
	case errors.Is(err, syscall.ELOOP):
		return nil, bad("is a symlink")
	case err != nil:
		return nil, bad(fmt.Sprintf("cannot be opened (%v)", errors.Unwrap(err)))
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, bad("cannot be read")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.Mode().IsRegular():
		return nil, bad("is not a regular file")
	case !ok || int(st.Uid) != os.Getuid():
		return nil, bad("belongs to another user")
	case fi.Mode().Perm() != 0o600:
		return nil, bad(fmt.Sprintf("has mode %04o, not 0600 (chmod 600 %s)", fi.Mode().Perm(), file))
	}
	b := make([]byte, 1026)
	n, err := io.ReadFull(f, b)
	p := bytes.TrimSuffix(b[:n], []byte("\n"))
	why := ""
	switch {
	case err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF):
		why = "cannot be read"
	case len(p) == 0:
		why = "is empty"
	case len(p) > 1024:
		why = "is longer than 1024 bytes"
	case bytes.ContainsAny(p, "\r\n\x00"):
		why = "holds more than one line (or a CR or NUL byte)"
	}
	if why != "" {
		clear(b)
		return nil, bad(why)
	}
	return p, nil
}

// withRoutes fills a multi-route app's role references from its plan routes, after checking
// they cover its catalog roles exactly once each, on one edge and one forward_host, with one
// forward_port per published service port. The primary route has no role.
func withRoutes(st *stack.Stack, name string, t stack.Target, app catalog.App) (catalog.App, error) {
	var edge *stack.Edge
	byRole := map[string]stack.Route{}
	for i, e := range st.Edges {
		for _, r := range e.Routes {
			if r.App != name {
				continue
			}
			if r.Role == app.Primary() {
				return app, fmt.Errorf("app %q: route %s has role %q; remove the role: the %s is the route with no role", name, r.Hostname, r.Role, r.Role)
			}
			role := r.Role
			if role == "" {
				role = app.Primary()
			}
			if _, dup := byRole[role]; dup || !slices.ContainsFunc(app.Routes, func(c catalog.RouteRole) bool { return c.Role == role }) {
				return app, fmt.Errorf("app %q: route %s: role %q must be one of its roles, once", name, r.Hostname, r.Role)
			}
			if edge != nil && edge.Target != e.Target {
				return app, fmt.Errorf("app %q: all its routes go through one edge", name)
			}
			edge, byRole[role] = &st.Edges[i], r
		}
	}
	ports, byKey := map[int]string{}, map[string]string{} // forward_port -> service:port -> role
	var c catalog.Route
	for _, cr := range app.Routes {
		r, ok := byRole[cr.Role]
		switch {
		case !ok && cr.Role == app.Primary():
			return app, fmt.Errorf("app %q needs a route with no role: its %s (service %s, port %d)", name, cr.Role, cr.Service, cr.Port)
		case !ok:
			return app, fmt.Errorf("app %q needs a route with role %s (service %s, port %d)", name, cr.Role, cr.Service, cr.Port)
		case !stack.MediaAddress(r.ForwardHost):
			return app, fmt.Errorf("app %q: route %s: forward_host %s must be one LAN IPv4 address of %s that the edge reaches, not a wildcard, loopback, link-local, multicast, IPv6 address or a name: Docker publishes every routed port on it", name, r.Hostname, r.ForwardHost, t.Name)
		}
		key := fmt.Sprint(cr.Service, ":", cr.Port)
		if other, used := ports[r.ForwardPort]; used && other != key {
			return app, fmt.Errorf("app %q: forward_port %d is given to two services; each published service port needs its own", name, r.ForwardPort)
		}
		if role, seen := byKey[key]; seen && byRole[role].ForwardPort != r.ForwardPort {
			return app, fmt.Errorf("app %q: routes %s and %s are the same service port (%s) and need one forward_port: set both to %d", name, byRole[role].Hostname, r.Hostname, key, byRole[role].ForwardPort)
		}
		ports[r.ForwardPort], byKey[key] = key, cr.Role
		if r.ForwardHost != byRole[app.Primary()].ForwardHost {
			return app, fmt.Errorf("app %q: its routes forward to one forward_host, an address of %s", name, t.Name)
		}
		forward := r.ForwardHost + ":" + fmt.Sprint(r.ForwardPort)
		if cr.Role == app.Primary() {
			c = catalog.Route{Host: edge.URLHost(r), Hostname: r.Hostname, Forward: forward, ForwardAddr: r.ForwardHost, EdgeAddress: edge.EgressAddress, Roles: map[string]catalog.RoleAddress{}}
			continue
		}
		c.Roles[cr.Role] = catalog.RoleAddress{Host: edge.URLHost(r), Hostname: r.Hostname, Forward: forward}
	}
	for _, sa := range st.Apps {
		if sa.Name == name {
			c.MediaAddress = sa.MediaAddress
		}
	}
	return app.WithRoute(c), nil
}

// routePaths copies each route of a multi-route app with its catalog paths and deny_paths, and
// gives a catalog app's routes the catalog's status path: its primary route keeps a status
// path the plan set, and takes the catalog's instead of the default; any other route, or an
// app without one, gets none, which verify-edge skips.
const defaultStatusPath = "/api/status" // stack's default for a route without status_path

func routePaths(edges []stack.Edge, cat catalog.Catalog) []stack.Edge {
	out := slices.Clone(edges)
	for i := range out {
		out[i].Routes = slices.Clone(out[i].Routes)
		for j, r := range out[i].Routes {
			app := cat[r.App]
			role := r.Role
			if role == "" {
				role = app.Primary()
			}
			if len(app.Routes) == 0 {
				out[i].Routes[j].DenyPaths = app.DenyPaths
			}
			if _, ok := cat[r.App]; ok {
				switch {
				case role != app.Primary() || app.StatusPath == "":
					out[i].Routes[j].StatusPath = ""
				case r.StatusPath == "" || r.StatusPath == defaultStatusPath:
					out[i].Routes[j].StatusPath = app.StatusPath
				}
			}
			for _, cr := range app.Routes {
				if cr.Role == role {
					out[i].Routes[j].Paths, out[i].Routes[j].DenyPaths = cr.Paths, cr.DenyPaths
				}
			}
		}
	}
	return out
}

// hostFile is placeScript on a Docker host over SSH, as the target user, for a file under
// dir (the app's directory): it refuses a symlink anywhere on the way.
func hostFile(r remote.Runner, dir string) func(ctx context.Context, mode, rel string, data []byte) error {
	return func(ctx context.Context, mode, rel string, data []byte) error {
		if path.Clean(rel) != rel || path.IsAbs(rel) || strings.HasPrefix(rel, "..") {
			return errors.New("file outside the app's directory")
		}
		guard := "d=" + remote.Quote(dir) + "; [ ! -L \"$d\" ] || exit 1; "
		if parent := path.Dir(rel); parent != "." {
			for _, c := range strings.Split(parent, "/") {
				guard += "d=$d/" + remote.Quote(c) + "; [ ! -L \"$d\" ] || exit 1; "
			}
		}
		f := remote.Quote(path.Join(dir, rel))
		cmd := guard + "sh -c " + remote.Quote(placeScript) + " sh " + remote.Quote(mode) + " " + f
		if mode == "exists" {
			cmd = guard + "[ -f " + f + " ] && [ ! -L " + f + " ]"
		}
		_, err := r.Run(ctx, cmd, data)
		return err
	}
}

// matrixInitStep (<app>.matrix-init) seeds the MAS provider ID and runs matrix-init's first
// pass from the pinned image: every secret but KyIdentity's client secret, and every config but
// MAS's. matrix-init keeps an existing secret, so a rerun keeps the seed; a different seed
// refuses (provider_id_mismatch), and so does a matrix-init that replaced it.
type matrixInitStep struct {
	app, target string
	seed        string // the provider ID
	env         []string
	hash        string
	file        func(ctx context.Context, mode, rel string, data []byte) error
	run         func(ctx context.Context, env []string) error
}

const providerFile = "matrix/secrets/upstream_provider_id"

func (s matrixInitStep) ID() string                            { return s.app + ".matrix-init" }
func (s matrixInitStep) InputHash() string                     { return s.hash }
func (s matrixInitStep) Inspect(context.Context) (bool, error) { return false, nil }
func (s matrixInitStep) seedCheck(ctx context.Context, mode string) error {
	if s.file(ctx, mode, providerFile, []byte(s.seed)) != nil {
		return suite.Errorf(suite.ProviderIDMismatch, "%s on %s is missing, unsafe or not this installation's MAS provider ID %s; KyIdentity's chat client redirect names it, so it is never replaced. Restore it from a backup, or remove the plan's installation from that directory", providerFile, s.target, s.seed)
	}
	return nil
}

func (s matrixInitStep) Apply(ctx context.Context) error {
	if err := s.seedCheck(ctx, "write"); err != nil {
		return err
	}
	if err := s.run(ctx, s.env); err != nil {
		return err
	}
	return s.seedCheck(ctx, "check")
}

func (s matrixInitStep) Verify(ctx context.Context) error {
	if err := s.seedCheck(ctx, "check"); err != nil {
		return err
	}
	for _, f := range []string{"matrix/synapse/homeserver.yaml", "matrix/synapse/signing.key", "matrix/element/config.json", "matrix/federation/federation.yaml", "matrix/secrets/mas_admin_client_id"} {
		if s.file(ctx, "exists", f, nil) != nil {
			return fmt.Errorf("matrix-init left no %s on %s", f, s.target)
		}
	}
	return nil
}

// credentialsStep (<app>.credentials) takes both clients' secrets from KyIdentity's export
// in memory to private files on the app's host, before the app first starts: the console's
// client and directory secrets beside its bundle (and the directory secret again in secrets/,
// where the console's entrypoint reads its HMAC secret), MAS's client secret in matrix/secrets.
// Each is written once from stdin and never replaced. matrix-init's second pass then writes
// MAS's configuration.
type credentialsStep struct {
	app, target   string
	bundle        string // the console's, in the container's data directory
	dataDir       string // the container's path of ./data
	console, chat string // client IDs
	source        stack.Integration
	issuer        string
	hash          string
	export        identityExec
	file          func(ctx context.Context, mode, rel string, data []byte) error
	run           func(ctx context.Context) error
	redact        *engine.Redactor
}

func (s credentialsStep) ID() string                            { return s.app + ".credentials" }
func (s credentialsStep) InputHash() string                     { return s.hash }
func (s credentialsStep) Inspect(context.Context) (bool, error) { return false, nil }
func (s credentialsStep) Apply(ctx context.Context) error       { return s.place(ctx, "write") }
func (s credentialsStep) Verify(ctx context.Context) error      { return s.place(ctx, "check") }

func (s credentialsStep) place(ctx context.Context, mode string) error {
	if err := identityCapabilities(ctx, s.export, true, "client-credential-export"); err != nil {
		return err
	}
	frames := map[string]clientCredentials{}
	for _, id := range []string{s.console, s.chat} {
		raw, err := s.export(ctx, []string{"apply-setup", "--file", s.source.Bundle, "--export-client", id})
		frame, ferr := readClientCredentials(raw, s.source, id, s.issuer)
		clear(raw)
		if err != nil || ferr != nil {
			return fmt.Errorf("KyIdentity's private export of client %s failed or does not match this plan", id)
		}
		s.redact.Add(frame.ClientSecret)
		s.redact.Add(frame.DirectorySecret)
		frames[id] = frame
	}
	dir := strings.TrimPrefix(path.Dir(s.bundle), s.dataDir+"/")
	console, chat := frames[s.console], frames[s.chat]
	for _, f := range []struct{ rel, value string }{
		{path.Join("data", dir, "client.secret"), console.ClientSecret},
		{path.Join("data", dir, "directory.secret"), console.DirectorySecret},
		{"data/installer/directory_secret", console.DirectorySecret},
		{"matrix/secrets/kyidentity_client_secret", chat.ClientSecret},
	} {
		if err := s.file(ctx, mode, f.rel, []byte(f.value)); err != nil {
			return fmt.Errorf("%s on %s is missing, unsafe or differs from KyIdentity's current client; retained secrets are never replaced", f.rel, s.target)
		}
	}
	if mode == "check" {
		if s.file(ctx, "exists", "matrix/mas/config.yaml", nil) != nil {
			return fmt.Errorf("matrix-init's second pass left no matrix/mas/config.yaml on %s", s.target)
		}
		return nil
	}
	return s.run(ctx)
}

// chatCheck re-exports the chat client after setup and compares MAS's copy of its secret.
func (s credentialsStep) chatCheck(ctx context.Context) error {
	raw, err := s.export(ctx, []string{"apply-setup", "--file", s.source.Bundle, "--export-client", s.chat})
	frame, ferr := readClientCredentials(raw, s.source, s.chat, s.issuer)
	clear(raw)
	if err != nil || ferr != nil || s.file(ctx, "check", "matrix/secrets/kyidentity_client_secret", []byte(frame.ClientSecret)) != nil {
		return fmt.Errorf("KyIdentity's %s client changed during setup, or MAS's copy of its secret on %s differs; it is never replaced", s.chat, s.target)
	}
	return nil
}

// messageSteps are KyMessages' steps on its Docker target around its deploy: matrix-init and
// the credentials before it, its setup (the console's, then both clients rechecked) after.
func (s *session) messageSteps(d driver, i stack.Integration) (before []engine.Step, setup engine.Step, err error) {
	host, ok := d.(sshDriver)
	if !ok {
		return nil, nil, fmt.Errorf("%s runs on a Docker target only", i.Product)
	}
	steps, _, err := s.setupSteps(d, i)
	if err != nil {
		return nil, nil, err
	}
	step := steps[0].(clientSetupStep)
	app := s.cat[i.Product]
	issuer := s.cat["kyidentity"].Kubernetes.Env[issuerEnv]
	dir := path.Join(host.t.Root, i.Product)
	file := hostFile(host.c, dir)
	a := host.app(i.Product, app, s.o.ReleaseSet)
	env := []string{"KY_KYIDENTITY_ISSUER=" + issuer, "KY_MATRIX_MAS_CLIENT_ID=" + i.ChatClient}
	run := func(ctx context.Context, env []string) error {
		if _, err := dockerhost.RunInit(ctx, a, "matrix-init", env, nil); err != nil {
			return fmt.Errorf("matrix-init on %s: %w", host.t.Name, err)
		}
		return nil
	}
	seed := providerID(s.installation, i.Product)
	sum := sha256.Sum256([]byte(strings.Join(append([]string{string(app.Compose), seed}, env...), "\x00")))
	hash := hex.EncodeToString(sum[:])
	creds := credentialsStep{app: i.Product, target: host.t.Name, bundle: i.Bundle, dataDir: "/app/data", console: i.IdentityClient, chat: i.ChatClient,
		source: step.source, issuer: issuer, hash: step.InputHash(), export: step.export, file: file, redact: s.redact,
		run: func(ctx context.Context) error { return run(ctx, env) }}
	step.after = creds.chatCheck
	step.everyday = &everydayUser{Bundle: s.firstIdentity().Bundle, Username: s.firstIdentity().Identity.Everyday.Username}
	launch := launcherStep{app: i.Product, bundle: step.source.Bundle, client: i.ChatClient, url: "https://" + roleHost(s.plan, i.Product, "chat") + "/",
		name: app.OIDC.Name, console: i.IdentityClient, exec: step.export}
	return []engine.Step{matrixInitStep{app: i.Product, target: host.t.Name, seed: seed, env: env, hash: hash, file: file, run: run}, creds, launch}, step, nil
}

// launcherStep (<app>.launcher) sets KyIdentity's launcher cards: the chat client's links to
// Element (without a link it opens the client's redirect origin, MAS) under the app's name,
// and the console's is shown to its admins only. Display only: access does not change.
// KyIdentity writes each setting only while unset, so an admin's choice stays, and Verify
// reruns it. A KyIdentity without a setting's capability is left as it is; a rerun after its
// upgrade adds it.
type launcherStep struct {
	app, bundle, client, url, name, console string
	exec                                    identityExec
}

func (s launcherStep) ID() string { return s.app + ".launcher" }
func (s launcherStep) InputHash() string {
	return strings.Join([]string{s.bundle, s.client, s.url, s.name, s.console}, "\x00")
}
func (s launcherStep) Inspect(context.Context) (bool, error) { return false, nil }
func (s launcherStep) Verify(ctx context.Context) error      { return s.Apply(ctx) }
func (s launcherStep) Apply(ctx context.Context) error {
	for _, l := range []struct{ capability, flag, client, value string }{
		{"client-launch-urls", "launch-url", s.client, s.url},
		{"client-launcher-display", "launcher-name", s.client, s.name},
		{"client-launcher-display", "launcher-audience", s.console, "admins"},
	} {
		var missing missingCapability
		if err := identityCapabilities(ctx, s.exec, false, l.capability); errors.As(err, &missing) && string(missing) == l.capability {
			continue
		} else if err != nil {
			return err
		}
		// KyIdentity takes one launcher flag per call.
		b, err := s.exec(ctx, []string{"apply-setup", "--file", s.bundle, "--" + l.flag, l.client + "=" + l.value})
		var r struct{ Schema, Client, Status string }
		if err != nil || len(b) > 1024 || json.Unmarshal(b, &r) != nil || r.Schema != "kyidentity."+l.flag+"/1" || r.Client != l.client || r.Status != "set" && r.Status != "unchanged" {
			return fmt.Errorf("KyIdentity did not set its %s launcher card's %s to %s", l.client, l.flag, l.value)
		}
	}
	return nil
}

// directoryRetry spaces directoryStep's resyncs after a failed delivery.
var directoryRetry = 10 * time.Second

// directoryStep (<app>.directory), after the routes, waits until KyIdentity's connector has
// delivered the console's directory, so apply reports the app set up only once the console
// holds it. Defence in depth: KyIdentity 0.4.5 and earlier dropped a removal made before
// that first delivery, and the console keeps the owner's accounts its setup made.
type directoryStep struct {
	app  string
	sync func(ctx context.Context) (client, detail string)
}

func (s directoryStep) ID() string                            { return s.app + ".directory" }
func (s directoryStep) InputHash() string                     { return "resync" }
func (s directoryStep) Inspect(context.Context) (bool, error) { return false, nil }
func (s directoryStep) Verify(context.Context) error          { return nil }
func (s directoryStep) Apply(ctx context.Context) error {
	client, detail := s.sync(ctx)
	// KyIdentity's resync reports a refused delivery (the console or its route not ready yet)
	// as failed at once; resyncing again re-sends it.
	for deadline := time.Now().Add(resyncWait); strings.HasPrefix(detail, "failed") && time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(directoryRetry):
		}
		client, detail = s.sync(ctx)
	}
	if detail == "" {
		return nil
	}
	fix := "Fix the connector"
	if strings.HasPrefix(detail, "failed") {
		// kyidentity.setup creates the connector before the routes (the app's setup needs its
		// secrets), so a Cloudflare tunnel answers its first event 502 and KyIdentity fences
		// it; resync never releases a fence.
		fix = "Its first events may be fenced as uncertain writes: a Cloudflare tunnel answers 502 before " + s.app + "'s route exists. If " + s.app + "'s logs show they never arrived, resume the fenced connector in KyIdentity's admin UI (Connectors > Resume all expired)"
	}
	return fmt.Errorf("KyIdentity's %s connector has not delivered its directory to %s (%s); until it does, offboarding someone may not reach chat. %s, then rerun apply", client, s.app, detail, fix)
}

// firstIdentity is the installed KyIdentity's integration, which composeClients found.
func (s *session) firstIdentity() stack.Integration {
	return s.integrations[slices.IndexFunc(s.integrations, func(x stack.Integration) bool { return x.Product == "kyidentity" && x.Identity != nil })]
}

// refuseMovedServerName refuses a plan whose server name differs from the one an installed
// Synapse was set up with: every Matrix ID ends in it.
func (s *session) refuseMovedServerName(ctx context.Context) error {
	for name, t := range s.placed {
		app := s.cat[name]
		host, ok := s.drivers[t.Name].(sshDriver)
		if app.ChatOIDC == nil || !ok {
			continue
		}
		f := path.Join(t.Root, name, "matrix/synapse/homeserver.yaml")
		out, err := host.c.Run(ctx, "if [ -f "+remote.Quote(f)+" ]; then cat "+remote.Quote(f)+"; fi", nil)
		if err != nil {
			return fmt.Errorf("read %s's server name on %s: %w", name, t.Name, err)
		}
		var hs struct {
			ServerName string `yaml:"server_name"`
		}
		if len(out) == 0 {
			continue
		}
		if yaml.Unmarshal(out, &hs) != nil || hs.ServerName == "" {
			return fmt.Errorf("%s on %s has no readable server_name; nothing was changed", f, t.Name)
		}
		// The plan's routes, not s.edges: upgrade and install clear s.edges to leave NPM alone.
		want := ""
		for _, e := range s.plan.Edges {
			for _, r := range e.Routes {
				if r.App == name && r.Role == "server_name" {
					want = r.Hostname
				}
			}
		}
		if want != hs.ServerName {
			return suite.Errorf(suite.ServerNameChanged, "this plan changes %s's Matrix server name from %s to %s, but every Matrix ID (@name:%s) and room depends on it: it can never change after install. Restore the server_name route's hostname; nothing was changed", name, hs.ServerName, want, hs.ServerName)
		}
	}
	return nil
}

// initAdminLine replaces the console's break-glass password from stdin, on its target. The
// console's three file-only values come from KYQ_SECRETS, as at its start.
func initAdminLine(t stack.Target, app string) string {
	return "cd " + remote.Quote(path.Join(t.Root, app)) + " && docker compose -p kyq-" + app +
		` -f compose.yaml -f compose.kyq.yaml exec -T app /bin/sh -c 'eval "$KYQ_SECRETS" && exec /app/kymessages init-admin'`
}

// messageHandover is printed after every apply that installs KyMessages.
func messageHandover(st *stack.Stack, app string, t stack.Target, admin, everyday string) string {
	media := ""
	for _, a := range st.Apps {
		if a.Name == app {
			media = a.MediaAddress
		}
	}
	// Only KyIdentity's mailed link verifies an address the installer did not set.
	relay := `No mail relay is set, so only the owner's accounts are verified and nobody else
  can get a chat account: set a relay in KyIdentity's admin UI (Settings > Mail).`
	for _, i := range st.Integrations {
		if i.Product == "kyidentity" && i.Identity != nil && i.Identity.Mail != nil {
			relay = "KyIdentity sends that mail through " + net.JoinHostPort(i.Identity.Mail.Host, fmt.Sprint(i.Identity.Mail.Port)) + "."
		}
	}
	server := strings.Split(roleHost(st, app, "server_name"), ":")[0]
	compose := initAdminLine(t, app)
	return fmt.Sprintf(`
KyMessages chat at https://%[1]s/ (Element), its console at https://%[2]s/.
  Server name %[3]s: Matrix IDs are @<the local part of a verified email>:%[3]s, and the
  server name can never change.
  Chat: everyday accounts only (%[5]s). %[4]s signs in to the console alone and has no chat.
  KyIdentity's launcher: everyday accounts see one "KyMessages" card, which opens chat; the
  console's card shows to its admins only. A KyIdentity without launcher display settings
  shows both cards to everyone assigned; the next apply after its upgrade sets them.
  To add a person: in KyIdentity assign them to both "KyMessages" and "KyMessages chat", and
  make sure their email is verified (the installer's owner accounts are; anyone else redeems
  KyIdentity's verification mail). An unverified address gets no Matrix account.
  %[10]s
  Someone assigned to "KyMessages chat" alone is locked out of chat at the next sweep.
  Removing someone from "KyMessages chat" alone stops only their new chat sign-ins: their
  open sessions keep working. To end their chat, remove them from "KyMessages" too, or
  offboard them.
  Calls: media goes straight to %[6]s, TCP 7881 and UDP 7882, which are open to your LAN; no
  TURN is installed, so callers must reach that address. Federation is closed.
  A KyIdentity sign-out does not end chat sessions. Offboarding (disabling or deleting the
  person in KyIdentity) locks them out of chat and calls and ends their open chat sessions
  within seconds.
  Outstanding human step: replace the console's local "admin" break-glass password. Its
  one-time password is in the first lines of docker logs kyq-%[7]s-app-1 on %[8]s. On %[8]s,
  pipe a new one of at least 12 characters (stdin only, never on the command line) into:
    %[9]s
  No catalog product can pair with KyRecovery in this release; no backups are deposited in KyRecovery.
`, roleHost(st, app, "chat"), roleHost(st, app, ""), server, admin, everyday, media, app, t.Name, compose, relay)
}
