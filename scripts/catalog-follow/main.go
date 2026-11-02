// Command catalog-follow moves each Ky app's catalog pin to the newest published,
// non-prerelease release of its repository on the same MAJOR.MINOR line (patch and .N
// releases), with the digest that release's workflow attested. dependency-release.yml runs it
// before deciding on a KyQuickStart release; run by hand it does the same.
//
//	go run ./scripts/catalog-follow [-apps internal/catalog/apps]
//
// For every app that follows a release it prints one line, "<app> <old> <new>", and rewrites
// only the manifest's version line, the image reference, one machine-maintained comment line
// ("# catalog-follow: ...") and, for an app whose upgrade migrates_data, upgrade.data_since:
// kept when the release's migration paths (its sources entry) hold the same git objects as the
// pinned release's, the new version otherwise or when that cannot be read, so the next upgrade
// still copies the data first. Human comments and other upgrade rules are left as they are. A newer
// MAJOR.MINOR is only reported, as a ::warning::: its upgrade rules need a human. An app whose
// release cannot be resolved or verified is skipped with a ::warning::, never pinned. It exits
// non-zero only when it cannot run at all.
package main

import (
	"bytes"
	"cmp"
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

// source is where an image's releases are published, which workflow attests them and where
// the repository keeps the code that migrates stored data (files or directories).
type source struct {
	repo, workflow string
	migrations     []string
}

// sources maps each ghcr.io/busnes-app image to its repository. A Ky image added to the
// catalog needs an entry here (TestSourcesCoverCatalog). Without migrations every release of
// an app that migrates_data is taken to migrate.
var sources = map[string]source{
	"kycalendar":        {"Busnes-app/KyCalendar", "release-image.yml", []string{"internal/store/migrations"}},
	"kydns-server":      {"Busnes-app/KyDNS-server", "release.yml", []string{"internal/store/store.go"}},
	"kydrive-server":    {"Busnes-app/kydrive-server", "release-image.yml", []string{"internal/store/migrations", "internal/drive/schema.go"}},
	"kyidentity-server": {"Busnes-app/KyIdentity-server", "release-image.yml", nil},
	"kymessage-server":  {"Busnes-app/KyMessage-Server", "release-image.yml", []string{"internal/store/migrations"}},
	"kynotes-server":    {"Busnes-app/KyNotes-server", "release-image.yml", []string{"internal/storage/migrations"}},
	"kypost-server":     {"Busnes-app/KyPost-Server", "release-image.yml", nil},
	"kypulse-server":    {"Busnes-app/KyPulse-server", "release-image.yml", []string{"internal/store/migrations"}},
	"kyrecovery-server": {"Busnes-app/KyRecovery-server", "release-image.yml", []string{"internal/db/db.go"}},
	"kyyard":            {"Busnes-app/KyYard-server", "release-image.yml", []string{"internal/store/migrations"}},
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
	digest   func(image, tag string) (string, error)      // sha256:<hex> of the tag's manifest
	verify   func(ref, repo, identity string) error       // the attestation, pinned to identity
	object   func(repo, tag, path string) (string, error) // path's git object at tag, "" when absent
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
	d := deps{releases: ghReleases, digest: registryDigest("https://ghcr.io"), verify: ghVerify, object: ghObject,
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
		Upgrade struct {
			MigratesData bool   `yaml:"migrates_data"`
			DataSince    string `yaml:"data_since"`
		} `yaml:"upgrade"`
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
	if err == nil && man.Upgrade.MigratesData {
		since := to
		if sameData(src, man.Version, to, d) {
			since = cmp.Or(man.Upgrade.DataSince, man.Version)
		}
		next, err = setDataSince(next, man.Upgrade.DataSince, since)
	}
	if err != nil {
		d.warn("%s: %v; left at %s", app, err, man.Version)
		return bump{}, nil, false
	}
	return bump{app, man.Version, to}, next, true
}

// sameData reports whether every migration path of src holds the same git object at to as at
// from. No paths, a path absent at either tag or one that cannot be read is not the same.
func sameData(src source, from, to string, d deps) bool {
	for _, p := range src.migrations {
		a, err := d.object(src.repo, "v"+from, p)
		if err == nil && a != "" {
			var b string
			if b, err = d.object(src.repo, "v"+to, p); err == nil && a == b {
				continue
			}
		}
		if err != nil {
			d.warn("%s: reading %s at v%s and v%s: %v; data_since moves to v%s", src.repo, p, from, to, err, to)
		}
		return false
	}
	return len(src.migrations) > 0
}

// setDataSince sets upgrade.data_since to since: it replaces the line for old, which must
// occur once, or adds one after migrates_data.
func setDataSince(m []byte, old, since string) ([]byte, error) {
	r := [2]string{"\n  migrates_data: true\n", "\n  migrates_data: true\n  data_since: " + since + "\n"}
	if old != "" {
		r = [2]string{"\n  data_since: " + old + "\n", "\n  data_since: " + since + "\n"}
	}
	if n := bytes.Count(m, []byte(r[0])); n != 1 {
		return nil, fmt.Errorf("%q occurs %d times, not once", strings.TrimSpace(r[0]), n)
	}
	return bytes.Replace(m, []byte(r[0]), []byte(r[1]), 1), nil
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

// ghObject returns the git object (blob or tree) path names at tag, from its directory's
// listing; "" when the directory has no such entry.
func ghObject(repo, tag, path string) (string, error) {
	dir, name := filepath.Split(path)
	out, err := exec.Command("gh", "api", "repos/"+repo+"/contents/"+strings.TrimSuffix(dir, "/")+"?ref="+url.QueryEscape(tag)).Output()
	if err != nil {
		return "", fmt.Errorf("gh api: %w", err)
	}
	var entries []struct{ Name, SHA string }
	if err := json.Unmarshal(out, &entries); err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Name == name {
			return e.SHA, nil
		}
	}
	return "", nil
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
