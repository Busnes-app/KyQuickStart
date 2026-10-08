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
