package offboard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
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
	Directory *runtimeDirectoryFile `json:"directory,omitempty"`
	Groups    *GroupConfig          `json:"groups,omitempty"`
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
	target *Target
	token  string
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

// LoadRuntime is read-only and makes no native requests. Files live in a separate
// service-owned 0700 target directory; relative basenames cannot escape that root.
func LoadRuntime(file string) (*Runtime, error) {
	if !filepath.IsAbs(file) || filepath.Clean(file) != file || !privateName.MatchString(filepath.Base(file)) {
		return nil, ErrRuntime
	}
	directory := filepath.Dir(file)
	root, err := openPrivateRoot(directory)
	if err != nil {
		return nil, ErrRuntime
	}
	defer root.Close()
	raw, err := readPrivate(root, filepath.Base(file), 128<<10)
	if err != nil {
		return nil, ErrRuntime
	}
	var cfg runtimeFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if !uniqueJSON(raw) || decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF || cfg.Schema != "ky.offboarding-runtime/1" || !setupID.MatchString(cfg.SetupID) || len(cfg.Apps) < 1 || len(cfg.Apps) > 32 || !privateName.MatchString(cfg.CertFile) || !privateName.MatchString(cfg.KeyFile) || cfg.CertFile == cfg.KeyFile {
		return nil, ErrRuntime
	}
	host, portText, err := net.SplitHostPort(cfg.Listen)
	port, portErr := strconv.Atoi(portText)
	address, addrErr := netip.ParseAddr(host)
	if err != nil || portErr != nil || addrErr != nil || address.Zone() != "" || address.IsMulticast() || port < 1 || port > 65535 || strconv.Itoa(port) != portText {
		return nil, ErrRuntime
	}
	if within(directory, cfg.StateDir) || within(cfg.StateDir, directory) {
		return nil, ErrRuntime
	}
	state, err := openPrivateRoot(cfg.StateDir)
	if err != nil {
		return nil, ErrRuntime
	}
	state.Close()
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
			(app.Kind == "immich" && !validClientID(app.ClientID)) || (app.Kind != "immich" && app.ClientID != "") {
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
	cert, err := readPrivate(root, cfg.CertFile, 16<<10)
	if err != nil {
		return nil, ErrRuntime
	}
	key, err := readPrivate(root, cfg.KeyFile, 16<<10)
	if err != nil {
		return nil, ErrRuntime
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, ErrRuntime
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.IsCA || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return nil, ErrRuntime
	}
	serverUsage := false
	for _, usage := range leaf.ExtKeyUsage {
		if usage == x509.ExtKeyUsageServerAuth {
			serverUsage = true
		}
	}
	if !serverUsage {
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
		decoded, err := hex.DecodeString(scimToken)
		if err != nil || len(decoded) != 32 || strings.ToLower(scimToken) != scimToken || strings.IndexFunc(productToken, func(r rune) bool { return r <= 32 || r >= 127 }) >= 0 {
			return nil, ErrRuntime
		}
		for _, token := range []string{productToken, scimToken} {
			digest := sha256.Sum256([]byte(token))
			if seenTokens[digest] {
				return nil, ErrRuntime
			}
			seenTokens[digest] = true
		}
		spec := TargetSpec{App: app.App, Kind: app.Kind, Origin: app.Origin, Issuer: app.Issuer, SourceID: app.SourceID, ClientID: app.ClientID, SetupID: cfg.SetupID, Groups: app.Groups}
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
		runtime.apps = append(runtime.apps, runtimeApp{target: target, token: scimToken})
	}
	return runtime, nil
}

// Serve owns the state lease, direct TLS listener and resident worker. Shutdown
// cancels and joins HTTP/native work before releasing target-local state.
func (r *Runtime) Serve(ctx context.Context) error {
	store, err := Open(r.state)
	if err != nil {
		return ErrRuntime
	}
	defer store.Close()
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
