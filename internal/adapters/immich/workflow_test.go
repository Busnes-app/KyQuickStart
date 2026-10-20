package immich

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const upstreamBase = "ghcr.io/immich-app/immich-server@sha256:949bbf9ff32543539f471bc275437e6d1a1a4fee43c55bb9e4c06b946909bf1c"

// The recorded sum is what immich-image.yml checks and labels the image with.
func TestSourcePatchSum(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("module", "source.patch"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("module", "source.patch.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if string(want) != hex.EncodeToString(sum[:])+"\n" {
		t.Fatalf("module/source.patch.sha256 is stale: want %x", sum)
	}
}

func TestOverlayDockerfile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("module", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	var from []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "FROM ") {
			from = append(from, line)
		}
	}
	if len(from) != 1 || from[0] != "FROM "+upstreamBase {
		t.Fatal("overlay must build only FROM the pinned upstream digest", from)
	}
	for _, s := range []string{
		`kyq.immich-access.source="1b6098c9dbfffe978bec2d414606ed7a4c8e019a"`,
		`kyq.immich-access.patch-sha256="$KYQ_SOURCE_PATCH_SHA256"`,
		`IMMICH_REPOSITORY_URL="https://github.com/Busnes-app/KyQuickStart"`,
		`IMMICH_SOURCE_URL="https://github.com/Busnes-app/KyQuickStart/tree/$SOURCE_TAG/internal/adapters/immich/module"`,
		`org.opencontainers.image.licenses="AGPL-3.0-only"`,
	} {
		if !strings.Contains(text, s) {
			t.Fatal("overlay lacks ", s)
		}
	}
}

func TestImageWorkflow(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "immich-image.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		On          map[string]map[string][]string `yaml:"on"`
		Permissions map[string]string              `yaml:"permissions"`
		Jobs        map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Env         map[string]string `yaml:"env"`
			Steps       []struct {
				ID   string         `yaml:"id"`
				Name string         `yaml:"name"`
				Uses string         `yaml:"uses"`
				Run  string         `yaml:"run"`
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	if len(w.On) != 1 || len(w.On["push"]["tags"]) != 1 || w.On["push"]["tags"][0] != "immich-v*" {
		t.Fatal("must run only on immich-v* tags", w.On)
	}
	if len(w.Permissions) != 1 || w.Permissions["contents"] != "read" || len(w.Jobs) != 1 {
		t.Fatal("top-level permissions must be contents: read, one job")
	}
	job, ok := w.Jobs["publish"]
	if !ok || job.Env["UPSTREAM_COMMIT"] != "1b6098c9dbfffe978bec2d414606ed7a4c8e019a" || job.Env["BASE"] != upstreamBase ||
		job.Env["IMAGE_NAME"] != "ghcr.io/busnes-app/kyq-immich-server" || !strings.HasPrefix(job.Env["NODE_IMAGE"], "node@sha256:") {
		t.Fatal("publish job must pin upstream commit, base, image name and Node image", job.Env)
	}
	order := []string{
		"Validate tag version", "", "Require the checkout to be the tagged commit", "Require CI to have passed for this commit",
		"Fetch upstream and verify its commit", "Check and apply the module patch", "Build the server in upstream's order",
		"Require the overlay's pinned base", "", "", "Build and publish the immutable version tag",
		"Require the image labels to name the tag, source and patch", "Publish GitHub image attestation", "Verify the attestation",
	}
	if len(job.Steps) != len(order) {
		t.Fatal("unexpected step count ", len(job.Steps))
	}
	runs := map[string]string{}
	for i, s := range job.Steps {
		if s.Name != order[i] {
			t.Fatalf("step %d is %q, want %q", i, s.Name, order[i])
		}
		if s.Uses != "" && !strings.Contains(s.Uses, "@") || strings.Contains(s.Uses, "@v") {
			t.Fatal("actions must be pinned by commit: ", s.Uses)
		}
		runs[s.Name] = s.Run
	}
	for step, needles := range map[string][]string{
		"Validate tag version":                                       {`^immich-v(3\.2\.0-ky\.[1-9][0-9]*)$`},
		"Fetch upstream and verify its commit":                       {`git ls-remote "$UPSTREAM" "refs/tags/$UPSTREAM_TAG"`, `"$head" != "$UPSTREAM_COMMIT"`},
		"Check and apply the module patch":                           {`source.patch.sha256`, `git -C upstream apply --check "$patch"`},
		"Build the server in upstream's order":                       {`--frozen-lockfile --ignore-scripts`, `@immich/sdk build`, `@immich/plugin-sdk build`, `tsc --noEmit`, `--filter immich build`},
		"Require the overlay's pinned base":                          {`"FROM $BASE"`},
		"Require the image labels to name the tag, source and patch": {`kyq.immich-access.source`, `kyq.immich-access.patch-sha256`},
		"Verify the attestation": {
			`--cert-identity "https://github.com/$GITHUB_REPOSITORY/.github/workflows/immich-image.yml@refs/tags/$TAG"`,
			`.buildDefinition.resolvedDependencies[].digest.gitCommit`,
		},
	} {
		for _, n := range needles {
			if !strings.Contains(runs[step], n) {
				t.Errorf("%s lacks %s", step, n)
			}
		}
	}
	for _, s := range job.Steps {
		if s.Name == "Build and publish the immutable version tag" &&
			(s.With["tags"] != "${{ env.IMAGE_NAME }}:${{ steps.version.outputs.value }}" || s.With["file"] != "internal/adapters/immich/module/Dockerfile" || s.With["push"] != true || s.With["platforms"] != "linux/amd64") {
			t.Fatal("must push only the immutable amd64 version tag of the overlay", s.With)
		}
	}
}
