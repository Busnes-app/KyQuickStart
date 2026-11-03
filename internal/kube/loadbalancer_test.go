package kube

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/managed"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

var lbKinds = map[string]string{
	"ipaddresspools": "IPAddressPool", "l2advertisements": "L2Advertisement", "bgpadvertisements": "BGPAdvertisement",
	"ciliumloadbalancerippools": "CiliumLoadBalancerIPPool", "ciliuml2announcementpolicies": "CiliumL2AnnouncementPolicy",
}

// lbCluster serves the given implementation's resources (group/version -> resource -> objects'
// specs) through discovery and a fake dynamic client.
func lbCluster(t *testing.T, deny string, resources map[string]map[string][]map[string]any) *Client {
	t.Helper()
	cs := cluster("37", deny)
	disc := cs.Discovery().(*fakediscovery.FakeDiscovery)
	lists := map[schema.GroupVersionResource]string{}
	var objs []runtime.Object
	for gv, rs := range resources {
		l := &metav1.APIResourceList{GroupVersion: gv}
		g, _ := schema.ParseGroupVersion(gv)
		for r, specs := range rs {
			l.APIResources = append(l.APIResources, metav1.APIResource{Name: r, Kind: r})
			lists[g.WithResource(r)] = lbKinds[r] + "List"
			for i, spec := range specs {
				u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
				u.SetAPIVersion(gv)
				u.SetKind(lbKinds[r]) // the fake client derives the resource from the kind
				u.SetName(r + string(rune('a'+i)))
				objs = append(objs, u)
			}
		}
		disc.Resources = append(disc.Resources, l)
	}
	c := New(cs)
	c.dyn = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists, objs...)
	return c
}

func metalLB(addresses []any, announce bool) map[string]map[string][]map[string]any {
	rs := map[string][]map[string]any{"ipaddresspools": {{"addresses": addresses}}, "l2advertisements": nil, "bgpadvertisements": nil}
	if announce {
		rs["l2advertisements"] = []map[string]any{{}}
	}
	return map[string]map[string][]map[string]any{"metallb.io/v1beta1": rs}
}

func TestLoadBalancerCheck(t *testing.T) {
	ctx := context.Background()
	want := map[string]string{"192.168.1.80": "npm"}
	cilium := map[string]map[string][]map[string]any{"cilium.io/v2alpha1": {
		"ciliumloadbalancerippools":    {{"blocks": []any{map[string]any{"start": "192.168.1.70", "stop": "192.168.1.90"}}}, {"disabled": true, "blocks": []any{map[string]any{"cidr": "192.168.2.0/24"}}}},
		"ciliuml2announcementpolicies": {{}},
	}}
	for name, c := range map[string]struct {
		client *Client
		ok     bool
		detail string
	}{
		"MetalLB CIDR":           {lbCluster(t, "", metalLB([]any{"192.168.1.80/32"}, true)), true, "192.168.1.80 from MetalLB"},
		"MetalLB range":          {lbCluster(t, "", metalLB([]any{"192.168.1.75-192.168.1.85"}, true)), true, "MetalLB"},
		"Cilium start/stop":      {lbCluster(t, "", cilium), true, "192.168.1.80 from Cilium LB IPAM"},
		"no implementation":      {lbCluster(t, "", nil), false, "no load-balancer implementation"},
		"address in no pool":     {lbCluster(t, "", metalLB([]any{"10.0.0.0/24"}, true)), false, "192.168.1.80 (for npm) is in no enabled MetalLB address pool"},
		"nothing announces":      {lbCluster(t, "", metalLB([]any{"192.168.1.0/24"}, false)), false, "nothing announces it"},
		"pools cannot be listed": {lbCluster(t, "list ipaddresspools", metalLB([]any{"192.168.1.0/24"}, true)), false, "denied: list ipaddresspools"},
		"Cilium disabled pool":   {lbCluster(t, "", map[string]map[string][]map[string]any{"cilium.io/v2": {"ciliumloadbalancerippools": {{"disabled": true, "blocks": []any{map[string]any{"cidr": "192.168.1.0/24"}}}}, "ciliuml2announcementpolicies": {{}}}}), false, "no enabled Cilium LB IPAM"},
	} {
		f := c.client.LoadBalancerCheck(ctx, want)
		if f.OK != c.ok || !strings.Contains(f.Detail, c.detail) || f.Check != "loadbalancer" {
			t.Errorf("%s: %+v, want ok=%v and %q", name, f, c.ok, c.detail)
		}
	}

	if f := lbCluster(t, "", metalLB([]any{"192.168.1.0/24"}, false)).LoadBalancerCheck(ctx, want); strings.Contains(f.Detail, "in no enabled") {
		t.Errorf("an unannounced pool's address reported as in no pool: %+v", f)
	}

	// Another Service holding or requesting the address is refused; this app's own is not.
	c := lbCluster(t, "", metalLB([]any{"192.168.1.0/24"}, true))
	svcs := c.cs.(*fake.Clientset).CoreV1()
	own := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: publicName, Namespace: "kyq-npm", Labels: map[string]string{managed.Label: managed.Value}, Annotations: loadBalancerAnnotations("192.168.1.80")}}
	svcs.Services("kyq-npm").Create(ctx, own, metav1.CreateOptions{})
	if f := c.LoadBalancerCheck(ctx, want); !f.OK {
		t.Errorf("its own Service refused: %+v", f)
	}
	other := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer}}
	other.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "192.168.1.80"}}
	svcs.Services("shop").Create(ctx, other, metav1.CreateOptions{})
	if f := c.LoadBalancerCheck(ctx, want); f.OK || !strings.Contains(f.Detail, "held by Service shop/web") {
		t.Errorf("held address accepted: %+v", f)
	}
}

// load_balancer_address makes app-public a LoadBalancer asking Cilium and MetalLB for the
// address; applies keep what MetalLB writes, and moving from kube-vip drops kube-vip's request.
func TestDeployLoadBalancerAddress(t *testing.T) {
	ctx := context.Background()
	client := New(fake.NewClientset())
	with := func(vip, lb string) App {
		c := npmApp(t)
		k := *c.Kubernetes
		k.KubeVIPAddress, k.LoadBalancerAddress = vip, lb
		c.Kubernetes = &k
		return App{Name: "npm", Client: client, Catalog: c, ReleaseSet: "rs-1"}
	}
	if err := deployOf(with("192.168.1.78", "")).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	svcs := client.cs.CoreV1().Services("kyq-npm")
	d := deployOf(with("", "192.168.1.80"))
	if done, _ := d.Inspect(ctx); done {
		t.Fatal("kube-vip Service counted as the load-balancer one")
	}
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	pub, _ := svcs.Get(ctx, publicName, metav1.GetOptions{})
	want := map[string]string{"metallb.io/loadBalancerIPs": "192.168.1.80", "lbipam.cilium.io/ips": "192.168.1.80"}
	if pub.Spec.Type != corev1.ServiceTypeLoadBalancer || pub.Spec.ExternalTrafficPolicy != corev1.ServiceExternalTrafficPolicyLocal || !maps.Equal(pub.Annotations, want) {
		t.Fatalf("public service = %v %v %v", pub.Spec.Type, pub.Spec.ExternalTrafficPolicy, pub.Annotations)
	}
	ports := map[int32]int32{}
	for _, p := range pub.Spec.Ports {
		ports[p.Port] = p.NodePort
	}
	if ports[80] != 30080 || ports[443] != 30443 {
		t.Fatalf("ports = %v: the address serves 80 and 443", ports)
	}
	pub.Annotations["metallb.io/ip-allocated-from-pool"] = "lan"
	svcs.Update(ctx, pub, metav1.UpdateOptions{})
	if done, err := d.Inspect(ctx); err != nil || !done {
		t.Fatalf("MetalLB state counted as drift: %v %v", done, err)
	}
	if err := d.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if pub, _ = svcs.Get(ctx, publicName, metav1.GetOptions{}); pub.Annotations["metallb.io/ip-allocated-from-pool"] != "lan" {
		t.Fatalf("apply dropped MetalLB state: %v", pub.Annotations)
	}
	delete(pub.Annotations, "lbipam.cilium.io/ips")
	svcs.Update(ctx, pub, metav1.UpdateOptions{})
	if done, _ := d.Inspect(ctx); done {
		t.Fatal("a removed address request counted as deployed")
	}
}
