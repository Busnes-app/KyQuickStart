package offboard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrAuditPending = errors.New("audit delivery remains pending")

type auditRetry struct{ after time.Duration }

func (*auditRetry) Error() string { return ErrAuditPending.Error() }
func (*auditRetry) Unwrap() error { return ErrAuditPending }

var auditFileName = regexp.MustCompile(`^([0-9a-f]{64})\.audit-([0-9]{10})-(started|result)$`)

// AuditDestination holds an ingestion-only paired source credential in memory.
type AuditDestination struct {
	origin, token string
	client        *http.Client
	requireAlerts bool
}

func (AuditDestination) String() string     { return "private audit destination" }
func (d AuditDestination) GoString() string { return d.String() }

func auditOrigin(origin string) bool {
	if !https(origin, true) {
		return false
	}
	u, _ := url.Parse(origin)
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return false
		}
	}
	return true
}

func NewAuditDestination(origin, token string) (*AuditDestination, error) {
	if !auditOrigin(origin) || token == "" || len(token) > 128 || strings.IndexFunc(token, func(r rune) bool { return r <= 32 || r >= 127 }) >= 0 {
		return nil, ErrState
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxResponseHeaderBytes = 8192
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	return &AuditDestination{origin: origin, token: token, client: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func oneHeader(h http.Header, key string, want string) bool {
	values := h.Values(key)
	return len(values) == 1 && values[0] == want
}
func (d *AuditDestination) request(ctx context.Context, method, body string, sequence int64, digest string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, method, d.origin+"/api/ingest/logs", strings.NewReader(body))
	if err != nil {
		return "", ErrState
	}
	req.Header.Set("Authorization", "Bearer "+d.token)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-ndjson")
		req.Header.Set("Ky-Log-Sequence", strconv.FormatInt(sequence, 10))
	}
	response, err := d.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrAuditPending
	}
	defer response.Body.Close()
	// Never consume a server's diagnostic body into logs or durable state.
	if response.StatusCode == 429 || response.StatusCode >= 500 {
		delay := 30 * time.Second
		if response.StatusCode == 429 {
			delay = time.Minute
		}
		if values := response.Header.Values("Retry-After"); len(values) == 1 {
			seconds, err := strconv.Atoi(values[0])
			if err == nil && seconds >= 1 && seconds <= 3600 && strconv.Itoa(seconds) == values[0] {
				delay = max(delay, time.Duration(seconds)*time.Second)
			}
		}
		return "", &auditRetry{after: delay}
	}
	if response.StatusCode != 204 || !oneHeader(response.Header, "Ky-Log-Receipt", "ky.log-ingest/1") {
		return "", ErrState
	}
	if method == http.MethodOptions {
		if d.requireAlerts && !oneHeader(response.Header, "Ky-Log-Alerts", "ky.offboarding-alerts/1") {
			return "", ErrState
		}
		values := response.Header.Values("Ky-Log-Source")
		if len(values) != 1 || !setupID.MatchString(values[0]) {
			return "", ErrState
		}
		return values[0], nil
	}
	if !oneHeader(response.Header, "Ky-Log-Sequence", strconv.FormatInt(sequence, 10)) || !oneHeader(response.Header, "Ky-Log-Digest", digest) {
		return "", ErrState
	}
	return "", nil
}

type auditHead struct {
	Schema   string    `json:"schema"`
	Origin   string    `json:"origin"`
	Source   string    `json:"source"`
	SetupID  string    `json:"setup_id"`
	Sequence int64     `json:"sequence"`
	Sent     string    `json:"sent"`
	Pending  string    `json:"pending"`
	Body     string    `json:"body"`
	Digest   string    `json:"digest"`
	Attempts uint32    `json:"attempts"`
	Next     time.Time `json:"next"`
}
type auditAcknowledgment struct {
	Schema   string `json:"schema"`
	Source   string `json:"source"`
	Sequence int64  `json:"sequence"`
	Digest   string `json:"digest"`
}

const auditHeadFile = "pulse-state.json"

func (s *Store) auditBody(name string, targets map[string]*Target) (string, error) {
	parts := auditFileName.FindStringSubmatch(name)
	if parts == nil {
		return "", ErrState
	}
	raw, err := readPrivate(s.root, name, 16384)
	if err != nil || !uniqueJSON(raw) {
		return "", ErrState
	}
	var a Audit
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&a) != nil || d.Decode(new(any)) != io.EOF || a.Schema != "ky.offboarding-audit/1" || a.Binding != parts[1] || a.Attempt == 0 || a.Time.IsZero() || name != auditName(a.Binding, a.Attempt, parts[3]) ||
		(parts[3] == "started" && a.Outcome != "started") || (parts[3] == "result" && a.Outcome != "success" && a.Outcome != "failed") {
		return "", ErrState
	}
	b, err := s.Binding(a.Binding)
	if err != nil || !targets[b.App].matches(b) {
		return "", ErrState
	}
	if parts[3] == "result" {
		var started Audit
		if s.read(auditName(a.Binding, a.Attempt, "started"), &started) != nil || started.Schema != a.Schema || started.Binding != a.Binding || started.Attempt != a.Attempt || started.Outcome != "started" || started.Time.IsZero() || a.Time.Before(started.Time) {
			return "", ErrState
		}
	}
	hash := sha256.Sum256(raw)
	seq := int64(a.Attempt) * 2
	if parts[3] == "started" {
		seq--
	}
	line, err := json.Marshal(struct {
		App      string   `json:"app"`
		Seq      int64    `json:"seq"`
		Hash     string   `json:"hash"`
		Fields   []string `json:"fields"`
		Action   string   `json:"action"`
		Outcome  string   `json:"outcome"`
		UserID   string   `json:"user_id"`
		Resource string   `json:"resource"`
		Message  string   `json:"message"`
	}{"kyoffboard", seq, hex.EncodeToString(hash[:]), []string{}, "offboarding." + a.Outcome, a.Outcome, "kyoffboard", b.App + ":" + a.Binding, "Product access revocation " + a.Outcome})
	if err != nil {
		return "", ErrState
	}
	body, err := json.Marshal(struct {
		Line string    `json:"line"`
		Time time.Time `json:"time"`
	}{string(line), a.Time.UTC()})
	if err != nil {
		return "", ErrState
	}
	return string(body) + "\n", nil
}
func bodyDigest(body string) string {
	hash := sha256.Sum256([]byte(body))
	return hex.EncodeToString(hash[:])
}

func (s *Store) auditAck(name, source string, sequence int64, targets map[string]*Target) error {
	if sequence <= 0 {
		return ErrState
	}
	body, err := s.auditBody(name, targets)
	if err != nil {
		return ErrState
	}
	var ack auditAcknowledgment
	if s.read(name+".sent", &ack) != nil || ack != (auditAcknowledgment{"ky.offboarding-receipt/1", source, sequence, bodyDigest(body)}) {
		return ErrState
	}
	return nil
}
func (s *Store) loadAuditHead(destination *AuditDestination, source, installation string, targets map[string]*Target) (auditHead, error) {
	var head auditHead
	err := s.read(auditHeadFile, &head)
	if errors.Is(err, os.ErrNotExist) {
		head = auditHead{Schema: "ky.offboarding-relay/1", Origin: destination.origin, Source: source, SetupID: installation}
		return head, s.write(auditHeadFile, head)
	}
	if err != nil || head.Schema != "ky.offboarding-relay/1" || head.Origin != destination.origin || head.Source != source || head.SetupID != installation || head.Sequence < 0 {
		return head, ErrState
	}
	prior := head.Sequence
	if head.Pending != "" {
		prior--
		body, err := s.auditBody(head.Pending, targets)
		if err != nil || head.Sequence <= 0 || body != head.Body || bodyDigest(body) != head.Digest || (head.Attempts > 0 && head.Next.IsZero()) {
			return head, ErrState
		}
	} else if head.Body != "" || head.Digest != "" || head.Attempts != 0 || !head.Next.IsZero() {
		return head, ErrState
	}
	if prior == 0 {
		if head.Sent != "" {
			return head, ErrState
		}
	} else if prior < 0 || s.auditAck(head.Sent, source, prior, targets) != nil {
		return head, ErrState
	}
	return head, nil
}

// deliverAudit persists the attempt before I/O, then the receipt before advancing.
func (s *Store) deliverAudit(ctx context.Context, destination *AuditDestination, head auditHead, targets map[string]*Target, now time.Time) error {
	var prior auditAcknowledgment
	err := s.read(head.Pending+".sent", &prior)
	ack := auditAcknowledgment{"ky.offboarding-receipt/1", head.Source, head.Sequence, head.Digest}
	if err == nil {
		if prior != ack {
			return ErrState
		}
	} else {
		if !errors.Is(err, os.ErrNotExist) {
			return ErrState
		}
		if now.Before(head.Next) {
			return ErrAuditPending
		}
		if head.Attempts == ^uint32(0) {
			return ErrState
		}
		head.Attempts++
		head.Next = now.UTC().Add(15*time.Second + backoff(head.Attempts))
		if s.write(auditHeadFile, head) != nil {
			return ErrState
		}
		_, err = destination.request(ctx, http.MethodPost, head.Body, head.Sequence, head.Digest)
		if err != nil {
			delay := backoff(head.Attempts)
			var retry *auditRetry
			if errors.As(err, &retry) {
				delay = max(delay, retry.after)
			}
			head.Next = time.Now().UTC().Add(delay)
			if s.write(auditHeadFile, head) != nil {
				return ErrState
			}
			return err
		}
		if s.publish(head.Pending+".sent", ack) != nil {
			return ErrState
		}
	}
	head.Sent = head.Pending
	head.Pending, head.Body, head.Digest = "", "", ""
	head.Attempts = 0
	head.Next = time.Time{}
	return s.write(auditHeadFile, head)
}

// ServeAudit is one ordered relay per store, independent of revocation deliveries.
// Cancel and join before closing Store. Historical audit files are retained.
func (s *Store) ServeAudit(ctx context.Context, destination *AuditDestination, targets []*Target) error {
	if destination == nil || !s.audit.TryLock() {
		return ErrState
	}
	defer s.audit.Unlock()
	defer destination.client.CloseIdleConnections()
	byApp := map[string]*Target{}
	installation := ""
	for _, target := range targets {
		if target == nil || !setupID.MatchString(target.spec.SetupID) || byApp[target.spec.App] != nil || (installation != "" && installation != target.spec.SetupID) {
			return ErrState
		}
		installation = target.spec.SetupID
		byApp[target.spec.App] = target
	}
	if len(byApp) == 0 || len(byApp) > 32 {
		return ErrState
	}
	var source string
	for {
		var err error
		source, err = destination.request(ctx, http.MethodOptions, "", 0, "")
		if err == nil {
			break
		}
		if !errors.Is(err, ErrAuditPending) {
			return err
		}
		delay := 30 * time.Second
		var retry *auditRetry
		if errors.As(err, &retry) {
			delay = max(delay, retry.after)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	directory, err := s.root.Open(".")
	if err != nil {
		return ErrState
	}
	defer func() { directory.Close() }()
	// ponytail: the existing flat outbox is scanned in bounded pages; split it
	// only if retained audit growth exceeds the deployment's delivery latency budget.
	tick := time.NewTicker(1100 * time.Millisecond)
	defer tick.Stop()
	var acknowledged int64
	var candidate string
	refresh := false
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		head, err := s.loadAuditHead(destination, source, installation, byApp)
		if err != nil {
			return ErrState
		}
		if head.Pending == "" {
			if refresh {
				// Btrfs can retain an open directory's old entry view after
				// rewind. Reopen at the next scan, after intervening writes.
				fresh, err := s.root.Open(".")
				if err != nil {
					return ErrState
				}
				directory.Close()
				directory = fresh
				refresh = false
			}
			entries, err := directory.ReadDir(1024)
			if err != nil && !errors.Is(err, io.EOF) {
				return ErrState
			}
			finished := errors.Is(err, io.EOF)
			for _, entry := range entries {
				name := entry.Name()
				if !strings.Contains(name, ".audit-") {
					continue
				}
				if strings.HasSuffix(name, ".sent") {
					var ack auditAcknowledgment
					base := strings.TrimSuffix(name, ".sent")
					if s.read(name, &ack) != nil || ack.Sequence > head.Sequence || s.auditAck(base, source, ack.Sequence, byApp) != nil {
						return ErrState
					}
					acknowledged++
					continue
				}
				if _, err := s.auditBody(name, byApp); err != nil {
					return ErrState
				}
				var ack auditAcknowledgment
				err = s.read(name+".sent", &ack)
				if err == nil {
					if ack.Sequence > head.Sequence || s.auditAck(name, source, ack.Sequence, byApp) != nil {
						return ErrState
					}
					continue
				}
				if !errors.Is(err, os.ErrNotExist) {
					return ErrState
				}
				parts := auditFileName.FindStringSubmatch(name)
				if parts[3] == "result" {
					attempt, _ := strconv.ParseUint(parts[2], 10, 32)
					var started auditAcknowledgment
					err := s.read(auditName(parts[1], uint32(attempt), "started")+".sent", &started)
					if errors.Is(err, os.ErrNotExist) {
						continue
					}
					if err != nil {
						return ErrState
					}
					if started.Sequence > head.Sequence || s.auditAck(auditName(parts[1], uint32(attempt), "started"), source, started.Sequence, byApp) != nil {
						return ErrState
					}
				}
				if candidate == "" {
					candidate = name
				}
			}
			if finished {
				if acknowledged != head.Sequence {
					return ErrState
				}
				refresh = true
				if candidate != "" {
					if head.Sequence == math.MaxInt64 {
						return ErrState
					}
					body, err := s.auditBody(candidate, byApp)
					if err != nil {
						return ErrState
					}
					head.Sequence++
					head.Pending = candidate
					head.Body = body
					head.Digest = bodyDigest(body)
					if s.write(auditHeadFile, head) != nil {
						return ErrState
					}
				}
				candidate = ""
				acknowledged = 0
			}
		}
		if head.Pending != "" {
			err = s.deliverAudit(ctx, destination, head, byApp, time.Now())
			if err != nil && !errors.Is(err, ErrAuditPending) {
				return err
			}
			if err == nil {
				refresh = true
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
