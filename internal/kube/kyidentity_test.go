package kube

import (
	"context"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// kyidentityApp is the catalog entry with the hostname the session takes from its route.
func kyidentityApp(t *testing.T) catalog.App {
	t.Helper()
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	app := cat["kyidentity"]
	app.Kubernetes = app.Kubernetes.WithHost("identity.example.com")
	return app
}

func TestRenderKyIdentity(t *testing.T) {
	o := render(App{Name: "kyidentity", Catalog: kyidentityApp(t), ReleaseSet: "rs-1", StorageClass: "local-path", Edge: "npm"})
	pod := o.Deployment.Spec.Template.Spec
	c := pod.Containers[0]
	sc := c.SecurityContext
	if *sc.RunAsUser != 100 || !*sc.RunAsNonRoot || !*sc.ReadOnlyRootFilesystem || *sc.AllowPrivilegeEscalation ||
		sc.Capabilities.Drop[0] != "ALL" || len(sc.Capabilities.Add) != 0 || *pod.SecurityContext.FSGroup != 100 {
		t.Fatalf("security context = %+v / %+v", sc, pod.SecurityContext)
	}
	if (App{Catalog: kyidentityApp(t)}).namespaceLabels()[podSecurity] != "restricted" {
		t.Fatal("namespace is not restricted")
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["KYIDENTITY_ISSUER_URL"] != "https://identity.example.com" || env["KYIDENTITY_DATA_DIR"] != "/data" || env["BOOTSTRAP_ADMIN_USER"] != "kyq-bootstrap" || env["BOOTSTRAP_ADMIN_PASS"] != "" {
		t.Fatalf("env = %v", env)
	}
	if p := c.ReadinessProbe.HTTPGet; p.Path != "/readyz" || p.Port.IntValue() != 5867 {
		t.Fatalf("probe = %+v", p)
	}
	var data bool
	for _, m := range c.VolumeMounts {
		data = data || m.MountPath == "/data" && m.Name == "vol-data" && m.SubPath == privateDir
	}
	if !data || len(pod.InitContainers) != 1 || len(o.Claims) != 1 || *o.Claims[0].Spec.StorageClassName != "local-path" {
		t.Fatalf("data volume: mounts %+v, init %d, claims %d", c.VolumeMounts, len(pod.InitContainers), len(o.Claims))
	}
	if o.Service == nil || o.Service.Spec.Ports[0].Port != 5867 || o.Public != nil || len(o.Policy.Spec.Ingress) != 1 {
		t.Fatalf("service %+v, ingress %+v", o.Service, o.Policy.Spec.Ingress)
	}
}

func TestDeployKyIdentity(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	d := deployOf(App{Name: "kyidentity", Client: New(cs), Catalog: kyidentityApp(t), ReleaseSet: "rs-1"})
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	dep, err := cs.AppsV1().Deployments("kyq-kyidentity").Get(ctx, appName, metav1.GetOptions{})
	if err != nil || dep.Spec.Template.Spec.Containers[0].Image != kyidentityApp(t).Images["kyidentity"] {
		t.Fatalf("deployment: %v", err)
	}
	if ns, _ := cs.CoreV1().Namespaces().Get(ctx, "kyq-kyidentity", metav1.GetOptions{}); ns.Labels[podSecurity] != "restricted" {
		t.Fatalf("namespace labels = %v", ns.Labels)
	}
	// The driver's steps end at health; apply adds the setup step from the plan's integration.
	if steps := Steps(App{Name: "kyidentity", Catalog: kyidentityApp(t)}); len(steps) != 3 || steps[2].ID() != "kyidentity.health" {
		t.Fatalf("%d steps, last %s", len(steps), steps[len(steps)-1].ID())
	}
}
