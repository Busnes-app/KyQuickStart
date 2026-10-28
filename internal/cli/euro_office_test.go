package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const officeRoute = "      - app: euro-office\n        hostname: office.example.com\n        forward_host: app.kyq-euro-office.svc.cluster.local\n        forward_port: 80\n        certificate_id: 1\n"

// editorPlan is drivePlan with euro-office on the cluster, routed at office.example.com.
func editorDrivePlan(t *testing.T, f *fixture) {
	t.Helper()
	drivePlan(t, f, "192.0.2.1")
	file := filepath.Join(f.state, "stack.yaml")
	b, _ := os.ReadFile(file)
	y := strings.Replace(string(b), "  - name: kydrive\n    target: nas\n", "  - name: kydrive\n    target: nas\n  - name: euro-office\n    target: k1\n", 1)
	y = strings.Replace(y, "integrations:\n", officeRoute+"integrations:\n", 1)
	if err := os.WriteFile(file, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
}

// With euro-office, KyDrive's Compose file gains the editor's URL and its secret, exported by
// the entrypoint like the session secret; without it, KyDrive is the catalog's, byte for byte
// (and TestStepInputsGolden pins its step inputs).
func TestEditorConnectsKyDrive(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	drivePlan(t, f, "192.0.2.1")
	plain, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	cat, _ := catalog.Load(catalog.Embedded())
	want := cat["kydrive"].WithRoute(catalog.Route{Host: "drive.example.com", Forward: "192.0.2.1:18443", ForwardAddr: "192.0.2.1"}).WithPrivateRecovery(false)
	if !bytes.Equal(plain.cat["kydrive"].Compose, want.Compose) || !slices.Equal(plain.cat["kydrive"].Secrets, cat["kydrive"].Secrets) {
		t.Fatal("KyDrive changed without euro-office")
	}

	editorDrivePlan(t, f)
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	drive := s.cat["kydrive"]
	if !slices.Equal(drive.Secrets, []string{"session_secret", "scim_token", "editor_secret"}) || !slices.Equal(cat["kydrive"].Secrets, []string{"session_secret", "scim_token"}) {
		t.Fatalf("secrets = %v", drive.Secrets)
	}
	// The image pin is the catalog's, not the editor's; catalog-follow bumps it.
	compose := bytes.ReplaceAll(drive.Compose, []byte(cat["kydrive"].Images["kydrive"]), []byte("KYDRIVE_IMAGE"))
	const file = "testdata/kydrive-editor.compose.golden"
	if *updateGolden {
		os.WriteFile(file, compose, 0o644)
	}
	golden, _ := os.ReadFile(file)
	if !bytes.Equal(compose, golden) {
		t.Fatalf("KyDrive's Compose file with the editor differs from %s:\n%s", file, compose)
	}
	for _, line := range []string{
		`      KYDRIVE_EDITOR_URL: "https://office.example.com"`,
		"    secrets: [session_secret, scim_token, editor_secret]",
		"e=$$(cat /run/secrets/editor_secret) || exit 1; KYDRIVE_EDITOR_SECRET=$$e; export KYDRIVE_EDITOR_SECRET; unset e; exec /app/kydrive-server",
		"  editor_secret:\n    file: ./secrets/editor_secret",
	} {
		if !strings.Contains(string(drive.Compose), line) {
			t.Errorf("compose lacks %q", line)
		}
	}
	if strings.Contains(string(drive.Compose), "KYDRIVE_EDITOR_SECRET:") {
		t.Error("the editor secret is in Compose's environment")
	}
	// The editor's route carries its catalog path blocks to the edge; KyDrive's carries none.
	for _, r := range s.edges[0].Routes {
		if want := cat[r.App].DenyPaths; !slices.Equal(r.DenyPaths, want) || r.App == euroOffice && len(want) != 2 {
			t.Fatalf("%s route deny paths = %q", r.App, r.DenyPaths)
		}
	}
	// A KyDrive template without the expected lines is refused, not half-edited.
	broken := cat["kydrive"]
	broken.Compose = bytes.Replace(broken.Compose, []byte("unset s; exec"), []byte("exec"), 1)
	if _, err := withEditor(broken, "office.example.com"); err == nil || !strings.Contains(err.Error(), "no longer has") {
		t.Fatalf("broken template = %v", err)
	}
}

// euro-office needs KyDrive, a Kubernetes target and exactly one route; plan and review refuse
// anything else before connecting.
func TestEditorPlanRefusals(t *testing.T) {
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(kc, nil, 0o600)
	base := "version: 1\ntargets:\n  - name: nas\n    host: 192.0.2.1\n    user: kyq\n    root: /srv/kyq\n  - name: k1\n    kubeconfig: " + kc + "\n"
	apps := "apps:\n  - name: npm\n    target: k1\n  - name: kyidentity\n    target: k1\n  - name: kydrive\n    target: nas\n"
	edges := "edges:\n  - target: k1\n    provider: lan\n    npm_url: kube://kyq-npm/app:81\n    allow_cidrs: [192.168.1.0/24]\n    routes:\n" +
		"      - app: kyidentity\n        hostname: identity.example.com\n        forward_host: app.kyq-kyidentity.svc.cluster.local\n        forward_port: 5867\n        certificate_id: 1\n" +
		"      - app: kydrive\n        hostname: drive.example.com\n        forward_host: 192.0.2.1\n        forward_port: 18443\n        certificate_id: 1\n"
	second := "      - app: euro-office\n        hostname: docs.example.com\n        forward_host: app.kyq-euro-office.svc.cluster.local\n        forward_port: 80\n        certificate_id: 1\n"
	cases := map[string]struct{ y, want string }{
		"ok":            {base + apps + "  - name: euro-office\n    target: k1\n" + edges + officeRoute, ""},
		"no kydrive":    {base + strings.Replace(apps, "  - name: kydrive\n    target: nas\n", "", 1) + "  - name: euro-office\n    target: k1\n" + strings.Split(edges, "      - app: kydrive")[0] + officeRoute, `needs "kydrive"`},
		"on docker":     {base + apps + "  - name: euro-office\n    target: nas\n" + edges, "no Compose deployment"},
		"no route":      {base + apps + "  - name: euro-office\n    target: k1\n" + edges, "exactly one edge route"},
		"two routes":    {base + apps + "  - name: euro-office\n    target: k1\n" + edges + officeRoute + second, "exactly one edge route"},
		"without it ok": {base + apps + edges, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			st, err := stack.Parse([]byte(c.y))
			if err != nil {
				t.Fatal(err)
			}
			err = validateSelection(st, cat, Options{})
			if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// euro-office runs after KyDrive, and its jwt_secret is KyDrive's editor_secret, read from
// KyDrive's target into memory: copied once, then only compared, never replaced or printed.
func TestEditorSecretTransfer(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	editorDrivePlan(t, f)
	identity := &fakeIdentity{export: driveExport(driveSecret)}
	s, _ := driveSteps(t, f, identity.stream, &fakeDrive{files: map[string]string{}})
	list, err := s.deploySteps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, st := range list {
		ids = append(ids, st.ID())
	}
	if i := slices.Index(ids, "euro-office.secrets"); i < 0 || slices.Index(ids, "kydrive.setup") > i || ids[i+1] != "euro-office.deploy" {
		t.Fatalf("steps = %v", ids)
	}

	// KyDrive's target generated the secret; the editor's cluster holds the other already.
	secrets := filepath.Join(f.h.root, "kydrive", "secrets")
	os.MkdirAll(secrets, 0o700)
	value := "Q0FGRUJBQkVDQUZFQkFCRUNBRkVCQUJFQ0FGRUJBQkU"
	os.WriteFile(filepath.Join(secrets, "editor_secret"), []byte(value), 0o600)
	labels := map[string]string{managed.Label: managed.Value}
	f.cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kyq-euro-office", Labels: labels}}, metav1.CreateOptions{})
	f.cs.CoreV1().Secrets("kyq-euro-office").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kyq-secrets", Namespace: "kyq-euro-office", Labels: labels},
		Data:       map[string][]byte{"secure_link_secret": []byte("U0VDVVJFTElOS1NFQ1VSRUxJTktTRUNVUkVMSU5LU0U")},
	}, metav1.CreateOptions{})
	s.drivers["k1"] = kubeDriver{c: kube.New(f.cs)}
	var step interface {
		Apply(context.Context) error
		Verify(context.Context) error
	}
	for _, st := range s.editorSteps(s.drivers["k1"].(kubeDriver)) {
		if st.ID() == "euro-office.secrets" {
			step = st
		}
	}
	if err := step.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	sec, _ := f.cs.CoreV1().Secrets("kyq-euro-office").Get(ctx, "kyq-secrets", metav1.GetOptions{})
	if string(sec.Data["jwt_secret"]) != value || len(sec.Data["secure_link_secret"]) != 43 {
		t.Fatalf("secret = %q", sec.Data)
	}
	if err := step.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if s.redact.Redact(value) != "[redacted]" || strings.Contains(f.out.String(), value) {
		t.Fatal("the shared secret is printable")
	}
	// Changed on KyDrive's side: refused, and the editor's copy kept.
	os.WriteFile(filepath.Join(secrets, "editor_secret"), []byte("UkVQTEFDRURSRVBMQUNFRFJFUExBQ0VEUkVQTEFDRUQ"), 0o600)
	if err := step.Verify(ctx); err == nil || !strings.Contains(err.Error(), "differs from its source") {
		t.Fatalf("changed source = %v", err)
	}
	if sec, _ := f.cs.CoreV1().Secrets("kyq-euro-office").Get(ctx, "kyq-secrets", metav1.GetOptions{}); string(sec.Data["jwt_secret"]) != value {
		t.Fatal("the editor's secret was replaced")
	}
	// An unsafe source file is refused by KyDrive's target driver.
	os.Chmod(filepath.Join(secrets, "editor_secret"), 0o644)
	if err := step.Verify(ctx); err == nil {
		t.Fatal("a world-readable source was accepted")
	}
}

// Guided planning offers euro-office right after KyDrive, and only when KyDrive installs.
func TestGuidedOffersEditorWithKyDrive(t *testing.T) {
	ask := func(drive string) string {
		var out bytes.Buffer
		in := "no\n1\nnas\ndocker\n192.0.2.1\nkyq\n22\n/opt/test\n\nskip\nskip\n" + drive
		Run(context.Background(), []string{"plan", "--state", t.TempDir()}, Options{Catalog: catalog.Embedded(), In: strings.NewReader(in), Out: &out})
		return out.String()
	}
	out := ask("install\nnas\n")
	drive, editor, identity := strings.Index(out, "\nkydrive —"), strings.Index(out, "\neuro-office —"), strings.Index(out, "\nkyidentity —")
	if drive < 0 || editor < drive || identity >= 0 && identity < editor || !strings.Contains(out, "Required products: kydrive\nOptional for KyDrive. It runs on a Kubernetes target only") {
		t.Fatalf("euro-office not offered after KyDrive:\n%s", out)
	}
	if out := ask("skip\n"); strings.Contains(out, "euro-office") || !strings.Contains(out, "\nkyidentity —") {
		t.Fatalf("euro-office offered without KyDrive:\n%s", out)
	}
}

// Removing euro-office takes KyDrive as the remaining plan deploys it: without the editor, byte
// for byte the KyDrive of a plan that never had one. A stopped KyDrive is refused, since the
// redeploy would start it; other apps need nothing.
func TestDriveWithoutEditor(t *testing.T) {
	f := newFixture(t, "../catalog/testdata/apps")
	f.o.Catalog = catalog.Embedded()
	drivePlan(t, f, "192.0.2.1")
	plain, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	editorDrivePlan(t, f)
	s, err := load(f.state, nil, f.o)
	if err != nil {
		t.Fatal(err)
	}
	next := f.stack(t)
	next.Apps = slices.DeleteFunc(next.Apps, func(a stack.App) bool { return a.Name == euroOffice })
	for i := range next.Edges {
		next.Edges[i].Routes = slices.DeleteFunc(next.Edges[i].Routes, func(r stack.Route) bool { return r.App == euroOffice })
	}
	drive, err := s.driveWithoutEditor(euroOffice, next, f.state, nil)
	if err != nil || drive == nil || !bytes.Equal(drive.Compose, plain.cat["kydrive"].Compose) || !slices.Equal(drive.Secrets, plain.cat["kydrive"].Secrets) {
		t.Fatalf("KyDrive without the editor = %v, %v", drive, err)
	}
	if drive, err := s.driveWithoutEditor("kyidentity", next, f.state, nil); drive != nil || err != nil {
		t.Fatalf("another app = %v, %v", drive, err)
	}
	for i := range s.plan.Apps {
		s.plan.Apps[i].Stopped = s.plan.Apps[i].Name == "kydrive"
	}
	if _, err := s.driveWithoutEditor(euroOffice, next, f.state, nil); code(err) != suite.AppStopped {
		t.Fatalf("stopped KyDrive = %v", err)
	}
}
