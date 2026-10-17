package edge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// KyDNS records carry no owner, so the records this installation created are listed in a
// ledger on the target. A record is ours only while the ledger names its ID, name and value.
type ledgerEntry struct {
	Server string `json:"server"`
	ID     int64  `json:"id"`
	Name   string `json:"name"` // KyDNS's form: lowercase with a trailing dot
	Value  string `json:"value"`
}

type dnsRecord struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
	View  string `json:"view"`
}

var (
	kydnsToken = regexp.MustCompile(`^kydns_[!-~]+$`) // printable, no spaces; EdgeSecret bounds the length
	errCode    = regexp.MustCompile(`^[a-z_]{1,64}$`)
)

type kydns struct {
	base, token string
	http        *http.Client
}

// fqdn is a hostname in KyDNS's stored form.
func fqdn(name string) string { return strings.ToLower(strings.TrimSuffix(name, ".")) + "." }

// do sends one KyDNS API request. A refusal carries KyDNS's error code, never its body.
func (k *kydns) do(ctx context.Context, method, path string, body, result any) (int, string, error) {
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, k.base+path, bytes.NewReader(data))
	if err != nil {
		return 0, "", errors.New("KyDNS request construction failed")
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := k.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, "", ctx.Err()
		}
		return 0, "", fmt.Errorf("KyDNS %s request failed: %w", method, engine.ErrTransient)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return res.StatusCode, "", errors.New("KyDNS response is unreadable or exceeds 4 MiB")
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		if result != nil && json.Unmarshal(raw, result) != nil {
			return res.StatusCode, "", errors.New("KyDNS response is invalid")
		}
		return res.StatusCode, "", nil
	}
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) != nil || !errCode.MatchString(e.Error.Code) {
		e.Error.Code = ""
	}
	code := e.Error.Code
	switch {
	case res.StatusCode == 401:
		return 401, code, errors.New("KyDNS rejects the lan_dns API token")
	case code == "read_only_replica":
		return res.StatusCode, code, fmt.Errorf("KyDNS at %s is a read-only replica; point lan_dns.url at its primary", k.base)
	case res.StatusCode == 429 || res.StatusCode >= 500:
		return res.StatusCode, code, fmt.Errorf("KyDNS HTTP %d: %w", res.StatusCode, engine.ErrTransient)
	}
	return res.StatusCode, code, fmt.Errorf("KyDNS HTTP %d (%s)", res.StatusCode, code)
}

// dnsStep keeps one A record per route hostname, pointing at the edge, and deletes the ones
// it created for hostnames that left the routes.
type dnsStep struct {
	n *NPM
	e stack.Edge
}

func (s dnsStep) ID() string { return s.e.Target + ".edge.lan-dns" }
func (s dnsStep) InputHash() string {
	b, _ := json.Marshal(struct {
		Installation string
		DNS          stack.LANDNS
		Routes       []stack.Route
	}{s.n.installation, *s.e.LANDNS, s.e.Routes})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s dnsStep) client(ctx context.Context) (*kydns, error) {
	d := s.e.LANDNS
	b, err := s.n.Secrets.EdgeSecret(ctx, d.Credentials.Secret, d.Credentials.Key)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(string(b))
	s.n.Redact.Add(token)
	if !kydnsToken.MatchString(token) {
		return nil, fmt.Errorf("secret kyq-edge/%s key %s is not a KyDNS API token", d.Credentials.Secret, d.Credentials.Key)
	}
	return &kydns{base: strings.TrimSuffix(d.URL, "/"), token: token, http: s.n.lan}, nil
}

type dnsPlan struct {
	ledger []ledgerEntry // without entries whose record is gone or differs
	stale  bool          // the stored ledger had such entries
	remove []ledgerEntry
	create []string
}

func (p dnsPlan) done() bool { return !p.stale && len(p.remove) == 0 && len(p.create) == 0 }

// plan reads KyDNS and the ledger and refuses any name another owner already publishes.
func (s dnsStep) plan(ctx context.Context, k *kydns) (dnsPlan, error) {
	var settings struct {
		Zone string `json:"private_domain"`
	}
	var services struct {
		Services []struct {
			Name    string   `json:"name"`
			Aliases []string `json:"aliases"`
		} `json:"services"`
	}
	var records struct {
		Records []dnsRecord `json:"records"`
	}
	for _, get := range []struct {
		path string
		out  any
	}{{"/api/v1/settings", &settings}, {"/api/v1/services", &services}, {"/api/v1/records", &records}} {
		if _, _, err := k.do(ctx, http.MethodGet, get.path, nil, get.out); err != nil {
			return dnsPlan{}, err
		}
	}
	zone := fqdn(settings.Zone)
	for _, r := range s.e.Routes {
		if !strings.HasSuffix(fqdn(r.Hostname), "."+zone) {
			return dnsPlan{}, fmt.Errorf("route hostname %s is outside KyDNS's private zone %s, and KyDNS publishes only names inside it", r.Hostname, zone)
		}
	}
	taken := map[string]string{} // name -> what publishes it, for names that are not ours
	for _, svc := range services.Services {
		for _, n := range append([]string{svc.Name}, svc.Aliases...) {
			taken[fqdn(n+"."+zone)] = "service " + svc.Name
		}
	}
	stored, err := s.n.Secrets.LANDNSLedger(ctx)
	if err != nil {
		return dnsPlan{}, err
	}
	var ledger []ledgerEntry
	if len(stored) > 0 && json.Unmarshal(stored, &ledger) != nil {
		return dnsPlan{}, errors.New("the LAN DNS ledger in kyq-edge/kyq-credentials is unreadable; fix or remove its kydns_records key")
	}
	var p dnsPlan
	ours := map[string]ledgerEntry{}
	for _, en := range ledger {
		i := slices.IndexFunc(records.Records, func(r dnsRecord) bool { return r.ID == en.ID })
		switch {
		case en.Server != k.base:
			p.ledger = append(p.ledger, en)
		case i < 0 || fqdn(records.Records[i].Name) != en.Name || records.Records[i].Type != "A" || records.Records[i].Value != en.Value || records.Records[i].View != "":
			p.stale = true // gone or replaced: never ours again
		default:
			p.ledger = append(p.ledger, en)
			ours[en.Name] = en
		}
	}
	for _, r := range records.Records {
		if !slices.ContainsFunc(p.ledger, func(en ledgerEntry) bool { return en.Server == k.base && en.ID == r.ID }) {
			taken[fqdn(r.Name)] = r.Type + " record (#" + strconv.FormatInt(r.ID, 10) + ")"
		}
	}
	routed := map[string]bool{}
	for _, r := range s.e.Routes {
		name := fqdn(r.Hostname)
		routed[name] = true
		if what, ok := taken[name]; ok {
			return dnsPlan{}, fmt.Errorf("KyDNS already has %s for %s that this installation did not create; remove it in KyDNS (also if an interrupted run left it) or drop lan_dns", what, r.Hostname)
		}
		if en, ok := ours[name]; !ok || en.Value != s.e.LANDNS.Address {
			p.create = append(p.create, name)
		}
	}
	for name, en := range ours {
		if !routed[name] || en.Value != s.e.LANDNS.Address {
			p.remove = append(p.remove, en)
		}
	}
	return p, nil
}

func (s dnsStep) Inspect(ctx context.Context) (bool, error) {
	k, err := s.client(ctx)
	if err != nil {
		return false, err
	}
	p, err := s.plan(ctx, k)
	return p.done(), err
}

// Apply records every change in the ledger as soon as KyDNS confirms it.
func (s dnsStep) Apply(ctx context.Context) error {
	k, err := s.client(ctx)
	if err != nil {
		return err
	}
	p, err := s.plan(ctx, k)
	if err != nil {
		return err
	}
	ledger := p.ledger
	save := func() error {
		b, _ := json.Marshal(ledger)
		return s.n.Secrets.SetLANDNSLedger(ctx, b)
	}
	if p.stale {
		if err := save(); err != nil {
			return err
		}
	}
	for _, en := range p.remove {
		if status, _, err := k.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/records/%d", en.ID), nil, nil); err != nil && status != 404 {
			return err
		}
		ledger = slices.DeleteFunc(ledger, func(x ledgerEntry) bool { return x == en })
		if err := save(); err != nil {
			return err
		}
	}
	for _, name := range p.create {
		var created struct {
			ID int64 `json:"id"`
		}
		_, code, err := k.do(ctx, http.MethodPost, "/api/v1/records", dnsRecord{Name: name, Type: "A", Value: s.e.LANDNS.Address}, &created)
		switch {
		case code == "name_out_of_zone" || code == "name_is_apex" || code == "wildcard_unsupported":
			return fmt.Errorf("KyDNS refuses %s (%s): lan_dns publishes only names inside KyDNS's private zone", strings.TrimSuffix(name, "."), code)
		case err != nil:
			return err
		case created.ID < 1:
			return errors.New("KyDNS returned no record ID")
		}
		ledger = append(ledger, ledgerEntry{Server: k.base, ID: created.ID, Name: name, Value: s.e.LANDNS.Address})
		if err := save(); err != nil {
			return err
		}
	}
	return nil
}

func (s dnsStep) Verify(ctx context.Context) error {
	ok, err := s.Inspect(ctx)
	if err == nil && !ok {
		err = errors.New("KyDNS records differ from the routes")
	}
	return err
}
