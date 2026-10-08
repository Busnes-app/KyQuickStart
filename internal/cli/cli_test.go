package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const stackYAML = `version: 1
targets:
  - name: nas
    host: 192.0.2.1
    user: kyq
apps:
  - name: hello
    target: nas
`

func runIn(t *testing.T, stack string, args ...string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o600)
	var out bytes.Buffer
	o := Options{Catalog: os.DirFS("../catalog/testdata/apps"), ReleaseSet: "test", Out: &out}
	err := Run(context.Background(), append(args, "--state", dir), o)
	return out.String(), err
}

func TestBadStackFailsBeforeSSH(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	for name, stack := range map[string]string{
		"typo":        strings.Replace(stackYAML, "user:", "usr:", 1),
		"metachar":    strings.Replace(stackYAML, "name: hello", "name: hello;id", 1),
		"unknown app": strings.Replace(stackYAML, "name: hello", "name: nope", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := runIn(t, stack, "apply")
			if err == nil || strings.Contains(err.Error(), "SSH_AUTH_SOCK") {
				t.Fatalf("err = %v, want a stack error before any connection", err)
			}
		})
	}
}

func TestGoodStackReachesSSH(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	_, err := runIn(t, stackYAML, "preflight")
	if err == nil || !strings.Contains(err.Error(), "SSH_AUTH_SOCK") {
		t.Fatalf("err = %v", err)
	}
}

func TestTrustFlag(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	if _, err := runIn(t, stackYAML, "apply", "--trust-host-key", "nas=MD5:aa"); err == nil {
		t.Error("accepted a non-SHA256 fingerprint")
	}
	_, err := runIn(t, stackYAML, "apply", "--trust-host-key", "other=SHA256:abc")
	if err == nil || !strings.Contains(err.Error(), `unknown target "other"`) {
		t.Errorf("err = %v", err)
	}
}

func TestUsageAndVersion(t *testing.T) {
	var out bytes.Buffer
	o := Options{Out: &out, ReleaseSet: "rs-7"}
	if err := Run(context.Background(), nil, o); err != ErrUsage {
		t.Errorf("no args: %v", err)
	}
	if err := Run(context.Background(), []string{"destroy"}, o); err != ErrUsage {
		t.Errorf("unknown command: %v", err)
	}
	if err := Run(context.Background(), []string{"version"}, o); err != nil || out.String() != "rs-7\n" {
		t.Errorf("version: %q %v", out.String(), err)
	}
}

func TestPlacementAndKubeconfigChecks(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	outside := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(outside, []byte("not: a kubeconfig\n"), 0o600)
	kubeStack := func(kc string) string {
		return "version: 1\ntargets:\n  - name: k1\n    kubeconfig: " + kc + "\napps:\n  - name: hello\n    target: k1\n"
	}

	// Inside the state directory (stack.yaml is a file there): refused before any connection.
	_, err := runIn(t, kubeStack("stack.yaml"), "preflight")
	if err == nil || !strings.Contains(err.Error(), "inside the state directory") {
		t.Errorf("relative kubeconfig in state: %v", err)
	}

	// A Docker-only app on a cluster: refused before any connection.
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "apps", "dockeronly"), 0o700)
	os.WriteFile(filepath.Join(dir, "apps", "dockeronly", "manifest.yaml"), []byte(
		"name: dockeronly\ncategory: third-party\nimages:\n  web: traefik/whoami:v1.11.0@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab\nhealth:\n  timeout_seconds: 30\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "apps", "dockeronly", "compose.yaml.tmpl"), []byte(
		"services:\n  web:\n    image: {{ .Images.web }}\n"), 0o600)
	state := filepath.Join(dir, "state")
	os.MkdirAll(state, 0o700)
	os.WriteFile(filepath.Join(state, "stack.yaml"), []byte(strings.Replace(kubeStack(outside), "hello", "dockeronly", 1)), 0o600)
	var out bytes.Buffer
	err = Run(context.Background(), []string{"preflight", "--state", state}, Options{Catalog: os.DirFS(filepath.Join(dir, "apps")), Out: &out})
	if err == nil || !strings.Contains(err.Error(), "no Kubernetes deployment") {
		t.Errorf("docker-only app on a cluster: %v", err)
	}

	// --trust-host-key names a Kubernetes target.
	_, err = runIn(t, kubeStack(outside), "preflight", "--trust-host-key", "k1=SHA256:abc")
	if err == nil || !strings.Contains(err.Error(), "Kubernetes target") {
		t.Errorf("trust flag on a cluster: %v", err)
	}

	// A missing kubeconfig is refused before any connection, naming target and path.
	missing := filepath.Join(t.TempDir(), "absent")
	_, err = runIn(t, kubeStack(missing), "preflight")
	if err == nil || !strings.Contains(err.Error(), `target "k1": kubeconfig `+missing) {
		t.Errorf("missing kubeconfig: %v", err)
	}

	// A kubeconfig outside the state dir passes load and fails at connect (it is not valid).
	_, err = runIn(t, kubeStack(outside), "preflight")
	if err == nil || !strings.Contains(err.Error(), "kubeconfig") || strings.Contains(err.Error(), "inside the state") {
		t.Errorf("outside kubeconfig: %v", err)
	}
}

func TestKubeconfigSymlinkIntoState(t *testing.T) {
	state := t.TempDir()
	inside := filepath.Join(state, "kubeconfig")
	os.WriteFile(inside, []byte("not: a kubeconfig\n"), 0o600)
	link := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(state, "stack.yaml"), []byte(
		"version: 1\ntargets:\n  - name: k1\n    kubeconfig: "+link+"\napps:\n  - name: hello\n    target: k1\n"), 0o600)
	var out bytes.Buffer
	o := Options{Catalog: os.DirFS("../catalog/testdata/apps"), ReleaseSet: "test", Out: &out}
	err := Run(context.Background(), []string{"preflight", "--state", state}, o)
	if err == nil || !strings.Contains(err.Error(), "inside the state directory") {
		t.Fatalf("err = %v", err)
	}
}
