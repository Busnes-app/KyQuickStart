package kube

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

// privateDir is the subdirectory of each claim a non-root app mounts.
const privateDir = "private"

// initResources bound the private-volumes init container: one mkdir.
var initResources = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("16Mi")}

// objects is everything the deploy step writes for one app, in one namespace.
type objects struct {
	Deployment *appsv1.Deployment
	Service    *corev1.Service // ClusterIP; nil when every port is published or there is none
	Claims     []*corev1.PersistentVolumeClaim
	Policy     *networkingv1.NetworkPolicy
	// Public is the NodePort Service for an edge's node_ports. omitempty keeps other apps' hashes.
	Public *corev1.Service `json:",omitempty"`
}

// services are the app's Services that exist, ClusterIP first.
func (o objects) services() []*corev1.Service {
	var out []*corev1.Service
	for _, s := range []*corev1.Service{o.Service, o.Public} {
		if s != nil {
			out = append(out, s)
		}
	}
	return out
}

func (o objects) hash() string {
	b, err := json.Marshal(o)
	if err != nil {
		panic(err) // typed API objects always marshal
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// render builds the app's objects. Catalog validation guarantees every quantity parses.
// An empty storage class leaves claims to the cluster default and out of the hash.
func render(a App) objects {
	app, c, k := a.Name, a.Catalog, a.Catalog.Kubernetes
	ns := namespaceOf(app)
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: ns, Labels: workloadLabels(a.ReleaseSet)}
	}
	res := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(k.Resources.CPU),
		corev1.ResourceMemory: resource.MustParse(k.Resources.Memory),
	}
	root := k.RunAs == 0 // only under baseline
	caps := &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}
	for _, name := range k.Capabilities {
		caps.Add = append(caps.Add, corev1.Capability(name))
	}
	container := corev1.Container{
		Name:      appName,
		Image:     c.Images[k.Image],
		Args:      k.Args,
		Resources: corev1.ResourceRequirements{Requests: res, Limits: res},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(!k.WritableRoot),
			RunAsNonRoot:             ptr.To(!root),
			RunAsUser:                ptr.To(k.RunAs),
			RunAsGroup:               ptr.To(k.RunAs),
			Capabilities:             caps,
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
	}
	for _, name := range slices.Sorted(maps.Keys(k.Env)) { // sorted: the hash must be stable
		container.Env = append(container.Env, corev1.EnvVar{Name: name, Value: k.Env[name]})
	}
	volumes := []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	if len(c.Secrets) > 0 {
		volumes = append(volumes, corev1.Volume{Name: "secrets", VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: secretName, DefaultMode: ptr.To(int32(0o440))},
		}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "secrets", MountPath: "/run/secrets", ReadOnly: true})
	}
	o := objects{Policy: &networkingv1.NetworkPolicy{ObjectMeta: meta(policyName), Spec: policy(a)}}
	var class *string // nil is omitted from the JSON the hash covers
	if a.StorageClass != "" {
		class = &a.StorageClass
	}
	// A claim's root may be group- or world-writable (fsGroup, hostPath provisioners), so a
	// non-root app mounts a private subdirectory its own uid creates first. Root apps own theirs.
	var inits []corev1.Container
	if !root && len(k.Volumes) > 0 {
		inits = append(inits, corev1.Container{
			Name:            "private-volumes",
			Image:           CredentialGeneratorImage,
			Command:         []string{"mkdir", "-p", "-m", "700"},
			Resources:       corev1.ResourceRequirements{Requests: initResources, Limits: initResources},
			SecurityContext: container.SecurityContext,
		})
	}
	for _, v := range k.Volumes {
		claim := "vol-" + v.Name
		o.Claims = append(o.Claims, &corev1.PersistentVolumeClaim{
			ObjectMeta: meta(claim),
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(v.Size)}},
				StorageClassName: class,
			},
		})
		volumes = append(volumes, corev1.Volume{Name: claim, VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim},
		}})
		mount := corev1.VolumeMount{Name: claim, MountPath: v.Mount}
		if inits != nil {
			at := "/volumes/" + v.Name
			inits[0].Command = append(inits[0].Command, at+"/"+privateDir)
			inits[0].VolumeMounts = append(inits[0].VolumeMounts, corev1.VolumeMount{Name: claim, MountPath: at})
			mount.SubPath = privateDir
		}
		container.VolumeMounts = append(container.VolumeMounts, mount)
	}
	if len(k.Ports) > 0 {
		svc := &corev1.Service{ObjectMeta: meta(appName), Spec: corev1.ServiceSpec{Selector: selector(app)}}
		for _, p := range k.Ports {
			port := int32(p)
			container.Ports = append(container.Ports, corev1.ContainerPort{ContainerPort: port})
			sp := corev1.ServicePort{Name: fmt.Sprintf("p%d", p), Port: port, TargetPort: intstr.FromInt32(port)}
			if node, ok := k.NodePorts[p]; ok {
				if o.Public == nil {
					// Local keeps the client's address, which the edge's connector_cidr check relies on.
					o.Public = &corev1.Service{ObjectMeta: meta(publicName), Spec: corev1.ServiceSpec{
						Type: corev1.ServiceTypeNodePort, Selector: selector(app),
						ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
					}}
				}
				sp.NodePort = int32(node)
				o.Public.Spec.Ports = append(o.Public.Spec.Ports, sp)
				continue
			}
			svc.Spec.Ports = append(svc.Spec.Ports, sp)
		}
		if len(svc.Spec.Ports) > 0 {
			o.Service = svc
		}
		if k.ReadinessPath != "" {
			container.ReadinessProbe = &corev1.Probe{
				ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: k.ReadinessPath, Port: intstr.FromInt32(int32(k.Ports[0]))}},
				PeriodSeconds: 5,
			}
		}
	}
	o.Deployment = &appsv1.Deployment{
		ObjectMeta: meta(appName),
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: selector(app)},
			// Recreate: a ReadWriteOnce claim cannot attach to two pods at once.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels(app, a.ReleaseSet)},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					EnableServiceLinks:           ptr.To(false),
					SecurityContext:              podSecurityContext(k),
					InitContainers:               inits,
					Containers:                   []corev1.Container{container},
					Volumes:                      volumes,
				},
			},
		},
	}
	return o
}

func podSecurityContext(k *catalog.Kubernetes) *corev1.PodSecurityContext {
	sc := &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		FSGroup:        ptr.To(k.RunAs),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if k.RunAs == 0 {
		sc.RunAsNonRoot, sc.FSGroup = nil, nil // a root init sets its own ownership
	}
	if k.LowPorts {
		sc.Sysctls = []corev1.Sysctl{{Name: "net.ipv4.ip_unprivileged_port_start", Value: "0"}}
	}
	return sc
}

// policy denies all ingress except, for an edge, to its published ports and, for an app on a
// cluster with an edge, from the edge's pods. An edge's egress is limited to DNS, HTTP(S)
// anywhere (ACME and upstreams) and pods in installer-managed namespaces, whose own policies
// decide what it reaches there.
func policy(a App) networkingv1.NetworkPolicySpec {
	k := a.Catalog.Kubernetes
	spec := networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}}
	tcp := func(ports ...int) []networkingv1.NetworkPolicyPort {
		var out []networkingv1.NetworkPolicyPort
		for _, p := range ports {
			out = append(out, networkingv1.NetworkPolicyPort{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt(p))})
		}
		return out
	}
	if a.Catalog.Category == "edge" {
		if len(k.NodePorts) > 0 {
			spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{Ports: tcp(slices.Sorted(maps.Keys(k.NodePorts))...)}}
		}
		dns := append(tcp(53), networkingv1.NetworkPolicyPort{Protocol: ptr.To(corev1.ProtocolUDP), Port: ptr.To(intstr.FromInt(53))})
		spec.PolicyTypes = append(spec.PolicyTypes, networkingv1.PolicyTypeEgress)
		spec.Egress = []networkingv1.NetworkPolicyEgressRule{
			{Ports: dns, To: []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{nsNameLabel: "kube-system"}},
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
			}}},
			{Ports: tcp(80, 443)},
			{To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{managed.Label: managed.Value}}}}},
		}
		return spec
	}
	if a.Edge != "" && len(k.Ports) > 0 {
		spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{Ports: tcp(k.Ports...), From: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{nsNameLabel: namespaceOf(a.Edge)}},
			PodSelector:       &metav1.LabelSelector{MatchLabels: selector(a.Edge)},
		}}}}
	}
	return spec
}
