package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/engine"
)

var (
	confirmedKey = strings.Repeat("a1", 32)
	otherKey     = strings.Repeat("b2", 32)
)

// fakePairing is KyRecovery's and KyPost's side of kypost.recovery-pair. A claim pairs KyPost
// to KyRecovery's key at that moment (claimKey, when set, is a key rotated in just before).
type fakePairing struct {
	status   bool   // KyRecovery advertises status
	key      string // KyRecovery's current recovery key
	claimKey string
	paired   string // the key KyPost is paired to in KyRecovery's status; "" unpaired
	backup   bool
	codes    []string // minted
	claimErr bool
	depErr   bool
	calls    []string
	stdin    []string
}

func (f *fakePairing) recovery(_ context.Context, args []string) ([]byte, error) {
	f.calls = append(f.calls, "kyrecovery "+strings.Join(args, " "))
	switch args[0] {
	case "apply-setup":
		features := []string{"kyidentity-oidc", "check"}
		if f.status {
			features = append(features, "status")
		}
		return json.Marshal(map[string]any{"schema": "ky.setup-capabilities/1", "service": "kyrecovery", "bundleSchema": "ky.product-setup/1", "features": features})
	case "status":
		paired := []map[string]string{{"service_name": "KyNotes", "recovery_key_id": otherKey, "last_backup_at": "2026-10-28T10:00:00Z"}}
		if f.paired != "" {
			last := ""
			if f.backup {
				last = "2026-10-28T12:00:00Z"
			}
			paired = append(paired, map[string]string{"service_name": "KyPost", "recovery_key_id": f.paired, "last_backup_at": last})
		}
		return json.Marshal(map[string]any{"schema": "kyrecovery.status/1", "recovery_key_id": f.key, "paired": paired})
	case "pair":
		if !slices.Equal(args, []string{"pair", "generate", "--ttl", "10", "--service", "KyPost", "--app", "KyPost"}) {
			return nil, errors.New("unexpected pair arguments")
		}
		code := []string{"481516", "234200"}[len(f.codes)%2]
		f.codes = append(f.codes, code)
		return []byte("=== PAIRING CODE GENERATED ===\nPairing Code: " + code + "\nExpires In:   10 minutes (12:10:00 UTC)\n"), nil
	}
	return nil, errors.New("unexpected KyRecovery command")
}

func (f *fakePairing) post(_ context.Context, args []string, stdin []byte) ([]byte, error) {
	f.calls = append(f.calls, "kypost "+strings.Join(args, " "))
	switch {
	case slices.Equal(args, []string{"apply-setup", "--file", "-"}):
		f.stdin = append(f.stdin, string(stdin))
		var b struct {
			Version  int
			Recovery struct{ URL, Code string }
		}
		if json.Unmarshal(stdin, &b) != nil || b.Version != 1 || b.Recovery.URL != "https://recovery.example.com/" || b.Recovery.Code != f.codes[len(f.codes)-1] || f.claimErr {
			return nil, errors.New("product setup command failed; inspect product logs locally")
		}
		f.paired = f.key
		if f.claimKey != "" {
			f.paired, f.key = f.claimKey, f.claimKey
		}
		return []byte(`{"version":1,"applied":["recovery-pairing"],"unchanged":[],"dns":[],"pending":[],"configurationApplied":true}`), nil
	case slices.Equal(args, []string{"deposit"}):
		if f.depErr || f.paired == "" {
			return nil, errors.New("product setup command failed; inspect product logs locally")
		}
		f.backup = true
		return []byte("capsule 7f3e (2048 bytes) deposited 2026-10-28T12:00:00Z\n"), nil
	}
	return nil, errors.New("unexpected KyPost command")
}

func (f *fakePairing) step(key string, outcome *string) recoveryPairStep {
	return recoveryPairStep{url: "https://recovery.example.com/", key: key, recovery: f.recovery,
		exec: func(ctx context.Context, args []string) ([]byte, error) { return f.post(ctx, args, nil) }, input: f.post, outcome: outcome}
}

// run is one apply of the step through the engine, as apply runs it.
func (f *fakePairing) run(t *testing.T, dir, key string) (string, string, error) {
	t.Helper()
	var out bytes.Buffer
	var outcome string
	err := (&engine.Engine{Dir: dir, Out: &out, Redact: &engine.Redactor{}}).Run(context.Background(), []engine.Step{f.step(key, &outcome)})
	return out.String(), outcome, err
}

func (f *fakePairing) minted() bool {
	return slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "kyrecovery pair") })
}

// Until KyRecovery reports its key and the owner has confirmed it, the step passes with the
// reason as KyPost's handover line and touches nothing: no code is minted.
func TestRecoveryPairWaits(t *testing.T) {
	for _, c := range []struct {
		name string
		f    fakePairing
		key  string
		want string
	}{
		{"no status", fakePairing{key: confirmedKey}, confirmedKey, waitStatus},
		{"unconfirmed", fakePairing{status: true, key: confirmedKey}, "", waitConfirm},
		{"before the ceremony", fakePairing{status: true}, confirmedKey, waitCeremony},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			out, outcome, err := c.f.run(t, dir, c.key)
			if err != nil || outcome != c.want || !strings.Contains(out, "kypost.recovery-pair ok: "+c.want) || c.f.minted() {
				t.Fatalf("err %v, outcome %q, minted %v, output:\n%s", err, outcome, c.f.minted(), out)
			}
			if !strings.HasPrefix(c.want, "Pair it with KyRecovery on its backup page") {
				t.Errorf("waiting line %q does not tell the owner to pair by hand", c.want)
			}
			if c.f.key == "" {
				// The ceremony ran since: the next apply pairs, though no input changed.
				c.f.key = confirmedKey
				if _, outcome, err := c.f.run(t, dir, c.key); err != nil || outcome != postPaired || len(c.f.codes) != 1 {
					t.Fatalf("after the ceremony: err %v, outcome %q, codes %v", err, outcome, c.f.codes)
				}
			}
		})
	}
}

func TestRecoveryPair(t *testing.T) {
	dir := t.TempDir()
	f := &fakePairing{status: true, key: confirmedKey}
	out, outcome, err := f.run(t, dir, confirmedKey)
	if err != nil || outcome != postPaired || !f.backup || len(f.codes) != 1 {
		t.Fatalf("err %v, outcome %q, backup %v, codes %v:\n%s", err, outcome, f.backup, f.codes, out)
	}
	// The code travels on KyPost's stdin only: never in an argument, the output or a result.
	results, _ := os.ReadFile(filepath.Join(dir, "kypost.recovery-pair.json"))
	for _, leak := range append(slices.Clone(f.calls), out, outcome, string(results)) {
		if strings.Contains(leak, f.codes[0]) {
			t.Errorf("pairing code in %q", leak)
		}
	}
	if len(f.stdin) != 1 || !strings.Contains(f.stdin[0], `"code":"`+f.codes[0]+`"`) {
		t.Errorf("KyPost's stdin = %q", f.stdin)
	}
	// A rerun reads KyRecovery's status and leaves both products alone.
	f.calls = nil
	out, outcome, err = f.run(t, dir, confirmedKey)
	if err != nil || !strings.Contains(out, "kypost.recovery-pair skipped") || outcome != postPaired || f.minted() || slices.Contains(f.calls, "kypost deposit") {
		t.Fatalf("rerun: err %v, outcome %q, calls %v:\n%s", err, outcome, f.calls, out)
	}
}

// Apply may die anywhere: after a failed claim the rerun mints a new code; after the claim,
// it only deposits; a product paired by hand is not paired again.
func TestRecoveryPairResumes(t *testing.T) {
	dir := t.TempDir()
	f := &fakePairing{status: true, key: confirmedKey, claimErr: true}
	if _, _, err := f.run(t, dir, confirmedKey); err == nil || strings.Contains(err.Error(), f.codes[0]) || !strings.Contains(err.Error(), "a rerun mints a new one") {
		t.Fatalf("failed claim: %v", err)
	}
	f.claimErr, f.depErr = false, true
	if _, _, err := f.run(t, dir, confirmedKey); err == nil || f.paired != confirmedKey || len(f.codes) != 2 || f.codes[0] == f.codes[1] {
		t.Fatalf("failed deposit: err %v, paired %q, codes %v", err, f.paired, f.codes)
	}
	f.depErr, f.calls = false, nil
	if _, outcome, err := f.run(t, dir, confirmedKey); err != nil || outcome != postPaired || f.minted() || !slices.Contains(f.calls, "kypost deposit") {
		t.Fatalf("resume: err %v, outcome %q, calls %v", err, outcome, f.calls)
	}

	byHand := &fakePairing{status: true, key: confirmedKey, paired: confirmedKey}
	if _, outcome, err := byHand.run(t, t.TempDir(), confirmedKey); err != nil || outcome != postPaired || byHand.minted() || len(byHand.stdin) != 0 {
		t.Fatalf("paired by hand: err %v, outcome %q, calls %v", err, outcome, byHand.calls)
	}
}

// Nothing is sealed to a key the owner did not confirm: a KyRecovery holding another key, a
// KyPost paired to another, or a key rotated in during the claim each fail before a deposit.
func TestRecoveryPairRefusesUnconfirmedKey(t *testing.T) {
	for _, c := range []struct {
		name string
		f    fakePairing
		want string
	}{
		{"KyRecovery's key", fakePairing{status: true, key: otherKey}, "not stack.yaml's recovery_key_id"},
		{"KyPost's pairing", fakePairing{status: true, key: confirmedKey, paired: otherKey}, "KyPost is paired with KyRecovery to recovery key " + otherKey},
		{"rotated during the claim", fakePairing{status: true, key: confirmedKey, claimKey: otherKey}, "not stack.yaml's recovery_key_id"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := c.f.run(t, t.TempDir(), confirmedKey)
			if err == nil || !strings.Contains(err.Error(), c.want) || slices.Contains(c.f.calls, "kypost deposit") {
				t.Fatalf("err %v, calls %v", err, c.f.calls)
			}
			if c.f.claimKey == "" && c.f.minted() {
				t.Error("minted a code for an unconfirmed key")
			}
		})
	}
}

func TestReadRecoveryStatus(t *testing.T) {
	for _, bad := range []string{
		`{"schema":"kyrecovery.status/2","recovery_key_id":"","paired":[]}`,
		`{"schema":"kyrecovery.status/1","recovery_key_id":"A1","paired":[]}`,
		`{"schema":"kyrecovery.status/1","recovery_key_id":"","paired":[{"service_name":"","recovery_key_id":"` + confirmedKey + `"}]}`,
		`{"schema":"kyrecovery.status/1","recovery_key_id":"","paired":[{"service_name":"KyPost","recovery_key_id":"x"}]}`,
		`{"schema":"kyrecovery.status/1","recovery_key_id":"","paired":[{"service_name":"KyPost\n","recovery_key_id":"` + confirmedKey + `"}]}`,
		`not json`,
	} {
		exec := func(context.Context, []string) ([]byte, error) { return []byte(bad), nil }
		if _, err := readRecoveryStatus(context.Background(), exec); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestPairingLines(t *testing.T) {
	handover := "\nKyPost:\n  " + pairByApply + "\nKyNotes:\n  " + noPairing + "\n"
	for post, want := range map[string]string{"": "\nKyPost:\n  " + pairByHand + "\nKyNotes:\n  " + pairByHand + "\n", postPaired: "\nKyPost:\n  " + postPaired + "\nKyNotes:\n  " + pairByHand + "\n"} {
		if got := pairingLines(handover, post); got != want {
			t.Errorf("pairingLines(%q) = %q", post, got)
		}
	}
}

// With an internal KyRecovery in the plan, kypost.recovery-pair runs after every edge step,
// since KyPost reaches KyRecovery through its route, and KyPost's handover line waits for it.
func TestRecoveryPairAfterEdges(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	postPlan(t, f, "      clients: []\n")
	path := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(path)
	y := strings.Replace(string(b), "  - name: kypost\n    target: nas\n", "  - name: kypost\n    target: nas\n  - name: kyrecovery\n    target: k1\n    recovery_key_id: "+confirmedKey+"\n", 1)
	y = strings.Replace(y, "integrations:", "      - app: kyrecovery\n        hostname: recovery.example.com\n        forward_host: app.kyq-kyrecovery.svc.cluster.local\n        forward_port: 8095\n        certificate_id: 1\n        exposure: internal\nintegrations:", 1)
	os.WriteFile(path, []byte(y), 0o600)
	s := postSession(t, f, &fakeIdentity{}, &fakePost{files: map[string]string{}})
	s.edges = s.plan.Edges
	var err error
	if s.npms["k1"], err = edge.NewNPM("kube://kyq-npm/app:81", "token", "test-installation", func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("no NPM in this test")
	}); err != nil {
		t.Fatal(err)
	}
	list, err := s.deploySteps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	order := ids(list)
	pair, lastEdge := slices.Index(order, "kypost.recovery-pair"), -1
	for k, id := range order {
		if strings.HasPrefix(id, "k1.edge.") {
			lastEdge = k
		}
	}
	if pair < 0 || lastEdge < 0 || pair < lastEdge || list[pair].InputHash() != "https://recovery.example.com/ "+confirmedKey {
		t.Fatalf("steps = %v", order)
	}
	if !strings.Contains(s.handover, "\n  "+pairByApply+"\n") || strings.Contains(s.handover, noPairing) {
		t.Fatalf("handover:\n%s", s.handover)
	}
}
