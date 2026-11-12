package kube

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// The nginx edge (internal/edge/nginx.go) in its cluster: *Client is its edge.Cluster.
var _ edge.Cluster = (*Client)(nil)

const (
	certManagerGroup = "cert-manager.io"
	// hastenAnnotation changes on an edge pod to make its kubelet re-read the pod's volumes.
	hastenAnnotation = "kyquickstart/edge-generation"
)

var edgeNS = namespaceOf(stack.NginxApp)

// edgeProxyAccess is what a plan with the nginx edge adds to required: its DaemonSet and
// ConfigMap, the annotation that hastens a pod's volumes, and the nodes its health reads.
// certificateAccess is what its managed certificates add.
var edgeProxyAccess, certificateAccess = func() (proxy, certs []authorizationv1.ResourceAttributes) {
	add := func(out *[]authorizationv1.ResourceAttributes, group, resource string, verbs ...string) {
		for _, v := range verbs {
			*out = append(*out, authorizationv1.ResourceAttributes{Group: group, Resource: resource, Verb: v})
		}
	}
	add(&proxy, "apps", "daemonsets", "get", "create", "update", "patch", "delete")
	add(&proxy, "", "configmaps", "get", "create", "update", "patch")
	add(&proxy, "", "pods", "patch")
	add(&proxy, "", "nodes", "list")
	add(&certs, certManagerGroup, "issuers", "get", "create", "update")
	add(&certs, certManagerGroup, "certificates", "get", "create", "update")
	return proxy, certs
}()

// ensureEdge creates what the nginx edge's pods and steps need before the DaemonSet: the
// edge credential namespace with its Secret (the LAN DNS ledger), and the ConfigMap with this
// release's main file. Route files in it are the route steps' and are never touched here.
func (c *Client) ensureEdge(ctx context.Context, release string, want *corev1.ConfigMap) error {
	if err := c.setEdgeCredential(ctx, release, "", nil); err != nil {
		return err
	}
	api := c.cs.CoreV1().ConfigMaps(want.Namespace)
	have, found, err := getOwned(ctx, api, "config map", want.Name)
	switch {
	case err != nil:
		return err
	case !found:
		_, err = api.Create(ctx, want, metav1.CreateOptions{})
	case have.Data[edge.NginxMainFile] != want.Data[edge.NginxMainFile]:
		if have.Data == nil {
			have.Data = map[string]string{}
		}
		maps.Copy(have.Data, want.Data)
		_, err = api.Update(ctx, have, metav1.UpdateOptions{})
	}
	return edgeWrite("config map "+want.Name, err)
}

// edgeWrite makes a lost race transient, so the engine reads and tries again.
func edgeWrite(what string, err error) error {
	switch {
	case apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err):
		return fmt.Errorf("%s: %w: %w", what, engine.ErrTransient, err)
	case err != nil:
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

func (c *Client) edgeConfig(ctx context.Context) (*corev1.ConfigMap, error) {
	cm, found, err := getOwned(ctx, c.cs.CoreV1().ConfigMaps(edgeNS), "config map", edgeConfig)
	if err == nil && !found {
		err = fmt.Errorf("config map %s/%s is absent; apply the %s app first", edgeNS, edgeConfig, stack.NginxApp)
	}
	return cm, err
}

// EdgeFiles is empty, with no version, before the nginx app's first deploy.
func (c *Client) EdgeFiles(ctx context.Context) (map[string]string, string, error) {
	cm, found, err := getOwned(ctx, c.cs.CoreV1().ConfigMaps(edgeNS), "config map", edgeConfig)
	if err != nil || !found {
		return map[string]string{}, "", err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	return cm.Data, cm.ResourceVersion, nil
}

func (c *Client) SetEdgeFiles(ctx context.Context, files map[string]string, version string) error {
	cm, err := c.edgeConfig(ctx)
	if err != nil {
		return err
	}
	cm.Data, cm.ResourceVersion = files, version
	_, err = c.cs.CoreV1().ConfigMaps(edgeNS).Update(ctx, cm, metav1.UpdateOptions{})
	return edgeWrite("config map "+edgeConfig, err)
}

// edgePods are the edge's ready pods: the ones the Service sends requests to.
func (c *Client) edgePods(ctx context.Context) ([]corev1.Pod, error) {
	list, err := c.cs.CoreV1().Pods(edgeNS).List(ctx, metav1.ListOptions{LabelSelector: metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: selector(stack.NginxApp)})})
	if err != nil {
		return nil, fmt.Errorf("pods of %s: %w", stack.NginxApp, err)
	}
	var out []corev1.Pod
	for _, p := range list.Items {
		if owned(&p) && podReady(&p) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (c *Client) EdgeLoaded(ctx context.Context, hasten string) (map[string]string, error) {
	pods, err := c.edgePods(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range pods {
		if hasten != "" && p.Annotations[hastenAnnotation] != hasten {
			// Best effort: without it the kubelet re-reads the volumes at its next sync.
			patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, hastenAnnotation, hasten)
			c.cs.CoreV1().Pods(edgeNS).Patch(ctx, p.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
		}
		// A pod that does not answer has not loaded anything the installer can vouch for.
		b, _ := c.Exec(ctx, edgeNS, p.Name, appName, edge.NginxLoaded)
		out[p.Name] = string(b)
	}
	return out, nil
}

// EdgeCurl runs curl with args in the nginx edge's ready pods, where its proxied requests
// leave from, and returns each pod's output by name (empty where curl failed). With kube-vip
// egress only the pod kube-vip names is asked: kube-vip rewrites the source of one pod, the
// one on the node holding the address, which the Service's Local policy makes the only pod
// that address's clients reach.
func (c *Client) EdgeCurl(ctx context.Context, args []string) (map[string][]byte, error) {
	pods, err := c.edgePods(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := c.cs.CoreV1().Services(edgeNS).Get(ctx, publicName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("service %s/%s: %w", edgeNS, publicName, err)
	}
	active := ""
	if svc.Annotations[kubeVIPPrefix+"egress"] == "true" {
		if active = svc.Annotations[kubeVIPPrefix+"active-endpoint"]; active == "" {
			return nil, fmt.Errorf("kube-vip has not named the pod whose egress it rewrites (annotation %sactive-endpoint on Service %s/%s)", kubeVIPPrefix, edgeNS, publicName)
		}
	}
	out := map[string][]byte{}
	for _, p := range pods {
		if active == "" || p.Status.PodIP == active {
			out[p.Name], _ = c.Exec(ctx, edgeNS, p.Name, appName, append([]string{"curl"}, args...))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no ready pod of %s to ask", stack.NginxApp)
	}
	return out, nil
}

// TLSCertificate reads a labelled kubernetes.io/tls Secret's certificate; the key is not kept.
func (c *Client) TLSCertificate(ctx context.Context, secret string) ([]byte, error) {
	s, err := c.cs.CoreV1().Secrets(edgeNS).Get(ctx, secret, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("secret %s/%s: %w", edgeNS, secret, err)
	case !owned(s) || s.Type != corev1.SecretTypeTLS:
		return nil, fmt.Errorf("secret %s/%s must be of type kubernetes.io/tls and carry the %s=%s label", edgeNS, secret, managed.Label, managed.Value)
	}
	return s.Data[corev1.TLSCertKey], nil
}

const acmeTokenKey = "api-token"

// ACMEToken reads the installer's copy of a DNS API token; nil when absent.
func (c *Client) ACMEToken(ctx context.Context, name string) ([]byte, error) {
	s, _, err := getOwned(ctx, c.cs.CoreV1().Secrets(edgeNS), "secret", name)
	if err != nil || s == nil {
		return nil, err
	}
	return s.Data[acmeTokenKey], nil
}

// SetACMEToken copies the DNS API token beside the Issuer that reads it: cert-manager takes
// an Issuer's credentials only from its own namespace.
func (c *Client) SetACMEToken(ctx context.Context, name string, token []byte) error {
	api := c.cs.CoreV1().Secrets(edgeNS)
	have, found, err := getOwned(ctx, api, "secret", name)
	switch {
	case err != nil:
		return err
	case !found:
		_, err = api.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: edgeNS, Labels: map[string]string{managed.Label: managed.Value}},
			Type:       corev1.SecretTypeOpaque, Data: map[string][]byte{acmeTokenKey: token},
		}, metav1.CreateOptions{})
	default:
		have.Data = map[string][]byte{acmeTokenKey: token}
		_, err = api.Update(ctx, have, metav1.UpdateOptions{})
	}
	if apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("secret %s/%s: %w", edgeNS, name, engine.ErrTransient)
	}
	if err != nil { // never the cluster's answer: it may echo the object
		return fmt.Errorf("secret %s/%s: cluster refused the DNS credential", edgeNS, name)
	}
	return nil
}

func certManagerResource(kind string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: certManagerGroup, Version: "v1", Resource: strings.ToLower(kind) + "s"}
}

// certManagerObject reads the installer's Issuer or Certificate; nil when absent.
func (c *Client) certManagerObject(ctx context.Context, kind, name string) (*unstructured.Unstructured, error) {
	if c.dyn == nil {
		return nil, fmt.Errorf("no dynamic client to read %s %s", kind, name)
	}
	o, err := c.dyn.Resource(certManagerResource(kind)).Namespace(edgeNS).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("%s %s: %w", kind, name, err)
	case !owned(o):
		return nil, fmt.Errorf("%s %s exists and is %w; remove or rename it by hand", kind, name, errNotOwned)
	}
	return o, nil
}

func (c *Client) CertManager(ctx context.Context, kind, name string) (hash string, ready bool, message string, err error) {
	o, err := c.certManagerObject(ctx, kind, name)
	if err != nil || o == nil {
		return "", false, "", err
	}
	message = "no Ready condition yet"
	conditions, _, _ := unstructured.NestedSlice(o.Object, "status", "conditions")
	for _, raw := range conditions {
		if cond, _ := raw.(map[string]any); cond["type"] == "Ready" {
			ready = cond["status"] == "True"
			message = fmt.Sprint(cond["reason"], ": ", cond["message"])
		}
	}
	return o.GetAnnotations()[hashAnnotation], ready, message, nil
}

func (c *Client) ApplyCertManager(ctx context.Context, kind, name, hash string, spec map[string]any) error {
	have, err := c.certManagerObject(ctx, kind, name)
	if err != nil {
		return err
	}
	want := &unstructured.Unstructured{Object: map[string]any{"apiVersion": certManagerGroup + "/v1", "kind": kind, "spec": spec}}
	want.SetName(name)
	want.SetNamespace(edgeNS)
	want.SetLabels(map[string]string{managed.Label: managed.Value})
	want.SetAnnotations(map[string]string{hashAnnotation: hash})
	api := c.dyn.Resource(certManagerResource(kind)).Namespace(edgeNS)
	// Strict: a field this cert-manager does not know is refused, never silently dropped.
	if have == nil {
		_, err = api.Create(ctx, want, metav1.CreateOptions{FieldValidation: metav1.FieldValidationStrict})
	} else {
		want.SetResourceVersion(have.GetResourceVersion())
		_, err = api.Update(ctx, want, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict})
	}
	if apierrors.IsNotFound(err) { // the cluster serves no such resource
		return errors.New(c.CertManagerCheck().Detail)
	}
	return edgeWrite(kind+" "+name, err)
}

// CertManagerCheck refuses a plan with managed certificates on a cluster without
// cert-manager: its v1 Issuers and Certificates must be served. The installer never installs
// cert-manager, a cluster-wide component that is the cluster owner's.
func (c *Client) CertManagerCheck() Finding {
	for _, resource := range []string{"issuers", "certificates"} {
		gv, err := c.groupVersion(certManagerGroup, resource)
		if err != nil {
			return Finding{"cert-manager", false, err.Error()}
		}
		served := false
		if !gv.Empty() {
			list, err := c.cs.Discovery().ServerResourcesForGroupVersion(certManagerGroup + "/v1")
			if err == nil {
				for _, r := range list.APIResources {
					served = served || r.Name == resource
				}
			}
		}
		if !served {
			return Finding{"cert-manager", false, "this cluster does not serve " + certManagerGroup + "/v1 " + resource + ": the nginx edge's certificates need cert-manager 1.5 or later, which the cluster's owner installs (https://cert-manager.io/docs/installation/); or give each route a certificate_secret instead"}
		}
	}
	return Finding{"cert-manager", true, certManagerGroup + "/v1 issuers and certificates served"}
}
