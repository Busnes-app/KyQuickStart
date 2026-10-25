package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/plan"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

type prompts struct {
	ctx     context.Context
	in      *bufio.Reader
	out     io.Writer
	answers map[string]string
	draft   string
}

func atomicFile(file string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(file), ".kyq-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), file)
}

func (p *prompts) ask(key, label, fallback string, valid func(string) bool) (string, error) {
	if a, ok := p.answers[key]; ok {
		fallback = a
	}
	for {
		fmt.Fprintf(p.out, "%s [%s]: ", label, fallback)
		type answer struct {
			line string
			err  error
		}
		ready := make(chan answer, 1)
		go func() { line, err := p.in.ReadString('\n'); ready <- answer{line, err} }()
		var line string
		var err error
		select {
		case <-p.ctx.Done():
			return "", p.ctx.Err()
		case a := <-ready:
			line, err = a.line, a.err
		}
		if err != nil {
			return "", fmt.Errorf("setup interrupted; answers saved, rerun plan to resume: %w", err)
		}
		value := strings.TrimSpace(line)
		if value == "" {
			value = fallback
		}
		clean := len(value) <= 4096 && !strings.ContainsFunc(value, unicode.IsControl)
		if !clean || (valid != nil && !valid(value)) {
			fmt.Fprintln(p.out, "Please enter a valid value for this field.")
			continue
		}
		p.answers[key] = value
		data, err := json.MarshalIndent(p.answers, "", "  ")
		if err != nil {
			return "", err
		}
		if err := atomicFile(p.draft, data); err != nil {
			return "", err
		}
		return value, nil
	}
}

func oneOf(values ...string) func(string) bool {
	return func(s string) bool {
		for _, v := range values {
			if s == v {
				return true
			}
		}
		return false
	}
}
func integer(min, max int) func(string) bool {
	return func(s string) bool { n, err := strconv.Atoi(s); return err == nil && n >= min && n <= max }
}
func httpsOrigin(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
func host(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	s = strings.ToLower(s)
	return stack.Hostname(s) || stack.ValidName(s)
}

// Computer names become safe internal keys; network addresses remain separate.
func computerName(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSuffix(s, "."))
	if !stack.ValidName(s) && !stack.Hostname(s) {
		return "", false
	}
	name := strings.ReplaceAll(s, ".", "-")
	if name[0] < 'a' || name[0] > 'z' {
		name = "host-" + name
	}
	if len(name) > 31 {
		sum := sha256.Sum256([]byte(s))
		name = strings.TrimRight(name[:22], "-") + fmt.Sprintf("-%x", sum[:4])
	}
	return name, true
}

func targetChoice(names []string) func(string) bool {
	return func(s string) bool { name, ok := computerName(s); return ok && oneOf(names...)(name) }
}

func guided(ctx context.Context, args []string, o Options) error {
	fl := flag.NewFlagSet("plan", flag.ContinueOnError)
	fl.SetOutput(o.Out)
	state := fl.String("state", "./installation", "installation directory; contains no credentials")
	demo := fl.Bool("demo", false, "use a disposable whoami test app, not suite products")
	unattended := fl.Bool("noninteractive", false, "validate and display an existing stack.yaml without prompts")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if fl.NArg() != 0 {
		return ErrUsage
	}
	if *unattended {
		return review([]string{"--state", *state}, o)
	}
	if o.In == nil {
		return errors.New("plan needs terminal input; use review/preflight/apply with a saved stack.yaml for unattended operation")
	}
	if err := runnerOwned(*state); err != nil {
		return err
	}
	if err := os.MkdirAll(*state, 0o700); err != nil {
		return err
	}
	// stack.yaml is a real shared write target: only one plan editor may publish it.
	lock := filepath.Join(*state, "plan.lock")
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("another plan editor may be active; if it exited unexpectedly, remove plan.lock and retry")
	}
	f.Close()
	defer os.Remove(lock)
	p := &prompts{ctx: ctx, in: bufio.NewReader(io.LimitReader(o.In, 1<<20)), out: o.Out, answers: map[string]string{}, draft: filepath.Join(*state, "plan-answers.json")}
	b, err := os.ReadFile(p.draft)
	if err == nil {
		if len(b) > 1<<20 || json.Unmarshal(b, &p.answers) != nil || p.answers == nil {
			return errors.New("saved plan answers are invalid")
		}
		for _, value := range p.answers {
			if len(value) > 4096 || strings.ContainsFunc(value, unicode.IsControl) {
				return errors.New("saved plan answers contain invalid values")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	previous, err := stack.Load(filepath.Join(*state, "stack.yaml"))
	if err == nil && len(p.answers) == 0 {
		seedAnswers(p.answers, previous)
	} else if err == nil {
		// stack.yaml owns preparation metadata the wizard never asks for, so hand edits beat the draft.
		seed := map[string]string{}
		seedAnswers(seed, previous)
		for k := range p.answers {
			if preparationKey(k) {
				delete(p.answers, k)
			}
		}
		for k, v := range seed {
			if preparationKey(k) {
				p.answers[k] = v
			}
		}
	}
	if previous != nil {
		if len(previous.Integrations) > 0 {
			if _, err := integrationOrder(previous); err != nil {
				return err
			}
		}
		for _, e := range previous.Edges {
			seen := map[string]bool{}
			for _, r := range e.Routes {
				if seen[r.App+":"+r.Role] {
					return errors.New("this saved plan has multiple routes per app; edit stack.yaml directly to preserve them")
				}
				seen[r.App+":"+r.Role] = true
			}
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("existing stack must be valid before editing: %w", err)
	}
	if previous != nil && *demo != previous.Demo && *demo {
		return errors.New("use a separate state directory for a demo installation")
	}
	if *demo {
		p.answers["demo"] = "yes"
	}
	fmt.Fprintln(o.Out, "KyQuickStart guided setup\nPlanning changes no servers. Enter choices, never passwords or tokens.\nAnswers are saved after each question; Enter keeps the displayed value.\nExisting installations are references only: recording them does not grant integration or change their data.")
	cat, err := catalog.Load(o.Catalog)
	if err != nil {
		return err
	}
	demoDefault := "no"
	if *demo || previous != nil && previous.Demo {
		demoDefault = "yes"
	}
	if len(cat) == 0 {
		fmt.Fprintln(o.Out, "Production products are not available in this build. You can test the guided flow and deployment using a disposable hello app.")
	}
	mode, err := p.ask("demo", "Use disposable demo catalog? yes/no", demoDefault, oneOf("yes", "no"))
	if err != nil {
		return err
	}
	st := &stack.Stack{Version: 1, Revision: 1, Demo: mode == "yes", InstallationID: stack.NewInstallationID()}
	if previous != nil {
		st.Revision = previous.Revision + 1
		if previous.InstallationID != "" { // written once
			st.InstallationID = previous.InstallationID
		}
	}
	if previous != nil && previous.Demo != st.Demo {
		return errors.New("use a separate state directory when changing between demo and production")
	}
	if st.Demo {
		cat, err = catalog.Load(catalog.Demo())
		if err != nil {
			return err
		}
		fmt.Fprintln(o.Out, "DEMO: hello is a diagnostic HTTP service, not a Ky product. Use a test target/root. SSO, owner admin grants and backups are not provisioned.")
	}
	count, err := p.ask("targets", "Number of target servers/clusters", "1", integer(1, 16))
	if err != nil {
		return err
	}
	n, _ := strconv.Atoi(count)
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("target.%d.", i)
		computer, err := p.ask(key+"name", "Computer name or DNS name (for example nas.example.lan)", fmt.Sprintf("server-%d", i+1), func(s string) bool { _, ok := computerName(s); return ok })
		if err != nil {
			return err
		}
		name, _ := computerName(computer)
		if name != computer {
			fmt.Fprintf(o.Out, "Using %s as this computer's installation label.\n", name)
		}
		kind, err := p.ask(key+"kind", "Target type: docker/kubernetes", "docker", oneOf("docker", "kubernetes"))
		if err != nil {
			return err
		}
		t := stack.Target{Name: name}
		if kind == "docker" {
			t.Host, err = p.ask(key+"host", "SSH server DNS name or IP", strings.TrimSuffix(computer, "."), host)
			if err != nil {
				return err
			}
			t.User, err = p.ask(key+"user", "SSH login (key/agent authentication)", "kyq", regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`).MatchString)
			if err != nil {
				return err
			}
			port, err := p.ask(key+"port", "SSH port", "22", integer(1, 65535))
			if err != nil {
				return err
			}
			t.Port, _ = strconv.Atoi(port)
			defaultRoot := stack.DefaultRoot
			if st.Demo {
				defaultRoot = "/opt/kyquickstart-demo"
			}
			t.Root, err = p.ask(key+"root", "Target installation directory", defaultRoot, func(s string) bool {
				return filepath.Clean(s) == s && regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`).MatchString(s)
			})
			if err != nil {
				return err
			}
			if !st.Demo {
				if t.Backup, err = askBackup(p, key); err != nil {
					return err
				}
			}
		} else {
			t.Kubeconfig, err = p.ask(key+"kubeconfig", "Absolute kubeconfig path outside installation state", "", filepath.IsAbs)
			if err != nil {
				return err
			}
			t.Context, err = p.ask(key+"context", "Kubernetes context (blank uses current)", "", nil)
			if err != nil {
				return err
			}
			t.StorageClass, err = p.ask(key+"storage_class", "StorageClass for app volumes (blank uses the cluster default)", "", nil)
			if err != nil {
				return err
			}
		}
		st.Targets = append(st.Targets, t)
	}
	var names []string
	for name := range cat {
		names = append(names, name)
	}
	sort.Strings(names)
	// KyDrive's editor is its option: asked right after KyDrive, and only when it installs.
	if i := slices.Index(names, euroOffice); i >= 0 && slices.Contains(names, "kydrive") {
		names = slices.Delete(names, i, i+1)
		names = slices.Insert(names, slices.Index(names, "kydrive")+1, euroOffice)
	}
	targetNames := []string{}
	for _, t := range st.Targets {
		targetNames = append(targetNames, t.Name)
	}
	for _, name := range names {
		if name == euroOffice && !slices.ContainsFunc(st.Apps, func(a stack.App) bool { return a.Name == "kydrive" }) {
			continue
		}
		app := cat[name]
		purpose := app.Description
		if name == "hello" && st.Demo {
			purpose = "Disposable diagnostic HTTP service for testing deployment"
		}
		if purpose == "" {
			purpose = app.Category + " application"
		}
		fmt.Fprintf(o.Out, "\n%s — %s\nRequired products: %s\n", name, purpose, strings.Join(app.DependsOn, ", "))
		if name == euroOffice {
			fmt.Fprintln(o.Out, "Optional for KyDrive. It runs on a Kubernetes target only, as root (Pod Security baseline, 4 GiB memory), behind that cluster's edge with its own route; KyDrive gets its URL and a shared secret.")
		}
		install, err := p.ask("app."+name+".install", "Install or skip?", "skip", oneOf("install", "skip"))
		if err != nil {
			return err
		}
		if install == "skip" {
			continue
		}
		eligible := []string{}
		for _, t := range st.Targets {
			if t.Kubernetes() && app.Kubernetes != nil || !t.Kubernetes() && app.Compose != nil {
				eligible = append(eligible, t.Name)
			}
		}
		if len(eligible) == 0 {
			return fmt.Errorf("%s has no compatible target; rerun plan to adjust targets", name)
		}
		placement, err := p.ask("app."+name+".target", "Install on target ("+strings.Join(eligible, ", ")+")", eligible[0], targetChoice(eligible))
		if err != nil {
			return err
		}
		placement, _ = computerName(placement)
		a := stack.App{Name: name, Target: placement}
		if app.Media != nil {
			fmt.Fprintf(o.Out, "%s sends call media (%s) straight to callers, not through the edge, so they are open to your LAN.\n", name, strings.Join(app.Media.Ports, ", "))
			if a.MediaAddress, err = p.ask("app."+name+".media_address", "Call media address: one LAN IPv4 address of "+placement+" (never a wildcard)", "", stack.MediaAddress); err != nil {
				return err
			}
		}
		if t, _ := st.Target(placement); app.NativeMail != nil && !t.Kubernetes() {
			if a.Mail, err = askMailbox(p, o.Out, name, placement); err != nil {
				return err
			}
		}
		st.Apps = append(st.Apps, a)
	}
	for _, app := range st.Apps {
		if err := guidedIntegration(p, st, stack.Existing{Product: app.Name, Target: app.Target}); err != nil {
			return err
		}
	}
	withChatClients(st, cat)
	count, err = p.ask("existing", "Number of existing Ky products to record", "0", integer(0, 64))
	if err != nil {
		return err
	}
	n, _ = strconv.Atoi(count)
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("existing.%d.", i)
		name, err := p.ask(key+"product", "Existing product name (for example kyidentity)", "", stack.ValidName)
		if err != nil {
			return err
		}
		target, err := p.ask(key+"target", "Existing product's target ("+strings.Join(targetNames, ", ")+")", targetNames[0], targetChoice(targetNames))
		if err != nil {
			return err
		}
		endpoint, err := p.ask(key+"url", "Existing product HTTPS origin", "", httpsOrigin)
		if err != nil {
			return err
		}
		target, _ = computerName(target)
		st.Existing = append(st.Existing, stack.Existing{Product: name, Target: target, URL: endpoint})
		if err := guidedIntegration(p, st, st.Existing[len(st.Existing)-1]); err != nil {
			return err
		}
	}
	if err := guidedEdges(p, st, cat); err != nil {
		return err
	}
	data, err := yaml.Marshal(st)
	if err != nil {
		return err
	}
	validated, err := stack.Parse(data)
	if err != nil {
		return fmt.Errorf("plan not published; rerun to correct saved answers: %w", err)
	}
	if err := validateSelection(validated, cat, o); err != nil {
		return fmt.Errorf("plan not published; rerun to correct saved answers: %w", err)
	}
	// Check kubeconfig placement and deployment compatibility before publishing the plan.
	if err := checkKubePaths(*state, validated); err != nil {
		return err
	}
	showPlan(o.Out, validated, cat)
	save, err := p.ask("save", "Save this installation plan? yes/no", "yes", oneOf("yes", "no"))
	if err != nil {
		return err
	}
	if save == "no" {
		fmt.Fprintln(o.Out, "Plan not published. Saved answers are available for editing.")
		return nil
	}
	if err := atomicFile(filepath.Join(*state, "stack.yaml"), data); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "\nPlan saved to %s\nNext commands:\n  kyquickstart review --state %s\n  kyquickstart preflight --state %s\n  kyquickstart apply --state %s\n", filepath.Join(*state, "stack.yaml"), remote.Quote(*state), remote.Quote(*state), remote.Quote(*state))
	if len(st.Integrations) > 0 {
		fmt.Fprintf(o.Out, "  kyquickstart integrate-stack --state %s --check\n  kyquickstart integrate-stack --state %s\n", remote.Quote(*state), remote.Quote(*state))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	delete(p.answers, "action") // Each run defaults to finishing without remote changes.
	action, err := p.ask("action", "Next action: finish/check/deploy (deploy changes selected targets)", "finish", oneOf("finish", "check", "deploy"))
	if err != nil {
		return err
	}
	if action == "finish" {
		return nil
	}
	o.In = p.in
	command := "preflight"
	if action == "deploy" {
		command = "apply"
	}
	return Run(ctx, []string{command, "--state", *state}, o)
}

func validateSelection(st *stack.Stack, cat catalog.Catalog, o Options) error {
	if err := yardPlan(st); err != nil {
		return err
	}
	if err := checkRunnerImage(slices.ContainsFunc(st.Apps, func(a stack.App) bool { return hostsRunner(cat[a.Name]) }), o); err != nil {
		return err
	}
	var names []string
	for _, app := range st.Apps {
		names = append(names, app.Name)
		t, _ := st.Target(app.Target)
		a, ok := cat[app.Name]
		if !ok {
			return fmt.Errorf("%s is unavailable", app.Name)
		}
		switch {
		case t.Kubernetes() && a.Kubernetes == nil:
			return fmt.Errorf("%s cannot run on %s: it has no Kubernetes deployment; place it on a Docker host", app.Name, app.Target)
		case !t.Kubernetes() && a.Compose == nil:
			return fmt.Errorf("%s cannot run on %s: it has no Compose deployment; place it on a Kubernetes target", app.Name, app.Target)
		}
		if _, err := withRoute(st, app.Name, t, a); err != nil {
			return err
		}
	}
	// After placement: an app's setup on the wrong kind of target is a placement error.
	if len(st.Integrations) > 0 {
		if _, err := integrationOrder(st); err != nil {
			return err
		}
	}
	if err := routedNeedsSetup(st); err != nil {
		return err
	}
	if err := exposurePlan(st, cat); err != nil {
		return err
	}
	if err := calendarAccounts(st); err != nil {
		return err
	}
	if err := messagePlan(st, cat); err != nil {
		return err
	}
	if _, err := plan.Order(names, cat); err != nil {
		return err
	}
	_, _, err := editorPlan(st, cat)
	return err
}

// routedNeedsSetup refuses a routed routedFirst app without its installed integration: its
// route would serve the open first-run web setup that only its setup closes.
func routedNeedsSetup(st *stack.Stack) error {
	for _, a := range st.Apps {
		if !slices.Contains(routedFirst, a.Name) || !slices.ContainsFunc(st.Edges, func(e stack.Edge) bool {
			return slices.ContainsFunc(e.Routes, func(r stack.Route) bool { return r.App == a.Name })
		}) {
			continue
		}
		if !slices.ContainsFunc(st.Integrations, func(i stack.Integration) bool { return i.Product == a.Name && i.Installed(st) }) {
			return fmt.Errorf("%s is routed but has no integration on %s: its route would expose its first-run web setup. Add {product: %s, target: %s, bundle: /data/installer/setup.json, identity_client: %s} to integrations, or remove its route", a.Name, a.Target, a.Name, a.Target, a.Name)
		}
	}
	return nil
}

// exposurePlan refuses an internal-only catalog app on an external route, and an external
// route signing in through a KyIdentity this plan routes only internally: the browser of
// whoever reaches that app must reach KyIdentity too.
func exposurePlan(st *stack.Stack, cat catalog.Catalog) error {
	var signIn string
	identity, identityExternal := false, false
	for _, e := range st.Edges {
		for _, r := range e.Routes {
			external := !e.Internal(r)
			if external && cat[r.App].Exposure == "internal" {
				return fmt.Errorf("%s is internal only: its route %s needs exposure: internal, which only allow_cidrs clients reach", r.App, r.Hostname)
			}
			if external && cat[r.App].OIDC != nil {
				signIn = r.Hostname
			}
			if r.App == "kyidentity" {
				identity, identityExternal = true, identityExternal || external
			}
		}
	}
	if signIn != "" && identity && !identityExternal {
		return fmt.Errorf("external route %s signs in through KyIdentity, so KyIdentity's route must be external too (LAN clients can still reach it directly through lan_dns)", signIn)
	}
	return nil
}

// calendarUsername is the rule KyCalendar's setup applies to every account it creates.
var calendarUsername = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`)

// calendarAccounts refuses a plan installing KyCalendar with an owner account its setup would
// refuse mid-apply: one outside its username rule, or its own break-glass "admin".
func calendarAccounts(st *stack.Stack) error {
	if !slices.ContainsFunc(st.Apps, func(a stack.App) bool { return a.Name == "kycalendar" }) {
		return nil
	}
	for _, i := range st.Integrations {
		if i.Product != "kyidentity" || i.Identity == nil || !i.Installed(st) {
			continue
		}
		for _, a := range [][2]string{{"admin", i.Identity.Admin.Username}, {"everyday", i.Identity.Everyday.Username}} {
			if u := a[1]; strings.EqualFold(u, "admin") || !calendarUsername.MatchString(u) {
				return fmt.Errorf(`KyCalendar cannot take the KyIdentity %s username %q: its usernames are 3 to 64 letters, digits, ".", "_" or "-", and "admin" is its own break-glass account. Change identity.%s.username in the kyidentity integration`, a[0], u, a[0])
			}
		}
	}
	return nil
}

func checkKubePaths(state string, st *stack.Stack) error {
	root, err := filepath.Abs(state)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	clusters := map[[2]string]string{}
	for _, t := range st.Targets {
		if !t.Kubernetes() {
			continue
		}
		kc := t.Kubeconfig
		if !filepath.IsAbs(kc) {
			kc = filepath.Join(root, kc)
		}
		kc, err = filepath.EvalSymlinks(kc)
		if err != nil {
			return fmt.Errorf("%s: kubeconfig file unavailable", t.Name)
		}
		rel, err := filepath.Rel(root, kc)
		if err != nil {
			return err
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("kubeconfig must be outside installation state")
		}
		key := [2]string{kc, t.Context}
		if other, ok := clusters[key]; ok {
			return fmt.Errorf("targets %s and %s are the same cluster; use one target", other, t.Name)
		}
		clusters[key] = t.Name
	}
	return nil
}

func showPlan(out io.Writer, st *stack.Stack, cat catalog.Catalog) {
	fmt.Fprintln(out, "\nInstallation plan")
	if st.Demo {
		fmt.Fprintln(out, "DEMO ONLY — suite identity, administrator grants and backups are unavailable.")
	}
	for _, t := range st.Targets {
		if t.Kubernetes() {
			fmt.Fprintf(out, "Target %s: Kubernetes (%s)\n", t.Name, t.Context)
			if t.StorageClass != "" {
				fmt.Fprintf(out, "  Volumes use StorageClass %s\n", t.StorageClass)
			}
		} else {
			fmt.Fprintf(out, "Target %s: Docker over SSH %s@%s:%d, files %s\n", t.Name, t.User, t.Host, t.Port, t.Root)
			if b := t.Backup; b != nil {
				ca := "system trust"
				if b.CAFile != "" {
					ca = "CA " + b.CAFile
				}
				fmt.Fprintf(out, "  Backups: %s/<app> (rest-server password in %s, %s); run by kyquickstart backup, never on a schedule\n", b.Repository, b.CredentialFile, ca)
			}
		}
	}
	for _, a := range st.Apps {
		fmt.Fprintf(out, "Install %s on %s; required: %s\n", a.Name, a.Target, strings.Join(cat[a.Name].DependsOn, ", "))
		if target, _ := st.Target(a.Target); target.Kubernetes() && len(cat[a.Name].Secrets) > 0 {
			fmt.Fprintf(out, "  Required temporary credential generator: %s (cluster-local; removed after generation)\n", kube.CredentialGeneratorImage)
		}
		if target, _ := st.Target(a.Target); target.Kubernetes() {
			if k := cat[a.Name].Kubernetes; k != nil && k.RunAs != 0 && len(k.Volumes) > 0 {
				fmt.Fprintf(out, "  Required volume init: %s (creates each claim's private directory)\n", kube.CredentialGeneratorImage)
			}
		}
		if m := cat[a.Name].Media; m != nil && a.MediaAddress != "" {
			fmt.Fprintf(out, "  Call media on %s: %s, open to the LAN, not through the edge; no TURN, federation closed\n", a.MediaAddress, strings.Join(m.Ports, ", "))
		}
		if m := a.Mail; m != nil {
			fmt.Fprintf(out, "  Native mail for %s (fixed at install); KyIdentity's directory creates the mailboxes\n", strings.Join(m.Domains, ", "))
			if r := m.Receiver; r != nil {
				fmt.Fprintf(out, "  Receiver: Maddy and Rspamd for %s on %s:25, PUBLIC TCP port 25; binary %s, TLS %s\n", r.Hostname, r.SMTPAddress, r.MaddyBinary, r.TLSDir)
			} else {
				fmt.Fprintln(out, "  No receiver yet: no mail port is published")
			}
		}
	}
	for _, e := range st.Existing {
		fmt.Fprintf(out, "Existing %s on %s: %s — integration pending, no adoption or changes\n", e.Product, e.Target, e.URL)
	}
	for _, i := range st.Integrations {
		if i.Installed(st) {
			fmt.Fprintf(out, "Selected setup %s on %s: protected bundle %s (apply runs it after health)\n", i.Product, i.Target, i.Bundle)
		} else {
			fmt.Fprintf(out, "Selected setup %s on %s: container %s, protected bundle %s (run integrate-stack explicitly)\n", i.Product, i.Target, i.Container, i.Bundle)
		}
		if i.Identity != nil {
			fmt.Fprintln(out, "    Prepare KyIdentity metadata and generate private owner/everyday/client credentials on target; resume preserves retained files.")
		}
		if i.Installed(st) && i.IdentityClient != "" {
			fmt.Fprintf(out, "  Add the catalog's %s client %s to KyIdentity's first setup and transfer its credentials in memory.\n", i.Product, i.IdentityClient)
		}
		if i.Product == "kynotes" && i.Installed(st) {
			fmt.Fprintln(out, "  KyNotes' setup runs after KyIdentity's route is live; KyNotes' route is added only after that setup creates its administrator, so its first-run web setup is never exposed.")
		}
		if i.Product == "kyrecovery" && i.IdentityClient != "" {
			fmt.Fprintln(out, "  The KyRecovery custodian ceremony stays a human step.")
		}
		if i.Notes != nil {
			if i.IdentityClient != "" {
				fmt.Fprintf(out, "  Transfer the selected KyIdentity client %s credentials in memory; resolve its dedicated owner and verify protected import on target.\n", i.IdentityClient)
			} else {
				fmt.Fprintln(out, "  Prepare native bundle from saved secret-free settings; credential files must already exist on target")
			}
		}
	}
	for _, e := range st.Edges {
		if e.Direct() {
			fmt.Fprintf(out, "Edge %s: Cloudflare tunnel %s straight to the apps, no NPM; cloudflared at %s on Docker network %s. Routes are public: no NPM path blocks or allow-list\n", e.Target, e.Connector.TunnelID, e.ConnectorCIDR, e.Connector.Network)
		} else if slices.ContainsFunc(st.Apps, func(a stack.App) bool { return a.Name == "npm" && a.Target == e.Target }) {
			fmt.Fprintf(out, "Edge %s: NPM installed by this plan (catalog npm), admin API %s, via %s\n", e.Target, e.NPMURL, e.Provider)
		} else {
			fmt.Fprintf(out, "Edge %s: existing NPM %s via %s\n", e.Target, e.NPMURL, e.Provider)
		}
		for _, c := range e.Certificates {
			fmt.Fprintf(out, "  Certificate %s requested by NPM from Let's Encrypt (DNS-01, %s): %s\n", c.Name, c.DNSProvider, strings.Join(c.Domains, ", "))
		}
		if e.Connector != nil && !e.Direct() {
			fmt.Fprintln(out, "  Deploy connector on existing Docker network", e.Connector.Network)
		}
		for _, r := range e.Routes {
			role := ""
			if r.Role != "" {
				role = " (" + r.App + " " + r.Role + ")"
			}
			fmt.Fprintf(out, "  https://%s -> %s:%d%s\n", e.URLHost(r), r.ForwardHost, r.ForwardPort, role)
		}
	}
	if len(st.Apps) == 0 {
		fmt.Fprintln(out, "No deployable apps selected. Existing product setup requires an explicit integration selection and integrate-stack.")
	}
	fmt.Fprintln(out, "Skipping a product preserves any existing installation and data.\nNo tunnel, frp server or VPS is provisioned: connectors use existing credentials. NPM is installed only on Kubernetes (catalog npm).")
}

func review(args []string, o Options) error {
	fl := flag.NewFlagSet("review", flag.ContinueOnError)
	fl.SetOutput(o.Out)
	state := fl.String("state", "./installation", "installation directory")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if fl.NArg() != 0 {
		return ErrUsage
	}
	st, err := stack.Load(filepath.Join(*state, "stack.yaml"))
	if err != nil {
		return err
	}
	cat, err := catalog.Load(o.Catalog)
	if st.Demo {
		cat, err = catalog.Load(catalog.Demo())
	}
	if err != nil {
		return err
	}
	if err := validateSelection(st, cat, o); err != nil {
		return err
	}
	if err := checkKubePaths(*state, st); err != nil {
		return err
	}
	showPlan(o.Out, st, cat)
	return nil
}

// preparationKey reports an integration answer owned by stack.yaml: notes and identity are
// never asked, and identity-client is asked only with stack.yaml's value as the default.
func preparationKey(k string) bool {
	return strings.HasPrefix(k, "integration.") && (strings.HasSuffix(k, ".notes") || strings.HasSuffix(k, ".identity") || strings.HasSuffix(k, ".identity-client"))
}

func seedAnswers(a map[string]string, st *stack.Stack) {
	a["targets"] = strconv.Itoa(len(st.Targets))
	a["existing"] = strconv.Itoa(len(st.Existing))
	if st.Demo {
		a["demo"] = "yes"
	}
	for i, t := range st.Targets {
		k := fmt.Sprintf("target.%d.", i)
		a[k+"name"], a[k+"host"], a[k+"user"], a[k+"port"], a[k+"root"], a[k+"kubeconfig"], a[k+"context"] = t.Name, t.Host, t.User, strconv.Itoa(t.Port), t.Root, t.Kubeconfig, t.Context
		a[k+"storage_class"] = t.StorageClass
		if b := t.Backup; b != nil {
			a[k+"backup.repository"], a[k+"backup.credential_file"], a[k+"backup.ca_file"] = b.Repository, b.CredentialFile, b.CAFile
		}
		a[k+"kind"] = "docker"
		if t.Kubernetes() {
			a[k+"kind"] = "kubernetes"
		}
	}
	for _, app := range st.Apps {
		a["app."+app.Name+".install"], a["app."+app.Name+".target"] = "install", app.Target
		if app.MediaAddress != "" {
			a["app."+app.Name+".media_address"] = app.MediaAddress
		}
		mailAnswers(a, app)
	}
	for i, e := range st.Existing {
		k := fmt.Sprintf("existing.%d.", i)
		a[k+"product"], a[k+"target"], a[k+"url"] = e.Product, e.Target, e.URL
	}
	for _, i := range st.Integrations {
		k := "integration." + i.Product + "." + i.Target + "."
		a[k+"configure"], a[k+"container"], a[k+"bundle"], a[k+"namespace"], a[k+"pod"] = "yes", i.Container, i.Bundle, i.Namespace, i.Pod
		a[k+"identity-client"], a[k+"chat-client"] = i.IdentityClient, i.ChatClient
		if i.Identity != nil && i.Identity.Mail != nil {
			a[k+"mail-password-file"] = i.Identity.Mail.PasswordFile // not in the preparation's JSON
		}
		if i.Notes != nil {
			raw, _ := json.Marshal(i.Notes)
			a[k+"notes"] = string(raw)
		}
		if i.Identity != nil {
			raw, _ := json.Marshal(i.Identity)
			a[k+"identity"] = string(raw)
		}
	}
	seedEdges(a, st.Edges)
}

// askBackup asks for a Docker target's off-host rest-server; a blank repository means none.
func askBackup(p *prompts, key string) (*stack.BackupDestination, error) {
	repo, err := p.ask(key+"backup.repository", "Off-host restic rest-server for backups, rest:https://<host>[:port]/<user> (blank for none)", "", func(s string) bool {
		return s == "" || stack.ValidBackupRepository(s)
	})
	if err != nil || repo == "" {
		return nil, err
	}
	b := &stack.BackupDestination{Repository: repo}
	if b.CredentialFile, err = p.ask(key+"backup.credential_file", "Absolute path on the target of the rest-server password file (mode 0600)", "", stack.ValidTargetPath); err != nil {
		return nil, err
	}
	b.CAFile, err = p.ask(key+"backup.ca_file", "Absolute path on the target of the rest-server's CA certificate (blank uses system trust)", "", func(s string) bool {
		return s == "" || stack.ValidTargetPath(s)
	})
	return b, err
}

func guidedEdges(p *prompts, st *stack.Stack, cat catalog.Catalog) error {
	for _, t := range st.Targets {
		apps := []string{}
		for _, a := range st.Apps {
			if a.Target == t.Name {
				apps = append(apps, a.Name)
			}
		}
		if len(apps) == 0 {
			continue
		}
		key := "edge." + t.Name + "."
		question, choices := "Public connection for "+t.Name+": none/cloudflare/cloudflare-direct (tunnel straight to the apps, no NPM)/frp/lan (LAN clients only, no tunnel)", []string{"none", "cloudflare", "cloudflare-direct", "frp", "lan"}
		if t.Kubernetes() {
			question, choices = "Public connection for "+t.Name+": none/cloudflare/frp/lan (LAN clients only, no tunnel); cloudflare-direct is Docker only", slices.DeleteFunc(choices, func(c string) bool { return c == "cloudflare-direct" })
		}
		provider, err := p.ask(key+"provider", question, "none", oneOf(choices...))
		if err != nil {
			return err
		}
		if provider == "none" {
			continue
		}
		e := stack.Edge{Target: t.Name, Provider: provider}
		if e.Direct() {
			fmt.Fprintln(p.out, "cloudflare-direct deploys cloudflared for an existing locally managed tunnel; its routes are public, with no NPM path blocks or allow-list. No credential values are entered here.")
			err = askDirect(p, key, &e)
		} else {
			fmt.Fprintln(p.out, "This build reuses existing NPM and certificates, and an existing tunnel or frp server. No credential values are entered here.")
			if e.NPMURL, err = p.ask(key+"npm", "Existing NPM LAN HTTPS management origin", "", httpsOrigin); err != nil {
				return err
			}
			if provider == "lan" {
				err = askLAN(p, key, &e)
			} else {
				err = askConnector(p, key, t, &e)
			}
		}
		if err != nil {
			return err
		}
		for _, name := range apps {
			rk := key + "route." + name + "."
			if e.Direct() {
				if err := directAllowed(name); err != nil {
					fmt.Fprintln(p.out, err)
					continue
				}
			}
			yes, err := p.ask(rk+"enabled", "Expose "+name+" through this edge? yes/no", "yes", oneOf("yes", "no"))
			if err != nil {
				return err
			}
			if yes == "no" {
				continue
			}
			exposure, err := askExposure(p, rk, e, name, cat[name])
			if err != nil {
				return err
			}
			before := len(e.Routes)
			if e.Direct() {
				r, err := askDirectRoute(p, rk, name)
				if err != nil {
					return err
				}
				e.Routes = append(e.Routes, r)
				continue
			}
			if len(cat[name].Routes) > 0 {
				routes, err := askRoutes(p, rk, st, name, cat[name])
				if err != nil {
					return err
				}
				e.Routes = append(e.Routes, routes...)
				setExposure(e.Routes[before:], exposure)
				continue
			}
			r := stack.Route{App: name}
			r.Hostname, err = p.ask(rk+"hostname", "Product's public hostname", "", stack.Hostname)
			if err != nil {
				return err
			}
			r.ForwardHost, err = p.ask(rk+"host", "Product upstream hostname/IP as seen from NPM", name, host)
			if err != nil {
				return err
			}
			port, err := p.ask(rk+"port", "Product upstream port (not NPM administration)", "8080", func(s string) bool { return integer(1, 65535)(s) && s != "81" })
			if err != nil {
				return err
			}
			r.ForwardPort, _ = strconv.Atoi(port)
			r.ForwardScheme, err = p.ask(rk+"scheme", "Product upstream scheme", "http", oneOf("http", "https"))
			if err != nil {
				return err
			}
			id, err := p.ask(rk+"certificate", "Existing NPM certificate ID", "", integer(1, 2147483647))
			if err != nil {
				return err
			}
			r.CertificateID, _ = strconv.Atoi(id)
			r.StatusPath, err = p.ask(rk+"status", "Product G11 status path", "/api/status", statusPath)
			if err != nil {
				return err
			}
			e.Routes = append(e.Routes, r)
			setExposure(e.Routes[before:], exposure)
		}
		if len(e.Routes) == 0 {
			return errors.New("edge has no routes; rerun plan and choose none or enable a route")
		}
		if (e.Provider == "cloudflare" || e.Provider == "frp") && !slices.ContainsFunc(e.Routes, func(r stack.Route) bool { return r.Exposure != "internal" }) {
			return fmt.Errorf("every route on %s's %s edge is internal, so the tunnel would carry none; rerun plan and choose lan", t.Name, e.Provider)
		}
		if (e.Provider == "cloudflare" || e.Provider == "frp") && slices.ContainsFunc(e.Routes, func(r stack.Route) bool { return r.Exposure == "internal" }) {
			fmt.Fprintln(p.out, "Internal routes serve only the LAN clients NPM is reached by directly; the tunnel never reaches them.")
			if err := askLAN(p, key, &e); err != nil {
				return err
			}
		}
		if e.Direct() {
			fmt.Fprintf(p.out, "The tunnel credentials must already be stored on %s in %s/edge/secrets/cloudflare_tunnel_credentials (mode 0600). See docs/edge.md.\n", t.Name, t.Root)
		} else {
			fmt.Fprintf(p.out, "NPM token must already be stored on %s in %s/edge/secrets/npm_token (mode 0600). See docs/edge.md for connector credentials and Kubernetes Secrets.\n", t.Name, t.Root)
		}
		st.Edges = append(st.Edges, e)
	}
	return nil
}

// askRoutes asks a multi-route app's hostnames, defaulting each role under one domain whose
// apex is the role with paths (KyMessages' server name), its address and its ports.
func askRoutes(p *prompts, rk string, st *stack.Stack, name string, app catalog.App) ([]stack.Route, error) {
	domain, err := p.ask(rk+"domain", name+"'s domain. It becomes the permanent Matrix server name (accounts are @name:<domain>) and the other hostnames go under it", "", stack.Hostname)
	if err != nil {
		return nil, err
	}
	fallbacks := map[string]string{}
	var list []string
	for _, cr := range app.Routes {
		switch {
		case cr.Role == app.Primary():
			fallbacks[cr.Role] = "message." + domain
		case len(cr.Paths) > 0:
			fallbacks[cr.Role] = domain
		default:
			fallbacks[cr.Role] = cr.Role + "." + domain
		}
		list = append(list, fallbacks[cr.Role]+" ("+cr.Role+")")
	}
	fmt.Fprintf(p.out, "%s's hostnames (each can be changed next): %s. Each needs a DNS record to the edge, and one certificate covering them all.\n", name, strings.Join(list, ", "))
	target, media := "", ""
	for _, a := range st.Apps {
		if a.Name == name {
			target, media = a.Target, a.MediaAddress
		}
	}
	host, err := p.ask(rk+"host", name+"'s address on "+target+" that NPM reaches: one LAN IPv4 address, where every routed port is published", media, stack.MediaAddress)
	if err != nil {
		return nil, err
	}
	id, err := p.ask(rk+"certificate", "Existing NPM certificate ID covering "+domain+" and *."+domain+" (a wildcard alone misses the apex)", "", integer(1, 2147483647))
	if err != nil {
		return nil, err
	}
	cert, _ := strconv.Atoi(id)
	var routes []stack.Route
	ports, used := map[string]int{}, map[int]bool{} // service:port -> forward_port
	for _, cr := range app.Routes {
		r := stack.Route{App: name, ForwardHost: host, ForwardScheme: "http", CertificateID: cert}
		k := rk + cr.Role + "."
		switch {
		case cr.Role == app.Primary():
			k = rk
		case len(cr.Paths) > 0:
			fmt.Fprintf(p.out, "%s's %s hostname is permanent: every account's ID ends in it.\n", name, cr.Role)
		}
		if cr.Role != app.Primary() {
			r.Role = cr.Role
		}
		if r.Hostname, err = p.ask(k+"hostname", name+" "+cr.Role+" hostname", fallbacks[cr.Role], stack.Hostname); err != nil {
			return nil, err
		}
		key := fmt.Sprint(cr.Service, ":", cr.Port)
		if port, ok := ports[key]; ok {
			r.ForwardPort = port
		} else {
			fallback := cr.Port
			for used[fallback] || fallback == 81 {
				fallback++
			}
			port, err := p.ask(k+"port", name+" "+cr.Role+" port published on "+host, strconv.Itoa(fallback), func(s string) bool {
				n, err := strconv.Atoi(s)
				return integer(1, 65535)(s) && s != "81" && err == nil && !used[n]
			})
			if err != nil {
				return nil, err
			}
			r.ForwardPort, _ = strconv.Atoi(port)
			ports[key], used[r.ForwardPort] = r.ForwardPort, true
		}
		routes = append(routes, r)
	}
	return routes, nil
}

// askDirect asks for a cloudflare-direct edge's cloudflared: its address and network, and the tunnel.
func askDirect(p *prompts, key string, e *stack.Edge) error {
	var err error
	if e.ConnectorCIDR, err = p.ask(key+"cidr", "cloudflared's fixed IPv4 /32 on the edge network (outside its --ip-range); the apps trust exactly this address", "", connectorCIDR); err != nil {
		return err
	}
	c := &stack.Connector{}
	if c.Network, err = p.ask(key+"network", "Existing Docker network dedicated to this edge (explicit --subnet and --ip-range)", "", stack.ValidName); err != nil {
		return err
	}
	if c.TunnelID, err = p.ask(key+"tunnel_id", "Existing locally managed Cloudflare tunnel UUID", "", tunnelID); err != nil {
		return err
	}
	e.Connector = c
	return nil
}

// askDirectRoute asks a cloudflare-direct route: cloudflared reaches the app on the edge
// network, so its published port stays on the target's loopback.
func askDirectRoute(p *prompts, rk, name string) (stack.Route, error) {
	r := stack.Route{App: name, ForwardHost: "127.0.0.1", ForwardScheme: "http"}
	var err error
	if r.Hostname, err = p.ask(rk+"hostname", "Product's public hostname", "", stack.Hostname); err != nil {
		return r, err
	}
	port, err := p.ask(rk+"port", "Port "+name+" publishes on the target's loopback (unique on the target)", strconv.Itoa(directApps[name]), integer(1, 65535))
	if err != nil {
		return r, err
	}
	r.ForwardPort, _ = strconv.Atoi(port)
	r.StatusPath, err = p.ask(rk+"status", "Product G11 status path", "/api/status", statusPath)
	return r, err
}

func statusPath(s string) bool {
	u, err := url.ParseRequestURI(s)
	return err == nil && strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") && u.RawQuery == "" && u.Fragment == ""
}

func connectorCIDR(s string) bool {
	ip, err := netip.ParsePrefix(s)
	return err == nil && ip.Addr().Is4() && ip.Bits() == 32 && !ip.Addr().IsUnspecified() && !ip.Addr().IsMulticast()
}

func tunnelID(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) != 5 {
		return false
	}
	for i, size := range []int{8, 4, 4, 4, 12} {
		if len(parts[i]) != size || strings.Trim(parts[i], "0123456789abcdef") != "" {
			return false
		}
	}
	return true
}

// askLAN asks which LAN clients NPM serves directly.
func askLAN(p *prompts, key string, e *stack.Edge) error {
	split := func(s string) []string {
		out := strings.Split(s, ",")
		for i := range out {
			out[i] = strings.TrimSpace(out[i])
		}
		return out
	}
	cidrs, err := p.ask(key+"allow_cidrs", "LAN client prefixes NPM serves, comma-separated (for example 192.168.1.0/24)", "", func(s string) bool {
		return !slices.ContainsFunc(split(s), func(c string) bool { return !stack.AllowCIDR(c) })
	})
	if err != nil {
		return err
	}
	e.AllowCIDRs = split(cidrs)
	port, err := p.ask(key+"client_port", "Port LAN clients reach NPM's HTTPS on (a NodePort, or 443)", "443", integer(1, 65535))
	e.ClientPort, _ = strconv.Atoi(port)
	return err
}

// askConnector asks for a tunnel edge's connector.
func askConnector(p *prompts, key string, t stack.Target, e *stack.Edge) error {
	var err error
	e.ConnectorCIDR, err = p.ask(key+"cidr", "Connector's fixed source IPv4 /32", "", connectorCIDR)
	if err != nil {
		return err
	}
	if e.Provider == "frp" {
		port, err := p.ask(key+"proxy_port", "Dedicated private NPM PROXY-protocol TLS port", "8443", integer(1024, 65535))
		if err != nil {
			return err
		}
		e.ProxyPort, _ = strconv.Atoi(port)
	}
	deploy := "no"
	if !t.Kubernetes() {
		deploy, err = p.ask(key+"deploy", "Deploy connector on this Docker target? yes/no", "no", oneOf("yes", "no"))
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintln(p.out, "Kubernetes connector deployment is unavailable. Only an externally managed connector can be referenced.")
	}
	if deploy == "yes" {
		c := &stack.Connector{}
		c.Network, err = p.ask(key+"network", "Existing shared Docker network", "", stack.ValidName)
		if err != nil {
			return err
		}
		c.NPMHost, err = p.ask(key+"npm_host", "NPM hostname reachable inside that network", "", host)
		if err != nil {
			return err
		}
		if e.Provider == "frp" {
			c.FRPServer, err = p.ask(key+"frp_server", "Existing frp server TLS hostname", "", host)
			if err != nil {
				return err
			}
			port, err := p.ask(key+"frp_port", "frp control port", "7000", integer(1, 65535))
			if err != nil {
				return err
			}
			c.FRPPort, _ = strconv.Atoi(port)
		} else {
			c.TunnelID, err = p.ask(key+"tunnel_id", "Existing locally managed Cloudflare tunnel UUID", "", tunnelID)
			if err != nil {
				return err
			}
		}
		e.Connector = c
	}
	return nil
}

// askExposure asks whether a tunnel edge's route for name is internal or external; an
// internal-only catalog app is internal without asking. Other edges have one exposure: "".
func askExposure(p *prompts, rk string, e stack.Edge, name string, app catalog.App) (string, error) {
	if e.Provider != "cloudflare" && e.Provider != "frp" {
		return "", nil
	}
	if app.Exposure == "internal" {
		fmt.Fprintf(p.out, "%s is internal only: LAN clients reach it, the tunnel does not.\n", name)
		return "internal", nil
	}
	return p.ask(rk+"exposure", "Who reaches "+name+": internal (LAN clients only)/external (through the tunnel)", "external", oneOf("internal", "external"))
}

// setExposure marks routes with exposure; "" leaves them the edge's own.
func setExposure(routes []stack.Route, exposure string) {
	for i := range routes {
		routes[i].Exposure = exposure
	}
}

func seedEdges(a map[string]string, edges []stack.Edge) {
	for _, e := range edges {
		k := "edge." + e.Target + "."
		a[k+"provider"], a[k+"npm"], a[k+"cidr"], a[k+"proxy_port"] = e.Provider, e.NPMURL, e.ConnectorCIDR, strconv.Itoa(e.ProxyPort)
		if len(e.AllowCIDRs) > 0 {
			a[k+"allow_cidrs"], a[k+"client_port"] = strings.Join(e.AllowCIDRs, ","), strconv.Itoa(e.ClientPort)
		}
		if c := e.Connector; c != nil {
			a[k+"deploy"] = "yes"
			a[k+"network"], a[k+"npm_host"], a[k+"frp_server"], a[k+"frp_port"], a[k+"tunnel_id"] = c.Network, c.NPMHost, c.FRPServer, strconv.Itoa(c.FRPPort), c.TunnelID
		}
		for _, r := range e.Routes {
			k := k + "route." + r.App + "."
			a[k+"enabled"] = "yes"
			if r.Exposure != "" {
				a[k+"exposure"] = r.Exposure
			}
			if r.Role != "" {
				a[k+r.Role+".hostname"], a[k+r.Role+".port"] = r.Hostname, strconv.Itoa(r.ForwardPort)
				continue
			}
			a[k+"hostname"], a[k+"host"], a[k+"port"], a[k+"scheme"], a[k+"certificate"], a[k+"status"] = r.Hostname, r.ForwardHost, strconv.Itoa(r.ForwardPort), r.ForwardScheme, strconv.Itoa(r.CertificateID), r.StatusPath
			a[k+"domain"] = ""
		}
	}
}
