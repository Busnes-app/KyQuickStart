package catalogtest

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestUnpinned(t *testing.T) {
	ky := "name: a\nversion: 0.4.7.1\nimages:\n  a: ghcr.io/busnes-app/kyidentity-server:0.4.7.1@sha256:" + strings.Repeat("a", 64) + "\n"
	other := "name: b\nversion: 2.16.0\nimages:\n  b: docker.io/jc21/nginx-proxy-manager:2.16.0@sha256:" + strings.Repeat("b", 64) + "\n"
	stale := "name: c\nversion: 0.4.3\nimages:\n  c: ghcr.io/busnes-app/kyyard:0.4.2@sha256:" + strings.Repeat("c", 64) + "\n"
	out := Unpinned(fstest.MapFS{"a/manifest.yaml": {Data: []byte(ky)}, "b/manifest.yaml": {Data: []byte(other)},
		"c/manifest.yaml": {Data: []byte(stale)}, "a/compose.yaml.tmpl": {Data: []byte("version: 0.4.7.1\n")}})
	read := func(p string) string { b, _ := fs.ReadFile(out, p); return string(b) }
	if got := read("a/manifest.yaml"); got != "name: a\nversion: 999.0.0\nimages:\n  a: ghcr.io/busnes-app/kyidentity-server:999.0.0@"+Digest+"\n" {
		t.Errorf("Ky app:\n%s", got)
	}
	if read("b/manifest.yaml") != other || read("c/manifest.yaml") != stale || read("a/compose.yaml.tmpl") != "version: 0.4.7.1\n" {
		t.Error("Unpinned changed a file that holds no Ky release pin")
	}
}
