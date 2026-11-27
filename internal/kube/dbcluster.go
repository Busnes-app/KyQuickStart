package kube

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
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
	// clusterCertificateYears is the life of the Cluster's database server certificate.
	// Renewed automatically on apply once within clusterCertificateRenewBefore of expiry,
	// or on demand via clusterReissueAnnotation.
	clusterCertificateYears = 10
	// clusterCertificateRenewBefore is how far before expiry apply re-issues kyq-database-tls.
	clusterCertificateRenewBefore = 180 * 24 * time.Hour
	// clusterReissueAnnotation, when set to "true" on kyq-database-tls, forces a fresh CA and
	// server certificate on the next apply (and is cleared once re-issued).
	clusterReissueAnnotation = "kyquickstart.busnes.app/reissue"
	// clusterCAAnnotation records the SHA-256 digest of kyq-database-tls's ca.crt on the app
	// Deployments' pod templates so a CA renewal rolls the pods that read ca.crt at startup.
	clusterCAAnnotation = "kyquickstart.busnes.app/database-ca-sha256"
	// clusterSSLGroups is PostgreSQL 18's TLS key-exchange group list: hybrid post-quantum
	// X25519MLKEM768 first (needs OpenSSL 3.5+ in the operand image; Debian trixie ships 3.5),
	// keeping classical X25519 and P-256 fallback and refusing other groups.
	clusterSSLGroups = "X25519MLKEM768:X25519:prime256v1"
)

// clusterNow returns the current time for database certificate issuance and renewal checks.
var clusterNow = time.Now

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
	// The major upgrade finds the claims the Cluster owns.
	return append(out, authorizationv1.ResourceAttributes{Resource: "persistentvolumeclaims", Verb: "list"})
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
	image := a.Catalog.Images[k.Replicas.ClusterImage]
	storage := map[string]any{"size": k.Replicas.DatabaseSize}
	if a.StorageClass != "" {
		storage["storageClass"] = a.StorageClass
	}
	memory := databaseResources.Memory().String()
	postgresql := map[string]any{"synchronous": map[string]any{"method": "any", "number": int64(1)}}
	// ssl_groups was added in PostgreSQL 18 (17 rejects it as an unknown parameter), so only
	// set it for 18+: an earlier 17 Cluster gets it on the deploy after database-upgrade.
	if imageMajor(image) >= 18 {
		postgresql["parameters"] = map[string]any{"ssl_groups": clusterSSLGroups}
	}
	o.Cluster = map[string]any{
		"instances": int64(k.DatabaseInstances),
		"imageName": image,
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
		"postgresql": postgresql,
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

// parsePEMCertificates decodes all CERTIFICATE PEM blocks in raw.
func parsePEMCertificates(raw []byte) []*x509.Certificate {
	var out []*x509.Certificate
	for rest := raw; len(rest) > 0; {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = next
		if block.Type != "CERTIFICATE" {
			continue
		}
		if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
			out = append(out, cert)
		}
	}
	return out
}

// clusterCertNeedsRenewal reports whether sec lacks a healthy, non-expiring database server
// certificate for host (or was explicitly marked for re-issue via clusterReissueAnnotation).
func clusterCertNeedsRenewal(sec *corev1.Secret, host string, now time.Time) bool {
	if sec == nil {
		return true
	}
	if sec.Annotations[clusterReissueAnnotation] == "true" {
		return true
	}
	caPEM := sec.Data[clusterCAKey]
	certPEM := sec.Data[corev1.TLSCertKey]
	keyPEM := sec.Data[corev1.TLSPrivateKeyKey]
	if len(caPEM) == 0 || len(certPEM) == 0 || len(keyPEM) == 0 {
		return true
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return true
	}
	leaves := parsePEMCertificates(certPEM)
	if len(leaves) == 0 {
		return true
	}
	leaf := leaves[0]
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return true
	}
	// Require the leaf and its verifying CA to be valid both now and throughout the renewal window.
	horizon := now.Add(clusterCertificateRenewBefore)
	for _, checkTime := range []time.Time{now, horizon} {
		for _, dnsName := range []string{host, host + ".cluster.local"} {
			if _, err := leaf.Verify(x509.VerifyOptions{
				Roots:       roots,
				DNSName:     dnsName,
				CurrentTime: checkTime,
			}); err != nil {
				return true
			}
		}
	}
	return false
}

// renewServerCertificate mints a fresh CA and server certificate for host. When prev holds a
// currently valid leaf signed by a still-valid CA in prev[ca.crt], that signing CA is appended
// after the new CA in the returned ca.crt bundle so app pods reloading ca.crt trust both the
// outgoing and incoming database leaves during rollout. Expired or unrelated CAs are dropped,
// bounding ca.crt to at most two certificates.
func renewServerCertificate(host string, prev *corev1.Secret, now time.Time) (caBundle, cert, key []byte, err error) {
	newCA, cert, key, err := serverCertificate(host, now)
	if err != nil {
		return nil, nil, nil, err
	}
	if prev == nil {
		return newCA, cert, key, nil
	}
	leaves := parsePEMCertificates(prev.Data[corev1.TLSCertKey])
	if len(leaves) == 0 {
		return newCA, cert, key, nil
	}
	oldLeaf := leaves[0]
	newCAs := parsePEMCertificates(newCA)
	for _, oldCA := range parsePEMCertificates(prev.Data[clusterCAKey]) {
		if !oldCA.IsCA || !now.Before(oldCA.NotAfter) || now.Before(oldCA.NotBefore) {
			continue
		}
		if len(newCAs) > 0 && bytes.Equal(oldCA.Raw, newCAs[0].Raw) {
			continue
		}
		pool := x509.NewCertPool()
		pool.AddCert(oldCA)
		if _, err := oldLeaf.Verify(x509.VerifyOptions{
			Roots:       pool,
			DNSName:     host,
			CurrentTime: now,
		}); err != nil {
			continue
		}
		oldPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: oldCA.Raw})
		return append(newCA, oldPEM...), cert, key, nil
	}
	return newCA, cert, key, nil
}

// clusterCAHash returns the SHA-256 hex digest of a ca.crt bundle.
func clusterCAHash(ca []byte) string {
	sum := sha256.Sum256(ca)
	return hex.EncodeToString(sum[:])
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
	// Only the database-upgrade step, after its copies, moves the data to another major.
	if have != nil {
		image, _, _ := unstructured.NestedString(have.Object, "spec", "imageName")
		if from, to := imageMajor(image), imageMajor(spec["imageName"].(string)); from != to {
			return fmt.Errorf("cluster %s runs PostgreSQL %d, and this release's is %d: step %s.database-upgrade moves it first", databaseName, from, to, a.Name)
		}
	}
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
// again. Deleting the Cluster instead would delete every claim it owns. The patch carries no
// resourceVersion: the operator writes the Cluster's status all the time, and the annotation
// is the installer's alone.
func (c *Client) hibernate(ctx context.Context, app string, on bool) error {
	o, err := c.clusterObject(ctx, app)
	if err != nil || o == nil || (o.GetAnnotations()[hibernation] == "on") == on {
		return err
	}
	value := "off"
	if on {
		value = "on"
	}
	return c.patchCluster(ctx, app, map[string]any{"metadata": map[string]any{"annotations": map[string]string{hibernation: value}}})
}

// waitCluster waits until every instance of the app's Cluster is ready.
func (c *Client) waitCluster(ctx context.Context, app string) error {
	return c.waitClusterFor(ctx, app, clusterState)
}

// waitClusterFor waits until state, why the Cluster is not yet as wanted, returns "".
func (c *Client) waitClusterFor(ctx context.Context, app string, state func(*unstructured.Unstructured) string) error {
	deadline := time.Now().Add(clusterTimeout)
	for {
		o, err := c.clusterObject(ctx, app)
		if err != nil && !transient(err) {
			return err
		}
		why := "no answer from the API server"
		if err == nil {
			if why = state(o); why == "" {
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
// CloudNativePG: its v1 Clusters must be served, by the catalog's cloudnative-pg app or the
// cluster owner's own installation.
func (c *Client) DatabaseOperatorCheck() Finding {
	const check = "database operator"
	gv, err := c.groupVersion(clusterGroup, clusterResource.Resource)
	if err != nil {
		return Finding{check, false, err.Error()}
	}
	if gv.Version != clusterResource.Version {
		return Finding{check, false, "this cluster does not serve " + clusterGroup + "/v1 clusters: database_instances needs the CloudNativePG operator, 1.24 or later. Add the cloudnative-pg app to the plan on this target (it needs a cluster-admin kubeconfig), have the cluster's owner install it (https://cloudnative-pg.io/documentation/current/installation_upgrade/), or remove database_instances for a single Postgres pod"}
	}
	return Finding{check, true, clusterGroup + "/v1 clusters served"}
}
