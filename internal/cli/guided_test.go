package cli

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

func TestGuidedPlanResumeExistingAndReview(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	o := Options{Catalog: catalog.Embedded(), In: strings.NewReader("yes\n1\nnas\ndocker\n192.0.2.1\nkyq\n22\n/opt/test\ninstall\nnas\n1\nkyidentity\nnas\nhttps://id.example.test\nnone\nyes\nfinish\n"), Out: &out}
	if err := Run(context.Background(), []string{"plan", "--demo", "--state", dir}, o); err != nil {
		t.Fatal(err, out.String())
	}
	st, err := stack.Load(filepath.Join(dir, "stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !st.Demo || len(st.Apps) != 1 || len(st.Existing) != 1 || st.Existing[0].Product != "kyidentity" || st.Revision != 1 {
		t.Fatal("plan lost selections or existing reference, or is not revision 1")
	}
	fi, _ := os.Stat(filepath.Join(dir, "stack.yaml"))
	if fi.Mode().Perm() != 0600 {
		t.Fatal("plan permissions")
	}
	for _, text := range []string{"DEMO", "integration pending", "Install hello on nas", "Next commands"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("missing %s", text)
		}
	}
	out.Reset()
	o.In = strings.NewReader(strings.Repeat("\n", 17))
	if err := Run(context.Background(), []string{"plan", "--state", dir}, o); err != nil {
		t.Fatal(err, out.String())
	}
	if st, err := stack.Load(filepath.Join(dir, "stack.yaml")); err != nil || st.Revision != 2 {
		t.Fatalf("edited plan revision: %v", err)
	}
	out.Reset()
	o.In = nil
	if err := Run(context.Background(), []string{"plan", "--noninteractive", "--state", dir}, o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Existing kyidentity") {
		t.Fatal("review lost reference")
	}
}

func TestGuidedInterruptedResumesWithoutPublishing(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	o := Options{Catalog: catalog.Embedded(), In: strings.NewReader("yes\n1\nnas\n"), Out: &out}
	if err := Run(context.Background(), []string{"plan", "--demo", "--state", dir}, o); err == nil {
		t.Fatal("EOF was treated as consent")
	}
	if _, err := os.Stat(filepath.Join(dir, "stack.yaml")); !os.IsNotExist(err) {
		t.Fatal("partial plan published")
	}
	if _, err := os.Stat(filepath.Join(dir, "plan-answers.json")); err != nil {
		t.Fatal("no resume answers")
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.lock")); !os.IsNotExist(err) {
		t.Fatal("plan lock left behind")
	}
	// First answers now default to those saved before interruption.
	o.In = strings.NewReader("\n\n\n\n192.0.2.1\n\n\n\ninstall\n\n0\nnone\nyes\nfinish\n")
	if err := Run(context.Background(), []string{"plan", "--state", dir}, o); err != nil {
		t.Fatal(err, out.String())
	}
}

func TestGuidedDeclinedEditPreservesPublishedPlan(t *testing.T) {
	dir := t.TempDir()
	old := []byte("demo: true\n" + stackYAML)
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), old, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	o := Options{Catalog: catalog.Embedded(), In: strings.NewReader("\n\n\n\n\n\n\n\nskip\n0\nno\n"), Out: &out}
	if err := Run(context.Background(), []string{"plan", "--state", dir}, o); err != nil {
		t.Fatal(err, out.String())
	}
	b, _ := os.ReadFile(filepath.Join(dir, "stack.yaml"))
	if !bytes.Equal(b, old) {
		t.Fatal("declining changed published plan")
	}
}

func TestExistingReferencesDoNotAuthorizeApply(t *testing.T) {
	dir := t.TempDir()
	inventory := "version: 1\ntargets:\n- name: nas\n  host: 192.0.2.1\n  user: kyq\nexisting:\n- product: kyidentity\n  target: nas\n  url: https://id.example.test\n"
	os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(inventory), 0600)
	var out bytes.Buffer
	err := Run(context.Background(), []string{"apply", "--state", dir}, Options{Catalog: catalog.Embedded(), Out: &out})
	if err == nil || !strings.Contains(err.Error(), "do not authorize") {
		t.Fatalf("reference allowed apply: %v", err)
	}
}

func TestGuidedRejectsCredentialsBeforeSaving(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	answers := "no\n1\nnas\ndocker\n192.0.2.1\nkyq\n22\n/opt/test\n\nskip\nskip\nskip\nskip\nskip\nskip\nskip\nskip\nskip\nskip\nskip\nskip\n1\nkyidentity\nnas\nhttps://user:private-password@id.example.test\nhttps://id.example.test\nno\nyes\nfinish\n"
	err := Run(context.Background(), []string{"plan", "--state", dir}, Options{Catalog: catalog.Embedded(), In: strings.NewReader(answers), Out: &out})
	if err != nil {
		t.Fatal(err, out.String())
	}
	for _, f := range []string{"stack.yaml", "plan-answers.json"} {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		if bytes.Contains(b, []byte("private-password")) {
			t.Fatal("credential saved")
		}
	}
}

func TestGuidedExistingEdgeChoicesRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []string
	}{
		{"frp", []string{"frp", "https://npm.example.test", "172.30.0.10/32", "8443", "yes", "npm-net", "npm", "frp.example.test", "7000", "yes", "hello.example.test", "hello", "8080", "http", "12", "/api/status"}},
		{"cloudflare", []string{"cloudflare", "https://npm.example.test", "172.30.0.10/32", "yes", "npm-net", "npm", "12345678-1234-1234-1234-123456789012", "yes", "hello.example.test", "hello", "8080", "http", "12", "/api/status"}},
		{"lan", []string{"lan", "https://npm.example.test", "192.168.1.0/24, fd00:1::/48", "30543", "yes", "hello.example.test", "hello", "8080", "http", "12", "/api/status"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := stack.Parse([]byte(stackYAML))
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			p := &prompts{ctx: context.Background(), in: bufio.NewReader(strings.NewReader(strings.Join(tc.answers, "\n") + "\n")), out: &out, answers: map[string]string{}, draft: filepath.Join(t.TempDir(), "answers.json")}
			if err := guidedEdges(p, st, nil); err != nil {
				t.Fatal(err, out.String())
			}
			data, _ := yaml.Marshal(st)
			if _, err := stack.Parse(data); err != nil {
				t.Fatal(err)
			}
			first := st.Edges[0]
			seedEdges(p.answers, st.Edges)
			st.Edges = nil
			p.in = bufio.NewReader(strings.NewReader(strings.Repeat("\n", len(tc.answers))))
			if err := guidedEdges(p, st, nil); err != nil {
				t.Fatal(err, out.String())
			}
			if !reflect.DeepEqual(first, st.Edges[0]) {
				t.Fatal("editing defaults lost an edge choice")
			}
		})
	}
}

func TestGuidedCancellationReleasesEditorLock(t *testing.T) {
	dir := t.TempDir()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	err := Run(ctx, []string{"plan", "--state", dir}, Options{Catalog: catalog.Embedded(), In: reader, Out: &out})
	if err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.lock")); !os.IsNotExist(err) {
		t.Fatal("cancel left editor locked")
	}
}

func TestComputerDNSNamesAndSSHDefault(t *testing.T) {
	for _, name := range []string{"K80.example.com", "K80", "nas.example.lan.", "1host.example.lan", strings.Repeat("a", 63) + ".example.lan"} {
		label, ok := computerName(name)
		if !ok || !stack.ValidName(label) {
			t.Fatalf("computer %q produced invalid label %q", name, label)
		}
	}
	for _, name := range []string{"bad;command", "foo..example", "-host.example", "https://host.example"} {
		if _, ok := computerName(name); ok {
			t.Fatalf("unsafe computer accepted: %s", name)
		}
	}
	dir := t.TempDir()
	var out bytes.Buffer
	answers := "yes\n1\nK80.example.com\ndocker\n\nkyq\n22\n/opt/test\ninstall\nK80.example.com\n1\nkyidentity\nK80.example.com\nhttps://id.example.test\nnone\nyes\nfinish\n"
	err := Run(context.Background(), []string{"plan", "--demo", "--state", dir}, Options{Catalog: catalog.Embedded(), In: strings.NewReader(answers), Out: &out})
	if err != nil {
		t.Fatal(err, out.String())
	}
	st, err := stack.Load(filepath.Join(dir, "stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Targets[0].Name != "k80-example-com" || st.Targets[0].Host != "K80.example.com" || st.Apps[0].Target != "k80-example-com" {
		t.Fatal("computer DNS name did not flow through target selection")
	}
	if st.Existing[0].Target != "k80-example-com" {
		t.Fatal("existing product target did not accept the DNS name")
	}
}

func TestPlanDeclaresClusterCredentialGenerator(t *testing.T) {
	st := &stack.Stack{Targets: []stack.Target{{Name: "cluster", Kubeconfig: "/outside/kubeconfig"}}, Apps: []stack.App{{Name: "hello", Target: "cluster"}}}
	cat := catalog.Catalog{"hello": {Manifest: catalog.Manifest{Name: "hello", Secrets: []string{"token"},
		Kubernetes: &catalog.Kubernetes{RunAs: 100, Volumes: []catalog.Volume{{Name: "data"}}}}}}
	var out bytes.Buffer
	showPlan(&out, st, cat)
	if !strings.Contains(out.String(), "Required temporary credential generator:") || !strings.Contains(out.String(), "Required volume init: "+kube.CredentialGeneratorImage) {
		t.Fatal("required helper image missing from installation plan")
	}
}

func TestGuidedKubernetesStorageClassResumes(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kc, []byte("not: a kubeconfig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	o := Options{Catalog: catalog.Embedded(), In: strings.NewReader("yes\n1\nk1\nkubernetes\n" + kc + "\n\nlocal-path\ninstall\nk1\n0\nnone\nyes\nfinish\n"), Out: &out}
	if err := Run(context.Background(), []string{"plan", "--demo", "--state", dir}, o); err != nil {
		t.Fatal(err, out.String())
	}
	st, err := stack.Load(filepath.Join(dir, "stack.yaml"))
	if err != nil || st.Targets[0].StorageClass != "local-path" {
		t.Fatalf("storage class not saved: %v %+v", err, st)
	}
	if !strings.Contains(out.String(), "Volumes use StorageClass local-path") {
		t.Fatal("review hides the storage class")
	}
	// Resume from stack.yaml alone, as on another workstation.
	if err := os.Remove(filepath.Join(dir, "plan-answers.json")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	o.In = strings.NewReader(strings.Repeat("\n", 20))
	if err := Run(context.Background(), []string{"plan", "--state", dir}, o); err != nil {
		t.Fatal(err, out.String())
	}
	if st, err := stack.Load(filepath.Join(dir, "stack.yaml")); err != nil || st.Targets[0].StorageClass != "local-path" || st.Revision != 2 {
		t.Fatalf("resume lost storage class: %v %+v", err, st)
	}
	if !strings.Contains(out.String(), "StorageClass for app volumes (blank uses the cluster default) [local-path]") {
		t.Fatal("resume did not seed the storage class")
	}
}
