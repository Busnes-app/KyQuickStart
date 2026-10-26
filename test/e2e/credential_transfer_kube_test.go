//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"debug/elf"
	"encoding/json"
	"encoding/pem"
	"io"
	"io/fs"
	"log"
	"math/big"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

// The static test executable doubles as a disposable TLS sidecar and a fixture
// inspection tool. No helper is installed in a product or exposed by the installer.
func TestCredentialFixture(t *testing.T) {
	if os.Getenv("KYQ_CREDENTIAL_FIXTURE") != "1" {
		t.Skip("disposable container helper only")
	}
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "tls":
		backend, err := url.Parse(os.Getenv("KYQ_FIXTURE_BACKEND"))
		if err != nil || backend.Scheme != "http" || !strings.HasPrefix(backend.Host, "127.0.0.1:") {
			t.Fatal("invalid fixture backend")
		}
		proxy := httputil.NewSingleHostReverseProxy(backend)
		director := proxy.Director
		proxy.Director = func(r *http.Request) { director(r); r.Header.Set("X-Forwarded-Proto", "https") }
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusBadGateway) }
		server := &http.Server{Addr: ":8443", Handler: proxy, ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
		if err := server.ListenAndServeTLS("/fixture-tls/cert.pem", "/fixture-tls/key.pem"); err != nil {
			t.Fatal("fixture TLS server stopped")
		}
	case "read":
		files := map[string][]byte{}
		for _, name := range []string{"setup.json", "client.secret", "directory.secret"} {
			raw, err := os.ReadFile("/data/installer/" + name)
			if err != nil {
				t.Fatal("cannot inspect owned fixture")
			}
			files[name] = raw
		}
		// Emit one private JSON frame, without Go test's PASS trailer.
		if json.NewEncoder(os.Stdout).Encode(files) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	case "write-client":
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 44))
		if err != nil || len(raw) != 43 {
			t.Fatal("invalid fixture credential input")
		}
		if os.WriteFile("/data/installer/client.secret", raw, 0600) != nil {
			t.Fatal("cannot change owned fixture")
		}
		clear(raw)
	default:
		t.Fatal("unknown fixture helper mode")
	}
}

func testCredentialKubernetesTransfer(t *testing.T, ctx context.Context, cs kubernetes.Interface, connected *kube.Client, cluster, kubeconfig string) {
	identityBinary, notesBinary := os.Getenv("KYQ_TEST_IDENTITY_BINARY"), os.Getenv("KYQ_TEST_NOTES_BINARY")
	if identityBinary == "" || notesBinary == "" {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	build := t.TempDir()
	for name, source := range map[string]string{"kyidentity": identityBinary, "kynotes-server": notesBinary, "fixture-tests": executable} {
		if !filepath.IsAbs(source) {
			t.Fatal("native fixture binaries must be absolute")
		}
		binary, err := elf.Open(source)
		if err != nil {
			t.Fatal("fixture executable is not ELF")
		}
		for _, p := range binary.Progs {
			if p.Type == elf.PT_INTERP {
				binary.Close()
				t.Fatal("kind transfer fixture requires static binaries: run with CGO_ENABLED=0")
			}
		}
		binary.Close()
		in, err := os.Open(source)
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.OpenFile(filepath.Join(build, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
		if err != nil {
			in.Close()
			t.Fatal(err)
		}
		_, copyErr := io.Copy(out, in)
		in.Close()
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatal("copy fixture executable", copyErr, closeErr)
		}
	}
	image := "kyq-transfer-kube-fixture:" + cluster
	dockerfile := "FROM gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a\nCOPY --chmod=0755 kyidentity /usr/local/bin/kyidentity\nCOPY --chmod=0755 kynotes-server /kynotes-server\nCOPY --chmod=0755 fixture-tests /fixture-tests\n"
	if os.WriteFile(filepath.Join(build, "Dockerfile"), []byte(dockerfile), 0600) != nil {
		t.Fatal("write fixture Dockerfile")
	}
	docker(t, "build", "-q", "-t", image, build)
	t.Cleanup(func() { docker(t, "image", "rm", image) })
	kind(t, "load", "docker-image", image, "--name", cluster)
	const namespace = "kyq-transfer-fixture"
	labels := map[string]string{managed.Label: managed.Value, "pod-security.kubernetes.io/enforce": "restricted"}
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: labels}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{}) })
	for _, policy := range []*networkingv1.NetworkPolicy{
		{ObjectMeta: metav1.ObjectMeta{Name: "default-deny"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "fixture-tls"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"kyq-fixture": "transfer"}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kyq-fixture": "transfer"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(8443))}}}}}},
	} {
		policy.Labels = map[string]string{managed.Label: managed.Value}
		if _, err := cs.NetworkingV1().NetworkPolicies(namespace).Create(ctx, policy, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	identityHost, notesHost := "identity."+namespace+".svc", "notes."+namespace+".svc"
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "owned KyQuickStart fixture"}, DNSNames: []string{identityHost, notesHost}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, public, private)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
	if _, err := cs.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "fixture-tls"}, Data: map[string][]byte{"cert.pem": certPEM, "key.pem": keyPEM}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	clear(key)
	clear(keyPEM)
	clear(private)
	if _, err := cs.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "fixture-ca"}, Data: map[string]string{"ca.pem": string(certPEM)}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	security := &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
	issuerURL, notesURL := "https://"+identityHost+":8443", "https://"+notesHost+":8443"
	for _, name := range []string{"identity", "notes"} {
		selector := map[string]string{"kyq-fixture": "transfer", "kyq-product": name}
		if _, err := cs.CoreV1().Services(namespace).Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: corev1.ServiceSpec{Selector: selector, Ports: []corev1.ServicePort{{Name: "tls", Port: 8443, TargetPort: intstr.FromInt32(8443)}}}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		backend, binary, health, init := "127.0.0.1:5867", "/usr/local/bin/kyidentity", "/readyz", "umask 077; mkdir /work/product"
		env := []corev1.EnvVar{{Name: "KYIDENTITY_DATA_DIR", Value: "/data"}, {Name: "KYIDENTITY_LISTEN_HOST", Value: "127.0.0.1"}, {Name: "KYIDENTITY_ISSUER_URL", Value: issuerURL}, {Name: "KYIDENTITY_ALLOW_PRIVATE_CALLBACKS", Value: "true"}, {Name: "PUSH_RELAY_URL"}, {Name: "APNS_RELAY_URL"}}
		var args []string
		if name == "notes" {
			backend, binary, health = "127.0.0.1:8080", "/kynotes-server", "/healthz"
			init = "umask 077; mkdir /work/product /work/product/backups && printf '%s\\n' 'data_dir: /data' 'server:' '  bind: 127.0.0.1:8080' '  behind_proxy: true' '  trusted_proxies: [127.0.0.1/32]' > /work/product/kynotes.yaml"
			env = []corev1.EnvVar{{Name: "KYNOTES_BACKUP_DIR", Value: "/data/backups"}}
			args = []string{"--config", "/data/kynotes.yaml"}
		}
		env = append(env, corev1.EnvVar{Name: "SSL_CERT_FILE", Value: "/fixture-ca/ca.pem"}, corev1.EnvVar{Name: "GOMAXPROCS", Value: "2"}, corev1.EnvVar{Name: "GOMEMLIMIT", Value: "256MiB"}, corev1.EnvVar{Name: "KYQ_CREDENTIAL_FIXTURE", Value: "1"})
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: selector}, Spec: corev1.PodSpec{
			AutomountServiceAccountToken: ptr.To(false), EnableServiceLinks: ptr.To(false), SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(65532)), RunAsGroup: ptr.To(int64(65532)), FSGroup: ptr.To(int64(65532)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			InitContainers: []corev1.Container{{Name: "fixture-data", Image: kube.CredentialGeneratorImage, SecurityContext: security, Command: []string{"sh", "-c", init}, VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/work"}}, Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("32Mi"), corev1.ResourceCPU: resource.MustParse("100m")}}}},
			Containers: []corev1.Container{
				{Name: name, Image: image, ImagePullPolicy: corev1.PullNever, Command: []string{binary}, Args: args, Env: env, SecurityContext: security, VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data", SubPath: "product"}, {Name: "ca", MountPath: "/fixture-ca", ReadOnly: true}}, Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("384Mi"), corev1.ResourceCPU: resource.MustParse("1")}}},
				{Name: "tls", Image: image, ImagePullPolicy: corev1.PullNever, Command: []string{"/fixture-tests", "-test.run=^TestCredentialFixture$", "--", "tls"}, Env: []corev1.EnvVar{{Name: "KYQ_CREDENTIAL_FIXTURE", Value: "1"}, {Name: "KYQ_FIXTURE_BACKEND", Value: "http://" + backend}, {Name: "GOMAXPROCS", Value: "1"}, {Name: "GOMEMLIMIT", Value: "64MiB"}}, SecurityContext: security, VolumeMounts: []corev1.VolumeMount{{Name: "tls", MountPath: "/fixture-tls", ReadOnly: true}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: health, Port: intstr.FromInt32(8443), Scheme: corev1.URISchemeHTTPS}}, PeriodSeconds: 1}, Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi"), corev1.ResourceCPU: resource.MustParse("200m")}}},
			},
			Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr.To(resource.MustParse("64Mi"))}}}, {Name: "ca", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "fixture-ca"}}}}, {Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "fixture-tls", DefaultMode: ptr.To(int32(0440))}}}},
		}}
		pod.Labels[managed.Label] = managed.Value
		if _, err := cs.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(120 * time.Second)
	for {
		pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ready := 0
		for _, pod := range pods.Items {
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					ready++
				}
			}
		}
		if ready == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native Kubernetes transfer fixture did not become ready")
		}
		time.Sleep(500 * time.Millisecond)
	}
	setupID := uuid.NewString()
	callback := notesURL + "/api/v1/auth/oidc/callback"
	identity := &stack.IdentityPreparation{Schema: "kyidentity.setup/1", SetupID: setupID, Admin: stack.IdentityAccount{Username: "owner-admin", Email: "owner-admin@example.test"}, Everyday: stack.IdentityAccount{Username: "owner", Email: "owner@example.test"}, Clients: []stack.IdentityClient{{ID: "notes", Name: "KyNotes", RedirectURIs: []string{callback}, AdministratorRole: "kynotes.admin", EverydayRole: "kynotes.user", SuiteWebhook: &stack.IdentityWebhook{URL: notesURL + "/api/v1/sync/events"}}}}
	st := &stack.Stack{Version: 1, Targets: []stack.Target{{Name: "k8s", Kubeconfig: kubeconfig}}, Existing: []stack.Existing{{Product: "kyidentity", Target: "k8s", URL: issuerURL}, {Product: "kynotes", Target: "k8s", URL: notesURL}}, Integrations: []stack.Integration{
		{Product: "kynotes", Target: "k8s", Namespace: namespace, Pod: "notes", Container: "notes", Bundle: "/data/installer/setup.json", IdentityClient: "notes", Notes: &stack.NotesPreparation{Version: 1, SSO: &stack.NotesSSO{IssuerURL: issuerURL, ClientID: "notes", RedirectURI: callback, ClientSecretFile: "/data/installer/client.secret", DirectoryHMACSecretFile: "/data/installer/directory.secret"}}},
		{Product: "kyidentity", Target: "k8s", Namespace: namespace, Pod: "identity", Container: "identity", Bundle: "/data/installer/" + setupID + "/setup.json", Identity: identity},
	}}
	state := t.TempDir()
	raw, err := yaml.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(filepath.Join(state, "stack.yaml"), raw, 0600) != nil {
		t.Fatal("write fixture plan")
	}
	run := func(check bool) ([]byte, error) {
		args := []string{"integrate-stack", "--state", state}
		if check {
			args = append(args, "--check")
		}
		var out bytes.Buffer
		err := cli.Run(ctx, args, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "e2e", Out: &out})
		return out.Bytes(), err
	}
	if out, err := run(true); err == nil || len(out) != 0 {
		t.Fatal("absent transfer inspected as ready")
	}
	var reports []byte
	for _, check := range []bool{false, true, false} {
		out, err := run(check)
		if err != nil {
			t.Fatal("native Kubernetes credential transfer failed", err)
		}
		if !bytes.Contains(out, []byte(`"product":"kynotes"`)) || !bytes.Contains(out, []byte(`"section":"sso"`)) {
			t.Fatal("native SSO setup not verified")
		}
		reports = append(reports, out...)
	}
	frame, err := connected.Exec(ctx, namespace, "identity", "identity", []string{"/usr/local/bin/kyidentity", "apply-setup", "--file", st.Integrations[1].Bundle, "--export-client", "notes"})
	if err != nil {
		t.Fatal("native source export failed")
	}
	defer clear(frame)
	var source struct {
		ClientSecret, DirectorySecret string
		Administrator                 struct{ Subject, Username, Role string }
	}
	if json.Unmarshal(frame, &source) != nil || source.ClientSecret == "" || source.DirectorySecret == "" {
		t.Fatal("invalid private native source frame")
	}
	inspect := func() map[string][]byte {
		out, err := connected.Exec(ctx, namespace, "notes", "notes", []string{"/fixture-tests", "-test.run=^TestCredentialFixture$", "--", "read"})
		if err != nil {
			t.Fatal("owned native destination inspection failed")
		}
		defer clear(out)
		var files map[string][]byte
		if json.Unmarshal(out, &files) != nil {
			t.Fatal("invalid fixture inspection")
		}
		return files
	}
	files := inspect()
	defer func() {
		for _, raw := range files {
			clear(raw)
		}
	}()
	if string(files["client.secret"]) != source.ClientSecret || string(files["directory.secret"]) != source.DirectorySecret {
		t.Fatal("native destination changed source credentials")
	}
	var bundle struct{ Admins []stack.NotesAdmin }
	if json.Unmarshal(files["setup.json"], &bundle) != nil || len(bundle.Admins) != 1 || bundle.Admins[0].Subject != source.Administrator.Subject || bundle.Admins[0].Username != "owner-admin" || bundle.Admins[0].Issuer != issuerURL || source.Administrator.Role != "kynotes.admin" {
		t.Fatal("native owner binding missing")
	}
	for _, secret := range []string{source.ClientSecret, source.DirectorySecret} {
		if bytes.Contains(reports, []byte(secret)) {
			t.Fatal("private credential entered installer report")
		}
	}
	if err := filepath.WalkDir(state, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(raw, []byte(source.ClientSecret)) || bytes.Contains(raw, []byte(source.DirectorySecret)) {
			t.Fatal("private credential persisted on workstation")
		}
		return nil
	}); err != nil {
		t.Fatal("could not inspect all workstation state", err)
	}
	write := func(raw []byte) {
		if _, err := connected.ExecInput(ctx, namespace, "notes", "notes", []string{"/fixture-tests", "-test.run=^TestCredentialFixture$", "--", "write-client"}, raw); err != nil {
			t.Fatal("owned fixture write failed")
		}
	}
	changed := bytes.Repeat([]byte{'x'}, 43)
	write(changed)
	for _, check := range []bool{true, false} {
		if _, err := run(check); err == nil {
			t.Fatal("changed imported credentials accepted")
		}
	}
	retained := inspect()
	if !bytes.Equal(retained["client.secret"], changed) {
		t.Fatal("conflicting credentials overwritten")
	}
	for _, raw := range retained {
		clear(raw)
	}
	write([]byte(source.ClientSecret))
	if _, err := run(true); err != nil {
		t.Fatal("retained native transfer could not be reinspected", err)
	}
	t.Log("restricted kind native credential transfer, TLS discovery, exact owner/credential binding, resume and drift preservation passed; browser sign-on remains a separate gate")
}
