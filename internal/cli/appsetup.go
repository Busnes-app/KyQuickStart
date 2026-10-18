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
	"reflect"
	"slices"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/stack"
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

// routeHost is the URL host of app's one edge route.
func routeHost(st *stack.Stack, app string) (string, error) {
	var hosts []string
	for _, e := range st.Edges {
		for _, r := range e.Routes {
			if r.App == app {
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
// setup intent, or checks a hand-written one against it. KyIdentity takes clients only in its
// first setup, so every one must be in the plan from the start.
func (s *session) composeClients(st *stack.Stack) error {
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
		want, err := catalogClient(i.IdentityClient, host, s.cat[i.Product].OIDC)
		if err != nil {
			return err
		}
		found := false
		for _, c := range n.Clients {
			if strings.EqualFold(c.ID, want.ID) {
				if !reflect.DeepEqual(c, want) {
					return fmt.Errorf("KyIdentity client %s differs from the catalog's %s client; remove it from identity.clients and apply derives it", c.ID, want.Name)
				}
				found = true
			}
		}
		if !found {
			n.Clients = append(n.Clients, want)
		}
		s.derived = append(s.derived, want.ID)
		switch i.Product {
		case "kyrecovery":
			s.handover += fmt.Sprintf(recoveryCeremony, host, n.Admin.Username)
		case "kypulse":
			var unwatched []string
			s.pulseTargets, unwatched = pulseTargets(st, s.cat)
			if len(s.pulseTargets) > 32 {
				return errors.New("kyPulse's setup watches at most 32 apps")
			}
			s.handover += pulseHandover(host, n.Admin.Username, s.pulseTargets, unwatched)
		}
	}
	if len(s.derived) > 0 {
		s.integrations = slices.Clone(s.integrations)
		s.integrations[k].Identity = &n
	}
	return nil
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
// KyIdentity: KyRecovery or kyPulse. Neither 0.4.2 has native preparation or credential import,
// so the step places its bundle and KyIdentity's client and directory secrets as private files
// through its pod's shell, never replacing a secret.
type clientSetupStep struct {
	i       stack.Integration
	name    string            // the app's name in KyIdentity, for messages
	source  stack.Integration // KyIdentity's, with the client composeClients added
	issuer  string
	targets []pulseTarget // kyPulse's monitoring extension; nil for any other app
	export  identityExec  // KyIdentity's apply-setup
	exec    identityExec  // the app's apply-setup
	setup   func(context.Context, identityExec, string, bool) ([]byte, error)
	place   func(ctx context.Context, args []string, input []byte) error
}

func (s clientSetupStep) ID() string { return s.i.Product + ".setup" }

func (s clientSetupStep) InputHash() string {
	in := []any{s.i, s.source, s.issuer}
	if s.targets != nil {
		in = append(in, s.targets)
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
	dir := path.Dir(s.i.Bundle)
	b := map[string]any{
		"schema": "ky.product-setup/1", "service": s.i.Product, "setupId": s.i.SetupID(),
		"sso": map[string]string{
			"issuerUrl": frame.IssuerURL, "clientId": frame.ClientID, "redirectUri": frame.RedirectURIs[0],
			"clientSecretFile": path.Join(dir, "client.secret"), "directorySecretFile": path.Join(dir, "directory.secret"),
		},
		"admins": []map[string]string{{"issuer": frame.IssuerURL, "subject": frame.Administrator.Subject, "username": frame.Administrator.Username}},
	}
	if len(s.targets) > 0 {
		b["monitoring"] = map[string]any{"targets": s.targets}
	}
	bundle, err := json.Marshal(b)
	if err != nil {
		return err
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
	// The bundle goes last: it names files that must already be in place.
	for _, f := range []struct {
		name, mode string
		data       []byte
	}{{"client.secret", mode, []byte(frame.ClientSecret)}, {"directory.secret", mode, []byte(frame.DirectorySecret)}, {"setup.json", bundleMode, bundle}} {
		if err := s.place(ctx, []string{f.mode, path.Join(dir, f.name)}, f.data); err != nil {
			return fmt.Errorf("%s's %s is missing, unsafe or differs from KyIdentity's current client; retained secrets are never replaced", s.name, f.name)
		}
	}
	if _, err := s.setup(ctx, s.exec, s.i.Bundle, check); err != nil {
		return err
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

// setupStep is apply's setup for an installed app's integration: KyIdentity, or an app that
// signs in through it, on Kubernetes (newSession refuses anything else).
func (s *session) setupStep(d driver, i stack.Integration) (engine.Step, error) {
	adapter := productAdapters[i.Product]
	exec, input, err := nativeProductExec(d, i, adapter)
	if err != nil {
		return nil, err
	}
	if i.Product == "kyidentity" {
		return identitySetupStep{i: i, exec: exec, input: input, late: s.derived}, nil
	}
	source := s.integrations[slices.IndexFunc(s.integrations, func(x stack.Integration) bool { return x.Product == "kyidentity" && x.Identity != nil })]
	export, _, err := nativeProductExec(s.drivers[s.placed["kyidentity"].Name], source, productAdapters["kyidentity"])
	if err != nil {
		return nil, err
	}
	k := d.(kubeDriver)
	place := func(ctx context.Context, args []string, input []byte) error {
		ns, pod, container, err := k.c.AppPod(ctx, i.Product)
		if err != nil {
			return err
		}
		_, err = k.c.ExecInput(ctx, ns, pod, container, append([]string{"sh", "-c", placeScript, "sh"}, args...), input)
		return err
	}
	step := clientSetupStep{i: i, name: s.cat[i.Product].OIDC.Name, source: source, issuer: s.cat["kyidentity"].Kubernetes.Env[issuerEnv],
		export: export, exec: exec, setup: adapter.setup, place: place}
	if i.Product == "kypulse" {
		step.targets = s.pulseTargets
	}
	return step, nil
}
