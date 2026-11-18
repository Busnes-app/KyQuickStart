//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/kube"
)

// browserEdgeCertificate keeps normal fixture behavior unless the real browser case is selected.
// Browsers need an actual issuing CA, rather than peer trust in a self-signed server leaf.
func browserEdgeCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	if os.Getenv("KYQ_TEST_NOTES_BROWSER") != "1" {
		return testCertificate(t)
	}
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(11), Subject: pkix.Name{CommonName: "owned Notes acceptance CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(12), Subject: pkix.Name{CommonName: "*.example.test"}, DNSNames: []string{"*.example.test"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...)
	return chain, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// seedNotesBrowser uses only the published old client's UI and returns the candidate verification.
// Its private browser profile retains client keys; cookies are cleared for fresh native sign-in.
func seedNotesBrowser(t *testing.T, ctx context.Context, c *kube.Client, certPEM []byte, nodeIP string) func() {
	t.Helper()
	if os.Getenv("KYQ_TEST_NOTES_BROWSER") != "1" {
		return nil
	}
	work := t.TempDir()
	profile := filepath.Join(work, "profile")
	if err := os.Mkdir(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	var caPEM []byte
	for rest := certPEM; len(rest) > 0; {
		block, next := pem.Decode(rest)
		rest = next
		if block == nil {
			t.Fatal("invalid browser fixture chain")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		if cert.IsCA {
			caPEM = pem.EncodeToMemory(block)
		}
	}
	if len(caPEM) == 0 {
		t.Fatal("browser fixture has no issuing CA")
	}
	caFile := filepath.Join(work, "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-N", "-d", "sql:" + profile, "--empty-password"}, {"-A", "-d", "sql:" + profile, "-n", "owned Notes acceptance CA", "-t", "C,,", "-i", caFile}} {
		if out, err := exec.CommandContext(ctx, "certutil", args...).CombinedOutput(); err != nil {
			t.Fatalf("private browser CA: %v %s", err, out)
		}
	}
	ns, pod, container, err := c.AppPod(ctx, "kyidentity")
	if err != nil {
		t.Fatal(err)
	}
	password, err := c.Exec(ctx, ns, pod, container, []string{"cat", "/data/installer/" + identityFixturePreparation().SetupID + "/everyday.password"})
	if err != nil || len(password) == 0 {
		t.Fatal("browser everyday credential unavailable")
	}
	defer clear(password)
	config := map[string]string{"profile": profile, "expected": filepath.Join(work, "expected.json"), "notes": "https://" + catalogNotesURLHost, "identity": "https://" + catalogIdentityURLHost, "username": "prepared-user", "password": strings.TrimSpace(string(password)), "nodeIP": nodeIP, "module": os.Getenv("KYQ_TEST_BROWSER_MODULE"), "browser": os.Getenv("KYQ_TEST_BROWSER_EXECUTABLE")}
	run := func(phase string) {
		t.Helper()
		config["phase"] = phase
		input, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		defer clear(input)
		bounded, cancel := context.WithTimeout(ctx, 6*time.Minute)
		defer cancel()
		args := []string{"--max-old-space-size=256", "notes_browser.mjs"}
		command := "node"
		if wrapper := os.Getenv("KYQ_TEST_BROWSER_WRAPPER"); wrapper != "" {
			command = wrapper
			args = append([]string{"node"}, args...)
		}
		cmd := exec.CommandContext(bounded, command, args...)
		cmd.Stdin = bytes.NewReader(input)
		cmd.Env = append(os.Environ(), "DEBUG=", "PUSH_RELAY_URL=", "APNS_RELAY_URL=")
		out, err := cmd.CombinedOutput()
		t.Logf("Notes native browser %s: %s", phase, out)
		if err != nil {
			t.Fatalf("Notes native browser %s failed: %v", phase, err)
		}
	}
	run("seed")
	return func() { run("verify") }
}

func TestNotesBrowserCertificate(t *testing.T) {
	t.Setenv("KYQ_TEST_NOTES_BROWSER", "1")
	chain, key := browserEdgeCertificate(t)
	pair, err := tls.X509KeyPair(chain, key)
	if err != nil || len(pair.Certificate) != 2 {
		t.Fatalf("browser certificate chain: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(pair.Certificate[1])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: catalogNotesHost}); err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "wrong.invalid"}); err == nil {
		t.Fatal("fixture certificate accepted wrong hostname")
	}
}
