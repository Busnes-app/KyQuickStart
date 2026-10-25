package cli

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/catalog/catalogtest"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/steps.golden.json")

// Every step's input hash and KyIdentity's intent (its clients and connectors) for the Docker
// and Kubernetes Ky app plans, byte for byte those of origin/master 8ace9f6 except
// kydrive.deploy and kydrive.health, whose healthcheck gained start_interval: backup and
// restore change no other deployment or setup input. KyPost's native mail plans add their own
// entries and leave the others alone. #105's fsGroup fix moved the Kubernetes non-root apps'
// deploy and health hashes, so apply redeploys them once. Ky release pins are replaced
// (catalogtest.Unpinned), so following a release changes no hash here.
func TestStepInputsGolden(t *testing.T) {
	ctx := context.Background()
	got := map[string]map[string]string{}
	record := func(name string, identity *fakeIdentity, s *session) {
		t.Helper()
		list, err := s.deploySteps(ctx)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, st := range list {
			m[st.ID()] = st.InputHash()
			if st.ID() == "kyidentity.setup" {
				if err := st.Apply(ctx); err != nil {
					t.Fatal(err)
				}
			}
		}
		m["intent"] = identity.intent
		got[name] = m
	}
	newF := func() *fixture {
		f := newFixture(t, "../catalog/testdata/apps")
		f.o.Catalog = catalogtest.Unpinned(catalog.Embedded())
		return f
	}

	f := newF()
	drivePlan(t, f, "192.0.2.1")
	id := &fakeIdentity{export: driveExport(driveSecret)}
	s, _ := driveSteps(t, f, id.stream, &fakeDrive{files: map[string]string{}})
	record("kydrive", id, s)

	f = newF()
	notesPlan(t, f, "      clients: []\n")
	id = &fakeIdentity{}
	record("kynotes", id, notesSession(t, f, id, &fakeNotes{files: map[string]string{}, caps: notesFeatures}, false))

	f = newF()
	calendarPlan(t, f, "      clients: []\n")
	id = &fakeIdentity{}
	record("kycalendar", id, calendarSession(t, f, id, &fakeCalendar{files: map[string]string{}}, false))

	f = newF()
	dnsPlan(t, f, "nas", "192.0.2.1", "192.168.1.20", dnsBundle)
	id = &fakeIdentity{}
	ds, _ := dnsSession(t, f, id, &fakeDNS{})
	ds.edges = nil
	record("kydns", id, ds)

	f = newF()
	postPlan(t, f, "      clients: []\n")
	id = &fakeIdentity{}
	record("kypost", id, postSession(t, f, id, &fakePost{files: map[string]string{}}))

	// KyPost's native mail, without and then with its receiver, on a host with no earlier data.
	for name, mail := range map[string]string{"kypost-native": postMailNative, "kypost-receiver": postMailReceiver} {
		f = newF()
		f.h.fail = "docker volume inspect"
		postMailPlan(t, f, mail)
		if mail == postMailReceiver {
			os.MkdirAll(f.h.root+"/kypost", 0o700)
			os.WriteFile(f.h.root+"/kypost/compose.yaml", []byte("services:\n  app:\n    environment:\n      KYPOST_NATIVE_MAIL: \"true\"\n"), 0o600)
		}
		id = &fakeIdentity{}
		record(name, id, postSession(t, f, id, &fakePost{files: map[string]string{}, spool: true}))
	}

	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	const file = "testdata/steps.golden.json"
	if *updateGolden {
		if err := os.WriteFile(file, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(want) {
		t.Errorf("step inputs differ from %s:\n%s", file, b)
	}
}
