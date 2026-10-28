package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// recoveryPair is kypost.recovery-pair for KyPost's integration i on d, when the plan routes
// KyRecovery as internal; nil for any other app or plan.
func (s *session) recoveryPair(d driver, i stack.Integration) (engine.Step, error) {
	t, ok := s.placed["kyrecovery"]
	if i.Product != "kypost" || !ok || !privateRecovery(s.plan) || s.drivers[t.Name] == nil {
		return nil, nil
	}
	host, err := routeHost(s.plan, "kyrecovery")
	if err != nil {
		return nil, err
	}
	recovery, _, err := nativeProductExec(s.drivers[t.Name], stack.Integration{Product: "kyrecovery", Target: t.Name}, productAdapters["kyrecovery"])
	if err != nil {
		return nil, err
	}
	exec, input, err := nativeProductExec(d, i, productAdapters["kypost"])
	if err != nil {
		return nil, err
	}
	var key string
	for _, a := range s.plan.Apps {
		if a.Name == "kyrecovery" {
			key = a.RecoveryKeyID
		}
	}
	return recoveryPairStep{url: "https://" + host + "/", key: key, recovery: recovery, exec: exec, input: input, outcome: &s.postPairing}, nil
}

// recoveryPairStep (kypost.recovery-pair) pairs the catalog KyPost with the plan's KyRecovery
// during apply and waits for its first deposit there (#93 stage 2). It pairs only to the key
// the owner confirmed (kyrecovery's recovery_key_id), once KyRecovery's status reports that
// same key. Until then it passes with a note, and KyPost's handover says to pair by hand. The
// pairing code exists only in memory: minted in KyRecovery's container, handed to KyPost on
// stdin, never in state, output or an error.
type recoveryPairStep struct {
	url      string // KyRecovery's URL as KyPost records it, the one the handover prints
	key      string // the owner's recovery_key_id; "" until confirmed
	recovery identityExec
	exec     identityExec // KyPost's kypost-server
	input    func(context.Context, []string, []byte) ([]byte, error)
	outcome  *string // KyPost's handover line, set by Verify
}

// postRecoveryService is the service name KyPost claims with, which KyRecovery's status lists.
const postRecoveryService = "KyPost"

func (s recoveryPairStep) ID() string        { return "kypost.recovery-pair" }
func (s recoveryPairStep) InputHash() string { return s.url + " " + s.key }

// pairing is what KyRecovery reports for KyPost. wait, when set, is why apply does not pair.
type pairing struct {
	wait              string
	paired, deposited bool
}

const (
	waitStatus   = "Pair it with KyRecovery on its backup page (see KyRecovery's step): this KyRecovery cannot report its recovery key to apply, so apply does not pair it."
	waitConfirm  = "Pair it with KyRecovery on its backup page, or confirm KyRecovery's recovery key and apply pairs it (see KyRecovery's step)."
	waitCeremony = "Pair it with KyRecovery on its backup page after the ceremony, or rerun apply then: KyRecovery has no recovery key yet."
	postPaired   = "Apply paired it with KyRecovery, to the recovery key you confirmed, and its first backup is there; it deposits on its own schedule."
)

func (s recoveryPairStep) state(ctx context.Context) (pairing, error) {
	ok, err := recoveryStatusCapable(ctx, s.recovery)
	switch {
	case err != nil:
		return pairing{}, err
	case !ok:
		return pairing{wait: waitStatus}, nil
	case s.key == "":
		return pairing{wait: waitConfirm}, nil
	}
	st, err := readRecoveryStatus(ctx, s.recovery)
	if err != nil {
		return pairing{}, err
	}
	switch {
	case st.RecoveryKeyID == "":
		return pairing{wait: waitCeremony}, nil
	case st.RecoveryKeyID != s.key:
		return pairing{}, fmt.Errorf("KyRecovery's recovery key is %s, not stack.yaml's recovery_key_id %s, and apply pairs no product to a key you have not confirmed. Compare the ceremony page with the custodian cards and correct recovery_key_id", st.RecoveryKeyID, s.key)
	}
	var p pairing
	for _, a := range st.Paired {
		if a.ServiceName != postRecoveryService {
			continue
		}
		if a.RecoveryKeyID != s.key {
			return pairing{}, fmt.Errorf("KyPost is paired with KyRecovery to recovery key %s, not the confirmed %s; unpair it on both sides and rerun apply", a.RecoveryKeyID, s.key)
		}
		p.paired, p.deposited = true, p.deposited || a.LastBackupAt != ""
	}
	return p, nil
}

func (s recoveryPairStep) Inspect(ctx context.Context) (bool, error) {
	p, err := s.state(ctx)
	return p.wait != "" || p.deposited, err
}

// Apply pairs KyPost unless KyRecovery already lists it, by hand or by an earlier run, then
// deposits once. A rerun after a failure mints a new code; an unclaimed one expires.
func (s recoveryPairStep) Apply(ctx context.Context) error {
	p, err := s.state(ctx)
	if err != nil || p.wait != "" {
		return err
	}
	if !p.paired {
		if err := s.pair(ctx); err != nil {
			return err
		}
		// The claim pinned whatever key KyRecovery held then: check it before sealing to it.
		if p, err = s.state(ctx); err != nil {
			return err
		}
		if !p.paired {
			return errors.New("KyPost reports a pairing that KyRecovery does not list: unpair it on KyPost's backup page and rerun apply")
		}
	}
	out, err := s.exec(ctx, []string{"deposit"})
	if err != nil || !bytes.Contains(out, []byte(" deposited ")) || bytes.Contains(out, []byte("warning:")) {
		return errors.New("KyPost's first deposit did not reach KyRecovery: start a backup on KyPost's backup page to see why, then rerun apply. If KyPost says it is not paired, revoke KyPost on KyRecovery's pairings page first")
	}
	return nil
}

var pairingCode = regexp.MustCompile(`(?m)^Pairing Code: ([0-9]{6})\r?$`)

func (s recoveryPairStep) pair(ctx context.Context) error {
	out, err := s.recovery(ctx, []string{"pair", "generate", "--ttl", "10", "--service", postRecoveryService, "--app", postRecoveryService})
	defer clear(out)
	m := pairingCode.FindSubmatch(out)
	if err != nil || m == nil {
		return errors.New("KyRecovery's pair generate printed no pairing code")
	}
	bundle, err := json.Marshal(map[string]any{"version": 1, "recovery": map[string]string{"url": s.url, "code": string(m[1])}})
	defer clear(bundle)
	if err != nil {
		return err
	}
	b, err := s.input(ctx, []string{"apply-setup", "--file", "-"}, bundle)
	var r struct{ Applied, Unchanged []string }
	if err != nil || json.Unmarshal(b, &r) != nil || !slices.Contains(append(r.Applied, r.Unchanged...), "recovery-pairing") {
		return fmt.Errorf("KyPost did not pair with KyRecovery at %s. An expired or used code fails here, and a rerun mints a new one; KyPost refuses a KyRecovery URL other than the one it is paired with", s.url)
	}
	return nil
}

func (s recoveryPairStep) Verify(ctx context.Context) error {
	p, err := s.state(ctx)
	switch {
	case err != nil:
		return err
	case p.wait != "":
		*s.outcome = p.wait
	case !p.deposited:
		return errors.New("KyRecovery holds no backup from KyPost yet")
	default:
		*s.outcome = postPaired
	}
	return nil
}

func (s recoveryPairStep) Note() string { return *s.outcome }

// recoveryStatus is KyRecovery's `status --json` (kyrecovery.status/1, advertised as setup
// capability status): its current recovery key, "" before the ceremony, and each paired
// service's key and last deposit. The installer reads only these fields.
type recoveryStatus struct {
	Schema        string `json:"schema"`
	RecoveryKeyID string `json:"recovery_key_id"`
	Paired        []struct {
		ServiceName   string `json:"service_name"`
		RecoveryKeyID string `json:"recovery_key_id"`
		LastBackupAt  string `json:"last_backup_at"`
	} `json:"paired"`
}

var keyID = regexp.MustCompile(`^[0-9a-f]{64}$`)

func readRecoveryStatus(ctx context.Context, exec identityExec) (recoveryStatus, error) {
	var st recoveryStatus
	b, err := exec(ctx, []string{"status", "--json"})
	if err != nil {
		return st, err
	}
	if len(b) > 128<<10 || json.Unmarshal(b, &st) != nil || st.Schema != "kyrecovery.status/1" || st.RecoveryKeyID != "" && !keyID.MatchString(st.RecoveryKeyID) || len(st.Paired) > 256 {
		return st, errors.New("invalid KyRecovery status report")
	}
	for _, p := range st.Paired {
		if !reportText(p.ServiceName, true) || !keyID.MatchString(p.RecoveryKeyID) || !reportText(p.LastBackupAt, false) {
			return st, errors.New("invalid KyRecovery status report")
		}
	}
	return st, nil
}

// recoveryStatusCapable reports whether KyRecovery advertises the status command.
func recoveryStatusCapable(ctx context.Context, exec identityExec) (bool, error) {
	b, err := exec(ctx, []string{"apply-setup", "--capabilities"})
	if err != nil {
		return false, err
	}
	var caps struct {
		Schema, Service string
		Features        []string
	}
	if len(b) > 128<<10 || json.Unmarshal(b, &caps) != nil || caps.Schema != "ky.setup-capabilities/1" || caps.Service != "kyrecovery" {
		return false, errors.New("KyRecovery does not advertise the required setup contract")
	}
	return slices.Contains(caps.Features, "status"), nil
}
