package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
)

// AC6: the files each Ky app's setup places (its bundle and secrets) and its SCIM connector's
// pass, byte for byte what origin/master c25e426 produced. testdata/bundles.golden.json was
// written by this test run in a checkout of c25e426; -update-golden rewrites it.
func TestSetupBundlesGolden(t *testing.T) {
	ctx := context.Background()
	got := map[string]map[string]string{}
	newF := func() *fixture {
		f := newFixture(t, "../catalog/testdata/apps")
		f.o.Catalog = catalog.Embedded()
		return f
	}
	apply := func(steps map[string]engine.Step, ids ...string) {
		t.Helper()
		for _, id := range ids {
			if err := steps[id].Apply(ctx); err != nil {
				t.Fatalf("%s: %v", id, err)
			}
		}
	}

	f := newF()
	drivePlan(t, f, "192.0.2.1")
	id := &fakeIdentity{export: driveExport(driveSecret)}
	drive := &fakeDrive{files: map[string]string{}}
	_, ds := driveSteps(t, f, id.stream, drive)
	apply(ds, "kyidentity.setup", "kydrive.setup")
	scim := ds["kyidentity.scim.kydrive"].(scimStep)
	drive.files["connector "+scim.tokenFile+" "+scim.file] = string(scim.bundle)
	got["kydrive"] = drive.files

	f = newF()
	calendarPlan(t, f, "      clients: []\n")
	id = &fakeIdentity{exports: map[string]string{"kycalendar": calendarExport(parentSetup)}}
	cal := &fakeCalendar{files: map[string]string{}}
	cs := stepsByID(t, calendarSession(t, f, id, cal, false))
	apply(cs, "kyidentity.setup")
	id.checkReport = firstSetupCheck(ownerReused)
	apply(cs, "kycalendar.setup")
	got["kycalendar"] = cal.files

	f = newF()
	postPlan(t, f, "      clients: []\n")
	id = &fakeIdentity{export: postExport(parentSetup, postSecret)}
	post := &fakePost{files: map[string]string{}}
	ps := stepsByID(t, postSession(t, f, id, post))
	apply(ps, "kyidentity.setup", "kypost.setup")
	got["kypost"] = post.files

	f = newF()
	dnsPlan(t, f, "nas", "192.0.2.1", "192.168.1.20", dnsBundle)
	id = &fakeIdentity{export: dnsExport(parentSetup, clientSecret)}
	dnss, _ := dnsSession(t, f, id, &fakeDNS{root: f.h.root})
	dnss.edges = nil
	dsteps := stepsByID(t, dnss)
	listenSetupSocket(t, f.h.root)
	apply(dsteps, "kyidentity.setup", "kydns.setup")
	files := map[string]string{}
	dir := filepath.Join(f.h.root, "kydns/data/installer", dnsSetupID)
	for _, name := range []string{"client.secret", "directory.secret", "setup.json"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(b)
	}
	got["kydns"] = files

	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	const file = "testdata/bundles.golden.json"
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
		t.Errorf("setup files differ from %s:\n%s", file, b)
	}
}
