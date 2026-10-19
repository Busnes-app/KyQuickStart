package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	"github.com/google/uuid"
)

const demoPlan = "version: 1\ndemo: true\nrevision: 2\ntargets:\n  - name: nas\n    host: 192.0.2.1\n    user: kyq\napps:\n  - name: hello\n    target: nas\n"

// shortDir is a private temporary directory with a path short enough for a socket, symlinks
// resolved (macOS's /var is one, which the ancestry check refuses).
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "kyq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	d, err = filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

type harness struct {
	s      *server
	socket string
	c      *http.Client
	stop   context.CancelFunc
	done   chan error
}

// start serves a runner whose state holds plan (none when empty) on a temp socket.
func start(t *testing.T, plan string) *harness {
	t.Helper()
	state := filepath.Join(shortDir(t), "state")
	s, err := newServer(state, false, cli.Options{Catalog: catalog.Embedded(), ReleaseSet: "0.5.0"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if plan != "" {
		os.WriteFile(filepath.Join(s.plan, "stack.yaml"), []byte(plan), 0o600)
	}
	return serveHarness(t, s)
}

func serveHarness(t *testing.T, s *server) *harness {
	t.Helper()
	socket := filepath.Join(shortDir(t), "run", "runner.sock")
	l, err := listen(socket, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{s: s, socket: socket, stop: cancel, done: make(chan error, 1)}
	go func() { h.done <- s.serve(ctx, l) }()
	t.Cleanup(func() { cancel(); <-h.done })
	h.c = &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	return h
}

// do sends one request and returns the status and body.
func (h *harness) do(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, "http://runner"+path, strings.NewReader(body))
	res, err := h.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

// strict decodes as KyYard's client does: unknown fields refused, no trailing data.
func strict(t *testing.T, b []byte, out any) {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil || d.Decode(new(any)) != io.EOF {
		t.Fatalf("KyYard would refuse %s: %v", b, err)
	}
}

// refused checks a refusal body as KyYard decodes it.
func refused(t *testing.T, status int, b []byte, wantStatus int, want suite.Code) {
	t.Helper()
	var r struct {
		Error *kyError `json:"error"`
	}
	strict(t, b, &r)
	if status != wantStatus || r.Error == nil || r.Error.Code != string(want) || r.Error.Message == "" {
		t.Fatalf("got %d %s, want %d %s", status, b, wantStatus, want)
	}
}

func request(id, kind string, apps ...string) string {
	r := kyRequest{ID: id, Kind: kind, Apps: append([]string{}, apps...), Actor: "0f9e3c8e-1d2b-4c5a-8e7f-6a5b4c3d2e1f"}
	if kind == "install" {
		r.Target = "nas"
	}
	b, _ := json.Marshal(r)
	return string(b)
}

func TestReadEndpointsDecodeAsKyYard(t *testing.T) {
	h := start(t, demoPlan)
	h.s.status = func(_ context.Context, plan string, o cli.Options) (suite.Status, error) {
		if plan != h.s.plan || o.RunnerID != h.s.id {
			t.Errorf("status called with %s %+v", plan, o)
		}
		now := time.Now().UTC()
		return suite.Status{Runner: suite.Runner{ReleaseSet: "0.5.0", PlanRevision: 2, PlanOwner: "runner"}, Targets: []suite.Target{{Name: "nas", Kind: "docker"}},
			Apps: []suite.App{{Name: "hello", Category: "ky", Version: "1.11.0", Target: "nas", State: "installed", Health: &suite.Health{OK: true, Checked: now}, LastResult: &suite.Result{Step: "hello.health", Status: "ok", Time: now}}}}, nil
	}
	code, b := h.do(t, "GET", "/v1/status", "")
	var st kyStatus
	strict(t, b, &st)
	if code != 200 || st.Runner.RunnerID != h.s.id || st.Operation != nil || len(st.Apps) != 1 || st.Apps[0].Health == nil {
		t.Fatalf("status %d %s", code, b)
	}

	code, b = h.do(t, "GET", "/v1/catalog", "")
	var c kyCatalog
	strict(t, b, &c)
	i := slices.IndexFunc(c.Apps, func(a kyCatalogApp) bool { return a.Name == "hello" })
	if code != 200 || c.ReleaseSet != "0.5.0" || i < 0 || !c.Apps[i].Installed || c.Apps[i].DependsOn == nil || c.Apps[i].Upgrade.NoSkip == nil {
		t.Fatalf("catalog %d %s", code, b)
	}

	code, b = h.do(t, "GET", "/v1/plan", "")
	var p kyPlan
	strict(t, b, &p)
	if code != 200 || p.Revision != 2 || p.Stack != demoPlan {
		t.Fatalf("plan %d %s", code, b)
	}
}

func TestNoPlanYet(t *testing.T) {
	h := start(t, "")
	for _, path := range []string{"/v1/status", "/v1/catalog", "/v1/plan"} {
		code, b := h.do(t, "GET", path, "")
		refused(t, code, b, 409, suite.OwnedByCLI)
	}
	code, b := h.do(t, "POST", "/v1/operations", request(uuid.NewString(), "upgrade"))
	refused(t, code, b, 409, suite.OwnedByCLI)
}

func TestOperationLifecycle(t *testing.T) {
	h := start(t, demoPlan)
	release := make(chan error)
	var got cli.Request
	h.s.run = func(ctx context.Context, kind, plan string, r cli.Request, o cli.Options) error {
		got = r
		o.Progress(engine.Event{ID: "hello.deploy", Status: "running", Time: time.Now().UTC()})
		o.Progress(engine.Event{ID: "hello.deploy", Status: "ok", Time: time.Now().UTC()})
		o.Progress(engine.Event{ID: "hello.health", Status: "running", Time: time.Now().UTC()})
		return <-release
	}
	id := uuid.NewString()
	body := request(id, "upgrade", "hello")
	code, b := h.do(t, "POST", "/v1/operations", body)
	var started struct {
		ID string `json:"id"`
	}
	strict(t, b, &started)
	if code != 202 || started.ID != id {
		t.Fatalf("start %d %s", code, b)
	}
	// The same request again starts nothing; a different one under the same ID conflicts.
	if code, b := h.do(t, "POST", "/v1/operations", body); code != 202 || !strings.Contains(string(b), id) {
		t.Fatalf("repeat %d %s", code, b)
	}
	code, b = h.do(t, "POST", "/v1/operations", strings.Replace(body, `"apps":["hello"]`, `"apps":[]`, 1))
	refused(t, code, b, 409, suite.IDConflict)
	code, b = h.do(t, "POST", "/v1/operations", request(uuid.NewString(), "upgrade"))
	refused(t, code, b, 409, suite.Busy)

	waitFor := func(state string, steps int) kyOperation {
		t.Helper()
		var op kyOperation
		for range 200 {
			code, b := h.do(t, "GET", "/v1/operations/"+id, "")
			op = kyOperation{}
			strict(t, b, &op)
			if code != 200 || op.ID != id || !slices.Contains(kyKinds, op.Kind) || !slices.Contains(kyStates, op.State) {
				t.Fatalf("operation %d %s", code, b)
			}
			if op.State == state && len(op.Steps) == steps {
				return op
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("operation never reached %s with %d steps: %+v", state, steps, op)
		return op
	}
	op := waitFor("running", 2)
	if op.Steps[0].Status != "ok" || op.Steps[1].Status != "running" || op.Finished != nil || op.Error != nil {
		t.Errorf("running operation = %+v", op)
	}
	h.s.status = func(context.Context, string, cli.Options) (suite.Status, error) { return suite.Status{}, nil }
	if code, b := h.do(t, "GET", "/v1/status", ""); code != 200 || !strings.Contains(string(b), `"operation":"`+id+`"`) {
		t.Errorf("status during operation: %d %s", code, b)
	}
	if got.Holder != "kyquickstart runner "+h.s.id+" for 0f9e3c8e-1d2b-4c5a-8e7f-6a5b4c3d2e1f" || !slices.Equal(got.Apps, []string{"hello"}) {
		t.Errorf("request = %+v", got)
	}
	release <- nil
	op = waitFor("succeeded", 2)
	if op.Finished == nil || op.Error != nil {
		t.Errorf("finished operation = %+v", op)
	}

	// A refusal becomes the operation's error, redacted and bounded.
	h.s.o.Redact.Add("hunter2-secret-value")
	h.s.run = func(context.Context, string, string, cli.Request, cli.Options) error {
		return suite.Errorf(suite.AckRequired, "token hunter2-secret-value %s", strings.Repeat("é", 400))
	}
	id = uuid.NewString()
	if code, b := h.do(t, "POST", "/v1/operations", request(id, "install", "hello")); code != 202 {
		t.Fatalf("start %d %s", code, b)
	}
	op = waitFor("failed", 0)
	if op.Error == nil || op.Error.Code != "ack_required" || strings.Contains(op.Error.Message, "hunter2") || !strings.Contains(op.Error.Message, "[redacted]") || len(op.Error.Message) > 512 || !strings.HasSuffix(op.Error.Message, "é") {
		t.Errorf("error = %+v", op.Error)
	}
	// Anything without a code is internal.
	h.s.run = func(context.Context, string, string, cli.Request, cli.Options) error {
		return errors.New("dial tcp: refused")
	}
	id = uuid.NewString()
	h.do(t, "POST", "/v1/operations", request(id, "uninstall", "hello"))
	if op = waitFor("failed", 0); op.Error.Code != "internal" || op.Error.Message != "dial tcp: refused" {
		t.Errorf("error = %+v", op.Error)
	}
	if fi, _ := os.Stat(h.s.file(id)); fi.Mode().Perm() != 0o600 {
		t.Errorf("operation file mode %v", fi.Mode())
	}
}

func TestRequestsAreChecked(t *testing.T) {
	h := start(t, demoPlan)
	h.s.run = func(context.Context, string, string, cli.Request, cli.Options) error { return nil }
	id := uuid.NewString()
	ok := request(id, "install", "hello")
	for name, body := range map[string]string{
		"unknown field":   strings.Replace(ok, `"id"`, `"extra":1,"id"`, 1),
		"trailing data":   ok + `{}`,
		"not json":        `id=1`,
		"uuid v1":         request("6ba7b810-9dad-11d1-80b4-00c04fd430c8", "upgrade"),
		"uppercase id":    request(strings.ToUpper(id), "upgrade"),
		"path in id":      request("../../../etc/passwd", "upgrade"),
		"kind":            request(id, "delete", "hello"),
		"two apps":        request(id, "uninstall", "hello", "other"),
		"no app":          request(id, "unmanage"),
		"missing apps":    `{"id":"` + id + `","kind":"upgrade","actor":"u"}`,
		"duplicate app":   request(id, "upgrade", "hello", "hello"),
		"bad app":         request(id, "upgrade", "Hello"),
		"install target":  strings.Replace(ok, `,"target":"nas"`, ``, 1),
		"target":          strings.Replace(request(id, "uninstall", "hello"), `"apps"`, `"target":"nas","apps"`, 1),
		"ack on removal":  strings.Replace(request(id, "uninstall", "hello"), `false`, `true`, 1),
		"actor":           strings.Replace(ok, `"actor":"0f9e3c8e-1d2b-4c5a-8e7f-6a5b4c3d2e1f"`, `"actor":"a b"`, 1),
		"actor too long":  strings.Replace(ok, `"actor":"0f9e3c8e-1d2b-4c5a-8e7f-6a5b4c3d2e1f"`, `"actor":"`+strings.Repeat("a", 129)+`"`, 1),
		"actor holder nl": strings.Replace(ok, `"actor":"0f9e3c8e-1d2b-4c5a-8e7f-6a5b4c3d2e1f"`, `"actor":"a\nb"`, 1),
	} {
		code, b := h.do(t, "POST", "/v1/operations", body)
		if code != 400 || !strings.Contains(string(b), `"invalid_request"`) {
			t.Errorf("%s: %d %s", name, code, b)
		}
	}
	code, b := h.do(t, "POST", "/v1/operations", strings.Replace(ok, `"kind"`, `"pad":"`+strings.Repeat("a", maxRequest)+`","kind"`, 1))
	refused(t, code, b, 413, suite.InvalidRequest)
	if entries, _ := os.ReadDir(filepath.Join(h.s.state, opsDir)); len(entries) != 0 {
		t.Errorf("a refused request was recorded: %v", entries)
	}
	if code, b := h.do(t, "POST", "/v1/operations", ok); code != 202 {
		t.Fatalf("valid request: %d %s", code, b)
	}
}

func TestRoutesAndMissingOperations(t *testing.T) {
	h := start(t, demoPlan)
	for _, c := range []struct {
		method, path string
		status       int
		code         suite.Code
	}{
		{"GET", "/v1/operations/" + uuid.NewString(), 404, suite.NotFound},
		{"GET", "/v1/operations/not-a-uuid", 404, suite.NotFound},
		{"GET", "/v1/nothing", 404, suite.NotFound},
		{"POST", "/v1/status", 405, suite.InvalidRequest},
		{"GET", "/v1/operations", 405, suite.InvalidRequest},
		{"DELETE", "/v1/operations/" + uuid.NewString(), 405, suite.InvalidRequest},
	} {
		code, b := h.do(t, c.method, c.path, "")
		refused(t, code, b, c.status, c.code)
	}
}

// Only not_found is a 404: KyYard reads any 404 as an operation the runner does not know.
func TestHTTPStatusTable(t *testing.T) {
	want := map[suite.Code]int{
		suite.InvalidRequest: 400, suite.NotFound: 404,
		suite.IDConflict: 409, suite.Busy: 409, suite.PlanStale: 409, suite.TargetLocked: 409, suite.OwnedByRunner: 409, suite.OwnedByCLI: 409, suite.OwnedByOtherPlan: 409,
		suite.UnknownApp: 422, suite.UnknownTarget: 422, suite.AlreadyInstalled: 422, suite.NotInstalled: 422, suite.Dependency: 422,
		suite.UpgradeRefused: 422, suite.BackupRequired: 422, suite.AckRequired: 422, suite.IssuerChanged: 422, suite.RunnerHost: 422,
		suite.StepFailed: 500, suite.Internal: 500,
	}
	for c, status := range want {
		if got := httpStatus(c); got != status {
			t.Errorf("%s: %d, want %d", c, got, status)
		}
	}
	doc, err := os.ReadFile("../../docs/superpowers/specs/2026-10-16-suite-runner-design.md")
	if err != nil {
		t.Fatal(err)
	}
	for c := range want {
		if !strings.Contains(string(doc), "`"+string(c)+"`") {
			t.Errorf("design doc does not list %s", c)
		}
	}
}

func TestRunningOperationBecomesUnknownAtStartup(t *testing.T) {
	state := filepath.Join(shortDir(t), "state")
	s, err := newServer(state, false, cli.Options{Catalog: catalog.Embedded()}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	running, done := &record{Operation: suite.Operation{ID: uuid.NewString(), Kind: "upgrade", Apps: []string{}, State: "running", Steps: []suite.Step{}}},
		&record{Operation: suite.Operation{ID: uuid.NewString(), Kind: "upgrade", Apps: []string{}, State: "succeeded", Steps: []suite.Step{}}}
	s.write(running)
	s.write(done)
	s, err = newServer(state, false, cli.Options{Catalog: catalog.Embedded()}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := s.read(running.ID); r.State != "unknown" || r.Error == nil || r.Finished != nil {
		t.Errorf("left running: %+v", r)
	}
	if r, _ := s.read(done.ID); r.State != "succeeded" {
		t.Errorf("finished operation changed: %+v", r)
	}
	id, _ := runnerID(s.state)
	if id != s.id {
		t.Error("runner ID changed across restarts")
	}
}

func TestShutdownWaitsThenRecordsUnknown(t *testing.T) {
	h := start(t, demoPlan)
	h.s.grace = 50 * time.Millisecond
	cancelled := make(chan struct{})
	h.s.run = func(ctx context.Context, _, _ string, _ cli.Request, _ cli.Options) error {
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}
	id := uuid.NewString()
	if code, b := h.do(t, "POST", "/v1/operations", request(id, "upgrade")); code != 202 {
		t.Fatalf("start %d %s", code, b)
	}
	h.stop()
	if err := <-h.done; err != nil {
		t.Fatal(err)
	}
	h.done <- nil // for the cleanup
	<-cancelled
	if r, _ := h.s.read(id); r.State != "unknown" {
		t.Errorf("operation after shutdown: %+v", r.Operation)
	}
	if _, err := os.Lstat(h.socket); !os.IsNotExist(err) {
		t.Error("socket left after shutdown")
	}

	// An operation that finishes within the grace keeps its result.
	h = start(t, demoPlan)
	h.s.run = func(context.Context, string, string, cli.Request, cli.Options) error {
		time.Sleep(50 * time.Millisecond)
		return nil
	}
	id = uuid.NewString()
	h.do(t, "POST", "/v1/operations", request(id, "upgrade"))
	h.stop()
	<-h.done
	h.done <- nil
	if r, _ := h.s.read(id); r.State != "succeeded" {
		t.Errorf("operation finishing within the grace: %+v", r.Operation)
	}
}

func TestSocket(t *testing.T) {
	dir := filepath.Join(shortDir(t), "run")
	socket := filepath.Join(dir, "runner.sock")
	l, err := listen(socket, false)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(socket); fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("directory mode %v", fi.Mode())
	}
	if _, err := listen(socket, false); err == nil || !strings.Contains(err.Error(), "another runner") {
		t.Fatalf("live socket: %v", err)
	}
	// A crashed runner leaves its socket file behind; the next one replaces it.
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	l, err = listen(socket, false)
	if err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	l.Close()

	os.WriteFile(socket, nil, 0o600)
	if _, err := listen(socket, false); err == nil || !strings.Contains(err.Error(), "non-socket") {
		t.Errorf("file at the socket path: %v", err)
	}
	os.Remove(socket)
	os.Chmod(dir, 0o750)
	if _, err := listen(socket, false); err == nil || !strings.Contains(err.Error(), "private") {
		t.Errorf("shared directory: %v", err)
	}
	for _, bad := range []string{"run/runner.sock", dir + "/../run/runner.sock", "/" + strings.Repeat("d", maxSocketPath)} {
		if _, err := listen(bad, false); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
