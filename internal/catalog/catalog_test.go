package catalog

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

const digest = "traefik/whoami:v1.11.0@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab"

func TestLoadFixture(t *testing.T) {
	cat, err := Load(os.DirFS("testdata/apps"))
	if err != nil {
		t.Fatal(err)
	}
	app, ok := cat["hello"]
	if !ok {
		t.Fatal("hello missing")
	}
	if len(app.Services) != 1 || app.Services[0] != "whoami" {
		t.Errorf("services = %v", app.Services)
	}
	if !strings.Contains(string(app.Compose), "image: "+digest) {
		t.Errorf("compose not rendered:\n%s", app.Compose)
	}
	if app.Health.TimeoutSeconds != 60 || len(app.Secrets) != 1 {
		t.Errorf("manifest = %+v", app.Manifest)
	}
}

func TestEmbeddedLoads(t *testing.T) {
	if _, err := Load(Embedded()); err != nil {
		t.Fatal(err)
	}
}

const manifest = "name: a\nimages:\n  web: " + digest + "\nhealth:\n  timeout_seconds: 30\n"
const compose = "services:\n  web:\n    image: {{ .Images.web }}\n"

func TestLoadRejects(t *testing.T) {
	cases := map[string]struct{ manifest, compose, want string }{
		"unknown key":     {manifest + "port: 80\n", compose, "field port not found"},
		"name mismatch":   {strings.Replace(manifest, "name: a", "name: b", 1), compose, "must match its directory"},
		"tag not digest":  {strings.Replace(manifest, digest, "traefik/whoami:latest", 1), compose, "not pinned by digest"},
		"missing key":     {manifest, strings.Replace(compose, ".Images.web", ".Images.nope", 1), "nope"},
		"foreign image":   {manifest, strings.Replace(compose, "{{ .Images.web }}", "nginx@sha256:"+strings.Repeat("0", 64), 1), "not one of the manifest's pinned images"},
		"build":           {manifest, compose + "    build: .\n", "build is not allowed"},
		"no services":     {manifest, "services: {}\n", "no services"},
		"zero timeout":    {strings.Replace(manifest, "timeout_seconds: 30", "timeout_seconds: 0", 1), compose, "timeout_seconds"},
		"bad secret name": {manifest + "secrets: [Token]\n", compose, "secret"},
		"self dependency": {manifest + "depends_on: [a]\n", compose, "depends_on"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"a/manifest.yaml":     {Data: []byte(c.manifest)},
				"a/compose.yaml.tmpl": {Data: []byte(c.compose)},
			}
			_, err := Load(fsys)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}
}
