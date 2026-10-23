package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"go.yaml.in/yaml/v3"
)

func dig(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

const compose = "services:\n  web:\n    image: {{ .Images.web }}\n"

func manifest(name, image, version, extra string) string {
	return "name: " + name + "\ncategory: ky\nversion: " + version + "\n# A human note about " + version + ".\nimages:\n  web: " +
		image + "\nhealth:\n  timeout_seconds: 30\n" + extra
}

// testCatalog writes a catalog of three apps: two Ky apps and one that is not.
func testCatalog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, m := range map[string]string{
		"id":  manifest("id", "ghcr.io/busnes-app/kyidentity-server:0.4.6@"+dig('a'), "0.4.6", "upgrade:\n  min_from: 0.4.0\n"),
		"dns": manifest("dns", "ghcr.io/busnes-app/kydns-server:0.4.2@"+dig('b'), "0.4.2", ""),
		"npm": manifest("npm", "docker.io/jc21/nginx-proxy-manager:2.16.0@"+dig('c'), "2.16.0", ""),
	} {
		os.MkdirAll(filepath.Join(dir, name), 0o755)
		os.WriteFile(filepath.Join(dir, name, "manifest.yaml"), []byte(m), 0o644)
		os.WriteFile(filepath.Join(dir, name, "compose.yaml.tmpl"), []byte(compose), 0o644)
	}
	return dir
}

type fake struct {
	rels     map[string][]release
	digests  map[string]string // image:tag -> digest
	bad      map[string]bool   // refs whose attestation fails
	verified []string          // "ref identity"
	listed   []string
	warnings []string
}

func (f *fake) deps() deps {
	return deps{
		releases: func(repo string) ([]release, error) {
			f.listed = append(f.listed, repo)
			if r, ok := f.rels[repo]; ok {
				return r, nil
			}
			return nil, errors.New("no such repo")
		},
		digest: func(image, tag string) (string, error) {
			if d, ok := f.digests[image+":"+tag]; ok {
				return d, nil
			}
			return "", errors.New("manifest unknown")
		},
		verify: func(ref, repo, identity string) error {
			f.verified = append(f.verified, ref+" "+identity)
			if f.bad[ref] {
				return errors.New("no attestation")
			}
			if !strings.HasPrefix(identity, "https://github.com/"+repo+"/") {
				return fmt.Errorf("identity %s is not %s's", identity, repo)
			}
			return nil
		},
		warn: func(format string, a ...any) { f.warnings = append(f.warnings, fmt.Sprintf(format, a...)) },
	}
}

func rel(tag string) release { return release{Tag: tag} }

func TestFollow(t *testing.T) {
	dir := testCatalog(t)
	f := &fake{
		rels: map[string][]release{
			"Busnes-app/KyIdentity-server": {rel("v0.5.0"), {Tag: "v0.4.9", Prerelease: true}, {Tag: "v0.4.10", Draft: true},
				rel("v0.4.7.2"), rel("v0.4.7"), rel("v0.4.7.01"), rel("0.4.8"), rel("v0.4.6"), rel("v0.3.9")},
			"Busnes-app/KyDNS-server": {rel("v0.4.3")},
		},
		digests: map[string]string{
			"ghcr.io/busnes-app/kyidentity-server:0.4.7.2": dig('d'),
			"ghcr.io/busnes-app/kydns-server:0.4.3":        dig('e'),
		},
		bad: map[string]bool{"oci://ghcr.io/busnes-app/kydns-server@" + dig('e'): true},
	}
	dnsBefore, _ := os.ReadFile(filepath.Join(dir, "dns", "manifest.yaml"))
	bumps, err := follow(dir, f.deps())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(bumps, []bump{{"id", "0.4.6", "0.4.7.2"}}) {
		t.Fatalf("bumps = %v", bumps)
	}
	if slices.Contains(f.listed, "") || len(f.listed) != 2 {
		t.Errorf("listed %v; npm is not a Ky app", f.listed)
	}
	want := "oci://ghcr.io/busnes-app/kyidentity-server@" + dig('d') + " https://github.com/Busnes-app/KyIdentity-server/.github/workflows/release-image.yml@refs/tags/v0.4.7.2"
	if !slices.Contains(f.verified, want) {
		t.Errorf("verified %v, want %s", f.verified, want)
	}
	if !slices.Contains(f.verified, "oci://ghcr.io/busnes-app/kydns-server@"+dig('e')+" https://github.com/Busnes-app/KyDNS-server/.github/workflows/release.yml@refs/tags/v0.4.3") {
		t.Errorf("KyDNS is not verified against release.yml: %v", f.verified)
	}
	joined := strings.Join(f.warnings, "\n")
	for _, w := range []string{"v0.5.0 is on a newer line", "dns: ghcr.io/busnes-app/kydns-server@" + dig('e') + " does not verify"} {
		if !strings.Contains(joined, w) {
			t.Errorf("warnings lack %q:\n%s", w, joined)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "dns", "manifest.yaml")); string(b) != string(dnsBefore) {
		t.Errorf("an unverified release changed dns:\n%s", b)
	}

	b, _ := os.ReadFile(filepath.Join(dir, "id", "manifest.yaml"))
	wantID := "name: id\ncategory: ky\nversion: 0.4.7.2\n# A human note about 0.4.6.\n" +
		"# catalog-follow: v0.4.7.2 attested by Busnes-app/KyIdentity-server release-image.yml@refs/tags/v0.4.7.2\n" +
		"images:\n  web: ghcr.io/busnes-app/kyidentity-server:0.4.7.2@" + dig('d') + "\nhealth:\n  timeout_seconds: 30\nupgrade:\n  min_from: 0.4.0\n"
	if string(b) != wantID {
		t.Errorf("id manifest:\n%s\nwant:\n%s", b, wantID)
	}
	if _, err := catalog.Load(os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}

	// Run again after a newer .N: the machine comment is replaced, not added.
	f.rels["Busnes-app/KyIdentity-server"] = append(f.rels["Busnes-app/KyIdentity-server"], rel("v0.4.7.3"))
	f.digests["ghcr.io/busnes-app/kyidentity-server:0.4.7.3"] = dig('f')
	if bumps, err := follow(dir, f.deps()); err != nil || !slices.Equal(bumps, []bump{{"id", "0.4.7.2", "0.4.7.3"}}) {
		t.Fatalf("second run: %v %v", bumps, err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "id", "manifest.yaml"))
	if strings.Count(string(b), marker) != 1 || !strings.Contains(string(b), marker+"v0.4.7.3 ") {
		t.Errorf("machine comment:\n%s", b)
	}
	// Nothing newer: nothing changes.
	if bumps, err := follow(dir, f.deps()); err != nil || len(bumps) != 0 {
		t.Fatalf("third run: %v %v", bumps, err)
	}
}

func TestFollowSkips(t *testing.T) {
	for name, f := range map[string]*fake{
		"no digest":       {rels: map[string][]release{"Busnes-app/KyIdentity-server": {rel("v0.4.7")}}},
		"bad digest":      {rels: map[string][]release{"Busnes-app/KyIdentity-server": {rel("v0.4.7")}}, digests: map[string]string{"ghcr.io/busnes-app/kyidentity-server:0.4.7": "sha256:abc"}},
		"listing fails":   {},
		"only new minors": {rels: map[string][]release{"Busnes-app/KyIdentity-server": {rel("v0.5.0"), rel("v1.0.0")}}},
	} {
		dir := testCatalog(t)
		os.RemoveAll(filepath.Join(dir, "dns"))
		before, _ := os.ReadFile(filepath.Join(dir, "id", "manifest.yaml"))
		bumps, err := follow(dir, f.deps())
		if err != nil || len(bumps) != 0 {
			t.Errorf("%s: %v %v", name, bumps, err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "id", "manifest.yaml")); string(b) != string(before) {
			t.Errorf("%s: manifest changed", name)
		}
		if len(f.warnings) == 0 {
			t.Errorf("%s: no warning", name)
		}
	}
}

func TestNewest(t *testing.T) {
	rels := []release{rel("v0.4.2"), rel("v0.4.3"), rel("v0.4.3.1"), rel("v0.4.10"), rel("v0.6.0"), rel("v1.0.0"), rel("v0.4.11.0"), rel("v00.4.12")}
	for current, want := range map[string][2]string{
		"0.4.2":    {"0.4.10", "1.0.0"},
		"0.4.10":   {"", "1.0.0"},
		"0.4.3":    {"0.4.10", "1.0.0"},
		"0.6.0":    {"", "1.0.0"},
		"1.0.0":    {"", ""},
		"0.3.0":    {"", "1.0.0"},
		"0.4.3.1":  {"0.4.10", "1.0.0"},
		"0.4.10.1": {"", "1.0.0"},
	} {
		if to, newer := newest(current, rels); to != want[0] || newer != want[1] {
			t.Errorf("newest(%s) = %q, %q; want %q, %q", current, to, newer, want[0], want[1])
		}
	}
}

func TestRewriteRefusesAmbiguity(t *testing.T) {
	m := []byte(manifest("id", "ghcr.io/busnes-app/kyidentity-server:0.4.6@"+dig('a'), "0.4.6", "") + "kubernetes:\n  image: ghcr.io/busnes-app/kyidentity-server:0.4.6@" + dig('a') + "\n")
	if _, err := rewrite(m, "0.4.6", "0.4.7", "ghcr.io/busnes-app/kyidentity-server:0.4.6@"+dig('a'), "x", marker+"x"); err == nil {
		t.Error("a twice-named image was rewritten")
	}
}

// Every Ky image in the real catalog has a source, so the daily run follows every app.
func TestSourcesCoverCatalog(t *testing.T) {
	n := 0
	fs.WalkDir(catalog.Embedded(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || filepath.Base(p) != "manifest.yaml" {
			return err
		}
		b, _ := fs.ReadFile(catalog.Embedded(), p)
		var m struct {
			Version string            `yaml:"version"`
			Images  map[string]string `yaml:"images"`
		}
		yaml.Unmarshal(b, &m)
		for _, ref := range m.Images {
			if strings.HasPrefix(ref, "ghcr.io/busnes-app/") {
				k := kyImage.FindStringSubmatch(ref)
				if k == nil || k[2] != m.Version {
					t.Errorf("%s: %s is not pinned as ghcr.io/busnes-app/<image>:%s@sha256:<digest>", p, ref, m.Version)
					continue
				}
				if _, ok := sources[k[1]]; !ok {
					t.Errorf("%s: no source for %s", p, k[1])
				}
				n++
			}
		}
		return nil
	})
	if n < 10 {
		t.Errorf("found %d Ky images in the catalog", n)
	}
}

func TestRegistryDigest(t *testing.T) {
	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`)
	sum := sha256.Sum256(body)
	want := "sha256:" + hex.EncodeToString(sum[:])
	header := want
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token" && r.URL.Query().Get("scope") == "repository:busnes-app/kyidentity-server:pull":
			w.Write([]byte(`{"token":"t0k"}`))
		case r.URL.Path == "/v2/busnes-app/kyidentity-server/manifests/0.4.7.1" && r.Header.Get("Authorization") == "Bearer t0k" &&
			strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json"):
			w.Header().Set("Docker-Content-Digest", header)
			w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	image := strings.TrimPrefix(srv.URL, "http://") + "/busnes-app/kyidentity-server"
	get := registryDigest(srv.URL)
	if got, err := get(image, "0.4.7.1"); err != nil || got != want {
		t.Fatalf("digest = %s %v, want %s", got, err, want)
	}
	if _, err := get(image, "0.4.8"); err == nil {
		t.Error("a missing tag resolved")
	}
	if _, err := get("ghcr.io/busnes-app/kyidentity-server", "0.4.7.1"); err == nil {
		t.Error("an image on another registry resolved")
	}
	header = dig('0')
	if _, err := get(image, "0.4.7.1"); err == nil || !strings.Contains(err.Error(), "hash to") {
		t.Errorf("a digest header that disagrees with the bytes: %v", err)
	}
}
