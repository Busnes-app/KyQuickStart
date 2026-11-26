package dockerhost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Trimmed from real `docker compose ps --all --format json` output (Compose 5.6).
const realPS = `{"Command":"\"/whoami\"","ExitCode":0,"Health":"","Name":"kyq-probe-whoami-1","Project":"kyq-probe","Service":"whoami","State":"running","Status":"Up Less than a second"}
{"Command":"\"/db\"","ExitCode":0,"Health":"starting","Name":"kyq-probe-db-1","Project":"kyq-probe","Service":"db","State":"running","Status":"Up 1 second (health: starting)"}
`

func TestParsePS(t *testing.T) {
	cs, err := parsePS([]byte(realPS))
	if err != nil || len(cs) != 2 {
		t.Fatalf("%v %v", cs, err)
	}
	if got := unhealthy(cs, []string{"whoami", "db"}); got != "service db is starting" {
		t.Errorf("unhealthy = %q", got)
	}
	if got := notRunning(cs, []string{"whoami", "db"}); got != "" {
		t.Errorf("notRunning = %q", got)
	}
	if got := notRunning(nil, []string{"whoami"}); got != "service whoami is not running" {
		t.Errorf("notRunning(empty) = %q", got)
	}
	if _, err := parsePS([]byte("not json")); err == nil {
		t.Error("garbage parsed")
	}
}

func TestHealthApplyWaits(t *testing.T) {
	pollInterval = time.Millisecond
	n := 0
	f := &fakeRunner{reply: func(string) ([]byte, error) {
		n++
		if n < 3 {
			return []byte(`{"Service":"web","State":"running","Health":"starting"}`), nil
		}
		return []byte(`{"Service":"web","State":"running","Health":"healthy"}`), nil
	}}
	h := Steps(fakeApp(f))[1]
	if err := h.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHealthApplyTimesOut(t *testing.T) {
	pollInterval = 50 * time.Millisecond
	f := &fakeRunner{reply: func(string) ([]byte, error) {
		return []byte(`{"Service":"web","State":"running","Health":"unhealthy"}`), nil
	}}
	err := Steps(fakeApp(f))[1].Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not healthy after 1s: service web is unhealthy") {
		t.Fatalf("err = %v", err)
	}
}

// A one-shot (catalog Once) that exited 0 is done; one that exited non-zero fails health at
// once, with its logs, and so does the up it stopped. Lines as Compose 5.7 prints them.
func TestOnceExitCode(t *testing.T) {
	pollInterval = time.Millisecond
	const db = `{"ExitCode":0,"Health":"healthy","Service":"postgres","State":"running","Status":"Up 9 seconds (healthy)"}` + "\n"
	dump := func(code string) string {
		return `{"ExitCode":` + code + `,"Health":"","Service":"postgres-17-dump","State":"exited","Status":"Exited (` + code + `) 10 seconds ago"}` + "\n"
	}
	run := func(ps string, upErr error) (*fakeRunner, App) {
		f := &fakeRunner{reply: func(cmd string) ([]byte, error) {
			switch {
			case strings.Contains(cmd, " ps "):
				return []byte(ps), nil
			case strings.Contains(cmd, " logs "):
				return []byte("postgres-17-dump-1  | postgres-17-to-18: PostgreSQL 17 has run on /v since it was moved to 18\n"), nil
			case strings.Contains(cmd, " up "):
				return nil, upErr
			}
			return nil, nil
		}}
		a := fakeApp(f)
		a.Catalog.Services, a.Catalog.Once = []string{"postgres"}, []string{"postgres-17-dump"}
		return f, a
	}

	_, a := run(db+dump("0"), nil)
	if err := Steps(a)[1].Apply(context.Background()); err != nil {
		t.Fatalf("an exited-0 one-shot is not healthy: %v", err)
	}
	if st, err := State(context.Background(), a); err != nil || st != "running" {
		t.Fatalf("state = %q, %v; want running", st, err)
	}

	f, a := run(db+dump("1"), nil)
	err := Steps(a)[1].Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "service postgres-17-dump failed:") || !strings.Contains(err.Error(), "has run on /v since") {
		t.Fatalf("health err = %v", err)
	}
	if n := strings.Count(fmt.Sprint(f.calls), " ps "); n != 1 {
		t.Fatalf("health polled %d times on a failed one-shot, want 1", n)
	}

	_, a = run(dump("1"), errors.New("exit status 1"))
	err = (lifecycleStep{a: a, op: "start"}).Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "docker compose up -d: exit status 1: service postgres-17-dump failed:") || !strings.Contains(err.Error(), "has run on /v since") {
		t.Fatalf("start err = %v", err)
	}
}
