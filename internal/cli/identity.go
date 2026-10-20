package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
)

const identityBinary = "/usr/local/bin/kyidentity"

var workloadName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,252}$`)
var identityRoleReportName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}/[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)
var identityGroupReportName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,63}$`)
var kubeWorkloadName = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

type identityExec func(context.Context, []string) ([]byte, error)

func dockerIdentityExec(r remote.Runner, container string) identityExec {
	return dockerProductExec(r, container, identityBinary)
}

func dockerProductExec(r remote.Runner, container, binary string, runtimeUser ...string) identityExec {
	exec := dockerProductInputExec(r, container, binary, runtimeUser...)
	return func(ctx context.Context, args []string) ([]byte, error) {
		return exec(ctx, args, nil)
	}
}

func dockerProductInputExec(r remote.Runner, container, binary string, runtimeUser ...string) func(context.Context, []string, []byte) ([]byte, error) {
	return dockerExec(r, "", remote.Quote(container), binary, runtimeUser...)
}

// dockerAppContainer is a shell prefix that sets $c to the ID of installed app's one running
// installer-managed container, or fails.
func dockerAppContainer(app string) string {
	return "c=$(docker ps -q --filter " + remote.Quote("label=com.docker.compose.project=kyq-"+app) +
		" --filter " + remote.Quote("label=com.docker.compose.service=app") + " --filter " + remote.Quote("label="+managed.Label+"="+managed.Value) + " --filter status=running) && " +
		"case $c in ''|*[!0-9a-f]*) echo " + remote.Quote("want one running "+app+" container") + " >&2; exit 1;; esac && "
}

// dockerExec runs binary in container, a shell word that prefix may set.
func dockerExec(r remote.Runner, prefix, container, binary string, runtimeUser ...string) func(context.Context, []string, []byte) ([]byte, error) {
	return func(ctx context.Context, args []string, input []byte) ([]byte, error) {
		if len(input) > 128<<10 {
			return nil, errors.New("product command input exceeds limit")
		}
		words := []string{"docker", "exec"}
		if len(input) > 0 {
			words = append(words, "-i")
		}
		if len(runtimeUser) > 0 && runtimeUser[0] != "" {
			words = append(words, "--user", remote.Quote(runtimeUser[0]))
		}
		words = append(words, container, remote.Quote(binary))
		for _, arg := range args {
			words = append(words, remote.Quote(arg))
		}
		b, err := r.Run(ctx, prefix+strings.Join(words, " "), input)
		if err != nil {
			return nil, errors.New("product setup command failed; inspect product logs locally")
		}
		return b, nil
	}
}

// identitySetup checks the product's advertised contract before invoking the mutation.
func identitySetup(ctx context.Context, exec identityExec, bundle string, check bool) ([]byte, error) {
	if err := identityCapabilities(ctx, exec, false); err != nil {
		return nil, err
	}
	args := []string{"apply-setup", "--file", bundle}
	if check {
		args = append(args, "--check")
	}
	b, err := exec(ctx, args)
	if err != nil {
		return nil, err
	}
	// Render only explicitly permitted fields, never arbitrary container stdout.
	var report struct {
		Schema  string `json:"schema"`
		Created []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"created"`
		Reused []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"reused"`
	}
	if len(b) > 128*1024 || json.Unmarshal(b, &report) != nil || report.Schema != "kyidentity.setup/1" || report.Created == nil || report.Reused == nil ||
		len(report.Created)+len(report.Reused) > 162 || (check && len(report.Created) > 0) {
		return nil, errors.New("invalid KyIdentity setup report")
	}
	for _, items := range [][]struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
		ID   string `json:"id"`
	}{report.Created, report.Reused} {
		for _, item := range items {
			validName := workloadName.MatchString(item.Name)
			if item.Kind == "role" {
				validName = identityRoleReportName.MatchString(item.Name)
			}
			if item.Kind == "group" {
				validName = identityGroupReportName.MatchString(item.Name) && strings.TrimSpace(item.Name) == item.Name
			}
			// 0.4.5's mail relay is one object: {mail, settings, mail}.
			mail := item.Kind == "mail" && item.Name == "settings" && item.ID == "mail"
			if (item.Kind != "user" && item.Kind != "client" && item.Kind != "role" && item.Kind != "connector" && item.Kind != "group" && !mail) || !validName || !workloadName.MatchString(item.ID) {
				return nil, errors.New("invalid KyIdentity setup object report")
			}
		}
	}
	return json.Marshal(report)
}

// missingCapability is a valid capabilities report that lacks one feature, as opposed to a
// failed or malformed query.
type missingCapability string

func (m missingCapability) Error() string {
	return "KyIdentity lacks setup capability " + string(m) + "; upgrade the product first"
}

func identityCapabilities(ctx context.Context, exec identityExec, prepare bool, extra ...string) error {
	b, err := exec(ctx, []string{"apply-setup", "--capabilities"})
	if err != nil {
		return err
	}
	var caps struct {
		Schema, Service, BundleSchema string
		Features                      []string
	}
	if len(b) > 128*1024 || json.Unmarshal(b, &caps) != nil || caps.Schema != "ky.setup-capabilities/1" || caps.Service != "kyidentity" || caps.BundleSchema != "kyidentity.setup/1" {
		return errors.New("selected product does not advertise the supported KyIdentity setup contract")
	}
	features := []string{"local-accounts", "confidential-oidc-clients", "assigned-only", "everyday-assignment", "app-role-assignments", "scim-connectors", "suite-webhook-connectors", "directory-groups", "app-group-assignments", "scim-groups", "check"}
	features = append(features, extra...)
	if prepare {
		features = append(features, "protected-setup-files", "bundle-preparation", "target-credentials")
	}
	for _, required := range features {
		found := false
		for _, f := range caps.Features {
			found = found || f == required
		}
		if !found {
			return missingCapability(required)
		}
	}
	return nil
}

func integrateIdentity(ctx context.Context, args []string, o Options) error {
	return integrateProduct(ctx, args, o, "kyidentity")
}

func integrateProduct(ctx context.Context, args []string, o Options, fixedService string) error {
	return integrateProductResult(ctx, args, o, fixedService, func(report []byte) error {
		_, err := fmt.Fprintf(o.Out, "%s\n", report)
		return err
	})
}

func integrateProductResult(ctx context.Context, args []string, o Options, fixedService string, emit func([]byte) error) (retErr error) {
	f := flag.NewFlagSet("integrate-product", flag.ContinueOnError)
	f.SetOutput(o.Out)
	service := f.String("product", fixedService, "product adapter name")
	state := f.String("state", ".", "state directory holding stack.yaml")
	target := f.String("target", "", "target name in stack.yaml")
	container := f.String("container", "", "existing KyIdentity container name")
	namespace := f.String("namespace", "", "Kubernetes namespace")
	pod := f.String("pod", "", "existing Kubernetes pod name")
	bundle := f.String("bundle", "", "absolute protected bundle path inside the product container")
	check := f.Bool("check", false, "verify the saved setup without mutation")
	prepare := f.Bool("prepare", false, "prepare the bundle from matching saved secret-free settings")
	trust := trustFlag{}
	f.Var(trust, "trust-host-key", "accept an unknown host key")
	if err := f.Parse(args); err != nil {
		return err
	}
	adapter, ok := productAdapters[*service]
	if !ok || fixedService != "" && *service != fixedService {
		return errors.New("select a supported --product adapter")
	}
	if f.NArg() != 0 || *target == "" || !workloadName.MatchString(*container) || !path.IsAbs(*bundle) || path.Clean(*bundle) != *bundle || len(*bundle) > 4096 || strings.ContainsFunc(*bundle, unicode.IsControl) {
		return errors.New("integrate-identity requires --target, --container and an absolute container-local --bundle path")
	}
	s, err := load(*state, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
	var preparation *stack.NotesPreparation
	var identityPreparation *stack.IdentityPreparation
	var selected, credentialSource stack.Integration
	if *prepare {
		for _, i := range s.integrations {
			if i.Product == *service && i.Target == *target && i.Container == *container && i.Namespace == *namespace && i.Pod == *pod && i.Bundle == *bundle {
				preparation = i.Notes
				identityPreparation = i.Identity
				selected = i
			}
		}
		if (*service != "kynotes" || preparation == nil) && (*service != "kyidentity" || identityPreparation == nil) {
			return errors.New("--prepare requires matching saved product preparation settings")
		}
		if selected.IdentityClient != "" {
			credentialSource, _, err = (&stack.Stack{Integrations: s.integrations}).IdentitySource(selected)
			if err != nil {
				return err
			}
		}
	}
	// Existing references grant no authority; this explicit command selects the mutation target.
	var t stack.Target
	for _, candidate := range s.allTargets {
		if candidate.Name == *target {
			t = candidate
		}
	}
	if t.Name == "" {
		return errors.New("integration target is not in stack.yaml")
	}
	if err := validateProductTarget(*service, t); err != nil {
		return err
	}
	if t.Kubernetes() {
		if !kubeWorkloadName.MatchString(*namespace) || len(*namespace) > 63 || !kubeWorkloadName.MatchString(*pod) || len(*pod) > 253 || !kubeWorkloadName.MatchString(*container) || len(*container) > 63 {
			return errors.New("Kubernetes integration requires valid --namespace, --pod and --container names")
		}
	} else if *namespace != "" || *pod != "" {
		return errors.New("--namespace and --pod require a Kubernetes target")
	}
	s.targets = []stack.Target{t}
	// Product setup has no edge reconciliation authority or need for NPM credentials.
	s.edges = nil
	if credentialSource.Product != "" && credentialSource.Target != t.Name {
		for _, candidate := range s.allTargets {
			if candidate.Name == credentialSource.Target {
				s.targets = append(s.targets, candidate)
			}
		}
	}
	if err := s.connect(ctx, trust); err != nil {
		return err
	}
	exec, inputExec, err := nativeProductExec(s.drivers[t.Name], stack.Integration{Container: *container, Namespace: *namespace, Pod: *pod}, adapter)
	if err != nil {
		return err
	}
	if !*check {
		done, err := s.lock(ctx, "kyquickstart product setup", []stack.Target{t})
		if err != nil {
			return err
		}
		defer func() { retErr = errors.Join(retErr, finish(ctx, done)) }()
	}
	if preparation != nil {
		if credentialSource.Product != "" {
			sourceExec, _, err := nativeProductExec(s.drivers[credentialSource.Target], credentialSource, productAdapters["kyidentity"])
			if err != nil {
				return err
			}
			if err := transferNotesCredentials(ctx, sourceExec, exec, inputExec, credentialSource, selected, *check); err != nil {
				return err
			}
		} else {
			if err := stageNotes(ctx, exec, inputExec, preparation, *bundle, *check); err != nil {
				return err
			}
		}
	}
	if identityPreparation != nil {
		if err := stageIdentity(ctx, exec, inputExec, identityPreparation, *bundle, *check); err != nil {
			return err
		}
	}
	report, err := adapter.setup(ctx, exec, *bundle, *check)
	if err != nil {
		return err
	}
	if !*check {
		if _, err := adapter.setup(ctx, exec, *bundle, true); err != nil {
			return fmt.Errorf("setup verification failed: %w", err)
		}
	}
	return emit(report)
}

func nativeProductExec(target driver, i stack.Integration, adapter productAdapter) (identityExec, func(context.Context, []string, []byte) ([]byte, error), error) {
	switch d := target.(type) {
	case sshDriver:
		input := dockerProductInputExec(d.c, i.Container, adapter.binary, adapter.runtimeUser)
		if i.Container == "" { // an installed app: its one running container, found per call
			if i.Product != "kypost" {
				adapter.runtimeUser = ""
			}
			input = dockerExec(d.c, dockerAppContainer(i.Product), `"$c"`, adapter.binary, adapter.runtimeUser)
		}
		return func(ctx context.Context, args []string) ([]byte, error) { return input(ctx, args, nil) }, input, nil
	case kubeDriver:
		// An installed app's integration names no pod: each call finds the app's ready one.
		where := func(ctx context.Context) (string, string, string, error) {
			if i.Pod == "" {
				return d.c.AppPod(ctx, i.Product)
			}
			return i.Namespace, i.Pod, i.Container, nil
		}
		input := func(ctx context.Context, args []string, input []byte) ([]byte, error) {
			ns, pod, container, err := where(ctx)
			if err != nil {
				return nil, err
			}
			args = append([]string{adapter.binary}, args...)
			if input == nil {
				return d.c.Exec(ctx, ns, pod, container, args)
			}
			return d.c.ExecInput(ctx, ns, pod, container, args, input)
		}
		return func(ctx context.Context, args []string) ([]byte, error) { return input(ctx, args, nil) }, input, nil
	}
	return nil, nil, errors.New("selected product target is not connected")
}

// identitySetupStep is apply's staged apply-setup for a catalog KyIdentity, from the app's
// saved integration: native preparation, then setup, each confirmed by its read-only check.
// The same step stages and applies a setup amendment (kyidentity.amend.<app>).
type identitySetupStep struct {
	id     string
	i      stack.Integration // Bundle is the setup's own
	intent any               // staged on stdin: i.Identity, or an amendment bundle
	caps   []string          // capabilities beyond preparation's
	exec   identityExec
	input  func(context.Context, []string, []byte) ([]byte, error)
	late   []string                                                     // clients other installed apps added to the intent
	mail   *stack.IdentityMail                                          // the first setup's relay; nil for none
	place  func(ctx context.Context, args []string, input []byte) error // KyIdentity's shell, for its relay password
}

func (s identitySetupStep) ID() string { return s.id }

func (s identitySetupStep) InputHash() string {
	b, err := json.Marshal(s.i)
	if err != nil {
		panic(err) // plain typed metadata always marshals
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Inspect is the read-only check; until it passes, Apply runs and reports the real refusal.
func (s identitySetupStep) Inspect(ctx context.Context) (bool, error) {
	return s.Verify(ctx) == nil, nil
}

func (s identitySetupStep) Apply(ctx context.Context) error {
	if err := s.relayPassword(ctx, "write"); err != nil {
		return err
	}
	if err := stageIdentity(ctx, s.exec, s.input, s.intent, s.i.Bundle, false, s.caps...); err != nil {
		if s.mail != nil {
			return fmt.Errorf("%w. KyIdentity's mail relay is set once, by its first setup: to change identity.mail after it ran (or add it later), change the relay in KyIdentity's admin UI (Settings > Mail) and put the plan's identity.mail back as it was", s.lateClients(ctx, err))
		}
		return s.lateClients(ctx, err)
	}
	_, err := identitySetup(ctx, s.exec, s.i.Bundle, false)
	return err
}

// relayPassword copies the workstation's SMTP password file into KyIdentity's container,
// in memory and on stdin only, never replacing a different copy; check only compares.
func (s identitySetupStep) relayPassword(ctx context.Context, mode string) error {
	if s.mail == nil || s.mail.PasswordFile == "" {
		return nil
	}
	b, err := readMailPassword(s.mail.PasswordFile)
	if err != nil {
		return err
	}
	defer clear(b)
	if s.place(ctx, []string{mode, s.mail.TargetFile}, b) != nil {
		return fmt.Errorf("KyIdentity's copy of the SMTP password, %s, is missing, unsafe or differs from %s; it is never replaced. To change the relay password, use KyIdentity's admin UI (Settings > Mail)", s.mail.TargetFile, s.mail.PasswordFile)
	}
	return nil
}

func (s identitySetupStep) Verify(ctx context.Context) error {
	if err := s.relayPassword(ctx, "check"); err != nil {
		return err
	}
	if err := stageIdentity(ctx, s.exec, s.input, s.intent, s.i.Bundle, true, s.caps...); err != nil {
		return err
	}
	_, err := identitySetup(ctx, s.exec, s.i.Bundle, true)
	return err
}

// lateClients names the real cause when the retained setup matches the intent without the
// clients other apps added: apply adds them with a setup amendment when KyIdentity can.
func (s identitySetupStep) lateClients(ctx context.Context, err error) error {
	late := staleClients(ctx, s.exec, s.input, s.i, s.late)
	switch {
	case late == nil:
		return err
	case identityCapabilities(ctx, s.exec, true, "setup-amendments") == nil:
		return fmt.Errorf("KyIdentity setup %s was prepared without client %s; rerun kyquickstart apply, which adds it with a setup amendment", s.i.Identity.SetupID, strings.Join(late, ", "))
	}
	return fmt.Errorf("KyIdentity setup %s was prepared without client %s, and this KyIdentity cannot add a client to an existing setup (Busnes-app/KyIdentity-server#110); upgrade it to 0.4.3 or later, whose setup amendments add it, or install KyIdentity afresh with it", s.i.Identity.SetupID, strings.Join(late, ", "))
}

// staleClients returns the clients of late that i's retained setup intent was prepared
// without, all of them or one, or nil.
func staleClients(ctx context.Context, exec identityExec, input func(context.Context, []string, []byte) ([]byte, error), i stack.Integration, late []string) []string {
	missing := [][]string{late}
	if len(late) > 1 {
		for _, id := range late {
			missing = append(missing, []string{id})
		}
	}
	for _, late := range missing {
		n := *i.Identity
		n.Clients = slices.DeleteFunc(slices.Clone(n.Clients), func(c stack.IdentityClient) bool { return slices.Contains(late, c.ID) })
		if len(late) > 0 && stageIdentity(ctx, exec, input, &n, i.Bundle, true) == nil {
			return late
		}
	}
	return nil
}

// retireBootstrapStep disables KyIdentity's first-start administrator once setup has created
// the owner's, through disable-user; a KyIdentity without it leaves note for the handover.
type retireBootstrapStep struct {
	exec             identityExec
	bootstrap, admin string
	note             *string
}

// bootstrapNote is printed while KyIdentity cannot retire its bootstrap administrator.
const bootstrapNote = `
Not yet: KyIdentity's first-start administrator %s stays active. This KyIdentity has no
  disable-user (0.4.3 or later has); disable it in KyIdentity's admin UI after %s's first sign-in.
`

func (s retireBootstrapStep) ID() string                            { return "kyidentity.retire-bootstrap" }
func (s retireBootstrapStep) InputHash() string                     { return s.bootstrap + " " + s.admin }
func (s retireBootstrapStep) Inspect(context.Context) (bool, error) { return false, nil }
func (s retireBootstrapStep) Note() string                          { return strings.TrimSpace(*s.note) }
func (s retireBootstrapStep) Apply(ctx context.Context) error {
	return s.run(ctx, "disabled", "unchanged")
}

// Verify reruns disable-user, which has no read-only form: on a disabled account it writes
// nothing and answers unchanged.
func (s retireBootstrapStep) Verify(ctx context.Context) error { return s.run(ctx, "unchanged") }

func (s retireBootstrapStep) run(ctx context.Context, want ...string) error {
	// Only a valid report without disable-user skips: a failed query must not leave the
	// bootstrap administrator active behind a passing step.
	var missing missingCapability
	if err := identityCapabilities(ctx, s.exec, false, "disable-user"); errors.As(err, &missing) && missing == "disable-user" {
		*s.note = fmt.Sprintf(bootstrapNote, s.bootstrap, s.admin)
		return nil
	} else if err != nil {
		return fmt.Errorf("kyidentity.retire-bootstrap cannot read KyIdentity's capabilities: %w", err)
	}
	*s.note = ""
	b, err := s.exec(ctx, []string{"disable-user", "--username", s.bootstrap, "--require-active-admin", s.admin})
	if err != nil {
		return fmt.Errorf("KyIdentity did not disable %s; it refuses unless %s is another active administrator with no end date", s.bootstrap, s.admin)
	}
	var r struct{ Schema, Username, Status string }
	if len(b) > 1024 || json.Unmarshal(b, &r) != nil || r.Schema != "kyidentity.disable-user/1" || r.Username != s.bootstrap || !slices.Contains(want, r.Status) {
		return fmt.Errorf("KyIdentity's disable-user report for %s is invalid or not %s", s.bootstrap, strings.Join(want, " or "))
	}
	return nil
}

// issuerEnv carries the catalog KyIdentity's issuer, taken from its route.
const issuerEnv = "KYIDENTITY_ISSUER_URL"

// refuseMovedSetup refuses, before any step runs, a plan that moves a URL an installed
// KyIdentity's completed setup is bound to: its issuer, which every client and token names,
// or the redirect of a client composeClients added to it. The recorded issuer is the one
// KyIdentity's Deployment runs with; when it differs, the refusal stands unless the running
// pod's read-only check shows the setup has not run.
func (s *session) refuseMovedSetup(ctx context.Context) error {
	t := s.placed["kyidentity"]
	k := slices.IndexFunc(s.integrations, func(i stack.Integration) bool {
		return i.Product == "kyidentity" && i.Identity != nil && i.Target == t.Name
	})
	d := s.drivers[t.Name]
	if k < 0 || d == nil {
		return nil
	}
	i := s.integrations[k]
	have, found, err := installedIdentityIssuer(ctx, d)
	if err != nil || !found {
		return err // not deployed: no setup to move
	}
	exec, _, err := nativeProductExec(d, i, productAdapters["kyidentity"])
	if err != nil {
		return err
	}
	if want := s.cat["kyidentity"].Kubernetes.Env[issuerEnv]; have != want {
		moved := fmt.Sprintf("this plan changes KyIdentity's issuer from %s to %s (its route hostname or its edge's client_port changed)", have, want)
		if err := identityCapabilities(ctx, exec, false); err != nil {
			return suite.Errorf(suite.IssuerChanged, "%s, and whether its setup has run cannot be read: %w; nothing was changed", moved, err)
		}
		if _, err := identitySetup(ctx, exec, i.Bundle, true); err == nil {
			return suite.Errorf(suite.IssuerChanged, "%s, but an issuer cannot change after setup: every client and token names it. Restore the route and client_port; nothing was changed", moved)
		}
	}
	for _, id := range s.derived {
		if err := movedClient(ctx, exec, i, id); err != nil {
			return err
		}
	}
	for _, a := range s.amendments {
		for _, c := range a.source.Identity.Clients {
			if err := movedClient(ctx, exec, a.source, c.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// movedClient refuses a plan that moves the URLs of client id, which composeClients took
// from the catalog, once i's setup (KyIdentity's first, or an amendment) has registered it.
func movedClient(ctx context.Context, exec identityExec, i stack.Integration, id string) error {
	want := i.Identity.Clients[slices.IndexFunc(i.Identity.Clients, func(c stack.IdentityClient) bool { return c.ID == id })]
	raw, err := exec(ctx, []string{"apply-setup", "--file", i.Bundle, "--export-client", want.ID})
	defer clear(raw)
	if err != nil {
		return nil // not registered: kyidentity.setup registers it, or says why not
	}
	var frame struct { // the URLs only: the secrets stay in raw
		RedirectURIs []string `json:"redirectUris"`
		DirectoryURL string   `json:"directoryUrl"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return errBadExport
	}
	wantDirectory := ""
	if want.SuiteWebhook != nil {
		wantDirectory = want.SuiteWebhook.URL
	}
	if !slices.Equal(frame.RedirectURIs, want.RedirectURIs) || frame.DirectoryURL != wantDirectory {
		return suite.Errorf(suite.IssuerChanged, "this plan moves %s's KyIdentity client from %s to %s (its route hostname or its edge's client_port changed), but KyIdentity's setup cannot change a registered client: that needs KyIdentity's own administration (Busnes-app/KyIdentity-server#110). Restore the route and client_port; nothing was changed", want.Name, strings.Join(frame.RedirectURIs, " "), strings.Join(want.RedirectURIs, " "))
	}
	return nil
}
