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

// composeRecovery adds an installed KyRecovery's catalog client to the installed
// KyIdentity's setup intent, or checks a hand-written one against it. KyIdentity takes
// clients only in its first setup, so both must be in the plan from the start.
func (s *session) composeRecovery(st *stack.Stack) error {
	r := slices.IndexFunc(s.integrations, func(i stack.Integration) bool { return i.Product == "kyrecovery" && i.Installed(st) })
	if r < 0 {
		return nil
	}
	k := slices.IndexFunc(s.integrations, func(i stack.Integration) bool {
		return i.Product == "kyidentity" && i.Identity != nil && i.Installed(st)
	})
	if k < 0 {
		return errors.New("KyRecovery's setup needs the plan to install KyIdentity with its identity settings")
	}
	host, err := routeHost(st, "kyrecovery")
	if err != nil {
		return err
	}
	want, err := catalogClient(s.integrations[r].IdentityClient, host, s.cat["kyrecovery"].OIDC)
	if err != nil {
		return err
	}
	// Copy: the saved plan keeps only what the operator wrote.
	n := *s.integrations[k].Identity
	n.Clients = append([]stack.IdentityClient(nil), n.Clients...)
	found := false
	for _, c := range n.Clients {
		if strings.EqualFold(c.ID, want.ID) {
			if !reflect.DeepEqual(c, want) {
				return fmt.Errorf("KyIdentity client %s differs from the catalog's KyRecovery client; remove it from identity.clients and apply derives it", c.ID)
			}
			found = true
		}
	}
	if !found {
		n.Clients = append(n.Clients, want)
	}
	s.integrations = slices.Clone(s.integrations)
	s.integrations[k].Identity = &n
	s.recoveryHandover = fmt.Sprintf(recoveryCeremony, host, n.Admin.Username)
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

// recoverySetupStep is apply's apply-setup for a catalog KyRecovery. KyRecovery 0.4.2 has no
// native preparation or credential import, so the step places its bundle and KyIdentity's
// client and directory secrets as private files through its pod's shell, never replacing one.
type recoverySetupStep struct {
	i      stack.Integration
	source stack.Integration // KyIdentity's, with the client composeRecovery added
	issuer string
	export identityExec // KyIdentity's apply-setup
	exec   identityExec // KyRecovery's apply-setup
	place  func(ctx context.Context, args []string, input []byte) error
}

func (s recoverySetupStep) ID() string { return "kyrecovery.setup" }

func (s recoverySetupStep) InputHash() string {
	b, err := json.Marshal([]any{s.i, s.source, s.issuer})
	if err != nil {
		panic(err) // plain typed metadata always marshals
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s recoverySetupStep) Inspect(ctx context.Context) (bool, error) {
	return s.Verify(ctx) == nil, nil
}

func (s recoverySetupStep) Apply(ctx context.Context) error  { return s.run(ctx, false) }
func (s recoverySetupStep) Verify(ctx context.Context) error { return s.run(ctx, true) }

// placeScript publishes $2 from stdin without replacing it: an existing file must match.
// Check mode only compares. Created directories and files are private to the pod's user.
const placeScript = `set -eu
f=$2
[ ! -L "$f" ]
if [ -e "$f" ]; then [ -f "$f" ] && cmp -s - "$f"; exit; fi
[ "$1" = write ]
umask 077
mkdir -p "${f%/*}"
t=$(mktemp "${f%/*}/.kyq.XXXXXX")
cat > "$t"
ln "$t" "$f" || { rm -f "$t"; exit 1; }
rm -f "$t"`

func (s recoverySetupStep) run(ctx context.Context, check bool) error {
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
	bundle, err := json.Marshal(map[string]any{
		"schema": "ky.product-setup/1", "service": "kyrecovery", "setupId": s.i.RecoverySetupID(),
		"sso": map[string]string{
			"issuerUrl": frame.IssuerURL, "clientId": frame.ClientID, "redirectUri": frame.RedirectURIs[0],
			"clientSecretFile": path.Join(dir, "client.secret"), "directorySecretFile": path.Join(dir, "directory.secret"),
		},
		"admins": []map[string]string{{"issuer": frame.IssuerURL, "subject": frame.Administrator.Subject, "username": frame.Administrator.Username}},
	})
	if err != nil {
		return err
	}
	mode := "write"
	if check {
		mode = "check"
	}
	// The bundle goes last: it names files that must already be in place.
	for _, f := range []struct {
		name string
		data []byte
	}{{"client.secret", []byte(frame.ClientSecret)}, {"directory.secret", []byte(frame.DirectorySecret)}, {"setup.json", bundle}} {
		if err := s.place(ctx, []string{mode, path.Join(dir, f.name)}, f.data); err != nil {
			return fmt.Errorf("KyRecovery's %s is missing, unsafe or differs from KyIdentity's current client; retained files are never replaced", f.name)
		}
	}
	if _, err := recoverySetup(ctx, s.exec, s.i.Bundle, check); err != nil {
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

// setupStep is apply's setup for an installed app's integration: KyIdentity or KyRecovery,
// both on Kubernetes (newSession refuses anything else).
func (s *session) setupStep(d driver, i stack.Integration) (engine.Step, error) {
	exec, input, err := nativeProductExec(d, i, productAdapters[i.Product])
	if err != nil {
		return nil, err
	}
	if i.Product == "kyidentity" {
		var late []string
		for _, r := range s.integrations {
			if r.Product == "kyrecovery" && r.IdentityClient != "" {
				late = append(late, r.IdentityClient)
			}
		}
		return identitySetupStep{i: i, exec: exec, input: input, late: late}, nil
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
	issuer := s.cat["kyidentity"].Kubernetes.Env["KYIDENTITY_ISSUER_URL"]
	return recoverySetupStep{i: i, source: source, issuer: issuer, export: export, exec: exec, place: place}, nil
}
