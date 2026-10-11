package immich

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
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
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type nativeImmichProducerPlan struct {
	Cross                  *nativeCrossForgejo
	Listen, BaseURL, Token string
	NewSubject             string
	Certificate, Key       []byte
}

func newNativeImmichProducerPlan(t *testing.T, ctx context.Context, owner, image string) *nativeImmichProducerPlan {
	t.Helper()
	ip := net.ParseIP("127.0.0.1")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("owned producer loopback unavailable")
	}
	address := listener.Addr().String()
	listener.Close()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{ip}, DNSNames: []string{owner + "-connector"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	callback := nativeImmichProducerRelay(t, ctx, owner, image, address)
	return &nativeImmichProducerPlan{Listen: address, BaseURL: callback + "/scim/photos", Token: strings.Repeat("cd", 32), Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), Key: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})}
}

func startNativeImmichProducerBridge(t *testing.T, ctx context.Context, plan *nativeImmichProducerPlan, owner, image, token string, ca []byte, origin string, accounts ...Binding) func() {
	t.Helper()
	bindings := []map[string]any{}
	for _, b := range accounts {
		bindings = append(bindings, map[string]any{"setup_id": "271333e2-c221-4601-b249-563b3d828530", "app": "photos", "kind": "immich", "origin": origin, "issuer": b.Issuer, "subject": b.Subject, "native_id": 0, "native_uuid": b.ID, "client_id": b.ClientID})
	}
	inputData := map[string]any{"Container": owner + "-server", "Owner": owner, "Image": image, "Token": token, "CA": ca, "Bindings": bindings, "Listen": plan.Listen, "Certificate": plan.Certificate, "Key": plan.Key, "SCIMToken": plan.Token, "NewSubject": plan.NewSubject}
	if plan.Cross != nil {
		crossBindings := []map[string]any{}
		for _, b := range plan.Cross.Bindings {
			crossBindings = append(crossBindings, map[string]any{"setup_id": "271333e2-c221-4601-b249-563b3d828530", "app": "git", "kind": "forgejo", "origin": plan.Cross.API.URL, "issuer": accounts[0].Issuer, "subject": b.Subject, "native_id": b.ID, "source_id": b.SourceID, "username": b.Username})
		}
		inputData["Cross"] = map[string]any{"Container": plan.Cross.Container, "Image": plan.Cross.Image, "Token": plan.Cross.Token, "SCIMToken": plan.Cross.SCIMToken, "Bindings": crossBindings}
		inputData["CA"] = append(append([]byte{}, ca...), plan.Cross.certificate()...)
	}
	input, err := json.Marshal(inputData)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "immich-producer.test")
	compile := exec.CommandContext(ctx, "go", "test", "-race", "-p", "1", "-c", "-o", binary, "../../offboard")
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("Immich producer bridge compile failed: %s", output)
	}
	command := exec.CommandContext(ctx, binary, "-test.run=^TestNativeImmichProducerBridge$", "-test.v")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if command.Start() != nil {
		t.Fatal("Immich producer helper startup failed")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_, _ = io.WriteString(stdin, "{\"Stop\":true}\n")
			stdin.Close()
			select {
			case err := <-done:
				t.Log(strings.TrimSpace(output.String()))
				if err != nil {
					t.Error("Immich producer helper failed")
				}
			case <-time.After(10 * time.Second):
				command.Process.Kill()
				<-done
				t.Error("Immich producer helper did not join")
			}
		})
	}
	t.Cleanup(stop)
	if _, err := stdin.Write(append(input, '\n')); err != nil {
		stop()
		t.Fatal("Immich producer input failed")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(plan.Certificate) {
		t.Fatal("Immich producer public trust invalid")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	observer := &http.Client{Transport: transport, Timeout: time.Second}
	defer observer.CloseIdleConnections()
	for deadline := time.Now().Add(10 * time.Second); ; {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+plan.Listen+"/scim/photos/Users?filter=externalId%20eq%20%22"+accounts[0].Subject+"%22&startIndex=1&count=2", nil)
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
			t.Fatal("Immich producer native TLS read not ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return stop
}

func (f *nativeIdentityProvider) waitDelivery(t *testing.T, client *http.Client, actor string, active bool, after int) int {
	return f.waitConnectorDelivery(t, client, f.connectorID, actor, active, after)
}

func (f *nativeIdentityProvider) waitConnectorDelivery(t *testing.T, client *http.Client, connector, actor string, active bool, after int) int {
	t.Helper()
	if connector == "" {
		t.Fatal("Immich producer connector receipt missing")
	}
	wait := 65 * time.Second
	if after == 0 {
		wait = 130 * time.Second
	}
	for deadline := time.Now().Add(wait); ; {
		var report struct {
			Users []struct {
				UserID                                   string
				Desired, Recorded, Acknowledged, Blocked bool
				Revision                                 int
				LastEvent                                *struct {
					Status, LastError string
					Attempts          int
					NextAttempt       *time.Time
				}
			}
		}
		f.request(t, client, http.MethodGet, "/api/admin/systems/"+connector+"/provisioning", nil, &report)
		for _, row := range report.Users {
			if row.UserID != f.subjects["owner"] && row.UserID != f.subjects["daily"] && (f.producer == nil || f.producer.NewSubject == "" || row.UserID != f.producer.NewSubject) {
				t.Fatal("Immich producer queued unassigned identity")
			}
			if row.UserID == f.subjects[actor] && row.Revision > after && row.Desired == active && row.Recorded == active && row.Acknowledged && !row.Blocked && row.LastEvent != nil && row.LastEvent.Status == "delivered" {
				return row.Revision
			}
		}
		if time.Now().After(deadline) || f.ctx.Err() != nil {
			for _, row := range report.Users {
				eventStatus, attempts := "none", 0
				if row.LastEvent != nil {
					eventStatus, attempts = row.LastEvent.Status, row.LastEvent.Attempts
				}
				t.Logf("producer state: actor=%t desired=%t recorded=%t acknowledged=%t blocked=%t revision=%d event=%s attempts=%d", row.UserID == f.subjects[actor], row.Desired, row.Recorded, row.Acknowledged, row.Blocked, row.Revision, eventStatus, attempts)
				if row.LastEvent != nil {
					for _, category := range []string{"x509", "connection refused", "timeout", "400", "403", "404", "409", "503", "uncertain", "malformed", "unsupported", "TLS"} {
						if strings.Contains(row.LastEvent.LastError, category) {
							t.Log("producer failure category:", category)
						}
					}
				}
			}
			t.Fatalf("Immich producer did not acknowledge %s lifecycle delivery", actor)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (f *nativeIdentityProvider) mutate(t *testing.T, client *http.Client, method, path string, body any) {
	t.Helper()
	var grant struct{ Kind, StepUpToken string }
	f.request(t, client, http.MethodPost, "/api/auth/step-up", map[string]string{"password": "disposable-identity-owner-password", "operation": method + " " + path}, &grant)
	if grant.Kind != "grant" || grant.StepUpToken == "" {
		t.Fatal("Immich producer operation-bound grant missing")
	}
	var result struct{ Success bool }
	f.requestHeaders(t, client, method, path, body, map[string]string{"X-KyIdentity-StepUp": grant.StepUpToken}, &result)
	if !result.Success {
		t.Fatal("Immich producer native mutation refused")
	}
}

func (f *nativeIdentityProvider) administratorRolePath(t *testing.T, client *http.Client, actor string) string {
	t.Helper()
	var report struct{ Roles []struct{ ID, Name string } }
	f.request(t, client, http.MethodGet, "/api/admin/app-registry/"+f.appID+"/roles", nil, &report)
	roleID := ""
	for _, role := range report.Roles {
		if role.Name == "admin" {
			if roleID != "" || role.ID == "" {
				t.Fatal("Immich producer role scope conflicts")
			}
			roleID = role.ID
		}
	}
	if roleID == "" || f.subjects[actor] == "" {
		t.Fatal("Immich producer role receipt missing")
	}
	return "/api/admin/app-registry/" + f.appID + "/roles/" + roleID + "/assignments/users/" + f.subjects[actor]
}

func nativeImmichProducerRelay(t *testing.T, ctx context.Context, owner, image, destination string) string {
	t.Helper()
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(image) {
		t.Fatal("producer relay requires immutable SSH fixture image")
	}
	keys := t.TempDir()
	if os.Chmod(keys, 0700) != nil {
		t.Fatal("producer relay private directory failed")
	}
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	hostBlock, err := ssh.MarshalPrivateKey(hostKey, "")
	if err != nil {
		t.Fatal(err)
	}
	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"host_ed25519": pem.EncodeToMemory(hostBlock), "authorized_keys": ssh.MarshalAuthorizedKey(clientSigner.PublicKey())} {
		if os.WriteFile(filepath.Join(keys, name), data, 0600) != nil {
			t.Fatal("producer relay private keys failed")
		}
	}
	name := owner + "-connector"
	command := exec.CommandContext(ctx, "docker", "run", "-d", "--name", name, "--network", owner, "--label", "kyq.fixture=immich", "--label", "kyq.fixture-owner="+owner, "--memory", "64m", "--memory-swap", "64m", "--cpus", "1", "-p", "127.0.0.1::22", "-v", keys+":/keys:ro", image, "-o", "GatewayPorts=clientspecified", "-o", "AllowTcpForwarding=remote")
	if command.Run() != nil {
		t.Fatal("owned producer SSH relay failed")
	}
	t.Cleanup(func() {
		if exec.Command("docker", "rm", "-fv", name).Run() != nil {
			t.Error("owned producer relay cleanup failed")
		}
	})
	ports, err := exec.CommandContext(ctx, "docker", "port", name, "22").Output()
	endpoint := strings.TrimSpace(string(ports))
	host, _, splitErr := net.SplitHostPort(endpoint)
	if err != nil || splitErr != nil || host != "127.0.0.1" {
		t.Fatal("producer relay SSH port not loopback")
	}
	config := &ssh.ClientConfig{User: "root", Auth: []ssh.AuthMethod{ssh.PublicKeys(clientSigner)}, HostKeyCallback: ssh.FixedHostKey(hostSigner.PublicKey()), Timeout: time.Second}
	var client *ssh.Client
	for deadline := time.Now().Add(10 * time.Second); ; {
		client, err = ssh.Dial("tcp", endpoint, config)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned producer SSH relay not ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	forwarded, err := client.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		client.Close()
		t.Fatal("owned producer remote forwarding refused")
	}
	_, port, err := net.SplitHostPort(forwarded.Addr().String())
	if err != nil {
		forwarded.Close()
		client.Close()
		t.Fatal(err)
	}
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	var copies sync.WaitGroup
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			incoming, err := forwarded.Accept()
			if err != nil {
				return
			}
			outgoing, err := net.DialTimeout("tcp", destination, time.Second)
			if err != nil {
				incoming.Close()
				continue
			}
			mu.Lock()
			connections[incoming] = true
			connections[outgoing] = true
			mu.Unlock()
			copies.Add(1)
			go func() {
				defer copies.Done()
				defer func() {
					incoming.Close()
					outgoing.Close()
					mu.Lock()
					delete(connections, incoming)
					delete(connections, outgoing)
					mu.Unlock()
				}()
				joined := make(chan struct{})
				go func() { io.Copy(outgoing, incoming); outgoing.Close(); close(joined) }()
				io.Copy(incoming, outgoing)
				incoming.Close()
				<-joined
			}()
		}
	}()
	t.Cleanup(func() {
		forwarded.Close()
		client.Close()
		<-accepted
		mu.Lock()
		for connection := range connections {
			connection.Close()
		}
		mu.Unlock()
		copies.Wait()
	})
	return "https://" + net.JoinHostPort(name, port)
}
