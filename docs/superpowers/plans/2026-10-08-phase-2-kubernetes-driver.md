# Phase 2: Kubernetes Driver Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `kyquickstart apply` installs a Ky product or edge component on a Kubernetes cluster
through a kubeconfig, with the same Inspect → Apply → Verify steps, secrets-on-target rule and
target lock as the Docker driver.

**Architecture:** A new `internal/kube` package mirrors `internal/dockerhost`: secrets, deploy and
health steps, a `Lease` lock and a read-only preflight, over a `kubernetes.Interface` (fake in unit
tests, a kind cluster in e2e). Objects are typed `client-go` values rendered in Go from a small
`kubernetes:` section of the catalog manifest, as KyYard builds them; no Helm. The CLI picks a
driver per target.

**Tech Stack:** Go 1.26.6, `k8s.io/client-go`, `k8s.io/api`, `k8s.io/apimachinery` v0.37.1,
`k8s.io/utils/ptr`, kind v0.33.0 (run with `go run`, never installed).

**Spec:**
- `docs/superpowers/plans/2026-10-06-kyquickstart-roadmap.md`, Phase 2
- `docs/superpowers/specs/2026-10-06-installer-architecture-design.md` (Decisions, Target drivers,
  Edge placement, Failure handling)
- Phase 1 code on `master` (`internal/dockerhost`, `internal/engine`, `internal/cli`)

## Global Constraints

- Module `github.com/Busnes-app/kyquickstart`, `go 1.26.6`; client-go family pinned at `v0.37.1`.
- Kubernetes hosts Ky products and edge components only. A manifest with `category: third-party`
  may not carry a `kubernetes:` section.
- No Helm. Objects are typed values built in Go.
- Every installer-managed object and namespace carries `ky.managed-by=kyquickstart`; workloads also
  carry `ky.release-set=<release set>`. Label keys come from one package, `internal/managed`.
- The installer never adopts or overwrites an object or namespace it did not create (no managed
  label): it stops with an error naming it.
- App namespaces are `kyq-<app>`, labelled `pod-security.kubernetes.io/enforce=restricted`, with a
  default-deny ingress NetworkPolicy. Pods run non-root with a read-only root filesystem, all
  capabilities dropped, `RuntimeDefault` seccomp, no service-account token, no service links.
- Secrets are generated once, sent to the API server, read back for redaction, never regenerated,
  never written to the workstation. Each is 32 random bytes, base64url (43 characters).
- The lock is the Lease `kyquickstart-lock` in namespace `kyquickstart`. A held lock is reported
  with holder and age and never broken; release only by the run that created it.
- The kubeconfig is referenced by path in `stack.yaml`, never copied, and may not live inside the
  state directory (the state directory never holds a secret).
- Preflight is read-only: server version ≥ 1.30, every required permission granted, a default
  StorageClass when an app on the target declares a volume.
- `make ci` passes at the end of every task. Run `gofmt -w` on new files. After `go get`, run
  `git add go.mod go.sum` before `make ci` (its tidy check diffs against the index).

## Review Focus

- **An operator scales the Deployment to 0 or deletes the Service by hand.** The next `apply`
  restores them. Tests: Task 5 `TestDeployInspectSeesDrift`, Task 8 e2e.
- **A namespace or object with the installer's name exists but is not installer-managed.** Refused
  with an error naming it, never overwritten. Tests: Task 4 `TestNamespaceNotOwned`, Task 5
  `TestDeployRefusesForeignObject`.
- **The Secret exists with some keys.** Existing values are kept, only missing ones are added.
  Test: Task 4 `TestSecretsAddOnlyMissing`.
- **A crashed run leaves the Lease behind.** The next run stops, names the holder and age, and the
  Lease survives; a release by a run that does not hold it changes nothing. Tests: Task 6 lock
  tests, Task 8 e2e.
- **The kubeconfig sits inside the state directory, or a Docker-only app is placed on a cluster.**
  Rejected before any connection. Tests: Task 7 `TestPlacementAndKubeconfigChecks`.

## Out of scope (later phases)

- Pod exec for `apply-setup` (Phase 6; copy KyYard `exec.go` then).
- NetworkPolicy rules admitting the edge pods, and the edge components themselves (Phase 4).
- K8up backups (Phase 8). Multi-container pods, StatefulSets, ConfigMaps (when a product needs them).

---

## File Structure

| Path | Responsibility |
|---|---|
| `internal/managed/managed.go` | Managed and release-set label keys, shared by both drivers |
| `internal/catalog/catalog.go` | Adds `category`, optional Compose, the `kubernetes:` section and its checks |
| `internal/catalog/testdata/apps/hello/manifest.yaml` | Fixture gains `category: ky` and a `kubernetes:` section |
| `internal/stack/stack.go` | Kubernetes targets (`kubeconfig`, `context`) |
| `internal/kube/client.go` | `Client`, `Connect`, `New` |
| `internal/kube/names.go` | Names, labels, ownership and the generic get/upsert helpers |
| `internal/kube/render.go` | Pure rendering of Deployment, Service, PVCs, NetworkPolicy |
| `internal/kube/namespace.go` | Namespace create-or-verify with Pod Security labels |
| `internal/kube/secrets.go` | Secrets step |
| `internal/kube/deploy.go` | Deploy step |
| `internal/kube/health.go` | Health step (rollout wait copied from KyYard) |
| `internal/kube/app.go` | `App`, `Steps`, hashing |
| `internal/kube/lock.go` | Lease lock, `LockedError` |
| `internal/kube/preflight.go` | Version, access review, default StorageClass |
| `internal/cli/driver.go` | Per-target driver interface and the SSH and Kubernetes adapters |
| `internal/cli/cli.go` | Placement checks, kubeconfig rules, driver wiring |
| `test/e2e/kube_test.go` | kind cluster e2e (build tag `e2e`) |

---

### Task 1: Shared labels and the catalog's Kubernetes section

**Files:**
- Create: `internal/managed/managed.go`
- Modify: `internal/dockerhost/deploy.go`, `internal/dockerhost/deploy_test.go` (use `managed.*`)
- Modify: `internal/catalog/catalog.go`, `internal/catalog/catalog_test.go`
- Modify: `internal/catalog/testdata/apps/hello/manifest.yaml`

**Interfaces:**
- Produces: `managed.Label = "ky.managed-by"`, `managed.Value = "kyquickstart"`,
  `managed.ReleaseSetLabel = "ky.release-set"`.
- Produces: `catalog.Manifest.Category string`, `catalog.Manifest.Kubernetes *catalog.Kubernetes`,
  `catalog.Kubernetes{Image string; Args []string; Ports []int; ReadinessPath string; RunAs int64;
  Resources catalog.Resources; Volumes []catalog.Volume}`, `catalog.Resources{CPU, Memory string}`,
  `catalog.Volume{Name, Mount, Size string}`. `catalog.App.Compose` and `.Services` are nil when the
  app ships no `compose.yaml.tmpl`.

- [ ] **Step 1: Add `internal/managed`**

```go
// Package managed names the labels that mark installer-managed workloads. KyYard refuses
// generic edits to anything labelled Label=Value.
package managed

const (
	Label           = "ky.managed-by"
	Value           = "kyquickstart"
	ReleaseSetLabel = "ky.release-set"
)
```

In `internal/dockerhost/deploy.go`, delete the `ManagedLabel`, `ManagedValue` and `ReleaseSetLabel`
constants and use `managed.Label`, `managed.Value`, `managed.ReleaseSetLabel` instead (import
`github.com/Busnes-app/kyquickstart/internal/managed`). Do the same in `deploy_test.go`. Run
`go test ./internal/dockerhost/` and expect PASS before going on.

- [ ] **Step 2: Write the failing catalog tests**

Replace the `manifest` constant and extend `TestLoadRejects` in `internal/catalog/catalog_test.go`,
and add two tests:

```go
const manifest = "name: a\ncategory: ky\nimages:\n  web: " + digest + "\nhealth:\n  timeout_seconds: 30\n"

const kube = "kubernetes:\n  image: web\n  ports: [8080]\n  readiness_path: /\n  run_as: 65532\n  resources:\n    cpu: 50m\n    memory: 32Mi\n"
```

Add these cases to the `TestLoadRejects` table (each runs with `compose` unless noted):

```go
		"no category":         {strings.Replace(manifest, "category: ky\n", "", 1), compose, "category"},
		"third-party on k8s":  {strings.Replace(manifest, "category: ky", "category: third-party", 1) + kube, compose, "Docker hosts only"},
		"k8s unknown image":   {manifest + strings.Replace(kube, "image: web", "image: nope", 1), compose, "kubernetes.image"},
		"readiness no ports":  {manifest + strings.Replace(kube, "ports: [8080]", "ports: []", 1), compose, "readiness_path"},
		"root user":           {manifest + strings.Replace(kube, "run_as: 65532", "run_as: 0", 1), compose, "run_as"},
		"bad memory":          {manifest + strings.Replace(kube, "32Mi", "32MB", 1), compose, "memory"},
		"bad volume size":     {manifest + kube + "  volumes:\n    - name: data\n      mount: /data\n      size: lots\n", compose, "size"},
```

Add:

```go
func TestKubernetesOnlyApp(t *testing.T) {
	fsys := fstest.MapFS{"a/manifest.yaml": {Data: []byte(manifest + kube)}}
	cat, err := Load(fsys)
	if err != nil {
		t.Fatal(err)
	}
	a := cat["a"]
	if a.Compose != nil || a.Services != nil || a.Kubernetes == nil || a.Kubernetes.RunAs != 65532 {
		t.Fatalf("app = %+v", a)
	}
}

func TestNoDeploymentAtAll(t *testing.T) {
	_, err := Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(manifest)}})
	if err == nil || !strings.Contains(err.Error(), "neither compose.yaml.tmpl nor kubernetes") {
		t.Fatalf("err = %v", err)
	}
}

func TestThirdPartyNeedsCompose(t *testing.T) {
	m := strings.Replace(manifest, "category: ky", "category: third-party", 1)
	_, err := Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(m)}})
	if err == nil || !strings.Contains(err.Error(), "compose.yaml.tmpl") {
		t.Fatalf("err = %v", err)
	}
}
```

`TestLoadFixture` additionally asserts the fixture's Kubernetes section:

```go
	if app.Category != "ky" || app.Kubernetes == nil || app.Kubernetes.Ports[0] != 8080 || len(app.Kubernetes.Volumes) != 1 {
		t.Errorf("kubernetes = %+v", app.Kubernetes)
	}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/catalog/`
Expected: FAIL to compile (`app.Category undefined`, `a.Kubernetes undefined`).

- [ ] **Step 4: Implement**

In `internal/catalog/catalog.go`, extend `Manifest` and add the types:

```go
type Manifest struct {
	Name       string            `yaml:"name"`
	Category   string            `yaml:"category"` // ky, edge or third-party
	Images     map[string]string `yaml:"images"`
	Secrets    []string          `yaml:"secrets"`
	DependsOn  []string          `yaml:"depends_on"`
	Health     Health            `yaml:"health"`
	Kubernetes *Kubernetes       `yaml:"kubernetes"`
}

// Kubernetes is how a Ky product or edge component runs in a cluster: one container.
type Kubernetes struct {
	Image         string    `yaml:"image"` // a key of Images
	Args          []string  `yaml:"args"`
	Ports         []int     `yaml:"ports"`
	ReadinessPath string    `yaml:"readiness_path"`
	RunAs         int64     `yaml:"run_as"` // uid and gid; never 0
	Resources     Resources `yaml:"resources"`
	Volumes       []Volume  `yaml:"volumes"`
}

// Resources are both the requests and the limits.
type Resources struct {
	CPU    string `yaml:"cpu"`
	Memory string `yaml:"memory"`
}

type Volume struct {
	Name  string `yaml:"name"`
	Mount string `yaml:"mount"`
	Size  string `yaml:"size"`
}

var (
	cpuRE   = regexp.MustCompile(`^[1-9][0-9]{0,5}m?$`)
	sizeRE  = regexp.MustCompile(`^[1-9][0-9]{0,5}(Mi|Gi)$`)
	volRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	mountRE = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)
)
```

In `loadApp`, read the template only when it exists, and check placement-independent rules:

```go
	text, err := fs.ReadFile(fsys, path.Join(dir, "compose.yaml.tmpl"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if m.Kubernetes == nil {
			return App{}, fmt.Errorf("neither compose.yaml.tmpl nor kubernetes")
		}
		if m.Category == "third-party" {
			return App{}, fmt.Errorf("third-party apps need compose.yaml.tmpl")
		}
		return App{Manifest: m}, nil
	case err != nil:
		return App{}, err
	}
```

(the rest of `loadApp` — parse, render, `checkCompose` — is unchanged; add `"errors"` to imports).

In `Manifest.validate`, after the existing name check, add:

```go
	switch m.Category {
	case "ky", "edge", "third-party":
	default:
		return fmt.Errorf("category %q must be ky, edge or third-party", m.Category)
	}
```

and before `return nil`:

```go
	if m.Kubernetes != nil {
		if m.Category == "third-party" {
			return fmt.Errorf("kubernetes: third-party apps run on Docker hosts only")
		}
		if err := m.Kubernetes.validate(m.Images); err != nil {
			return fmt.Errorf("kubernetes.%w", err)
		}
	}
```

and add:

```go
func (k Kubernetes) validate(images map[string]string) error {
	if _, ok := images[k.Image]; !ok {
		return fmt.Errorf("image %q is not a key of images", k.Image)
	}
	seen := map[int]bool{}
	for _, p := range k.Ports {
		if p < 1 || p > 65535 || seen[p] {
			return fmt.Errorf("ports: %d must be unique and 1-65535", p)
		}
		seen[p] = true
	}
	if k.ReadinessPath != "" && (len(k.Ports) == 0 || !strings.HasPrefix(k.ReadinessPath, "/")) {
		return fmt.Errorf("readiness_path %q needs a port and must start with /", k.ReadinessPath)
	}
	if k.RunAs < 1 || k.RunAs > 2147483647 {
		return fmt.Errorf("run_as %d must be a non-root uid", k.RunAs)
	}
	if !cpuRE.MatchString(k.Resources.CPU) {
		return fmt.Errorf("resources.cpu %q must look like 100m or 2", k.Resources.CPU)
	}
	if !sizeRE.MatchString(k.Resources.Memory) {
		return fmt.Errorf("resources.memory %q must look like 64Mi or 1Gi", k.Resources.Memory)
	}
	mounts := map[string]bool{"/tmp": true, "/run/secrets": true}
	for _, v := range k.Volumes {
		switch {
		case !volRE.MatchString(v.Name):
			return fmt.Errorf("volumes: name %q must match %s", v.Name, volRE)
		case !mountRE.MatchString(v.Mount) || mounts[v.Mount]:
			return fmt.Errorf("volumes: mount %q must be a unique absolute path, not /tmp or /run/secrets", v.Mount)
		case !sizeRE.MatchString(v.Size):
			return fmt.Errorf("volumes: size %q must look like 1Gi", v.Size)
		}
		mounts[v.Mount] = true
	}
	return nil
}
```

(add `"strings"` to imports). `/tmp` and `/run/secrets` are reserved because the renderer mounts them.

Update the fixture `internal/catalog/testdata/apps/hello/manifest.yaml`:

```yaml
name: hello
category: ky
images:
  whoami: traefik/whoami:v1.11.0@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab
secrets: [token]
health:
  timeout_seconds: 120
kubernetes:
  image: whoami
  args: ["--port", "8080"]
  ports: [8080]
  readiness_path: /
  run_as: 65532
  resources:
    cpu: 50m
    memory: 32Mi
  volumes:
    - name: data
      mount: /data
      size: 16Mi
```

(`whoami` was verified to serve on 8080 as uid 65532 with a read-only root and no capabilities.)

- [ ] **Step 5: Run checks**

Run: `go test ./internal/... && make ci`
Expected: PASS. The Docker e2e is unaffected; it uses the same fixture's Compose template.

- [ ] **Step 6: Commit**

```bash
git add internal/managed internal/dockerhost internal/catalog
git commit -m "feat: add catalog categories and a Kubernetes deployment section"
```

---

### Task 2: Kubernetes targets in `stack.yaml`

**Files:**
- Modify: `internal/stack/stack.go`, `internal/stack/stack_test.go`

**Interfaces:**
- Produces: `stack.Target.Kubeconfig string`, `stack.Target.Context string`,
  `(stack.Target).Kubernetes() bool`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/stack/stack_test.go`:

```go
const kubeStack = `
version: 1
targets:
  - name: k1
    kubeconfig: /home/op/.kube/k1.yaml
    context: admin@k1
apps:
  - name: hello
    target: k1
`

func TestKubernetesTarget(t *testing.T) {
	s, err := Parse([]byte(kubeStack))
	if err != nil {
		t.Fatal(err)
	}
	k, _ := s.Target("k1")
	if !k.Kubernetes() || k.Port != 0 || k.Root != "" || k.Context != "admin@k1" {
		t.Fatalf("k1 = %+v", k)
	}
}

func TestKubernetesTargetRejects(t *testing.T) {
	cases := map[string]struct{ old, new, want string }{
		"ssh field":    {"context: admin@k1", "context: admin@k1\n    host: k1.lan", "ssh fields"},
		"bad context":  {"context: admin@k1", "context: \"a b\"", "context"},
		"newline path": {"kubeconfig: /home/op/.kube/k1.yaml", "kubeconfig: \"/a\\nb\"", "kubeconfig"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := strings.Replace(kubeStack, c.old, c.new, 1)
			_, err := Parse([]byte(in))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/stack/`
Expected: FAIL, `field kubeconfig not found` / `k.Kubernetes undefined`.

- [ ] **Step 3: Implement**

In `internal/stack/stack.go`, extend `Target`:

```go
type Target struct {
	Name       string `yaml:"name"`
	Host       string `yaml:"host"`
	Port       int    `yaml:"port"`
	User       string `yaml:"user"`
	Root       string `yaml:"root"`
	Kubeconfig string `yaml:"kubeconfig"` // set only for a Kubernetes target; a path, never contents
	Context    string `yaml:"context"`    // empty uses the kubeconfig's current context
}

// Kubernetes reports whether the target is a cluster reached through a kubeconfig.
func (t Target) Kubernetes() bool { return t.Kubeconfig != "" }

var contextRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:/-]{0,252}$`)
```

In `Parse`, apply SSH defaults only to SSH targets:

```go
	for i := range s.Targets {
		if s.Targets[i].Kubernetes() {
			continue
		}
		if s.Targets[i].Port == 0 {
			s.Targets[i].Port = 22
		}
		if s.Targets[i].Root == "" {
			s.Targets[i].Root = DefaultRoot
		}
	}
```

In `validate`, inside the targets loop, check Kubernetes targets before the SSH `switch` and
`continue` past it:

```go
		if t.Kubernetes() {
			switch {
			case !ValidName(t.Name):
				return fmt.Errorf("target %q: name must match %s", t.Name, nameRE)
			case seen[t.Name]:
				return fmt.Errorf("duplicate target %q", t.Name)
			case t.Host != "" || t.User != "" || t.Root != "" || t.Port != 0:
				return fmt.Errorf("target %q: a Kubernetes target takes no ssh fields (host, port, user, root)", t.Name)
			case strings.ContainsAny(t.Kubeconfig, "\x00\n\r"):
				return fmt.Errorf("target %q: kubeconfig path contains a control character", t.Name)
			case t.Context != "" && !contextRE.MatchString(t.Context):
				return fmt.Errorf("target %q: context %q must match %s", t.Name, t.Context, contextRE)
			}
			seen[t.Name] = true
			continue
		}
		if t.Context != "" {
			return fmt.Errorf("target %q: context needs kubeconfig", t.Name)
		}
```

- [ ] **Step 4: Run checks**

Run: `go test ./internal/stack/ && make ci`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/stack
git commit -m "feat: accept Kubernetes targets in stack.yaml"
```

---

### Task 3: Kubernetes client, names and object rendering

**Files:**
- Create: `internal/kube/client.go`, `internal/kube/names.go`, `internal/kube/render.go`
- Test: `internal/kube/render_test.go`

**Interfaces:**
- Consumes: `catalog.App`, `catalog.Kubernetes`, `managed.*`.
- Produces: `kube.Client` (field `cs kubernetes.Interface`), `kube.New(kubernetes.Interface) *Client`,
  `kube.Connect(kubeconfig, context string) (*Client, error)`, `namespaceOf(app string) string`,
  `workloadLabels(releaseSet string) map[string]string`, `owned(metav1.Object) bool`,
  `getOwned[T]`, `upsert[T]`, `render(app string, c catalog.App, releaseSet string) objects`,
  `objects{Deployment *appsv1.Deployment; Service *corev1.Service; Claims []*corev1.PersistentVolumeClaim; Policy *networkingv1.NetworkPolicy}`,
  `(objects).hash() string`, constants `appName = "app"`, `secretName = "kyq-secrets"`,
  `hashAnnotation = "kyquickstart/input-hash"`.

- [ ] **Step 1: Add the dependencies**

Run: `go get k8s.io/client-go@v0.37.1 k8s.io/api@v0.37.1 k8s.io/apimachinery@v0.37.1 k8s.io/utils`
(the last resolves to the version client-go v0.37.1 requires).

- [ ] **Step 2: Write the failing tests**

`internal/kube/render_test.go`:

```go
package kube

import (
	"os"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	corev1 "k8s.io/api/core/v1"
)

func fixture(t *testing.T) catalog.App {
	t.Helper()
	cat, err := catalog.Load(os.DirFS("../catalog/testdata/apps"))
	if err != nil {
		t.Fatal(err)
	}
	return cat["hello"]
}

func TestRenderIsRestricted(t *testing.T) {
	o := render("hello", fixture(t), "rs-1")
	d := o.Deployment
	if d.Namespace != "kyq-hello" || d.Name != appName || *d.Spec.Replicas != 1 {
		t.Fatalf("deployment meta = %s/%s replicas %d", d.Namespace, d.Name, *d.Spec.Replicas)
	}
	if d.Labels[managed.Label] != managed.Value || d.Labels[managed.ReleaseSetLabel] != "rs-1" {
		t.Errorf("labels = %v", d.Labels)
	}
	pod := d.Spec.Template.Spec
	if *pod.AutomountServiceAccountToken || *pod.EnableServiceLinks {
		t.Error("service-account token or service links enabled")
	}
	c := pod.Containers[0]
	sc := c.SecurityContext
	if *sc.AllowPrivilegeEscalation || !*sc.ReadOnlyRootFilesystem || !*sc.RunAsNonRoot || *sc.RunAsUser != 65532 ||
		len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("security context = %+v", sc)
	}
	if c.Image != fixture(t).Images["whoami"] || c.Args[1] != "8080" || c.ReadinessProbe.HTTPGet.Port.IntValue() != 8080 {
		t.Errorf("container = %+v", c)
	}
	if c.Resources.Limits.Memory().String() != "32Mi" || c.Resources.Requests.Cpu().String() != "50m" {
		t.Errorf("resources = %+v", c.Resources)
	}
	mounts := map[string]bool{}
	for _, m := range c.VolumeMounts {
		mounts[m.MountPath] = true
	}
	if !mounts["/tmp"] || !mounts["/run/secrets"] || !mounts["/data"] {
		t.Errorf("mounts = %v", mounts)
	}
	if len(o.Claims) != 1 || o.Claims[0].Spec.Resources.Requests.Storage().String() != "16Mi" {
		t.Errorf("claims = %+v", o.Claims)
	}
	if o.Service == nil || o.Service.Spec.Ports[0].Port != 8080 {
		t.Errorf("service = %+v", o.Service)
	}
	if len(o.Policy.Spec.PolicyTypes) != 1 || len(o.Policy.Spec.Ingress) != 0 {
		t.Errorf("policy = %+v", o.Policy.Spec)
	}
}

func TestRenderHashFollowsInputs(t *testing.T) {
	a, b := render("hello", fixture(t), "rs-1"), render("hello", fixture(t), "rs-1")
	if a.hash() != b.hash() {
		t.Fatal("hash not deterministic")
	}
	if a.hash() == render("hello", fixture(t), "rs-2").hash() {
		t.Fatal("release set change did not change the hash")
	}
}

func TestRenderWithoutPortsOrSecrets(t *testing.T) {
	app := fixture(t)
	k := *app.Kubernetes
	k.Ports, k.ReadinessPath, k.Volumes = nil, "", nil
	app.Kubernetes, app.Secrets = &k, nil
	o := render("hello", app, "rs-1")
	if o.Service != nil || len(o.Claims) != 0 || o.Deployment.Spec.Template.Spec.Containers[0].ReadinessProbe != nil {
		t.Fatalf("objects = %+v", o)
	}
	for _, v := range o.Deployment.Spec.Template.Spec.Volumes {
		if v.Secret != nil {
			t.Fatal("secret volume without secrets")
		}
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/kube/`
Expected: FAIL, `undefined: render`.

- [ ] **Step 4: Implement**

`internal/kube/client.go`:

```go
// Package kube installs Ky products and edge components on a Kubernetes cluster.
package kube

import (
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type Client struct{ cs kubernetes.Interface }

// New wraps a clientset; tests pass a fake one.
func New(cs kubernetes.Interface) *Client { return &Client{cs: cs} }

// Connect loads a kubeconfig the way kubectl does, exec plugins included. An empty context uses
// the file's current context.
func Connect(kubeconfig, context string) (*Client, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: context},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig %s: %w", kubeconfig, err)
	}
	cfg.Timeout = 30 * time.Second
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig %s: %w", kubeconfig, err)
	}
	return New(cs), nil
}
```

`internal/kube/names.go`:

```go
package kube

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	appName        = "app"
	secretName     = "kyq-secrets"
	policyName     = "default-deny-ingress"
	hashAnnotation = "kyquickstart/input-hash"
	podSecurity    = "pod-security.kubernetes.io/enforce"
	nameLabel      = "app.kubernetes.io/name"
)

func namespaceOf(app string) string { return "kyq-" + app }

func workloadLabels(releaseSet string) map[string]string {
	return map[string]string{managed.Label: managed.Value, managed.ReleaseSetLabel: releaseSet}
}

func podLabels(app, releaseSet string) map[string]string {
	l := workloadLabels(releaseSet)
	maps.Copy(l, selector(app))
	return l
}

func selector(app string) map[string]string { return map[string]string{nameLabel: app} }

func owned(o metav1.Object) bool { return o.GetLabels()[managed.Label] == managed.Value }

var errNotOwned = errors.New("not managed by kyquickstart")

type getter[T metav1.Object] interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (T, error)
}

type writer[T metav1.Object] interface {
	getter[T]
	Create(ctx context.Context, o T, opts metav1.CreateOptions) (T, error)
	Update(ctx context.Context, o T, opts metav1.UpdateOptions) (T, error)
}

// getOwned reads an object; found is false when it is absent. An object of that name the
// installer did not create is an error, never something to adopt.
func getOwned[T metav1.Object](ctx context.Context, api getter[T], kind, name string) (T, bool, error) {
	var zero T
	o, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, fmt.Errorf("%s %s: %w", kind, name, err)
	}
	if !owned(o) {
		return zero, false, fmt.Errorf("%s %s exists and is %w; remove or rename it by hand", kind, name, errNotOwned)
	}
	return o, true, nil
}

// upsert creates want, or replaces the installer's object of the same name with it. keep copies
// fields the API server owns from the existing object. A conflict means someone wrote in
// between: transient, so the engine inspects again.
func upsert[T metav1.Object](ctx context.Context, api writer[T], kind string, want T, keep func(have T)) error {
	have, found, err := getOwned(ctx, api, kind, want.GetName())
	if err != nil {
		return err
	}
	if found {
		want.SetResourceVersion(have.GetResourceVersion())
		if keep != nil {
			keep(have)
		}
		_, err = api.Update(ctx, want, metav1.UpdateOptions{})
	} else {
		_, err = api.Create(ctx, want, metav1.CreateOptions{})
	}
	switch {
	case apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err):
		return fmt.Errorf("%s %s: %w: %w", kind, want.GetName(), engine.ErrTransient, err)
	case err != nil:
		return fmt.Errorf("%s %s: %w", kind, want.GetName(), err)
	}
	return nil
}
```

`internal/kube/render.go`:

```go
package kube

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

// objects is everything the deploy step writes for one app, in one namespace.
type objects struct {
	Deployment *appsv1.Deployment
	Service    *corev1.Service // nil when the app listens on no port
	Claims     []*corev1.PersistentVolumeClaim
	Policy     *networkingv1.NetworkPolicy
}

func (o objects) hash() string {
	b, err := json.Marshal(o)
	if err != nil {
		panic(err) // typed API objects always marshal
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// render builds the app's objects. Catalog validation guarantees every quantity parses.
func render(app string, c catalog.App, releaseSet string) objects {
	k := c.Kubernetes
	ns := namespaceOf(app)
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: ns, Labels: workloadLabels(releaseSet)}
	}
	res := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(k.Resources.CPU),
		corev1.ResourceMemory: resource.MustParse(k.Resources.Memory),
	}
	container := corev1.Container{
		Name:      appName,
		Image:     c.Images[k.Image],
		Args:      k.Args,
		Resources: corev1.ResourceRequirements{Requests: res, Limits: res},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			RunAsNonRoot:             ptr.To(true),
			RunAsUser:                ptr.To(k.RunAs),
			RunAsGroup:               ptr.To(k.RunAs),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
	}
	volumes := []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	if len(c.Secrets) > 0 {
		volumes = append(volumes, corev1.Volume{Name: "secrets", VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: secretName, DefaultMode: ptr.To(int32(0o440))},
		}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "secrets", MountPath: "/run/secrets", ReadOnly: true})
	}
	o := objects{Policy: &networkingv1.NetworkPolicy{
		ObjectMeta: meta(policyName),
		// No ingress rules: nothing reaches the app until the edge is admitted (Phase 4).
		Spec: networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}},
	}}
	for _, v := range k.Volumes {
		claim := "vol-" + v.Name
		o.Claims = append(o.Claims, &corev1.PersistentVolumeClaim{
			ObjectMeta: meta(claim),
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(v.Size)}},
			},
		})
		volumes = append(volumes, corev1.Volume{Name: claim, VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim},
		}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: claim, MountPath: v.Mount})
	}
	if len(k.Ports) > 0 {
		svc := &corev1.Service{ObjectMeta: meta(appName), Spec: corev1.ServiceSpec{Selector: selector(app)}}
		for _, p := range k.Ports {
			port := int32(p)
			container.Ports = append(container.Ports, corev1.ContainerPort{ContainerPort: port})
			svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Name: fmt.Sprintf("p%d", p), Port: port, TargetPort: intstr.FromInt32(port)})
		}
		o.Service = svc
		if k.ReadinessPath != "" {
			container.ReadinessProbe = &corev1.Probe{
				ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: k.ReadinessPath, Port: intstr.FromInt32(int32(k.Ports[0]))}},
				PeriodSeconds: 5,
			}
		}
	}
	o.Deployment = &appsv1.Deployment{
		ObjectMeta: meta(appName),
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: selector(app)},
			// Recreate: a ReadWriteOnce claim cannot attach to two pods at once.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels(app, releaseSet)},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					EnableServiceLinks:           ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptr.To(true),
						FSGroup:        ptr.To(k.RunAs),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{container},
					Volumes:    volumes,
				},
			},
		},
	}
	return o
}
```

- [ ] **Step 5: Run checks**

Run: `go test ./internal/kube/ && git add go.mod go.sum && make ci`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/kube
git commit -m "feat: render restricted Kubernetes objects for an app"
```

---

### Task 4: Namespace and secrets step

**Files:**
- Create: `internal/kube/namespace.go`, `internal/kube/secrets.go`, `internal/kube/app.go`
- Test: `internal/kube/secrets_test.go`

**Interfaces:**
- Consumes: Task 3 names and helpers, `engine.Redactor`.
- Produces: `kube.App{Name string; Client *Client; Catalog catalog.App; ReleaseSet string; Redact *engine.Redactor}`,
  `(App).ensureNamespace(ctx) error`, `(*Client).ensureNamespace(ctx, name string, labels map[string]string) error`,
  `secretsStep{a App}`, `hashOf(parts ...string) string`.

- [ ] **Step 1: Write the failing tests**

`internal/kube/secrets_test.go`:

```go
package kube

import (
	"context"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func testApp(t *testing.T, secrets ...string) (App, *fake.Clientset) {
	t.Helper()
	cs := fake.NewClientset()
	c := fixture(t)
	c.Secrets = secrets
	return App{Name: "hello", Client: New(cs), Catalog: c, ReleaseSet: "rs-1", Redact: &engine.Redactor{}}, cs
}

func TestSecretsCreatedOnce(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token", "db_password")
	s := secretsStep{a}
	if done, err := s.Inspect(ctx); err != nil || done {
		t.Fatalf("Inspect before = %v %v", done, err)
	}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	ns, err := cs.CoreV1().Namespaces().Get(ctx, "kyq-hello", metav1.GetOptions{})
	if err != nil || ns.Labels[podSecurity] != "restricted" || !owned(ns) {
		t.Fatalf("namespace = %+v, %v", ns, err)
	}
	sec, _ := cs.CoreV1().Secrets("kyq-hello").Get(ctx, secretName, metav1.GetOptions{})
	first := string(sec.Data["token"])
	if len(first) != 43 || len(sec.Data["db_password"]) != 43 || !owned(sec) {
		t.Fatalf("secret = %+v", sec)
	}
	if err := s.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Redact.Redact("x"+first) != "x[redacted]" {
		t.Error("secret not registered for redaction")
	}
	if done, _ := s.Inspect(ctx); !done {
		t.Fatal("Inspect after Apply is not done")
	}
}

func TestSecretsAddOnlyMissing(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token", "db_password")
	if err := a.ensureNamespace(ctx); err != nil {
		t.Fatal(err)
	}
	keep := strings.Repeat("k", 43)
	cs.CoreV1().Secrets("kyq-hello").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "kyq-hello", Labels: workloadLabels("rs-0")},
		Data:       map[string][]byte{"token": []byte(keep)},
	}, metav1.CreateOptions{})
	s := secretsStep{a}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	sec, _ := cs.CoreV1().Secrets("kyq-hello").Get(ctx, secretName, metav1.GetOptions{})
	if string(sec.Data["token"]) != keep || len(sec.Data["db_password"]) != 43 {
		t.Fatalf("data = %q", sec.Data)
	}
}

func TestSecretsVerifyRejectsWrongLength(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token")
	s := secretsStep{a}
	if err := s.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	sec, _ := cs.CoreV1().Secrets("kyq-hello").Get(ctx, secretName, metav1.GetOptions{})
	sec.Data["token"] = []byte("short")
	cs.CoreV1().Secrets("kyq-hello").Update(ctx, sec, metav1.UpdateOptions{})
	err := s.Verify(ctx)
	if err == nil || strings.Contains(err.Error(), "short") {
		t.Fatalf("err = %v (must fail without printing the value)", err)
	}
}

func TestNamespaceNotOwned(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token")
	cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kyq-hello"}}, metav1.CreateOptions{})
	err := secretsStep{a}.Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "not managed by kyquickstart") {
		t.Fatalf("err = %v", err)
	}
	if _, err := cs.CoreV1().Secrets("kyq-hello").Get(ctx, secretName, metav1.GetOptions{}); err == nil {
		t.Fatal("wrote a secret into a namespace it does not own")
	}
}

func TestNoSecretsIsDone(t *testing.T) {
	a, _ := testApp(t)
	if done, err := (secretsStep{a}).Inspect(context.Background()); err != nil || !done {
		t.Fatalf("Inspect = %v %v", done, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/kube/`
Expected: FAIL, `undefined: App` / `undefined: secretsStep`.

- [ ] **Step 3: Implement**

`internal/kube/app.go`:

```go
package kube

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
)

// App is one catalog app placed on one cluster.
type App struct {
	Name       string
	Client     *Client
	Catalog    catalog.App
	ReleaseSet string
	Redact     *engine.Redactor
}

func hashOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
```

`internal/kube/namespace.go`:

```go
package kube

import (
	"context"
	"fmt"
	"maps"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (a App) ensureNamespace(ctx context.Context) error {
	l := workloadLabels(a.ReleaseSet)
	l[podSecurity] = "restricted"
	return a.Client.ensureNamespace(ctx, namespaceOf(a.Name), l)
}

// ensureNamespace creates name with labels, or brings the installer's existing namespace up to
// them. A namespace the installer did not create is refused.
func (c *Client) ensureNamespace(ctx context.Context, name string, labels map[string]string) error {
	api := c.cs.CoreV1().Namespaces()
	have, err := api.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		_, err = api.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("namespace %s: %w", name, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("namespace %s: %w", name, err)
	case !owned(have):
		return fmt.Errorf("namespace %s exists and is not managed by kyquickstart; remove or rename it by hand", name)
	}
	if have.Labels == nil {
		have.Labels = map[string]string{}
	}
	before := maps.Clone(have.Labels)
	maps.Copy(have.Labels, labels)
	if maps.Equal(before, have.Labels) {
		return nil
	}
	if _, err := api.Update(ctx, have, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("namespace %s: %w", name, err)
	}
	return nil
}
```

`internal/kube/secrets.go`:

```go
package kube

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// secretsStep keeps one Secret per app holding every declared secret. Values are created once
// and never replaced; a key added to the catalog later is generated on its own.
type secretsStep struct{ a App }

func (s secretsStep) ID() string        { return s.a.Name + ".secrets" }
func (s secretsStep) InputHash() string { return hashOf(s.a.Catalog.Secrets...) }

func (s secretsStep) read(ctx context.Context) (*corev1.Secret, bool, error) {
	return getOwned(ctx, s.a.Client.cs.CoreV1().Secrets(namespaceOf(s.a.Name)), "secret", secretName)
}

func (s secretsStep) missing(sec *corev1.Secret) []string {
	var out []string
	for _, n := range s.a.Catalog.Secrets {
		if sec == nil || len(sec.Data[n]) == 0 {
			out = append(out, n)
		}
	}
	return out
}

func (s secretsStep) Inspect(ctx context.Context) (bool, error) {
	sec, _, err := s.read(ctx)
	if err != nil {
		return false, err
	}
	return len(s.missing(sec)) == 0, nil
}

func (s secretsStep) Apply(ctx context.Context) error {
	if err := s.a.ensureNamespace(ctx); err != nil {
		return err
	}
	sec, found, err := s.read(ctx)
	if err != nil {
		return err
	}
	if !found {
		sec = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespaceOf(s.a.Name)},
			Type:       corev1.SecretTypeOpaque,
		}
	}
	sec.Labels = workloadLabels(s.a.ReleaseSet)
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	for _, n := range s.missing(sec) {
		b := make([]byte, 32)
		rand.Read(b)
		v := base64.RawURLEncoding.EncodeToString(b)
		s.a.Redact.Add(v)
		sec.Data[n] = []byte(v)
	}
	// Written against the resourceVersion just read: a concurrent writer is a conflict, retried
	// by the engine after a fresh Inspect, never silently overwritten.
	api := s.a.Client.cs.CoreV1().Secrets(sec.Namespace)
	if found {
		_, err = api.Update(ctx, sec, metav1.UpdateOptions{})
	} else {
		_, err = api.Create(ctx, sec, metav1.CreateOptions{})
	}
	switch {
	case apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err):
		return fmt.Errorf("secret %s: %w: %w", secretName, engine.ErrTransient, err)
	case err != nil:
		return fmt.Errorf("secret %s: %w", secretName, err)
	}
	return nil
}

// Verify reads every secret back, which also registers it for redaction on re-runs.
func (s secretsStep) Verify(ctx context.Context) error {
	sec, _, err := s.read(ctx)
	if err != nil {
		return err
	}
	if m := s.missing(sec); len(m) > 0 {
		return fmt.Errorf("secrets missing: %v", m)
	}
	for _, n := range s.a.Catalog.Secrets {
		if len(sec.Data[n]) != 43 {
			return fmt.Errorf("secret %s is %d bytes, want 43", n, len(sec.Data[n]))
		}
		s.a.Redact.Add(string(sec.Data[n]))
	}
	return nil
}
```

- [ ] **Step 4: Run checks**

Run: `go test -race ./internal/kube/ && make ci`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kube
git commit -m "feat: create app namespaces and secrets on a cluster"
```

---

### Task 5: Deploy and health steps

**Files:**
- Create: `internal/kube/deploy.go`, `internal/kube/health.go`
- Modify: `internal/kube/app.go` (add `Steps`)
- Test: `internal/kube/deploy_test.go`, `internal/kube/health_test.go`

**Interfaces:**
- Consumes: Tasks 3-4.
- Produces: `kube.Steps(a App) []engine.Step` (secrets, deploy, health), `deployStep`, `healthStep`,
  package var `pollInterval`.

- [ ] **Step 1: Write the failing tests**

`internal/kube/deploy_test.go`:

```go
package kube

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func deployOf(a App) deployStep { return Steps(a)[1].(deployStep) }

func TestDeployCreatesThenIsDone(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token")
	d := deployOf(a)
	if done, err := d.Inspect(ctx); err != nil || done {
		t.Fatalf("Inspect before = %v %v", done, err)
	}
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	dep, err := cs.AppsV1().Deployments("kyq-hello").Get(ctx, appName, metav1.GetOptions{})
	if err != nil || dep.Annotations[hashAnnotation] != d.hash {
		t.Fatalf("deployment = %+v, %v", dep, err)
	}
	for kind, err := range map[string]error{
		"service": get(cs.CoreV1().Services("kyq-hello").Get(ctx, appName, metav1.GetOptions{})),
		"claim":   get(cs.CoreV1().PersistentVolumeClaims("kyq-hello").Get(ctx, "vol-data", metav1.GetOptions{})),
		"policy":  get(cs.NetworkingV1().NetworkPolicies("kyq-hello").Get(ctx, policyName, metav1.GetOptions{})),
	} {
		if err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
	if err := d.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if done, err := d.Inspect(ctx); err != nil || !done {
		t.Fatalf("Inspect after = %v %v", done, err)
	}
	// A second Apply updates in place without error (Service keeps its cluster IP).
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
}

func get[T any](_ T, err error) error { return err }

func TestDeployInspectSeesDrift(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token")
	d := deployOf(a)
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	deps := cs.AppsV1().Deployments("kyq-hello")
	dep, _ := deps.Get(ctx, appName, metav1.GetOptions{})
	dep.Spec.Replicas = ptr.To(int32(0))
	deps.Update(ctx, dep, metav1.UpdateOptions{})
	if done, _ := d.Inspect(ctx); done {
		t.Fatal("scaled-to-zero deployment counted as deployed")
	}
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	cs.CoreV1().Services("kyq-hello").Delete(ctx, appName, metav1.DeleteOptions{})
	if err := d.Verify(ctx); err == nil || !strings.Contains(err.Error(), "service") {
		t.Fatalf("Verify after service deletion = %v", err)
	}
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if done, _ := d.Inspect(ctx); !done {
		t.Fatal("not restored")
	}
}

func TestDeployRefusesForeignObject(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token")
	if err := a.ensureNamespace(ctx); err != nil {
		t.Fatal(err)
	}
	foreign := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: "kyq-hello", Labels: map[string]string{"team": "other"}}}
	cs.AppsV1().Deployments("kyq-hello").Create(ctx, foreign, metav1.CreateOptions{})
	err := deployOf(a).Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "not managed by kyquickstart") {
		t.Fatalf("err = %v", err)
	}
	got, _ := cs.AppsV1().Deployments("kyq-hello").Get(ctx, appName, metav1.GetOptions{})
	if got.Labels["team"] != "other" {
		t.Fatal("foreign deployment was overwritten")
	}
}

func TestDeployKeepsExistingClaim(t *testing.T) {
	ctx := context.Background()
	a, cs := testApp(t, "token")
	if err := a.ensureNamespace(ctx); err != nil {
		t.Fatal(err)
	}
	claim := render("hello", a.Catalog, "rs-0").Claims[0]
	claim.Spec.VolumeName = "bound-pv"
	cs.CoreV1().PersistentVolumeClaims("kyq-hello").Create(ctx, claim, metav1.CreateOptions{})
	if err := deployOf(a).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := cs.CoreV1().PersistentVolumeClaims("kyq-hello").Get(ctx, "vol-data", metav1.GetOptions{})
	if got.Spec.VolumeName != "bound-pv" {
		t.Fatal("existing claim was rewritten")
	}
}
```

`internal/kube/health_test.go`:

```go
package kube

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func setStatus(t *testing.T, a App, s appsv1.DeploymentStatus) {
	t.Helper()
	deps := a.Client.cs.AppsV1().Deployments("kyq-hello")
	d, err := deps.Get(context.Background(), appName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d.Status = s
	if _, err := deps.UpdateStatus(context.Background(), d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func readyStatus() appsv1.DeploymentStatus {
	return appsv1.DeploymentStatus{Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1}
}

func TestHealthWaitsForReady(t *testing.T) {
	pollInterval = time.Millisecond
	ctx := context.Background()
	a, _ := testApp(t, "token")
	if err := deployOf(a).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	h := Steps(a)[2]
	if done, _ := h.Inspect(ctx); done {
		t.Fatal("unready deployment counted as healthy")
	}
	go func() { // no t.Fatal off the test goroutine
		time.Sleep(20 * time.Millisecond)
		deps := a.Client.cs.AppsV1().Deployments("kyq-hello")
		d, _ := deps.Get(ctx, appName, metav1.GetOptions{})
		d.Status = readyStatus()
		deps.UpdateStatus(ctx, d, metav1.UpdateOptions{})
	}()
	if err := h.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.Verify(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHealthFailsOnStuckRollout(t *testing.T) {
	pollInterval = time.Millisecond
	ctx := context.Background()
	a, _ := testApp(t, "token")
	if err := deployOf(a).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	setStatus(t, a, appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{{
		Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded",
	}}})
	err := Steps(a)[2].Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "ProgressDeadlineExceeded") {
		t.Fatalf("err = %v", err)
	}
}
```

The fake clientset does not run controllers, so tests set Deployment status by hand.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/kube/`
Expected: FAIL, `undefined: Steps`.

- [ ] **Step 3: Implement**

Append to `internal/kube/app.go` (add the `engine` import if absent):

```go
// Steps returns the app's install steps: secrets, deploy, health.
func Steps(a App) []engine.Step {
	hash := render(a.Name, a.Catalog, a.ReleaseSet).hash()
	return []engine.Step{secretsStep{a}, deployStep{a: a, hash: hash}, healthStep{a: a, hash: hash}}
}
```

`internal/kube/deploy.go`:

```go
package kube

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type deployStep struct {
	a    App
	hash string
}

func (d deployStep) ID() string        { return d.a.Name + ".deploy" }
func (d deployStep) InputHash() string { return d.hash }

// check returns why the deployment is not current, or "" if it is.
func (d deployStep) check(ctx context.Context) (string, error) {
	ns := namespaceOf(d.a.Name)
	cs := d.a.Client.cs
	want := render(d.a.Name, d.a.Catalog, d.a.ReleaseSet)
	dep, found, err := getOwned(ctx, cs.AppsV1().Deployments(ns), "deployment", appName)
	switch {
	case err != nil:
		return "", err
	case !found:
		return "deployment missing", nil
	case dep.Annotations[hashAnnotation] != d.hash:
		return "deployed objects differ from this release", nil
	case dep.Spec.Replicas == nil || *dep.Spec.Replicas != 1:
		return "deployment is not at one replica", nil
	}
	if want.Service != nil {
		if _, found, err := getOwned(ctx, cs.CoreV1().Services(ns), "service", appName); err != nil || !found {
			return "service missing", err
		}
	}
	for _, c := range want.Claims {
		if _, found, err := getOwned(ctx, cs.CoreV1().PersistentVolumeClaims(ns), "claim", c.Name); err != nil || !found {
			return "claim " + c.Name + " missing", err
		}
	}
	if _, found, err := getOwned(ctx, cs.NetworkingV1().NetworkPolicies(ns), "network policy", policyName); err != nil || !found {
		return "network policy missing", err
	}
	return "", nil
}

func (d deployStep) Inspect(ctx context.Context) (bool, error) {
	why, err := d.check(ctx)
	return why == "", err
}

// Apply writes the policy, claims and service, then the Deployment carrying the input hash, so
// a run that dies early redeploys next time.
func (d deployStep) Apply(ctx context.Context) error {
	if err := d.a.ensureNamespace(ctx); err != nil {
		return err
	}
	ns := namespaceOf(d.a.Name)
	cs := d.a.Client.cs
	o := render(d.a.Name, d.a.Catalog, d.a.ReleaseSet)
	if err := upsert(ctx, cs.NetworkingV1().NetworkPolicies(ns), "network policy", o.Policy, nil); err != nil {
		return err
	}
	for _, c := range o.Claims {
		// A claim's spec is immutable once bound: create it if missing, never rewrite it.
		_, found, err := getOwned(ctx, cs.CoreV1().PersistentVolumeClaims(ns), "claim", c.Name)
		if err != nil {
			return err
		}
		if !found {
			if _, err := cs.CoreV1().PersistentVolumeClaims(ns).Create(ctx, c, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("claim %s: %w", c.Name, err)
			}
		}
	}
	if o.Service != nil {
		svc := o.Service
		err := upsert(ctx, cs.CoreV1().Services(ns), "service", svc, func(have *corev1.Service) {
			svc.Spec.ClusterIP, svc.Spec.ClusterIPs = have.Spec.ClusterIP, have.Spec.ClusterIPs
			svc.Spec.IPFamilies, svc.Spec.IPFamilyPolicy = have.Spec.IPFamilies, have.Spec.IPFamilyPolicy
		})
		if err != nil {
			return err
		}
	}
	o.Deployment.Annotations = map[string]string{hashAnnotation: d.hash}
	return upsert(ctx, cs.AppsV1().Deployments(ns), "deployment", o.Deployment, nil)
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

`internal/kube/health.go` (rollout logic copied from `KyYard-Server/internal/runtime/kubernetes/deploy.go`,
MIT, Busnes.app; outcomes replaced by plain errors):

```go
package kube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

var pollInterval = 2 * time.Second

type healthStep struct {
	a    App
	hash string
}

func (h healthStep) ID() string        { return h.a.Name + ".health" }
func (h healthStep) InputHash() string { return h.hash }

// state returns why the app is not ready ("" when it is) and whether the controller gave up.
func (h healthStep) state(ctx context.Context) (string, bool, error) {
	d, found, err := getOwned(ctx, h.a.Client.cs.AppsV1().Deployments(namespaceOf(h.a.Name)), "deployment", appName)
	switch {
	case err != nil:
		return "", false, err
	case !found:
		return "deployment missing", false, nil
	case ready(d):
		return "", false, nil
	case failed(d):
		return "rollout failed: " + reasons(d), true, nil
	}
	return fmt.Sprintf("%d of 1 replicas ready", d.Status.ReadyReplicas), false, nil
}

func (h healthStep) Inspect(ctx context.Context) (bool, error) {
	why, _, err := h.state(ctx)
	return why == "", err
}

// Apply waits for the rollout; it changes nothing. A failure must hold on two consecutive polls:
// right after a write the controller can still report the previous ReplicaSet's failure. A
// transient read error is not an answer, so the wait polls on.
func (h healthStep) Apply(ctx context.Context) error {
	timeout := time.Duration(h.a.Catalog.Health.TimeoutSeconds) * time.Second
	deadline := time.Now().Add(timeout)
	strikes := 0
	last := "no answer from the API server"
	for {
		why, gaveUp, err := h.state(ctx)
		switch {
		case err != nil && !transient(err):
			return err
		case err == nil && why == "":
			return nil
		case err == nil:
			last = why
			if gaveUp {
				if strikes++; strikes >= 2 {
					return errors.New(why)
				}
			} else {
				strikes = 0
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not ready after %s: %s", timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (h healthStep) Verify(ctx context.Context) error {
	why, _, err := h.state(ctx)
	if err != nil {
		return err
	}
	if why != "" {
		return errors.New(why)
	}
	return nil
}

// transient is a read error worth another poll: no answer from the API server, or one that says
// try again (429, 5xx). Any other answer (403, 404) will not change by waiting.
func transient(err error) bool {
	if errors.Is(err, errNotOwned) {
		return false
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return true
	}
	code := status.Status().Code
	return code == http.StatusTooManyRequests || code >= http.StatusInternalServerError
}

func ready(d *appsv1.Deployment) bool {
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	s := d.Status
	return s.ObservedGeneration >= d.Generation && s.Replicas == want && s.UpdatedReplicas == want && s.ReadyReplicas == want && s.AvailableReplicas == want
}

// failed is a rollout the controller gave up on for the current generation: a pod the cluster
// refused to create (ReplicaFailure), or the progress deadline passed.
func failed(d *appsv1.Deployment) bool {
	if d.Status.ObservedGeneration < d.Generation {
		return false
	}
	return slices.ContainsFunc(d.Status.Conditions, func(c appsv1.DeploymentCondition) bool {
		return c.Type == appsv1.DeploymentReplicaFailure && c.Status == corev1.ConditionTrue ||
			c.Type == appsv1.DeploymentProgressing && c.Reason == "ProgressDeadlineExceeded"
	})
}

// reasons names the failing conditions, for the operator.
func reasons(d *appsv1.Deployment) string {
	var out []string
	for _, c := range d.Status.Conditions {
		if c.Reason != "" && (c.Type == appsv1.DeploymentReplicaFailure || c.Type == appsv1.DeploymentProgressing) {
			out = append(out, string(c.Type)+"="+c.Reason)
		}
	}
	return fmt.Sprint(out)
}
```

- [ ] **Step 4: Run checks**

Run: `go test -race ./internal/kube/ && make ci`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kube
git commit -m "feat: deploy and health steps on a cluster"
```

---

### Task 6: Lease lock and preflight

**Files:**
- Create: `internal/kube/lock.go`, `internal/kube/preflight.go`
- Test: `internal/kube/lock_test.go`, `internal/kube/preflight_test.go`

**Interfaces:**
- Produces: `kube.Acquire(ctx, c *Client, holder string) (release func(context.Context) error, err error)`,
  `kube.LockedError{Holder string; Started time.Time}`,
  `kube.Preflight(ctx, c *Client, needsStorage bool) []kube.Finding`,
  `kube.Finding{Check string; OK bool; Detail string}` (same fields and order as `dockerhost.Finding`).

- [ ] **Step 1: Write the failing tests**

`internal/kube/lock_test.go`:

```go
package kube

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLeaseLock(t *testing.T) {
	ctx := context.Background()
	c := New(fake.NewClientset())
	release, err := Acquire(ctx, c, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Acquire(ctx, c, "run-b")
	var le *LockedError
	if !errors.As(err, &le) || le.Holder != "run-a" || time.Since(le.Started) > time.Minute {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(le.Error(), "by hand") {
		t.Errorf("message = %q", le.Error())
	}
	if err := release(ctx); err != nil {
		t.Fatal(err)
	}
	release2, err := Acquire(ctx, c, "run-b")
	if err != nil {
		t.Fatal(err)
	}
	release2(ctx)
}

func TestReleaseLeavesSomeoneElsesLease(t *testing.T) {
	ctx := context.Background()
	c := New(fake.NewClientset())
	release, err := Acquire(ctx, c, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	leases := c.cs.CoordinationV1().Leases(lockNamespace)
	l, _ := leases.Get(ctx, lockName, metav1.GetOptions{})
	l.Annotations[runAnnotation] = "someone-else"
	leases.Update(ctx, l, metav1.UpdateOptions{})
	if err := release(ctx); err == nil {
		t.Fatal("released a lease it does not hold")
	}
	if _, err := leases.Get(ctx, lockName, metav1.GetOptions{}); err != nil {
		t.Fatalf("lease gone: %v", err)
	}
}
```

`internal/kube/preflight_test.go`:

```go
package kube

import (
	"context"
	"strings"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func cluster(minor string, deny string) *fake.Clientset {
	cs := fake.NewClientset()
	cs.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{Major: "1", Minor: minor}
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		r := a.(ktesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		attrs := r.Spec.ResourceAttributes
		r.Status.Allowed = attrs.Verb+" "+attrs.Resource != deny
		return true, r, nil
	})
	return cs
}

func find(fs []Finding, check string) Finding {
	for _, f := range fs {
		if f.Check == check {
			return f
		}
	}
	return Finding{Check: "absent"}
}

func TestPreflight(t *testing.T) {
	ctx := context.Background()
	ok := cluster("37+", "")
	ok.StorageV1().StorageClasses().Create(ctx, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{
		Name: "standard", Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"},
	}}, metav1.CreateOptions{})
	for _, f := range Preflight(ctx, New(ok), true) {
		if !f.OK {
			t.Errorf("healthy cluster failed %+v", f)
		}
	}
	if f := find(Preflight(ctx, New(cluster("29", "")), false), "kubernetes"); f.OK {
		t.Errorf("1.29 accepted: %+v", f)
	}
	if f := find(Preflight(ctx, New(cluster("37", "create deployments")), false), "permissions"); f.OK || !strings.Contains(f.Detail, "create deployments") {
		t.Errorf("denied permission not reported: %+v", f)
	}
	if f := find(Preflight(ctx, New(cluster("37", "")), true), "storage"); f.OK {
		t.Errorf("missing default StorageClass accepted: %+v", f)
	}
	if f := find(Preflight(ctx, New(cluster("37", "")), false), "storage"); f.Check != "absent" {
		t.Errorf("storage checked when no app needs it: %+v", f)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/kube/`
Expected: FAIL, `undefined: Acquire` / `undefined: Preflight`.

- [ ] **Step 3: Implement**

`internal/kube/lock.go`:

```go
package kube

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/managed"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	lockNamespace = "kyquickstart"
	lockName      = "kyquickstart-lock"
	runAnnotation = "kyquickstart/run"
)

// LockedError is a cluster held by another run. The Lease is never broken automatically.
type LockedError struct {
	Holder  string
	Started time.Time
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("cluster is locked by %q since %s (%s ago); delete Lease %s/%s by hand once you are sure that run is gone",
		e.Holder, e.Started.Format(time.RFC3339), time.Since(e.Started).Round(time.Second), lockNamespace, lockName)
}

var errNotHeld = errors.New("lock is not held by this run")

// Acquire creates the lock Lease for one run; release deletes it only while this run holds it.
func Acquire(ctx context.Context, c *Client, holder string) (release func(context.Context) error, err error) {
	b := make([]byte, 16)
	rand.Read(b)
	run := hex.EncodeToString(b)
	labels := map[string]string{managed.Label: managed.Value, podSecurity: "restricted"}
	if err := c.ensureNamespace(ctx, lockNamespace, labels); err != nil {
		return nil, err
	}
	leases := c.cs.CoordinationV1().Leases(lockNamespace)
	release = func(ctx context.Context) error {
		l, err := leases.Get(ctx, lockName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) || err == nil && l.Annotations[runAnnotation] != run {
			return fmt.Errorf("release %s/%s: %w", lockNamespace, lockName, errNotHeld)
		}
		if err != nil {
			return fmt.Errorf("release %s/%s: %w", lockNamespace, lockName, err)
		}
		uid, rv := l.UID, l.ResourceVersion
		return leases.Delete(ctx, lockName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})
	}
	_, err = leases.Create(ctx, &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: lockName, Namespace: lockNamespace,
			Labels: map[string]string{managed.Label: managed.Value}, Annotations: map[string]string{runAnnotation: run}},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder, AcquireTime: &metav1.MicroTime{Time: time.Now()}},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		le := &LockedError{}
		if l, err := leases.Get(ctx, lockName, metav1.GetOptions{}); err == nil {
			if l.Spec.HolderIdentity != nil {
				le.Holder = *l.Spec.HolderIdentity
			}
			if l.Spec.AcquireTime != nil {
				le.Started = l.Spec.AcquireTime.Time
			}
		}
		return nil, le
	}
	if err != nil {
		// The create may have landed before the error (a cancelled run): remove it if it is ours.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if rerr := release(cctx); rerr != nil && !errors.Is(rerr, errNotHeld) {
			return nil, errors.Join(fmt.Errorf("acquire lock: %w", err), rerr)
		}
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	return release, nil
}
```

`internal/kube/preflight.go`:

```go
package kube

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Finding is one read-only preflight check.
type Finding struct {
	Check  string
	OK     bool
	Detail string
}

const minMinor = 30 // Kubernetes 1.30

// required is every permission an install uses; namespace "" means all namespaces.
var required = func() []authorizationv1.ResourceAttributes {
	var out []authorizationv1.ResourceAttributes
	add := func(group, resource, ns string, verbs ...string) {
		for _, v := range verbs {
			out = append(out, authorizationv1.ResourceAttributes{Group: group, Resource: resource, Namespace: ns, Verb: v})
		}
	}
	add("", "namespaces", "", "get", "create", "update")
	add("", "secrets", "", "get", "create", "update")
	add("", "services", "", "get", "create", "update")
	add("", "persistentvolumeclaims", "", "get", "create")
	add("apps", "deployments", "", "get", "create", "update")
	add("networking.k8s.io", "networkpolicies", "", "get", "create", "update")
	add("coordination.k8s.io", "leases", lockNamespace, "get", "create", "delete")
	return out
}()

// Preflight checks a cluster and changes nothing.
func Preflight(ctx context.Context, c *Client, needsStorage bool) []Finding {
	out := []Finding{c.versionCheck(), c.accessCheck(ctx)}
	if needsStorage {
		out = append(out, c.storageCheck(ctx))
	}
	return out
}

func (c *Client) versionCheck() Finding {
	v, err := c.cs.Discovery().ServerVersion()
	if err != nil {
		return Finding{"kubernetes", false, err.Error()}
	}
	minor, err := strconv.Atoi(strings.TrimRight(v.Minor, "+"))
	if err != nil || v.Major != "1" {
		return Finding{"kubernetes", false, fmt.Sprintf("cannot read version %s.%s", v.Major, v.Minor)}
	}
	if minor < minMinor {
		return Finding{"kubernetes", false, fmt.Sprintf("1.%d is older than 1.%d", minor, minMinor)}
	}
	return Finding{"kubernetes", true, "1." + v.Minor}
}

func (c *Client) accessCheck(ctx context.Context) Finding {
	var denied []string
	for _, attrs := range required {
		r, err := c.cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx,
			&authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &attrs}},
			metav1.CreateOptions{})
		if err != nil {
			return Finding{"permissions", false, err.Error()}
		}
		if !r.Status.Allowed {
			denied = append(denied, attrs.Verb+" "+attrs.Resource)
		}
	}
	if len(denied) > 0 {
		return Finding{"permissions", false, "denied: " + strings.Join(denied, ", ")}
	}
	return Finding{"permissions", true, fmt.Sprintf("%d checks allowed", len(required))}
}

func (c *Client) storageCheck(ctx context.Context) Finding {
	list, err := c.cs.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Finding{"storage", false, err.Error()}
	}
	for _, sc := range list.Items {
		if sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			return Finding{"storage", true, "default StorageClass " + sc.Name}
		}
	}
	return Finding{"storage", false, "no default StorageClass; an app here needs a volume"}
}
```

- [ ] **Step 4: Run checks**

Run: `go test -race ./internal/kube/ && make ci`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kube
git commit -m "feat: Lease lock and read-only preflight for clusters"
```

---

### Task 7: CLI drivers and placement

**Files:**
- Create: `internal/cli/driver.go`
- Modify: `internal/cli/cli.go`, `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: `dockerhost.{Preflight, Acquire, Steps, App}`, `kube.{Connect, Preflight, Acquire, Steps, App}`,
  `stack.Target.Kubernetes()`.
- Produces: unexported `driver` interface; `cli.Run` behaviour unchanged for SSH targets.

- [ ] **Step 1: Write the failing tests**

Append to `internal/cli/cli_test.go`:

```go
func TestPlacementAndKubeconfigChecks(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	outside := filepath.Join(t.TempDir(), "kubeconfig")
	os.WriteFile(outside, []byte("not: a kubeconfig\n"), 0o600)
	kubeStack := func(kc string) string {
		return "version: 1\ntargets:\n  - name: k1\n    kubeconfig: " + kc + "\napps:\n  - name: hello\n    target: k1\n"
	}

	// Inside the state directory: refused before any connection.
	_, err := runIn(t, kubeStack("kubeconfig"), "preflight")
	if err == nil || !strings.Contains(err.Error(), "inside the state directory") {
		t.Errorf("relative kubeconfig in state: %v", err)
	}

	// A Docker-only app on a cluster: refused before any connection.
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "apps", "dockeronly"), 0o700)
	os.WriteFile(filepath.Join(dir, "apps", "dockeronly", "manifest.yaml"), []byte(
		"name: dockeronly\ncategory: third-party\nimages:\n  web: traefik/whoami:v1.11.0@sha256:200689790a0a0ea48ca45992e0450bc26ccab5307375b41c84dfc4f2475937ab\nhealth:\n  timeout_seconds: 30\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "apps", "dockeronly", "compose.yaml.tmpl"), []byte(
		"services:\n  web:\n    image: {{ .Images.web }}\n"), 0o600)
	state := filepath.Join(dir, "state")
	os.MkdirAll(state, 0o700)
	os.WriteFile(filepath.Join(state, "stack.yaml"), []byte(strings.Replace(kubeStack(outside), "hello", "dockeronly", 1)), 0o600)
	var out bytes.Buffer
	err = Run(context.Background(), []string{"preflight", "--state", state}, Options{Catalog: os.DirFS(filepath.Join(dir, "apps")), Out: &out})
	if err == nil || !strings.Contains(err.Error(), "no Kubernetes deployment") {
		t.Errorf("docker-only app on a cluster: %v", err)
	}

	// --trust-host-key names a Kubernetes target.
	_, err = runIn(t, kubeStack(outside), "preflight", "--trust-host-key", "k1=SHA256:abc")
	if err == nil || !strings.Contains(err.Error(), "Kubernetes target") {
		t.Errorf("trust flag on a cluster: %v", err)
	}

	// A kubeconfig outside the state dir passes load and fails at connect (it is not valid).
	_, err = runIn(t, kubeStack(outside), "preflight")
	if err == nil || !strings.Contains(err.Error(), "kubeconfig") || strings.Contains(err.Error(), "inside the state") {
		t.Errorf("outside kubeconfig: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/`
Expected: FAIL (the relative kubeconfig is accepted, or SSH dial is attempted).

- [ ] **Step 3: Implement**

`internal/cli/driver.go`:

```go
package cli

import (
	"context"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// finding has the fields of dockerhost.Finding and kube.Finding, so both convert to it.
type finding struct {
	Check  string
	OK     bool
	Detail string
}

// driver is what apply and preflight need from one target.
type driver interface {
	preflight(ctx context.Context) []finding
	acquire(ctx context.Context, holder string) (func(context.Context) error, error)
	steps(name string, app catalog.App, releaseSet string, redact *engine.Redactor) []engine.Step
	close() error
}

type sshDriver struct {
	t stack.Target
	c *remote.SSH
}

func (d sshDriver) preflight(ctx context.Context) []finding {
	var out []finding
	for _, f := range dockerhost.Preflight(ctx, d.c, d.t) {
		out = append(out, finding(f))
	}
	return out
}

func (d sshDriver) acquire(ctx context.Context, holder string) (func(context.Context) error, error) {
	return dockerhost.Acquire(ctx, d.c, d.t.Root, holder)
}

func (d sshDriver) steps(name string, app catalog.App, releaseSet string, redact *engine.Redactor) []engine.Step {
	return dockerhost.Steps(dockerhost.App{Name: name, Root: d.t.Root, Runner: d.c, Catalog: app, ReleaseSet: releaseSet, Redact: redact})
}

func (d sshDriver) close() error { return d.c.Close() }

type kubeDriver struct {
	c            *kube.Client
	needsStorage bool
}

func (d kubeDriver) preflight(ctx context.Context) []finding {
	var out []finding
	for _, f := range kube.Preflight(ctx, d.c, d.needsStorage) {
		out = append(out, finding(f))
	}
	return out
}

func (d kubeDriver) acquire(ctx context.Context, holder string) (func(context.Context) error, error) {
	return kube.Acquire(ctx, d.c, holder)
}

func (d kubeDriver) steps(name string, app catalog.App, releaseSet string, redact *engine.Redactor) []engine.Step {
	return kube.Steps(kube.App{Name: name, Client: d.c, Catalog: app, ReleaseSet: releaseSet, Redact: redact})
}

func (d kubeDriver) close() error { return nil }
```

In `internal/cli/cli.go`:

1. Replace the `conns map[string]*remote.SSH` field with `drivers map[string]driver` and initialise
   it in `load`.
2. In `load`, directly after the `--trust-host-key` loop and before `placed` and `targets` are
   built, resolve every kubeconfig path in `st.Targets` (so `placed` and `targets` carry it):

```go
	stateAbs, err := filepath.Abs(state)
	if err != nil {
		return nil, err
	}
	for i, t := range st.Targets {
		if !t.Kubernetes() {
			continue
		}
		if _, ok := trust[t.Name]; ok {
			return nil, fmt.Errorf("--trust-host-key names %q, a Kubernetes target", t.Name)
		}
		kc := t.Kubeconfig
		if !filepath.IsAbs(kc) {
			kc = filepath.Join(stateAbs, kc)
		}
		if rel, err := filepath.Rel(stateAbs, kc); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("target %q: kubeconfig %s is inside the state directory, which must hold no secrets", t.Name, kc)
		}
		st.Targets[i].Kubeconfig = kc
	}
```

   Then, after `plan.Order` succeeds (so every app is known to be in the catalog), check placement:

```go
	for name, t := range s.placed {
		app := cat[name]
		switch {
		case t.Kubernetes() && app.Kubernetes == nil:
			return nil, fmt.Errorf("app %q has no Kubernetes deployment; place it on a Docker host", name)
		case !t.Kubernetes() && app.Compose == nil:
			return nil, fmt.Errorf("app %q has no Compose deployment; place it on a Kubernetes target", name)
		}
	}
```

3. Replace `connect` so it builds one driver per target:

```go
func (s *session) connect(ctx context.Context, trust trustFlag) error {
	confirm := s.confirmer()
	for _, t := range s.targets {
		if t.Kubernetes() {
			c, err := kube.Connect(t.Kubeconfig, t.Context)
			if err != nil {
				return fmt.Errorf("%s: %w", t.Name, err)
			}
			s.drivers[t.Name] = kubeDriver{c: c, needsStorage: s.needsStorage(t.Name)}
			continue
		}
		c, err := remote.Dial(ctx, remote.SSHConfig{
			Name: t.Name, Host: t.Host, Port: t.Port, User: t.User,
			KnownHosts: filepath.Join(s.state, "known_hosts"),
			Trust:      trust[t.Name],
			Confirm:    confirm,
		})
		if err != nil {
			return err
		}
		s.drivers[t.Name] = sshDriver{t: t, c: c}
	}
	return nil
}

func (s *session) needsStorage(target string) bool {
	for name, t := range s.placed {
		if k := s.cat[name].Kubernetes; t.Name == target && k != nil && len(k.Volumes) > 0 {
			return true
		}
	}
	return false
}
```

4. `close`, `preflight` and `apply` use `s.drivers[t.Name]`:
   - `close`: `for _, d := range s.drivers { d.close() }`.
   - `preflight`: iterate `s.drivers[t.Name].preflight(ctx)` (same output format).
   - `apply`: `release, err := s.drivers[t.Name].acquire(ctx, holder)`, and steps via
     `s.drivers[t.Name].steps(name, s.cat[name], s.o.ReleaseSet, redact)`.

`cli.go` no longer calls `dockerhost` directly; drop that import. The SSH target's
`--trust-host-key` validation, the usage text and all existing tests stay as they are.

- [ ] **Step 4: Run checks**

Run: `go test -race ./internal/cli/ && make ci && make build && ./kyquickstart version`
Expected: PASS; `dev`.

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat: pick a Docker or Kubernetes driver per target"
```

---

### Task 8: kind end-to-end test

**Files:**
- Create: `test/e2e/kube_test.go`
- Modify: `Makefile` (e2e timeout), `.github/workflows/ci.yml` (none needed beyond the timeout; the
  existing `e2e` job runs every e2e test)

**Interfaces:**
- Consumes: `cli.Run`, `cli.Options`, `kube.LockedError`, the hello fixture.

kind is run with `go run sigs.k8s.io/kind@v0.33.0`, never installed. Node image
`kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5`
(kind v0.33.0's default, verified to build a cluster here in 43 s with default StorageClass
`standard`).

- [ ] **Step 1: Write the test**

`test/e2e/kube_test.go`:

```go
//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
)

const kindNode = "kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5"

func kind(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("go", append([]string{"run", "sigs.k8s.io/kind@v0.33.0"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("kind %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestKubernetesApply(t *testing.T) {
	ctx := context.Background()
	work := t.TempDir()
	state := filepath.Join(work, "state")
	os.MkdirAll(state, 0o700)
	kc := filepath.Join(work, "kubeconfig") // outside the state directory
	id := make([]byte, 4)
	rand.Read(id)
	name := "kyq-e2e-" + hex.EncodeToString(id)
	kind(t, "create", "cluster", "--name", name, "--image", kindNode, "--kubeconfig", kc, "--wait", "180s")
	t.Cleanup(func() { exec.Command("go", "run", "sigs.k8s.io/kind@v0.33.0", "delete", "cluster", "--name", name).Run() })

	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	cs := kubernetes.NewForConfigOrDie(cfg)

	os.WriteFile(filepath.Join(state, "stack.yaml"), []byte(
		"version: 1\ntargets:\n  - name: k8s\n    kubeconfig: "+kc+"\napps:\n  - name: hello\n    target: k8s\n"), 0o600)
	apply := func() (string, error) {
		var out bytes.Buffer
		o := cli.Options{Catalog: os.DirFS("../../internal/catalog/testdata/apps"), ReleaseSet: "e2e", Out: &out}
		err := cli.Run(ctx, []string{"apply", "--state", state}, o)
		t.Logf("apply:\n%s", out.String())
		return out.String(), err
	}

	// First apply installs.
	out, err := apply()
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	ns, err := cs.CoreV1().Namespaces().Get(ctx, "kyq-hello", metav1.GetOptions{})
	if err != nil || ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" || ns.Labels[managed.Label] != managed.Value {
		t.Fatalf("namespace = %+v, %v", ns, err)
	}
	sec, err := cs.CoreV1().Secrets("kyq-hello").Get(ctx, "kyq-secrets", metav1.GetOptions{})
	if err != nil || len(sec.Data["token"]) != 43 {
		t.Fatalf("secret: %v", err)
	}
	secret := string(sec.Data["token"])
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
	dep, err := cs.AppsV1().Deployments("kyq-hello").Get(ctx, "app", metav1.GetOptions{})
	if err != nil || dep.Labels[managed.ReleaseSetLabel] != "e2e" || dep.Status.ReadyReplicas != 1 {
		t.Fatalf("deployment = %+v, %v", dep.Status, err)
	}

	// Second apply changes nothing.
	out, err = apply()
	if err != nil || status(out, "hello.secrets") != "skipped" || status(out, "hello.deploy") != "skipped" || status(out, "hello.health") != "skipped" {
		t.Fatalf("second apply: %v", err)
	}

	// Scaled to zero by hand: redeployed.
	dep, _ = cs.AppsV1().Deployments("kyq-hello").Get(ctx, "app", metav1.GetOptions{})
	dep.Spec.Replicas = ptr.To(int32(0))
	if _, err := cs.AppsV1().Deployments("kyq-hello").Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	out, err = apply()
	if err != nil || status(out, "hello.deploy") != "ok" || status(out, "hello.secrets") != "skipped" {
		t.Fatalf("apply after scale-down: %v", err)
	}
	dep, _ = cs.AppsV1().Deployments("kyq-hello").Get(ctx, "app", metav1.GetOptions{})
	if *dep.Spec.Replicas != 1 || dep.Status.ReadyReplicas != 1 {
		t.Fatalf("not restored: %+v", dep.Status)
	}

	// A Lease left by another run stops apply and survives.
	started := metav1.NewMicroTime(time.Now().Add(-time.Hour))
	other := "other run"
	_, err = cs.CoordinationV1().Leases("kyquickstart").Create(ctx, &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "kyquickstart-lock", Namespace: "kyquickstart", Annotations: map[string]string{"kyquickstart/run": "x"}},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &other, AcquireTime: &started},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = apply()
	var le *kube.LockedError
	if !errors.As(err, &le) || le.Holder != "other run" {
		t.Fatalf("err = %v, want kube.LockedError", err)
	}
	if _, err := cs.CoordinationV1().Leases("kyquickstart").Get(ctx, "kyquickstart-lock", metav1.GetOptions{}); err != nil {
		t.Fatalf("stale lease removed: %v", err)
	}
}
```

(`status` is the helper already in `test/e2e/e2e_test.go`, same package.)

In the `Makefile`, give the e2e target room for cluster creation:

```make
e2e:
	go test -tags e2e -count=1 -v -timeout 20m ./test/e2e/
```

- [ ] **Step 2: Run it**

Run: `make e2e`
Expected: both `TestApply` and `TestKubernetesApply` PASS. Then `docker ps -a --filter name=kyq-e2e`
must be empty (the kind node container is a `kyq-e2e-*-control-plane` and is deleted in cleanup).

If the first apply's health step times out, read the logged step output and the Deployment's
conditions; fix the manifest or renderer, never the timeout.

- [ ] **Step 3: Commit**

```bash
git add test/e2e/kube_test.go Makefile
git commit -m "test: add kind end-to-end apply"
```

---

### Task 9: Docs pass

**Files:**
- Modify: `AGENTS.md` (KyQuickStart section), `docs/superpowers/plans/2026-10-06-kyquickstart-roadmap.md`

- [ ] **Step 1: Update `AGENTS.md`**

Under `## Ownership` add:

```markdown
- Phase 2 Kubernetes driver: `docs/superpowers/plans/2026-10-08-phase-2-kubernetes-driver.md`.
```

Under `## Local Contracts` add:

```markdown
- On a cluster each app lives in namespace `kyq-<app>` (Pod Security `restricted`, default-deny
  ingress) and the run lock is Lease `kyquickstart/kyquickstart-lock`. The installer never adopts
  a namespace or object without the managed label. A kubeconfig is referenced by path and may not
  live inside the state directory.
```

Replace the `make e2e` Verification bullet with:

```markdown
- `make e2e`: needs Docker; runs `apply` against an sshd container on the local Docker socket and
  against a kind cluster (`go run sigs.k8s.io/kind@v0.33.0`), including drift and stale-lock cases.
```

- [ ] **Step 2: Update the roadmap**

At the end of the Phase 2 paragraph add: `Detailed plan:
docs/superpowers/plans/2026-10-08-phase-2-kubernetes-driver.md.`

- [ ] **Step 3: Commit**

```bash
git add AGENTS.md docs/superpowers/plans/2026-10-06-kyquickstart-roadmap.md
git commit -m "docs: record phase 2 contracts and checks"
```
