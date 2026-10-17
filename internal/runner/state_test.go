package runner

import (
	"archive/tar"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/stack"
	"golang.org/x/crypto/ssh"
)

type entry struct {
	name string
	body string
	typ  byte
	size int64 // header size when not len(body)
}

func tarOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o600, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.typ != 0 {
			h.Typeflag, h.Size, h.Linkname = e.typ, 0, "/etc/passwd"
		}
		if e.size != 0 {
			h.Size = e.size
		}
		tw.WriteHeader(h)
		tw.Write([]byte(e.body))
	}
	tw.Close()
	return b.Bytes()
}

const kubePlan = "version: 1\nrevision: 4\ntargets:\n  - name: nas\n    host: 192.0.2.1\n    user: kyq\n  - name: k1\n    kubeconfig: /home/op/.kube/config\n    context: admin@k1\n    storage_class: local-path\napps: []\n"

const tokenKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: cluster
  cluster: {server: "https://k1.lan:6443", certificate-authority-data: Q0E=}
users:
- name: runner
  user: {token: abc}
contexts:
- name: runner
  context: {cluster: cluster, user: runner}
current-context: runner
`

func newState(t *testing.T) string {
	t.Helper()
	s, err := openState(filepath.Join(shortDir(t), "state"), false)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestImportRewritesKubeconfigsIntoTheRunner(t *testing.T) {
	state := newState(t)
	err := importPlan(state, bytes.NewReader(tarOf(t,
		entry{name: "stack.yaml", body: kubePlan},
		entry{name: "known_hosts", body: "[192.0.2.1]:22 ssh-ed25519 AAAA\n"},
		entry{name: "results/hello.deploy.json", body: `{"status":"ok"}`},
		entry{name: "kubeconfigs/k1.yaml", body: tokenKubeconfig},
	)))
	if err != nil {
		t.Fatal(err)
	}
	st, err := stack.Load(filepath.Join(state, planDir, "stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	k1, _ := st.Target("k1")
	if st.Revision != 4 || k1.Kubeconfig != filepath.Join(state, kubeDir, "k1.yaml") || k1.Context != "" || k1.StorageClass != "local-path" {
		t.Errorf("imported plan = %+v", st)
	}
	for _, f := range []string{filepath.Join(planDir, "known_hosts"), filepath.Join(planDir, "results", "hello.deploy.json"), filepath.Join(kubeDir, "k1.yaml"), filepath.Join(planDir, "stack.yaml")} {
		fi, err := os.Stat(filepath.Join(state, f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", f, err, fi)
		}
	}
	// The workstation's plan dir stays secret-free: kubeconfigs live beside it.
	if b, _ := os.ReadFile(filepath.Join(state, planDir, "stack.yaml")); strings.Contains(string(b), "abc") {
		t.Error("token in the plan")
	}
	// A rerun at the same revision is accepted; an older plan is refused and changes nothing.
	same := tarOf(t, entry{name: "stack.yaml", body: kubePlan}, entry{name: "kubeconfigs/k1.yaml", body: tokenKubeconfig})
	if err := importPlan(state, bytes.NewReader(same)); err != nil {
		t.Fatalf("same revision: %v", err)
	}
	older := tarOf(t, entry{name: "stack.yaml", body: strings.Replace(kubePlan, "revision: 4", "revision: 3", 1)}, entry{name: "kubeconfigs/k1.yaml", body: tokenKubeconfig})
	if err := importPlan(state, bytes.NewReader(older)); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("older revision: %v", err)
	}
	if st, _ := stack.Load(filepath.Join(state, planDir, "stack.yaml")); st.Revision != 4 {
		t.Error("an older plan replaced a newer one")
	}
	var out bytes.Buffer
	if err := export(state, &out); err != nil || !strings.Contains(out.String(), "revision: 4") {
		t.Errorf("export: %v %s", err, out.String())
	}
}

func TestImportRefusesUnsafeArchives(t *testing.T) {
	plan := entry{name: "stack.yaml", body: strings.Replace(demoPlan, "demo: true\n", "", 1)}
	for name, archive := range map[string][]byte{
		"symlink":            tarOf(t, plan, entry{name: "known_hosts", typ: tar.TypeSymlink}),
		"hard link":          tarOf(t, plan, entry{name: "known_hosts", typ: tar.TypeLink}),
		"directory":          tarOf(t, entry{name: "results/", typ: tar.TypeDir}, plan),
		"dot-dot":            tarOf(t, plan, entry{name: "results/../../runner.json", body: "{}"}),
		"dot-dot result":     tarOf(t, plan, entry{name: "../plan/results/x.json", body: "{}"}),
		"absolute":           tarOf(t, plan, entry{name: "/state/plan/stack.yaml", body: plan.body}),
		"unexpected":         tarOf(t, plan, entry{name: "ssh/id_ed25519", body: "key"}),
		"nested result":      tarOf(t, plan, entry{name: "results/a/b.json", body: "{}"}),
		"hidden result":      tarOf(t, plan, entry{name: "results/.x.json", body: "{}"}),
		"oversize result":    tarOf(t, plan, entry{name: "results/a.json", body: strings.Repeat("a", maxSmall+1)}),
		"oversize header":    tarOf(t, plan, entry{name: "known_hosts", body: "x", size: maxPlan + 1}),
		"duplicate":          tarOf(t, plan, plan),
		"no plan":            tarOf(t, entry{name: "known_hosts", body: "x"}),
		"invalid plan":       tarOf(t, entry{name: "stack.yaml", body: "version: 2\n"}),
		"stray kubeconfig":   tarOf(t, plan, entry{name: "kubeconfigs/nas.yaml", body: tokenKubeconfig}),
		"missing kubeconfig": tarOf(t, entry{name: "stack.yaml", body: kubePlan}),
		"exec kubeconfig": tarOf(t, entry{name: "stack.yaml", body: kubePlan}, entry{name: "kubeconfigs/k1.yaml",
			body: strings.Replace(tokenKubeconfig, "{token: abc}", "{token: abc, exec: {command: /bin/sh, apiVersion: client.authentication.k8s.io/v1}}", 1)}),
		"insecure kubeconfig": tarOf(t, entry{name: "stack.yaml", body: kubePlan}, entry{name: "kubeconfigs/k1.yaml",
			body: strings.Replace(tokenKubeconfig, "certificate-authority-data: Q0E=", "insecure-skip-tls-verify: true", 1)}),
		"ca file kubeconfig": tarOf(t, entry{name: "stack.yaml", body: kubePlan}, entry{name: "kubeconfigs/k1.yaml",
			body: strings.Replace(tokenKubeconfig, "certificate-authority-data: Q0E=", "certificate-authority: /etc/ca", 1)}),
		"not a tar": []byte("stack.yaml"),
	} {
		state := newState(t)
		if err := importPlan(state, bytes.NewReader(archive)); err == nil {
			t.Errorf("%s: accepted", name)
		}
		for _, d := range []string{planDir, kubeDir, filepath.Join(planDir, "results")} {
			if entries, _ := os.ReadDir(filepath.Join(state, d)); len(entries) > 0 && !(d == planDir && len(entries) == 1 && entries[0].Name() == "results") {
				t.Errorf("%s: wrote %s: %v", name, d, entries)
			}
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(state), "runner.json")); err == nil {
			t.Errorf("%s: wrote outside the state directory", name)
		}
	}

	entries := []entry{plan}
	for i := range maxImport / maxSmall {
		entries = append(entries, entry{name: fmt.Sprintf("results/a%d.json", i), body: strings.Repeat("a", maxSmall)})
	}
	big := tarOf(t, entries...)
	if err := importPlan(newState(t), bytes.NewReader(big)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("oversize archive: %v", err)
	}
}

func TestKeygenOnce(t *testing.T) {
	state := newState(t)
	var a, b bytes.Buffer
	if err := keygen(state, &a); err != nil {
		t.Fatal(err)
	}
	if err := keygen(state, &b); err != nil || a.String() != b.String() {
		t.Fatalf("second keygen: %v\n%s\n%s", err, a.String(), b.String())
	}
	pub, comment, _, rest, err := ssh.ParseAuthorizedKey(a.Bytes())
	id, _ := runnerID(state)
	if err != nil || pub.Type() != ssh.KeyAlgoED25519 || comment != "kyquickstart-runner "+id || len(rest) != 0 {
		t.Fatalf("keygen printed %q: %v", a.String(), err)
	}
	key, _ := os.ReadFile(filepath.Join(state, keyFile))
	if fi, _ := os.Stat(filepath.Join(state, keyFile)); fi.Mode().Perm() != 0o600 || strings.Contains(a.String(), "PRIVATE") || bytes.Contains(a.Bytes(), key) {
		t.Errorf("key file mode %v or private key printed", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Dir(filepath.Join(state, keyFile))); fi.Mode().Perm() != 0o700 {
		t.Errorf("ssh directory mode %v", fi.Mode())
	}
}

func TestStateMustBePrivate(t *testing.T) {
	dir := filepath.Join(shortDir(t), "state")
	os.Mkdir(dir, 0o755)
	if _, err := openState(dir, false); err == nil {
		t.Fatal("accepted a state directory others can read")
	}
}

// With --pod-volumes the volume root may be world-writable, but only as a mount point; the
// subdirectory and every ancestor above the root are checked as without the flag.
func TestPodVolumes(t *testing.T) {
	root := filepath.Join(shortDir(t), "vol")
	os.Mkdir(root, 0o777)
	os.Chmod(root, 0o777)
	dir := filepath.Join(root, "runner")
	if _, err := openState(dir, false); err == nil {
		t.Fatal("without the flag: accepted a world-writable parent")
	}
	if _, err := openState(dir, true); err == nil || !strings.Contains(err.Error(), "not a mount point") {
		t.Fatalf("a root that is not a mount point: %v", err)
	}
	if fi, err := os.Stat("/dev/shm"); err == nil && fi.IsDir() {
		if err := volumeRoot("/dev/shm"); err != nil {
			t.Errorf("/dev/shm is a mount point: %v", err)
		}
	}
	link := filepath.Join(shortDir(t), "link")
	os.Symlink(root, link)
	if err := volumeRoot(link); err == nil {
		t.Error("a symlinked volume root")
	}

	saved := volumeRoot
	t.Cleanup(func() { volumeRoot = saved })
	volumeRoot = func(string) error { return nil } // root is a mount point
	if _, err := openState(dir, true); err != nil {
		t.Fatalf("a world-writable mount point: %v", err)
	}
	if fi, _ := os.Lstat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("state subdirectory mode %v", fi.Mode())
	}
	l, err := listen(filepath.Join(root, "sock", "runner.sock"), true)
	if err != nil {
		t.Fatalf("socket in a pod volume: %v", err)
	}
	l.Close()

	os.Symlink(shortDir(t), filepath.Join(root, "linked"))
	os.Mkdir(filepath.Join(root, "shared"), 0o755)
	os.WriteFile(filepath.Join(root, "file"), nil, 0o600)
	for _, sub := range []string{"linked", "shared", "file"} {
		if _, err := openState(filepath.Join(root, sub), true); err == nil {
			t.Errorf("accepted subdirectory %s", sub)
		}
	}

	above := filepath.Join(shortDir(t), "above")
	os.Mkdir(above, 0o777)
	os.Chmod(above, 0o777)
	os.Mkdir(filepath.Join(above, "vol"), 0o777)
	if _, err := openState(filepath.Join(above, "vol", "runner"), true); err == nil || !strings.Contains(err.Error(), "ancestor") {
		t.Errorf("an untrusted ancestor above the mount: %v", err)
	}
}
