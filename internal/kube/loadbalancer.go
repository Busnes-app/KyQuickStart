package kube

import (
	"context"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// An app's load_balancer_address asks the cluster's own load-balancer implementation, Cilium
// LB IPAM or MetalLB, for one fixed address. Each reads only its own annotation.
const (
	metalLBIPs   = "metallb.io/loadBalancerIPs"
	ciliumLBIPs  = "lbipam.cilium.io/ips"
	metalLBGroup = "metallb.io"
	ciliumGroup  = "cilium.io"
)

func loadBalancerAnnotations(address string) map[string]string {
	return map[string]string{metalLBIPs: address, ciliumLBIPs: address}
}

// lbImpl is one load-balancer implementation preflight recognizes: the resource holding its
// address pools, and the resources that announce a pool's addresses (any one is enough).
type lbImpl struct {
	name     string
	group    string
	pools    string
	announce []string
}

var lbImpls = []lbImpl{
	{"MetalLB", metalLBGroup, "ipaddresspools", []string{"l2advertisements", "bgpadvertisements"}},
	{"Cilium LB IPAM", ciliumGroup, "ciliumloadbalancerippools", []string{"ciliuml2announcementpolicies", "ciliumbgpadvertisements", "ciliumbgppeeringpolicies"}},
}

// loadBalancerRead is what LoadBalancerCheck reads: only a plan with a load_balancer_address
// needs it, so it is not in required.
var loadBalancerRead = func() []authorizationv1.ResourceAttributes {
	var out []authorizationv1.ResourceAttributes
	for _, l := range lbImpls {
		for _, r := range append([]string{l.pools}, l.announce...) {
			out = append(out, authorizationv1.ResourceAttributes{Group: l.group, Resource: r, Verb: "list"})
		}
	}
	return out
}()

// announces reports whether adv announces pool. A MetalLB advertisement selects pools by name
// or label, every pool when it names none; Cilium's select Services, so any one counts.
func announces(group string, adv, pool unstructured.Unstructured) bool {
	if group != metalLBGroup {
		return true
	}
	names, _, _ := unstructured.NestedStringSlice(adv.Object, "spec", "ipAddressPools")
	selectors, _, _ := unstructured.NestedSlice(adv.Object, "spec", "ipAddressPoolSelectors")
	if len(names) == 0 && len(selectors) == 0 || slices.Contains(names, pool.GetName()) {
		return true
	}
	for _, raw := range selectors {
		m, _ := raw.(map[string]any)
		var ls metav1.LabelSelector
		if runtime.DefaultUnstructuredConverter.FromUnstructured(m, &ls) != nil {
			continue
		}
		if sel, err := metav1.LabelSelectorAsSelector(&ls); err == nil && sel.Matches(labels.Set(pool.GetLabels())) {
			return true
		}
	}
	return false
}

// LoadBalancerCheck refuses a load_balancer_address (address -> app) the cluster cannot
// assign: no Cilium LB IPAM or MetalLB, no enabled pool holding it, nothing announcing it, or
// another Service already holding or requesting it. It changes nothing.
func (c *Client) LoadBalancerCheck(ctx context.Context, want map[string]string) Finding {
	fail := func(f string, a ...any) Finding { return Finding{"loadbalancer", false, fmt.Sprintf(f, a...)} }
	denied, err := c.denied(ctx, loadBalancerRead)
	if err != nil {
		return fail("%v", err)
	}
	if len(denied) > 0 {
		return fail("denied: %s (load_balancer_address checks the cluster's address pools)", strings.Join(denied, ", "))
	}
	var found []string
	var missing []string
	held := map[string]string{} // address -> the implementation whose announced pool holds it
	pooled := map[string]bool{} // address -> some pool holds it, announced or not
	for _, l := range lbImpls {
		gv, err := c.groupVersion(l.group, l.pools)
		if err != nil {
			return fail("%v", err)
		}
		if gv.Empty() {
			continue
		}
		found = append(found, l.name)
		pools, err := c.list(ctx, gv.WithResource(l.pools))
		if err != nil {
			return fail("%s: %v", l.name, err)
		}
		var advs []unstructured.Unstructured
		for _, r := range l.announce {
			agv, err := c.groupVersion(l.group, r)
			if err != nil {
				return fail("%v", err)
			}
			if agv.Empty() {
				continue
			}
			items, err := c.list(ctx, agv.WithResource(r))
			if err != nil {
				return fail("%s: %v", l.name, err)
			}
			advs = append(advs, items...)
		}
		for addr := range want {
			holds := slices.DeleteFunc(slices.Clone(pools), func(p unstructured.Unstructured) bool { return !poolHolds(p, addr) })
			if len(holds) == 0 {
				continue
			}
			pooled[addr] = true
			if !slices.ContainsFunc(holds, func(p unstructured.Unstructured) bool {
				return slices.ContainsFunc(advs, func(a unstructured.Unstructured) bool { return announces(l.group, a, p) })
			}) {
				missing = append(missing, fmt.Sprintf("%s's pool holds %s but nothing announces it (%s)", l.name, addr, strings.Join(l.announce, ", ")))
				continue
			}
			held[addr] = l.name
		}
	}
	if len(found) == 0 {
		return fail("the cluster has no load-balancer implementation this installer can check (Cilium LB IPAM with L2 announcements or BGP, or MetalLB); remove load_balancer_address, or use kube_vip_address with kube-vip")
	}
	svcs, err := c.cs.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fail("%v", err)
	}
	var problems []string
	for _, addr := range slices.Sorted(maps.Keys(want)) {
		app := want[addr]
		if !pooled[addr] {
			problems = append(problems, fmt.Sprintf("%s (for %s) is in no enabled %s address pool", addr, app, strings.Join(found, " or ")))
		}
		for _, svc := range svcs.Items {
			if svc.Namespace == namespaceOf(app) && svc.Name == publicName && owned(&svc) {
				continue
			}
			if serviceWants(&svc, addr) {
				problems = append(problems, fmt.Sprintf("%s (for %s) is held by Service %s/%s", addr, app, svc.Namespace, svc.Name))
			}
		}
	}
	problems = append(missing, problems...)
	if len(problems) > 0 {
		return fail("%s", strings.Join(problems, "; "))
	}
	var ok []string
	for _, addr := range slices.Sorted(maps.Keys(held)) {
		ok = append(ok, addr+" from "+held[addr])
	}
	return Finding{"loadbalancer", true, strings.Join(ok, ", ")}
}

// groupVersion is the version the API server prefers for group that serves resource, or the
// empty GroupVersion when it serves no such resource.
func (c *Client) groupVersion(group, resource string) (schema.GroupVersion, error) {
	groups, err := c.cs.Discovery().ServerGroups()
	if err != nil {
		return schema.GroupVersion{}, fmt.Errorf("API discovery: %w", err)
	}
	for _, g := range groups.Groups {
		if g.Name != group {
			continue
		}
		versions := []string{g.PreferredVersion.GroupVersion}
		for _, v := range g.Versions {
			versions = append(versions, v.GroupVersion)
		}
		for _, v := range versions {
			list, err := c.cs.Discovery().ServerResourcesForGroupVersion(v)
			if err != nil {
				continue
			}
			for _, r := range list.APIResources {
				if r.Name == resource {
					return schema.ParseGroupVersion(v)
				}
			}
		}
	}
	return schema.GroupVersion{}, nil
}

func (c *Client) list(ctx context.Context, r schema.GroupVersionResource) ([]unstructured.Unstructured, error) {
	if c.dyn == nil {
		return nil, fmt.Errorf("no dynamic client to read %s", r.Resource)
	}
	l, err := c.dyn.Resource(r).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", r.Resource, err)
	}
	return l.Items, nil
}

// poolHolds reports whether a MetalLB IPAddressPool (spec.addresses: CIDRs or "a-b" ranges)
// or an enabled Cilium pool (spec.blocks: cidr, or start and stop) contains addr.
func poolHolds(p unstructured.Unstructured, addr string) bool {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	if off, _, _ := unstructured.NestedBool(p.Object, "spec", "disabled"); off {
		return false
	}
	var ranges []string
	addrs, _, _ := unstructured.NestedStringSlice(p.Object, "spec", "addresses")
	ranges = append(ranges, addrs...)
	blocks, _, _ := unstructured.NestedSlice(p.Object, "spec", "blocks")
	for _, b := range blocks {
		m, _ := b.(map[string]any)
		cidr, _ := m["cidr"].(string)
		start, _ := m["start"].(string)
		stop, _ := m["stop"].(string)
		switch {
		case cidr != "":
			ranges = append(ranges, cidr)
		case start != "" && stop != "":
			ranges = append(ranges, start+"-"+stop)
		case start != "":
			ranges = append(ranges, start+"-"+start)
		}
	}
	for _, r := range ranges {
		r = strings.TrimSpace(r)
		if from, to, ok := strings.Cut(r, "-"); ok {
			lo, err1 := netip.ParseAddr(strings.TrimSpace(from))
			hi, err2 := netip.ParseAddr(strings.TrimSpace(to))
			if err1 == nil && err2 == nil && lo.Compare(ip) <= 0 && ip.Compare(hi) <= 0 {
				return true
			}
			continue
		}
		if pfx, err := netip.ParsePrefix(r); err == nil && pfx.Contains(ip) {
			return true
		}
	}
	return false
}

// serviceWants reports whether a Service holds addr or asks a load balancer for it.
func serviceWants(s *corev1.Service, addr string) bool {
	if s.Spec.LoadBalancerIP == addr || slices.Contains(s.Spec.ExternalIPs, addr) {
		return true
	}
	for _, in := range s.Status.LoadBalancer.Ingress {
		if in.IP == addr {
			return true
		}
	}
	for _, k := range []string{metalLBIPs, "metallb.universe.tf/loadBalancerIPs", ciliumLBIPs, "io.cilium/lb-ipam-ips", kubeVIPPrefix + "loadbalancerIPs"} {
		for _, a := range strings.Split(s.Annotations[k], ",") {
			if strings.TrimSpace(a) == addr {
				return true
			}
		}
	}
	return false
}
