package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const otherID = "2c9d6f0a-7b1e-4c3d-9a8b-5e6f7a8b9c0d"

// fakeHosts serves proxy hosts the way NPM does: updates merge meta, enable sets nginx_online.
func fakeHosts(t *testing.T, hosts []host) (*NPM, func() []host) {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		find := func() *host {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, hostPath+"/"), "/")
			id, _ := strconv.Atoi(parts[0])
			for i := range hosts {
				if hosts[i].ID == id {
					return &hosts[i]
				}
			}
			t.Fatalf("unknown host %s", r.URL.Path)
			return nil
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/nginx/certificates/"):
			fmt.Fprint(w, `{"domain_names":["*.example.com"],"expires_on":"2099-01-01T00:00:00Z"}`)
		case r.Method == "GET" && r.URL.Path == hostPath:
			json.NewEncoder(w).Encode(hosts)
		case r.Method == "GET":
			fmt.Fprint(w, `[]`)
		case r.Method == "POST" && r.URL.Path == hostPath:
			var c hostConfig
			json.NewDecoder(r.Body).Decode(&c)
			hosts = append(hosts, host{ID: 100 + len(hosts), hostConfig: c})
			fmt.Fprint(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/enable"):
			h := find()
			h.Enabled, h.Meta["nginx_online"] = true, true
			fmt.Fprint(w, `true`)
		case strings.HasSuffix(r.URL.Path, "/disable"):
			find().Enabled = false
			fmt.Fprint(w, `true`)
		case r.Method == "PUT":
			h := find()
			meta := h.Meta
			json.NewDecoder(r.Body).Decode(&h.hostConfig)
			maps.Copy(meta, h.Meta)
			h.Meta = meta
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	n, err := NewNPM(srv.URL, "secret-token", testID, nil)
	if err != nil {
		t.Fatal(err)
	}
	n.http.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	t.Cleanup(n.Close)
	return n, func() []host {
		mu.Lock()
		defer mu.Unlock()
		return append([]host(nil), hosts...)
	}
}

// enabledHost is the configuration installation id writes for hostname, enabled.
func enabledHost(id int, installation, hostname string) host {
	e := testEdge()
	r := e.Routes[0]
	r.Hostname = hostname
	c := (&NPM{installation: installation}).config(e, r)
	c.Enabled = true
	c.Meta["nginx_online"] = true
	return host{ID: id, hostConfig: c}
}

// legacyHost is what builds before installation IDs wrote: target marks only.
func legacyHost(id int, hostname string) host {
	h := enabledHost(id, testID, hostname)
	h.Advanced = strings.Replace(h.Advanced, "# kyquickstart:"+testID+":", "# kyquickstart:", 1)
	delete(h.Meta, "ky.installation")
	return h
}

func TestForeignInstallationUntouched(t *testing.T) {
	ctx := context.Background()
	e := testEdge()
	theirs := enabledHost(1, otherID, "hello.example.com")
	theirsRemoved := enabledHost(2, otherID, "gone.example.com")
	n, hosts := fakeHosts(t, []host{theirs, theirsRemoved})
	for _, adopt := range []bool{false, true} {
		n.Adopt = adopt
		if err := n.Preflight(ctx, e); err == nil || !strings.Contains(err.Error(), "foreign NPM host") || strings.Contains(err.Error(), "adopt") {
			t.Fatalf("adopt=%v: claimed another installation's host: %v", adopt, err)
		}
		if err := n.Steps(e)[0].Apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := hosts(); !got[0].Enabled || !got[1].Enabled || got[0].Advanced != theirs.Advanced {
		t.Fatal("another installation's hosts were changed")
	}
}

func TestLegacyHostsNeedAdoption(t *testing.T) {
	ctx := context.Background()
	e := testEdge()
	n, hosts := fakeHosts(t, []host{legacyHost(1, "hello.example.com"), legacyHost(2, "gone.example.com")})
	steps := n.Steps(e)
	if err := n.Preflight(ctx, e); err == nil || !strings.Contains(err.Error(), "--adopt-edge-hosts") {
		t.Fatalf("legacy host claimed without the flag: %v", err)
	}
	if err := steps[0].Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if got := hosts(); !got[1].Enabled {
		t.Fatal("legacy removed host disabled without the flag")
	}

	n.Adopt = true
	for _, s := range steps {
		if err := s.Apply(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got := hosts()
	if got[0].Meta["ky.installation"] != testID || !strings.HasPrefix(got[0].Advanced, "# kyquickstart:"+testID+":nas:hello:") || !got[0].Enabled {
		t.Fatalf("adopted host not rewritten: %+v", got[0])
	}
	if got[1].Enabled {
		t.Fatal("adopted removed host still enabled")
	}
	// Rewritten once: later runs own it without the flag.
	n.Adopt = false
	if ok, err := steps[1].Inspect(ctx); !ok || err != nil {
		t.Fatalf("adopted host is not ours afterwards: %v %v", ok, err)
	}
}
