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
