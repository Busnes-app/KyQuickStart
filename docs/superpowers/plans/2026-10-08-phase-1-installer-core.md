# Phase 1: Installer Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `kyquickstart apply` deploys a catalog app to a Docker host over SSH, idempotently and resumably, with secrets generated on the target and a target-side lock. No OIDC yet.

**Architecture:** A step engine runs Inspect → Apply → Verify steps and records one result file per step. Each app is an embedded manifest plus a Compose template, rendered and checked at load. The Docker-host driver turns an app into three steps (secrets, deploy, health) over a `remote.Runner`, which is SSH in production and local `sh` in tests.

**Tech Stack:** Go 1.26.6, `golang.org/x/crypto` v0.55.0 (`ssh`, `ssh/agent`, `ssh/knownhosts`), `go.yaml.in/yaml/v3` v3.0.5, standard library otherwise.

**Spec:**
- `docs/superpowers/plans/2026-10-06-kyquickstart-roadmap.md`, Phase 1 (fixed interfaces and behaviors)
- `docs/superpowers/specs/2026-10-06-installer-architecture-design.md`

## Global Constraints

- Module `github.com/Busnes-app/kyquickstart`, `go 1.26.6`.
- One static binary; no runtime dependency on the operator workstation beyond an SSH agent.
- No secret in `stack.yaml`, step results, logs or error output; secrets are created on targets and read back.
- Every installer-managed container carries `ky.managed-by=kyquickstart` and `ky.release-set=<release set>`.
- Images pinned by digest; a Compose service whose image is not a manifest digest is rejected at load.
- Every step is Inspect → Apply → Verify and idempotent; stop at the first failure.
- Target-side lock for `apply`; a stale lock is reported with holder and age, never broken.
- SSH host keys are pinned in `<state>/known_hosts`; a changed key is a hard failure.
- Every remote command runs under `sh -c`, whatever the login shell (fish is common).
- Preflight floors: Docker Engine 24.0, Compose 2.21, writable root.
- `make ci` (tidy check, gofmt, vet, race tests) passes at the end of every task. Run `gofmt -w` on new files; code blocks here are not guaranteed gofmt-aligned.

## Review Focus

- **A crashed run leaves the target lock behind.** The next run stops, names the holder and age, and leaves the lock untouched. Tests: Task 6 `TestAcquireReportsHolderAndKeepsLock`, Task 11 e2e.
- **A target's SSH host key changes.** Hard failure even when `--trust-host-key` names the new key and an interactive confirm would say yes. Test: Task 9 `TestChangedHostKeyIsRefused`.
- **A command's error output echoes a secret.** The secret is redacted on screen, in the returned error and in the result file. Test: Task 5 `TestFailureRedactsSecrets`.
- **An operator stops containers by hand between runs.** A re-run fails the skip check's Verify and redeploys. Tests: Task 5 `TestFailedVerifyRerunsStep`, Task 7 `TestDeployInspectSeesStoppedService`, Task 11 e2e.
- **`stack.yaml` has a typo, an unknown key or a shell metacharacter.** Rejected at load, before any connection. Tests: Task 2 `TestParseRejects`, Task 10 `TestBadStackFailsBeforeSSH`.

---

## File Structure

| Path | Responsibility |
|---|---|
| `go.mod`, `Makefile`, `.github/workflows/ci.yml`, `.gitignore` | Module, `make ci`, CI |
| `internal/remote/remote.go` | `Runner`, `ExitError`, `Quote` |
| `internal/remote/local.go` | `Local` runner (`sh -c` on this machine), used by tests |
| `internal/remote/ssh.go` | `Dial`, `SSH` runner, pinned host keys |
| `internal/stack/stack.go` | `stack.yaml` v1 schema, strict decode, defaults, validation |
| `internal/catalog/catalog.go` | Manifest schema, load, render, Compose checks |
| `internal/catalog/embed.go`, `internal/catalog/apps/.gitkeep` | Embedded catalog (empty in Phase 1) |
| `internal/catalog/testdata/apps/hello/` | Fixture app (`traefik/whoami`) for tests and e2e |
| `internal/plan/plan.go` | `Order`: dependency order |
| `internal/engine/engine.go` | `Step`, `ErrTransient`, `Engine`, result files |
| `internal/engine/redact.go` | `Redactor` |
| `internal/dockerhost/app.go` | `App`, `Steps`, shared helpers |
| `internal/dockerhost/secrets.go` | Secrets step |
| `internal/dockerhost/deploy.go` | Deploy step, managed-label override |
| `internal/dockerhost/health.go` | Health step, `docker compose ps` parsing |
| `internal/dockerhost/lock.go` | `Acquire`, `LockedError` |
| `internal/dockerhost/preflight.go` | `Preflight`, `Finding` |
| `internal/cli/cli.go` | Flags, `Run`, preflight and apply wiring |
| `cmd/kyquickstart/main.go` | Signals, exit codes |
| `test/e2e/` | Build tag `e2e`: sshd container on the host Docker socket |

Target layout: `<root>/.lock/holder`, `<root>/<app>/compose.yaml`, `compose.kyq.yaml`, `.input-hash`, `secrets/<name>` (mode 600, directory 700). Compose project `kyq-<app>`.

Workstation layout: `<state>/stack.yaml`, `<state>/known_hosts`, `<state>/results/<step-id>.json`.

---

### Task 1: Module, remote basics, CI

**Files:**
- Create: `go.mod`, `Makefile`, `.github/workflows/ci.yml`, `.gitignore`
- Create: `internal/remote/remote.go`, `internal/remote/local.go`
- Test: `internal/remote/remote_test.go`

**Interfaces:**
- Produces: `remote.Runner`, `remote.ExitError{Code int; Stderr []byte}`, `remote.Quote(string) string`, `remote.Local{}`.

- [ ] **Step 1: Create the module and build files**

`go.mod`:

```
module github.com/Busnes-app/kyquickstart

go 1.26.6
```

`.gitignore`:

```
/kyquickstart
/coverage.out
```

`Makefile` (recipe lines are tabs):

```make
.PHONY: build test-race tidy-check lint ci e2e

build:
	go build -trimpath -o kyquickstart ./cmd/kyquickstart

test-race:
	go test -race -count=1 ./...

# A stale go.sum fails CI; fail here first.
tidy-check:
	go mod tidy
	git diff --exit-code -- go.mod go.sum

lint:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	go vet -tags e2e ./...

ci: tidy-check lint test-race

# Needs Docker on this machine.
e2e:
	go test -tags e2e -count=1 -v ./test/e2e/
```

`.github/workflows/ci.yml`:

```yaml
name: CI

on:
  push:
    branches: [master]
  pull_request:

permissions:
  contents: read

jobs:
  test:
    # The binary runs on operator workstations, so macOS is in the matrix.
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, macos-latest]
        go: ['1.26.x', 'stable']
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ matrix.go }}
      - run: make ci

  vulncheck:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: 'stable'
      - run: go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

- [ ] **Step 2: Write the failing tests**

`internal/remote/remote_test.go`:

```go
package remote

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestQuoteSurvivesShell(t *testing.T) {
	for _, s := range []string{"", "plain", "a b", "it's", "$(id)", "`id`", "a\nb", `\'"`} {
		out, err := Local{}.Run(context.Background(), "printf %s "+Quote(s), nil)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if string(out) != s {
			t.Errorf("Quote(%q) came back as %q", s, out)
		}
	}
}

func TestLocalExitError(t *testing.T) {
	out, err := Local{}.Run(context.Background(), "echo out; echo err >&2; exit 3", nil)
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 {
		t.Fatalf("err = %v, want exit 3", err)
	}
	if string(out) != "out\n" || string(ee.Stderr) != "err\n" {
		t.Errorf("stdout %q, stderr %q", out, ee.Stderr)
	}
	if !strings.Contains(ee.Error(), "exit status 3: err") {
		t.Errorf("Error() = %q", ee.Error())
	}
}

func TestLocalStdin(t *testing.T) {
	out, err := Local{}.Run(context.Background(), "cat", []byte("hello"))
	if err != nil || string(out) != "hello" {
		t.Fatalf("out %q, err %v", out, err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/remote/`
Expected: FAIL, `undefined: Local`.

- [ ] **Step 4: Implement**

`internal/remote/remote.go`:

```go
// Package remote runs shell commands on a target.
package remote

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// Runner runs one POSIX shell command. A non-zero exit returns *ExitError, and stdout is
// returned either way.
type Runner interface {
	Run(ctx context.Context, cmd string, stdin []byte) (stdout []byte, err error)
}

// ExitError is a command that ran and exited non-zero.
type ExitError struct {
	Code   int
	Stderr []byte
}

func (e *ExitError) Error() string {
	msg := bytes.TrimSpace(e.Stderr)
	if len(msg) > 2048 {
		msg = msg[len(msg)-2048:]
	}
	return fmt.Sprintf("exit status %d: %s", e.Code, msg)
}

// Quote returns s as a single POSIX shell word.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
```

`internal/remote/local.go`:

```go
package remote

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// Local runs commands with sh on this machine. Tests use it in place of SSH.
type Local struct{}

func (Local) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Stdin = bytes.NewReader(stdin)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	if ctx.Err() != nil {
		return out.Bytes(), ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.Bytes(), &ExitError{Code: ee.ExitCode(), Stderr: errb.Bytes()}
	}
	return out.Bytes(), err
}
```

- [ ] **Step 5: Run checks**

Run: `go test ./internal/remote/ && make ci`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod Makefile .gitignore .github/workflows/ci.yml internal/remote
git commit -m "feat: add module skeleton and remote runner basics"
```

---

### Task 2: `stack.yaml` schema

**Files:**
- Create: `internal/stack/stack.go`
- Test: `internal/stack/stack_test.go`

**Interfaces:**
- Produces: `stack.Stack{Version int; Targets []Target; Apps []App}`, `stack.Target{Name, Host string; Port int; User, Root string}`, `stack.App{Name, Target string}`, `stack.Load(path string) (*Stack, error)`, `stack.Parse([]byte) (*Stack, error)`, `(*Stack).Target(name string) (Target, bool)`, `stack.ValidName(string) bool`, `stack.DefaultRoot`.

- [ ] **Step 1: Add the dependency**

Run: `go get go.yaml.in/yaml/v3@v3.0.5`

- [ ] **Step 2: Write the failing tests**

`internal/stack/stack_test.go`:

```go
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/stack/`
Expected: FAIL, `undefined: Parse`.

- [ ] **Step 4: Implement**

`internal/stack/stack.go`:

```go
// Package stack reads stack.yaml, the secret-free plan every command consumes.
package stack

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

const DefaultRoot = "/opt/kyquickstart"

type Stack struct {
	Version int      `yaml:"version"`
	Targets []Target `yaml:"targets"`
	Apps    []App    `yaml:"apps"`
}

type Target struct {
	Name string `yaml:"name"`
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	User string `yaml:"user"`
	Root string `yaml:"root"`
}

type App struct {
	Name   string `yaml:"name"`
	Target string `yaml:"target"`
}

var (
	nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	hostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	userRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	rootRE = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)
)

// ValidName reports whether s is a usable target or app name. App names become step IDs,
// file names and Compose project names.
func ValidName(s string) bool { return nameRE.MatchString(s) }

func Load(path string) (*Stack, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

func Parse(b []byte) (*Stack, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var s Stack
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("stack.yaml: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("stack.yaml: must hold exactly one document")
	}
	for i := range s.Targets {
		if s.Targets[i].Port == 0 {
			s.Targets[i].Port = 22
		}
		if s.Targets[i].Root == "" {
			s.Targets[i].Root = DefaultRoot
		}
	}
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("stack.yaml: %w", err)
	}
	return &s, nil
}

func (s *Stack) Target(name string) (Target, bool) {
	for _, t := range s.Targets {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}

func (s *Stack) validate() error {
	if s.Version != 1 {
		return fmt.Errorf("version %d: only version 1 is supported", s.Version)
	}
	if len(s.Targets) == 0 {
		return errors.New("no targets")
	}
	seen := map[string]bool{}
	for _, t := range s.Targets {
		switch {
		case !ValidName(t.Name):
			return fmt.Errorf("target %q: name must match %s", t.Name, nameRE)
		case seen[t.Name]:
			return fmt.Errorf("duplicate target %q", t.Name)
		case !hostRE.MatchString(t.Host) && net.ParseIP(t.Host) == nil:
			return fmt.Errorf("target %q: host %q is not a hostname or IP address", t.Name, t.Host)
		case t.Port < 1 || t.Port > 65535:
			return fmt.Errorf("target %q: port %d is out of range", t.Name, t.Port)
		case !userRE.MatchString(t.User):
			return fmt.Errorf("target %q: user %q must match %s", t.Name, t.User, userRE)
		case !validRoot(t.Root):
			return fmt.Errorf("target %q: root %q must be an absolute path without . or ..", t.Name, t.Root)
		}
		seen[t.Name] = true
	}
	apps := map[string]bool{}
	for _, a := range s.Apps {
		switch {
		case !ValidName(a.Name):
			return fmt.Errorf("app %q: name must match %s", a.Name, nameRE)
		case apps[a.Name]:
			return fmt.Errorf("duplicate app %q", a.Name)
		case !seen[a.Target]:
			return fmt.Errorf("app %q: unknown target %q", a.Name, a.Target)
		}
		apps[a.Name] = true
	}
	return nil
}

func validRoot(r string) bool {
	if !rootRE.MatchString(r) {
		return false
	}
	for _, seg := range strings.Split(r[1:], "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}
```

- [ ] **Step 5: Run checks**

Run: `go test ./internal/stack/ && make ci`
Expected: PASS. If the "unknown key" case fails, print the error and adjust only the expected substring to yaml.v3's actual "field usr not found" wording.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/stack
git commit -m "feat: add stack.yaml schema with strict validation"
```

---

### Task 3: Catalog

**Files:**
- Create: `internal/catalog/catalog.go`, `internal/catalog/embed.go`, `internal/catalog/apps/.gitkeep` (empty)
- Create: `internal/catalog/testdata/apps/hello/manifest.yaml`, `internal/catalog/testdata/apps/hello/compose.yaml.tmpl`
- Test: `internal/catalog/catalog_test.go`

**Interfaces:**
- Consumes: `stack.ValidName`.
- Produces: `catalog.Manifest{Name string; Images map[string]string; Secrets []string; DependsOn []string; Health Health}`, `catalog.Health{TimeoutSeconds int}`, `catalog.App{Manifest; Compose []byte; Services []string}`, `catalog.Catalog` (`map[string]App`), `catalog.Load(fs.FS) (Catalog, error)`, `catalog.Embedded() fs.FS`.

- [ ] **Step 1: Create the fixture app**

`internal/catalog/testdata/apps/hello/manifest.yaml`:

```yaml
name: hello
images:
  whoami: traefik/whoami:v1.11.0@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab
secrets: [token]
health:
  timeout_seconds: 60
```

`internal/catalog/testdata/apps/hello/compose.yaml.tmpl`:

```yaml
services:
  whoami:
    image: {{ .Images.whoami }}
    secrets: [token]
secrets:
  token:
    file: ./secrets/token
```

- [ ] **Step 2: Write the failing tests**

`internal/catalog/catalog_test.go`:

```go
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/catalog/`
Expected: FAIL, `undefined: Load`.

- [ ] **Step 4: Implement**

`internal/catalog/embed.go`:

```go
package catalog

import (
	"embed"
	"io/fs"
)

//go:embed all:apps
var embedded embed.FS

// Embedded is the catalog shipped in this binary: one directory per app.
func Embedded() fs.FS {
	sub, err := fs.Sub(embedded, "apps")
	if err != nil {
		panic(err) // "apps" is a valid path; fs.Sub fails only on invalid ones
	}
	return sub
}
```

`internal/catalog/catalog.go`:

```go
// Package catalog loads app manifests and renders their Compose files.
package catalog

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"text/template"

	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
)

type Manifest struct {
	Name      string            `yaml:"name"`
	Images    map[string]string `yaml:"images"`
	Secrets   []string          `yaml:"secrets"`
	DependsOn []string          `yaml:"depends_on"`
	Health    Health            `yaml:"health"`
}

type Health struct {
	TimeoutSeconds int `yaml:"timeout_seconds"`
}

// App is a validated manifest with its rendered Compose file.
type App struct {
	Manifest
	Compose  []byte
	Services []string // sorted
}

type Catalog map[string]App

var (
	keyRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	imageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
)

// Load reads every directory of fsys as one app.
func Load(fsys fs.FS) (Catalog, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	cat := Catalog{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		app, err := loadApp(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("app %s: %w", e.Name(), err)
		}
		cat[app.Name] = app
	}
	return cat, nil
}

func loadApp(fsys fs.FS, dir string) (App, error) {
	raw, err := fs.ReadFile(fsys, path.Join(dir, "manifest.yaml"))
	if err != nil {
		return App{}, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return App{}, fmt.Errorf("manifest.yaml: %w", err)
	}
	if err := m.validate(dir); err != nil {
		return App{}, fmt.Errorf("manifest.yaml: %w", err)
	}
	text, err := fs.ReadFile(fsys, path.Join(dir, "compose.yaml.tmpl"))
	if err != nil {
		return App{}, err
	}
	t, err := template.New(dir).Option("missingkey=error").Parse(string(text))
	if err != nil {
		return App{}, fmt.Errorf("compose.yaml.tmpl: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, m); err != nil {
		return App{}, fmt.Errorf("compose.yaml.tmpl: %w", err)
	}
	services, err := checkCompose(buf.Bytes(), m.Images)
	if err != nil {
		return App{}, fmt.Errorf("compose.yaml.tmpl: %w", err)
	}
	return App{Manifest: m, Compose: buf.Bytes(), Services: services}, nil
}

func (m Manifest) validate(dir string) error {
	if m.Name != dir || !stack.ValidName(m.Name) {
		return fmt.Errorf("name %q must match its directory %q and be a valid app name", m.Name, dir)
	}
	if len(m.Images) == 0 {
		return fmt.Errorf("no images")
	}
	for k, ref := range m.Images {
		if !keyRE.MatchString(k) {
			return fmt.Errorf("image key %q must match %s", k, keyRE)
		}
		if !imageRE.MatchString(ref) {
			return fmt.Errorf("image %s = %q is not pinned by digest", k, ref)
		}
	}
	seen := map[string]bool{}
	for _, s := range m.Secrets {
		if !keyRE.MatchString(s) || seen[s] {
			return fmt.Errorf("secret %q must be unique and match %s", s, keyRE)
		}
		seen[s] = true
	}
	for _, d := range m.DependsOn {
		if !stack.ValidName(d) || d == m.Name {
			return fmt.Errorf("depends_on %q is not another app", d)
		}
	}
	if m.Health.TimeoutSeconds < 1 || m.Health.TimeoutSeconds > 1800 {
		return fmt.Errorf("health.timeout_seconds %d must be 1-1800", m.Health.TimeoutSeconds)
	}
	return nil
}

// checkCompose returns the sorted service names, refusing builds and any image the
// manifest does not pin.
func checkCompose(b []byte, images map[string]string) ([]string, error) {
	var c struct {
		Services map[string]struct {
			Image string `yaml:"image"`
			Build any    `yaml:"build"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if len(c.Services) == 0 {
		return nil, fmt.Errorf("no services")
	}
	pinned := map[string]bool{}
	for _, ref := range images {
		pinned[ref] = true
	}
	var names []string
	for name, s := range c.Services {
		if s.Build != nil {
			return nil, fmt.Errorf("service %s: build is not allowed", name)
		}
		if !pinned[s.Image] {
			return nil, fmt.Errorf("service %s: image %q is not one of the manifest's pinned images", name, s.Image)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
```

- [ ] **Step 5: Run checks**

Run: `go test ./internal/catalog/ && make ci`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/catalog
git commit -m "feat: add catalog manifests with digest-pinned compose rendering"
```

---

### Task 4: Dependency order

**Files:**
- Create: `internal/plan/plan.go`
- Test: `internal/plan/plan_test.go`

**Interfaces:**
- Consumes: `catalog.Catalog`, `catalog.App`, `catalog.Manifest`.
- Produces: `plan.Order(apps []string, cat catalog.Catalog) ([]string, error)`.

- [ ] **Step 1: Write the failing tests**

`internal/plan/plan_test.go`:

```go
package plan

import (
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
)

func cat(deps map[string][]string) catalog.Catalog {
	c := catalog.Catalog{}
	for name, d := range deps {
		c[name] = catalog.App{Manifest: catalog.Manifest{Name: name, DependsOn: d}}
	}
	return c
}

func TestOrder(t *testing.T) {
	c := cat(map[string][]string{"web": {"db"}, "db": nil, "cache": nil, "worker": {"db", "cache"}})
	got, err := Order([]string{"worker", "web", "db", "cache"}, c)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cache", "db", "web", "worker"}
	if !slices.Equal(got, want) {
		t.Errorf("Order = %v, want %v", got, want)
	}
}

func TestOrderErrors(t *testing.T) {
	c := cat(map[string][]string{"a": {"b"}, "b": {"a"}, "c": {"missing"}, "missing": nil, "d": nil})
	cases := map[string]struct {
		apps []string
		want string
	}{
		"unknown app":        {[]string{"zzz"}, `"zzz" is not in the catalog`},
		"missing dependency": {[]string{"c"}, `"c" needs "missing", which is not selected`},
		"cycle":              {[]string{"a", "b", "d"}, "dependency cycle among a, b"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Order(tc.apps, c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/plan/`
Expected: FAIL, `undefined: Order`.

- [ ] **Step 3: Implement**

`internal/plan/plan.go`:

```go
// Package plan orders the selected apps.
package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
)

// Order returns apps with every app after its dependencies; ties sort by name.
func Order(apps []string, cat catalog.Catalog) ([]string, error) {
	names := append([]string(nil), apps...)
	sort.Strings(names)
	selected := map[string]bool{}
	for _, a := range names {
		if _, ok := cat[a]; !ok {
			return nil, fmt.Errorf("app %q is not in the catalog", a)
		}
		selected[a] = true
	}
	waiting := map[string]int{}
	dependents := map[string][]string{}
	for _, a := range names {
		for _, d := range cat[a].DependsOn {
			if !selected[d] {
				return nil, fmt.Errorf("app %q needs %q, which is not selected", a, d)
			}
			waiting[a]++
			dependents[d] = append(dependents[d], a)
		}
	}
	var ready, out []string
	for _, a := range names {
		if waiting[a] == 0 {
			ready = append(ready, a)
		}
	}
	for len(ready) > 0 {
		sort.Strings(ready)
		a := ready[0]
		ready = ready[1:]
		out = append(out, a)
		for _, b := range dependents[a] {
			if waiting[b]--; waiting[b] == 0 {
				ready = append(ready, b)
			}
		}
	}
	if len(out) != len(names) {
		var stuck []string
		for _, a := range names {
			if waiting[a] > 0 {
				stuck = append(stuck, a)
			}
		}
		return nil, fmt.Errorf("dependency cycle among %s", strings.Join(stuck, ", "))
	}
	return out, nil
}
```

- [ ] **Step 4: Run checks**

Run: `go test ./internal/plan/ && make ci`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/plan
git commit -m "feat: order apps by dependency"
```

---

### Task 5: Step engine and redactor

**Files:**
- Create: `internal/engine/engine.go`, `internal/engine/redact.go`
- Test: `internal/engine/engine_test.go`

**Interfaces:**
- Produces: `engine.Step` (as in the roadmap), `engine.ErrTransient`, `engine.Engine{Dir string; Out io.Writer; Redact *Redactor; Sleep func(context.Context, time.Duration) error}`, `(*Engine).Run(ctx, []Step) error`, `engine.Result{Status, InputHash string; Time time.Time; Diagnostics string}`, `engine.Redactor` with `Add(string)` and `Redact(string) string`.

- [ ] **Step 1: Write the failing tests**

`internal/engine/engine_test.go`:

```go
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeStep struct {
	id, hash         string
	inspect          func() (bool, error)
	apply, verify    func() error
	inspects, applies int
}

func (f *fakeStep) ID() string        { return f.id }
func (f *fakeStep) InputHash() string { return f.hash }
func (f *fakeStep) Inspect(context.Context) (bool, error) {
	f.inspects++
	if f.inspect == nil {
		return false, nil
	}
	return f.inspect()
}
func (f *fakeStep) Apply(context.Context) error {
	f.applies++
	if f.apply == nil {
		return nil
	}
	return f.apply()
}
func (f *fakeStep) Verify(context.Context) error {
	if f.verify == nil {
		return nil
	}
	return f.verify()
}

func newEngine(t *testing.T) (*Engine, *bytes.Buffer, *[]time.Duration) {
	var out bytes.Buffer
	var slept []time.Duration
	e := &Engine{
		Dir:    filepath.Join(t.TempDir(), "results"),
		Out:    &out,
		Redact: &Redactor{},
		Sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	}
	return e, &out, &slept
}

func readResult(t *testing.T, e *Engine, id string) Result {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.Dir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunThenSkip(t *testing.T) {
	e, out, _ := newEngine(t)
	s := &fakeStep{id: "a.one", hash: "h1"}
	if err := e.Run(context.Background(), []Step{s}); err != nil {
		t.Fatal(err)
	}
	if s.applies != 1 || readResult(t, e, "a.one").Status != "ok" {
		t.Fatalf("applies %d, result %+v", s.applies, readResult(t, e, "a.one"))
	}
	out.Reset()
	if err := e.Run(context.Background(), []Step{s}); err != nil {
		t.Fatal(err)
	}
	if s.applies != 1 || s.inspects != 1 || !strings.Contains(out.String(), "skipped") {
		t.Errorf("second run: applies %d, inspects %d, out %q", s.applies, s.inspects, out)
	}
}

func TestChangedHashRerunsStep(t *testing.T) {
	e, _, _ := newEngine(t)
	s := &fakeStep{id: "a.one", hash: "h1"}
	e.Run(context.Background(), []Step{s})
	s.hash = "h2"
	if err := e.Run(context.Background(), []Step{s}); err != nil {
		t.Fatal(err)
	}
	if s.applies != 2 || readResult(t, e, "a.one").InputHash != "h2" {
		t.Errorf("applies %d, result %+v", s.applies, readResult(t, e, "a.one"))
	}
}

// An operator stopped the containers: the skip check's Verify fails, so the step runs again.
func TestFailedVerifyRerunsStep(t *testing.T) {
	e, _, _ := newEngine(t)
	s := &fakeStep{id: "a.deploy", hash: "h"}
	e.Run(context.Background(), []Step{s})
	stopped := true
	s.verify = func() error {
		if stopped {
			return errors.New("service web is not running")
		}
		return nil
	}
	s.apply = func() error { stopped = false; return nil }
	if err := e.Run(context.Background(), []Step{s}); err != nil {
		t.Fatal(err)
	}
	if s.applies != 2 {
		t.Errorf("applies = %d, want a redeploy", s.applies)
	}
}

func TestInspectDoneSkipsApply(t *testing.T) {
	e, _, _ := newEngine(t)
	s := &fakeStep{id: "a.one", hash: "h", inspect: func() (bool, error) { return true, nil }}
	if err := e.Run(context.Background(), []Step{s}); err != nil {
		t.Fatal(err)
	}
	if s.applies != 0 {
		t.Errorf("applies = %d", s.applies)
	}
}

// A transient Apply error is followed by a fresh Inspect: the work may have landed.
func TestTransientInspectsBeforeRetry(t *testing.T) {
	e, _, slept := newEngine(t)
	landed := false
	s := &fakeStep{id: "a.pull", hash: "h",
		inspect: func() (bool, error) { return landed, nil },
		apply: func() error {
			landed = true
			return fmt.Errorf("pull: %w", ErrTransient)
		},
	}
	if err := e.Run(context.Background(), []Step{s}); err != nil {
		t.Fatal(err)
	}
	if s.applies != 1 || s.inspects != 2 || len(*slept) != 1 {
		t.Errorf("applies %d, inspects %d, sleeps %v", s.applies, s.inspects, *slept)
	}
}

func TestTransientGivesUp(t *testing.T) {
	e, _, slept := newEngine(t)
	s := &fakeStep{id: "a.pull", hash: "h", apply: func() error { return ErrTransient }}
	if err := e.Run(context.Background(), []Step{s}); err == nil {
		t.Fatal("no error")
	}
	if s.applies != 3 || len(*slept) != 2 {
		t.Errorf("applies %d, sleeps %v", s.applies, *slept)
	}
}

func TestStopsAtFirstFailure(t *testing.T) {
	e, _, _ := newEngine(t)
	bad := &fakeStep{id: "a.one", hash: "h", apply: func() error { return errors.New("boom") }}
	next := &fakeStep{id: "a.two", hash: "h"}
	err := e.Run(context.Background(), []Step{bad, next})
	if err == nil || !strings.Contains(err.Error(), "a.one") {
		t.Fatalf("err = %v", err)
	}
	if bad.applies != 1 || next.inspects != 0 {
		t.Errorf("bad applies %d, next inspects %d", bad.applies, next.inspects)
	}
	if r := readResult(t, e, "a.one"); r.Status != "failed" || !strings.Contains(r.Diagnostics, "boom") {
		t.Errorf("result %+v", r)
	}
}

func TestFailureRedactsSecrets(t *testing.T) {
	e, out, _ := newEngine(t)
	secret := "s3cr3t-value-that-leaked"
	e.Redact.Add(secret)
	s := &fakeStep{id: "a.up", hash: "h", apply: func() error {
		return errors.New("docker compose up: exit status 1: DB_PASSWORD=" + secret)
	}}
	err := e.Run(context.Background(), []Step{s})
	if err == nil {
		t.Fatal("no error")
	}
	raw, _ := os.ReadFile(filepath.Join(e.Dir, "a.up.json"))
	for where, text := range map[string]string{"error": err.Error(), "output": out.String(), "result": string(raw)} {
		if strings.Contains(text, secret) {
			t.Errorf("secret in %s: %s", where, text)
		}
		if !strings.Contains(text, "[redacted]") {
			t.Errorf("%s lacks the redaction marker: %s", where, text)
		}
	}
}

func TestDuplicateStepIDs(t *testing.T) {
	e, _, _ := newEngine(t)
	a, b := &fakeStep{id: "a.one"}, &fakeStep{id: "a.one"}
	if err := e.Run(context.Background(), []Step{a, b}); err == nil || a.inspects != 0 {
		t.Fatalf("err %v, inspects %d", err, a.inspects)
	}
}

func TestRedactorIgnoresShortValues(t *testing.T) {
	r := &Redactor{}
	r.Add("abc")
	if got := r.Redact("abc"); got != "abc" {
		t.Errorf("short value redacted: %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/engine/`
Expected: FAIL, `undefined: Engine`.

- [ ] **Step 3: Implement**

`internal/engine/redact.go`:

```go
package engine

import (
	"strings"
	"sync"
)

// minSecret keeps short values from redacting ordinary words.
const minSecret = 8

// Redactor removes registered secrets from text before it is shown or stored.
type Redactor struct {
	mu      sync.Mutex
	secrets []string
}

func (r *Redactor) Add(s string) {
	if len(s) < minSecret {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secrets = append(r.secrets, s)
}

func (r *Redactor) Redact(s string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.secrets {
		s = strings.ReplaceAll(s, x, "[redacted]")
	}
	return s
}
```

`internal/engine/engine.go`:

```go
// Package engine runs installer steps: Inspect, Apply if needed, then Verify.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type Step interface {
	ID() string        // "<app>.<step>", unique; names the result file
	InputHash() string // changes force Apply even if a prior run succeeded
	Inspect(ctx context.Context) (done bool, err error)
	Apply(ctx context.Context) error
	Verify(ctx context.Context) error
}

// ErrTransient marks an Apply error worth a bounded retry (image pulls, 429, 503).
var ErrTransient = errors.New("transient")

// Result is one step's record on the workstation. It never holds a secret.
type Result struct {
	Status      string    `json:"status"` // "ok" or "failed"
	InputHash   string    `json:"input_hash"`
	Time        time.Time `json:"time"`
	Diagnostics string    `json:"diagnostics,omitempty"`
}

type Engine struct {
	Dir    string // one <step-id>.json per step
	Out    io.Writer
	Redact *Redactor
	Sleep  func(context.Context, time.Duration) error // nil sleeps for real
}

const attempts = 3

// Run runs steps in order and stops at the first failure. A step whose last result is ok
// with the same input hash, and whose Verify still passes, is skipped.
func (e *Engine) Run(ctx context.Context, steps []Step) error {
	seen := map[string]bool{}
	for _, s := range steps {
		if seen[s.ID()] {
			return fmt.Errorf("duplicate step %s", s.ID())
		}
		seen[s.ID()] = true
	}
	if err := os.MkdirAll(e.Dir, 0o700); err != nil {
		return err
	}
	for _, s := range steps {
		status, err := e.runStep(ctx, s)
		if err != nil {
			msg := e.Redact.Redact(err.Error())
			fmt.Fprintf(e.Out, "%-20s failed: %s\n", s.ID(), msg)
			res := Result{Status: "failed", InputHash: s.InputHash(), Time: time.Now().UTC(), Diagnostics: msg}
			return errors.Join(fmt.Errorf("%s: %s", s.ID(), msg), e.write(s.ID(), res))
		}
		if status == "ok" {
			if err := e.write(s.ID(), Result{Status: "ok", InputHash: s.InputHash(), Time: time.Now().UTC()}); err != nil {
				return err
			}
		}
		fmt.Fprintf(e.Out, "%-20s %s\n", s.ID(), status)
	}
	return nil
}

func (e *Engine) runStep(ctx context.Context, s Step) (string, error) {
	if prior, ok := e.read(s.ID()); ok && prior.Status == "ok" && prior.InputHash == s.InputHash() && s.Verify(ctx) == nil {
		return "skipped", nil
	}
	for attempt := 1; ; attempt++ {
		done, err := s.Inspect(ctx)
		if err != nil {
			return "", fmt.Errorf("inspect: %w", err)
		}
		if done {
			break
		}
		err = s.Apply(ctx)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrTransient) || attempt == attempts {
			return "", fmt.Errorf("apply: %w", err)
		}
		if err := e.sleep(ctx, time.Duration(attempt)*2*time.Second); err != nil {
			return "", err
		}
	}
	if err := s.Verify(ctx); err != nil {
		return "", fmt.Errorf("verify: %w", err)
	}
	return "ok", nil
}

func (e *Engine) sleep(ctx context.Context, d time.Duration) error {
	if e.Sleep != nil {
		return e.Sleep(ctx, d)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func (e *Engine) read(id string) (Result, bool) {
	b, err := os.ReadFile(filepath.Join(e.Dir, id+".json"))
	if err != nil {
		return Result{}, false
	}
	var r Result
	return r, json.Unmarshal(b, &r) == nil
}

func (e *Engine) write(id string, r Result) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(e.Dir, id+".json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(e.Dir, id+".json"))
}
```

- [ ] **Step 4: Run checks**

Run: `go test -race ./internal/engine/ && make ci`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/engine
git commit -m "feat: add step engine with result files and redaction"
```

---

### Task 6: Target lock and secrets step

**Files:**
- Create: `internal/dockerhost/app.go`, `internal/dockerhost/lock.go`, `internal/dockerhost/secrets.go`
- Test: `internal/dockerhost/lock_test.go`, `internal/dockerhost/secrets_test.go`

**Interfaces:**
- Consumes: `remote.Runner`, `remote.Quote`, `remote.ExitError`, `remote.Local`, `catalog.App`, `engine.Redactor`.
- Produces: `dockerhost.App{Name, Root string; Runner remote.Runner; Catalog catalog.App; ReleaseSet string; Redact *engine.Redactor}`, `dockerhost.Acquire(ctx, r remote.Runner, root, holder string) (release func(context.Context) error, err error)`, `dockerhost.LockedError{Root, Holder string; Started time.Time}`, unexported `secretsStep`, `hashOf(...string) string`.

- [ ] **Step 1: Write the failing tests**

`internal/dockerhost/lock_test.go`:

```go
package dockerhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

func TestAcquireReportsHolderAndKeepsLock(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "it's root")
	release, err := Acquire(ctx, remote.Local{}, root, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	holder := filepath.Join(root, ".lock", "holder")
	before, _ := os.ReadFile(holder)

	_, err = Acquire(ctx, remote.Local{}, root, "run-b")
	var le *LockedError
	if !errors.As(err, &le) || le.Holder != "run-a" || time.Since(le.Started) > time.Minute {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(le.Error(), "run-a") || !strings.Contains(le.Error(), "by hand") {
		t.Errorf("message = %q", le.Error())
	}
	if after, _ := os.ReadFile(holder); string(after) != string(before) {
		t.Errorf("holder changed: %s", after)
	}

	if err := release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".lock")); !os.IsNotExist(err) {
		t.Fatalf("lock still present: %v", err)
	}
	release2, err := Acquire(ctx, remote.Local{}, root, "run-b")
	if err != nil {
		t.Fatal(err)
	}
	release2(ctx)
}

func TestReleaseLeavesSomeoneElsesLock(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	release, err := Acquire(ctx, remote.Local{}, root, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	holder := filepath.Join(root, ".lock", "holder")
	os.WriteFile(holder, []byte(`{"holder":"run-z"}`), 0o600)
	if err := release(ctx); err == nil {
		t.Fatal("released a lock it does not hold")
	}
	if b, _ := os.ReadFile(holder); string(b) != `{"holder":"run-z"}` {
		t.Errorf("holder = %s", b)
	}
}

func TestLockWithoutHolder(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".lock"), 0o700)
	_, err := Acquire(context.Background(), remote.Local{}, root, "run-a")
	var le *LockedError
	if !errors.As(err, &le) || le.Holder != "" || !strings.Contains(le.Error(), "unknown holder") {
		t.Fatalf("err = %v", err)
	}
}

func TestAcquireUnwritableRoot(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	_, err := Acquire(context.Background(), remote.Local{}, filepath.Join(file, "root"), "run-a")
	var le *LockedError
	if err == nil || errors.As(err, &le) {
		t.Fatalf("err = %v, want a plain failure", err)
	}
}
```

`internal/dockerhost/secrets_test.go`:

```go
package dockerhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
)

func localApp(t *testing.T, secrets ...string) App {
	return App{
		Name:    "hello",
		Root:    filepath.Join(t.TempDir(), "it's root"),
		Runner:  remote.Local{},
		Catalog: catalog.App{Manifest: catalog.Manifest{Name: "hello", Secrets: secrets}},
		Redact:  &engine.Redactor{},
	}
}

func TestSecretsCreatedOnceAndPrivate(t *testing.T) {
	ctx := context.Background()
	a := localApp(t, "db_password", "token")
	s := secretsStep{a}
	if done, err := s.Inspect(ctx); err != nil || done {
		t.Fatalf("Inspect before = %v, %v", done, err)
	}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(a.Root, "hello", "secrets", "token")
	first, _ := os.ReadFile(f)
	if len(first) != 43 {
		t.Fatalf("secret is %d bytes, want 43 (32 bytes base64url)", len(first))
	}
	if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(f)); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", fi.Mode().Perm())
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if got := a.Redact.Redact("x" + string(first)); got != "x[redacted]" {
		t.Errorf("secret not registered for redaction: %q", got)
	}
	if done, err := s.Inspect(ctx); err != nil || !done {
		t.Fatalf("Inspect after = %v, %v", done, err)
	}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(f); string(again) != string(first) {
		t.Error("secret overwritten")
	}
}

func TestSecretsVerifyRejectsOpenMode(t *testing.T) {
	ctx := context.Background()
	a := localApp(t, "token")
	s := secretsStep{a}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	os.Chmod(filepath.Join(a.Root, "hello", "secrets", "token"), 0o644)
	if err := s.Verify(ctx); err == nil {
		t.Fatal("Verify accepted mode 644")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/dockerhost/`
Expected: FAIL, `undefined: Acquire`.

- [ ] **Step 3: Implement**

`internal/dockerhost/app.go`:

```go
// Package dockerhost installs apps on a Docker host over a remote.Runner.
package dockerhost

import (
	"crypto/sha256"
	"encoding/hex"
	"path"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// App is one catalog app placed on one Docker host.
type App struct {
	Name       string
	Root       string // the target's installer root
	Runner     remote.Runner
	Catalog    catalog.App
	ReleaseSet string
	Redact     *engine.Redactor
}

func (a App) dir() string { return path.Join(a.Root, a.Name) }

func hashOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
```

`internal/dockerhost/lock.go`:

```go
package dockerhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// LockedError is a target held by another run. The lock is never broken automatically.
type LockedError struct {
	Root    string
	Holder  string
	Started time.Time
}

func (e *LockedError) Error() string {
	lock := path.Join(e.Root, ".lock")
	if e.Holder == "" {
		return fmt.Sprintf("target is locked by an unknown holder; remove %s by hand once you are sure no run is active", lock)
	}
	return fmt.Sprintf("target is locked by %s since %s (%s ago); remove %s by hand once you are sure that run is gone",
		e.Holder, e.Started.Format(time.RFC3339), time.Since(e.Started).Round(time.Second), lock)
}

type holderRecord struct {
	Holder  string    `json:"holder"`
	Run     string    `json:"run"`
	Started time.Time `json:"started"`
}

const lockedExit = 75

// Acquire takes <root>/.lock for one run. release removes it only if this run still holds it.
func Acquire(ctx context.Context, r remote.Runner, root, holder string) (release func(context.Context) error, err error) {
	run := make([]byte, 16)
	rand.Read(run)
	body, err := json.Marshal(holderRecord{Holder: holder, Run: hex.EncodeToString(run), Started: time.Now().UTC()})
	if err != nil {
		return nil, err
	}
	lock := remote.Quote(path.Join(root, ".lock"))
	// mkdir is the atomic test-and-set; the final mkdir only repeats to report its error.
	cmd := fmt.Sprintf("umask 077; mkdir -p %s && if mkdir %s 2>/dev/null; then cat > %s/holder; elif test -d %s; then cat %s/holder 2>/dev/null; exit %d; else mkdir %s; fi",
		remote.Quote(root), lock, lock, lock, lock, lockedExit, lock)
	out, err := r.Run(ctx, cmd, body)
	var ee *remote.ExitError
	if errors.As(err, &ee) && ee.Code == lockedExit {
		le := &LockedError{Root: root}
		var h holderRecord
		if json.Unmarshal(out, &h) == nil {
			le.Holder, le.Started = h.Holder, h.Started
		}
		return nil, le
	}
	if err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	return func(ctx context.Context) error {
		cmd := fmt.Sprintf("cmp -s - %s/holder && rm %s/holder && rmdir %s", lock, lock, lock)
		if _, err := r.Run(ctx, cmd, body); err != nil {
			return fmt.Errorf("release lock %s: not held by this run or not removable: %w", path.Join(root, ".lock"), err)
		}
		return nil
	}, nil
}
```

`internal/dockerhost/secrets.go`:

```go
package dockerhost

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"path"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

// secretsStep creates each declared secret once, on the target, and never overwrites it.
type secretsStep struct{ a App }

func (s secretsStep) ID() string        { return s.a.Name + ".secrets" }
func (s secretsStep) InputHash() string { return hashOf(s.a.Catalog.Secrets...) }

func (s secretsStep) file(name string) string { return path.Join(s.a.dir(), "secrets", name) }

func (s secretsStep) exists(ctx context.Context, name string) (bool, error) {
	out, err := s.a.Runner.Run(ctx, "if test -e "+remote.Quote(s.file(name))+"; then echo yes; fi", nil)
	return strings.TrimSpace(string(out)) == "yes", err
}

func (s secretsStep) Inspect(ctx context.Context) (bool, error) {
	for _, n := range s.a.Catalog.Secrets {
		if ok, err := s.exists(ctx, n); err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (s secretsStep) Apply(ctx context.Context) error {
	for _, n := range s.a.Catalog.Secrets {
		ok, err := s.exists(ctx, n)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		b := make([]byte, 32)
		rand.Read(b)
		v := base64.RawURLEncoding.EncodeToString(b)
		s.a.Redact.Add(v)
		f := s.file(n)
		// set -C: the write fails rather than replace a secret that appeared meanwhile.
		cmd := "umask 077; set -C; mkdir -p " + remote.Quote(path.Dir(f)) + " && cat > " + remote.Quote(f)
		if _, err := s.a.Runner.Run(ctx, cmd, []byte(v)); err != nil {
			return fmt.Errorf("write secret %s: %w", n, err)
		}
	}
	return nil
}

// Verify reads every secret back, which also registers it for redaction on re-runs.
func (s secretsStep) Verify(ctx context.Context) error {
	for _, n := range s.a.Catalog.Secrets {
		f := remote.Quote(s.file(n))
		out, err := s.a.Runner.Run(ctx, `test -n "$(find `+f+` -prune -type f -perm 600)" && cat `+f, nil)
		if err != nil {
			return fmt.Errorf("secret %s is missing or not mode 600: %w", n, err)
		}
		if len(out) == 0 {
			return fmt.Errorf("secret %s is empty", n)
		}
		s.a.Redact.Add(string(out))
	}
	return nil
}
```

- [ ] **Step 4: Run checks**

Run: `go test -race ./internal/dockerhost/ && make ci`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dockerhost
git commit -m "feat: add target lock and on-target secrets step"
```

---

### Task 7: Deploy and health steps

**Files:**
- Create: `internal/dockerhost/deploy.go`, `internal/dockerhost/health.go`
- Modify: `internal/dockerhost/app.go` (add `Steps`, `compose`, `ps`)
- Test: `internal/dockerhost/fake_test.go`, `internal/dockerhost/deploy_test.go`, `internal/dockerhost/health_test.go`

**Interfaces:**
- Consumes: `secretsStep` and `hashOf` (Task 6), `engine.Step`, `engine.ErrTransient`.
- Produces: `dockerhost.Steps(a App) []engine.Step` (secrets, deploy, health, in that order), constants `ManagedLabel = "ky.managed-by"`, `ManagedValue = "kyquickstart"`, `ReleaseSetLabel = "ky.release-set"`.

- [ ] **Step 1: Write the failing tests**

`internal/dockerhost/fake_test.go`:

```go
package dockerhost

import "context"

type call struct{ cmd, stdin string }

// fakeRunner records commands and answers through reply.
type fakeRunner struct {
	calls []call
	reply func(cmd string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, cmd string, stdin []byte) ([]byte, error) {
	f.calls = append(f.calls, call{cmd, string(stdin)})
	if f.reply == nil {
		return nil, nil
	}
	return f.reply(cmd)
}
```

`internal/dockerhost/deploy_test.go`:

```go
package dockerhost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"go.yaml.in/yaml/v3"
)

const (
	running = `{"Service":"web","State":"running","Health":""}`
	exited  = `{"Service":"web","State":"exited","Health":""}`
)

func fakeApp(r remote.Runner) App {
	return App{
		Name:       "hello",
		Root:       "/opt/kyq",
		Runner:     r,
		Catalog:    catalog.App{Manifest: catalog.Manifest{Name: "hello", Health: catalog.Health{TimeoutSeconds: 1}}, Compose: []byte("services:\n  web:\n    image: x\n"), Services: []string{"web"}},
		ReleaseSet: "rs-1",
		Redact:     &engine.Redactor{},
	}
}

func deployOf(a App) deployStep { return Steps(a)[1].(deployStep) }

func TestOverrideLabelsEveryService(t *testing.T) {
	var o struct {
		Services map[string]struct{ Labels map[string]string }
	}
	if err := yaml.Unmarshal(override([]string{"web", "db"}, "rs-1"), &o); err != nil {
		t.Fatal(err)
	}
	for _, svc := range []string{"web", "db"} {
		l := o.Services[svc].Labels
		if l[ManagedLabel] != ManagedValue || l[ReleaseSetLabel] != "rs-1" {
			t.Errorf("%s labels = %v", svc, l)
		}
	}
}

func TestDeployApplyOrder(t *testing.T) {
	f := &fakeRunner{}
	a := fakeApp(f)
	d := deployOf(a)
	if err := d.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"cat > '/opt/kyq/hello/compose.yaml'", "cat > '/opt/kyq/hello/compose.kyq.yaml'", "pull --quiet", "up -d --remove-orphans", "> '/opt/kyq/hello/.input-hash'"}
	if len(f.calls) != len(want) {
		t.Fatalf("calls = %v", f.calls)
	}
	for i, w := range want {
		if !strings.Contains(f.calls[i].cmd, w) {
			t.Errorf("call %d = %q, want %q", i, f.calls[i].cmd, w)
		}
	}
	if f.calls[0].stdin != string(a.Catalog.Compose) || !strings.Contains(f.calls[1].stdin, ManagedValue) {
		t.Errorf("stdin: %q / %q", f.calls[0].stdin, f.calls[1].stdin)
	}
	if !strings.Contains(f.calls[3].cmd, "-p 'kyq-hello'") {
		t.Errorf("project name missing: %q", f.calls[3].cmd)
	}
	if !strings.Contains(f.calls[4].cmd, d.hash) {
		t.Errorf("hash not written: %q", f.calls[4].cmd)
	}
}

func TestDeployPullFailureIsTransient(t *testing.T) {
	f := &fakeRunner{reply: func(cmd string) ([]byte, error) {
		if strings.Contains(cmd, "pull") {
			return nil, &remote.ExitError{Code: 1, Stderr: []byte("toomanyrequests")}
		}
		return nil, nil
	}}
	err := deployOf(fakeApp(f)).Apply(context.Background())
	if !errors.Is(err, engine.ErrTransient) {
		t.Fatalf("err = %v", err)
	}
	for _, c := range f.calls {
		if strings.Contains(c.cmd, " up ") {
			t.Fatal("up ran after a failed pull")
		}
	}
}

func deployReplies(hash, ps string) func(string) ([]byte, error) {
	return func(cmd string) ([]byte, error) {
		if strings.Contains(cmd, ".input-hash") {
			return []byte(hash), nil
		}
		return []byte(ps), nil
	}
}

func TestDeployInspectSeesStoppedService(t *testing.T) {
	f := &fakeRunner{}
	d := deployOf(fakeApp(f))
	ctx := context.Background()

	f.reply = deployReplies(d.hash, running)
	if done, err := d.Inspect(ctx); err != nil || !done {
		t.Fatalf("running: %v %v", done, err)
	}
	f.reply = deployReplies(d.hash, exited)
	if done, err := d.Inspect(ctx); err != nil || done {
		t.Fatalf("exited: %v %v", done, err)
	}
	if err := d.Verify(ctx); err == nil || !strings.Contains(err.Error(), "web is not running") {
		t.Fatalf("Verify = %v", err)
	}
	f.reply = deployReplies(d.hash, "")
	if done, _ := d.Inspect(ctx); done {
		t.Fatal("removed containers counted as deployed")
	}
	f.reply = deployReplies("other", running)
	if done, _ := d.Inspect(ctx); done {
		t.Fatal("stale hash counted as deployed")
	}
}
```

`internal/dockerhost/health_test.go`:

```go
package dockerhost

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Trimmed from real `docker compose ps --all --format json` output (Compose 5.6).
const realPS = `{"Command":"\"/whoami\"","ExitCode":0,"Health":"","Name":"kyq-probe-whoami-1","Project":"kyq-probe","Service":"whoami","State":"running","Status":"Up Less than a second"}
{"Command":"\"/db\"","ExitCode":0,"Health":"starting","Name":"kyq-probe-db-1","Project":"kyq-probe","Service":"db","State":"running","Status":"Up 1 second (health: starting)"}
`

func TestParsePS(t *testing.T) {
	cs, err := parsePS([]byte(realPS))
	if err != nil || len(cs) != 2 {
		t.Fatalf("%v %v", cs, err)
	}
	if got := unhealthy(cs, []string{"whoami", "db"}); got != "service db is starting" {
		t.Errorf("unhealthy = %q", got)
	}
	if got := notRunning(cs, []string{"whoami", "db"}); got != "" {
		t.Errorf("notRunning = %q", got)
	}
	if got := notRunning(nil, []string{"whoami"}); got != "service whoami is not running" {
		t.Errorf("notRunning(empty) = %q", got)
	}
	if _, err := parsePS([]byte("not json")); err == nil {
		t.Error("garbage parsed")
	}
}

func TestHealthApplyWaits(t *testing.T) {
	pollInterval = time.Millisecond
	n := 0
	f := &fakeRunner{reply: func(string) ([]byte, error) {
		n++
		if n < 3 {
			return []byte(`{"Service":"web","State":"running","Health":"starting"}`), nil
		}
		return []byte(`{"Service":"web","State":"running","Health":"healthy"}`), nil
	}}
	h := Steps(fakeApp(f))[2]
	if err := h.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHealthApplyTimesOut(t *testing.T) {
	pollInterval = 50 * time.Millisecond
	f := &fakeRunner{reply: func(string) ([]byte, error) {
		return []byte(`{"Service":"web","State":"running","Health":"unhealthy"}`), nil
	}}
	err := Steps(fakeApp(f))[2].Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not healthy after 1s: service web is unhealthy") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/dockerhost/`
Expected: FAIL, `undefined: Steps`.

- [ ] **Step 3: Implement**

Append to `internal/dockerhost/app.go` and add `"context"` and `"fmt"` to its imports:

```go
// Steps returns the app's install steps: secrets, deploy, health.
func Steps(a App) []engine.Step {
	ov := override(a.Catalog.Services, a.ReleaseSet)
	hash := hashOf(string(a.Catalog.Compose), string(ov))
	return []engine.Step{secretsStep{a}, deployStep{a: a, override: ov, hash: hash}, healthStep{a: a, hash: hash}}
}

// compose runs docker compose for this app's project from its directory.
func (a App) compose(args string) string {
	return "cd " + remote.Quote(a.dir()) + " && docker compose -p " + remote.Quote("kyq-"+a.Name) +
		" -f compose.yaml -f compose.kyq.yaml " + args
}

func (a App) ps(ctx context.Context) ([]container, error) {
	out, err := a.Runner.Run(ctx, a.compose("ps --all --format json"), nil)
	if err != nil {
		return nil, fmt.Errorf("docker compose ps: %w", err)
	}
	return parsePS(out)
}
```

`internal/dockerhost/deploy.go`:

```go
package dockerhost

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"go.yaml.in/yaml/v3"
)

const (
	ManagedLabel    = "ky.managed-by"
	ManagedValue    = "kyquickstart"
	ReleaseSetLabel = "ky.release-set"
)

// override is compose.kyq.yaml: the managed labels on every service.
func override(services []string, releaseSet string) []byte {
	labels := map[string]string{ManagedLabel: ManagedValue, ReleaseSetLabel: releaseSet}
	svcs := map[string]any{}
	for _, s := range services {
		svcs[s] = map[string]any{"labels": labels}
	}
	b, err := yaml.Marshal(map[string]any{"services": svcs})
	if err != nil {
		panic(err) // maps of strings always marshal
	}
	return b
}

type deployStep struct {
	a        App
	override []byte
	hash     string
}

func (d deployStep) ID() string        { return d.a.Name + ".deploy" }
func (d deployStep) InputHash() string { return d.hash }

// check returns why the deployment is not current, or "" if it is.
func (d deployStep) check(ctx context.Context) (string, error) {
	out, err := d.a.Runner.Run(ctx, "cat "+remote.Quote(d.a.dir()+"/.input-hash")+" 2>/dev/null || true", nil)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(out)) != d.hash {
		return "deployed files differ from this release", nil
	}
	cs, err := d.a.ps(ctx)
	if err != nil {
		return "", err
	}
	return notRunning(cs, d.a.Catalog.Services), nil
}

func (d deployStep) Inspect(ctx context.Context) (bool, error) {
	why, err := d.check(ctx)
	return why == "", err
}

func (d deployStep) Apply(ctx context.Context) error {
	dir := d.a.dir()
	for _, f := range []struct {
		name string
		body []byte
	}{{"compose.yaml", d.a.Catalog.Compose}, {"compose.kyq.yaml", d.override}} {
		cmd := "umask 077; mkdir -p " + remote.Quote(dir) + " && cat > " + remote.Quote(dir+"/"+f.name)
		if _, err := d.a.Runner.Run(ctx, cmd, f.body); err != nil {
			return fmt.Errorf("write %s: %w", f.name, err)
		}
	}
	if _, err := d.a.Runner.Run(ctx, d.a.compose("pull --quiet"), nil); err != nil {
		return fmt.Errorf("docker compose pull: %w: %w", engine.ErrTransient, err)
	}
	if _, err := d.a.Runner.Run(ctx, d.a.compose("up -d --remove-orphans"), nil); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}
	// Written last: a run that dies before here redeploys next time.
	_, err := d.a.Runner.Run(ctx, "umask 077; printf %s "+remote.Quote(d.hash)+" > "+remote.Quote(dir+"/.input-hash"), nil)
	return err
}

func (d deployStep) Verify(ctx context.Context) error {
	why, err := d.check(ctx)
	if err != nil {
		return err
	}
	if why != "" {
		return errors.New(why)
	}
	return nil
}
```

`internal/dockerhost/health.go`:

```go
package dockerhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var pollInterval = 2 * time.Second

type container struct {
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
}

// parsePS reads `docker compose ps --format json`: one object per line (Compose 2.21+).
func parsePS(out []byte) ([]container, error) {
	var cs []container
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var c container
		if err := json.Unmarshal(line, &c); err != nil {
			return nil, fmt.Errorf("docker compose ps output: %w", err)
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// notRunning names the first service without a running container, or "".
func notRunning(cs []container, services []string) string {
	up := map[string]bool{}
	for _, c := range cs {
		if c.State == "running" {
			up[c.Service] = true
		}
	}
	for _, s := range services {
		if !up[s] {
			return "service " + s + " is not running"
		}
	}
	return ""
}

// unhealthy names the first service not running or not yet healthy, or "".
// A container without a healthcheck counts as healthy once running.
func unhealthy(cs []container, services []string) string {
	if why := notRunning(cs, services); why != "" {
		return why
	}
	for _, c := range cs {
		if c.State == "running" && c.Health != "" && c.Health != "healthy" {
			return "service " + c.Service + " is " + c.Health
		}
	}
	return ""
}

type healthStep struct {
	a    App
	hash string
}

func (h healthStep) ID() string        { return h.a.Name + ".health" }
func (h healthStep) InputHash() string { return h.hash }

func (h healthStep) state(ctx context.Context) (string, error) {
	cs, err := h.a.ps(ctx)
	if err != nil {
		return "", err
	}
	return unhealthy(cs, h.a.Catalog.Services), nil
}

func (h healthStep) Inspect(ctx context.Context) (bool, error) {
	why, err := h.state(ctx)
	return why == "", err
}

// Apply waits for health; it changes nothing.
func (h healthStep) Apply(ctx context.Context) error {
	timeout := time.Duration(h.a.Catalog.Health.TimeoutSeconds) * time.Second
	deadline := time.Now().Add(timeout)
	for {
		why, err := h.state(ctx)
		if err != nil || why == "" {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not healthy after %s: %s", timeout, why)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (h healthStep) Verify(ctx context.Context) error {
	why, err := h.state(ctx)
	if err != nil {
		return err
	}
	if why != "" {
		return errors.New(why)
	}
	return nil
}
```

- [ ] **Step 4: Run checks**

Run: `go test -race ./internal/dockerhost/ && make ci`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dockerhost
git commit -m "feat: add deploy and health steps with managed labels"
```

---

### Task 8: Preflight

**Files:**
- Create: `internal/dockerhost/preflight.go`
- Test: `internal/dockerhost/preflight_test.go`

**Interfaces:**
- Consumes: `fakeRunner` (Task 7), `stack.Target`, `remote.Local`.
- Produces: `dockerhost.Preflight(ctx, r remote.Runner, t stack.Target) []Finding`, `dockerhost.Finding{Check string; OK bool; Detail string}`.

- [ ] **Step 1: Write the failing tests**

`internal/dockerhost/preflight_test.go`:

```go
package dockerhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

func versions(engine, compose string) *fakeRunner {
	return &fakeRunner{reply: func(cmd string) ([]byte, error) {
		switch {
		case strings.HasPrefix(cmd, "docker version"):
			return []byte(engine + "\n"), nil
		case strings.HasPrefix(cmd, "docker compose version"):
			return []byte(compose + "\n"), nil
		}
		return []byte("/opt\n"), nil
	}}
}

func TestPreflightVersions(t *testing.T) {
	cases := map[string]struct {
		engine, compose string
		ok              [2]bool
	}{
		"current":        {"29.8.2", "5.1.3", [2]bool{true, true}},
		"floors":         {"24.0.0", "v2.21.0", [2]bool{true, true}},
		"old engine":     {"23.0.6", "2.29.1", [2]bool{false, true}},
		"old compose":    {"27.1.1", "2.20.3", [2]bool{true, false}},
		"desktop suffix": {"27.1.1", "2.29.1-desktop.1", [2]bool{true, true}},
		"garbage":        {"", "nope", [2]bool{false, false}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := Preflight(context.Background(), versions(c.engine, c.compose), stack.Target{Root: "/opt/kyq"})
			if got[0].OK != c.ok[0] || got[1].OK != c.ok[1] {
				t.Errorf("findings = %+v", got)
			}
		})
	}
}

func TestPreflightDockerUnreachable(t *testing.T) {
	f := &fakeRunner{reply: func(cmd string) ([]byte, error) {
		return nil, &remote.ExitError{Code: 1, Stderr: []byte("permission denied while trying to connect to the Docker daemon socket")}
	}}
	got := Preflight(context.Background(), f, stack.Target{Root: "/opt/kyq"})
	if got[0].OK || !strings.Contains(got[0].Detail, "permission denied") {
		t.Errorf("finding = %+v", got[0])
	}
}

func rootFinding(t *testing.T, root string) Finding {
	t.Helper()
	for _, f := range Preflight(context.Background(), remote.Local{}, stack.Target{Root: root}) {
		if f.Check == "root" {
			return f
		}
	}
	t.Fatal("no root finding")
	return Finding{}
}

func TestPreflightRoot(t *testing.T) {
	dir := t.TempDir()
	if f := rootFinding(t, filepath.Join(dir, "a", "b")); !f.OK {
		t.Errorf("nested under writable dir: %+v", f)
	}
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o600)
	if f := rootFinding(t, filepath.Join(file, "b")); f.OK {
		t.Errorf("under a file: %+v", f)
	}
	if os.Geteuid() != 0 {
		ro := filepath.Join(dir, "ro")
		os.Mkdir(ro, 0o500)
		if f := rootFinding(t, filepath.Join(ro, "b")); f.OK {
			t.Errorf("under read-only dir: %+v", f)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/dockerhost/ -run Preflight`
Expected: FAIL, `undefined: Preflight`.

- [ ] **Step 3: Implement**

`internal/dockerhost/preflight.go`:

```go
package dockerhost

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// Finding is one read-only preflight check.
type Finding struct {
	Check  string
	OK     bool
	Detail string
}

// Preflight checks a Docker host and changes nothing.
func Preflight(ctx context.Context, r remote.Runner, t stack.Target) []Finding {
	return []Finding{
		versionCheck(ctx, r, "docker engine", "docker version --format '{{.Server.Version}}'", 24, 0),
		versionCheck(ctx, r, "docker compose", "docker compose version --short", 2, 21),
		rootCheck(ctx, r, t.Root),
	}
}

func versionCheck(ctx context.Context, r remote.Runner, check, cmd string, major, minor int) Finding {
	out, err := r.Run(ctx, cmd, nil)
	if err != nil {
		return Finding{check, false, err.Error()}
	}
	v := strings.TrimSpace(string(out))
	parts := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3)
	if len(parts) < 2 {
		return Finding{check, false, fmt.Sprintf("cannot read version %q", v)}
	}
	gotMajor, err1 := strconv.Atoi(parts[0])
	gotMinor, err2 := strconv.Atoi(parts[1])
	switch {
	case err1 != nil || err2 != nil:
		return Finding{check, false, fmt.Sprintf("cannot read version %q", v)}
	case gotMajor < major || gotMajor == major && gotMinor < minor:
		return Finding{check, false, fmt.Sprintf("%s is older than %d.%d", v, major, minor)}
	}
	return Finding{check, true, v}
}

// rootCheck passes when the nearest existing ancestor of root is a writable directory.
func rootCheck(ctx context.Context, r remote.Runner, root string) Finding {
	cmd := "d=" + remote.Quote(root) + `; while [ ! -e "$d" ]; do d=$(dirname "$d"); done; [ -d "$d" ] && [ -w "$d" ]`
	if _, err := r.Run(ctx, cmd, nil); err != nil {
		return Finding{"root", false, root + " cannot be created or written by this user"}
	}
	return Finding{"root", true, root}
}
```

- [ ] **Step 4: Run checks**

Run: `go test -race ./internal/dockerhost/ && make ci`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dockerhost
git commit -m "feat: add read-only Docker host preflight"
```

---

### Task 9: SSH runner with pinned host keys

**Files:**
- Create: `internal/remote/ssh.go`
- Test: `internal/remote/ssh_test.go`

**Interfaces:**
- Consumes: `remote.Quote`, `remote.ExitError`.
- Produces: `remote.SSHConfig{Name, Host string; Port int; User, KnownHosts, Trust string; Confirm func(name, fingerprint string) bool}`, `remote.Dial(ctx, SSHConfig) (*SSH, error)`, `(*SSH).Run`, `(*SSH).Close() error`.

- [ ] **Step 1: Add the dependency**

Run: `go get golang.org/x/crypto@v0.55.0`

- [ ] **Step 2: Write the failing tests**

`internal/remote/ssh_test.go`:

```go
package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

var addr = &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 22}

func hostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s.PublicKey()
}

func check(t *testing.T, c SSHConfig, key ssh.PublicKey) error {
	t.Helper()
	cb, err := hostKeyCallback(c)
	if err != nil {
		t.Fatal(err)
	}
	return cb("nas.example.lan:22", addr, key)
}

func TestUnknownKeyNeedsTrustOrConfirm(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	key := hostKey(t)
	fp := ssh.FingerprintSHA256(key)
	base := SSHConfig{Name: "nas", KnownHosts: kh}

	if err := check(t, base, key); err == nil || !strings.Contains(err.Error(), "unknown host key "+fp) {
		t.Fatalf("no trust: %v", err)
	}
	wrong := base
	wrong.Trust = ssh.FingerprintSHA256(hostKey(t))
	if err := check(t, wrong, key); err == nil {
		t.Fatal("accepted with a different trusted fingerprint")
	}
	no := base
	no.Confirm = func(string, string) bool { return false }
	if err := check(t, no, key); err == nil {
		t.Fatal("accepted after the operator said no")
	}
	if b, _ := os.ReadFile(kh); len(b) != 0 {
		t.Fatalf("refused key was pinned: %s", b)
	}

	trusted := base
	trusted.Trust = fp
	if err := check(t, trusted, key); err != nil {
		t.Fatal(err)
	}
	if err := check(t, base, key); err != nil {
		t.Fatalf("pinned key refused on the next run: %v", err)
	}
}

func TestConfirmPinsKey(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	key := hostKey(t)
	var asked string
	c := SSHConfig{Name: "nas", KnownHosts: kh, Confirm: func(name, fp string) bool { asked = name + " " + fp; return true }}
	if err := check(t, c, key); err != nil {
		t.Fatal(err)
	}
	if asked != "nas "+ssh.FingerprintSHA256(key) {
		t.Errorf("asked %q", asked)
	}
	if err := check(t, SSHConfig{Name: "nas", KnownHosts: kh}, key); err != nil {
		t.Fatal(err)
	}
}

func TestChangedHostKeyIsRefused(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	old, replaced := hostKey(t), hostKey(t)
	if err := check(t, SSHConfig{Name: "nas", KnownHosts: kh, Trust: ssh.FingerprintSHA256(old)}, old); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(kh)
	c := SSHConfig{
		Name:       "nas",
		KnownHosts: kh,
		Trust:      ssh.FingerprintSHA256(replaced),
		Confirm:    func(string, string) bool { return true },
	}
	err := check(t, c, replaced)
	if err == nil || !strings.Contains(err.Error(), "host key changed") {
		t.Fatalf("err = %v", err)
	}
	if after, _ := os.ReadFile(kh); string(after) != string(before) {
		t.Error("known_hosts changed")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/remote/`
Expected: FAIL, `undefined: SSHConfig`.

- [ ] **Step 4: Implement**

`internal/remote/ssh.go`:

```go
package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

type SSHConfig struct {
	Name       string // target name, used in messages and --trust-host-key
	Host       string
	Port       int
	User       string
	KnownHosts string                              // pinned host keys
	Trust      string                              // "SHA256:..." accepted for an unknown host
	Confirm    func(name, fingerprint string) bool // nil when not interactive
}

// SSH runs commands on one target over one connection, authenticating with the SSH agent.
type SSH struct {
	client *ssh.Client
	agent  net.Conn
}

const dialTimeout = 15 * time.Second

func Dial(ctx context.Context, c SSHConfig) (*SSH, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("SSH_AUTH_SOCK is not set: start an SSH agent and add your key")
	}
	hk, err := hostKeyCallback(c)
	if err != nil {
		return nil, err
	}
	ac, err := (&net.Dialer{}).DialContext(ctx, "unix", sock)
	if err != nil {
		return nil, fmt.Errorf("ssh agent: %w", err)
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		ac.Close()
		return nil, fmt.Errorf("%s: %w", c.Name, err)
	}
	conn.SetDeadline(time.Now().Add(dialTimeout))
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:            c.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeysCallback(agent.NewClient(ac).Signers)},
		HostKeyCallback: hk,
	})
	if err != nil {
		conn.Close()
		ac.Close()
		return nil, fmt.Errorf("%s: ssh: %w", c.Name, err)
	}
	conn.SetDeadline(time.Time{})
	return &SSH{client: ssh.NewClient(sc, chans, reqs), agent: ac}, nil
}

func (s *SSH) Run(ctx context.Context, cmd string, stdin []byte) ([]byte, error) {
	sess, err := s.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	var out, errb bytes.Buffer
	sess.Stdin, sess.Stdout, sess.Stderr = bytes.NewReader(stdin), &out, &errb
	done := make(chan error, 1)
	// The login shell may not be POSIX (fish, for one), so every command runs under sh.
	go func() { done <- sess.Run("sh -c " + Quote(cmd)) }()
	select {
	case <-ctx.Done():
		sess.Close()
		<-done
		return out.Bytes(), ctx.Err()
	case err = <-done:
	}
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		return out.Bytes(), &ExitError{Code: ee.ExitStatus(), Stderr: errb.Bytes()}
	}
	return out.Bytes(), err
}

func (s *SSH) Close() error {
	return errors.Join(s.client.Close(), s.agent.Close())
}

// hostKeyCallback accepts a pinned key, pins an unknown key only when trusted by flag or
// confirmed by the operator, and always refuses a changed key.
func hostKeyCallback(c SSHConfig) (ssh.HostKeyCallback, error) {
	f, err := os.OpenFile(c.KnownHosts, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	known, err := knownhosts.New(c.KnownHosts)
	if err != nil {
		return nil, err
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := known(hostname, remote, key)
		var ke *knownhosts.KeyError
		if !errors.As(err, &ke) {
			return err
		}
		fp := ssh.FingerprintSHA256(key)
		if len(ke.Want) > 0 {
			return fmt.Errorf("%s: host key changed: got %s, pinned %s in %s; a reinstall or an interception, so check with the host's owner before editing that file",
				c.Name, fp, ssh.FingerprintSHA256(ke.Want[0].Key), c.KnownHosts)
		}
		if fp != c.Trust && (c.Confirm == nil || !c.Confirm(c.Name, fp)) {
			return fmt.Errorf("%s: unknown host key %s; compare it with `ssh-keygen -lf` on the host, then confirm interactively or pass --trust-host-key %s=<fingerprint>",
				c.Name, fp, c.Name)
		}
		f, err := os.OpenFile(c.KnownHosts, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(f, knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key))
		return errors.Join(err, f.Close())
	}, nil
}
```

- [ ] **Step 5: Run checks**

Run: `go test -race ./internal/remote/ && make ci`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/remote
git commit -m "feat: add SSH runner with pinned host keys"
```

---

### Task 10: CLI and binary

**Files:**
- Create: `internal/cli/cli.go`, `cmd/kyquickstart/main.go`
- Test: `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: every package above.
- Produces: `cli.Run(ctx, args []string, o Options) error`, `cli.Options{Catalog fs.FS; ReleaseSet string; In io.Reader; Out io.Writer}`, `cli.ErrUsage`, `cli.Usage`.

- [ ] **Step 1: Write the failing tests**

`internal/cli/cli_test.go`:

```go
package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const stackYAML = `version: 1
targets:
  - name: nas
    host: 192.0.2.1
    user: kyq
apps:
  - name: hello
    target: nas
`

func runIn(t *testing.T, stack string, args ...string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o600)
	var out bytes.Buffer
	o := Options{Catalog: os.DirFS("../catalog/testdata/apps"), ReleaseSet: "test", Out: &out}
	err := Run(context.Background(), append(args, "--state", dir), o)
	return out.String(), err
}

func TestBadStackFailsBeforeSSH(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	for name, stack := range map[string]string{
		"typo":        strings.Replace(stackYAML, "user:", "usr:", 1),
		"metachar":    strings.Replace(stackYAML, "name: hello", "name: hello;id", 1),
		"unknown app": strings.Replace(stackYAML, "name: hello", "name: nope", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := runIn(t, stack, "apply")
			if err == nil || strings.Contains(err.Error(), "SSH_AUTH_SOCK") {
				t.Fatalf("err = %v, want a stack error before any connection", err)
			}
		})
	}
}

func TestGoodStackReachesSSH(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	_, err := runIn(t, stackYAML, "preflight")
	if err == nil || !strings.Contains(err.Error(), "SSH_AUTH_SOCK") {
		t.Fatalf("err = %v", err)
	}
}

func TestTrustFlag(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	if _, err := runIn(t, stackYAML, "apply", "--trust-host-key", "nas=MD5:aa"); err == nil {
		t.Error("accepted a non-SHA256 fingerprint")
	}
	_, err := runIn(t, stackYAML, "apply", "--trust-host-key", "other=SHA256:abc")
	if err == nil || !strings.Contains(err.Error(), `unknown target "other"`) {
		t.Errorf("err = %v", err)
	}
}

func TestUsageAndVersion(t *testing.T) {
	var out bytes.Buffer
	o := Options{Out: &out, ReleaseSet: "rs-7"}
	if err := Run(context.Background(), nil, o); err != ErrUsage {
		t.Errorf("no args: %v", err)
	}
	if err := Run(context.Background(), []string{"destroy"}, o); err != ErrUsage {
		t.Errorf("unknown command: %v", err)
	}
	if err := Run(context.Background(), []string{"version"}, o); err != nil || out.String() != "rs-7\n" {
		t.Errorf("version: %q %v", out.String(), err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/`
Expected: FAIL, `undefined: Run`.

- [ ] **Step 3: Implement**

`internal/cli/cli.go`:

```go
// Package cli wires the kyquickstart commands.
package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/plan"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// ErrUsage means the arguments name no command; main prints Usage.
var ErrUsage = errors.New("usage")

const Usage = `usage: kyquickstart <command> [flags]

commands:
  preflight   check every target; changes nothing
  apply       preflight, then install the apps in stack.yaml
  version     print the release set

flags for preflight and apply:
  --state DIR                          state directory holding stack.yaml (default .)
  --trust-host-key NAME=SHA256:<fp>    accept this unknown host key for target NAME`

type Options struct {
	Catalog    fs.FS
	ReleaseSet string
	In         io.Reader // answers host key prompts; nil when not interactive
	Out        io.Writer
}

func Run(ctx context.Context, args []string, o Options) error {
	if len(args) == 0 {
		return ErrUsage
	}
	cmd := args[0]
	switch cmd {
	case "version":
		_, err := fmt.Fprintln(o.Out, o.ReleaseSet)
		return err
	case "preflight", "apply":
	default:
		return ErrUsage
	}
	fl := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fl.SetOutput(o.Out)
	state := fl.String("state", ".", "state directory holding stack.yaml")
	trust := trustFlag{}
	fl.Var(trust, "trust-host-key", "accept an unknown host key: <target>=SHA256:<fingerprint>")
	if err := fl.Parse(args[1:]); err != nil {
		return err
	}
	if fl.NArg() > 0 {
		return ErrUsage
	}
	s, err := load(*state, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
	if err := s.connect(ctx, trust); err != nil {
		return err
	}
	if err := s.preflight(ctx); err != nil || cmd == "preflight" {
		return err
	}
	return s.apply(ctx)
}

type trustFlag map[string]string

func (t trustFlag) String() string { return "" }

func (t trustFlag) Set(v string) error {
	name, fp, ok := strings.Cut(v, "=")
	if !ok || !stack.ValidName(name) || !strings.HasPrefix(fp, "SHA256:") {
		return errors.New("want <target>=SHA256:<fingerprint>")
	}
	t[name] = fp
	return nil
}

type session struct {
	o       Options
	state   string
	cat     catalog.Catalog
	order   []string
	placed  map[string]stack.Target // app -> target
	targets []stack.Target          // targets holding an app, by name
	conns   map[string]*remote.SSH  // target name -> connection
}

// load validates everything that needs no connection.
func load(state string, trust trustFlag, o Options) (*session, error) {
	st, err := stack.Load(filepath.Join(state, "stack.yaml"))
	if err != nil {
		return nil, err
	}
	cat, err := catalog.Load(o.Catalog)
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	for name := range trust {
		if _, ok := st.Target(name); !ok {
			return nil, fmt.Errorf("--trust-host-key names unknown target %q", name)
		}
	}
	s := &session{o: o, state: state, cat: cat, placed: map[string]stack.Target{}, conns: map[string]*remote.SSH{}}
	var names []string
	used := map[string]bool{}
	for _, a := range st.Apps {
		t, _ := st.Target(a.Target)
		s.placed[a.Name] = t
		names = append(names, a.Name)
		if !used[t.Name] {
			used[t.Name] = true
			s.targets = append(s.targets, t)
		}
	}
	sort.Slice(s.targets, func(i, j int) bool { return s.targets[i].Name < s.targets[j].Name })
	if s.order, err = plan.Order(names, cat); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *session) connect(ctx context.Context, trust trustFlag) error {
	confirm := s.confirmer()
	for _, t := range s.targets {
		c, err := remote.Dial(ctx, remote.SSHConfig{
			Name: t.Name, Host: t.Host, Port: t.Port, User: t.User,
			KnownHosts: filepath.Join(s.state, "known_hosts"),
			Trust:      trust[t.Name],
			Confirm:    confirm,
		})
		if err != nil {
			return err
		}
		s.conns[t.Name] = c
	}
	return nil
}

func (s *session) confirmer() func(name, fp string) bool {
	if s.o.In == nil {
		return nil
	}
	in := bufio.NewReader(s.o.In)
	return func(name, fp string) bool {
		fmt.Fprintf(s.o.Out, "Target %s presents host key %s.\nCompare it with `ssh-keygen -lf` on the host. Trust it? [y/N] ", name, fp)
		line, _ := in.ReadString('\n')
		a := strings.ToLower(strings.TrimSpace(line))
		return a == "y" || a == "yes"
	}
}

func (s *session) close() {
	for _, c := range s.conns {
		c.Close()
	}
}

func (s *session) preflight(ctx context.Context) error {
	failed := 0
	for _, t := range s.targets {
		for _, f := range dockerhost.Preflight(ctx, s.conns[t.Name], t) {
			mark := "ok  "
			if !f.OK {
				mark = "FAIL"
				failed++
			}
			fmt.Fprintf(s.o.Out, "%s  %-12s %-16s %s\n", mark, t.Name, f.Check, f.Detail)
		}
	}
	if failed > 0 {
		return fmt.Errorf("preflight: %d check(s) failed; nothing was changed", failed)
	}
	return nil
}

// apply locks every target before the first step and releases them at the end.
func (s *session) apply(ctx context.Context) (err error) {
	holder := holderName()
	var releases []func(context.Context) error
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		for _, release := range releases {
			err = errors.Join(err, release(rctx))
		}
	}()
	for _, t := range s.targets {
		release, err := dockerhost.Acquire(ctx, s.conns[t.Name], t.Root, holder)
		if err != nil {
			return fmt.Errorf("%s: %w", t.Name, err)
		}
		releases = append(releases, release)
	}
	redact := &engine.Redactor{}
	var steps []engine.Step
	for _, name := range s.order {
		t := s.placed[name]
		steps = append(steps, dockerhost.Steps(dockerhost.App{
			Name: name, Root: t.Root, Runner: s.conns[t.Name],
			Catalog: s.cat[name], ReleaseSet: s.o.ReleaseSet, Redact: redact,
		})...)
	}
	e := &engine.Engine{Dir: filepath.Join(s.state, "results"), Out: s.o.Out, Redact: redact}
	return e.Run(ctx, steps)
}

func holderName() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("kyquickstart %s@%s pid %d", os.Getenv("USER"), host, os.Getpid())
}
```

`cmd/kyquickstart/main.go`:

```go
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/cli"
)

// version is the release set, set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	o := cli.Options{Catalog: catalog.Embedded(), ReleaseSet: version, Out: os.Stdout}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		o.In = os.Stdin
	}
	err := cli.Run(ctx, os.Args[1:], o)
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, cli.ErrUsage):
		fmt.Fprintln(os.Stderr, cli.Usage)
		return 2
	default:
		fmt.Fprintln(os.Stderr, "kyquickstart:", err)
		return 1
	}
}
```

- [ ] **Step 4: Run checks**

Run: `go test -race ./internal/cli/ && make ci && make build && ./kyquickstart version`
Expected: PASS; last line prints `dev`.

- [ ] **Step 5: Commit**

```bash
git add internal/cli cmd
git commit -m "feat: add preflight and apply commands"
```

---

### Task 11: End-to-end test

**Files:**
- Create: `test/e2e/sshd/Dockerfile`, `test/e2e/e2e_test.go`
- Modify: `.github/workflows/ci.yml` (add the `e2e` job)

**Interfaces:**
- Consumes: `cli.Run`, `cli.Options`, `dockerhost.LockedError`, `remote.Dial`, `remote.SSHConfig`, the `hello` fixture.

The sshd container uses the host's Docker socket, so Compose bind-mount paths resolve on the host. The test root is mounted at the same path inside the container for that reason. The container writes files as root, so cleanup removes them through the container before Go's `TempDir` cleanup runs.

- [ ] **Step 1: Write the sshd image**

`test/e2e/sshd/Dockerfile`:

```dockerfile
FROM docker:29.4-cli@sha256:51e23845f5caff1e688a2fae003b0c69d635c9200ad544731db1593731df1d3a
RUN apk add --no-cache openssh-server
ENTRYPOINT ["/usr/sbin/sshd", "-D", "-e", "-h", "/keys/host_ed25519", "-o", "AuthorizedKeysFile=/keys/authorized_keys", "-o", "StrictModes=no", "-o", "PasswordAuthentication=no", "-o", "PermitRootLogin=prohibit-password"]
```

- [ ] **Step 2: Write the test**

`test/e2e/e2e_test.go`:

```go
//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// serveAgent runs an in-process SSH agent holding key and points SSH_AUTH_SOCK at it.
func serveAgent(t *testing.T, dir string, key ed25519.PrivateKey) {
	kr := agent.NewKeyring()
	if err := kr.Add(agent.AddedKey{PrivateKey: key}); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "agent.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go agent.ServeAgent(kr, c)
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)
}

func TestApply(t *testing.T) {
	ctx := context.Background()
	work := t.TempDir()
	root, keys, state := filepath.Join(work, "root"), filepath.Join(work, "keys"), filepath.Join(work, "state")
	for _, d := range []string{root, keys, state} {
		os.MkdirAll(d, 0o700)
	}

	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(hostPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(keys, "host_ed25519"), pem.EncodeToMemory(block), 0o600)
	hostSigner, _ := ssh.NewSignerFromKey(hostPriv)
	hostFP := ssh.FingerprintSHA256(hostSigner.PublicKey())

	_, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	clientSigner, _ := ssh.NewSignerFromKey(clientPriv)
	os.WriteFile(filepath.Join(keys, "authorized_keys"), ssh.MarshalAuthorizedKey(clientSigner.PublicKey()), 0o600)
	serveAgent(t, work, clientPriv)

	id := make([]byte, 4)
	rand.Read(id)
	name := "kyq-e2e-" + hex.EncodeToString(id)
	docker(t, "build", "-q", "-t", "kyq-e2e-sshd", "sshd")
	docker(t, "run", "-d", "--name", name, "-p", "127.0.0.1::22",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", keys+":/keys:ro", "-v", root+":"+root, "kyq-e2e-sshd")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	t.Cleanup(func() {
		exec.Command("docker", "compose", "-p", "kyq-hello", "down").Run()
		exec.Command("docker", "exec", name, "rm", "-rf", root).Run()
	})
	hostPort := docker(t, "port", name, "22")
	_, port, _ := net.SplitHostPort(hostPort)
	waitForSSH(t, port, hostFP)

	stackYAML := fmt.Sprintf("version: 1\ntargets:\n  - name: e2e\n    host: 127.0.0.1\n    port: %s\n    user: root\n    root: %s\napps:\n  - name: hello\n    target: e2e\n", port, root)
	os.WriteFile(filepath.Join(state, "stack.yaml"), []byte(stackYAML), 0o600)

	apply := func() (string, error) {
		var out bytes.Buffer
		o := cli.Options{Catalog: os.DirFS("../../internal/catalog/testdata/apps"), ReleaseSet: "e2e", Out: &out}
		err := cli.Run(ctx, []string{"apply", "--state", state, "--trust-host-key", "e2e=" + hostFP}, o)
		return out.String(), err
	}

	// First apply installs.
	out, err := apply()
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	secretFile := filepath.Join(root, "hello", "secrets", "token")
	if mode := docker(t, "exec", name, "stat", "-c", "%a", secretFile); mode != "600" {
		t.Errorf("secret mode %s", mode)
	}
	secret := docker(t, "exec", name, "cat", secretFile)
	if strings.Contains(out, secret) {
		t.Error("secret printed")
	}
	filepath.WalkDir(state, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(secret)) {
				t.Errorf("secret stored in %s", p)
			}
		}
		return nil
	})
	labelled := docker(t, "ps", "--filter", "label=com.docker.compose.project=kyq-hello",
		"--filter", "label=ky.managed-by=kyquickstart", "--filter", "label=ky.release-set=e2e", "--format", "{{.Names}}")
	if labelled == "" {
		t.Error("no container carries the managed labels")
	}

	// Second apply changes nothing.
	out, err = apply()
	if err != nil || status(out, "hello.secrets") != "skipped" || status(out, "hello.deploy") != "skipped" || status(out, "hello.health") != "skipped" {
		t.Fatalf("second apply: %v\n%s", err, out)
	}

	// Containers stopped by hand are redeployed. Health is skipped: its Verify passes after
	// the redeploy.
	docker(t, "compose", "-p", "kyq-hello", "stop")
	out, err = apply()
	if err != nil || status(out, "hello.deploy") != "ok" || status(out, "hello.secrets") != "skipped" {
		t.Fatalf("apply after stop: %v\n%s", err, out)
	}
	if running := docker(t, "ps", "--filter", "label=com.docker.compose.project=kyq-hello", "--filter", "status=running", "--format", "{{.Names}}"); running == "" {
		t.Error("not running after redeploy")
	}

	// A lock left by another run stops apply and survives.
	stale := `{"holder":"other run","run":"x","started":"2026-01-01T00:00:00Z"}`
	docker(t, "exec", name, "sh", "-c", "mkdir "+root+"/.lock && printf %s '"+stale+"' > "+root+"/.lock/holder")
	_, err = apply()
	var le *dockerhost.LockedError
	if !errors.As(err, &le) || le.Holder != "other run" {
		t.Fatalf("err = %v, want LockedError", err)
	}
	if got := docker(t, "exec", name, "cat", root+"/.lock/holder"); got != stale {
		t.Errorf("lock holder changed: %s", got)
	}
}

// status returns the engine's status word for step id in apply output.
func status(out, id string) string {
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == id {
			return strings.TrimSuffix(f[1], ":")
		}
	}
	return ""
}

func waitForSSH(t *testing.T, port, fp string) {
	t.Helper()
	kh := filepath.Join(t.TempDir(), "known_hosts")
	var p int
	fmt.Sscan(port, &p)
	var err error
	for range 50 {
		var c *remote.SSH
		c, err = remote.Dial(context.Background(), remote.SSHConfig{Name: "e2e", Host: "127.0.0.1", Port: p, User: "root", KnownHosts: kh, Trust: fp})
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("sshd not ready: %v", err)
}
```

- [ ] **Step 3: Run it**

Run: `make e2e`
Expected: PASS. Then run `docker ps -a --filter name=kyq-e2e` and `docker compose -p kyq-hello ps`; both must be empty.

After the hand stop, `hello.deploy` must report `ok`: its skip check runs Verify, which sees no running container. If it reports `skipped`, the deploy Verify is wrong; fix the code, not the assertion.

- [ ] **Step 4: Add the CI job**

Append to `.github/workflows/ci.yml` under `jobs:`:

```yaml
  e2e:
    # sshd in a container on the runner's Docker socket; deploys the hello fixture.
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26.x'
      - run: make e2e
```

- [ ] **Step 5: Run checks and commit**

Run: `make ci`
Expected: PASS (`go vet -tags e2e` covers the e2e file).

```bash
git add test/e2e .github/workflows/ci.yml
git commit -m "test: add end-to-end apply against an SSH Docker host"
```

---

### Task 12: Docs pass

**Files:**
- Modify: `AGENTS.md` (KyQuickStart section: Ownership, Work Guidance, Verification)
- Modify: `docs/superpowers/plans/2026-10-06-kyquickstart-roadmap.md` (Phase 1 `internal/cli/` row)

- [ ] **Step 1: Update `AGENTS.md`**

Under `## Ownership`, add:

```markdown
- Phase 1 installer core: `docs/superpowers/plans/2026-10-08-phase-1-installer-core.md`.
```

Under `## Local Contracts`, add:

```markdown
- Remote commands are POSIX `sh`, quoted with `remote.Quote`, and run under `sh -c` whatever the
  login shell. Unit tests run them for real through `remote.Local`.
- Host keys are pinned in `<state>/known_hosts`; a changed key is never accepted, by flag or prompt.
```

Set `## Verification` to:

```markdown
- `make ci`: tidy check, gofmt, vet (including the `e2e` tag), race tests.
- `make e2e`: needs Docker; runs `apply` three times and a stale-lock case against an sshd
  container on the local Docker socket.
```

- [ ] **Step 2: Update the roadmap row**

Replace the `internal/cli/` row's text with:

```markdown
| `internal/cli/` | Command wiring; `Options{Catalog fs.FS; ReleaseSet string; In io.Reader; Out io.Writer}` so tests inject a fixture catalog; `--state` and `--trust-host-key` are flags |
```

- [ ] **Step 3: Commit**

```bash
git add AGENTS.md docs/superpowers/plans/2026-10-06-kyquickstart-roadmap.md
git commit -m "docs: record phase 1 contracts and checks"
```
