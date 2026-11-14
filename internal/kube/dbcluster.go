package kube

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/managed"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

// With database_instances in the plan, the installer's Postgres is a CloudNativePG Cluster
// "database" in the app's namespace: that many instances on different nodes, one of them the
// primary, every commit confirmed by a standby before it is acknowledged. The operator fails
// over to a standby when the primary's node is lost.
const (
	clusterGroup     = "postgresql.cnpg.io"
	clusterTLSSecret = "kyq-database-tls"
	clusterCADir     = "/run/kyq-database-ca"
	clusterCAKey     = "ca.crt"
	clusterLabel     = "cnpg.io/cluster" // the operator's, on every pod of the Cluster
	hibernation      = "cnpg.io/hibernation"
	clusterHealthy   = "Cluster in healthy state"
	clusterStatus    = 8000 // each instance's status port, which the operator polls
	usernameKey      = "username"
	// clusterCertificateYears is the life of the server certificate, which nothing renews.
	clusterCertificateYears = 10
)

// clusterTimeout bounds a Cluster's first start: one instance at a time, each pulling the
// image on its node and copying the instance before it.
var clusterTimeout = 15 * time.Minute

// clusterPods selects every pod the operator makes for the Cluster.
var clusterPods = &metav1.LabelSelector{MatchLabels: map[string]string{clusterLabel: databaseName}}

var clusterResource = schema.GroupVersionResource{Group: clusterGroup, Version: "v1", Resource: "clusters"}

// clusterAccess is what a plan with database_instances adds to required.
var clusterAccess = func() []authorizationv1.ResourceAttributes {
	var out []authorizationv1.ResourceAttributes
	for _, v := range []string{"get", "create", "update", "patch"} {
		out = append(out, authorizationv1.ResourceAttributes{Group: clusterGroup, Resource: clusterResource.Resource, Verb: v})
	}
	return out
}()

// clustered reports whether the installer's Postgres is a CloudNativePG Cluster.
func (a App) clustered() bool {
	return a.ownDatabase() && a.Catalog.Kubernetes.DatabaseInstances > 0
}

// clusterHost is the Service the operator points at the primary.
func clusterHost(app string) string {
	return fmt.Sprintf("%s-rw.%s.svc", databaseName, namespaceOf(app))
}

// clusterURL is how the app's pods reach the primary: encrypted, and only a server holding
// the certificate the installer made is believed.
func clusterURL(app string, password []byte) []byte {
	u := url.URL{
		Scheme: "postgres", User: url.UserPassword(databaseUser, string(password)),
		Host: fmt.Sprintf("%s:%d", clusterHost(app), databasePort), Path: "/" + databaseUser,
		RawQuery: "sslmode=verify-full&sslrootcert=" + clusterCADir + "/" + clusterCAKey,
	}
	return []byte(u.String())
}

// databaseCluster adds the Cluster and the policy beside the namespace's default deny. Unlike the
// single pod, the instances' egress is left open: each must reach the API server, which no
// portable policy can name.
func databaseCluster(o *objects, a App, meta func(string) metav1.ObjectMeta) {
	app, k := a.Name, a.Catalog.Kubernetes
	storage := map[string]any{"size": k.Replicas.DatabaseSize}
	if a.StorageClass != "" {
		storage["storageClass"] = a.StorageClass
	}
	memory := databaseResources.Memory().String()
	o.Cluster = map[string]any{
		"instances": int64(k.DatabaseInstances),
		"imageName": a.Catalog.Images[k.Replicas.ClusterImage],
		// The same database a single pod gets: UTF8 with the C locale, owned by a role that
		// is not a superuser. The operator keeps Postgres's own superuser without a password.
		"bootstrap": map[string]any{"initdb": map[string]any{
			"database": databaseUser, "owner": databaseUser, "secret": map[string]any{"name": databaseSecret},
			"encoding": "UTF8", "localeCollate": "C", "localeCType": "C",
		}},
		"certificates": map[string]any{"serverCASecret": clusterTLSSecret, "serverTLSSecret": clusterTLSSecret},
		// A commit waits for one standby, so a failover loses nothing that was acknowledged:
		// KyIdentity relies on it for sign-outs and spent codes. With fewer than two
		// instances up, writes wait.
		"postgresql": map[string]any{"synchronous": map[string]any{"method": "any", "number": int64(1)}},
		// Never two instances on one node: a node's loss must cost one of them.
		"affinity": map[string]any{
			"enablePodAntiAffinity": true, "topologyKey": "kubernetes.io/hostname", "podAntiAffinityType": "required",
		},
		"storage": storage,
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "100m", "memory": memory},
			"limits":   map[string]any{"cpu": databaseResources.Cpu().String(), "memory": memory},
		},
	}
	members := *clusterPods
	tcp := func(ports ...int32) []networkingv1.NetworkPolicyPort {
		var out []networkingv1.NetworkPolicyPort
		for _, p := range ports {
			out = append(out, networkingv1.NetworkPolicyPort{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt32(p))})
		}
		return out
	}
	o.DatabasePolicy = &networkingv1.NetworkPolicy{ObjectMeta: meta(databaseName), Spec: networkingv1.NetworkPolicySpec{
		PodSelector: members,
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		Ingress: []networkingv1.NetworkPolicyIngressRule{
			{Ports: tcp(databasePort), From: []networkingv1.NetworkPolicyPeer{{
				PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{servesLabel: app}},
			}}},
			// The instances replicate from each other, and the operator, wherever the
			// cluster's owner installed it, reads their status and promotes a standby.
			{Ports: tcp(databasePort, clusterStatus), From: []networkingv1.NetworkPolicyPeer{
				{PodSelector: &members},
				{NamespaceSelector: &metav1.LabelSelector{}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{nameLabel: "cloudnative-pg"}}},
			}},
		},
	}}
}

// serverCertificate makes the Cluster's server certificate for host, and the authority that
// signed it. The authority's key is never kept, so nothing else can be issued under it. The
// operator's own authority is not used: it is replaced every 90 days, and KyIdentity reads
// the one it trusts only when it starts.
func serverCertificate(host string, now time.Time) (ca, cert, key []byte, err error) {
	template := func(name string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
		if err != nil {
			return nil, nil, err
		}
		return &x509.Certificate{
			SerialNumber: serial, Subject: pkix.Name{CommonName: name},
			NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(clusterCertificateYears, 0, 0),
			BasicConstraintsValid: true,
		}, k, nil
	}
	authority, authorityKey, err := template("kyquickstart database authority")
	if err != nil {
		return nil, nil, nil, err
	}
	authority.IsCA, authority.MaxPathLenZero, authority.KeyUsage = true, true, x509.KeyUsageCertSign
	server, serverKey, err := template(host)
	if err != nil {
		return nil, nil, nil, err
	}
	server.DNSNames = []string{host, host + ".cluster.local"}
	server.KeyUsage, server.ExtKeyUsage = x509.KeyUsageDigitalSignature, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	caDER, err := x509.CreateCertificate(rand.Reader, authority, authority, &authorityKey.PublicKey, authorityKey)
	if err != nil {
		return nil, nil, nil, err
	}
	certDER, err := x509.CreateCertificate(rand.Reader, server, authority, &serverKey.PublicKey, authorityKey)
	if err != nil {
		return nil, nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		return nil, nil, nil, err
	}
	block := func(kind string, der []byte) []byte { return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}) }
	return block("CERTIFICATE", caDER), block("CERTIFICATE", certDER), block("EC PRIVATE KEY", keyDER), nil
}

// clusterObject reads the app's Cluster; nil when absent, or when this cluster serves none.
func (c *Client) clusterObject(ctx context.Context, app string) (*unstructured.Unstructured, error) {
	if c.dyn == nil {
		return nil, errors.New("no dynamic client to read the database Cluster")
	}
	o, err := c.dyn.Resource(clusterResource).Namespace(namespaceOf(app)).Get(ctx, databaseName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("cluster %s: %w", databaseName, err)
	case !owned(o):
		return nil, fmt.Errorf("cluster %s exists and is %w; remove or rename it by hand", databaseName, errNotOwned)
	}
	return o, nil
}

// applyCluster writes the Cluster carrying the input hash, and wakes a hibernated one.
func (c *Client) applyCluster(ctx context.Context, a App, spec map[string]any, hash string) error {
	have, err := c.clusterObject(ctx, a.Name)
	if err != nil {
		return err
	}
	want := &unstructured.Unstructured{Object: map[string]any{"apiVersion": clusterGroup + "/v1", "kind": "Cluster", "spec": spec}}
	want.SetName(databaseName)
	want.SetNamespace(namespaceOf(a.Name))
	want.SetLabels(workloadLabels(a.ReleaseSet))
	want.SetAnnotations(map[string]string{hashAnnotation: hash})
	api := c.dyn.Resource(clusterResource).Namespace(namespaceOf(a.Name))
	// Strict: a field this operator does not know is refused, never silently dropped.
	if have == nil {
		_, err = api.Create(ctx, want, metav1.CreateOptions{FieldValidation: metav1.FieldValidationStrict})
	} else {
		want.SetResourceVersion(have.GetResourceVersion())
		_, err = api.Update(ctx, want, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict})
	}
	if apierrors.IsNotFound(err) { // the cluster serves no such resource
		return errors.New(c.DatabaseOperatorCheck().Detail)
	}
	return edgeWrite("cluster "+databaseName, err)
}

// clusterState returns why the Cluster is not serving, "" when every instance is ready.
func clusterState(o *unstructured.Unstructured) string {
	if o == nil {
		return "cluster " + databaseName + " missing"
	}
	if o.GetAnnotations()[hibernation] == "on" {
		return "cluster " + databaseName + " is hibernated"
	}
	want, _, _ := unstructured.NestedInt64(o.Object, "spec", "instances")
	ready, _, _ := unstructured.NestedInt64(o.Object, "status", "readyInstances")
	phase, _, _ := unstructured.NestedString(o.Object, "status", "phase")
	if ready != want || phase != clusterHealthy {
		return fmt.Sprintf("cluster %s: %d of %d instances ready (%s)", databaseName, ready, want, phase)
	}
	return ""
}

// hibernate stops the Cluster's pods and keeps its claims, or with on false starts them
// again. Deleting the Cluster instead would delete every claim it owns.
func (c *Client) hibernate(ctx context.Context, app string, on bool) error {
	o, err := c.clusterObject(ctx, app)
	if err != nil || o == nil || (o.GetAnnotations()[hibernation] == "on") == on {
		return err
	}
	value := "off"
	if on {
		value = "on"
	}
	patch := fmt.Sprintf(`{"metadata":{"resourceVersion":%q,"annotations":{%q:%q}}}`, o.GetResourceVersion(), hibernation, value)
	if _, err := c.dyn.Resource(clusterResource).Namespace(namespaceOf(app)).Patch(ctx, databaseName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("cluster %s: %w", databaseName, err)
	}
	return nil
}

// waitCluster waits until every instance of the app's Cluster is ready.
func (c *Client) waitCluster(ctx context.Context, app string) error {
	deadline := time.Now().Add(clusterTimeout)
	for {
		o, err := c.clusterObject(ctx, app)
		if err != nil && !transient(err) {
			return err
		}
		why := "no answer from the API server"
		if err == nil {
			if why = clusterState(o); why == "" {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not ready after %s: %s. The operator records why on the Cluster: kubectl -n %s describe cluster %s", clusterTimeout, why, namespaceOf(app), databaseName)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// unmanageCluster reports whether the Cluster still carries a managed label, and with fix
// removes them.
func (c *Client) unmanageCluster(ctx context.Context, app string, fix bool) (bool, error) {
	api := c.dyn.Resource(clusterResource).Namespace(namespaceOf(app))
	o, err := api.Get(ctx, databaseName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cluster %s: %w", databaseName, err)
	}
	_, a := o.GetLabels()[managed.Label]
	_, b := o.GetLabels()[managed.ReleaseSetLabel]
	if (a || b) && fix {
		if _, err := api.Patch(ctx, databaseName, types.MergePatchType, unlabel, metav1.PatchOptions{}); err != nil {
			return true, fmt.Errorf("cluster %s: %w", databaseName, err)
		}
	}
	return a || b, nil
}

// DatabaseOperatorCheck refuses a plan with database_instances on a cluster without
// CloudNativePG: its v1 Clusters must be served. The installer never installs the operator, a
// cluster-wide component that is the cluster owner's.
func (c *Client) DatabaseOperatorCheck() Finding {
	const check = "database operator"
	gv, err := c.groupVersion(clusterGroup, clusterResource.Resource)
	if err != nil {
		return Finding{check, false, err.Error()}
	}
	if gv.Version != clusterResource.Version {
		return Finding{check, false, "this cluster does not serve " + clusterGroup + "/v1 clusters: database_instances needs the CloudNativePG operator, 1.24 or later, which the cluster's owner installs (https://cloudnative-pg.io/documentation/current/installation_upgrade/); or remove database_instances for a single Postgres pod"}
	}
	return Finding{check, true, clusterGroup + "/v1 clusters served"}
}
