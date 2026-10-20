package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/google/uuid"
)

// catalogClient is the KyIdentity client an app's oidc block describes at its route host.
func catalogClient(id, host string, o *catalog.OIDC) (stack.IdentityClient, error) {
	if o == nil || o.Public || o.BackchannelLogoutPath != "" {
		return stack.IdentityClient{}, errors.New("KyIdentity's setup registers only confidential clients without back-channel logout")
	}
	origin := "https://" + host
	c := stack.IdentityClient{ID: id, Name: o.Name, AdministratorRole: o.AdminRole, EverydayRole: o.UserRole}
	for _, p := range o.RedirectPaths {
		c.RedirectURIs = append(c.RedirectURIs, origin+p)
	}
	for _, p := range o.LogoutPaths {
		c.PostLogoutURIs = append(c.PostLogoutURIs, origin+p)
	}
	if o.DirectoryPath != "" {
		c.SuiteWebhook = &stack.IdentityWebhook{URL: origin + o.DirectoryPath}
	}
	return c, nil
}

// routeHost is the URL host of app's one edge route, or its primary route.
func routeHost(st *stack.Stack, app string) (string, error) {
	var hosts []string
	for _, e := range st.Edges {
		for _, r := range e.Routes {
			if r.App == app && r.Role == "" {
				hosts = append(hosts, e.URLHost(r))
			}
		}
	}
	if len(hosts) != 1 {
		return "", fmt.Errorf("app %q signs in through its route hostname; give it exactly one edge route", app)
	}
	return hosts[0], nil
}

// composeClients adds each installed app's catalog client to the installed KyIdentity's
// setup intent, or checks a hand-written one against it. An app whose integration records an
// identity_amendment brings its client in that setup amendment instead.
func (s *session) composeClients(st *stack.Stack) error {
	s.integrations, s.derived, s.amendments, s.handover = st.Integrations, nil, nil, ""
	k := slices.IndexFunc(s.integrations, func(i stack.Integration) bool {
		return i.Product == "kyidentity" && i.Identity != nil && i.Installed(st)
	})
	var n stack.IdentityPreparation
	if k >= 0 {
		// Copy: the saved plan keeps only what the operator wrote.
		n = *s.integrations[k].Identity
		n.Clients = append([]stack.IdentityClient(nil), n.Clients...)
	}
	for _, i := range s.integrations {
		if i.IdentityClient == "" || !i.Installed(st) {
			continue
		}
		if k < 0 {
			return fmt.Errorf("%s's setup needs the plan to install KyIdentity with its identity settings", i.Product)
		}
		host, err := routeHost(st, i.Product)
		if err != nil {
			return err
		}
		console, err := catalogClient(i.IdentityClient, host, s.cat[i.Product].OIDC)
		if err != nil {
			return err
		}
		wanted := []stack.IdentityClient{console}
		if o := s.cat[i.Product].ChatOIDC; o != nil && i.ChatClient != "" {
			wanted = append(wanted, chatClientFor(i.ChatClient, roleHost(st, i.Product, o.Role), providerID(st.InstallationID, i.Product), o))
		}
		for _, want := range wanted {
			found := false
			for _, c := range n.Clients {
				if strings.EqualFold(c.ID, want.ID) {
					if i.IdentityAmendment != "" {
						return fmt.Errorf("KyIdentity client %s comes in setup amendment %s; remove it from identity.clients", c.ID, i.IdentityAmendment)
					}
					if !reflect.DeepEqual(c, want) {
						return fmt.Errorf("KyIdentity client %s differs from the catalog's %s client; remove it from identity.clients and apply derives it", c.ID, want.Name)
					}
					found = true
				}
			}
			if i.IdentityAmendment == "" {
				if !found {
					n.Clients = append(n.Clients, want)
				}
				s.derived = append(s.derived, want.ID)
			}
		}
		if i.IdentityAmendment != "" {
			s.amendments = append(s.amendments, newAmendment(i, s.integrations[k], wanted...))
		}
		switch i.Product {
		case "kyrecovery":
			text := fmt.Sprintf(recoveryCeremony, host, n.Admin.Username)
			if !s.placed["kyrecovery"].Kubernetes() {
				text = strings.ReplaceAll(text, "pod's log (kubectl -n kyq-kyrecovery logs deployment/app), and lost when that pod is replaced.", "container's log (docker logs kyq-kyrecovery-app-1 on its target), and lost when that container is replaced.")
			}
			s.handover += text
		case "kydrive":
			t := s.placed["kydrive"]
			s.handover += fmt.Sprintf(driveHandover, host, n.Admin.Username, path.Join(t.Root, "kydrive"), t.Name, n.Everyday.Username)
		case "kynotes":
			s.handover += fmt.Sprintf(notesHandover, host, n.Admin.Username, n.Everyday.Username)
		case "kydns":
			e, r := appRoute(st, "kydns")
			s.handover += fmt.Sprintf(dnsHandover, host, n.Admin.Username, net.JoinHostPort(r.ForwardHost, "53"), s.placed["kydns"].Name, r.ForwardHost, r.ForwardPort, e.EgressAddress)
		case "kyyard":
			s.handover += yardHandover(s.state, host, n.Admin.Username, s.placed["kyyard"])
		case "kycalendar":
			s.handover += calendarRuntimeHandover(s.state, host, n.Admin.Username, n.Everyday.Username, s.placed["kycalendar"])
		case "kypost":
			s.handover += fmt.Sprintf(postHandover, host, n.Admin.Username, n.Everyday.Username, postContainer, s.placed["kypost"].Name)
		case "kymessage":
			s.handover += messageHandover(st, i.Product, s.placed[i.Product], n.Admin.Username, n.Everyday.Username)
		case "kypulse":
			var unwatched []string
			s.pulseTargets, unwatched = pulseTargets(st, s.cat)
			if len(s.pulseTargets) > 32 {
				return errors.New("kyPulse's setup watches at most 32 apps")
			}
			text := pulseHandover(host, n.Admin.Username, s.pulseTargets, unwatched)
			if !s.placed["kypulse"].Kubernetes() {
				text = strings.ReplaceAll(text, "its first pod's log (kubectl -n kyq-kypulse logs deployment/app), and lost when that pod is replaced.", "its first container's log (docker logs kyq-kypulse-app-1 on its target), and lost when that container is replaced.")
			}
			s.handover += text
		}
	}
	if n.Mail != nil {
		// KyIdentity reads its copy of the relay password beside its preparation directories.
		m := *n.Mail
		m.TargetFile = path.Join(path.Dir(path.Dir(s.integrations[k].Bundle)), "smtp.password")
		n.Mail = &m
	}
	if len(s.derived) > 0 || n.Mail != nil {
		s.integrations = slices.Clone(s.integrations)
		s.integrations[k].Identity = &n
	}
	for _, name := range slices.Sorted(maps.Keys(s.placed)) {
		s.handover += backupLines(name, s.placed[name], s.cat[name])
	}
	return nil
}

// identityAmendment is the KyIdentity setup amendment that adds app's client after
// KyIdentity's first setup. source is KyIdentity's integration as that client's setup and
// export see it: the amendment's bundle path, setup ID and client, the parent's accounts.
type identityAmendment struct {
	app    string
	bundle json.RawMessage // staged on stdin
	source stack.Integration
}

func newAmendment(app, parent stack.Integration, c ...stack.IdentityClient) identityAmendment {
	p := parent.Identity
	source := parent
	source.Bundle = path.Join(path.Dir(path.Dir(parent.Bundle)), app.IdentityAmendment, "setup.json")
	source.Identity = &stack.IdentityPreparation{Schema: p.Schema, SetupID: app.IdentityAmendment, Admin: p.Admin, Everyday: p.Everyday, Clients: c}
	bundle, err := json.Marshal(struct {
		Schema  string                 `json:"schema"`
		SetupID string                 `json:"setupId"`
		Extends string                 `json:"extends"`
		Clients []stack.IdentityClient `json:"clients"`
	}{p.Schema, app.IdentityAmendment, p.SetupID, source.Identity.Clients})
	if err != nil {
		panic(err) // plain typed metadata always marshals
	}
	return identityAmendment{app: app.Product, bundle: bundle, source: source}
}

// amendmentID derives an amendment's setup UUID from its parent's and its client's, so a run
// that stops before publishing the plan stages the same amendment again.
func amendmentID(parent, client string) string {
	return uuid.NewSHA1(uuid.MustParse(parent), []byte(client)).String()
}

// amendLate records a setup amendment for each installed app whose catalog client the
// running KyIdentity's retained setup was prepared without, when that KyIdentity takes
// amendments, and returns apply's publish with them in the plan. It only reads the target.
func (s *session) amendLate(ctx context.Context, state string, prior func() error) (func() error, error) {
	t := s.placed["kyidentity"]
	k := slices.IndexFunc(s.integrations, func(i stack.Integration) bool {
		return i.Product == "kyidentity" && i.Identity != nil && i.Target == t.Name
	})
	d := s.drivers[t.Name]
	if k < 0 || d == nil || len(s.derived) == 0 {
		return prior, nil
	}
	if _, found, err := installedIdentityIssuer(ctx, d); err != nil || !found {
		return prior, err // not deployed: the first setup takes every client
	}
	i := s.integrations[k]
	exec, input, err := nativeProductExec(d, i, productAdapters["kyidentity"])
	if err != nil {
		return nil, err
	}
	// Only clients apply derived; a hand-written one belongs to the operator's intent.
	saved := s.plan.Integrations[k].Identity.Clients
	added := slices.DeleteFunc(slices.Clone(s.derived), func(id string) bool {
		return slices.ContainsFunc(saved, func(c stack.IdentityClient) bool { return strings.EqualFold(c.ID, id) })
	})
	if len(added) == 0 || identityCapabilities(ctx, exec, true, "setup-amendments") != nil || stageIdentity(ctx, exec, input, i.Identity, i.Bundle, true) == nil {
		return prior, nil // nothing late, or kyidentity.setup gives the refusal
	}
	late := staleClients(ctx, exec, input, i, added)
	if late == nil {
		return prior, nil
	}
	next := *s.plan
	next.Integrations = slices.Clone(next.Integrations)
	for j, a := range next.Integrations {
		if a.Product != "kyidentity" && a.Installed(&next) && (slices.Contains(late, a.IdentityClient) || a.ChatClient != "" && slices.Contains(late, a.ChatClient)) {
			next.Integrations[j].IdentityAmendment = amendmentID(i.Identity.SetupID, a.IdentityClient)
			fmt.Fprintf(s.o.Out, "KyIdentity setup %s ran without %s's client: adding it with setup amendment %s, recorded in stack.yaml when apply passes\n", i.Identity.SetupID, a.Product, next.Integrations[j].IdentityAmendment)
		}
	}
	if err := s.composeClients(&next); err != nil {
		return nil, err
	}
	installation := s.installation
	_, data, err := changePlan(state, s.revision, func(p *stack.Stack) {
		p.InstallationID = installation
		for j := range p.Integrations {
			p.Integrations[j].IdentityAmendment = next.Integrations[j].IdentityAmendment
		}
	})
	if err != nil {
		return nil, err
	}
	s.plan, s.changesPlan = &next, true
	revision := s.revision
	return func() error { return publish(state, revision, data) }, nil
}

// recoveryCeremony is printed after every apply that installs KyRecovery: apply cannot see
// whether the ceremony has run, and never runs it.
const recoveryCeremony = `
Outstanding human step, unless already done: the KyRecovery custodian ceremony.
  Sign in at https://%s/ through KyIdentity as %s, open /admin/ceremony, choose k of n and
  print the custodian cards. KyRecovery refuses every product pairing until then, so mint no
  pairing codes before it. Its local "admin" break-glass password is printed once, to its first
  pod's log (kubectl -n kyq-kyrecovery logs deployment/app), and lost when that pod is replaced.
`

// driveHandover is printed after every apply that installs KyDrive.
const driveHandover = `
KyDrive at https://%[1]s/: sign in through KyIdentity as %[2]s (its administrator) or %[5]s.
  KyIdentity's SCIM connector provisions both, and everyone later assigned to KyDrive, at its
  endpoint https://%[1]s/scim/v2; allow it a minute after apply. Its local "admin" break-glass
  password is in %[3]s/data/initial-admin-password on %[4]s: sign in once, change it and
  delete the file.
`

// notesHandover is printed after every apply that installs KyNotes.
const notesHandover = `
KyNotes at https://%[1]s/: sign in through KyIdentity as %[2]s (administers only; it cannot
  open notes) or %[3]s, whose account KyNotes creates at first sign-in. Assign KyIdentity role
  kynotes.user to anyone else who should sign in. KyNotes has no local password.
  Outstanding human step: on its Backups screen, as %[2]s, pair it with KyRecovery using a
  one-time code from KyRecovery's administrator, or pin the suite public key by hand. KyNotes
  makes no backups until then.
`

// calendarHandover is printed after every apply that installs KyCalendar; %[4]s is kubectl
// pointed at its target.
const calendarHandover = `
KyCalendar at https://%[1]s/: sign in through KyIdentity as %[2]s (its console only; it has no
  calendars) or %[3]s (calendars). Its setup created exactly these two; it admits no one else.
  Outstanding human steps:
  1. Its local "admin" break-glass account. Read its one-time password:
       %[4]s -n kyq-kycalendar exec deployment/app -- cat /app/data/initial-admin-password
     Sign in with it once at https://%[1]s/ and change it. Only after that, delete the file:
       %[4]s -n kyq-kycalendar exec deployment/app -- rm /app/data/initial-admin-password
  2. On its Backup & recovery screen, as %[2]s, pair it with KyRecovery using a one-time code
     from KyRecovery's administrator, or pin the suite public key by hand. It makes no backups
     until then.
`

// dnsHandover is printed after every apply that installs KyDNS; %[3]s is its DNS address,
// %[5]s:%[6]d where its admin UI is published, %[7]s its edge's egress_address.
const dnsHandover = `
KyDNS at https://%[1]s/: sign in through KyIdentity as %[2]s, the one account it admits
  (KyIdentity refuses everyone else). It answers DNS at %[3]s (UDP and TCP).
  Outstanding human steps:
  1. Point clients at %[5]s for DNS: your router's DHCP DNS server, or each client by hand.
  2. Its local recovery administrator has no password. From an interactive shell on %[4]s
     (ssh -t, so the password does not echo), set one:
       docker exec -it kyq-kydns-app-1 /usr/local/bin/kydns admin reset-password
     It asks for the password twice. Use it only to recover, at https://%[1]s/login.
  3. Pair it with KyRecovery on its Backups screen, signed in as that local administrator:
     %[2]s cannot enroll its first recovery key. It makes no backups until then.
  Notes:
  - Firewall: ports 53 and %[6]d are open on %[5]s. Its admin UI on %[6]d trusts forwarded
    requests only from egress_address %[7]s; apply checked that the edge is trusted and this
    workstation is refused. DNS answers the private ranges KyDNS sees. Both rest on the Docker
    host showing KyDNS real client addresses: restrict both ports to your LAN and the edge at
    the host or network firewall.
  - Never set egress_address to a Docker network gateway: every peer can look like it.
`

// appRoute is app's one edge route, or its primary, and its edge, which routeHost has found.
func appRoute(st *stack.Stack, app string) (stack.Edge, stack.Route) {
	for _, e := range st.Edges {
		for _, r := range e.Routes {
			if r.App == app && r.Role == "" {
				return e, r
			}
		}
	}
	return stack.Edge{}, stack.Route{}
}

// kubectl is the kubectl command line for target t, whose kubeconfig is relative to state.
func kubectl(state string, t stack.Target) string {
	kc := t.Kubeconfig
	if !filepath.IsAbs(kc) {
		kc = filepath.Join(state, kc)
	}
	if abs, err := filepath.Abs(kc); err == nil {
		kc = abs
	}
	cmd := "kubectl --kubeconfig " + remote.Quote(kc)
	if t.Context != "" {
		cmd += " --context " + remote.Quote(t.Context)
	}
	return cmd
}

// pulseTarget is a kyPulse monitoring target: an app's readiness path through its route.
type pulseTarget struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// pulseTargets watches every other app the plan installs through its one edge route, as a
// user reaches it. unwatched are the apps without a readiness path or a single route (NPM).
func pulseTargets(st *stack.Stack, cat catalog.Catalog) (targets []pulseTarget, unwatched []string) {
	targets = []pulseTarget{}
	for _, a := range st.Apps {
		if a.Name == "kypulse" { // kyPulse refuses its own origin
			continue
		}
		k := cat[a.Name].Kubernetes
		host, err := routeHost(st, a.Name)
		if k == nil || k.ReadinessPath == "" || err != nil {
			unwatched = append(unwatched, a.Name)
			continue
		}
		targets = append(targets, pulseTarget{Key: a.Name, Name: a.Name, URL: "https://" + host + k.ReadinessPath})
	}
	return targets, unwatched
}

// pulseHandover is printed after every apply that installs kyPulse.
func pulseHandover(host, admin string, targets []pulseTarget, unwatched []string) string {
	var names []string
	for _, t := range targets {
		names = append(names, t.Name)
	}
	out := fmt.Sprintf("\nkyPulse at https://%s/ (sign in through KyIdentity as %s) watches: %s.\n", host, admin, strings.Join(names, ", "))
	if len(unwatched) > 0 {
		out += fmt.Sprintf("  Not watched (no readiness path or single edge route): %s.\n", strings.Join(unwatched, ", "))
	}
	return out + `  Set its alert webhook in its UI. Its local "admin" break-glass password is printed once, to
  its first pod's log (kubectl -n kyq-kypulse logs deployment/app), and lost when that pod is replaced.
`
}

// clientSetupStep is apply's apply-setup for an installed Ky app that signs in through
// KyIdentity: KyRecovery, kyPulse, KyCalendar, KyDrive or KyPost. None of their 0.4.2 releases imports
// credentials, so the step places its bundle and KyIdentity's client and directory secrets as
// private files through its container's shell, never replacing a secret. KyIdentity may run on
// another target: the export crosses to the app's only in memory.
type clientSetupStep struct {
	i         stack.Integration
	name      string            // the app's name in KyIdentity, for messages
	source    stack.Integration // KyIdentity's with the client composeClients added, or its amendment's
	issuer    string
	scimToken string        // the app's own SCIM token file, which its bundle names; "" for none
	targets   []pulseTarget // kyPulse's monitoring extension; nil for any other app
	everyday  *everydayUser // KyCalendar's everyday account; nil for any other app
	export    identityExec  // KyIdentity's apply-setup
	exec      identityExec  // the app's apply-setup
	setup     func(context.Context, identityExec, string, bool) ([]byte, error)
	place     func(ctx context.Context, args []string, input []byte) error
	native    func(clientCredentials, string) any // KyPost's own bundle from the export and secret file; nil for ky.product-setup/1
	after     func(context.Context) error         // KyMessages' chat client recheck; nil for any other app
}

// everydayUser is the owner's everyday account, whose subject KyIdentity's read-only
// inspection of its first setup (bundle) reports: its export names only the administrator.
type everydayUser struct {
	Bundle, Username string
}

func (s clientSetupStep) ID() string { return s.i.Product + ".setup" }

func (s clientSetupStep) InputHash() string {
	in := []any{s.i, s.source, s.issuer}
	if s.targets != nil {
		in = append(in, s.targets)
	}
	if s.everyday != nil {
		in = append(in, s.everyday)
	}
	if s.scimToken != "" {
		in = append(in, s.scimToken)
	}
	b, err := json.Marshal(in)
	if err != nil {
		panic(err) // plain typed metadata always marshals
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s clientSetupStep) Inspect(ctx context.Context) (bool, error) {
	return s.Verify(ctx) == nil, nil
}

func (s clientSetupStep) Apply(ctx context.Context) error  { return s.run(ctx, false) }
func (s clientSetupStep) Verify(ctx context.Context) error { return s.run(ctx, true) }

// placeScript publishes $2 from stdin. write never replaces a file: an existing one must
// match. replace swaps in a regular file atomically. check only compares. Created directories
// and files are private to the pod's user.
const placeScript = `set -eu
f=$2
[ ! -L "$f" ]
if [ -e "$f" ] && [ "$1" != replace ]; then [ -f "$f" ] && cmp -s - "$f"; exit; fi
[ "$1" = write ] || [ "$1" = replace ]
umask 077
mkdir -p "${f%/*}"
t=$(mktemp "${f%/*}/.kyq.XXXXXX")
cat > "$t"
if [ "$1" = replace ]; then [ ! -e "$f" ] || [ -f "$f" ] || { rm -f "$t"; exit 1; }; mv -f "$t" "$f"; exit; fi
ln "$t" "$f" || { rm -f "$t"; exit 1; }
rm -f "$t"`

func (s clientSetupStep) run(ctx context.Context, check bool) error {
	if err := identityCapabilities(ctx, s.export, true, "client-credential-export"); err != nil {
		return err
	}
	exportArgs := []string{"apply-setup", "--file", s.source.Bundle, "--export-client", s.i.IdentityClient}
	raw, err := s.export(ctx, exportArgs)
	defer clear(raw)
	if err != nil {
		return errors.New("private source credential export failed")
	}
	frame, err := readClientCredentials(raw, s.source, s.i.IdentityClient, s.issuer)
	if err != nil {
		return err
	}
	var everyday []map[string]string
	if s.everyday != nil {
		subject, err := everydaySubject(ctx, s.export, *s.everyday)
		if err != nil {
			return err
		}
		everyday = []map[string]string{{"issuer": frame.IssuerURL, "subject": subject, "username": s.everyday.Username}}
	}
	mode, bundleMode := "write", "write"
	if s.targets != nil {
		// kyPulse's targets follow the plan: it keeps a target the bundle omits and refuses an
		// edited one, so its bundle may be replaced. Secrets never are.
		bundleMode = "replace"
	}
	if check {
		mode, bundleMode = "check", "check"
	}
	dir := path.Dir(s.i.Bundle)
	type file struct {
		name, mode string
		data       []byte
	}
	files := []file{{"client.secret", mode, []byte(frame.ClientSecret)}}
	sso := map[string]string{
		"issuerUrl": frame.IssuerURL, "clientId": frame.ClientID, "redirectUri": frame.RedirectURIs[0],
		"clientSecretFile": path.Join(dir, "client.secret"),
	}
	// readClientCredentials holds a directory secret exactly when the client has a directory.
	if frame.DirectorySecret != "" {
		sso["directorySecretFile"] = path.Join(dir, "directory.secret")
		files = append(files, file{"directory.secret", mode, []byte(frame.DirectorySecret)})
	}
	if s.scimToken != "" {
		sso["scimTokenFile"] = s.scimToken
	}
	b := map[string]any{
		"schema": "ky.product-setup/1", "service": s.i.Product, "setupId": s.i.SetupID(), "sso": sso,
		"admins": []map[string]string{{"issuer": frame.IssuerURL, "subject": frame.Administrator.Subject, "username": frame.Administrator.Username}},
	}
	if len(s.targets) > 0 {
		b["monitoring"] = map[string]any{"targets": s.targets}
	}
	if everyday != nil {
		b["everyday"] = everyday
	}
	var v any = b
	if s.native != nil {
		v = s.native(frame, path.Join(dir, "client.secret"))
	}
	bundle, err := json.Marshal(v)
	if err != nil {
		return err
	}
	// The bundle goes last: it names files that must already be in place.
	for _, f := range append(files, file{"setup.json", bundleMode, bundle}) {
		if err := s.place(ctx, []string{f.mode, path.Join(dir, f.name)}, f.data); err != nil {
			return fmt.Errorf("%s's %s is missing, unsafe or differs from KyIdentity's current client; retained secrets are never replaced", s.name, f.name)
		}
	}
	if _, err := s.setup(ctx, s.exec, s.i.Bundle, check); err != nil {
		return err
	}
	if s.after != nil {
		if err := s.after(ctx); err != nil {
			return err
		}
	}
	if !check {
		verified, err := s.export(ctx, exportArgs)
		defer clear(verified)
		if err != nil || !bytes.Equal(raw, verified) {
			return errors.New("source credential ownership changed during transfer; destination files are retained")
		}
	}
	return nil
}

// everydaySubject is u's KyIdentity subject: the native ID of the one user its read-only
// inspection of u.Bundle reports reused under u's exact username.
func everydaySubject(ctx context.Context, exec identityExec, u everydayUser) (string, error) {
	fail := fmt.Errorf("KyIdentity's read-only inspection of its first setup %s does not report everyday account %s exactly once; check that the plan's KyIdentity everyday username is the one that setup created", u.Bundle, u.Username)
	b, err := identitySetup(ctx, exec, u.Bundle, true)
	if err != nil {
		return "", fail
	}
	var r struct{ Created, Reused []productObject }
	if json.Unmarshal(b, &r) != nil || len(r.Created) != 0 {
		return "", fail
	}
	var ids []string
	for _, o := range r.Reused {
		if o.Kind == "user" && o.Name == u.Username {
			ids = append(ids, o.ID)
		}
	}
	if len(ids) != 1 {
		return "", fail
	}
	return ids[0], nil
}

// notesSetupStep (kynotes.setup) is apply's setup for the catalog KyNotes. Its distroless
// image has no shell, so it takes the client natively: stage the derived bundle, import
// KyIdentity's export on stdin, re-export to confirm the source, then apply-setup.
type notesSetupStep struct {
	i      stack.Integration // Notes derived from the catalog client and KyIdentity's issuer
	source stack.Integration // KyIdentity's with the client, or its amendment's
	export identityExec      // KyIdentity's apply-setup
	exec   identityExec      // KyNotes' apply-setup
	input  func(context.Context, []string, []byte) ([]byte, error)
}

func (s notesSetupStep) ID() string { return "kynotes.setup" }

func (s notesSetupStep) InputHash() string {
	b, err := json.Marshal([]any{s.i, s.source})
	if err != nil {
		panic(err) // plain typed metadata always marshals
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s notesSetupStep) Inspect(ctx context.Context) (bool, error) { return s.Verify(ctx) == nil, nil }

// Apply names the likeliest cause of a failure; the product's own output stays hidden.
func (s notesSetupStep) Apply(ctx context.Context) error {
	if err := s.run(ctx, false); err != nil {
		return fmt.Errorf("%w; no KyNotes route was added. Its container must resolve %s and trust its certificate (docs/guided-setup.md); rerun apply after fixing", err, s.i.Notes.SSO.IssuerURL)
	}
	return nil
}
func (s notesSetupStep) Verify(ctx context.Context) error { return s.run(ctx, true) }

func (s notesSetupStep) run(ctx context.Context, check bool) error {
	if err := transferNotesCredentials(ctx, s.export, s.exec, s.input, s.source, s.i, check); err != nil {
		return err
	}
	_, err := notesSetup(ctx, s.exec, s.i.Bundle, check)
	return err
}

// notesPreparation is the catalog KyNotes' secret-free bundle: client c at issuer, secrets
// beside the bundle where its import writes them, backup settings as its env fixes them.
func notesPreparation(bundle, issuer string, c stack.IdentityClient, env map[string]string) (*stack.NotesPreparation, error) {
	dir := path.Dir(bundle)
	sso := &stack.NotesSSO{IssuerURL: issuer, ClientID: c.ID, ClientSecretFile: path.Join(dir, "client.secret"), RedirectURI: c.RedirectURIs[0]}
	if c.SuiteWebhook != nil {
		sso.DirectoryHMACSecretFile = path.Join(dir, "directory.secret")
	}
	keep, err := strconv.Atoi(env["KYNOTES_BACKUP_KEEP"])
	if err != nil || keep < 1 {
		return nil, fmt.Errorf("catalog kynotes: KYNOTES_BACKUP_KEEP %q must be a positive integer", env["KYNOTES_BACKUP_KEEP"])
	}
	return &stack.NotesPreparation{Version: 1, SSO: sso, Backup: &stack.NotesBackup{Dir: env["KYNOTES_BACKUP_DIR"], Keep: keep}}, nil
}

// scimStep (kyidentity.scim.<app>) adds KyIdentity's SCIM connector to the client of an app
// that declares oidc.scim_token_file. KyIdentity takes a connector on a later pass of the
// setup that registered the client, from a bundle outside its preparation directories, once
// the receiver is ready: the app's setup has created its administrator, whom KyIdentity would
// otherwise push first and the app's setup then refuse to adopt. The app's target generated
// the token; a copy goes to a private file in KyIdentity's pod, never replacing one.
type scimStep struct {
	app       string
	token     func(context.Context) ([]byte, error) // read on the app's target
	tokenFile string                                // the copy in KyIdentity's pod
	file      string                                // the pass's bundle there
	bundle    []byte
	place     func(ctx context.Context, args []string, input []byte) error // KyIdentity's shell
	exec      identityExec                                                 // KyIdentity's apply-setup
}

func (s scimStep) ID() string { return "kyidentity.scim." + s.app }

func (s scimStep) InputHash() string {
	sum := sha256.Sum256(append([]byte(s.tokenFile+"\x00"+s.file+"\x00"), s.bundle...))
	return hex.EncodeToString(sum[:])
}

func (s scimStep) Inspect(ctx context.Context) (bool, error) { return s.Verify(ctx) == nil, nil }
func (s scimStep) Apply(ctx context.Context) error           { return s.run(ctx, false) }
func (s scimStep) Verify(ctx context.Context) error          { return s.run(ctx, true) }

func (s scimStep) run(ctx context.Context, check bool) error {
	if err := identityCapabilities(ctx, s.exec, false); err != nil {
		return err
	}
	token, err := s.token(ctx)
	defer clear(token)
	if err != nil {
		return fmt.Errorf("%s's SCIM token on its target: %w", s.app, err)
	}
	mode := "write"
	if check {
		mode = "check"
	}
	if s.place(ctx, []string{mode, s.tokenFile}, token) != nil {
		return fmt.Errorf("KyIdentity's copy of %s's SCIM token, %s, is missing, unsafe or differs from %s's; it is never replaced", s.app, s.tokenFile, s.app)
	}
	if s.place(ctx, []string{mode, s.file}, s.bundle) != nil {
		return fmt.Errorf("KyIdentity's SCIM setup pass %s is missing, unsafe or differs from this plan's", s.file)
	}
	_, err = identitySetup(ctx, s.exec, s.file, check)
	return err
}

// scimPass is the bundle of the pass that adds scim to client id of src's setup (KyIdentity's
// first, or an amendment of parent): that setup's ID, its intent for the client and, as its
// preparation generated them, the paths of the client secret and the accounts' passwords.
func scimPass(src stack.Integration, parent, id string, scim stack.IdentitySCIM) []byte {
	n, scope := src.Identity, path.Dir(src.Bundle)
	type account struct {
		stack.IdentityAccount
		PasswordFile string `json:"passwordFile"`
	}
	type client struct {
		stack.IdentityClient
		SecretFile string `json:"secretFile"`
	}
	c := client{n.Clients[slices.IndexFunc(n.Clients, func(c stack.IdentityClient) bool { return c.ID == id })], path.Join(scope, "client-"+id+".secret")}
	c.SCIM = &scim
	b := map[string]any{"schema": n.Schema, "setupId": n.SetupID, "clients": []client{c}}
	if parent != "" {
		b["extends"] = parent
	} else {
		b["admin"] = account{n.Admin, path.Join(scope, "admin.password")}
		b["everyday"] = account{n.Everyday, path.Join(scope, "everyday.password")}
	}
	raw, err := json.Marshal(b)
	if err != nil {
		panic(err) // plain typed metadata always marshals
	}
	return raw
}

// setupSteps are apply's setup for an installed app's integration: KyIdentity's (its setup,
// the setup amendments and the bootstrap administrator's retirement), or an app's that signs
// in through it, on Kubernetes or a Docker host (newSession refuses anything else). connector
// is the app's kyidentity.scim step, which apply runs after the edges route to the app.
func (s *session) setupSteps(d driver, i stack.Integration) (steps []engine.Step, connector engine.Step, err error) {
	adapter := productAdapters[i.Product]
	exec, input, err := nativeProductExec(d, i, adapter)
	if err != nil {
		return nil, nil, err
	}
	place := func(d driver, i stack.Integration, user string) (func(context.Context, []string, []byte) error, error) {
		// The pod's or container's shell, as its runtime user: sh -c placeScript sh <mode> <path>.
		_, shell, err := nativeProductExec(d, i, productAdapter{binary: "sh", runtimeUser: user})
		return func(ctx context.Context, args []string, input []byte) error {
			_, err := shell(ctx, append([]string{"-c", placeScript, "sh"}, args...), input)
			return err
		}, err
	}
	if i.Product == "kyidentity" {
		first := identitySetupStep{id: "kyidentity.setup", i: i, intent: i.Identity, exec: exec, input: input, late: s.derived}
		if m := i.Identity.Mail; m != nil {
			first.caps, first.mail = []string{"mail-relay"}, m
			if m.PasswordFile != "" {
				if first.place, err = place(d, i, ""); err != nil {
					return nil, nil, err
				}
			}
		}
		steps := []engine.Step{first}
		for _, a := range s.amendments {
			steps = append(steps, identitySetupStep{id: "kyidentity.amend." + a.app, i: a.source, intent: a.bundle, caps: []string{"setup-amendments"}, exec: exec, input: input})
		}
		if bootstrap := s.cat["kyidentity"].Kubernetes.Env["BOOTSTRAP_ADMIN_USER"]; bootstrap != "" {
			steps = append(steps, retireBootstrapStep{exec: exec, bootstrap: bootstrap, admin: i.Identity.Admin.Username, note: &s.bootstrapNote})
		}
		return steps, nil, nil
	}
	first := s.integrations[slices.IndexFunc(s.integrations, func(x stack.Integration) bool { return x.Product == "kyidentity" && x.Identity != nil })]
	source, parent := first, ""
	if a := slices.IndexFunc(s.amendments, func(a identityAmendment) bool { return a.app == i.Product }); a >= 0 {
		source, parent = s.amendments[a].source, first.Identity.SetupID
	}
	identity := s.drivers[s.placed["kyidentity"].Name]
	export, _, err := nativeProductExec(identity, source, productAdapters["kyidentity"])
	if err != nil {
		return nil, nil, err
	}
	issuer := s.cat["kyidentity"].Kubernetes.Env[issuerEnv]
	if i.Product == "kynotes" {
		c := slices.IndexFunc(source.Identity.Clients, func(c stack.IdentityClient) bool { return c.ID == i.IdentityClient })
		if c < 0 {
			return nil, nil, fmt.Errorf("KyIdentity's setup has no client %s for KyNotes", i.IdentityClient)
		}
		if i.Notes, err = notesPreparation(i.Bundle, issuer, source.Identity.Clients[c], s.cat["kynotes"].Kubernetes.Env); err != nil {
			return nil, nil, err
		}
		return []engine.Step{notesSetupStep{i: i, source: source, export: export, exec: exec, input: input}}, nil, nil
	}
	o := s.cat[i.Product].OIDC
	step := clientSetupStep{i: i, name: o.Name, source: source, issuer: issuer, scimToken: o.SCIMTokenFile,
		export: export, exec: exec, setup: adapter.setup}
	if i.Product == "kydns" {
		// Its image has no shell: place on the host, in the bind directory it reads as /data.
		host, ok := d.(sshDriver)
		if !ok {
			return nil, nil, errors.New("KyDNS runs on a Docker target only")
		}
		if step.place, err = hostPlace(host.c, path.Join(host.t.Root, "kydns"), i.Bundle); err != nil {
			return nil, nil, err
		}
		step.setup = waitForSocket(host.c, path.Join(host.t.Root, "kydns/data/installer/setup.sock"), host.t.Name, adapter.setup)
		e, r := appRoute(s.plan, "kydns")
		network := "http://" + net.JoinHostPort(r.ForwardHost, strconv.Itoa(r.ForwardPort)) + "/api/v1/installer/network"
		npm, ok := s.drivers[e.Target].(kubeDriver)
		if !ok || s.placed["npm"].Name != e.Target {
			return nil, nil, fmt.Errorf("KyDNS's route must be on the catalog NPM on a cluster, which apply checks KyDNS trusts; edge %s is not", e.Target)
		}
		curl, _, err := nativeProductExec(npm, stack.Integration{Product: "npm"}, productAdapter{binary: "curl"})
		if err != nil {
			return nil, nil, err
		}
		// kydns.edge runs after the edges, as connectors do: it asks from NPM's pod.
		return []engine.Step{step, gateStep{url: network, edge: e.Target, egress: e.EgressAddress, target: host.t.Name}},
			edgeTrustStep{url: network, edge: e.Target, egress: e.EgressAddress, curl: curl}, nil
	}
	if step.place, err = place(d, i, adapter.runtimeUser); err != nil {
		return nil, nil, err
	}
	switch i.Product {
	case "kypulse":
		step.targets = s.pulseTargets
	case "kycalendar":
		// It admits only the identities its setup names, and creates none at sign-in.
		step.everyday = &everydayUser{Bundle: first.Bundle, Username: first.Identity.Everyday.Username}
	case "kypost":
		step.native = postBundle
	}
	if o.SCIMTokenFile == "" {
		return []engine.Step{step}, nil, nil
	}
	host, err := routeHost(s.plan, i.Product)
	if err != nil {
		return nil, nil, err
	}
	identityPlace, err := place(identity, source, "")
	if err != nil {
		return nil, nil, err
	}
	installer := path.Dir(path.Dir(source.Bundle))
	scim := stack.IdentitySCIM{URL: "https://" + host + "/scim/v2", TokenFile: path.Join(installer, "scim-"+i.IdentityClient+".token")}
	connector = scimStep{app: i.Product, tokenFile: scim.TokenFile, file: path.Join(installer, "scim-"+i.IdentityClient+".json"),
		bundle: scimPass(source, parent, i.IdentityClient, scim), place: identityPlace, exec: export,
		token: func(ctx context.Context) ([]byte, error) {
			return d.appSecret(ctx, i.Product, path.Base(o.SCIMTokenFile))
		}}
	return []engine.Step{step}, connector, nil
}

// hostDataDir is where an app on a Docker target keeps the installer's files: its ./data bind
// directory, which the container reads as /data.
const hostDataDir = "/data/installer/"

// hostPlace is placeScript run on the Docker host over SSH as the target user, who owns the
// app's bind directory, for an image without a shell. It maps the container's /data/installer/
// to <dir>/data/installer/, refuses anything else, and refuses a symlinked directory on the way.
func hostPlace(r remote.Runner, dir, bundle string) (func(context.Context, []string, []byte) error, error) {
	setup := path.Dir(bundle)
	if path.Clean(bundle) != bundle || path.Dir(setup)+"/" != hostDataDir || (stack.Integration{Bundle: bundle}).SetupID() == "" {
		return nil, fmt.Errorf("bundle %s must be <uuid>/setup.json in %s, the bind directory the installer writes from the host", bundle, hostDataDir)
	}
	data := path.Join(dir, "data")
	guard := "d=" + remote.Quote(data) + "; [ ! -L \"$d\" ] || exit 1; for c in installer " + remote.Quote(path.Base(setup)) + "; do d=$d/$c; [ ! -L \"$d\" ] || exit 1; done; "
	return func(ctx context.Context, args []string, input []byte) error {
		if path.Dir(args[1]) != setup {
			return errors.New("setup file outside its setup directory")
		}
		_, err := r.Run(ctx, guard+"sh -c "+remote.Quote(placeScript)+" sh "+remote.Quote(args[0])+" "+remote.Quote(path.Join(data, strings.TrimPrefix(args[1], "/data/"))), input)
		return err
	}, nil
}

// waitForSocket runs setup once KyDNS has opened its setup socket on the host. Compose has no
// healthcheck for a shell-less image, so the first apply-setup can come before it listens. A
// check does not wait: it reports the product's own refusal.
func waitForSocket(r remote.Runner, socket, target string, setup func(context.Context, identityExec, string, bool) ([]byte, error)) func(context.Context, identityExec, string, bool) ([]byte, error) {
	return func(ctx context.Context, exec identityExec, bundle string, check bool) ([]byte, error) {
		if check {
			return setup(ctx, exec, bundle, check)
		}
		wait, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		for {
			if _, err := r.Run(wait, "test -S "+remote.Quote(socket), nil); err == nil {
				return setup(ctx, exec, bundle, check)
			}
			select {
			case <-wait.Done():
				return nil, fmt.Errorf("KyDNS's setup socket did not open within a minute; on %s run docker logs kyq-kydns-app-1 (is something else bound to its forward_host's port 53?)", target)
			case <-time.After(time.Second):
			}
		}
	}
}

// gateStep (kydns.gate) proves KyDNS's transport gate is closed to everyone but its edge:
// the workstation, which is not the edge, asks its admin port directly with the edge's
// X-Forwarded-Proto, and only 403 passes. It fails when egress_address is an address other
// peers share, such as a Docker network gateway that hides every client behind it.
type gateStep struct {
	url, edge, egress, target string
	client                    *http.Client // nil: a direct client with a 10 s timeout
}

func (g gateStep) ID() string                                { return "kydns.gate" }
func (g gateStep) InputHash() string                         { return g.url + " " + g.egress }
func (g gateStep) Inspect(ctx context.Context) (bool, error) { return g.Verify(ctx) == nil, nil }
func (g gateStep) Apply(ctx context.Context) error           { return g.Verify(ctx) }

// port is the admin port the probe asks.
func (g gateStep) port() string {
	u, _ := url.Parse(g.url)
	return u.Port()
}

func (g gateStep) Verify(ctx context.Context) error {
	c := g.client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", g.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Forwarded-Proto", "https")
	res, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach KyDNS's admin port %s from this workstation. This read-only probe proves KyDNS refuses every peer but the edge. KyDNS is installed and set up; rerunning apply from a LAN host that reaches port %s is safe and finishes", g.url, g.port())
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		return fmt.Errorf("KyDNS trusted this workstation's forwarded request (HTTP %d at %s): egress_address %s on edge %s is not the edge's alone. A Docker host that hides client addresses (Docker's userland proxy can) shows KyDNS every peer from one gateway; KyDNS is not supported there. Set egress_address to the edge's own address and rerun apply. KyDNS runs with this setting until then; to stop it, on %s run docker compose -p kyq-kydns stop", res.StatusCode, g.url, g.egress, g.edge, g.target)
	}
	return nil
}

// edgeTrustStep (kydns.edge) proves KyDNS trusts its edge: from NPM's pod, the source its
// proxied requests leave from, it asks KyDNS's admin port with the edge's X-Forwarded-Proto and
// passes only on 200 with proxyHeadersTrusted and scheme https. It reads, never changes.
type edgeTrustStep struct {
	url, edge, egress string
	curl              identityExec // curl in NPM's pod
}

func (e edgeTrustStep) ID() string                                { return "kydns.edge" }
func (e edgeTrustStep) InputHash() string                         { return e.url + " " + e.egress }
func (e edgeTrustStep) Inspect(ctx context.Context) (bool, error) { return e.Verify(ctx) == nil, nil }
func (e edgeTrustStep) Apply(ctx context.Context) error           { return e.Verify(ctx) }

func (e edgeTrustStep) Verify(ctx context.Context) error {
	out, err := e.curl(ctx, []string{"-sS", "-m", "10", "-H", "X-Forwarded-Proto: https", e.url})
	var n struct {
		ProxyHeadersTrusted bool   `json:"proxyHeadersTrusted"`
		Scheme              string `json:"scheme"`
	}
	if err == nil && len(out) <= 4<<10 && json.Unmarshal(out, &n) == nil && n.ProxyHeadersTrusted && n.Scheme == "https" {
		return nil
	}
	return fmt.Errorf("KyDNS does not trust edge %s at egress_address %s, so its admin UI answers 403 through the route: NPM moved node, the address is wrong, or this Docker host hides client addresses and KyDNS is not supported on it. Fix egress_address and rerun apply", e.edge, e.egress)
}
