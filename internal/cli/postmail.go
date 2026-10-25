package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

// KyPost's opt-in native mail (a plan app's mail block): the native rendering of its catalog
// template, its domains in the setup bundle, KyIdentity's directory and, once the receiving
// spool exists, the bundled Maddy receiver with Rspamd on port 25. See docs/guided-setup.md
// ("KyPost native mail") and KyPost's docs/guides/NATIVE_MAIL.md.

// mailOf is app's mail block in st, nil for none.
func mailOf(st *stack.Stack, app string) *stack.Mail {
	for _, a := range st.Apps {
		if a.Name == app {
			return a.Mail
		}
	}
	return nil
}

// withMail renders app for its plan's mail block: the native rendering, or the receiver's with
// the operator's receiver inputs. Without a block it is the ordinary rendering, which opens no
// mail port. It runs before the route references are filled.
func withMail(st *stack.Stack, name string, t stack.Target, app catalog.App) (catalog.App, error) {
	m := mailOf(st, name)
	switch {
	case m == nil:
		return app, nil
	case !app.MailModes():
		return app, fmt.Errorf("app %q has no native mail: remove its mail block", name)
	case t.Kubernetes():
		return app, fmt.Errorf("app %q: native mail runs on a Docker target only (KyPost's receiver needs a host's TCP 25 and its files); place it on a Docker host", name)
	case m.Receiver == nil:
		return app.WithMail(catalog.MailNative, catalog.MailRefs{})
	}
	r := m.Receiver
	return app.WithMail(catalog.MailReceiver, catalog.MailRefs{SMTPAddress: r.SMTPAddress, Hostname: r.Hostname, MaddyBinary: r.MaddyBinary, TLSDir: r.TLSDir})
}

// mailPlan refuses a mail block without the app's installed setup: its domains go in that
// bundle, and KyIdentity's directory, which creates the mailboxes, is a pass of its client's.
func mailPlan(st *stack.Stack) error {
	for _, a := range st.Apps {
		if a.Mail == nil {
			continue
		}
		if !slices.ContainsFunc(st.Integrations, func(i stack.Integration) bool {
			return i.Product == a.Name && i.IdentityClient != "" && i.Installed(st)
		}) {
			return fmt.Errorf("app %q: native mail needs its setup through KyIdentity, an integrations entry for %s with identity_client: it applies the domains and KyIdentity's directory, which creates the mailboxes", a.Name, a.Name)
		}
	}
	return nil
}

// postSpool is KyPost's receiving spool, which `kypost-server receiving init` creates once.
const postSpool = "/kypost/state/receiving"

// deployedMail reads how app's last deployment on host ran: found is false when it has no
// Compose file there.
func deployedMail(ctx context.Context, host sshDriver, app string) (native, receiver, found bool, err error) {
	f := path.Join(host.t.Root, app, "compose.yaml")
	out, err := host.c.Run(ctx, "if [ -f "+remote.Quote(f)+" ]; then head -c 65537 "+remote.Quote(f)+"; fi", nil)
	if err != nil {
		return false, false, false, fmt.Errorf("read %s's deployment on %s: %w", app, host.t.Name, err)
	}
	if len(out) == 0 {
		return false, false, false, nil
	}
	var c struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if len(out) > 65536 || yaml.Unmarshal(out, &c) != nil {
		return false, false, false, fmt.Errorf("%s on %s is not readable; nothing was changed", f, host.t.Name)
	}
	env := c.Services["app"].Environment
	mode := env["KYPOST_NATIVE_MAIL"]
	if mode != "true" && mode != "false" {
		return false, false, false, fmt.Errorf("%s on %s does not say whether KyPost runs native mail; nothing was changed", f, host.t.Name)
	}
	return mode == "true", env["KYPOST_NATIVE_RECEIVER"] == "true", true, nil
}

// refuseMailChange refuses, before any step, a plan that would convert an installed KyPost
// between webmail for existing IMAP accounts and native mail, either way: KyPost converts
// no IMAP account, and native users lose their mailboxes without it. KyPost data left by an
// uninstall counts as installed. It also refuses adding the receiver before the receiving
// spool exists, which KyPost's receiver needs to start.
func (s *session) refuseMailChange(ctx context.Context) error {
	for name, t := range s.placed {
		host, ok := s.drivers[t.Name].(sshDriver)
		if !ok || s.cat[name].NativeMail == nil {
			continue
		}
		m := mailOf(s.plan, name)
		native, receiver, found, err := deployedMail(ctx, host, name)
		if err != nil {
			return err
		}
		if !found && m != nil {
			// No Compose file, but data from an earlier install (uninstall keeps volumes) is
			// of an unknown mode: never start native mail on it.
			if _, err := host.c.Run(ctx, "docker volume inspect "+remote.Quote("kyq-"+name+"_state")+" >/dev/null 2>&1", nil); err == nil {
				return fmt.Errorf("%s on %s has data from an earlier install (Docker volume kyq-%s_state) and no record of its mail mode: native mail needs a fresh KyPost. Remove the mail block, or install on a host without KyPost data; nothing was changed", name, t.Name, name)
			}
		}
		switch {
		case found && !native && m != nil:
			return fmt.Errorf("%s on %s is installed as webmail for existing IMAP accounts: KyPost converts no IMAP account to native mail, so native mail needs a fresh KyPost (a new install, with new KyIdentity identities). Remove the mail block to keep this one; nothing was changed", name, t.Name)
		case found && native && m == nil:
			return fmt.Errorf("%s on %s runs native mail: without it its users lose access to their mailboxes, which are not converted back to IMAP. Restore the mail block; nothing was changed", name, t.Name)
		case m != nil && m.Receiver != nil && !receiver:
			_, test, err := nativeProductExec(host, stack.Integration{Product: name}, productAdapter{binary: "test", runtimeUser: "kypost"})
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("%s's receiver needs its receiving spool, which KyPost creates only after a domain is verified and a mailbox exists: apply native mail without mail.receiver first; nothing was changed", name)
			}
			if _, err := test(ctx, []string{"-d", postSpool}, nil); err != nil {
				return fmt.Errorf("%s on %s has no receiving spool (%s), so its receiver would not start: once a domain is verified and a mailbox exists, create it once with\n  docker exec --user kypost -e KYPOST_NATIVE_RECEIVING=true %s kypost-server receiving init\nthen apply again; nothing was changed", name, t.Name, postSpool, postContainer)
			}
		}
	}
	return nil
}

// mailFindings are the read-only preflight checks of the receiver inputs on host.
func mailFindings(ctx context.Context, host sshDriver, app string, m *stack.Mail, n *catalog.NativeMail) []dockerhost.Finding {
	if m == nil || m.Receiver == nil || n == nil {
		return nil
	}
	return dockerhost.ReceiverChecks(ctx, host.c, app, *m.Receiver, n.MaddySHA256)
}

// postBundle is the catalog KyPost's native version-1 setup: sign-in through KyIdentity, the
// client secret in a file beside the bundle and, with native mail, its domains. autoProvision
// creates an account at first sign-in for whoever KyIdentity lets through; KyIdentity's
// assignment to the client is the gate. A native mailbox comes only from KyIdentity's directory.
func postBundle(domains []string) func(clientCredentials, string) any {
	return func(f clientCredentials, secretFile string) any {
		type sso struct {
			Enabled       bool   `json:"enabled"`
			IssuerURL     string `json:"issuerUrl"`
			ClientID      string `json:"clientId"`
			AutoProvision bool   `json:"autoProvision"`
		}
		return struct {
			Version          int      `json:"version"`
			SSO              sso      `json:"sso"`
			ClientSecretFile string   `json:"clientSecretFile"`
			Domains          []string `json:"domains,omitempty"`
		}{1, sso{true, f.IssuerURL, f.ClientID, true}, secretFile, domains}
	}
}

var postTXT = regexp.MustCompile(`^kypost-mail-verify=[0-9a-f]{64}$`)

// postRecords is what apply prints of KyPost's setup report with native mail: each domain's
// TXT proof as KyPost reports it, then the MX and A records the receiver needs (only once it
// is in the plan) and what KyPost still reports pending. Every planned domain must be in the
// report, and every reported record must be that domain's _kypost-mail TXT challenge.
func postRecords(report []byte, m *stack.Mail, target string) (string, error) {
	var r struct {
		DNS []struct {
			Domain, Name, Value string
			Established         bool
		}
		Pending []string
	}
	if json.Unmarshal(report, &r) != nil {
		return "", errors.New("invalid KyPost configuration report")
	}
	var b strings.Builder
	b.WriteString("\nKyPost mail DNS records. Publish them at your DNS provider; KyQuickStart edits no DNS:\n")
	reported := map[string]bool{}
	for _, d := range r.DNS {
		if !stack.MailDomain(d.Domain) || d.Name != "_kypost-mail."+d.Domain || !postTXT.MatchString(d.Value) {
			return "", errors.New("KyPost reported a DNS record that is not a domain's _kypost-mail TXT challenge")
		}
		reported[d.Domain] = true
		state := "not yet verified: publish it, then Verify in Admin → Server → Mail domain"
		if d.Established {
			state = "verified; keep it published"
		}
		fmt.Fprintf(&b, "  TXT  %s  %q  (%s)\n", d.Name, d.Value, state)
	}
	for _, d := range m.Domains {
		if !reported[d] {
			return "", fmt.Errorf("KyPost's setup report has no TXT challenge for planned domain %s", d)
		}
	}
	b.WriteString("  Keep every TXT record published while its domain is in use: without it that domain stops\n  receiving, sending and getting mailboxes. SPF, DKIM, a custom MAIL FROM and DMARC are your\n  relay provider's and yours to publish; KyPost signs no DKIM.\n")
	if rc := m.Receiver; rc != nil {
		for _, d := range m.Domains {
			fmt.Fprintf(&b, "  MX   %s  10 %s\n", d, rc.Hostname)
		}
		fmt.Fprintf(&b, "  A    %s  the public IPv4 address whose TCP 25 reaches %s:25 on %s (the certificate in\n       %s must name %s). This install publishes IPv4 only: add no AAAA record.\n", rc.Hostname, rc.SMTPAddress, target, rc.TLSDir, rc.Hostname)
		b.WriteString("  Change a domain's MX only for a dedicated test domain, or under your own approved cutover\n  plan with the current MX and DNS recorded for rollback.\n")
	} else {
		b.WriteString("  No MX record yet: KyPost receives nothing until its receiver is deployed. Point no MX at\n  this host before then.\n")
	}
	if len(r.Pending) > 0 {
		b.WriteString("KyPost reports these still pending:\n")
		for _, p := range r.Pending {
			fmt.Fprintf(&b, "  - %s\n", p)
		}
	}
	return b.String(), nil
}

// directoryPassStep (kyidentity.directory.<app>) adds KyIdentity's signed suite directory to
// a client whose app verifies it with its own OIDC client secret (KyPost): a later pass of the
// setup that registered the client, with suiteWebhook signed by that client's secret file in
// KyIdentity's setup scope. No secret is created or copied. It runs after the edges, as
// scimStep does: its first delivery goes through the app's route.
type directoryPassStep struct {
	app    string
	file   string // the pass's bundle in KyIdentity's container
	bundle []byte
	place  func(ctx context.Context, args []string, input []byte) error // KyIdentity's shell
	exec   identityExec                                                 // KyIdentity's apply-setup
}

func (s directoryPassStep) ID() string { return "kyidentity.directory." + s.app }

func (s directoryPassStep) InputHash() string {
	sum := sha256.Sum256(append([]byte(s.file+"\x00"), s.bundle...))
	return hex.EncodeToString(sum[:])
}

func (s directoryPassStep) Inspect(ctx context.Context) (bool, error) {
	return s.Verify(ctx) == nil, nil
}
func (s directoryPassStep) Apply(ctx context.Context) error  { return s.run(ctx, false) }
func (s directoryPassStep) Verify(ctx context.Context) error { return s.run(ctx, true) }

func (s directoryPassStep) run(ctx context.Context, check bool) error {
	if err := identityCapabilities(ctx, s.exec, false); err != nil {
		return err
	}
	mode := "write"
	if check {
		mode = "check"
	}
	if s.place(ctx, []string{mode, s.file}, s.bundle) != nil {
		return fmt.Errorf("KyIdentity's directory setup pass %s for %s is missing, unsafe or differs from this plan's", s.file, s.app)
	}
	_, err := identitySetup(ctx, s.exec, s.file, check)
	return err
}

// directoryPass is the bundle of the pass that adds a signed suite directory at url to client
// id of src's setup (KyIdentity's first, or an amendment of parent), signed with the client's
// own secret file, as KyPost verifies it.
func directoryPass(src stack.Integration, parent, id, url string) []byte {
	n, scope := src.Identity, path.Dir(src.Bundle)
	secret := path.Join(scope, "client-"+id+".secret")
	type webhook struct {
		URL               string `json:"url"`
		SigningSecretFile string `json:"signingSecretFile"`
	}
	type client struct {
		stack.IdentityClient
		SecretFile string `json:"secretFile"`
		// Shallower than the embedded client's suiteWebhook, so it is the one encoded.
		SuiteWebhook webhook `json:"suiteWebhook"`
	}
	c := client{n.Clients[slices.IndexFunc(n.Clients, func(c stack.IdentityClient) bool { return c.ID == id })], secret, webhook{url, secret}}
	return setupPass(src, parent, c)
}

// postNativeHandover is printed instead of postHandover after an apply that installs KyPost
// with native mail; %[4]s is its container, %[5]s its Docker target, %[6]s its domains.
const postNativeHandover = `
KyPost at https://%[1]s/: native mail for %[6]s, chosen at install. KyPost stores each
  user's mail itself; KyIdentity's directory (kyidentity.directory.kypost) creates the mailbox
  of each person assigned to KyPost whose one primary email is on a verified domain. Sign in
  through KyIdentity as %[2]s for administration only: an administrator (kypost.admin) gets no
  mailbox. Mail is for %[3]s, the owner's everyday identity: give it, and anyone else with
  KyIdentity role kypost.user, exactly one primary email on one of these domains (plain ASCII).
  KyPost's own limits (its docs/guides/NATIVE_MAIL.md): the Maddy receiver is a controlled
  qualification profile for a dedicated test domain; public production MX stays gated in
  KyPost (tombstone pruning, capacity recovery, hard volume quotas, power-loss and packaging/
  licensing checks). Use a dedicated test domain first, keep your current MX and DNS recorded
  for rollback, and change production MX only under your own approved plan.
  On its first start KyPost downloads its Ollama model (about 2.9 GB) and retries until it has
  it; until then a lighter built-in sorter labels mail, and KyPost reports healthy.
  Outstanding human steps, on %[5]s for the commands:
  1. Its local "admin" break-glass account: separate from KyIdentity's %[2]s, for when
     KyIdentity is down and to release a restore hold. Read its one-time password:
       docker exec --user kypost %[4]s cat /kypost/config/first-run-password.txt
     Sign in with it once at https://%[1]s/ with KyPost's own password sign-in, not KyIdentity,
     and change it. Only after that, delete the file:
       docker exec --user kypost %[4]s rm /kypost/config/first-run-password.txt
  2. Publish each domain's TXT record below exactly, then, as %[2]s, Admin → Server → Mail
     domain → Verify. Keep it published for as long as the domain is in use.
  3. Outgoing mail goes only through an SMTP relay account you own; KyQuickStart configures
     none. In Admin → Server → Mail domain → 2. Configure outgoing delivery, save its host, port
     (implicit TLS, usually 465), username and password, tick Relay sending domains, then Check
     saved relay. That checks TLS and login only, not delivery: send a test and read its headers.
  4. Confirm %[3]s's mailbox appears in Admin → Server → Mail addresses. A mailbox waits while
     its domain is unverified or the identity has no primary email there.
%[7]s  Then run KyPost's configuration report and resolve every failed check:
       docker exec --user kypost %[4]s kypost-server setup-status
     Its externalChecks remain yours: HTTPS and the forwarded client address, receiver TLS
     and reachability, actual incoming and outgoing mail (headers, Sent), and a backup restored
     on another host. A passing report is not acceptance.
  Backups: KyPost's sealed backups hold mailboxes only up to 64 MiB each and 256 MiB in all;
  KyQuickStart sets no KYPOST_BULK_BACKUP_REPOSITORY, so real mail volumes outgrow them.
  No catalog product can pair with KyRecovery in this release; no backups are deposited in KyRecovery.
  Removing someone in KyIdentity ends their KyPost sessions through the directory; their
  mailbox is kept.
  The KyPost-KyCalendar invitation pairing is not configured by this installer.
`

// postReceiverPending is step 5 before the receiver is in the plan.
const postReceiverPending = `  5. Receiving is off: no mail port is open. Once a domain is verified and a mailbox exists,
     create the receiving spool, once (never again; it refuses an existing one):
       docker exec --user kypost -e KYPOST_NATIVE_RECEIVING=true %[1]s kypost-server receiving init
     Install the exact Maddy 0.9.5 binary KyPost pins and a certificate for the receiving
     hostname (docs/guided-setup.md, "KyPost native mail"), add mail.receiver to stack.yaml
     (or rerun plan) and apply again: that publishes TCP 25 and starts Maddy and Rspamd.
`

// postReceiverLive is step 5 with the receiver.
const postReceiverLive = `  5. Receiving: Maddy answers on %[2]s:25 for %[3]s, with Rspamd refusing scores of 15
     and more. Publish the MX and A records below, open TCP 25 to %[2]s in your firewall,
     with no TCP proxy in front (Rspamd's SPF checks need the sender's address), then from an
     outside network check STARTTLS and the certificate on port 25, send ordinary and PGP mail
     with an attachment, and confirm an unknown address is refused. Receiver state:
       docker exec %[1]s supervisorctl -c /etc/supervisord.conf status receiver
     After renewing the certificate in %[4]s (copy in, then mv), or adding a domain, restart it:
       docker exec %[1]s supervisorctl -c /etc/supervisord.conf restart receiver
`

// postNativeText is the native handover for app's plan mail block m.
func postNativeText(host, admin, everyday, target string, m *stack.Mail) string {
	step := fmt.Sprintf(postReceiverPending, postContainer)
	if r := m.Receiver; r != nil {
		step = fmt.Sprintf(postReceiverLive, postContainer, r.SMTPAddress, r.Hostname, r.TLSDir)
	}
	return fmt.Sprintf(postNativeHandover, host, admin, everyday, postContainer, target, strings.Join(m.Domains, ", "), step)
}

// askMailbox asks for app's opt-in native mail on a Docker target: its domains and, once the
// receiving spool exists, the receiver. Nil keeps webmail for existing IMAP accounts.
func askMailbox(p *prompts, out io.Writer, app, target string) (*stack.Mail, error) {
	k := "app." + app + ".mail."
	fmt.Fprintf(out, "%s is webmail for existing IMAP accounts, or native mail: mailboxes on your own domains, created from KyIdentity's directory. This is fixed at install: an installed %s is never converted.\n", app, app)
	mode, err := p.ask("app."+app+".mail", "Mail mode: imap/native", "imap", oneOf("imap", "native"))
	if err != nil || mode == "imap" {
		return nil, err
	}
	raw, err := p.ask(k+"domains", "Mail domains, comma-separated (use a dedicated test domain first)", "", func(s string) bool {
		return (&stack.Mail{Domains: splitDomains(s)}).Valid()
	})
	if err != nil {
		return nil, err
	}
	m := &stack.Mail{Domains: splitDomains(raw)}
	fmt.Fprintf(out, "Native mail needs %s's setup through KyIdentity: if stack.yaml has no integrations entry for %s with identity_client, add it before apply (docs/guided-setup.md, \"KyPost native mail\"); review names it.\n", app, app)
	fmt.Fprintf(out, "The receiver is KyPost's bundled Maddy with Rspamd on public TCP port 25 of %s. It needs the receiving spool, which you create once after a domain is verified and a mailbox exists: answer no on the first install, then rerun plan.\n", target)
	if rcv, err := p.ask(k+"receiver", "Deploy the receiver now? yes/no", "no", oneOf("yes", "no")); err != nil || rcv == "no" {
		return m, err
	}
	r := &stack.MailReceiver{}
	if r.Hostname, err = p.ask(k+"hostname", "Receiving hostname: the domains' MX target, named by its certificate", "", stack.MailDomain); err != nil {
		return nil, err
	}
	if r.SMTPAddress, err = p.ask(k+"smtp_address", "IPv4 address of "+target+" to publish TCP 25 on (never a wildcard)", "", stack.MediaAddress); err != nil {
		return nil, err
	}
	if r.MaddyBinary, err = p.ask(k+"maddy_binary", "Path on "+target+" of the Maddy 0.9.5 binary KyPost pins", "/opt/kypost-receiving/maddy", stack.ValidTargetPath); err != nil {
		return nil, err
	}
	if r.TLSDir, err = p.ask(k+"tls_dir", "Directory on "+target+" holding fullchain.pem and privkey.pem (owned by uid 1000, 0600)", "", stack.ValidTargetPath); err != nil {
		return nil, err
	}
	confirm, err := p.ask(k+"publish_port_25", "Publish TCP port 25 on "+r.SMTPAddress+" to the internet? yes/no (no leaves the receiver out)", "no", oneOf("yes", "no"))
	if err != nil || confirm == "no" {
		return m, err
	}
	r.PublishPort25 = true
	m.Receiver = r
	return m, nil
}

// splitDomains reads a comma-separated domain list.
func splitDomains(s string) []string {
	var out []string
	for _, d := range strings.Split(s, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// mailAnswers records app's mail block as guided resume answers.
func mailAnswers(a map[string]string, app stack.App) {
	m := app.Mail
	if m == nil {
		return
	}
	k := "app." + app.Name + ".mail."
	a["app."+app.Name+".mail"], a[k+"domains"], a[k+"receiver"] = "native", strings.Join(m.Domains, ","), "no"
	if r := m.Receiver; r != nil {
		a[k+"receiver"], a[k+"hostname"], a[k+"smtp_address"], a[k+"maddy_binary"], a[k+"tls_dir"], a[k+"publish_port_25"] = "yes", r.Hostname, r.SMTPAddress, r.MaddyBinary, r.TLSDir, "yes"
	}
}
