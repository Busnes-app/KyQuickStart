// Command catalog-follow moves each Ky app's catalog pin to the newest published,
// non-prerelease release of its repository on the same MAJOR.MINOR line (patch and .N
// releases), with the digest that release's workflow attested. dependency-release.yml runs it
// before deciding on a KyQuickStart release; run by hand it does the same.
//
//	go run ./scripts/catalog-follow [-apps internal/catalog/apps]
//
// For every app that follows a release it prints one line, "<app> <old> <new>", and rewrites
// only the manifest's version line, the image reference and one machine-maintained comment
// line ("# catalog-follow: ..."). Human comments and upgrade rules are left as they are. A newer
// MAJOR.MINOR is only reported, as a ::warning::: its upgrade rules need a human. An app whose
// release cannot be resolved or verified is skipped with a ::warning::, never pinned. It exits
// non-zero only when it cannot run at all.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"go.yaml.in/yaml/v3"
)

// source is where an image's releases are published and which workflow attests them.
type source struct{ repo, workflow string }

// sources maps each ghcr.io/busnes-app image to its repository. A Ky image added to the
// catalog needs an entry here (TestSourcesCoverCatalog).
var sources = map[string]source{
	"kycalendar":        {"Busnes-app/KyCalendar", "release-image.yml"},
	"kydns-server":      {"Busnes-app/KyDNS-server", "release.yml"},
	"kydrive-server":    {"Busnes-app/kydrive-server", "release-image.yml"},
	"kyidentity-server": {"Busnes-app/KyIdentity-server", "release-image.yml"},
	"kymessage-server":  {"Busnes-app/KyMessage-Server", "release-image.yml"},
	"kynotes-server":    {"Busnes-app/KyNotes-server", "release-image.yml"},
	"kypost-server":     {"Busnes-app/KyPost-Server", "release-image.yml"},
	"kypulse-server":    {"Busnes-app/KyPulse-server", "release-image.yml"},
	"kyrecovery-server": {"Busnes-app/KyRecovery-server", "release-image.yml"},
	"kyyard":            {"Busnes-app/KyYard-server", "release-image.yml"},
}

// release is one GitHub release as the API lists it.
type release struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// deps are the outside world, replaced in tests.
type deps struct {
	releases func(repo string) ([]release, error)
	digest   func(image, tag string) (string, error) // sha256:<hex> of the tag's manifest
	verify   func(ref, repo, identity string) error  // the attestation, pinned to identity
	warn     func(format string, a ...any)
}

var (
	kyImage = regexp.MustCompile(`^ghcr\.io/busnes-app/([a-z0-9-]+):([^@]+)@(sha256:[0-9a-f]{64})$`)
	tagRE   = regexp.MustCompile(`^v([0-9]+\.[0-9]+\.[0-9]+(?:\.[1-9][0-9]*)?)$`)
	digRE   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	marker  = "# catalog-follow: "
)

// bump is one app moved to a newer release.
type bump struct{ app, from, to string }

func main() {
	apps := flag.String("apps", "internal/catalog/apps", "the catalog's apps directory")
	flag.Parse()
	d := deps{releases: ghReleases, digest: registryDigest("https://ghcr.io"), verify: ghVerify,
		warn: func(f string, a ...any) { fmt.Fprintf(os.Stderr, "::warning::"+f+"\n", a...) }}
	bumps, err := follow(*apps, d)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog-follow:", err)
		os.Exit(1)
	}
	for _, b := range bumps {
		fmt.Println(b.app, b.from, b.to)
	}
}

// follow moves every Ky app under dir it can, in name order.
func follow(dir string, d deps) ([]bump, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	if _, err := catalog.Load(os.DirFS(dir)); err != nil {
		return nil, fmt.Errorf("the catalog does not load before following: %w", err)
	}
	var out []bump
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		file := filepath.Join(dir, e.Name(), "manifest.yaml")
		old, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		b, next, ok := followApp(e.Name(), old, d)
		if !ok {
			continue
		}
		if err := os.WriteFile(file, next, 0o644); err != nil {
			return nil, err
		}
		// The whole catalog must still load; otherwise this app keeps its pin.
		if _, err := catalog.Load(os.DirFS(dir)); err != nil {
			d.warn("%s: %s does not load (%v); left at %s", e.Name(), b.to, err, b.from)
			if err := os.WriteFile(file, old, 0o644); err != nil {
				return nil, err
			}
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

// followApp returns app's manifest m moved to its newest verified release on the same line.
func followApp(app string, m []byte, d deps) (bump, []byte, bool) {
	var man struct {
		Version string            `yaml:"version"`
		Images  map[string]string `yaml:"images"`
	}
	if err := yaml.Unmarshal(m, &man); err != nil {
		d.warn("%s: manifest does not parse: %v", app, err)
		return bump{}, nil, false
	}
	var pins []string
	for _, ref := range man.Images {
		if p := kyImage.FindStringSubmatch(ref); p != nil && p[2] == man.Version {
			pins = append(pins, ref)
		}
	}
	switch len(pins) {
	case 0:
		return bump{}, nil, false // not a Ky app: no ghcr.io/busnes-app image tagged with its version
	case 1:
	default:
		d.warn("%s: %d images are tagged %s; follow it by hand", app, len(pins), man.Version)
		return bump{}, nil, false
	}
	p := kyImage.FindStringSubmatch(pins[0])
	name := p[1]
	src, ok := sources[name]
	if !ok {
		d.warn("%s: no source repository for ghcr.io/busnes-app/%s; add it to scripts/catalog-follow", app, name)
		return bump{}, nil, false
	}
	rels, err := d.releases(src.repo)
	if err != nil {
		d.warn("%s: listing %s releases: %v", app, src.repo, err)
		return bump{}, nil, false
	}
	to, newer := newest(man.Version, rels)
	if newer != "" {
		d.warn("%s: %s v%s is on a newer line than the catalog's %s; it needs a human PR", app, src.repo, newer, man.Version)
	}
	if to == "" {
		return bump{}, nil, false
	}
	image := "ghcr.io/busnes-app/" + name
	dg, err := d.digest(image, to)
	if err != nil || !digRE.MatchString(dg) {
		d.warn("%s: resolving %s:%s: %v %q; left at %s", app, image, to, err, dg, man.Version)
		return bump{}, nil, false
	}
	identity := "https://github.com/" + src.repo + "/.github/workflows/" + src.workflow + "@refs/tags/v" + to
	if err := d.verify("oci://"+image+"@"+dg, src.repo, identity); err != nil {
		d.warn("%s: %s@%s does not verify as %s: %v; left at %s", app, image, dg, identity, err, man.Version)
		return bump{}, nil, false
	}
	next, err := rewrite(m, man.Version, to, pins[0], image+":"+to+"@"+dg,
		marker+"v"+to+" attested by "+src.repo+" "+src.workflow+"@refs/tags/v"+to)
	if err != nil {
		d.warn("%s: %v; left at %s", app, err, man.Version)
		return bump{}, nil, false
	}
	return bump{app, man.Version, to}, next, true
}

// newest returns the newest published, non-prerelease version after current on its
// MAJOR.MINOR line ("" for none), and the newest on a later line ("" for none).
func newest(current string, rels []release) (to, newer string) {
	line := func(v string) string { p := strings.SplitN(v, ".", 3); return p[0] + "." + p[1] }
	later := func(a, b string) bool { return a != b && catalog.VersionAtLeast(a, b) }
	for _, r := range rels {
		m := tagRE.FindStringSubmatch(r.Tag)
		if r.Draft || r.Prerelease || m == nil || !catalog.VersionAtLeast(m[1], "0.0.0") {
			continue
		}
		v := m[1]
		switch {
		case !later(v, current):
		case line(v) == line(current):
			if to == "" || later(v, to) {
				to = v
			}
		case newer == "" || later(v, newer):
			newer = v
		}
	}
	return to, newer
}

// rewrite replaces m's version line and image reference, each of which must occur once, and
// sets the machine comment above images:, replacing an earlier one.
func rewrite(m []byte, from, to, oldRef, newRef, comment string) ([]byte, error) {
	for _, r := range [][2]string{{"\nversion: " + from + "\n", "\nversion: " + to + "\n"}, {" " + oldRef + "\n", " " + newRef + "\n"}} {
		if n := bytes.Count(m, []byte(r[0])); n != 1 {
			return nil, fmt.Errorf("%q occurs %d times, not once", strings.TrimSpace(r[0]), n)
		}
		m = bytes.Replace(m, []byte(r[0]), []byte(r[1]), 1)
	}
	lines := strings.SplitAfter(string(m), "\n")
	lines = slices.DeleteFunc(lines, func(l string) bool { return strings.HasPrefix(l, marker) })
	i := slices.Index(lines, "images:\n")
	if i < 0 {
		return nil, errors.New("no top-level images: line")
	}
	lines = slices.Insert(lines, i, comment+"\n")
	return []byte(strings.Join(lines, "")), nil
}

func ghReleases(repo string) ([]release, error) {
	out, err := exec.Command("gh", "api", "--paginate", "--slurp", "repos/"+repo+"/releases").Output()
	if err != nil {
		return nil, fmt.Errorf("gh api: %w", err)
	}
	var pages [][]release
	if err := json.Unmarshal(out, &pages); err != nil {
		return nil, err
	}
	return slices.Concat(pages...), nil
}

func ghVerify(ref, repo, identity string) error {
	out, err := exec.Command("gh", "attestation", "verify", ref, "--repo", repo, "--cert-identity", identity).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// registryDigest resolves image:tag anonymously and returns the sha256 of the manifest bytes
// the registry serves, which is what an image@digest pull fetches.
func registryDigest(base string) func(image, tag string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	return func(image, tag string) (string, error) {
		u, err := url.Parse(base)
		if err != nil {
			return "", err
		}
		repo, ok := strings.CutPrefix(image, u.Host+"/")
		if !ok {
			return "", fmt.Errorf("%s is not on %s", image, u.Host)
		}
		res, err := client.Get(base + "/token?scope=" + url.QueryEscape("repository:"+repo+":pull"))
		if err != nil {
			return "", err
		}
		var tok struct {
			Token string `json:"token"`
		}
		err = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&tok)
		res.Body.Close()
		if err != nil || res.StatusCode != http.StatusOK || tok.Token == "" {
			return "", fmt.Errorf("registry token: HTTP %d %v", res.StatusCode, err)
		}
		req, _ := http.NewRequest(http.MethodGet, base+"/v2/"+repo+"/manifests/"+url.PathEscape(tag), nil)
		req.Header.Set("Authorization", "Bearer "+tok.Token)
		req.Header.Set("Accept", strings.Join([]string{
			"application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json",
			"application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json"}, ", "))
		res, err = client.Do(req)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		if err != nil || res.StatusCode != http.StatusOK {
			return "", fmt.Errorf("manifest: HTTP %d %v", res.StatusCode, err)
		}
		sum := sha256.Sum256(body)
		dg := "sha256:" + hex.EncodeToString(sum[:])
		if h := res.Header.Get("Docker-Content-Digest"); h != "" && h != dg {
			return "", fmt.Errorf("the registry names %s, its bytes hash to %s", h, dg)
		}
		return dg, nil
	}
}
