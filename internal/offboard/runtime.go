package offboard

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/adapters/immich"
)

var ErrRuntime = errors.New("offboarding runtime configuration or service is unavailable")
var privateName = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

type runtimeFile struct {
	Schema   string            `json:"schema"`
	SetupID  string            `json:"setup_id"`
	Listen   string            `json:"listen"`
	StateDir string            `json:"state_dir"`
	CertFile string            `json:"cert_file"`
	KeyFile  string            `json:"key_file"`
	Apps     []runtimeAppFile  `json:"apps"`
	Audit    *runtimeAuditFile `json:"audit,omitempty"`
}

type runtimeAuditFile struct {
	Origin        string `json:"origin"`
	TokenFile     string `json:"token_file"`
	RequireAlerts bool   `json:"require_alerts"`
}

type runtimeAppFile struct {
	App       string                `json:"app"`
	Kind      string                `json:"kind"`
	Origin    string                `json:"origin"`
	Issuer    string                `json:"issuer"`
	SourceID  int64                 `json:"source_id,omitempty"`
	ClientID  string                `json:"client_id,omitempty"`
	TokenFile string                `json:"product_token_file"`
	SCIMFile  string                `json:"scim_token_file"`
	APIOrigin string                `json:"api_origin,omitempty"`
	Accounts  []runtimeAccountFile  `json:"accounts,omitempty"`
	Directory *runtimeDirectoryFile `json:"directory,omitempty"`
	Groups    *GroupConfig          `json:"groups,omitempty"`
}

// runtimeAccountFile is one setup-created Immich account, registered before serving.
type runtimeAccountFile struct {
	NativeUUID string `json:"native_uuid"`
	Subject    string `json:"subject"`
}

func validAccounts(accounts []runtimeAccountFile) bool {
	if len(accounts) < 1 || len(accounts) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, a := range accounts {
		if !nativeUUID.MatchString(a.NativeUUID) || !validSubject(a.Subject) || !validClientID(a.Subject) || seen["u"+a.NativeUUID] || seen["s"+a.Subject] {
			return false
		}
		seen["u"+a.NativeUUID], seen["s"+a.Subject] = true, true
	}
	return true
}

type runtimeDirectoryFile struct {
	AdministratorRole string                    `json:"administrator_role"`
	EverydayRole      string                    `json:"everyday_role"`
	CreateAccounts    bool                      `json:"create_accounts"`
	TokenFile         string                    `json:"token_file,omitempty"`
	CreationTokenFile string                    `json:"creation_token_file,omitempty"`
	BookStack         *BookStackDirectoryConfig `json:"bookstack,omitempty"`
}

type runtimeApp struct {
	target   *Target
	token    string
	accounts []Binding
}
type Runtime struct {
	listen, state string
	certificate   tls.Certificate
	apps          []runtimeApp
	audit         *AuditDestination
}

// Diagnostics never expand the credential-bearing runtime object.
func (Runtime) String() string     { return "offboarding runtime (private configuration)" }
func (r Runtime) GoString() string { return r.String() }

type RuntimeReport struct {
	Schema       string `json:"schema"`
	Status       string `json:"status"`
	Applications int    `json:"applications"`
}

func (r *Runtime) Report() RuntimeReport {
	return RuntimeReport{Schema: "ky.offboarding-runtime/1", Status: "valid", Applications: len(r.apps)}
}

func within(directory, path string) bool {
	rel, err := filepath.Rel(directory, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// parseRuntime reads and checks the configuration's top level. The caller closes root.
func parseRuntime(file string) (runtimeFile, *os.Root, error) {
	var cfg runtimeFile
	if !filepath.IsAbs(file) || filepath.Clean(file) != file || !privateName.MatchString(filepath.Base(file)) {
		return cfg, nil, ErrRuntime
	}
	directory := filepath.Dir(file)
	root, err := openPrivateRoot(directory)
	if err != nil {
		return cfg, nil, ErrRuntime
	}
	fail := func() (runtimeFile, *os.Root, error) { root.Close(); return cfg, nil, ErrRuntime }
	raw, err := readPrivate(root, filepath.Base(file), 128<<10)
	if err != nil {
		return fail()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if !uniqueJSON(raw) || decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF || cfg.Schema != "ky.offboarding-runtime/1" || !setupID.MatchString(cfg.SetupID) || len(cfg.Apps) < 1 || len(cfg.Apps) > 32 || !privateName.MatchString(cfg.CertFile) || !privateName.MatchString(cfg.KeyFile) || cfg.CertFile == cfg.KeyFile {
		return fail()
	}
	host, portText, err := net.SplitHostPort(cfg.Listen)
	port, portErr := strconv.Atoi(portText)
	address, addrErr := netip.ParseAddr(host)
	if err != nil || portErr != nil || addrErr != nil || address.Zone() != "" || address.IsMulticast() || port < 1 || port > 65535 || strconv.Itoa(port) != portText {
		return fail()
	}
	if within(directory, cfg.StateDir) || within(cfg.StateDir, directory) {
		return fail()
	}
	state, err := openPrivateRoot(cfg.StateDir)
	if err != nil {
		return fail()
	}
	state.Close()
	return cfg, root, nil
}

// LoadRuntime is read-only and makes no native requests. Files live in a separate
// service-owned 0700 target directory; relative basenames cannot escape that root.
func LoadRuntime(file string) (*Runtime, error) {
	cfg, root, err := parseRuntime(file)
	if err != nil {
		return nil, ErrRuntime
	}
	defer root.Close()
	seenApps, seenOrigins, seenFiles := map[string]bool{}, map[string]bool{}, map[string]bool{filepath.Base(file): true, cfg.CertFile: true, cfg.KeyFile: true}
	if cfg.Audit != nil {
		if !auditOrigin(cfg.Audit.Origin) || !privateName.MatchString(cfg.Audit.TokenFile) || seenFiles[cfg.Audit.TokenFile] {
			return nil, ErrRuntime
		}
		seenFiles[cfg.Audit.TokenFile] = true
	}
	// Validate the entire public intent before reading a single credential.
	for _, app := range cfg.Apps {
		if !appName.MatchString(app.App) || !https(app.Origin, true) || !https(app.Issuer, false) || seenApps[app.App] ||
			!privateName.MatchString(app.TokenFile) || !privateName.MatchString(app.SCIMFile) || app.TokenFile == app.SCIMFile || seenFiles[app.TokenFile] || seenFiles[app.SCIMFile] ||
			(app.Kind != "bookstack" && app.Kind != "forgejo" && app.Kind != "immich") ||
			(app.Kind != "forgejo" && app.SourceID != 0) || (app.Kind == "forgejo" && app.SourceID <= 0) ||
			(app.Kind == "immich" && !validClientID(app.ClientID)) || (app.Kind != "immich" && app.ClientID != "") ||
			(app.APIOrigin != "" && (app.Kind != "immich" || !immich.InternalOrigin(app.APIOrigin))) ||
			(app.Accounts != nil && (app.Kind != "immich" || !validAccounts(app.Accounts))) {
			return nil, ErrRuntime
		}
		if app.Directory != nil && ((app.Kind != "forgejo" && app.Kind != "immich" && app.Kind != "bookstack") || app.Directory.AdministratorRole == "" || !validDirectoryRoles(app.Directory.AdministratorRole, app.Directory.EverydayRole)) {
			return nil, ErrRuntime
		}
		if app.Directory != nil {
			d := app.Directory
			if app.Kind == "bookstack" {
				if d.AdministratorRole != "admin" || !validBookStackDirectory(d.BookStack) ||
					!privateName.MatchString(d.TokenFile) || seenFiles[d.TokenFile] || d.TokenFile == app.TokenFile || d.TokenFile == app.SCIMFile {
					return nil, ErrRuntime
				}
				seenFiles[d.TokenFile] = true
				if d.CreateAccounts && d.CreationTokenFile == "" {
					return nil, ErrRuntime
				}
				if d.CreationTokenFile != "" {
					if !privateName.MatchString(d.CreationTokenFile) || seenFiles[d.CreationTokenFile] || d.CreationTokenFile == app.TokenFile || d.CreationTokenFile == app.SCIMFile {
						return nil, ErrRuntime
					}
					seenFiles[d.CreationTokenFile] = true
				}
			} else if d.TokenFile != "" || d.CreationTokenFile != "" || d.BookStack != nil {
				return nil, ErrRuntime
			}
		}
		if app.Groups != nil && (app.Kind != "forgejo" || app.Directory == nil || !validGroupConfig(app.Groups)) {
			return nil, ErrRuntime
		}
		origin, _ := url.Parse(app.Origin)
		port := 443
		if origin.Port() != "" {
			parsed, err := strconv.Atoi(origin.Port())
			if err != nil || parsed < 1 || parsed > 65535 {
				return nil, ErrRuntime
			}
			port = parsed
		}
		originKey := net.JoinHostPort(strings.TrimSuffix(strings.ToLower(origin.Hostname()), "."), strconv.Itoa(port))
		if seenOrigins[originKey] {
			return nil, ErrRuntime
		}
		seenApps[app.App], seenOrigins[originKey], seenFiles[app.TokenFile], seenFiles[app.SCIMFile] = true, true, true, true
	}
	pair, err := loadCertificate(root, cfg)
	if err != nil {
		return nil, ErrRuntime
	}
	runtime := &Runtime{listen: cfg.Listen, state: cfg.StateDir, certificate: pair}
	seenTokens := map[[32]byte]bool{}
	if cfg.Audit != nil {
		raw, err := readPrivate(root, cfg.Audit.TokenFile, 129)
		if err != nil {
			return nil, ErrRuntime
		}
		token := strings.TrimSuffix(string(raw), "\n")
		runtime.audit, err = NewAuditDestination(cfg.Audit.Origin, token)
		if err != nil {
			return nil, ErrRuntime
		}
		runtime.audit.requireAlerts = cfg.Audit.RequireAlerts
		seenTokens[sha256.Sum256([]byte(token))] = true
	}
	for _, app := range cfg.Apps {
		product, err := readPrivate(root, app.TokenFile, 16<<10)
		if err != nil {
			return nil, ErrRuntime
		}
		scim, err := readPrivate(root, app.SCIMFile, 128)
		if err != nil {
			return nil, ErrRuntime
		}
		productToken, scimToken := strings.TrimSuffix(string(product), "\n"), strings.TrimSuffix(string(scim), "\n")
		if !validSCIMToken(scimToken) || strings.IndexFunc(productToken, func(r rune) bool { return r <= 32 || r >= 127 }) >= 0 {
			return nil, ErrRuntime
		}
		for _, token := range []string{productToken, scimToken} {
			digest := sha256.Sum256([]byte(token))
			if seenTokens[digest] {
				return nil, ErrRuntime
			}
			seenTokens[digest] = true
		}
		spec := TargetSpec{App: app.App, Kind: app.Kind, Origin: app.Origin, APIOrigin: app.APIOrigin, Issuer: app.Issuer, SourceID: app.SourceID, ClientID: app.ClientID, SetupID: cfg.SetupID, Groups: app.Groups}
		if app.Directory != nil {
			spec.AdministratorRole, spec.EverydayRole = app.Directory.AdministratorRole, app.Directory.EverydayRole
			spec.CreateAccounts = app.Directory.CreateAccounts
		}
		directoryToken, creationToken := "", ""
		if app.Directory != nil && app.Kind == "bookstack" {
			raw, err := readPrivate(root, app.Directory.TokenFile, 128)
			if err != nil {
				return nil, ErrRuntime
			}
			directoryToken = strings.TrimSuffix(string(raw), "\n")
			digest := sha256.Sum256([]byte(directoryToken))
			if seenTokens[digest] {
				return nil, ErrRuntime
			}
			seenTokens[digest] = true
			spec.BookStackDirectory = app.Directory.BookStack
			if app.Directory.CreationTokenFile != "" {
				raw, err := readPrivate(root, app.Directory.CreationTokenFile, 128)
				if err != nil {
					return nil, ErrRuntime
				}
				creationToken = strings.TrimSuffix(string(raw), "\n")
				digest := sha256.Sum256([]byte(creationToken))
				if seenTokens[digest] {
					return nil, ErrRuntime
				}
				seenTokens[digest] = true
			}
		}
		target, err := newTarget(spec, productToken, directoryToken, creationToken)
		if err != nil {
			return nil, ErrRuntime
		}
		accounts := make([]Binding, 0, len(app.Accounts))
		for _, a := range app.Accounts {
			b := Binding{SetupID: cfg.SetupID, App: app.App, Kind: app.Kind, Origin: app.Origin, Issuer: app.Issuer, Subject: a.Subject, NativeUUID: a.NativeUUID, ClientID: app.ClientID}
			if !target.matches(b) {
				return nil, ErrRuntime
			}
			accounts = append(accounts, b)
		}
		runtime.apps = append(runtime.apps, runtimeApp{target: target, token: scimToken, accounts: accounts})
	}
	return runtime, nil
}

// loadCertificate requires an unexpired, non-CA server-auth leaf matching its key.
func loadCertificate(root *os.Root, cfg runtimeFile) (tls.Certificate, error) {
	cert, err := readPrivate(root, cfg.CertFile, 16<<10)
	if err != nil {
		return tls.Certificate{}, ErrRuntime
	}
	key, err := readPrivate(root, cfg.KeyFile, 16<<10)
	if err != nil {
		return tls.Certificate{}, ErrRuntime
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return tls.Certificate{}, ErrRuntime
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.IsCA || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) || !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		return tls.Certificate{}, ErrRuntime
	}
	return pair, nil
}

// bridgeService is the bridge's Compose service name, the leaf's DNS SAN.
const bridgeService = "directory"

// InitTLS creates the bridge's private key and self-signed server leaf on the target.
// The edge proxy does not verify upstream certificates. Existing valid files are
// kept; exactly one file, or an invalid pair, is refused. Renew by deleting both.
func InitTLS(file string) error {
	cfg, root, err := parseRuntime(file)
	if err != nil {
		return ErrRuntime
	}
	defer root.Close()
	_, certErr := root.Lstat(cfg.CertFile)
	_, keyErr := root.Lstat(cfg.KeyFile)
	if certErr == nil && keyErr == nil {
		_, err := loadCertificate(root, cfg)
		return err
	}
	if !errors.Is(certErr, os.ErrNotExist) || !errors.Is(keyErr, os.ErrNotExist) {
		return ErrRuntime
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return ErrRuntime
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return ErrRuntime
	}
	now := time.Now()
	leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: bridgeService}, DNSNames: []string{bridgeService},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(825 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	host, _, _ := net.SplitHostPort(cfg.Listen)
	if ip := net.ParseIP(host); !ip.IsUnspecified() {
		leaf.IPAddresses = []net.IP{ip}
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
	if err != nil {
		return ErrRuntime
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return ErrRuntime
	}
	if createPrivate(root, cfg.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})) != nil {
		return ErrRuntime
	}
	if createPrivate(root, cfg.CertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) != nil {
		root.Remove(cfg.KeyFile)
		return ErrRuntime
	}
	return nil
}

// createPrivate never replaces a file; a failed write leaves nothing behind.
func createPrivate(root *os.Root, name string, data []byte) error {
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrRuntime
	}
	_, writeErr := f.Write(data)
	if errors.Join(writeErr, f.Chmod(0600), f.Sync(), f.Close()) != nil {
		root.Remove(name)
		return ErrRuntime
	}
	return nil
}

// Healthcheck probes this runtime's own listener: an unauthenticated SCIM read must be refused.
func (r *Runtime) Healthcheck(ctx context.Context) error {
	host, port, _ := net.SplitHostPort(r.listen)
	if address, _ := netip.ParseAddr(host); address.IsUnspecified() {
		host = "127.0.0.1"
		if address.Is6() {
			host = "::1"
		}
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 8192,
			// A local liveness probe of the bridge's own self-signed leaf; it sends no credential.
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+net.JoinHostPort(host, port)+"/scim/"+r.apps[0].target.spec.App+"/Users", nil)
	if err != nil {
		return ErrRuntime
	}
	response, err := client.Do(req)
	if err != nil {
		return ErrRuntime
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
		return ErrRuntime
	}
	return nil
}

// register binds setup-created accounts before serving. A retained binding was proved
// when first bound; a different one refuses before native I/O and is never replaced.
func (r *Runtime) register(ctx context.Context, store *Store) error {
	for _, app := range r.apps {
		for _, b := range app.accounts {
			if retained, err := store.Binding(bindingID(b)); err == nil {
				if retained != b {
					return ErrRuntime
				}
				continue
			}
			callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, err := store.Register(callCtx, app.target, b)
			cancel()
			if err != nil {
				return ErrRuntime
			}
		}
	}
	return nil
}

// Serve owns the state lease, direct TLS listener and resident worker. Shutdown
// cancels and joins HTTP/native work before releasing target-local state.
func (r *Runtime) Serve(ctx context.Context) error {
	store, err := Open(r.state)
	if err != nil {
		return ErrRuntime
	}
	defer store.Close()
	if err := r.register(ctx, store); err != nil {
		return err
	}
	receivers := make(map[string]*Receiver, len(r.apps))
	targets := make([]*Target, 0, len(r.apps))
	for _, app := range r.apps {
		receiver, err := NewReceiver(store, app.target, app.token)
		if err != nil {
			return ErrRuntime
		}
		receivers[app.target.spec.App] = receiver
		targets = append(targets, app.target)
	}
	listener, err := net.Listen("tcp", r.listen)
	if err != nil {
		return ErrRuntime
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var handlers sync.WaitGroup
	var handlerMu sync.Mutex
	stopping := false
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			handlerMu.Lock()
			if stopping {
				handlerMu.Unlock()
				scimError(w, http.StatusServiceUnavailable)
				return
			}
			handlers.Add(1)
			handlerMu.Unlock()
			defer handlers.Done()
			parts := strings.Split(req.URL.Path, "/")
			if len(parts) >= 3 && parts[1] == "scim" {
				if receiver := receivers[parts[2]]; receiver != nil {
					receiver.ServeHTTP(w, req)
					return
				}
			}
			w.Header().Set("Cache-Control", "no-store")
			scimError(w, http.StatusForbidden)
		}),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
		BaseContext: func(net.Listener) context.Context { return runCtx }, ErrorLog: log.New(io.Discard, "", 0),
	}
	workerDone, serverDone := make(chan error, 1), make(chan error, 1)
	var auditDone chan error
	if r.audit != nil {
		auditDone = make(chan error, 1)
		go func() { auditDone <- store.ServeAudit(runCtx, r.audit, targets) }()
	}
	go func() { workerDone <- store.ServeWorker(runCtx, targets) }()
	go func() {
		serverDone <- server.Serve(tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{r.certificate}}))
	}()
	workerFinished, serverFinished := false, false
	auditFinished := false
	result := ErrRuntime
	select {
	case <-ctx.Done():
		result = ctx.Err()
	case <-workerDone:
		workerFinished = true
	case <-serverDone:
		serverFinished = true
	case <-auditDone:
		auditFinished = true
	}
	cancel()
	handlerMu.Lock()
	stopping = true
	handlerMu.Unlock()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if server.Shutdown(shutdownCtx) != nil {
		server.Close()
	}
	shutdownCancel()
	if !serverFinished {
		<-serverDone
	}
	if !workerFinished {
		<-workerDone
	}
	if auditDone != nil && !auditFinished {
		<-auditDone
	}
	handlers.Wait()
	return result
}
