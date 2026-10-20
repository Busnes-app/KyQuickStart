package dockerhost

import (
	"context"
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

// An after_setup service crash-looping before its setup step neither fails deploy's check nor
// holds apply's health step; restore's Start and backup's restart still wait for it.
func TestAfterSetupSkippedOnlyByApply(t *testing.T) {
	pollInterval = time.Millisecond
	ps := `{"Service":"web","State":"running","Health":"healthy"}` + "\n" + `{"Service":"bridge","State":"restarting","Health":"unhealthy"}`
	hash := ""
	f := &fakeRunner{reply: func(cmd string) ([]byte, error) {
		if strings.Contains(cmd, ".input-hash") {
			return []byte(hash), nil
		}
		return []byte(ps), nil
	}}
	a := fakeApp(f)
	a.Catalog.Services = []string{"bridge", "web"}
	a.Catalog.Health.AfterSetup = []string{"bridge"}
	steps := Steps(a)
	hash = steps[0].InputHash()
	ctx := context.Background()
	for _, s := range steps {
		if err := s.Verify(ctx); err != nil {
			t.Errorf("%s: %v", s.ID(), err)
		}
	}
	if err := Healthy(ctx, a, "bridge"); err == nil {
		t.Error("an unhealthy bridge passed")
	}
	if err := WaitHealthy(ctx, a, "bridge"); err == nil || !strings.Contains(err.Error(), "service bridge is not running") {
		t.Errorf("Start's wait: %v", err)
	}
	if err := (Store{App: a}).restart(ctx); err == nil {
		t.Error("restart did not wait for the bridge")
	}
	a.Catalog.Health.AfterSetup = nil
	if err := Steps(a)[1].Verify(ctx); err == nil {
		t.Error("without after_setup the bridge was skipped")
	}
}
