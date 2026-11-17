package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// tlsSettle is how old a renewed certificate must be before it replaces a valid copy: the
// edge loads a renewal moments to minutes after it is written, and KyPost must not put a key
// in pairing links before the edge serves it.
const tlsSettle = time.Hour

// tlsCertRoute is the route whose edge's public chain apply copies to app's host: app's route
// where the catalog NPM or the nginx edge alone terminates the clients' TLS. A cloudflare
// edge's external route is Cloudflare's (the probe), or with lan_dns split between Cloudflare
// and the edge, which no one file covers; an NPM the operator runs hands out its certificate
// only bundled with the private key.
func tlsCertRoute(st *stack.Stack, app string) (stack.Edge, stack.Route, bool) {
	e, r := appRoute(st, app)
	return e, r, r.App != "" && e.ClusterApp() != "" && (e.Internal(r) || e.Provider != "cloudflare")
}

// copiesTLSChain reports whether app's rendered Compose file reads a copied chain.
func (s *session) copiesTLSChain(app string) bool {
	return bytes.Contains(s.cat[app].Compose, []byte(catalog.TLSCertFile))
}

// publicChain keeps only raw's CERTIFICATE blocks, re-encoded, and refuses a chain whose first
// certificate (the leaf both apps pin) does not name host or is outside its validity at now.
// Every other block, a private key included, is dropped here, before anything is written.
func publicChain(raw []byte, host string, now time.Time) ([]byte, *x509.Certificate, error) {
	if len(raw) > dockerhost.MaxTLSChain {
		return nil, nil, fmt.Errorf("the chain exceeds %d bytes", dockerhost.MaxTLSChain)
	}
	var out []byte
	var leaf *x509.Certificate
	for rest := raw; ; {
		var b *pem.Block
		if b, rest = pem.Decode(rest); b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("a certificate does not parse: %w", err)
		}
		if leaf == nil {
			leaf = c
		}
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b.Bytes})...)
	}
	switch {
	case leaf == nil:
		return nil, nil, errors.New("it holds no certificate")
	case leaf.VerifyHostname(host) != nil:
		return nil, nil, fmt.Errorf("its leaf does not name %s", host)
	case now.Before(leaf.NotBefore) || now.After(leaf.NotAfter):
		return nil, nil, fmt.Errorf("its leaf is not valid now (%s to %s)", leaf.NotBefore.UTC().Format(time.RFC3339), leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	return out, leaf, nil
}

// tlsCertStep (<app>.tls-cert) copies the public chain app's edge serves for its hostname to
// the app's host, where its Compose file mounts it read-only. It runs before the app's deploy,
// since KyPost refuses to start without the file, and writes only when the chain changed;
// neither app needs a restart to read a new one.
type tlsCertStep struct {
	app, host, source string
	issuer            string // the edge step issuing the route's certificate; "" when it exists already
	read              func(context.Context) ([]byte, error)
	hostRead          func(context.Context) ([]byte, error)
	hostWrite         func(context.Context, []byte) error
	now               func() time.Time

	decided   bool
	cur, want []byte
	note      string
}

func (s *tlsCertStep) ID() string        { return s.app + ".tls-cert" }
func (s *tlsCertStep) InputHash() string { return fmt.Sprintf("%x", sha256Sum(s.source+" "+s.host)) }
func (s *tlsCertStep) Note() string      { return s.note }

// decide reads the edge's chain and the host's copy once per run and settles what the copy
// must be.
func (s *tlsCertStep) decide(ctx context.Context) error {
	if s.decided {
		return nil
	}
	raw, err := s.read(ctx)
	if err != nil {
		return fmt.Errorf("read the chain %s serves for %s: %w", s.source, s.host, err)
	}
	now := s.now()
	chain, leaf, err := publicChain(raw, s.host, now)
	if err != nil {
		return fmt.Errorf("the chain %s serves for %s: %w", s.source, s.host, err)
	}
	if s.cur, err = s.hostRead(ctx); err != nil {
		return err
	}
	fp := sha256.Sum256(leaf.Raw)
	s.want, s.note = chain, fmt.Sprintf("copied the chain %s serves: leaf SHA-256 %x, valid until %s", s.source, fp, leaf.NotAfter.UTC().Format(time.RFC3339))
	if _, _, err := publicChain(s.cur, s.host, now); err == nil && !bytes.Equal(s.cur, chain) && now.Sub(leaf.NotBefore) < tlsSettle {
		s.want, s.note = s.cur, fmt.Sprintf("kept the current copy: the certificate in %s was renewed under an hour ago, and the next run copies it", s.source)
	}
	s.decided = true
	return nil
}

func (s *tlsCertStep) Inspect(ctx context.Context) (bool, error) {
	if err := s.decide(ctx); err != nil {
		return false, err
	}
	return bytes.Equal(s.cur, s.want), nil
}

func (s *tlsCertStep) Apply(ctx context.Context) error { return s.hostWrite(ctx, s.want) }

func (s *tlsCertStep) Verify(ctx context.Context) error {
	if err := s.decide(ctx); err != nil {
		return err
	}
	got, err := s.hostRead(ctx)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, s.want) {
		return fmt.Errorf("%s's copy of the chain %s serves differs from it", s.app, s.source)
	}
	return nil
}

// tlsCertStep returns app's tls-cert step, nil when its Compose file reads no copy. The
// chain comes from where the edge keeps it, through the cluster access apply already has:
// the nginx edge's TLS Secret (its tls.crt; the key is never kept), or NPM's fullchain.pem
// read in NPM's pod.
func (s *session) tlsCertStep(app string) (engine.Step, error) {
	if !s.copiesTLSChain(app) {
		return nil, nil
	}
	e, r, _ := tlsCertRoute(s.plan, app)
	host, ok := s.drivers[s.placed[app].Name].(sshDriver)
	if !ok {
		return nil, fmt.Errorf("%s copies its edge's certificate chain only on a Docker target", app)
	}
	cluster, ok := s.drivers[e.Target].(kubeDriver)
	if !ok {
		return nil, fmt.Errorf("%s: edge %s is not connected", app, e.Target)
	}
	st := &tlsCertStep{app: app, host: r.Hostname, now: time.Now,
		hostRead: func(ctx context.Context) ([]byte, error) {
			return dockerhost.ReadTLSChain(ctx, host.c, host.t.Root, app)
		},
		hostWrite: func(ctx context.Context, b []byte) error {
			return dockerhost.WriteTLSChain(ctx, host.c, host.t.Root, app, b)
		}}
	if r.Certificate != "" {
		st.issuer = edge.CertStepID(e, r.Certificate)
	}
	if e.Nginx() {
		secret := edge.TLSSecret(r)
		st.source = "the nginx edge's TLS Secret " + secret
		st.read = func(ctx context.Context) ([]byte, error) {
			b, err := cluster.c.TLSCertificate(ctx, secret)
			if err == nil && b == nil {
				err = errors.New("the Secret is absent")
			}
			return b, err
		}
		return st, nil
	}
	st.source = "NPM on " + e.Target
	st.read = func(ctx context.Context) ([]byte, error) {
		n, err := s.npmFor(ctx, e)
		if err != nil {
			return nil, err
		}
		file, err := n.ChainPath(ctx, e, r)
		if err != nil {
			return nil, err
		}
		ns, pod, container, err := cluster.c.AppPod(ctx, e.ClusterApp())
		if err != nil {
			return nil, err
		}
		return cluster.c.Exec(ctx, ns, pod, container, []string{"cat", file})
	}
	return st, nil
}

// issueFirst moves the edge step issuing a certificate a tls-cert step copies to just before
// that step: the edges' steps otherwise follow every app, and KyPost refuses to start without
// its copy. The edge apps deploy first (operatorsFirst), so their certificates can be issued.
func issueFirst(steps []engine.Step) []engine.Step {
	for i := 0; i < len(steps); i++ {
		t, ok := steps[i].(*tlsCertStep)
		if !ok || t.issuer == "" {
			continue
		}
		if j := slices.IndexFunc(steps, func(s engine.Step) bool { return s.ID() == t.issuer }); j > i {
			issue := steps[j]
			steps = slices.Insert(slices.Delete(steps, j, j+1), i, issue)
			i++
		}
	}
	return steps
}

// RefreshTLS copies again the public chain each installed KyDrive's and KyPost's edge serves,
// under the target locks, writing only what changed: the suite runner's daily job, which
// follows the edge's renewals. Its results sit beside apply's, so a failure is the app's last
// result in status until a later run passes.
func RefreshTLS(ctx context.Context, state, holder string, o Options) error {
	s, err := load(state, nil, o)
	if err != nil {
		return err
	}
	defer s.close()
	s.order = slices.DeleteFunc(s.order, func(a string) bool { return !s.copiesTLSChain(a) })
	if len(s.order) == 0 {
		return nil
	}
	s.targets = slices.DeleteFunc(s.targets, func(t stack.Target) bool {
		return !slices.ContainsFunc(s.order, func(a string) bool {
			e, _, _ := tlsCertRoute(s.plan, a)
			return s.placed[a].Name == t.Name || e.Target == t.Name
		})
	})
	s.edges = nil
	if err := s.connect(ctx, nil); err != nil {
		return err
	}
	return s.operate(ctx, holder, func(context.Context) ([]engine.Step, error) {
		var steps []engine.Step
		for _, a := range s.order {
			st, err := s.tlsCertStep(a)
			if err != nil {
				return nil, err
			}
			steps = append(steps, st)
		}
		return steps, nil
	}, nil)
}
