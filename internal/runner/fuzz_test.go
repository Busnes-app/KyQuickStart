package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	"github.com/google/uuid"
)

// A start request is KyYard's POST body. The runner holds no plan here, so a request that
// passes the checks is refused as owned_by_cli and nothing is recorded. Every answer decodes
// as KyYard decodes it; a request that passes names its record file inside operations/, and
// its stored encoding decodes to an equal request that passes again (the id_conflict check
// compares those encodings).
func FuzzStartRequest(f *testing.F) {
	id := uuid.NewString()
	ok := request(id, "install", "hello")
	for _, body := range []string{
		ok, request(id, "upgrade"), request(id, "upgrade", "hello", "kyidentity"), request(id, "uninstall", "hello"),
		strings.Replace(ok, `"id"`, `"extra":1,"id"`, 1), ok + `{}`, `id=1`, `null`, `{}`,
		request("6ba7b810-9dad-11d1-80b4-00c04fd430c8", "upgrade"), request(strings.ToUpper(id), "upgrade"),
		request("../../../etc/passwd", "upgrade"), request("{"+id+"}", "upgrade"), request("urn:uuid:"+id, "upgrade"),
		request(id, "delete", "hello"), request(id, "uninstall", "hello", "other"), request(id, "upgrade", "hello", "hello"),
		`{"id":"` + id + `","kind":"upgrade","actor":"u"}`,
		strings.Replace(ok, `,"target":"nas"`, ``, 1),
		strings.Replace(request(id, "uninstall", "hello"), `false`, `true`, 1),
		strings.Replace(ok, `"actor":"0f9e3c8e-1d2b-4c5a-8e7f-6a5b4c3d2e1f"`, `"actor":"a\nb"`, 1),
		strings.Replace(ok, `"actor":"0f9e3c8e-1d2b-4c5a-8e7f-6a5b4c3d2e1f"`, `"actor":"`+strings.Repeat("a", 129)+`"`, 1),
		strings.Replace(ok, `"hello"`, `"`+strings.Repeat("é", 400)+`"`, 1),
	} {
		f.Add([]byte(body))
	}
	d, err := os.MkdirTemp("", "kyq")
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { os.RemoveAll(d) })
	if d, err = filepath.EvalSymlinks(d); err != nil {
		f.Fatal(err)
	}
	s, err := newServer(filepath.Join(d, "state"), false, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "0.5.0"}, io.Discard)
	if err != nil {
		f.Fatal(err)
	}
	s.run = func(context.Context, string, string, cli.Request, cli.Options) error {
		f.Error("an operation ran without a plan")
		return nil
	}
	ops := filepath.Join(s.state, opsDir)
	f.Fuzz(func(t *testing.T, body []byte) {
		w := httptest.NewRecorder()
		s.handleStart(w, httptest.NewRequest(http.MethodPost, "/v1/operations", strings.NewReader(string(body))))
		refused(t, w.Code, w.Body.Bytes(), w.Code, map[int]suite.Code{400: suite.InvalidRequest, 413: suite.InvalidRequest, 409: suite.OwnedByCLI}[w.Code])
		var r struct{ Error kyError }
		json.Unmarshal(w.Body.Bytes(), &r)
		if len(r.Error.Message) > maxMessage || !utf8.ValidString(r.Error.Message) {
			t.Fatalf("message of %d bytes, valid UTF-8 %v", len(r.Error.Message), utf8.ValidString(r.Error.Message))
		}
		if entries, _ := os.ReadDir(ops); len(entries) != 0 {
			t.Fatalf("a request was recorded without a plan: %v", entries)
		}
		if w.Code != http.StatusConflict {
			return
		}
		var req suite.Request
		strict(t, body, &req)
		if filepath.Dir(s.file(req.ID)) != ops || filepath.Base(s.file(req.ID)) != req.ID+".json" {
			t.Fatalf("id %q names %s, outside %s", req.ID, s.file(req.ID), ops)
		}
		if !stack.ValidName(req.Target) && req.Target != "" || strings.ContainsAny(req.Actor, "\x00\r\n ") {
			t.Fatalf("accepted target %q actor %q", req.Target, req.Actor)
		}
		for i, a := range req.Apps {
			if !stack.ValidName(a) || slices.Contains(req.Apps[:i], a) {
				t.Fatalf("accepted apps %q", req.Apps)
			}
		}
		stored, _ := json.Marshal(req)
		var back suite.Request
		strict(t, stored, &back)
		if !reflect.DeepEqual(req, back) || validRequest(back) != nil {
			t.Fatalf("stored request %s decodes to %+v", stored, back)
		}
	})
}
