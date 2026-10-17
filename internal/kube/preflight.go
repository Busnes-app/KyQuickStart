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
	// patch is unmanage's; delete of deployments and services is uninstall's.
	add("", "namespaces", "", "get", "create", "update", "patch")
	add("", "pods", "", "get", "list", "create", "delete")
	add("", "pods/exec", "", "create")
	add("", "pods/portforward", "", "create") // the in-cluster NPM's admin API
	add("", "secrets", "", "get", "create", "update", "patch")
	add("", "services", "", "get", "list", "create", "update", "patch", "delete") // list: preflight's NodePorts
	add("", "persistentvolumeclaims", "", "get", "create", "patch")
	add("apps", "deployments", "", "get", "create", "update", "patch", "delete")
	add("networking.k8s.io", "networkpolicies", "", "get", "create", "update", "patch")
	add("storage.k8s.io", "storageclasses", "", "list")
	add("coordination.k8s.io", "leases", lockNamespace, "get", "create", "delete")
	return out
}()

// Preflight checks a cluster and changes nothing. A named storageClass must exist; with none,
// claims need a default class. nodePorts maps each NodePort the plan's apps publish to its app.
func Preflight(ctx context.Context, c *Client, needsStorage bool, storageClass string, nodePorts map[int]string) []Finding {
	out := []Finding{c.versionCheck(), c.accessCheck(ctx)}
	if needsStorage {
		out = append(out, c.storageCheck(ctx, storageClass))
	}
	if len(nodePorts) > 0 {
		out = append(out, c.nodePortCheck(ctx, nodePorts))
	}
	return out
}

// nodePortCheck refuses a wanted NodePort held by any Service but its app's own, which a
// rerun finds in place.
func (c *Client) nodePortCheck(ctx context.Context, want map[int]string) Finding {
	list, err := c.cs.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return Finding{"nodeports", false, err.Error()}
	}
	var held []string
	for _, svc := range list.Items {
		for _, p := range svc.Spec.Ports {
			app, ok := want[int(p.NodePort)]
			if ok && !(svc.Namespace == namespaceOf(app) && svc.Name == publicName && owned(&svc)) {
				held = append(held, fmt.Sprintf("%d (for %s) is held by Service %s/%s", p.NodePort, app, svc.Namespace, svc.Name))
			}
		}
	}
	if len(held) > 0 {
		return Finding{"nodeports", false, strings.Join(held, "; ") + "; set the app's node_ports in stack.yaml"}
	}
	return Finding{"nodeports", true, fmt.Sprintf("%d free", len(want))}
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
	denied, err := c.denied(ctx, required)
	if err != nil {
		return Finding{"permissions", false, err.Error()}
	}
	if len(denied) > 0 {
		return Finding{"permissions", false, "denied: " + strings.Join(denied, ", ")}
	}
	return Finding{"permissions", true, fmt.Sprintf("%d checks allowed", len(required))}
}

// denied returns the attributes this client may not use, as "verb resource".
func (c *Client) denied(ctx context.Context, attrs []authorizationv1.ResourceAttributes) ([]string, error) {
	var out []string
	for _, a := range attrs {
		r, err := c.cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx,
			&authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &a}},
			metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}
		if !r.Status.Allowed {
			out = append(out, strings.TrimSuffix(a.Verb+" "+a.Resource+"/"+a.Subresource, "/"))
		}
	}
	return out, nil
}

func (c *Client) storageCheck(ctx context.Context, named string) Finding {
	list, err := c.cs.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Finding{"storage", false, err.Error()}
	}
	if named != "" {
		for _, sc := range list.Items {
			if sc.Name == named {
				return Finding{"storage", true, "StorageClass " + named}
			}
		}
		return Finding{"storage", false, "StorageClass " + named + " named by storage_class does not exist"}
	}
	for _, sc := range list.Items {
		if sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			return Finding{"storage", true, "default StorageClass " + sc.Name}
		}
	}
	return Finding{"storage", false, "no default StorageClass; an app here needs a volume"}
}
