package kube

import (
	"encoding/json"
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
