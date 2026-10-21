// Package runner is the suite runner: kyquickstart serve, which KyYard drives over a Unix
// socket (runner protocol v1 in docs/superpowers/specs/2026-10-16-suite-runner-design.md),
// and the serve-* commands the workstation's handoff runs inside its container.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
)

const (
	maxRequest    = 64 << 10
	maxMessage    = 512
	maxSocketPath = 107 // Linux sun_path less its terminator
	statusTimeout = 8 * time.Second
	// shutdownGrace is how long SIGTERM waits for a running operation; give the container a
	// longer stop timeout.
	shutdownGrace = 4 * time.Minute
)

var actorRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@-]{0,127}$`)

// record is a stored operation: the wire operation and the request that started it.
type record struct {
	suite.Operation
	Request suite.Request `json:"request"`
}

type server struct {
	state, plan, id string
	o               cli.Options
	run             func(ctx context.Context, kind, plan string, r cli.Request, o cli.Options) error
	status          func(ctx context.Context, plan string, o cli.Options) (suite.Status, error)
	grace           time.Duration
	log             *log.Logger

	mu      sync.Mutex // guards running and every operation file
	running *record
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func operate(ctx context.Context, kind, plan string, r cli.Request, o cli.Options) error {
	switch kind {
	case "install":
		return cli.Install(ctx, plan, r, o)
	case "uninstall":
		return cli.Uninstall(ctx, plan, r, o)
	case "unmanage":
		return cli.Unmanage(ctx, plan, r, o)
	}
	return cli.Upgrade(ctx, plan, r, o)
}

func newServer(state string, podVolume bool, o cli.Options, stderr io.Writer) (*server, error) {
	state, err := openState(state, podVolume)
	if err != nil {
		return nil, err
	}
	id, err := runnerID(state)
	if err != nil {
		return nil, err
	}
	o.RunnerID, o.SSHKey, o.Redact, o.In, o.Out = id, filepath.Join(state, keyFile), &engine.Redactor{}, nil, stderr
	s := &server{state: state, plan: filepath.Join(state, planDir), id: id, o: o, run: operate, status: cli.Status, grace: shutdownGrace, log: log.New(stderr, "kyquickstart runner: ", 0)}
	return s, s.recover()
}

// recover marks operations a previous process left running as unknown; none is retried.
func (s *server) recover() error {
	files, err := filepath.Glob(filepath.Join(s.state, opsDir, "*.json"))
	if err != nil {
		return err
	}
	for _, f := range files {
		rec, err := s.read(strings.TrimSuffix(filepath.Base(f), ".json"))
		if err != nil {
			return err
		}
		if rec.State == "running" {
			s.stopped(rec)
			if err := s.write(rec); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *server) stopped(rec *record) {
	rec.State, rec.Finished = "unknown", nil
	rec.Error = &suite.Refusal{Code: suite.Internal, Message: "the runner stopped while this operation ran; check its targets before retrying"}
}

func (s *server) file(id string) string { return filepath.Join(s.state, opsDir, id+".json") }

func (s *server) read(id string) (*record, error) {
	b, err := os.ReadFile(s.file(id))
	if err != nil {
		return nil, err
	}
	var rec record
	if err := json.Unmarshal(b, &rec); err != nil || rec.ID != id {
		return nil, fmt.Errorf("operation %s is malformed", id)
	}
	return &rec, nil
}

func (s *server) write(rec *record) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return writeFile(s.file(rec.ID), b)
}

// listen creates the socket as ky-primitives applysetup.StartLocal does: a canonical path in
// a private directory of this user, under ancestors no other user can modify.
func listen(socket string, podVolume bool) (net.Listener, error) {
	if !filepath.IsAbs(socket) || socket != filepath.Clean(socket) {
		return nil, errors.New("socket path must be absolute and canonical")
	}
	if len(socket) > maxSocketPath {
		return nil, fmt.Errorf("socket path exceeds the %d-byte Unix limit", maxSocketPath)
	}
	if err := privateDir("socket", filepath.Dir(socket), podVolume); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("socket path is occupied by a non-socket")
		}
		conn, err := net.DialTimeout("unix", socket, time.Second)
		if err == nil {
			conn.Close()
			return nil, errors.New("another runner owns the socket")
		}
		// Only a refused connection identifies a stale socket.
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return nil, errors.New("existing socket cannot be inspected")
		}
		if err := os.Remove(socket); err != nil {
			return nil, errors.New("cannot remove the stale socket")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("cannot inspect the socket path")
	}
	l, err := net.Listen("unix", socket)
	if err != nil {
		return nil, errors.New("cannot bind the socket")
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		l.Close()
		return nil, errors.New("cannot protect the socket")
	}
	return l, nil
}

// serve answers on l until ctx ends, then lets a running operation finish within s.grace
// before cancelling it and recording it as unknown.
func (s *server) serve(ctx context.Context, l net.Listener) error {
	srv := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := srv.Shutdown(stop)
	s.mu.Lock()
	rec := s.running
	s.mu.Unlock()
	if rec == nil || waitFor(&s.wg, s.grace) {
		return err
	}
	s.mu.Lock()
	s.cancel()
	s.mu.Unlock()
	waitFor(&s.wg, 45*time.Second) // the cancelled run releases its target locks
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped(rec)
	return errors.Join(err, s.write(rec))
}

func waitFor(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", only(http.MethodGet, s.handleStatus))
	mux.HandleFunc("/v1/catalog", only(http.MethodGet, s.handleCatalog))
	mux.HandleFunc("/v1/plan", only(http.MethodGet, s.handlePlan))
	mux.HandleFunc("/v1/operations", only(http.MethodPost, s.handleStart))
	mux.HandleFunc("/v1/operations/{id}", only(http.MethodGet, s.handleOperation))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		s.refuse(w, suite.Errorf(suite.NotFound, "no such route"))
	})
	return mux
}

func only(method string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			reply(w, http.StatusMethodNotAllowed, map[string]suite.Refusal{"error": {Code: suite.InvalidRequest, Message: "method not allowed"}})
			return
		}
		h(w, r)
	}
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

// httpStatus maps a refusal to its HTTP status, as the runner design's table lists. None is
// 404 except not_found: KyYard reads any 404 as an unknown operation.
func httpStatus(c suite.Code) int {
	switch c {
	case suite.InvalidRequest:
		return http.StatusBadRequest
	case suite.NotFound:
		return http.StatusNotFound
	case suite.IDConflict, suite.Busy, suite.PlanStale, suite.TargetLocked, suite.OwnedByRunner, suite.OwnedByCLI, suite.OwnedByOtherPlan:
		return http.StatusConflict
	case suite.UnknownApp, suite.UnknownTarget, suite.AlreadyInstalled, suite.NotInstalled, suite.Dependency,
		suite.UpgradeRefused, suite.BackupRequired, suite.AckRequired, suite.IssuerChanged, suite.RunnerHost, suite.ServerNameChanged, suite.ProviderIDMismatch,
		suite.BackupUnconfigured, suite.BackupUnsupported, suite.RestoreRefused:
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError // step_failed, restore_incomplete, internal
}

// refusal is err on the wire: its code, or internal, and its message redacted and bounded.
func (s *server) refusal(err error) *suite.Refusal {
	code, msg := suite.Internal, err.Error()
	var se *suite.Error
	if errors.As(err, &se) {
		code, msg = se.Code, se.Message
	}
	msg = s.o.Redact.Redact(msg)
	if len(msg) > maxMessage {
		i := maxMessage
		for i > 0 && !utf8.RuneStart(msg[i]) {
			i--
		}
		msg = msg[:i]
	}
	return &suite.Refusal{Code: code, Message: msg}
}

func (s *server) refuse(w http.ResponseWriter, err error) {
	r := s.refusal(err)
	reply(w, httpStatus(r.Code), map[string]*suite.Refusal{"error": r})
}

var errNoPlan = suite.Errorf(suite.OwnedByCLI, "no plan has been handed to this runner; run kyquickstart handoff from the workstation")

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !hasPlan(s.state) {
		s.refuse(w, errNoPlan)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), statusTimeout)
	defer cancel()
	st, err := s.status(ctx, s.plan, s.o)
	if err != nil {
		s.refuse(w, err)
		return
	}
	st.Runner.RunnerID = s.id
	s.mu.Lock()
	if s.running != nil {
		st.Operation = &s.running.ID
	}
	s.mu.Unlock()
	reply(w, http.StatusOK, st)
}

func (s *server) handleCatalog(w http.ResponseWriter, _ *http.Request) {
	if !hasPlan(s.state) {
		s.refuse(w, errNoPlan)
		return
	}
	c, err := cli.Catalog(s.plan, s.o)
	if err != nil {
		s.refuse(w, err)
		return
	}
	reply(w, http.StatusOK, c)
}

func (s *server) handlePlan(w http.ResponseWriter, _ *http.Request) {
	var b bytes.Buffer
	if err := export(s.state, &b); err != nil {
		if !hasPlan(s.state) {
			err = errNoPlan
		}
		s.refuse(w, err)
		return
	}
	st, err := stack.Parse(b.Bytes())
	if err != nil {
		s.refuse(w, err)
		return
	}
	reply(w, http.StatusOK, struct {
		Revision int    `json:"revision"`
		Stack    string `json:"stack"`
	}{st.Revision, b.String()})
}

func validRequest(r suite.Request) error {
	one := r.Kind != "upgrade"
	switch {
	case !validUUID(r.ID):
		return errors.New("id must be a lowercase UUID v4")
	case !slices.Contains([]string{"install", "uninstall", "unmanage", "upgrade"}, r.Kind):
		return errors.New("kind must be install, uninstall, unmanage or upgrade")
	case r.Apps == nil || one && len(r.Apps) != 1 || len(r.Apps) > 64:
		return errors.New("apps must list exactly one app, or for upgrade any number (none: every installed app)")
	case (r.Kind == "install") != (r.Target != ""):
		return errors.New("target is required for install and refused otherwise")
	case r.Target != "" && !stack.ValidName(r.Target):
		return errors.New("target is not a valid name")
	case r.AcknowledgeNoBackup && r.Kind != "install" && r.Kind != "upgrade":
		return errors.New("acknowledge_no_backup is only for install and upgrade")
	case !actorRE.MatchString(r.Actor):
		return errors.New("actor must match " + actorRE.String())
	}
	for i, a := range r.Apps {
		if !stack.ValidName(a) || slices.Contains(r.Apps[:i], a) {
			return fmt.Errorf("app %q is not a valid name or appears twice", a)
		}
	}
	return nil
}

func (s *server) handleStart(w http.ResponseWriter, r *http.Request) {
	var req suite.Request
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequest))
	d.DisallowUnknownFields()
	err := d.Decode(&req)
	if err == nil && d.Decode(new(any)) != io.EOF {
		err = errors.New("trailing data")
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		reply(w, http.StatusRequestEntityTooLarge, map[string]suite.Refusal{"error": {Code: suite.InvalidRequest, Message: "request exceeds 64 KiB"}})
		return
	}
	if err == nil {
		err = validRequest(req)
	}
	if err != nil {
		s.refuse(w, suite.Errorf(suite.InvalidRequest, "%s", err.Error()))
		return
	}
	if !hasPlan(s.state) {
		s.refuse(w, errNoPlan)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	accepted := map[string]string{"id": req.ID}
	if prev, err := s.read(req.ID); err == nil {
		a, _ := json.Marshal(prev.Request)
		b, _ := json.Marshal(req)
		if !bytes.Equal(a, b) {
			s.refuse(w, suite.Errorf(suite.IDConflict, "operation %s was started with a different request", req.ID))
			return
		}
		reply(w, http.StatusAccepted, accepted)
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.refuse(w, err)
		return
	}
	if s.running != nil {
		s.refuse(w, suite.Errorf(suite.Busy, "operation %s is running", s.running.ID))
		return
	}
	rec := &record{Request: req, Operation: suite.Operation{ID: req.ID, Kind: req.Kind, Apps: req.Apps, Actor: req.Actor,
		State: "running", Started: time.Now().UTC(), Steps: []suite.Step{}}}
	if err := s.write(rec); err != nil {
		s.refuse(w, err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.running, s.cancel = rec, cancel
	s.wg.Add(1)
	go s.execute(ctx, rec)
	reply(w, http.StatusAccepted, accepted)
}

// execute runs rec, recording every step as it starts and ends.
func (s *server) execute(ctx context.Context, rec *record) {
	defer s.wg.Done()
	o := s.o
	o.Progress = func(e engine.Event) {
		s.mu.Lock()
		defer s.mu.Unlock()
		step := suite.Step{ID: e.ID, Status: e.Status, Time: e.Time, Diagnostics: e.Diagnostics}
		if i := slices.IndexFunc(rec.Steps, func(st suite.Step) bool { return st.ID == e.ID }); i >= 0 {
			rec.Steps[i] = step
		} else {
			rec.Steps = append(rec.Steps, step)
		}
		if err := s.write(rec); err != nil {
			s.log.Printf("operation %s: %v", rec.ID, err)
		}
	}
	r := cli.Request{Apps: rec.Request.Apps, Target: rec.Request.Target, AcknowledgeNoBackup: rec.Request.AcknowledgeNoBackup,
		Holder: fmt.Sprintf("kyquickstart runner %s for %s", s.id, rec.Actor)}
	err := s.run(ctx, rec.Kind, s.plan, r, o)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	rec.State, rec.Finished = "succeeded", &now
	if err != nil {
		rec.State, rec.Error = "failed", s.refusal(err)
	}
	if err := s.write(rec); err != nil {
		s.log.Printf("operation %s: %v", rec.ID, err)
	}
	s.running = nil
	s.cancel()
}

func (s *server) handleOperation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validUUID(id) {
		s.refuse(w, suite.Errorf(suite.NotFound, "no such operation"))
		return
	}
	s.mu.Lock()
	rec, err := s.read(id)
	s.mu.Unlock()
	if errors.Is(err, os.ErrNotExist) {
		err = suite.Errorf(suite.NotFound, "no such operation")
	}
	if err != nil {
		s.refuse(w, err)
		return
	}
	reply(w, http.StatusOK, rec.Operation)
}
