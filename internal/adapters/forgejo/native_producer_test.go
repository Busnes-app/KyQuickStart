package forgejo

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Host networking is used only with an actual identity binary explicitly bound
// to loopback. Its public leaf is installed before startup with verified trust.
type nativeProducerPlan struct {
	Listen, BaseURL, Token string
	NewSubject             string
	GroupID                string
	OrganizationID         int64
	Certificate, Key       []byte
}

func newNativeProducerPlan(t *testing.T) *nativeProducerPlan {
	t.Helper()
	ip := net.ParseIP("127.0.0.1")

	listener, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{ip}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return &nativeProducerPlan{Listen: address, BaseURL: "https://" + address + "/scim/forgejo", Token: strings.Repeat("cd", 32), Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), Key: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})}
}

func startNativeProducerBridge(t *testing.T, ctx context.Context, plan *nativeProducerPlan, container, image, token string, ca []byte, origin, issuer string, accounts ...Binding) func() {
	t.Helper()
	bindings := []map[string]any{}
	for _, b := range accounts {
		bindings = append(bindings, map[string]any{"setup_id": "271333e2-c221-4601-b249-563b3d828530", "app": "forgejo", "kind": "forgejo", "origin": origin, "issuer": issuer, "subject": b.Subject, "native_id": b.ID, "source_id": b.SourceID, "username": b.Username})
	}
	input, err := json.Marshal(map[string]any{"container": container, "image": image, "token": token, "ca": ca, "bindings": bindings, "listen": plan.Listen, "certificate": plan.Certificate, "key": plan.Key, "scim_token": plan.Token, "new_subject": plan.NewSubject, "group_id": plan.GroupID, "organization_id": plan.OrganizationID})
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "producer-bridge.test")
	compile := exec.CommandContext(ctx, "go", "test", "-race", "-c", "-o", binary, "./internal/offboard")
	compile.Dir = "../../.."
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("producer bridge compile failed: %s", output)
	}
	command := exec.CommandContext(ctx, binary, "-test.run", "^TestNativeIdentityProducerBridge$", "-test.v")
	command.Env = append(os.Environ(), "KYQ_TEST_FORGEJO_PRODUCER_JSON="+string(input))
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_, _ = io.WriteString(stdin, "{\"stop\":true}\n")
			stdin.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Log(strings.TrimSpace(output.String()))
					t.Error("native producer bridge failed")
				} else {
					t.Log(strings.TrimSpace(output.String()))
				}
			case <-time.After(10 * time.Second):
				_ = command.Process.Kill()
				<-done
				t.Error("native producer bridge did not join")
			}
		})
	}
	t.Cleanup(stop)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(plan.Certificate) {
		t.Fatal("producer public trust invalid")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	observer := &http.Client{Transport: transport, Timeout: time.Second}
	defer observer.CloseIdleConnections()
	for deadline := time.Now().Add(5 * time.Second); ; {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, plan.BaseURL+"/Users?filter=externalId%20eq%20%22"+accounts[0].Subject+"%22&startIndex=1&count=2", nil)
		req.Header.Set("Authorization", "Bearer "+plan.Token)
		response, err := observer.Do(req)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			stop()
			t.Fatal("producer bridge did not pass verified TLS native read")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return stop
}

// Read the actual producer's durable acknowledgment, never infer delivery from
// Forgejo's profile alone. Role changes must advance the recorded revision.
func (f *nativeIdentityFixture) waitDelivery(t *testing.T, ctx context.Context, client *http.Client, issuer, actor string, active bool, after int) int {
	t.Helper()
	if f.connectorID == "" {
		t.Fatal("native producer connector receipt missing")
	}
	for deadline := time.Now().Add(65 * time.Second); ; {
		var report struct {
			Users []struct {
				UserID                                   string `json:"userId"`
				Desired, Recorded, Acknowledged, Blocked bool
				Revision                                 int
				LastEvent                                *struct{ Status string }
			}
		}
		f.request(t, client, issuer, http.MethodGet, "/api/admin/systems/"+f.connectorID+"/provisioning", nil, nil, &report)
		for _, row := range report.Users {
			if row.UserID != f.subjects["owner"] && row.UserID != f.subjects["daily"] && row.UserID != f.subjects["new-daily"] {
				t.Fatal("native producer queued an unassigned fixture identity")
			}
			if row.UserID == f.subjects[actor] && row.Revision > after && row.Desired == active && row.Recorded == active && row.Acknowledged && !row.Blocked && row.LastEvent != nil && row.LastEvent.Status == "delivered" {
				return row.Revision
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			t.Fatalf("native producer did not acknowledge %s lifecycle delivery", actor)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
