package stack

import (
	"strings"
	"testing"
)

func TestMailDomain(t *testing.T) {
	for _, d := range []string{"example.com", "mail-test.example.co.uk", "xn--bcher-kva.example", "a1.b2"} {
		if !MailDomain(d) {
			t.Errorf("%s refused", d)
		}
	}
	for _, d := range []string{"", "localhost", "Example.com", "example.com.", ".example.com", "-a.example.com", "a-.example.com", "a..com", "192.0.2.1", "exa mple.com",
		"exämple.com", "a_b.example.com", "example.com\n", strings.Repeat("a", 64) + ".com", strings.Repeat("abcdefghi.", 24) + "com"} {
		if MailDomain(d) {
			t.Errorf("%q accepted", d)
		}
	}
}

func TestParseMail(t *testing.T) {
	const mail = "    mail:\n      domains: [mail-test.example.com]\n      receiver:\n        hostname: mx.mail-test.example.com\n        smtp_address: 192.0.2.5\n" +
		"        maddy_binary: /opt/kypost-receiving/maddy\n        tls_dir: /srv/tls\n        publish_port_25: true\n"
	plan := strings.Replace(good, "  - name: hello\n    target: nas\n", "  - name: kypost\n    target: nas\n"+mail, 1)
	s, err := Parse([]byte(plan))
	if err != nil {
		t.Fatal(err)
	}
	if m := s.Apps[0].Mail; m == nil || m.Receiver == nil || !m.Receiver.PublishPort25 || m.Domains[0] != "mail-test.example.com" {
		t.Fatalf("mail = %+v", s.Apps[0].Mail)
	}
	for name, c := range map[string]struct{ old, new, want string }{
		"no domains":  {"domains: [mail-test.example.com]", "domains: []", "mail.domains needs 1 to 32 domains"},
		"duplicate":   {"domains: [mail-test.example.com]", "domains: [mail-test.example.com, mail-test.example.com]", "must be a unique"},
		"bad domain":  {"domains: [mail-test.example.com]", "domains: [\"mail;rm.example.com\"]", "must be a unique lowercase ASCII domain"},
		"unconfirmed": {"publish_port_25: true", "publish_port_25: false", "set publish_port_25: true"},
		"no confirm":  {"        publish_port_25: true\n", "", "set publish_port_25: true"},
		"wildcard":    {"smtp_address: 192.0.2.5", "smtp_address: 0.0.0.0", "smtp_address"},
		"loopback":    {"smtp_address: 192.0.2.5", "smtp_address: 127.0.0.1", "smtp_address"},
		"ipv6":        {"smtp_address: 192.0.2.5", "smtp_address: \"2001:db8::1\"", "smtp_address"},
		"hostname":    {"hostname: mx.mail-test.example.com", "hostname: MX", "mail.receiver.hostname"},
		"binary path": {"maddy_binary: /opt/kypost-receiving/maddy", "maddy_binary: /opt/../maddy", "maddy_binary"},
		"quoted path": {"tls_dir: /srv/tls", "tls_dir: \"/srv/t{ls}\"", "tls_dir"},
		"relative":    {"tls_dir: /srv/tls", "tls_dir: srv/tls", "tls_dir"},
		"unknown key": {"tls_dir: /srv/tls", "tls_dir: /srv/tls\n        port: 2525", "field port not found"},
		"40 domains":  {"domains: [mail-test.example.com]", "domains: [" + strings.Repeat("a.example.com, ", 39) + "a.example.com]", "1 to 32"},
	} {
		in := strings.Replace(plan, c.old, c.new, 1)
		if in == plan {
			t.Fatalf("%s: case did not change the input", name)
		}
		if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
