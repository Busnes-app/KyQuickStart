package edge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// Cluster is what the nginx edge takes from its Kubernetes target: the ConfigMap its pods
// read, what those pods run, their TLS Secrets and cert-manager's objects, all in the nginx
// app's namespace.
type Cluster interface {
	Secrets
	// EdgeFiles reads the configuration files and a version SetEdgeFiles checks.
	EdgeFiles(ctx context.Context) (files map[string]string, version string, err error)
	// SetEdgeFiles replaces the files; a version no longer current is engine.ErrTransient.
	SetEdgeFiles(ctx context.Context, files map[string]string, version string) error
	// EdgeLoaded returns NginxLoaded's output from every ready edge pod, by pod name. hasten
	// first asks each pod's kubelet to re-read its volumes now instead of at its next sync.
	EdgeLoaded(ctx context.Context, hasten string) (map[string]string, error)
	// TLSCertificate reads tls.crt of a kubernetes.io/tls Secret, nil when absent; never its key.
	TLSCertificate(ctx context.Context, secret string) ([]byte, error)
	// ACMEToken reads, and SetACMEToken writes, the installer's Secret name holding the DNS
	// API token for an Issuer, which reads credentials only from its own namespace.
	ACMEToken(ctx context.Context, name string) ([]byte, error)
	SetACMEToken(ctx context.Context, name string, token []byte) error
	// CertManager reads the installer's Issuer or Certificate name: the input hash it was
	// written with ("" when absent) and, for a Certificate, its Ready condition.
	CertManager(ctx context.Context, kind, name string) (hash string, ready bool, message string, err error)
	// ApplyCertManager creates or replaces the installer's Issuer or Certificate.
	ApplyCertManager(ctx context.Context, kind, name, hash string, spec map[string]any) error
}

// Nginx reconciles the routes of a proxy: nginx edge: the catalog nginx app's pods serve
// configuration rendered here by the generator NPM's hosts use.
type Nginx struct {
	c            Cluster
	installation string
	lan          *http.Client // KyDNS
	redact       *engine.Redactor
}

// Wait bounds how long the pods get to load a change, and a certificate to be issued; poll
// spaces the checks. Tests shorten them.
var nginxWait, nginxIssueWait, nginxPoll = 3 * time.Minute, 15 * time.Minute, 2 * time.Second

func NewNginx(c Cluster, installation string, redact *engine.Redactor) (*Nginx, error) {
	if installation == "" {
		return nil, errors.New("route ownership needs stack.yaml's installation_id; run kyquickstart apply")
	}
	return &Nginx{c: c, installation: installation, lan: npmClient(lanTransport()), redact: redact}, nil
}

func (n *Nginx) Close() { n.lan.CloseIdleConnections() }

// prefix starts the first line of every file this installation wrote for e's target.
func (n *Nginx) prefix(e stack.Edge) string {
	return "# kyquickstart:" + n.installation + ":" + e.Target + ":"
}

func (n *Nginx) want(e stack.Edge, r stack.Route) string {
	return nginxServer(n.prefix(e)+routeMark(r), e, r)
}

// serverNames lists the names a file's server blocks answer for.
func serverNames(file string) []string {
	var out []string
	for _, line := range strings.Split(file, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "server_name"); ok {
			out = append(out, strings.Fields(strings.TrimSuffix(strings.TrimSpace(rest), ";"))...)
		}
	}
	return out
}

// claim refuses r's hostname when a file this route does not own answers for it: another
// installation's, another app's, or one written by hand.
func (n *Nginx) claim(files map[string]string, e stack.Edge, r stack.Route) error {
	key, mark := routeKey(r.Hostname), n.prefix(e)+routeMark(r)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		file := files[name]
		mine := name == key+enabledExt || name == key+disabledExt
		switch {
		case mine && strings.HasPrefix(file, mark):
		case mine:
			return fmt.Errorf("hostname %s is claimed by an unmanaged or different edge route (%s); refusing adoption", r.Hostname, name)
		case strings.HasSuffix(name, enabledExt) && slices.ContainsFunc(serverNames(file), func(s string) bool { return overlaps(s, r.Hostname) }):
			return fmt.Errorf("hostname %s is claimed by %s, which this route does not own", r.Hostname, name)
		}
	}
	return nil
}

// certificate refuses a route whose TLS Secret is absent, expired or does not cover it.
func (n *Nginx) certificate(ctx context.Context, r stack.Route) error {
	name := tlsSecret(r)
	b, err := n.c.TLSCertificate(ctx, name)
	if err != nil {
		return err
	}
	how := "create it as a kubernetes.io/tls Secret in the nginx app's namespace"
	if r.Certificate != "" {
		how = "certificate " + r.Certificate + " is not issued yet; run kyquickstart apply"
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return fmt.Errorf("TLS Secret %s for %s is absent or holds no certificate: %s", name, r.Hostname, how)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil || leaf.VerifyHostname(r.Hostname) != nil || !leaf.NotAfter.After(time.Now()) {
		return fmt.Errorf("TLS Secret %s must hold an unexpired certificate that covers %s", name, r.Hostname)
	}
	return nil
}

// serving reports why some ready pod does not run files' generation, "" when all do.
func (n *Nginx) serving(ctx context.Context, files map[string]string, hasten bool) (string, error) {
	gen := NginxGeneration(files)
	poke := ""
	if hasten {
		poke = gen
	}
	loaded, err := n.c.EdgeLoaded(ctx, poke)
	if err != nil {
		return "", err
	}
	if len(loaded) == 0 {
		return "no edge pod is ready", nil
	}
	for _, pod := range slices.Sorted(maps.Keys(loaded)) {
		have, reason, _ := strings.Cut(loaded[pod], "\n")
		if strings.TrimSpace(have) == gen {
			continue
		}
		if reason = strings.TrimSpace(reason); reason != "" {
			if len(reason) > 400 {
				reason = reason[:400]
			}
			return fmt.Sprintf("pod %s rejected the configuration: %s", pod, reason), nil
		}
		return fmt.Sprintf("pod %s has not loaded the configuration yet", pod), nil
	}
	return "", nil
}

// await waits until every ready pod runs files' generation.
func (n *Nginx) await(ctx context.Context, files map[string]string) error {
	deadline := time.Now().Add(nginxWait)
	for hasten := true; ; hasten = false {
		why, err := n.serving(ctx, files, hasten)
		if err != nil || why == "" {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the edge did not load the configuration within %s: %s", nginxWait, why)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(nginxPoll):
		}
	}
}

type nginxRoute struct {
	n *Nginx
	e stack.Edge
	r stack.Route
}

func (s nginxRoute) ID() string {
	return s.e.Target + ".edge." + routeKey(s.r.Hostname)
}
func (s nginxRoute) InputHash() string {
	sum := sha256.Sum256([]byte("nginx\x00" + s.n.want(s.e, s.r)))
	return hex.EncodeToString(sum[:])
}

// state returns why the route is not served as wanted, "" when it is.
func (s nginxRoute) state(ctx context.Context) (string, error) {
	files, _, err := s.n.c.EdgeFiles(ctx)
	if err != nil {
		return "", err
	}
	if err := s.n.claim(files, s.e, s.r); err != nil {
		return "", err
	}
	if files[routeKey(s.r.Hostname)+enabledExt] != s.n.want(s.e, s.r) {
		return "the route is absent, differs or is disabled", nil
	}
	if err := s.n.certificate(ctx, s.r); err != nil {
		return "", err
	}
	return s.n.serving(ctx, files, false)
}

func (s nginxRoute) Inspect(ctx context.Context) (bool, error) {
	why, err := s.state(ctx)
	return why == "", err
}

// Apply enables the route only once its certificate is valid, and reports it only once every
// ready pod serves it. A route the pods do not load is disabled again, never left half-applied.
func (s nginxRoute) Apply(ctx context.Context) error {
	files, version, err := s.n.c.EdgeFiles(ctx)
	if err != nil {
		return err
	}
	if err := s.n.claim(files, s.e, s.r); err != nil {
		return err
	}
	if err := s.n.certificate(ctx, s.r); err != nil {
		return err
	}
	key := routeKey(s.r.Hostname)
	if want := s.n.want(s.e, s.r); files[key+enabledExt] != want {
		delete(files, key+disabledExt)
		files[key+enabledExt] = want
		if err := s.n.c.SetEdgeFiles(ctx, files, version); err != nil {
			return err
		}
	}
	if err := s.n.await(ctx, files); err != nil {
		return errors.Join(err, s.n.disable(ctx, key))
	}
	return nil
}

func (s nginxRoute) Verify(ctx context.Context) error {
	why, err := s.state(ctx)
	if err == nil && why != "" {
		err = errors.New(why)
	}
	return err
}

// disable renames key's file so nginx no longer includes it; the file stays for the operator.
func (n *Nginx) disable(ctx context.Context, keys ...string) error {
	files, version, err := n.c.EdgeFiles(ctx)
	if err != nil {
		return err
	}
	changed := false
	for _, key := range keys {
		if file, ok := files[key+enabledExt]; ok {
			delete(files, key+enabledExt)
			files[key+disabledExt] = file
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return n.c.SetEdgeFiles(ctx, files, version)
}

// nginxRemoved disables this installation's routes on the target whose hostname left
// stack.yaml. Deleting the disabled file is left to the operator.
type nginxRemoved struct {
	n *Nginx
	e stack.Edge
}

func (s nginxRemoved) ID() string { return s.e.Target + ".edge.removed" }
func (s nginxRemoved) InputHash() string {
	names := []string{"nginx", s.n.installation}
	for _, r := range s.e.Routes {
		names = append(names, r.Hostname)
	}
	sum := sha256.Sum256([]byte(strings.Join(names, "\n")))
	return hex.EncodeToString(sum[:])
}

// pending lists the keys of enabled files this installation owns outside the routes.
func (s nginxRemoved) pending(files map[string]string) []string {
	routed := map[string]bool{}
	for _, r := range s.e.Routes {
		routed[routeKey(r.Hostname)] = true
	}
	var out []string
	for _, name := range slices.Sorted(maps.Keys(files)) {
		key, ok := strings.CutSuffix(name, enabledExt)
		if ok && !routed[key] && strings.HasPrefix(files[name], s.n.prefix(s.e)) {
			out = append(out, key)
		}
	}
	return out
}

func (s nginxRemoved) Inspect(ctx context.Context) (bool, error) { return s.Verify(ctx) == nil, nil }

func (s nginxRemoved) Apply(ctx context.Context) error {
	files, _, err := s.n.c.EdgeFiles(ctx)
	if err != nil {
		return err
	}
	if err := s.n.disable(ctx, s.pending(files)...); err != nil {
		return err
	}
	if files, _, err = s.n.c.EdgeFiles(ctx); err != nil {
		return err
	}
	return s.n.await(ctx, files)
}

func (s nginxRemoved) Verify(ctx context.Context) error {
	files, _, err := s.n.c.EdgeFiles(ctx)
	if err != nil {
		return err
	}
	if left := s.pending(files); len(left) > 0 {
		return fmt.Errorf("installer-owned route %s%s is enabled but absent from stack.yaml", left[0], enabledExt)
	}
	why, err := s.n.serving(ctx, files, false)
	if err == nil && why != "" {
		err = errors.New(why)
	}
	return err
}

// letsEncrypt is the ACME directory certificates are requested from, as NPM's are.
const letsEncrypt = "https://acme-v02.api.letsencrypt.org/directory"

// nginxCert has cert-manager request c: an Issuer (Let's Encrypt, DNS-01 through Cloudflare)
// and a Certificate, both named after it. Renewal is cert-manager's job.
type nginxCert struct {
	n *Nginx
	e stack.Edge
	c stack.Certificate
}

func (s nginxCert) name() string { return "kyq-" + s.c.Name }

// issuer and certificate are the objects' specs. The DNS token stays in its Secret.
func (s nginxCert) issuer() map[string]any {
	return map[string]any{"acme": map[string]any{
		"server":              letsEncrypt,
		"email":               s.c.Email,
		"privateKeySecretRef": map[string]any{"name": s.name() + "-account"},
		"solvers": []any{map[string]any{"dns01": map[string]any{"cloudflare": map[string]any{
			"apiTokenSecretRef": map[string]any{"name": s.name() + "-dns", "key": "api-token"},
		}}}},
	}}
}

func (s nginxCert) certificate() map[string]any {
	names := make([]any, len(s.c.Domains))
	for i, d := range s.c.Domains {
		names[i] = d
	}
	return map[string]any{
		"secretName": ManagedTLSSecret(s.c.Name),
		"dnsNames":   names,
		"issuerRef":  map[string]any{"name": s.name(), "kind": "Issuer", "group": "cert-manager.io"},
		// The label by which the installer accepts a TLS Secret as the edge's.
		"secretTemplate": map[string]any{"labels": map[string]any{managed.Label: managed.Value}},
	}
}

func (s nginxCert) ID() string { return s.e.Target + ".edge.cert." + s.c.Name }
func (s nginxCert) InputHash() string {
	b, _ := json.Marshal([]any{"nginx", s.n.installation, s.issuer(), s.certificate()}) // references only
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// state returns why the certificate is not issued as wanted, "" when it is.
func (s nginxCert) state(ctx context.Context) (string, error) {
	token, err := dnsCredential(ctx, s.n.c, s.n.redact, s.c)
	if err != nil {
		return "", err
	}
	if have, err := s.n.c.ACMEToken(ctx, s.name()+"-dns"); err != nil || !bytes.Equal(have, []byte(token)) {
		return "the Issuer's copy of the DNS credential is absent or differs", err
	}
	for kind, spec := range map[string]map[string]any{"Issuer": s.issuer(), "Certificate": s.certificate()} {
		hash, ready, message, err := s.n.c.CertManager(ctx, kind, s.name())
		switch {
		case err != nil:
			return "", err
		case hash != s.hash(spec):
			return kind + " " + s.name() + " is absent or differs", nil
		case kind == "Certificate" && !ready:
			return "cert-manager has not issued certificate " + s.c.Name + ": " + message, nil
		}
	}
	return "", nil
}

func (s nginxCert) hash(spec map[string]any) string {
	b, _ := json.Marshal([]any{s.n.installation, spec})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s nginxCert) Inspect(ctx context.Context) (bool, error) {
	why, err := s.state(ctx)
	return why == "", err
}

func (s nginxCert) Apply(ctx context.Context) error {
	token, err := dnsCredential(ctx, s.n.c, s.n.redact, s.c)
	if err != nil {
		return err
	}
	if err := s.n.c.SetACMEToken(ctx, s.name()+"-dns", []byte(token)); err != nil {
		return err
	}
	if err := s.n.c.ApplyCertManager(ctx, "Issuer", s.name(), s.hash(s.issuer()), s.issuer()); err != nil {
		return err
	}
	if err := s.n.c.ApplyCertManager(ctx, "Certificate", s.name(), s.hash(s.certificate()), s.certificate()); err != nil {
		return err
	}
	deadline := time.Now().Add(nginxIssueWait)
	for {
		why, err := s.state(ctx)
		if err != nil || why == "" {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s after %s; cert-manager keeps trying: kubectl describe the Certificate %s in the nginx app's namespace", why, nginxIssueWait, s.name())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(nginxPoll):
		}
	}
}

func (s nginxCert) Verify(ctx context.Context) error {
	why, err := s.state(ctx)
	if err == nil && why != "" {
		err = errors.New(why)
	}
	return err
}

// Steps requires an Edge already validated by stack.Parse, in NPM's order: removed routes are
// disabled first, then certificates are issued before the routes that use them.
func (n *Nginx) Steps(e stack.Edge) []engine.Step {
	out := []engine.Step{nginxRemoved{n: n, e: e}}
	for _, c := range e.Certificates {
		out = append(out, nginxCert{n: n, e: e, c: c})
	}
	for _, r := range e.Routes {
		out = append(out, nginxRoute{n: n, e: e, r: r})
	}
	if e.LANDNS != nil {
		out = append(out, dnsStep{n: n.dns(), e: e})
	}
	return out
}

func (n *Nginx) dns() lanDNS { return lanDNS{n.installation, n.lan, n.c, n.redact} }

// Preflight is read-only: hostname ownership of every route, each certificate's DNS credential
// and each operator TLS Secret. A managed certificate not yet issued is its step's to check.
func (n *Nginx) Preflight(ctx context.Context, e stack.Edge) error {
	files, version, err := n.c.EdgeFiles(ctx)
	if err != nil {
		return err
	}
	for _, c := range e.Certificates {
		if _, err := dnsCredential(ctx, n.c, n.redact, c); err != nil {
			return err
		}
	}
	for _, r := range e.Routes {
		if err := n.claim(files, e, r); err != nil {
			return err
		}
		// Before the nginx app's first deploy its namespace cannot hold the Secret yet.
		if r.CertificateSecret != "" && version != "" {
			if err := n.certificate(ctx, r); err != nil {
				return err
			}
		}
	}
	if e.LANDNS != nil {
		s := dnsStep{n: n.dns(), e: e}
		k, err := s.client(ctx)
		if err != nil {
			return err
		}
		if _, err := s.plan(ctx, k); err != nil {
			return err
		}
	}
	return nil
}
