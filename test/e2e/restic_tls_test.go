//go:build e2e

package e2e

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// Name only the owned container so a cancelled restic retry can be removed exactly.
// Host networking lets this fixture reach its host TLS server; the production
// environment, credentials, arguments and pinned image otherwise stay intact.
type namedResticRunner struct{ name string }

func (r namedResticRunner) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	cmd = strings.Replace(cmd, "docker run --rm ", "docker run --rm --name "+remote.Quote(r.name)+" ", 1)
	cmd = strings.Replace(cmd, "--network bridge", "--network host", 1)
	return (remote.Local{}).Run(ctx, cmd, stdin)
}

type tlsAttemptLog struct{ attempts chan struct{} }

func (l tlsAttemptLog) Write(p []byte) (int, error) {
	select {
	case l.attempts <- struct{}{}:
	default:
	}
	return len(p), nil
}

func TestResticTLSKeyFloor(t *testing.T) {
	gateway := strings.Fields(docker(t, "network", "inspect", "bridge", "--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}"))
	var ip net.IP
	for _, candidate := range gateway {
		if parsed := net.ParseIP(candidate); parsed.To4() != nil {
			ip = parsed
		}
	}
	if ip == nil {
		t.Fatal("Docker bridge has no IPv4 gateway")
	}
	for _, tc := range []struct {
		where string
		bits  int
		allow bool
	}{
		{"leaf", 1024, false}, {"leaf", 2047, false},
		{"root", 1024, false}, {"root", 2047, false},
		{"intermediate", 1024, false}, {"intermediate", 2047, false},
		{"leaf", 2048, true}, {"leaf", 0, true},
	} {
		t.Run(fmt.Sprintf("%s-%d", tc.where, tc.bits), func(t *testing.T) {
			certificate, ca := resticTLSCertificate(t, ip, tc.where, tc.bits)
			attempts := make(chan struct{}, 1)
			var requests atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusNotFound) // a deliberately absent disposable repository
				select {
				case attempts <- struct{}{}:
				default:
				}
			}))
			server.Listener.Close()
			listener, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
			if err != nil {
				t.Fatal(err)
			}
			server.Listener = listener
			server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
			server.Config.ErrorLog = log.New(tlsAttemptLog{attempts}, "", 0)
			server.StartTLS()
			defer server.Close()
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(ca) {
				t.Fatal("fixture CA")
			}
			// Verify each weak fixture is usable with normal trust/hostname checks.
			// This control completes TLS only and sends no HTTP.
			control, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", listener.Addr().String(), &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
			if err != nil {
				t.Fatalf("fixture control handshake: %v", err)
			}
			control.Close()
			if requests.Load() != 0 {
				t.Fatal("control sent HTTP")
			}
			dir := t.TempDir()
			for name, value := range map[string][]byte{"ca.pem": ca, "rest-password": []byte("Disposable-REST-Password"), "repository-password": []byte("Disposable-Repository-Password")} {
				if err := os.WriteFile(filepath.Join(dir, name), value, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			name := "kyq-restic-tls-" + randomPassword()[:12]
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			store := dockerhost.Store{
				App:  dockerhost.App{Name: "fixture", Runner: namedResticRunner{name}},
				Dest: stack.BackupDestination{Repository: "rest:" + server.URL + "/suite", CredentialFile: filepath.Join(dir, "rest-password"), CAFile: filepath.Join(dir, "ca.pem")},
			}
			done := make(chan error, 1)
			go func() {
				_, err := store.Snapshots(ctx, filepath.Join(dir, "repository-password"), "")
				done <- err
			}()
			// Remove the exact owned container before joining the cancelled command;
			// cancelling the local shell alone does not stop a Docker retry process.
			defer func() {
				cancel()
				exec.Command("docker", "rm", "-fv", name).Run()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Error("restic command did not exit after fixture removal")
				}
			}()
			select {
			case <-attempts:
			case <-done:
				// Preserve the completion for cleanup's join.
				done <- nil
				if requests.Load() == 0 {
					t.Fatal("restic exited without a TLS/HTTP observation")
				}
			case <-ctx.Done():
				t.Fatal("no TLS/HTTP observation")
			}
			if got := requests.Load() > 0; got != tc.allow {
				t.Errorf("HTTP reached %s RSA-%d peer: %v, want %v", tc.where, tc.bits, got, tc.allow)
			}
		})
	}
}

func resticTLSCertificate(t *testing.T, ip net.IP, weakAt string, bits int) (tls.Certificate, []byte) {
	t.Helper()
	var issuer *x509.Certificate
	var issuerKey crypto.Signer
	var chain [][]byte
	var ca []byte
	var leafKey crypto.Signer
	for i, name := range []string{"root", "intermediate", "leaf"} {
		var key crypto.Signer
		var err error
		if name == weakAt && bits != 0 {
			key, err = rsa.GenerateKey(rand.Reader, bits)
		} else {
			key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		}
		if err != nil {
			t.Fatal(err)
		}
		cert := &x509.Certificate{SerialNumber: big.NewInt(int64(i + 1)), Subject: pkix.Name{CommonName: "disposable restic " + name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true, IsCA: name != "leaf", KeyUsage: x509.KeyUsageDigitalSignature}
		if cert.IsCA {
			cert.KeyUsage |= x509.KeyUsageCertSign
		} else {
			cert.IPAddresses = []net.IP{ip}
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			leafKey = key
		}
		parent, signer := issuer, issuerKey
		if i == 0 {
			parent, signer = cert, key
		}
		der, err := x509.CreateCertificate(rand.Reader, cert, parent, key.Public(), signer)
		if err != nil {
			t.Fatal(err)
		}
		issuer, err = x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		issuerKey = key
		if i == 0 {
			ca = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		} else {
			chain = append([][]byte{der}, chain...)
		}
	}
	return tls.Certificate{Certificate: chain, PrivateKey: leafKey}, ca
}
