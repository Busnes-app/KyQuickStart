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
				if seen[r.App] {
					return errors.New("this saved plan has multiple routes per app; edit stack.yaml directly to preserve them")
				}
				seen[r.App] = true
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
	targetNames := []string{}
	for _, t := range st.Targets {
		targetNames = append(targetNames, t.Name)
	}
	for _, name := range names {
		app := cat[name]
		purpose := app.Description
		if name == "hello" && st.Demo {
			purpose = "Disposable diagnostic HTTP service for testing deployment"
		}
		if purpose == "" {
			purpose = app.Category + " application"
		}
		fmt.Fprintf(o.Out, "\n%s — %s\nRequired products: %s\n", name, purpose, strings.Join(app.DependsOn, ", "))
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
		st.Apps = append(st.Apps, stack.App{Name: name, Target: placement})
	}
	for _, app := range st.Apps {
		if err := guidedIntegration(p, st, stack.Existing{Product: app.Name, Target: app.Target}); err != nil {
			return err
		}
	}
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
	if err := guidedEdges(p, st); err != nil {
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
	if err := validateSelection(validated, cat); err != nil {
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

func validateSelection(st *stack.Stack, cat catalog.Catalog) error {
	if len(st.Integrations) > 0 {
		if _, err := integrationOrder(st); err != nil {
			return err
		}
	}
	var names []string
	for _, app := range st.Apps {
		names = append(names, app.Name)
		t, _ := st.Target(app.Target)
		a, ok := cat[app.Name]
		if !ok {
			return fmt.Errorf("%s is unavailable", app.Name)
		}
		if t.Kubernetes() && a.Kubernetes == nil || !t.Kubernetes() && a.Compose == nil {
			return fmt.Errorf("%s cannot run on %s", app.Name, app.Target)
		}
	}
	_, err := plan.Order(names, cat)
	return err
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
		if i.Notes != nil {
			if i.IdentityClient != "" {
				fmt.Fprintf(out, "  Transfer the selected KyIdentity client %s credentials in memory; resolve its dedicated owner and verify protected import on target.\n", i.IdentityClient)
			} else {
				fmt.Fprintln(out, "  Prepare native bundle from saved secret-free settings; credential files must already exist on target")
			}
		}
	}
	for _, e := range st.Edges {
		fmt.Fprintf(out, "Edge %s: existing NPM %s via %s\n", e.Target, e.NPMURL, e.Provider)
		if e.Connector != nil {
			fmt.Fprintln(out, "  Deploy connector on existing Docker network", e.Connector.Network)
		}
		for _, r := range e.Routes {
			fmt.Fprintf(out, "  https://%s -> %s:%d\n", r.Hostname, r.ForwardHost, r.ForwardPort)
		}
	}
	if len(st.Apps) == 0 {
		fmt.Fprintln(out, "No deployable apps selected. Existing product setup requires an explicit integration selection and integrate-stack.")
	}
	fmt.Fprintln(out, "Skipping a product preserves any existing installation and data.\nAutomatic suite provisioning, new NPM, certificates and tunnel/VPS provisioning remain unavailable.")
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
	if err := validateSelection(st, cat); err != nil {
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
		a[k+"kind"] = "docker"
		if t.Kubernetes() {
			a[k+"kind"] = "kubernetes"
		}
	}
	for _, app := range st.Apps {
		a["app."+app.Name+".install"], a["app."+app.Name+".target"] = "install", app.Target
	}
	for i, e := range st.Existing {
		k := fmt.Sprintf("existing.%d.", i)
		a[k+"product"], a[k+"target"], a[k+"url"] = e.Product, e.Target, e.URL
	}
	for _, i := range st.Integrations {
		k := "integration." + i.Product + "." + i.Target + "."
		a[k+"configure"], a[k+"container"], a[k+"bundle"], a[k+"namespace"], a[k+"pod"] = "yes", i.Container, i.Bundle, i.Namespace, i.Pod
		a[k+"identity-client"] = i.IdentityClient
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

func guidedEdges(p *prompts, st *stack.Stack) error {
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
		provider, err := p.ask(key+"provider", "Public connection for "+t.Name+": none/cloudflare/frp/lan (LAN clients only, no tunnel)", "none", oneOf("none", "cloudflare", "frp", "lan"))
		if err != nil {
			return err
		}
		if provider == "none" {
			continue
		}
		fmt.Fprintln(p.out, "This build reuses existing NPM and certificates, and an existing tunnel or frp server. No credential values are entered here.")
		e := stack.Edge{Target: t.Name, Provider: provider}
		e.NPMURL, err = p.ask(key+"npm", "Existing NPM LAN HTTPS management origin", "", httpsOrigin)
		if err != nil {
			return err
		}
		if provider == "lan" {
			err = askLAN(p, key, &e)
		} else {
			err = askConnector(p, key, t, &e)
		}
		if err != nil {
			return err
		}
		for _, name := range apps {
			rk := key + "route." + name + "."
			yes, err := p.ask(rk+"enabled", "Expose "+name+" through this edge? yes/no", "yes", oneOf("yes", "no"))
			if err != nil {
				return err
			}
			if yes == "no" {
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
			r.StatusPath, err = p.ask(rk+"status", "Product G11 status path", "/api/status", func(s string) bool {
				u, err := url.ParseRequestURI(s)
				return err == nil && strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") && u.RawQuery == "" && u.Fragment == ""
			})
			if err != nil {
				return err
			}
			e.Routes = append(e.Routes, r)
		}
		if len(e.Routes) == 0 {
			return errors.New("edge has no routes; rerun plan and choose none or enable a route")
		}
		fmt.Fprintf(p.out, "NPM token must already be stored on %s in %s/edge/secrets/npm_token (mode 0600). See docs/edge.md for connector credentials and Kubernetes Secrets.\n", t.Name, t.Root)
		st.Edges = append(st.Edges, e)
	}
	return nil
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
	e.ConnectorCIDR, err = p.ask(key+"cidr", "Connector's fixed source IPv4 /32", "", func(s string) bool {
		ip, err := netip.ParsePrefix(s)
		return err == nil && ip.Addr().Is4() && ip.Bits() == 32 && !ip.Addr().IsUnspecified() && !ip.Addr().IsMulticast()
	})
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
			c.TunnelID, err = p.ask(key+"tunnel_id", "Existing locally managed Cloudflare tunnel UUID", "", func(s string) bool {
				parts := strings.Split(s, "-")
				if len(parts) != 5 {
					return false
				}
				sizes := []int{8, 4, 4, 4, 12}
				for i, v := range parts {
					if len(v) != sizes[i] {
						return false
					}
					for _, ch := range v {
						if !strings.ContainsRune("0123456789abcdef", ch) {
							return false
						}
					}
				}
				return true
			})
			if err != nil {
				return err
			}
		}
		e.Connector = c
	}
	return nil
}

func seedEdges(a map[string]string, edges []stack.Edge) {
	for _, e := range edges {
		k := "edge." + e.Target + "."
		a[k+"provider"], a[k+"npm"], a[k+"cidr"], a[k+"proxy_port"] = e.Provider, e.NPMURL, e.ConnectorCIDR, strconv.Itoa(e.ProxyPort)
		if e.Provider == "lan" {
			a[k+"allow_cidrs"], a[k+"client_port"] = strings.Join(e.AllowCIDRs, ","), strconv.Itoa(e.ClientPort)
		}
		if c := e.Connector; c != nil {
			a[k+"deploy"] = "yes"
			a[k+"network"], a[k+"npm_host"], a[k+"frp_server"], a[k+"frp_port"], a[k+"tunnel_id"] = c.Network, c.NPMHost, c.FRPServer, strconv.Itoa(c.FRPPort), c.TunnelID
		}
		for _, r := range e.Routes {
			k := k + "route." + r.App + "."
			a[k+"enabled"] = "yes"
			a[k+"hostname"], a[k+"host"], a[k+"port"], a[k+"scheme"], a[k+"certificate"], a[k+"status"] = r.Hostname, r.ForwardHost, strconv.Itoa(r.ForwardPort), r.ForwardScheme, strconv.Itoa(r.CertificateID), r.StatusPath
		}
	}
}
