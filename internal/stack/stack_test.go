package stack

import (
	"strings"
	"testing"
)

const good = `
version: 1
targets:
  - name: nas
    host: nas.example.lan
    user: kyq
  - name: k1
    host: 192.0.2.10
    port: 2222
    user: deploy
    root: /srv/kyq
apps:
  - name: hello
    target: nas
`

func TestParseDefaults(t *testing.T) {
	s, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	nas, ok := s.Target("nas")
	if !ok || nas.Port != 22 || nas.Root != DefaultRoot {
		t.Errorf("nas = %+v, want port 22 and root %s", nas, DefaultRoot)
	}
	k1, _ := s.Target("k1")
	if k1.Port != 2222 || k1.Root != "/srv/kyq" {
		t.Errorf("k1 = %+v", k1)
	}
	if _, ok := s.Target("nope"); ok {
		t.Error("Target(nope) found")
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]struct{ old, new, want string }{
		"unknown key":      {"user: kyq", "usr: kyq", "field usr not found"},
		"metachar app":     {"name: hello", "name: hello;rm", `app "hello;rm"`},
		"metachar target":  {"target: nas", "target: nas$(id)", `unknown target "nas$(id)"`},
		"option host":      {"host: nas.example.lan", "host: -oProxyCommand=x", "host"},
		"dotdot root":      {"root: /srv/kyq", "root: /srv/../etc", "root"},
		"relative root":    {"root: /srv/kyq", "root: srv/kyq", "root"},
		"bad port":         {"port: 2222", "port: 70000", "port"},
		"bad user":         {"user: deploy", "user: Deploy", "user"},
		"version 2":        {"version: 1", "version: 2", "version 2"},
		"revision":         {"version: 1", "version: 1\nrevision: -1", "revision"},
		"duplicate target": {"name: k1", "name: nas", `duplicate target "nas"`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := strings.Replace(good, c.old, c.new, 1)
			if in == good {
				t.Fatalf("case did not change the input")
			}
			_, err := Parse([]byte(in))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

// recovery_key_id is the key ID KyRecovery's ceremony page shows, on kyrecovery alone.
func TestRecoveryKeyID(t *testing.T) {
	key := strings.Repeat("0f", 32)
	with := func(app, id string) string {
		return strings.Replace(good, "  - name: hello\n    target: nas\n", "  - name: "+app+"\n    target: nas\n    recovery_key_id: "+id+"\n", 1)
	}
	if s, err := Parse([]byte(with("kyrecovery", key))); err != nil || s.Apps[0].RecoveryKeyID != key {
		t.Fatalf("valid key: %v", err)
	}
	for _, in := range []string{with("hello", key), with("kyrecovery", strings.ToUpper(key)), with("kyrecovery", key[2:]), with("kyrecovery", key+"00")} {
		if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), "recovery_key_id") {
			t.Errorf("accepted, or err %v, for:\n%s", err, in)
		}
	}
}

func TestParseRejectsDuplicateApp(t *testing.T) {
	_, err := Parse([]byte(good + "  - name: hello\n    target: k1\n"))
	if err == nil || !strings.Contains(err.Error(), `duplicate app "hello"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRejectsSecondDocument(t *testing.T) {
	_, err := Parse([]byte(good + "---\nversion: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "exactly one document") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRejectsEmpty(t *testing.T) {
	if _, err := Parse(nil); err == nil {
		t.Fatal("empty stack.yaml accepted")
	}
}

const kubeStack = `
version: 1
targets:
  - name: k1
    kubeconfig: /home/op/.kube/k1.yaml
    context: admin@k1
apps:
  - name: hello
    target: k1
`

func TestKubernetesTarget(t *testing.T) {
	s, err := Parse([]byte(kubeStack))
	if err != nil {
		t.Fatal(err)
	}
	k, _ := s.Target("k1")
	if !k.Kubernetes() || k.Port != 0 || k.Root != "" || k.Context != "admin@k1" {
		t.Fatalf("k1 = %+v", k)
	}
}

func TestStorageClass(t *testing.T) {
	s, err := Parse([]byte(strings.Replace(kubeStack, "context: admin@k1", "context: admin@k1\n    storage_class: local-path.v1", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if k, _ := s.Target("k1"); k.StorageClass != "local-path.v1" {
		t.Fatalf("k1 = %+v", k)
	}
	docker := "version: 1\ntargets:\n  - name: d1\n    host: d1.lan\n    user: kyq\n    storage_class: fast\napps: []\n"
	if _, err := Parse([]byte(docker)); err == nil || !strings.Contains(err.Error(), "storage_class need") {
		t.Fatalf("docker target with storage_class: err = %v", err)
	}
}

func TestKubernetesTargetRejects(t *testing.T) {
	cases := map[string]struct{ old, new, want string }{
		"ssh field":    {"context: admin@k1", "context: admin@k1\n    host: k1.lan", "ssh fields"},
		"bad context":  {"context: admin@k1", "context: \"a b\"", "context"},
		"newline path": {"kubeconfig: /home/op/.kube/k1.yaml", "kubeconfig: \"/a\\nb\"", "kubeconfig"},
		"bad class":    {"context: admin@k1", "context: admin@k1\n    storage_class: Fast_SSD", "storage_class"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := strings.Replace(kubeStack, c.old, c.new, 1)
			_, err := Parse([]byte(in))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}
