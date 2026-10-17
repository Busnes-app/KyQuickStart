package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

const kydnsTok = "kydns_0123456789abcdefSECRET"

// fakeKyDNS serves KyDNS's admin API: names normalised with a trailing dot, records only
// inside the private zone, insert-only creation.
type fakeKyDNS struct {
	mu        sync.Mutex
	zone      string
	records   []dnsRecord
	services  []map[string]any
	next      int64
	refuse    bool // refuse every write as out of zone, as after a zone change
	mutations []string
}

func (f *fakeKyDNS) serve(t *testing.T) (*NPM, *fakeSecrets, stack.Edge) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+kydnsTok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.Method != "GET" {
			f.mutations = append(f.mutations, r.Method+" "+r.URL.Path+" "+string(body))
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/settings":
			fmt.Fprintf(w, `{"private_domain":%q,"ttl":60}`, f.zone)
		case r.Method == "GET" && r.URL.Path == "/api/v1/services":
			json.NewEncoder(w).Encode(map[string]any{"services": f.services})
		case r.Method == "GET" && r.URL.Path == "/api/v1/records":
			json.NewEncoder(w).Encode(map[string]any{"records": f.records})
		case r.Method == "POST" && r.URL.Path == "/api/v1/records":
			var d dnsRecord
			json.Unmarshal(body, &d)
			d.Name = fqdn(d.Name)
			if f.refuse || !strings.HasSuffix(d.Name, "."+f.zone+".") {
				w.WriteHeader(http.StatusBadRequest)
				// A hostile answer echoing the caller's token must not reach an error.
				fmt.Fprintf(w, `{"error":{"code":"name_out_of_zone","field":"name","message":"%s outside %s"}}`, kydnsTok, f.zone)
				return
			}
			f.next++
			d.ID = f.next
			f.records = append(f.records, d)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":%d}`, d.ID)
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v1/records/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/v1/records/"), 10, 64)
			for i, rec := range f.records {
				if rec.ID == id {
					f.records = append(f.records[:i], f.records[i+1:]...)
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	n, err := NewNPM("https://npm.test", "admin-session-token", testID, nil)
	if err != nil {
		t.Fatal(err)
	}
	n.lan.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	secrets := &fakeSecrets{values: map[string]string{"kydns-api/token": kydnsTok + "\n"}}
	n.Secrets = secrets
	t.Cleanup(n.Close)
	e := testEdge()
	e.Routes = []stack.Route{
		{App: "hello", Hostname: "hello.k80.example.com", CertificateID: 1},
		{App: "notes", Hostname: "notes.k80.example.com", CertificateID: 1},
	}
	e.LANDNS = &stack.LANDNS{Provider: "kydns", URL: srv.URL + "/", Credentials: stack.SecretRef{Secret: "kydns-api", Key: "token"}, Address: "192.168.1.81"}
	return n, secrets, e
}

func (f *fakeKyDNS) names() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, r := range f.records {
		out[r.Name] = r.Value
	}
	return out
}

func TestLANDNSCreateSkipRemoveAndReaddress(t *testing.T) {
	ctx := context.Background()
	foreign := dnsRecord{ID: 100, Name: "nas.example.com.", Type: "A", Value: "192.168.1.5"}
	f := &fakeKyDNS{zone: "example.com", records: []dnsRecord{foreign}, next: 100}
	n, secrets, e := f.serve(t)
	// An entry for another KyDNS server whose ID happens to match a record here.
	other := ledgerEntry{Server: "https://old-kydns.example.com", ID: 100, Name: "nas.example.com.", Value: "192.168.1.5"}
	secrets.ledger, _ = json.Marshal([]ledgerEntry{other})
	s := dnsStep{n: n, e: e}
	if err := n.Preflight(ctx, stack.Edge{Target: e.Target, LANDNS: e.LANDNS, Routes: nil}); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"nas.example.com.": "192.168.1.5", "hello.k80.example.com.": "192.168.1.81", "notes.k80.example.com.": "192.168.1.81"}
	if fmt.Sprint(f.names()) != fmt.Sprint(want) {
		t.Fatalf("records %v", f.names())
	}
	var ledger []ledgerEntry
	json.Unmarshal(secrets.ledger, &ledger)
	if len(ledger) != 3 || ledger[0] != other || ledger[1].ID != 101 || ledger[1].Name != "hello.k80.example.com." || ledger[1].Server != strings.TrimSuffix(e.LANDNS.URL, "/") {
		t.Fatalf("ledger %+v", ledger)
	}
	if strings.Contains(string(secrets.ledger), kydnsTok) {
		t.Fatal("the token reached the ledger")
	}

	// Rerun: nothing to do.
	before := len(f.mutations)
	if done, err := s.Inspect(ctx); !done || err != nil {
		t.Fatalf("rerun not a no-op: %v %v", done, err)
	}

	// A removed route deletes only its own record; the foreign one stays.
	e.Routes = e.Routes[:1]
	s = dnsStep{n: n, e: e}
	if done, _ := s.Inspect(ctx); done {
		t.Fatal("removed route not noticed")
	}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.names()["notes.k80.example.com."]; ok || f.names()["nas.example.com."] == "" {
		t.Fatalf("records after removal %v", f.names())
	}
	if len(f.mutations) != before+1 || !strings.HasPrefix(f.mutations[before], "DELETE /api/v1/records/102") {
		t.Fatalf("removal mutations %q", f.mutations[before:])
	}

	// A new address replaces its own record.
	e.LANDNS.Address = "192.168.1.82"
	if err := (dnsStep{n: n, e: e}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.names(); got["hello.k80.example.com."] != "192.168.1.82" || len(got) != 2 {
		t.Fatalf("records after re-address %v", got)
	}
}

func TestLANDNSRefusesForeignNames(t *testing.T) {
	ctx := context.Background()
	for name, f := range map[string]*fakeKyDNS{
		"record":        {zone: "example.com", records: []dnsRecord{{ID: 7, Name: "hello.k80.example.com.", Type: "A", Value: "192.168.1.81"}}},
		"cname":         {zone: "example.com", records: []dnsRecord{{ID: 7, Name: "notes.k80.example.com.", Type: "CNAME", Value: "x.example.com."}}},
		"service alias": {zone: "example.com", services: []map[string]any{{"name": "nas", "aliases": []string{"hello.k80"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			n, secrets, e := f.serve(t)
			s := dnsStep{n: n, e: e}
			if _, err := s.Inspect(ctx); err == nil || !strings.Contains(err.Error(), "did not create") {
				t.Fatalf("inspect: %v", err)
			}
			if err := s.Apply(ctx); err == nil {
				t.Fatal("applied over a foreign name")
			}
			if err := n.Preflight(ctx, stack.Edge{Target: e.Target, LANDNS: e.LANDNS, Routes: e.Routes}); err == nil {
				t.Fatal("preflight passed")
			}
			if len(f.mutations) != 0 || secrets.saves != 0 {
				t.Fatalf("changed state: %q, %d ledger saves", f.mutations, secrets.saves)
			}
		})
	}

	// An entry whose ID now holds another name is dropped; that record is foreign from then on.
	f := &fakeKyDNS{zone: "example.com", records: []dnsRecord{{ID: 9, Name: "other.k80.example.com.", Type: "A", Value: "192.168.1.81"}}, next: 9}
	n, secrets, e := f.serve(t)
	secrets.ledger, _ = json.Marshal([]ledgerEntry{{Server: strings.TrimSuffix(e.LANDNS.URL, "/"), ID: 9, Name: "hello.k80.example.com.", Value: "192.168.1.81"}})
	e.Routes = e.Routes[:1]
	if err := (dnsStep{n: n, e: e}).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.names(); got["other.k80.example.com."] == "" || got["hello.k80.example.com."] == "" || strings.Contains(string(secrets.ledger), `"id":9,`) {
		t.Fatalf("stale entry trusted: %v %s", got, secrets.ledger)
	}

	// A ledger entry whose record was replaced (same name, new ID) is dropped, not trusted.
	f = &fakeKyDNS{zone: "example.com", records: []dnsRecord{{ID: 9, Name: "hello.k80.example.com.", Type: "A", Value: "192.168.1.81"}}}
	n, secrets, e = f.serve(t)
	secrets.ledger, _ = json.Marshal([]ledgerEntry{{Server: strings.TrimSuffix(e.LANDNS.URL, "/"), ID: 3, Name: "hello.k80.example.com.", Value: "192.168.1.81"}})
	if err := (dnsStep{n: n, e: e}).Apply(ctx); err == nil || len(f.mutations) != 0 {
		t.Fatalf("trusted a stale ledger entry: %v %q", err, f.mutations)
	}
}

func TestLANDNSZoneRefusalAndSecrets(t *testing.T) {
	ctx := context.Background()
	f := &fakeKyDNS{zone: "lan.example.net"}
	n, _, e := f.serve(t)
	s := dnsStep{n: n, e: e}
	_, err := s.Inspect(ctx)
	if err == nil || !strings.Contains(err.Error(), "outside KyDNS's private zone lan.example.net.") || len(f.mutations) != 0 {
		t.Fatalf("zone precheck: %v", err)
	}

	if n.Redact.Redact(kydnsTok) != "[redacted]" {
		t.Fatal("KyDNS token was not registered for redaction")
	}

	// A wrong token, or a value that is not a KyDNS token, never appears in an error.
	for tok, want := range map[string]string{"kydns_wrong": "rejects the lan_dns API token", "not-a-token": "is not a KyDNS API token", "kydns_a b": "is not a KyDNS API token"} {
		f := &fakeKyDNS{zone: "example.com"}
		n, secrets, e := f.serve(t)
		secrets.values["kydns-api/token"] = tok
		if _, err := (dnsStep{n: n, e: e}).Inspect(ctx); err == nil || strings.Contains(err.Error(), tok) || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: %v", tok, err)
		}
	}
}

func TestLANDNSApplySurfacesZoneRefusal(t *testing.T) {
	ctx := context.Background()
	f := &fakeKyDNS{zone: "example.com", refuse: true}
	n, secrets, e := f.serve(t)
	err := dnsStep{n: n, e: e}.Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "KyDNS refuses hello.k80.example.com (name_out_of_zone)") || strings.Contains(err.Error(), kydnsTok) {
		t.Fatalf("zone refusal: %v", err)
	}
	if secrets.saves != 0 {
		t.Fatal("ledger recorded a refused record")
	}
	// The refusal body echoes the token; the client's error carries only the code.
	k, _ := dnsStep{n: n, e: e}.client(ctx)
	_, code, err := k.do(ctx, http.MethodPost, "/api/v1/records", dnsRecord{Name: "x.example.com", Type: "A", Value: "192.168.1.81"}, nil)
	if code != "name_out_of_zone" || err == nil || strings.Contains(err.Error(), kydnsTok) {
		t.Fatalf("refusal %q %v", code, err)
	}
}
