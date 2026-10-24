package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

const (
	postMailNative   = "    mail:\n      domains: [mail-test.example.com]\n"
	postMailReceiver = postMailNative + "      receiver:\n        hostname: mx.mail-test.example.com\n        smtp_address: 192.0.2.1\n" +
		"        maddy_binary: /opt/kypost-receiving/maddy\n        tls_dir: /srv/kypost-receiving/tls\n        publish_port_25: true\n"
	postToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	postTXTOK = `[{"domain":"mail-test.example.com","name":"_kypost-mail.mail-test.example.com","value":"kypost-mail-verify=` + postToken + `","established":false}]`
)

// postMailPlan is postPlan with KyPost's mail block mail ("" for none).
func postMailPlan(t *testing.T, f *fixture, mail string) {
	t.Helper()
	postPlan(t, f, "      clients: []\n")
	p := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(p)
	y := strings.Replace(string(b), "  - name: kypost\n    target: nas\n", "  - name: kypost\n    target: nas\n"+mail, 1)
	if err := os.WriteFile(p, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
}

type postService struct {
	Ports       []string
	Volumes     []string
	Environment map[string]string
	NetworkMode string `yaml:"network_mode"`
	User        string
	ReadOnly    bool     `yaml:"read_only"`
	CapDrop     []string `yaml:"cap_drop"`
}

func postCompose(t *testing.T, a catalog.App) map[string]postService {
	t.Helper()
	var c struct{ Services map[string]postService }
	if err := yaml.Unmarshal(a.Compose, &c); err != nil {
		t.Fatal(err)
	}
	return c.Services
}

// Attack path: port 25 is published only when the plan has native mail with the receiver and
// the operator confirmed it. The IMAP rendering is the one every existing install runs.
func TestKyPostMailRenderings(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	render := func(mail string) catalog.App {
		t.Helper()
		postMailPlan(t, f, mail)
		s, err := load(f.state, nil, f.o)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(s.cat["kypost"].Compose), "{{") {
			t.Fatalf("unreplaced reference:\n%s", s.cat["kypost"].Compose)
		}
		return s.cat["kypost"]
	}
	for mode, mail := range map[string]string{"imap": "", "native": postMailNative} {
		a := render(mail)
		svc := postCompose(t, a)
		e := svc["app"].Environment
		native := map[string]string{"imap": "false", "native": "true"}[mode]
		if len(svc) != 1 || !slices.Equal(svc["app"].Ports, []string{"192.0.2.1:18866:5866"}) || strings.Contains(string(a.Compose), ":25:2525") || strings.Contains(string(a.Compose), "rspamd:") ||
			e["KYPOST_NATIVE_MAIL"] != native || e["KYPOST_NATIVE_RECEIVING"] != "false" || e["KYPOST_NATIVE_RECEIVER"] != "false" || e["KYPOST_RECEIVING_RSPAMD"] != "" ||
			len(a.Binds) != 0 || len(a.Nested) != 0 || !slices.Equal(a.Services, []string{"app"}) {
			t.Errorf("%s: services %+v, binds %v", mode, svc, a.Binds)
		}
	}
	a := render(postMailReceiver)
	svc := postCompose(t, a)
	app, rspamd := svc["app"], svc["rspamd"]
	e := app.Environment
	if !slices.Equal(app.Ports, []string{"192.0.2.1:18866:5866", "192.0.2.1:25:2525"}) || e["KYPOST_NATIVE_MAIL"] != "true" || e["KYPOST_NATIVE_RECEIVING"] != "true" ||
		e["KYPOST_NATIVE_RECEIVER"] != "true" || e["KYPOST_RECEIVING_RSPAMD"] != "true" || e["KYPOST_RECEIVING_HOSTNAME"] != "mx.mail-test.example.com" ||
		!slices.Contains(app.Volumes, "/opt/kypost-receiving/maddy:/opt/kypost/receiving/maddy:ro") || !slices.Contains(app.Volumes, "/srv/kypost-receiving/tls:/opt/kypost/receiving/tls:ro") {
		t.Fatalf("receiver app = %+v", app)
	}
	if rspamd.NetworkMode != "service:app" || rspamd.User != "11333:11333" || !rspamd.ReadOnly || !slices.Equal(rspamd.CapDrop, []string{"ALL"}) || len(rspamd.Ports) != 0 ||
		!slices.Equal(rspamd.Volumes, []string{"./rspamd/rspamd.conf:/etc/rspamd/rspamd.conf:ro"}) || !slices.Equal(a.Services, []string{"app", "rspamd"}) {
		t.Fatalf("rspamd = %+v, services %v", rspamd, a.Services)
	}
	// KyPost v0.4.3's scripts/rspamd/rspamd.conf, byte for byte.
	sum := sha256.Sum256(a.Files["rspamd/rspamd.conf"])
	if hex.EncodeToString(sum[:]) != "d325d6ce8765b678be4dc06755ffad04ee377de66aee6a02d86207663e9ba7bc" || !slices.Equal(a.Nested, []string{"./rspamd/rspamd.conf"}) {
		t.Fatalf("rspamd.conf %x, nested %v", sum, a.Nested)
	}

	// Refused before connecting: no confirmation, a non-mail app, no setup, Kubernetes.
	for name, c := range map[string]struct{ old, new, want string }{
		"unconfirmed": {"publish_port_25: true", "publish_port_25: false", "set publish_port_25: true to confirm it"},
		"wildcard":    {"smtp_address: 192.0.2.1", "smtp_address: 0.0.0.0", "smtp_address \"0.0.0.0\" must be one IPv4 address"},
		"uppercase":   {"domains: [mail-test.example.com]", "domains: [Mail-Test.example.com]", "must be a unique lowercase ASCII domain"},
		"no setup":    {"  - product: kypost\n    target: nas\n    bundle: " + postBundlePath + "\n    identity_client: kypost\n", "", "native mail needs its setup through KyIdentity"},
		"not kypost":  {"  - name: kyidentity\n    target: k1\n", "  - name: kyidentity\n    target: k1\n" + postMailNative, `app "kyidentity" has no native mail`},
	} {
		postMailPlan(t, f, postMailReceiver)
		p := filepath.Join(f.state, "stack.yaml")
		b, _ := os.ReadFile(p)
		y := strings.Replace(string(b), c.old, c.new, 1)
		if y == string(b) {
			t.Fatalf("%s: case did not change the plan", name)
		}
		os.WriteFile(p, []byte(y), 0o600)
		if _, err := load(f.state, nil, f.o); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// KyPost's setup with native mail: its domains in the bundle, which holds no secret and may be
// replaced; the TXT challenges printed after apply; KyIdentity's directory as a later pass of
// the client's setup, signed with the client's own secret file, after the edges.
func TestKyPostNativeSetup(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	f.h.fail = "docker volume inspect" // no earlier KyPost data
	postMailPlan(t, f, postMailNative)
	identity := &fakeIdentity{export: postExport(parentSetup, postSecret)}
	post := &fakePost{files: map[string]string{}, dns: postTXTOK}
	s := postSession(t, f, identity, post)
	steps := stepsByID(t, s)
	if err := steps["kyidentity.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(identity.intent, "suiteWebhook") {
		t.Fatalf("KyPost's directory in KyIdentity's first setup: %s", identity.intent)
	}
	if err := steps["kypost.setup"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	dir := path.Dir(postBundlePath)
	want := `{"version":1,"sso":{"enabled":true,"issuerUrl":"https://identity.example.com","clientId":"kypost","autoProvision":true},"clientSecretFile":"` + dir + `/client.secret","domains":["mail-test.example.com"]}`
	if post.files[postBundlePath] != want || !slices.Equal(post.placed, []string{"write " + dir + "/client.secret", "replace " + postBundlePath}) {
		t.Fatalf("bundle %s, placed %q", post.files[postBundlePath], post.placed)
	}
	for _, line := range []string{
		`TXT  _kypost-mail.mail-test.example.com  "kypost-mail-verify=` + postToken + `"  (not yet verified`,
		"KyQuickStart edits no DNS", "No MX record yet", "Point no MX at\n  this host before then", "KyPost signs no DKIM", "KyPost reports these still pending:\n  - relay",
	} {
		if !strings.Contains(s.postRecords, line) {
			t.Errorf("records lack %q:\n%s", line, s.postRecords)
		}
	}
	// A later domain replaces the secret-free bundle; the secret is only compared.
	post.placed = nil
	setup := steps["kypost.setup"].(clientSetupStep)
	setup.mail = &stack.Mail{Domains: []string{"mail-test.example.com", "second.example.org"}}
	setup.native = postBundle(setup.mail.Domains)
	post.dns = strings.TrimSuffix(postTXTOK, "]") + `,{"domain":"second.example.org","name":"_kypost-mail.second.example.org","value":"kypost-mail-verify=` + postToken + `","established":true}]`
	if err := setup.Apply(ctx); err != nil || !strings.Contains(post.files[postBundlePath], `"domains":["mail-test.example.com","second.example.org"]`) ||
		!slices.Equal(post.placed, []string{"write " + dir + "/client.secret", "replace " + postBundlePath}) {
		t.Fatalf("second domain: %v, placed %q", err, post.placed)
	}
	if setup.InputHash() == steps["kypost.setup"].InputHash() {
		t.Fatal("domains are not in kypost.setup's input")
	}

	all, err := s.deploySteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	last, ok := all[len(all)-1].(directoryPassStep)
	secret := "/data/installer/" + parentSetup + "/client-kypost.secret"
	wantPass := `{"admin":{"username":"owner-admin","email":"admin@example.test","passwordFile":"/data/installer/` + parentSetup + `/admin.password"},` +
		`"clients":[{"id":"kypost","name":"KyPost","redirectUris":["https://post.example.com/api/auth/oidc/callback"],"postLogoutUris":null,"administratorRole":"kypost.admin","everydayRole":"kypost.user",` +
		`"secretFile":"` + secret + `","suiteWebhook":{"url":"https://post.example.com/api/sync/webhook","signingSecretFile":"` + secret + `"}}],`
	if !ok || last.ID() != "kyidentity.directory.kypost" || last.file != "/data/installer/directory-kypost.json" || !strings.HasPrefix(string(last.bundle), wantPass) ||
		!strings.Contains(string(last.bundle), `"setupId":"`+parentSetup+`"`) || strings.Contains(string(last.bundle), postSecret) {
		t.Fatalf("directory step = %T %+v\n%s\nwant prefix\n%s", all[len(all)-1], last, last.bundle, wantPass)
	}
	// The IMAP plan has no directory step and an unchanged bundle.
	postMailPlan(t, f, "")
	if all, err := postSession(t, f, &fakeIdentity{}, &fakePost{files: map[string]string{}}).deploySteps(ctx); err != nil || slices.Contains(ids(all), "kyidentity.directory.kypost") {
		t.Fatalf("IMAP steps = %v, %v", ids(all), err)
	}
}

// The handover names the native steps and KyPost's own production caveats.
func TestKyPostNativeHandover(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	for _, c := range []struct {
		mail string
		want []string
	}{
		{postMailNative, []string{"native mail for mail-test.example.com, chosen at install", "an administrator (kypost.admin) gets no\n  mailbox", "owner, the owner's everyday identity",
			"controlled\n  qualification profile for a dedicated test domain", "change production MX only under your own approved plan", "Check\n     saved relay",
			"Receiving is off: no mail port is open", "docker exec --user kypost -e KYPOST_NATIVE_RECEIVING=true kyq-kypost-app-1 kypost-server receiving init",
			"add mail.receiver to stack.yaml", "docker exec --user kypost kyq-kypost-app-1 kypost-server setup-status", "64 MiB", "KYPOST_BULK_BACKUP_REPOSITORY"}},
		{postMailReceiver, []string{"Maddy answers on 192.0.2.1:25 for mx.mail-test.example.com", "no TCP proxy in front", "supervisorctl -c /etc/supervisord.conf status receiver", "/srv/kypost-receiving/tls"}},
	} {
		postMailPlan(t, f, c.mail)
		s, err := load(f.state, nil, f.o)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range c.want {
			if !strings.Contains(s.handover, w) {
				t.Errorf("handover lacks %q:\n%s", w, s.handover)
			}
		}
		if strings.Contains(s.handover, "opens no mail port") {
			t.Error("native handover says webmail")
		}
	}
}

// Every reported record must be a _kypost-mail TXT challenge, and every planned domain in it.
func TestPostRecordsRefuses(t *testing.T) {
	m := &stack.Mail{Domains: []string{"mail-test.example.com"}}
	report := func(dns string) []byte { return []byte(`{"dns":` + dns + `,"pending":[]}`) }
	if _, err := postRecords(report(postTXTOK), m, "nas"); err != nil {
		t.Fatal(err)
	}
	for name, dns := range map[string]string{
		"missing domain": `[]`,
		"other name":     strings.Replace(postTXTOK, `"_kypost-mail.mail-test.example.com"`, `"mail-test.example.com"`, 1),
		"other value":    strings.Replace(postTXTOK, "kypost-mail-verify="+postToken, "v=spf1 +all", 1),
		"bad domain":     strings.ReplaceAll(postTXTOK, "mail-test.example.com", "MAIL.example.com"),
	} {
		if _, err := postRecords(report(dns), m, "nas"); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	m.Receiver = &stack.MailReceiver{Hostname: "mx.mail-test.example.com", SMTPAddress: "192.0.2.1", TLSDir: "/srv/tls"}
	out, err := postRecords(report(postTXTOK), m, "nas")
	if err != nil || !strings.Contains(out, "MX   mail-test.example.com  10 mx.mail-test.example.com") ||
		!strings.Contains(out, "A    mx.mail-test.example.com  the public IPv4 address whose TCP 25 reaches 192.0.2.1:25 on nas") || !strings.Contains(out, "add no AAAA") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Attack path: an installed KyPost is never converted between IMAP webmail and native mail,
// nor started native over earlier data, and the receiver waits for the receiving spool.
func TestKyPostMailModeRefusals(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	deployed := func(mode, receiver string) {
		t.Helper()
		dir := filepath.Join(f.h.root, "kypost")
		os.RemoveAll(dir)
		if mode == "" {
			return
		}
		os.MkdirAll(dir, 0o700)
		y := "services:\n  app:\n    environment:\n      KYPOST_NATIVE_MAIL: \"" + mode + "\"\n      KYPOST_NATIVE_RECEIVER: \"" + receiver + "\"\n"
		os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(y), 0o600)
	}
	refuse := func(mail string, post *fakePost) error {
		t.Helper()
		postMailPlan(t, f, mail)
		s := postSession(t, f, &fakeIdentity{}, post)
		return s.refuseMailChange(ctx)
	}
	f.h.fail = "docker volume inspect" // no earlier KyPost data
	for name, c := range map[string]struct {
		mode, receiver, mail string
		spool                bool
		want                 string
	}{
		"imap stays":            {"false", "false", "", false, ""},
		"fresh native":          {"", "", postMailNative, false, ""},
		"imap to native":        {"false", "false", postMailNative, false, "KyPost converts no IMAP account to native mail"},
		"imap to receiver":      {"false", "false", postMailReceiver, true, "KyPost converts no IMAP account"},
		"native to imap":        {"true", "false", "", false, "runs native mail: without it its users lose access"},
		"receiver, no spool":    {"true", "false", postMailReceiver, false, "has no receiving spool (/kypost/state/receiving)"},
		"receiver with spool":   {"true", "false", postMailReceiver, true, ""},
		"receiver running":      {"true", "true", postMailReceiver, false, ""},
		"receiver on first run": {"", "", postMailReceiver, true, "apply native mail without mail.receiver first"},
		"receiver removed":      {"true", "true", postMailNative, false, ""},
		"unreadable":            {"maybe", "false", postMailNative, false, "does not say whether KyPost runs native mail"},
	} {
		deployed(c.mode, c.receiver)
		err := refuse(c.mail, &fakePost{files: map[string]string{}, spool: c.spool})
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Data an uninstall left, of no recorded mode, never starts native.
	deployed("", "")
	f.h.fail = ""
	if err := refuse(postMailNative, &fakePost{files: map[string]string{}}); err == nil || !strings.Contains(err.Error(), "Docker volume kyq-kypost_state") {
		t.Fatalf("earlier data: %v", err)
	}
	if err := refuse("", &fakePost{files: map[string]string{}}); err != nil {
		t.Fatalf("IMAP over earlier data: %v", err)
	}
	// deploySteps refuses before any step.
	deployed("false", "false")
	postMailPlan(t, f, postMailNative)
	if _, err := postSession(t, f, &fakeIdentity{}, &fakePost{files: map[string]string{}}).deploySteps(ctx); err == nil || !strings.Contains(err.Error(), "converts no IMAP account") {
		t.Fatalf("deploySteps: %v", err)
	}
}

// The receiver rendering's deploy writes KyPost's rspamd.conf readable by Rspamd's uid inside
// the private bind directory, and the file is part of the deploy's input.
func TestKyPostReceiverDeployWritesRspamdConf(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	postMailPlan(t, f, postMailReceiver)
	os.MkdirAll(filepath.Join(f.h.root, "kypost"), 0o700)
	os.WriteFile(filepath.Join(f.h.root, "kypost/compose.yaml"), []byte("services:\n  app:\n    environment:\n      KYPOST_NATIVE_MAIL: \"true\"\n"), 0o600)
	s := postSession(t, f, &fakeIdentity{}, &fakePost{files: map[string]string{}, spool: true})
	steps := stepsByID(t, s)
	if err := steps["kypost.deploy"].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(f.h.root, "kypost/rspamd/rspamd.conf")
	st, err := os.Stat(conf)
	b, _ := os.ReadFile(conf)
	dir, _ := os.Stat(filepath.Dir(conf))
	if err != nil || st.Mode().Perm() != 0o644 || dir.Mode().Perm() != 0o700 || string(b) != string(s.cat["kypost"].Files["rspamd/rspamd.conf"]) {
		t.Fatalf("rspamd.conf: %v, mode %v, dir %v", err, st, dir)
	}
	compose, _ := os.ReadFile(filepath.Join(f.h.root, "kypost/compose.yaml"))
	if !strings.Contains(string(compose), `"192.0.2.1:25:2525"`) {
		t.Fatalf("compose.yaml:\n%s", compose)
	}
	native := steps["kypost.deploy"].InputHash()
	files := s.cat["kypost"].Files
	s.cat["kypost"] = func(a catalog.App) catalog.App {
		a.Files = map[string][]byte{"rspamd/rspamd.conf": append([]byte("# changed\n"), files["rspamd/rspamd.conf"]...)}
		return a
	}(s.cat["kypost"])
	if stepsByID(t, s)["kypost.deploy"].InputHash() == native {
		t.Fatal("rspamd.conf is not in the deploy's input")
	}
}

// Guided plan asks the mail mode on KyPost's Docker target; IMAP is the default.
func TestAskMailbox(t *testing.T) {
	for _, c := range []struct {
		answers map[string]string
		want    string
	}{
		{map[string]string{}, "null"},
		{map[string]string{"app.kypost.mail": "native", "app.kypost.mail.domains": "mail-test.example.com, second.example.org"}, `{"Domains":["mail-test.example.com","second.example.org"],"Receiver":null}`},
		{map[string]string{"app.kypost.mail": "native", "app.kypost.mail.domains": "mail-test.example.com", "app.kypost.mail.receiver": "yes", "app.kypost.mail.hostname": "mx.mail-test.example.com",
			"app.kypost.mail.smtp_address": "192.0.2.1", "app.kypost.mail.tls_dir": "/srv/tls", "app.kypost.mail.publish_port_25": "no"}, `{"Domains":["mail-test.example.com"],"Receiver":null}`},
		{map[string]string{"app.kypost.mail": "native", "app.kypost.mail.domains": "mail-test.example.com", "app.kypost.mail.receiver": "yes", "app.kypost.mail.hostname": "mx.mail-test.example.com",
			"app.kypost.mail.smtp_address": "192.0.2.1", "app.kypost.mail.tls_dir": "/srv/tls", "app.kypost.mail.publish_port_25": "yes"},
			`{"Domains":["mail-test.example.com"],"Receiver":{"Hostname":"mx.mail-test.example.com","SMTPAddress":"192.0.2.1","MaddyBinary":"/opt/kypost-receiving/maddy","TLSDir":"/srv/tls","PublishPort25":true}}`},
	} {
		var out strings.Builder
		p := &prompts{ctx: context.Background(), in: bufio.NewReader(strings.NewReader(strings.Repeat("\n", 16))), out: &out, answers: c.answers, draft: filepath.Join(t.TempDir(), "answers.json")}
		m, err := askMailbox(p, &out, "kypost", "nas")
		got, _ := json.Marshal(m)
		if err != nil || string(got) != c.want {
			t.Errorf("answers %v: %s, %v\n%s", c.answers, got, err, out.String())
		}
		if m != nil {
			a := map[string]string{}
			mailAnswers(a, stack.App{Name: "kypost", Mail: m})
			for k, v := range a {
				if k != "app.kypost.mail.receiver" && k != "app.kypost.mail.publish_port_25" && c.answers[k] != v && strings.ReplaceAll(c.answers[k], " ", "") != v {
					t.Errorf("resume answer %s = %q, asked %q", k, v, c.answers[k])
				}
			}
		}
	}
}
