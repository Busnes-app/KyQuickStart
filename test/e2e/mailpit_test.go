//go:build e2e

package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// mailpitImage is Mailpit v1.31.6's linux/amd64 manifest (index sha256:23fe39e81bde…, the
// v1.31.6 tag), from `docker buildx imagetools inspect docker.io/axllent/mailpit:v1.31.6` on
// 2026-10-20. Stock upstream; `gh attestation verify --repo axllent/mailpit` finds none.
const mailpitImage = "docker.io/axllent/mailpit@sha256:059454962fbda891cfa621c86a251cfd9cfe645cb43e0dcd9351a35e7a8f3155"

// mailDomain is the domain Mailpit's people have addresses in; mailHost is its SMTP host,
// which the cluster's DNS resolves to its kind address.
const (
	mailDomain = "mail.example.test"
	mailHost   = "smtp." + mailDomain
)

// testCert issues a throwaway CA and a server certificate from it for ip, or for dns when ip
// is nil.
func testCert(t *testing.T, cn string, ip net.IP, dns ...string) (caPEM, certPEM, keyPEM []byte) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn + " CA"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ = x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: dns,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip != nil {
		leaf.Subject.CommonName, leaf.IPAddresses = ip.String(), []net.IP{ip}
	} else {
		leaf.Subject.CommonName = dns[0]
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// mailpit is a test-only mail sink on the kind network: SMTP with STARTTLS required (a test-CA
// certificate for mailHost) and SMTP AUTH required from a test credential file. Docker picks
// its address: a fixed --ip needs a network with a user-configured subnet, which CI's kind
// network is not.
type mailpit struct {
	ip, api  string // its kind address, for mailHost; its API on this host
	caPEM    []byte
	password string // the relay credential, also in the plan's password_file
}

func startMailpit(t *testing.T, work string) mailpit {
	t.Helper()
	b := make([]byte, 4)
	rand.Read(b)
	name := "kyq-e2e-mailpit-" + hex.EncodeToString(b)
	dir := filepath.Join(work, "mailpit")
	os.Mkdir(dir, 0o700)
	m := mailpit{password: randomPassword()}
	var cert, key []byte
	m.caPEM, cert, key = testCert(t, "kyquickstart e2e Mailpit", nil, mailHost)
	for f, b := range map[string][]byte{"smtp-auth": []byte("kyidentity:" + m.password + "\n"), "cert.pem": cert, "key.pem": key} {
		os.WriteFile(filepath.Join(dir, f), b, 0o600)
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	docker(t, "run", "-d", "--name", name, "--network", "kind", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--read-only", "--tmpfs", "/tmp",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "64m", "--memory-swap", "64m", "--cpus", "1", "--pids-limit", "64",
		"-p", "127.0.0.1::8025", "-v", dir+":/conf:ro", mailpitImage,
		"--smtp-tls-cert", "/conf/cert.pem", "--smtp-tls-key", "/conf/key.pem", "--smtp-require-starttls",
		"--smtp-auth-file", "/conf/smtp-auth", "--tags-username", "--smtp-disable-rdns")
	m.api = "http://" + docker(t, "port", name, "8025/tcp")
	m.ip = docker(t, "inspect", name, "--format", `{{(index .NetworkSettings.Networks "kind").IPAddress}}`)
	if net.ParseIP(m.ip).To4() == nil {
		t.Fatalf("Mailpit's kind address %q", m.ip)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if r, err := http.Get(m.api + "/api/v1/messages"); err == nil {
			r.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Fatalf("Mailpit: %s", logs)
		}
	}
	return m
}

// relay is the plan's identity.mail for this sink, its password in a 0600 workstation file.
func (m mailpit) relay(t *testing.T, work string) *stack.IdentityMail {
	t.Helper()
	file := filepath.Join(work, "smtp.password")
	if err := os.WriteFile(file, []byte(m.password+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &stack.IdentityMail{Host: mailHost, Port: 1025, Security: "starttls", From: "KyIdentity <identity@" + mailDomain + ">", Username: "kyidentity", PasswordFile: file}
}

// refusesInsecure proves the sink takes no mail in the clear and none without AUTH, dialled
// from this host at its kind address, its certificate checked for mailHost.
func (m mailpit) refusesInsecure(t *testing.T) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(m.caPEM)
	c, err := smtp.Dial(net.JoinHostPort(m.ip, "1025"))
	if err != nil {
		t.Fatalf("Mailpit SMTP: %v", err)
	}
	if err := c.Mail("probe@" + mailDomain); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("Mailpit took MAIL in the clear: %v", err)
	}
	c.Close()
	c, _ = smtp.Dial(net.JoinHostPort(m.ip, "1025"))
	defer c.Close()
	if err := c.StartTLS(&tls.Config{ServerName: mailHost, RootCAs: pool, MinVersion: tls.VersionTLS12}); err != nil {
		t.Fatalf("Mailpit STARTTLS with the test CA: %v", err)
	}
	if err := c.Mail("probe@" + mailDomain); err == nil || !strings.Contains(err.Error(), "Authentication required") {
		t.Fatalf("Mailpit took MAIL without AUTH: %v", err)
	}
}

type mailMessage struct {
	ID       string
	To       []struct{ Address string }
	Subject  string
	Username string
	Tags     []string
	Text     string
}

var resetToken = regexp.MustCompile(`/reset\?token=([A-Za-z0-9_-]+)`)

// resetLink waits for KyIdentity's reset mail to to and returns its token. Mailpit records
// the SMTP AUTH user; it accepts mail only after STARTTLS.
func (m mailpit) resetLink(t *testing.T, to string) string {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(time.Second) {
		var list struct{ Messages []mailMessage }
		if r, err := http.Get(m.api + "/api/v1/messages"); err == nil {
			json.NewDecoder(r.Body).Decode(&list)
			r.Body.Close()
		}
		for _, msg := range list.Messages {
			if !slices.ContainsFunc(msg.To, func(a struct{ Address string }) bool { return a.Address == to }) {
				continue
			}
			r, err := http.Get(m.api + "/api/v1/message/" + msg.ID)
			if err != nil {
				t.Fatal(err)
			}
			var full mailMessage
			json.NewDecoder(r.Body).Decode(&full)
			r.Body.Close()
			tok := resetToken.FindStringSubmatch(full.Text)
			if full.Username != "kyidentity" || tok == nil || !strings.Contains(full.Text, "https://"+catalogIdentityURLHost+"/reset?token=") {
				t.Fatalf("mail to %s: AUTH user %q, subject %q, link found %v", to, full.Username, full.Subject, tok != nil)
			}
			t.Logf("Mailpit: %q to %s, SMTP AUTH user %s, accepted after STARTTLS (required)", full.Subject, to, full.Username)
			return tok[1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no mail to %s in Mailpit", to)
		}
	}
}
