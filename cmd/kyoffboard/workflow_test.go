package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type workflowStep struct {
	ID   string            `yaml:"id"`
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	With map[string]any    `yaml:"with"`
}

type workflowJob struct {
	Name        string            `yaml:"name"`
	Permissions map[string]string `yaml:"permissions"`
	Env         map[string]string `yaml:"env"`
	Strategy    struct {
		Matrix yaml.Node `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []workflowStep `yaml:"steps"`
}

type workflow struct {
	On          map[string]map[string][]string `yaml:"on"`
	Concurrency map[string]any                 `yaml:"concurrency"`
	Permissions map[string]string              `yaml:"permissions"`
	Jobs        yaml.Node                      `yaml:"jobs"`
}

func readWorkflow(t *testing.T, name string) (workflow, []string, map[string]workflowJob) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var w workflow
	if err := yaml.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	var ids []string
	jobs := map[string]workflowJob{}
	for i := 0; i+1 < len(w.Jobs.Content); i += 2 {
		var job workflowJob
		if err := w.Jobs.Content[i+1].Decode(&job); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, w.Jobs.Content[i].Value)
		jobs[w.Jobs.Content[i].Value] = job
	}
	return w, ids, jobs
}

// checkNames are the check-run names GitHub reports for ci.yml: an unnamed matrix
// job is "<id> (<value>, ...)" in matrix key order.
func checkNames(t *testing.T) []string {
	_, ids, jobs := readWorkflow(t, "ci.yml")
	var names []string
	for _, id := range ids {
		job := jobs[id]
		if job.Name != "" {
			names = append(names, job.Name)
			continue
		}
		combos := [][]string{nil}
		m := job.Strategy.Matrix.Content
		for i := 0; i+1 < len(m); i += 2 {
			var values []string
			if err := m[i+1].Decode(&values); err != nil {
				t.Fatal("unsupported matrix in ci.yml job ", id)
			}
			var next [][]string
			for _, c := range combos {
				for _, v := range values {
					next = append(next, append(slices.Clone(c), v))
				}
			}
			combos = next
		}
		for _, c := range combos {
			if c == nil {
				names = append(names, id)
			} else {
				names = append(names, id+" ("+strings.Join(c, ", ")+")")
			}
		}
	}
	return names
}

func TestImageWorkflowPublishesOnlyAttestedTaggedCommits(t *testing.T) {
	w, _, jobs := readWorkflow(t, "kyoffboard-image.yml")
	if len(w.On) != 1 || !slices.Equal(w.On["push"]["tags"], []string{"kyoffboard-v*"}) || len(w.On["push"]) != 1 {
		t.Fatal("trigger must be only a push of kyoffboard-v* tags", w.On)
	}
	if w.Concurrency["cancel-in-progress"] != false {
		t.Fatal("publication must never be cancelled")
	}
	if len(w.Permissions) != 1 || w.Permissions["contents"] != "read" {
		t.Fatal("workflow permissions must default to contents: read", w.Permissions)
	}
	job, ok := jobs["publish"]
	if !ok || len(jobs) != 1 {
		t.Fatal("one publish job expected")
	}
	want := map[string]string{"contents": "read", "packages": "write", "id-token": "write", "attestations": "write", "checks": "read"}
	if len(job.Permissions) != len(want) {
		t.Fatal("unexpected job permissions", job.Permissions)
	}
	for k, v := range want {
		if job.Permissions[k] != v {
			t.Fatal("unexpected job permissions", job.Permissions)
		}
	}
	if job.Env["IMAGE_NAME"] != "ghcr.io/busnes-app/kyoffboard" || job.Env["TAG"] != "${{ github.ref_name }}" {
		t.Fatal("unexpected image or tag source", job.Env)
	}

	steps := map[string]workflowStep{}
	pinned := regexp.MustCompile(`^[a-z0-9-]+/[a-z0-9-]+@[0-9a-f]{40}$`)
	for _, s := range job.Steps {
		steps[s.Name] = s
		if s.Uses != "" && !pinned.MatchString(s.Uses) {
			t.Fatal("action not pinned by commit SHA: ", s.Uses)
		}
	}
	step := func(name string) workflowStep {
		s, ok := steps[name]
		if !ok {
			t.Fatal("missing step: ", name)
		}
		return s
	}

	const tagPattern = `^kyoffboard-v([0-9]+\.[0-9]+\.[0-9]+)$`
	if !strings.Contains(step("Validate tag version").Run, `[[ ! "$TAG" =~ `+tagPattern+` ]]`) {
		t.Fatal("tag regex missing or changed")
	}
	tag := regexp.MustCompile(tagPattern)
	for value, ok := range map[string]bool{"kyoffboard-v1.2.3": true, "kyoffboard-v10.0.12": true, "v1.2.3": false, "kyoffboard-v1.2": false, "kyoffboard-v1.2.3-rc.1": false, "kyoffboard-v1.2.3\n": false} {
		if tag.MatchString(value) != ok {
			t.Fatal("tag regex wrong for ", value)
		}
	}

	checkout := step("Require the checkout to be the tagged commit").Run
	for _, s := range []string{`git rev-parse HEAD`, `git rev-parse "$GITHUB_SHA^{commit}"`, `git rev-parse "refs/tags/$TAG^{commit}"`} {
		if !strings.Contains(checkout, s) {
			t.Fatal("checkout guard lacks ", s)
		}
	}

	ci := step("Require CI to have passed for this commit")
	var required []string
	for _, line := range strings.Split(ci.Env["REQUIRED"], "\n") {
		if line = strings.TrimSpace(line); line != "" {
			required = append(required, line)
		}
	}
	names := checkNames(t)
	slices.Sort(required)
	slices.Sort(names)
	if !slices.Equal(required, names) || !slices.Contains(names, "kyoffboard image build & run") {
		t.Fatalf("REQUIRED must name every ci.yml job:\n got %q\nwant %q", required, names)
	}
	for _, s := range []string{`select(.app.slug == "github-actions")`, `filter=latest`, `'^completed/success$'`, `exit 1`} {
		if !strings.Contains(ci.Run, s) {
			t.Fatal("CI guard lacks ", s)
		}
	}

	build := step("Build and publish the immutable version tag")
	if build.With["tags"] != "${{ env.IMAGE_NAME }}:${{ steps.version.outputs.value }}" || build.With["file"] != "cmd/kyoffboard/Dockerfile" || build.With["push"] != true {
		t.Fatal("must push only the immutable version tag", build.With)
	}

	verify := step("Verify the attestation").Run
	for _, s := range []string{
		`gh attestation verify "oci://$IMAGE_NAME@$DIGEST" --repo "$GITHUB_REPOSITORY"`,
		`--cert-identity "https://github.com/$GITHUB_REPOSITORY/.github/workflows/kyoffboard-image.yml@refs/tags/$TAG"`,
		`.buildDefinition.resolvedDependencies[].digest.gitCommit`,
		`want="$(git rev-parse "$TAG^{commit}")"`,
		`!= "$want"`,
	} {
		if !strings.Contains(verify, s) {
			t.Fatal("attestation verification lacks ", s)
		}
	}
	if steps["Publish GitHub image attestation"].With["subject-digest"] != "${{ steps.build.outputs.digest }}" {
		t.Fatal("attestation must cover the pushed digest")
	}
}

// Deployments pin the digest kyoffboard-image.yml attested, never a local build.
func TestNoSourcePinsAKyoffboardDigest(t *testing.T) {
	pin := regexp.MustCompile(`kyoffboard[^\s"'` + "`" + `]*@sha256:[0-9a-f]{64}`)
	for _, root := range []string{filepath.Join("..", "..", "cmd"), filepath.Join("..", "..", "internal")} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			raw, err := os.ReadFile(path)
			if err == nil && pin.Match(raw) {
				t.Error("kyoffboard digest pinned in ", path)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
