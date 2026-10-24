package stack

import (
	"bytes"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// stack.yaml is operator-written and comes back from a runner on handoff pull, which
// re-marshals the parsed plan and writes it. An accepted plan must survive that: its
// marshalled form parses again, and marshals to the same bytes (nil and empty lists differ
// only in Go).
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		good, kubeStack,
		strings.Replace(kubeStack, "context: admin@k1", "context: admin@k1\n    storage_class: local-path.v1", 1),
		strings.Replace(good, "user: kyq", "usr: kyq", 1),
		strings.Replace(good, "root: /srv/kyq", "root: /srv/../etc", 1),
		strings.Replace(good, "host: nas.example.lan", "host: -oProxyCommand=x", 1),
		strings.Replace(good, "port: 2222", "port: 70000", 1),
		strings.Replace(good, "version: 1", "version: 1\nrevision: -1", 1),
		strings.Replace(good, "version: 1", "version: 1\nrevision: 3\ninstallation_id: 0f9e3c8e-1d2b-4c5a-8e7f-6a5b4c3d2e1f", 1),
		good + "  - name: hello\n    target: k1\n",
		good + "---\nversion: 1\n",
		good + "existing:\n  - product: kyidentity\n    target: nas\n    url: https://id.example.lan\n",
		"version: 1\ndemo: true\nrevision: 2\ntargets:\n  - name: nas\n    host: 192.0.2.1\n    user: kyq\napps:\n  - name: hello\n    target: nas\n",
		"", "version: 1\n", "{version: 1, targets: [{name: a, host: '::1', user: u}], apps: []}",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		s, err := Parse(b)
		if err != nil {
			if s != nil {
				t.Fatalf("refusal %v came with a stack", err)
			}
			return
		}
		for _, tg := range s.Targets {
			if !tg.Kubernetes() && (tg.Port == 0 || !validRoot(tg.Root)) {
				t.Fatalf("ssh target %q without its defaults: port %d root %q", tg.Name, tg.Port, tg.Root)
			}
		}
		out, err := yaml.Marshal(s)
		if err != nil {
			t.Fatalf("accepted stack does not marshal: %v", err)
		}
		again, err := Parse(out)
		if err != nil {
			t.Fatalf("marshalled stack refused: %v\n%s", err, out)
		}
		if out2, err := yaml.Marshal(again); err != nil || !bytes.Equal(out, out2) {
			t.Fatalf("stack changed through marshal and parse:\n%s\n%s", out, out2)
		}
	})
}
