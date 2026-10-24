package stack

import (
	"errors"
	"fmt"
	"strings"
)

// Mail is KyPost's opt-in native mail: mailboxes on the operator's own domains instead of
// webmail for existing IMAP accounts. It is chosen at KyPost's first install and never
// changes afterwards (apply refuses converting an installed KyPost either way).
type Mail struct {
	// Domains go to KyPost's apply-setup, which answers each with its TXT proof challenge.
	Domains []string `yaml:"domains"`
	// Receiver is KyPost's bundled Maddy receiver with Rspamd on public TCP 25. Nil publishes
	// no mail port; it is added on a later apply, after the receiving spool exists.
	Receiver *MailReceiver `yaml:"receiver,omitempty"`
}

// MailReceiver is what KyPost's Maddy profile needs on the Docker host. The binary and the
// TLS files are the operator's: KyQuickStart downloads and generates neither.
type MailReceiver struct {
	Hostname string `yaml:"hostname"` // on the certificate, and the domains' MX target
	// SMTPAddress is the IPv4 address of the target that host port 25 publishes on.
	SMTPAddress string `yaml:"smtp_address"`
	MaddyBinary string `yaml:"maddy_binary"` // the exact Maddy 0.9.5 executable KyPost pins
	TLSDir      string `yaml:"tls_dir"`      // holds fullchain.pem and privkey.pem
	// PublishPort25 is the operator's explicit confirmation that TCP 25 on SMTPAddress is
	// published to the internet. A receiver without it is refused.
	PublishPort25 bool `yaml:"publish_port_25"`
}

// MaxMailDomains is KyPost's own limit for one setup bundle.
const MaxMailDomains = 32

// MailDomain accepts what KyPost's setup accepts as a mail domain: lowercase ASCII
// letter-digit-hyphen labels, at least two, whose _kypost-mail TXT name fits in 253 bytes.
// A last label of digits only (an IP address) is refused as well.
func MailDomain(d string) bool {
	if len("_kypost-mail."+d) > 253 || !strings.Contains(d, ".") {
		return false
	}
	labels := strings.Split(d, ".")
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, r := range l {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return strings.Trim(labels[len(labels)-1], "0123456789") != ""
}

// Valid reports whether m is an acceptable mail block.
func (m Mail) Valid() bool { return m.validate() == nil }

func (m Mail) validate() error {
	if len(m.Domains) == 0 || len(m.Domains) > MaxMailDomains {
		return fmt.Errorf("mail.domains needs 1 to %d domains", MaxMailDomains)
	}
	seen := map[string]bool{}
	for _, d := range m.Domains {
		if !MailDomain(d) || seen[d] {
			return fmt.Errorf("mail.domains: %q must be a unique lowercase ASCII domain name such as mail.example.com, without a trailing dot", d)
		}
		seen[d] = true
	}
	r := m.Receiver
	if r == nil {
		return nil
	}
	switch {
	case !MailDomain(r.Hostname):
		return fmt.Errorf("mail.receiver.hostname %q must be a lowercase ASCII hostname, the name on the receiver's certificate", r.Hostname)
	case !MediaAddress(r.SMTPAddress):
		return fmt.Errorf("mail.receiver.smtp_address %q must be one IPv4 address of the target: not a hostname, IPv6, unspecified, loopback, link-local, multicast or broadcast", r.SMTPAddress)
	case !validRoot(r.MaddyBinary):
		return errors.New("mail.receiver.maddy_binary must be an absolute path on the target of letters, digits, '.', '_' and '-'")
	case !validRoot(r.TLSDir):
		return errors.New("mail.receiver.tls_dir must be an absolute path on the target of letters, digits, '.', '_' and '-'")
	case !r.PublishPort25:
		return errors.New("mail.receiver publishes TCP port 25 to the internet: set publish_port_25: true to confirm it, or remove receiver")
	}
	return nil
}
