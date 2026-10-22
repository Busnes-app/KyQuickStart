package kube

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/netip"
	"os"
	"slices"
	"strings"
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

func renderHello(c catalog.App, releaseSet, storageClass string) objects {
	return render(App{Name: "hello", Catalog: c, ReleaseSet: releaseSet, StorageClass: storageClass})
}

func npmApp(t *testing.T) catalog.App {
	t.Helper()
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	return cat["npm"]
}

func TestRenderIsRestricted(t *testing.T) {
	o := renderHello(fixture(t), "rs-1", "")
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
	if o.Public != nil || pod.SecurityContext.Sysctls != nil || len(sc.Capabilities.Add) != 0 {
		t.Errorf("restricted app published ports, set sysctls or added capabilities")
	}
}

// A non-root app's claim is mounted through a 0700 subdirectory its own uid creates, so a
// group- or world-writable claim root never becomes the app's data directory.
func TestRenderPrivateVolumes(t *testing.T) {
	pod := renderHello(fixture(t), "rs-1", "").Deployment.Spec.Template.Spec
	if len(pod.InitContainers) != 1 {
		t.Fatalf("init containers = %+v", pod.InitContainers)
	}
	init := pod.InitContainers[0]
	if init.Image != CredentialGeneratorImage || !slices.Equal(init.Command, []string{"mkdir", "-p", "-m", "700", "/volumes/data/private"}) ||
		init.SecurityContext != pod.Containers[0].SecurityContext || init.Resources.Limits.Memory().String() != "16Mi" ||
		len(init.VolumeMounts) != 1 || init.VolumeMounts[0].Name != "vol-data" || init.VolumeMounts[0].MountPath != "/volumes/data" {
		t.Fatalf("init = %+v", init)
	}
	for _, m := range pod.Containers[0].VolumeMounts {
		if m.Name == "vol-data" && (m.MountPath != "/data" || m.SubPath != privateDir) {
			t.Fatalf("data mount = %+v", m)
		}
	}
	if npm := render(App{Name: "npm", Catalog: npmApp(t)}).Deployment.Spec.Template.Spec; len(npm.InitContainers) != 0 || npm.Containers[0].VolumeMounts[2].SubPath != "" {
		t.Fatal("a root app's claims changed")
	}
}

// An edge's egress reaches its upstreams off the cluster at exactly their address and port,
// and a change of upstream redeploys it.
func TestRenderEdgeUpstreams(t *testing.T) {
	a := App{Name: "npm", Catalog: npmApp(t)}
	before := render(a)
	a.Upstreams = []netip.AddrPort{netip.MustParseAddrPort("192.168.1.91:18443")}
	o := render(a)
	e := o.Policy.Spec.Egress
	if len(e) != 4 || e[3].To[0].IPBlock.CIDR != "192.168.1.91/32" || len(e[3].Ports) != 1 || e[3].Ports[0].Port.IntValue() != 18443 || len(e[3].To[0].IPBlock.Except) != 0 {
		t.Fatalf("egress = %+v", e)
	}
	if o.hash() == before.hash() {
		t.Fatal("upstream change keeps the deployment hash")
	}
}

// The edge's admin API port never leaves ClusterIP; only node_ports reach the NodePort Service.
func TestRenderNPMKeepsAdminInCluster(t *testing.T) {
	o := render(App{Name: "npm", Catalog: npmApp(t), ReleaseSet: "rs-1", StorageClass: "local-path"})
	if o.Service == nil || o.Service.Spec.Type != "" || len(o.Service.Spec.Ports) != 1 || o.Service.Spec.Ports[0].Port != catalog.AdminPort {
		t.Fatalf("cluster service = %+v", o.Service)
	}
	pub := o.Public
	if pub == nil || pub.Name != publicName || pub.Spec.Type != corev1.ServiceTypeNodePort || pub.Spec.ExternalTrafficPolicy != corev1.ServiceExternalTrafficPolicyLocal {
		t.Fatalf("public service = %+v", pub)
	}
	got := map[int32]int32{}
	for _, p := range pub.Spec.Ports {
		got[p.Port] = p.NodePort
	}
	if len(got) != 2 || got[80] != 30080 || got[443] != 30443 {
		t.Fatalf("published = %v", got)
	}
	for _, rule := range o.Policy.Spec.Ingress {
		for _, p := range rule.Ports {
			if p.Port.IntValue() == catalog.AdminPort {
				t.Fatal("policy admits the admin port")
			}
		}
	}
	if len(o.Policy.Spec.Ingress) != 1 || len(o.Policy.Spec.Ingress[0].From) != 0 || len(o.Policy.Spec.Ingress[0].Ports) != 2 {
		t.Fatalf("ingress = %+v", o.Policy.Spec.Ingress)
	}
	if len(o.Policy.Spec.PolicyTypes) != 2 || len(o.Policy.Spec.Egress) != 3 || o.Policy.Spec.Egress[0].To[0].NamespaceSelector.MatchLabels[nsNameLabel] != "kube-system" {
		t.Fatalf("egress = %+v", o.Policy.Spec)
	}
	pod := o.Deployment.Spec.Template.Spec
	sc := pod.Containers[0].SecurityContext
	want := []corev1.Capability{"CHOWN", "DAC_OVERRIDE", "SETGID", "SETUID", "KILL"}
	if *sc.AllowPrivilegeEscalation || *sc.RunAsNonRoot || *sc.RunAsUser != 0 || *sc.ReadOnlyRootFilesystem ||
		sc.Capabilities.Drop[0] != "ALL" || !slices.Equal(sc.Capabilities.Add, want) || sc.Privileged != nil || pod.HostNetwork {
		t.Fatalf("security context = %+v", sc)
	}
	if pod.SecurityContext.RunAsNonRoot != nil || pod.SecurityContext.FSGroup != nil || pod.SecurityContext.Sysctls[0].Value != "0" {
		t.Fatalf("pod security context = %+v", pod.SecurityContext)
	}
	if c := pod.Containers[0]; c.ReadinessProbe.HTTPGet.Port.IntValue() != catalog.AdminPort || len(o.Claims) != 2 {
		t.Fatalf("probe or claims = %+v %d", c.ReadinessProbe, len(o.Claims))
	}
	if (App{Catalog: npmApp(t)}).namespaceLabels()[podSecurity] != "baseline" {
		t.Fatal("npm namespace is not baseline")
	}
}

func TestRenderAdmitsEdgeOnly(t *testing.T) {
	o := render(App{Name: "hello", Catalog: fixture(t), ReleaseSet: "rs-1", Edge: "npm"})
	in := o.Policy.Spec.Ingress
	if len(in) != 1 || len(in[0].From) != 1 || in[0].From[0].NamespaceSelector.MatchLabels[nsNameLabel] != "kyq-npm" ||
		in[0].From[0].PodSelector.MatchLabels[nameLabel] != "npm" || in[0].Ports[0].Port.IntValue() != 8080 {
		t.Fatalf("ingress = %+v", in)
	}
	if len(o.Policy.Spec.PolicyTypes) != 1 {
		t.Fatal("app egress restricted")
	}
	if o.hash() == renderHello(fixture(t), "rs-1", "").hash() {
		t.Fatal("edge admission does not change the hash")
	}
}

// AC3: KyNotes drains apply-setup on stop, runs restricted as 65532 and keeps /data and
// /backups on private claims.
func TestRenderKyNotes(t *testing.T) {
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	a := App{Name: "kynotes", Catalog: cat["kynotes"]}
	o := render(a)
	pod := o.Deployment.Spec.Template.Spec
	sc := pod.Containers[0].SecurityContext
	if pod.TerminationGracePeriodSeconds == nil || *pod.TerminationGracePeriodSeconds != 1020 {
		t.Fatalf("grace = %v", pod.TerminationGracePeriodSeconds)
	}
	if *sc.RunAsUser != 65532 || *sc.RunAsGroup != 65532 || !*sc.RunAsNonRoot || !*sc.ReadOnlyRootFilesystem || *sc.AllowPrivilegeEscalation || !*pod.SecurityContext.RunAsNonRoot {
		t.Fatalf("security context = %+v", sc)
	}
	if len(pod.InitContainers) != 1 || pod.InitContainers[0].Name != "private-volumes" || !slices.Equal(pod.InitContainers[0].Command, []string{"mkdir", "-p", "-m", "700", "/volumes/data/private", "/volumes/backups/private"}) {
		t.Fatalf("init = %+v", pod.InitContainers)
	}
	mounts := map[string]string{}
	for _, m := range pod.Containers[0].VolumeMounts {
		mounts[m.MountPath] = m.Name + ":" + m.SubPath
	}
	if mounts["/data"] != "vol-data:private" || mounts["/backups"] != "vol-backups:private" || len(o.Claims) != 2 {
		t.Fatalf("mounts = %v, claims %d", mounts, len(o.Claims))
	}
	if a.namespaceLabels()[podSecurity] != "restricted" {
		t.Fatal("namespace not restricted")
	}
	// An app without a grace keeps the default and its hash.
	if renderHello(fixture(t), "rs-1", "").Deployment.Spec.Template.Spec.TerminationGracePeriodSeconds != nil {
		t.Fatal("default grace rendered")
	}
}

func TestRenderKyCalendar(t *testing.T) {
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	c := cat["kycalendar"]
	c.Kubernetes = c.Kubernetes.WithHost("calendar.example.com")
	a := App{Name: "kycalendar", Catalog: c}
	o := render(a)
	pod := o.Deployment.Spec.Template.Spec
	ctr := pod.Containers[0]
	sc := ctr.SecurityContext
	if pod.TerminationGracePeriodSeconds == nil || *pod.TerminationGracePeriodSeconds != 1200 {
		t.Fatalf("grace = %v", pod.TerminationGracePeriodSeconds)
	}
	if *sc.RunAsUser != 10001 || *sc.RunAsGroup != 10001 || !*sc.RunAsNonRoot || !*sc.ReadOnlyRootFilesystem || *sc.AllowPrivilegeEscalation || !*pod.SecurityContext.RunAsNonRoot {
		t.Fatalf("security context = %+v", sc)
	}
	if len(pod.InitContainers) != 1 || pod.InitContainers[0].Name != "private-volumes" || !slices.Equal(pod.InitContainers[0].Command, []string{"mkdir", "-p", "-m", "700", "/volumes/data/private", "/volumes/backups/private"}) {
		t.Fatalf("init = %+v", pod.InitContainers)
	}
	mounts := map[string]string{}
	for _, m := range ctr.VolumeMounts {
		mounts[m.MountPath] = m.Name + ":" + m.SubPath
	}
	tmp := slices.IndexFunc(pod.Volumes, func(v corev1.Volume) bool { return v.Name == "tmp" && v.EmptyDir != nil })
	if mounts["/app/data"] != "vol-data:private" || mounts["/app/backups"] != "vol-backups:private" || mounts["/tmp"] != "tmp:" || tmp < 0 || len(o.Claims) != 2 {
		t.Fatalf("mounts = %v, claims %d", mounts, len(o.Claims))
	}
	if p := ctr.ReadinessProbe; p == nil || p.HTTPGet.Path != "/api/installer/network" || p.HTTPGet.Port.IntValue() != 8080 {
		t.Fatalf("readiness = %+v", p)
	}
	env := map[string]string{}
	for _, e := range ctr.Env {
		env[e.Name] = e.Value
	}
	if env["KY_APP_URL"] != "https://calendar.example.com" {
		t.Fatalf("env = %v", env)
	}
	if a.namespaceLabels()[podSecurity] != "restricted" {
		t.Fatal("namespace not restricted")
	}
}

func TestNamespaceLevelFailsClosed(t *testing.T) {
	for level, want := range map[string]string{"baseline": "baseline", "restricted": "restricted", "": "restricted", "privileged": "restricted"} {
		if got := (App{Catalog: catalog.App{Manifest: catalog.Manifest{PodSecurity: level}}}).namespaceLabels()[podSecurity]; got != want {
			t.Errorf("%q -> %q", level, got)
		}
	}
}

func TestRenderHashFollowsInputs(t *testing.T) {
	a, b := renderHello(fixture(t), "rs-1", ""), renderHello(fixture(t), "rs-1", "")
	if a.hash() != b.hash() {
		t.Fatal("hash not deterministic")
	}
	if a.hash() == renderHello(fixture(t), "rs-2", "").hash() {
		t.Fatal("release set change did not change the hash")
	}
}

func TestRenderEnvIsSorted(t *testing.T) {
	app := fixture(t)
	k := *app.Kubernetes
	k.Env = map[string]string{"B": "2", "A": "1", "C": "3"}
	app.Kubernetes = &k
	env := renderHello(app, "rs-1", "").Deployment.Spec.Template.Spec.Containers[0].Env
	if len(env) != 3 || env[0].Name != "A" || env[1].Value != "2" || env[2].Name != "C" {
		t.Fatalf("env = %+v", env)
	}
}

func TestRenderWithoutPortsOrSecrets(t *testing.T) {
	app := fixture(t)
	k := *app.Kubernetes
	k.Ports, k.ReadinessPath, k.Volumes = nil, "", nil
	app.Kubernetes, app.Secrets = &k, nil
	o := renderHello(app, "rs-1", "")
	if o.Service != nil || len(o.Claims) != 0 || o.Deployment.Spec.Template.Spec.Containers[0].ReadinessProbe != nil {
		t.Fatalf("objects = %+v", o)
	}
	for _, v := range o.Deployment.Spec.Template.Spec.Volumes {
		if v.Secret != nil {
			t.Fatal("secret volume without secrets")
		}
	}
}

func TestRenderStorageClass(t *testing.T) {
	unset := renderHello(fixture(t), "rs-1", "")
	set := renderHello(fixture(t), "rs-1", "local-path")
	if len(set.Claims) == 0 {
		t.Fatal("fixture has no volumes")
	}
	for i, c := range set.Claims {
		if c.Spec.StorageClassName == nil || *c.Spec.StorageClassName != "local-path" {
			t.Errorf("claim %s class = %v", c.Name, c.Spec.StorageClassName)
		}
		if unset.Claims[i].Spec.StorageClassName != nil {
			t.Errorf("unset claim %s has a class", c.Name)
		}
	}
	// Unset must hash exactly as before the field existed, so existing installs do not redeploy.
	if b, _ := json.Marshal(unset); strings.Contains(string(b), "storageClassName") {
		t.Error("unset class reaches the hashed JSON")
	}
	if set.hash() == unset.hash() {
		t.Error("storage class does not change the hash")
	}
}

func TestRenderCPURequestBelowLimit(t *testing.T) {
	c := fixture(t)
	k := *c.Kubernetes
	k.Resources.CPURequest = "10m"
	c.Kubernetes = &k
	res := renderHello(c, "rs-1", "").Deployment.Spec.Template.Spec.Containers[0].Resources
	if res.Requests.Cpu().String() != "10m" || res.Limits.Cpu().String() != "50m" || res.Requests.Memory().Cmp(*res.Limits.Memory()) != 0 {
		t.Errorf("resources = %+v, want cpu request 10m under a 50m limit and memory request = limit", res)
	}
	if renderHello(fixture(t), "rs-1", "").Deployment.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String() != "50m" {
		t.Error("an app without cpu_request no longer requests its limit")
	}
}

const testRunnerImage = "ghcr.io/busnes-app/kyquickstart-runner:9.9.9@sha256:1111111111111111111111111111111111111111111111111111111111111111"

// AC3: KyYard runs restricted beside the suite runner, which shares only the socket directory.
func TestRenderKyYard(t *testing.T) {
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	c := cat["kyyard"]
	c.Kubernetes = c.Kubernetes.WithHost("yard.example.com")
	a := App{Name: "kyyard", Catalog: c, RunnerImage: testRunnerImage, Edge: "npm"}
	o := render(a)
	pod := o.Deployment.Spec.Template.Spec
	if len(pod.Containers) != 2 || pod.Containers[0].Name != "app" || pod.Containers[1].Name != "runner" {
		t.Fatalf("containers = %+v", pod.Containers)
	}
	if *pod.AutomountServiceAccountToken {
		t.Fatal("service-account token mounted")
	}
	if pod.TerminationGracePeriodSeconds == nil || *pod.TerminationGracePeriodSeconds != 1200 {
		t.Fatalf("grace = %v", pod.TerminationGracePeriodSeconds)
	}
	for _, ctr := range pod.Containers {
		sc := ctr.SecurityContext
		if *sc.RunAsUser != 10001 || *sc.RunAsGroup != 10001 || !*sc.RunAsNonRoot || !*sc.ReadOnlyRootFilesystem || *sc.AllowPrivilegeEscalation ||
			!slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) || len(sc.Capabilities.Add) != 0 || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
			t.Fatalf("%s security context = %+v", ctr.Name, sc)
		}
	}
	mounts := func(ctr corev1.Container) map[string]string {
		m := map[string]string{}
		for _, v := range ctr.VolumeMounts {
			m[v.MountPath] = v.Name + ":" + v.SubPath
		}
		return m
	}
	app, runner := pod.Containers[0], pod.Containers[1]
	if got := mounts(runner); len(got) != 2 || got["/state"] != "runner-state:" || got["/run/kyquickstart"] != "runner-socket:" {
		t.Fatalf("runner mounts = %v", got)
	}
	if runner.Image != testRunnerImage || len(runner.Args) != 0 ||
		!slices.Equal(runner.Command, []string{"kyquickstart", "serve", "--state", "/state/runner", "--socket", "/run/kyquickstart/sock/runner.sock", "--pod-volumes"}) {
		t.Fatalf("runner = %s %v %v", runner.Image, runner.Command, runner.Args)
	}
	if r := runner.Resources; r.Requests.Cpu().String() != "50m" || r.Requests.Memory().String() != "64Mi" || r.Limits.Memory().String() != "512Mi" || !r.Limits.Cpu().IsZero() {
		t.Fatalf("runner resources = %+v", r)
	}
	if got := mounts(app); len(got) != 3 || got["/data"] != "vol-data:private" || got["/tmp"] != "tmp:" || got["/run/kyquickstart/sock"] != "runner-socket:sock" {
		t.Fatalf("app mounts = %v", got)
	}
	env := map[string]string{}
	for _, e := range app.Env {
		env[e.Name] = e.Value
	}
	if env["KY_SUITE_RUNNER_SOCKET"] != "/run/kyquickstart/sock/runner.sock" || env["KY_APP_URL"] != "https://yard.example.com" {
		t.Fatalf("app env = %v", env)
	}
	if p := app.ReadinessProbe; p == nil || p.HTTPGet.Path != "/health/ready" || p.HTTPGet.Port.IntValue() != 9273 || runner.ReadinessProbe != nil {
		t.Fatalf("readiness = %+v", p)
	}
	vols := map[string]corev1.Volume{}
	for _, v := range pod.Volumes {
		vols[v.Name] = v
	}
	if v := vols["runner-socket"]; v.EmptyDir == nil {
		t.Fatalf("socket volume = %+v", v)
	}
	if v := vols["runner-state"]; v.PersistentVolumeClaim == nil || v.PersistentVolumeClaim.ClaimName != "runner-state" {
		t.Fatalf("state volume = %+v", v)
	}
	claims := map[string]string{}
	for _, c := range o.Claims {
		claims[c.Name] = c.Spec.Resources.Requests.Storage().String()
	}
	if len(claims) != 2 || claims["vol-data"] != "10Gi" || claims["runner-state"] != "1Gi" {
		t.Fatalf("claims = %v", claims)
	}
	if init := pod.InitContainers; len(init) != 1 || !slices.Equal(init[0].Command, []string{"mkdir", "-p", "-m", "700", "/volumes/data/private", "/volumes/runner-socket/sock"}) ||
		len(init[0].VolumeMounts) != 2 || init[0].VolumeMounts[1].Name != "runner-socket" {
		t.Fatalf("init = %+v", init)
	}
	if a.namespaceLabels()[podSecurity] != "restricted" {
		t.Fatal("namespace not restricted")
	}
	b := a
	b.RunnerImage = strings.Replace(testRunnerImage, "1111", "2222", 1)
	if render(b).hash() == o.hash() {
		t.Fatal("runner image change keeps the deployment hash")
	}
}

// Euro-Office: root under baseline with its measured capabilities, both claims seeded from the
// image by a root init container (no private subdirectory), its secrets where its entrypoint
// reads them, and egress to cluster DNS and the edge's pods only.
func TestRenderEuroOffice(t *testing.T) {
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	o := render(App{Name: "euro-office", Catalog: cat["euro-office"], ReleaseSet: "rs-1", Edge: "npm"})
	if (App{Catalog: cat["euro-office"]}).namespaceLabels()[podSecurity] != "baseline" {
		t.Fatal("euro-office namespace is not baseline")
	}
	spec := o.Deployment.Spec.Template.Spec
	c := spec.Containers[0]
	sc := c.SecurityContext
	if *sc.RunAsUser != 0 || *sc.ReadOnlyRootFilesystem || *sc.AllowPrivilegeEscalation || fmt.Sprint(sc.Capabilities.Drop, sc.Capabilities.Add) != "[ALL] [CHOWN DAC_OVERRIDE FOWNER SETGID SETUID KILL]" {
		t.Fatalf("security context = %+v", sc)
	}
	if spec.SecurityContext.FSGroup != nil || spec.SecurityContext.Sysctls[0].Name != "net.ipv4.ip_unprivileged_port_start" {
		t.Fatalf("pod security context = %+v", spec.SecurityContext)
	}
	mounts := map[string]corev1.VolumeMount{}
	for _, m := range c.VolumeMounts {
		mounts[m.MountPath] = m
	}
	if m := mounts["/var/www/euro-office/Data/.private"]; m.Name != "secrets" || !m.ReadOnly {
		t.Fatalf("secrets mount = %+v", c.VolumeMounts)
	}
	if _, ok := mounts["/run/secrets"]; ok {
		t.Fatal("secrets also mounted at /run/secrets")
	}
	for path, claim := range map[string]string{"/var/lib/postgresql": "vol-database", "/var/lib/euro-office": "vol-cache"} {
		if m := mounts[path]; m.Name != claim || m.SubPath != "" {
			t.Fatalf("%s mount = %+v", path, m)
		}
	}
	if len(spec.InitContainers) != 1 {
		t.Fatalf("init containers = %+v", spec.InitContainers)
	}
	seed := spec.InitContainers[0]
	want := "test -e /seed/database/.kyq-seeded || { cp -a /var/lib/postgresql/. /seed/database/; chown --reference=/var/lib/postgresql /seed/database; chmod --reference=/var/lib/postgresql /seed/database; touch /seed/database/.kyq-seeded; }\n" +
		"test -e /seed/cache/.kyq-seeded || { cp -a /var/lib/euro-office/. /seed/cache/; chown --reference=/var/lib/euro-office /seed/cache; chmod --reference=/var/lib/euro-office /seed/cache; touch /seed/cache/.kyq-seeded; }\n"
	if seed.Image != c.Image || seed.SecurityContext != c.SecurityContext || fmt.Sprint(seed.Command[:2]) != "[/bin/sh -ec]" || seed.Command[2] != want {
		t.Fatalf("seed = %+v", seed)
	}
	if len(seed.VolumeMounts) != 2 || seed.VolumeMounts[0].MountPath != "/seed/database" || seed.VolumeMounts[1].Name != "vol-cache" {
		t.Fatalf("seed mounts = %+v", seed.VolumeMounts)
	}
	p := o.Policy.Spec
	if len(p.PolicyTypes) != 2 || len(p.Egress) != 2 || len(p.Egress[0].Ports) != 2 || p.Egress[0].To[0].PodSelector.MatchLabels["k8s-app"] != "kube-dns" ||
		len(p.Egress[1].Ports) != 0 || p.Egress[1].To[0].NamespaceSelector.MatchLabels[nsNameLabel] != "kyq-npm" || !maps.Equal(p.Egress[1].To[0].PodSelector.MatchLabels, selector("npm")) {
		t.Fatalf("policy = %+v", p)
	}
	// Without an edge on the cluster, only DNS.
	if p := render(App{Name: "euro-office", Catalog: cat["euro-office"], ReleaseSet: "rs-1"}).Policy.Spec; len(p.Egress) != 1 || len(p.Ingress) != 0 {
		t.Fatalf("edgeless policy = %+v", p)
	}
}
