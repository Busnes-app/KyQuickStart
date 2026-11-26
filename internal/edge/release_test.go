package edge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// Released NPM hosts keep serving with the same configuration, become foreign, and the next
// apply's removed-route step (the app left the routes) leaves them enabled. Another app's
// host and another installation's are not touched.
func TestNPMReleaseKeepsHostsServing(t *testing.T) {
	ctx := context.Background()
	// A lan edge: on frp a released host keeps the frp listener, which later applies refuse,
	// so the CLI refuses releasing there.
	e := testEdge()
	e.Provider, e.AllowCIDRs, e.ClientPort, e.ProxyPort, e.ConnectorCIDR = "lan", []string{"192.168.1.0/24"}, 443, 0, ""
	mine := func(id int, hostname, role string) host {
		h := enabledHost(id, testID, hostname)
		r := stack.Route{App: "chat", Role: role, Hostname: hostname}
		h.Advanced = strings.Replace(h.Advanced, "hello:"+hostname+"\n", routeMark(r), 1)
		h.Port = 8008
		return h
	}
	console, matrix := mine(1, "chat.example.com", ""), mine(2, "matrix.example.com", "matrix")
	legacy := legacyHost(3, "auth.example.com")
	legacy.Advanced = strings.Replace(legacy.Advanced, ":hello:", ":chat:auth:", 1)
	other := enabledHost(4, testID, "hello.example.com")
	theirs := enabledHost(5, otherID, "element.example.com")
	n, hosts := fakeHosts(t, []host{console, matrix, legacy, other, theirs})
	e.Routes = []stack.Route{e.Routes[0],
		{App: "chat", Hostname: "chat.example.com"}, {App: "chat", Role: "matrix", Hostname: "matrix.example.com"},
		{App: "chat", Role: "auth", Hostname: "auth.example.com"}, {App: "chat", Role: "chat", Hostname: "element.example.com"}}

	steps := n.Release(e, "chat")
	if out, err := runSteps(t, steps); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got := hosts()
	for i, before := range []host{console, matrix, legacy} {
		h := got[i]
		_, want, _ := strings.Cut(before.Advanced, "\n")
		_, have, _ := strings.Cut(h.Advanced, "\n")
		if !h.Enabled || h.Port != before.Port || h.Host != before.Host || h.Certificate != before.Certificate || have != want {
			t.Errorf("%s changed: %+v", before.Domains[0], h.hostConfig)
		}
		if n.claim(h, e, "") != foreign || h.Meta["ky.managed-by"] != "" || h.Meta["ky.installation"] != "" || !strings.HasPrefix(h.Advanced, "# released by kyquickstart installation "+testID+" (chat)") {
			t.Errorf("%s still ours: %q %v", before.Domains[0], strings.SplitN(h.Advanced, "\n", 2)[0], h.Meta)
		}
	}
	if got[3].Advanced != other.Advanced || got[3].Meta["ky.managed-by"] != "kyquickstart" || got[4].Advanced != theirs.Advanced {
		t.Fatal("another app's or installation's host was released")
	}
	if done, err := steps[0].Inspect(ctx); !done || err != nil {
		t.Fatalf("rerun not a no-op: %v %v", done, err)
	}

	// The app leaves the plan: the next apply disables nothing it released.
	e.Routes = e.Routes[:1]
	if out, err := runSteps(t, n.Steps(e)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, h := range hosts()[:3] {
		if !h.Enabled {
			t.Errorf("released host %s disabled by a later apply", h.Domains[0])
		}
	}
}

// Released nginx-edge files keep their names and configuration, so an enabled route stays
// enabled, and a later apply without the app's routes does not disable them.
func TestNginxReleaseKeepsFilesActive(t *testing.T) {
	n, f, e := nginxFixture(t)
	if out, err := runSteps(t, n.Steps(e)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	key := routeKey("other.example.test")
	before := f.files[key+enabledExt]
	// The pods do not load the release: it fails, and a rerun is not done until they have.
	f.stale = true
	if _, err := runSteps(t, n.Release(e, "other")); err == nil {
		t.Fatal("release passed while no pod loaded it")
	}
	if done, err := n.Release(e, "other")[0].Inspect(context.Background()); done || err != nil {
		t.Fatalf("rerun after an unloaded release reported done: %v", err)
	}
	f.stale = false
	if out, err := runSteps(t, n.Release(e, "other")); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	file, on := f.files[key+enabledExt]
	_, want, _ := strings.Cut(before, "\n")
	if !on || !strings.HasPrefix(file, "# released by kyquickstart installation "+testID+" (other)") || !strings.HasSuffix(file, want) {
		t.Fatalf("released file: %q", file)
	}
	if f.files[routeKey("hello.example.test")+enabledExt] != n.want(e, e.Routes[0]) || f.loaded != NginxGeneration(f.files) {
		t.Fatal("another route changed, or the pods were not waited for")
	}
	e.Routes = e.Routes[:1]
	if out, err := runSteps(t, n.Steps(e)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, on := f.files[key+enabledExt]; !on {
		t.Fatalf("a later apply disabled the released route: %v", f.files)
	}
}

// Released LAN DNS records leave the ledger without a KyDNS call, and the next apply without
// the app's routes deletes none of them.
func TestLANDNSReleaseKeepsRecords(t *testing.T) {
	ctx := context.Background()
	f := &fakeKyDNS{zone: "example.com", next: 100}
	n, secrets, e := f.serve(t)
	if err := (dnsStep{n: n.dns(), e: e}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	writes := len(f.mutations)
	if out, err := runSteps(t, n.Release(e, "notes")[1:]); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var ledger []ledgerEntry
	json.Unmarshal(secrets.ledger, &ledger)
	if len(f.mutations) != writes || len(ledger) != 1 || ledger[0].Name != "hello.k80.example.com." {
		t.Fatalf("mutations %q, ledger %+v", f.mutations[writes:], ledger)
	}
	e.Routes = e.Routes[:1]
	if err := (dnsStep{n: n.dns(), e: e}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if f.names()["notes.k80.example.com."] != "192.168.1.81" || len(f.mutations) != writes {
		t.Fatalf("a later apply touched the released record: %v %q", f.names(), f.mutations[writes:])
	}
}
