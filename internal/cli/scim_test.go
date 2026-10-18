package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	driveSCIMToken = "U2NpbVRva2VuU2NpbVRva2VuU2NpbVRva2VuU2NpbVQ"
	scimTokenFile  = "/data/installer/scim-kydrive.token"
	scimPassFile   = "/data/installer/scim-kydrive.json"
)

// fakeSCIMPass is KyIdentity's pod as the SCIM connector step sees it: its shell's files, and
// apply-setup on the pass bundle, which it records; everything else goes to identity.
type fakeSCIMPass struct {
	identity *fakeIdentity
	files    map[string]string
	applied  bool
	passes   []string // apply-setup arguments on the pass bundle
	read     string   // the pass bundle each apply-setup read
}

func (p *fakeSCIMPass) stream(ctx context.Context, namespace, pod, container string, args []string, o remotecommand.StreamOptions) error {
	if args[0] == "sh" {
		in, _ := io.ReadAll(o.Stdin)
		if args[1] != "-c" || args[2] != placeScript {
			return errors.New("unexpected script")
		}
		return fakePlace(p.files, args[4], args[5], in)
	}
	if f := slices.Index(args, "--file"); f < 0 || args[f+1] != scimPassFile {
		return p.identity.stream(ctx, namespace, pod, container, args, o)
	}
	p.passes = append(p.passes, strings.Join(args[1:], " "))
	p.read = p.files[scimPassFile]
	check := args[len(args)-1] == "--check"
	created := `[]`
	switch {
	case check && !p.applied:
		return errors.New("connector is not configured")
	case !check && !p.applied:
		p.applied, created = true, `[{"kind":"connector","name":"kydrive","id":"c0ffee"}]`
	}
	_, err := io.WriteString(o.Stdout, `{"schema":"kyidentity.setup/1","created":`+created+`,"reused":[{"kind":"client","name":"kydrive","id":"kydrive"}]}`)
	return err
}

// writeDriveToken places the SCIM token KyDrive's secrets step generated on its Docker target.
func writeDriveToken(t *testing.T, f *fixture, token string) {
	t.Helper()
	dir := filepath.Join(f.h.root, "kydrive", "secrets")
	os.MkdirAll(dir, 0o700)
	os.Chmod(filepath.Join(f.h.root, "kydrive"), 0o700)
	os.Remove(filepath.Join(dir, "scim_token"))
	if err := os.WriteFile(filepath.Join(dir, "scim_token"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
}

// KyIdentity's SCIM connector to KyDrive: once KyDrive is set up, KyDrive's token is read on
// its Docker target, copied into KyIdentity's pod and named by a pass of KyIdentity's first
// setup that adds scim to KyDrive's client and nothing else.
func TestKyDriveSCIMConnector(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	drivePlan(t, f, "192.0.2.1")
	writeDriveToken(t, f, driveSCIMToken)
	identity := &fakeSCIMPass{identity: &fakeIdentity{export: driveExport(driveSecret)}, files: map[string]string{}}
	drive := &fakeDrive{files: map[string]string{}}
	s, steps := driveSteps(t, f, identity.stream, drive)

	list, err := s.deploySteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	order := ids(list)
	if order[len(order)-1] != "kyidentity.scim.kydrive" || slices.Index(order, "kydrive.setup") > slices.Index(order, "kyidentity.scim.kydrive") {
		t.Fatalf("steps = %v", order)
	}
	connector := steps["kyidentity.scim.kydrive"]
	if done, _ := connector.Inspect(ctx); done || len(identity.files) != 0 {
		t.Fatal("connector inspected as done, or inspection wrote")
	}
	for _, id := range []string{"kyidentity.setup", "kydrive.setup", "kyidentity.scim.kydrive"} {
		if err := steps[id].Apply(ctx); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if done, _ := connector.Inspect(ctx); !done {
		t.Fatal("applied connector not inspected as done")
	}
	if identity.files[scimTokenFile] != driveSCIMToken {
		t.Fatalf("KyIdentity's copy of the token = %q", identity.files[scimTokenFile])
	}
	const scope = "/data/installer/271333e2-c221-4601-b249-563b3d828530"
	const wantPass = `{"admin":{"username":"owner-admin","email":"admin@example.test","passwordFile":"` + scope + `/admin.password"},` +
		`"clients":[{"id":"kydrive","name":"KyDrive","redirectUris":["https://drive.example.com/api/sso/kysignon/callback"],"postLogoutUris":null,` +
		`"administratorRole":"kydrive.admin","everydayRole":"kydrive.user","scim":{"url":"https://drive.example.com/scim/v2","tokenFile":"` + scimTokenFile + `"},` +
		`"secretFile":"` + scope + `/client-kydrive.secret"}],` +
		`"everyday":{"username":"owner","email":"owner@example.test","passwordFile":"` + scope + `/everyday.password"},` +
		`"schema":"kyidentity.setup/1","setupId":"271333e2-c221-4601-b249-563b3d828530"}`
	if identity.read != wantPass || identity.files[scimPassFile] != wantPass {
		t.Fatalf("setup pass =\n%s\nwant\n%s", identity.read, wantPass)
	}
	if got := strings.Join(identity.passes, "\n"); got != "apply-setup --file "+scimPassFile+"\napply-setup --file "+scimPassFile+" --check" {
		t.Fatalf("passes:\n%s", got)
	}
	// The first setup's own intent stays without scim: KyIdentity keeps it immutable.
	if strings.Contains(identity.identity.intent, "scim") {
		t.Fatalf("first setup intent carries scim: %s", identity.identity.intent)
	}
	if strings.Contains(f.out.String(), driveSCIMToken) {
		t.Error("token printed")
	}
	filepath.WalkDir(f.state, func(p string, d os.DirEntry, err error) error {
		if b, _ := os.ReadFile(p); err == nil && !d.IsDir() && strings.Contains(string(b), driveSCIMToken) {
			t.Errorf("token written to %s", p)
		}
		return nil
	})

	// A token changed on KyDrive's target never replaces KyIdentity's copy.
	writeDriveToken(t, f, "Q2hhbmdlZENoYW5nZWRDaGFuZ2VkQ2hhbmdlZENoYW4")
	if err := connector.Apply(ctx); err == nil || !strings.Contains(err.Error(), "never replaced") || identity.files[scimTokenFile] != driveSCIMToken {
		t.Fatalf("changed token: %v", err)
	}
	if done, _ := connector.Inspect(ctx); done {
		t.Fatal("changed token inspected as done")
	}
	// An unreadable token on the target stops the step before KyIdentity's pod is touched.
	os.Chmod(filepath.Join(f.h.root, "kydrive", "secrets", "scim_token"), 0o644)
	delete(identity.files, scimTokenFile)
	if err := connector.Apply(ctx); err == nil || !strings.Contains(err.Error(), "SCIM token on its target") || identity.files[scimTokenFile] != "" {
		t.Fatalf("unsafe token: %v", err)
	}
}
