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
