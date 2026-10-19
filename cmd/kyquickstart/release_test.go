package main

import (
	"bytes"
	"debug/buildinfo"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const script = "../../scripts/release-binaries.sh"

var digest = "sha256:" + strings.Repeat("0123456789abcdef", 4)

func release(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	return exec.Command("sh", append([]string{script}, args...)...).CombinedOutput()
}

func TestReleaseBinariesStampThePin(t *testing.T) {
	pin := "ghcr.io/busnes-app/kyquickstart-runner:9.8.7@" + digest
	dist := filepath.Join(t.TempDir(), "dist")
	if out, err := release(t, "9.8.7", pin, dist); err != nil {
		t.Fatalf("release-binaries.sh: %v\n%s", err, out)
	}
	var want, got []string
	for _, p := range []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64"} {
		want = append(want, "kyquickstart-9.8.7-"+p)
	}
	entries, _ := os.ReadDir(dist)
	for _, e := range entries {
		got = append(got, e.Name())
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("dist holds %v, want %v", got, want)
	}
	for _, name := range want {
		f := filepath.Join(dist, name)
		goos, goarch, _ := strings.Cut(strings.TrimPrefix(name, "kyquickstart-9.8.7-"), "-")
		info, err := buildinfo.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		settings := map[string]string{}
		for _, s := range info.Settings {
			settings[s.Key] = s.Value
		}
		for k, v := range map[string]string{"-trimpath": "true", "CGO_ENABLED": "0", "GOOS": goos, "GOARCH": goarch} {
			if settings[k] != v {
				t.Errorf("%s: build setting %s=%q, want %q", name, k, settings[k], v)
			}
		}
		// -trimpath keeps -ldflags out of the build info, so the stamp is checked in the bytes.
		if b, _ := os.ReadFile(f); !bytes.Contains(b, []byte(pin)) {
			t.Errorf("%s does not carry %s", name, pin)
		}
	}
	host := filepath.Join(dist, "kyquickstart-9.8.7-"+runtime.GOOS+"-"+runtime.GOARCH)
	for args, want := range map[string]string{"version": "9.8.7\n", "version --runner-image": pin + "\n"} {
		if out, err := exec.Command(host, strings.Fields(args)...).Output(); err != nil || string(out) != want {
			t.Errorf("%s: %q %v, want %q", args, out, err, want)
		}
	}
}

func TestReleaseBinariesRefuse(t *testing.T) {
	pin := func(repo, tag, d string) string { return "ghcr.io/busnes-app/" + repo + ":" + tag + "@" + d }
	good := pin("kyquickstart-runner", "9.8.7", digest)
	for name, args := range map[string][2]string{
		"v prefix":       {"v9.8.7", pin("kyquickstart-runner", "v9.8.7", digest)},
		"empty version":  {"", good},
		"short version":  {"9.8", pin("kyquickstart-runner", "9.8", digest)},
		"other tag":      {"9.8.7", pin("kyquickstart-runner", "9.8.6", digest)},
		"dot is literal": {"9.8.7", pin("kyquickstart-runner", "9x8x7", digest)},
		"no digest":      {"9.8.7", "ghcr.io/busnes-app/kyquickstart-runner:9.8.7"},
		"63 hex":         {"9.8.7", pin("kyquickstart-runner", "9.8.7", digest[:len(digest)-1])},
		"uppercase":      {"9.8.7", pin("kyquickstart-runner", "9.8.7", strings.ToUpper(digest))},
		"other repo":     {"9.8.7", pin("kyyard", "9.8.7", digest)},
		"two lines":      {"9.8.7", good + "\n" + good},
	} {
		dist := filepath.Join(t.TempDir(), "dist")
		if out, err := release(t, args[0], args[1], dist); err == nil {
			t.Errorf("%s: accepted\n%s", name, out)
		}
		if _, err := os.Lstat(dist); !os.IsNotExist(err) {
			t.Errorf("%s: created %s (%v)", name, dist, err)
		}
	}

	dist := t.TempDir()
	keep := filepath.Join(dist, "keep")
	os.WriteFile(keep, []byte("mine"), 0o600)
	if out, err := release(t, "9.8.7", good, dist); err == nil {
		t.Errorf("existing OUTDIR: accepted\n%s", out)
	}
	if entries, _ := os.ReadDir(dist); len(entries) != 1 {
		t.Errorf("existing OUTDIR changed: %v", entries)
	}
	if b, _ := os.ReadFile(keep); string(b) != "mine" {
		t.Errorf("existing OUTDIR file changed: %q", b)
	}
}

// A source build pins no runner image, so it cannot install KyYard.
func TestReleaseSourceBuildPinsNothing(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "kyquickstart")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	if out, err := exec.Command(bin, "version").Output(); err != nil || string(out) != "dev\n" {
		t.Errorf("version: %q %v", out, err)
	}
	out, err := exec.Command(bin, "version", "--runner-image").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "this build pins no suite runner image (source build); use a release binary") {
		t.Errorf("version --runner-image: %q %v", out, err)
	}
	// A stray argument prints the usage, which names the one flag.
	out, err = exec.Command(bin, "version", "extra").CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(out), "version [--runner-image]") {
		t.Errorf("version extra: %v, %q", err, out)
	}
}

type workflow struct {
	Permissions map[string]string
	Jobs        map[string]struct {
		Needs       []string
		Env         map[string]string
		Permissions map[string]string
		Steps       []struct {
			Name, Uses, Run string
			Env             map[string]string
		}
	}
}

func TestReleaseWorkflow(t *testing.T) {
	b, err := os.ReadFile("../../.github/workflows/release-image.yml")
	if err != nil {
		t.Fatal(err)
	}
	var w workflow
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	if len(w.Permissions) != 1 || w.Permissions["contents"] != "read" {
		t.Errorf("top-level permissions %v, want contents: read", w.Permissions)
	}
	for _, j := range []string{"publish", "binaries", "promote"} {
		if _, ok := w.Jobs[j]; !ok {
			t.Fatalf("no %s job", j)
		}
	}
	if !slices.Contains(w.Jobs["binaries"].Needs, "publish") {
		t.Errorf("binaries needs %v, want publish", w.Jobs["binaries"].Needs)
	}
	if !slices.Contains(w.Jobs["promote"].Needs, "binaries") || !slices.Contains(w.Jobs["promote"].Needs, "publish") {
		t.Errorf("promote needs %v, want publish and binaries", w.Jobs["promote"].Needs)
	}

	if _, ok := w.Jobs["binaries"].Env["GH_TOKEN"]; ok {
		t.Error("binaries sets GH_TOKEN for every step; set it only on the gh steps")
	}

	pinned := regexp.MustCompile(`@[0-9a-f]{40}$`)
	runs := map[string]string{}
	steps := map[string][]string{}
	required := ""
	for name, j := range w.Jobs {
		if got := j.Permissions["contents"] == "write"; got != (name == "binaries") {
			t.Errorf("%s: contents: %q", name, j.Permissions["contents"])
		}
		if got := j.Permissions["packages"] == "write"; got != (name == "publish" || name == "promote") {
			t.Errorf("%s: packages: %q", name, j.Permissions["packages"])
		}
		for _, s := range j.Steps {
			if s.Uses != "" && !pinned.MatchString(s.Uses) {
				t.Errorf("%s: %s is not pinned to a commit", name, s.Uses)
			}
			if strings.Contains(s.Run, "${{") {
				t.Errorf("%s: step %q expands an expression in run:; pass it through env:", name, s.Name)
			}
			runs[name] += s.Run + "\n"
			steps[name] = append(steps[name], s.Name)
			if name == "publish" && s.Env["REQUIRED"] != "" {
				required = s.Env["REQUIRED"]
			}
		}
	}

	for job, wants := range map[string][]string{
		"publish": {`grep -qx 'var version = "dev"'`, `grep -qx 'var runnerImage = ""'`, `git rev-parse "$GITHUB_SHA^{commit}"`, "check-runs?filter=latest"},
		"binaries": {"scripts/release-binaries.sh", `git rev-parse "$GITHUB_SHA^{commit}"`, "gh attestation verify",
			"release-image.yml@refs/tags/$TAG", "gh release upload", "gh release download", "gh release delete-asset", "version --runner-image"},
	} {
		for _, want := range wants {
			if !strings.Contains(runs[job], want) {
				t.Errorf("%s runs no %q", job, want)
			}
		}
	}
	for _, job := range []string{"test (ubuntu-latest, 1.26.x)", "test (macos-latest, stable)", "vulncheck", "e2e", "Runner image build & run"} {
		if !slices.Contains(strings.Split(required, "\n"), job) {
			t.Errorf("publish's REQUIRED list lacks %q", job)
		}
	}
	for _, s := range []string{"Decide whether this release may move stable", "Promote the verified digest to stable"} {
		if !slices.Contains(steps["promote"], s) || slices.Contains(steps["publish"], s) {
			t.Errorf("step %q must run in promote only", s)
		}
	}
}
