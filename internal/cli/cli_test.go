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
