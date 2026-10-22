package kube

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/netip"
	"path"
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

// The suite runner sidecar (docs/kyyard-runner.k8s.yaml): its state claim and the socket
// directory it shares with the app.
const (
	runnerState  = "runner-state"
	runnerSocket = "runner-socket"
)

// socketDir is the socket's directory in the runner-socket volume.
var socketDir = path.Base(path.Dir(catalog.SuiteRunnerSocket))

var runnerRequests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")}

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
	requests := res
	if k.Resources.CPURequest != "" {
		requests = res.DeepCopy()
		requests[corev1.ResourceCPU] = resource.MustParse(k.Resources.CPURequest)
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
		Resources: corev1.ResourceRequirements{Requests: requests, Limits: res},
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
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "secrets", MountPath: k.SecretsMount(), ReadOnly: true})
	}
	o := objects{Policy: &networkingv1.NetworkPolicy{ObjectMeta: meta(policyName), Spec: policy(a)}}
	var class *string // nil is omitted from the JSON the hash covers
	if a.StorageClass != "" {
		class = &a.StorageClass
	}
	// A claim's root may be group- or world-writable (fsGroup, hostPath provisioners), so a
	// non-root app mounts a private subdirectory its own uid creates first. Root apps own theirs.
	var inits []corev1.Container
	if !root && (len(k.Volumes) > 0 || k.SuiteRunner) {
		inits = append(inits, corev1.Container{
			Name:            "private-volumes",
			Image:           CredentialGeneratorImage,
			Command:         []string{"mkdir", "-p", "-m", "700"},
			Resources:       corev1.ResourceRequirements{Requests: initResources, Limits: initResources},
			SecurityContext: container.SecurityContext,
		})
	}
	// A seeded claim starts as a copy of what the image holds at its mount (a bundled database,
	// say), which an empty claim would hide. The marker comes last: an interrupted copy reruns.
	var seed *corev1.Container
	for _, v := range k.Volumes {
		if !v.Seed {
			continue
		}
		if seed == nil {
			seed = &corev1.Container{
				Name: "seed-volumes", Image: container.Image, Command: []string{"/bin/sh", "-ec", ""},
				Resources: container.Resources, SecurityContext: container.SecurityContext,
			}
		}
		at := "/seed/" + v.Name
		seed.Command[2] += fmt.Sprintf("test -e %[1]s/.kyq-seeded || { cp -a %[2]s/. %[1]s/; chown --reference=%[2]s %[1]s; chmod --reference=%[2]s %[1]s; touch %[1]s/.kyq-seeded; }\n", at, v.Mount)
		seed.VolumeMounts = append(seed.VolumeMounts, corev1.VolumeMount{Name: "vol-" + v.Name, MountPath: at})
	}
	if seed != nil {
		inits = append(inits, *seed)
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
		if !root {
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
	containers := []corev1.Container{container}
	if k.SuiteRunner {
		containers, volumes = withRunner(containers, volumes, a.RunnerImage)
		// The app's socket directory: created private first, so the kubelet's world-writable
		// volume root never becomes an ancestor KyYard's socket check refuses.
		inits[0].Command = append(inits[0].Command, "/volumes/"+runnerSocket+"/"+socketDir)
		inits[0].VolumeMounts = append(inits[0].VolumeMounts, corev1.VolumeMount{Name: runnerSocket, MountPath: "/volumes/" + runnerSocket})
		o.Claims = append(o.Claims, &corev1.PersistentVolumeClaim{
			ObjectMeta: meta(runnerState),
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
				StorageClassName: class,
			},
		})
	}
	var grace *int64 // nil keeps the default and other apps' hashes
	if k.TerminationGraceSeconds > 0 {
		grace = &k.TerminationGraceSeconds
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
					AutomountServiceAccountToken:  ptr.To(false),
					EnableServiceLinks:            ptr.To(false),
					TerminationGracePeriodSeconds: grace,
					SecurityContext:               podSecurityContext(k),
					InitContainers:                inits,
					Containers:                    containers,
					Volumes:                       volumes,
				},
			},
		},
	}
	return o
}

// withRunner adds the suite runner beside the app (containers[0]): restricted like it, it
// mounts only its state claim and the socket volume, whose socket directory alone the app
// mounts.
func withRunner(containers []corev1.Container, volumes []corev1.Volume, image string) ([]corev1.Container, []corev1.Volume) {
	uid := int64(catalog.SuiteRunnerUID)
	socket := corev1.VolumeMount{Name: runnerSocket, MountPath: catalog.SuiteRunnerMount}
	containers[0].VolumeMounts = append(containers[0].VolumeMounts, corev1.VolumeMount{
		Name: runnerSocket, MountPath: path.Dir(catalog.SuiteRunnerSocket), SubPath: socketDir,
	})
	containers = append(containers, corev1.Container{
		Name:  runnerContainer,
		Image: image,
		// Volume roots are the kubelet's: the runner works in subdirectories it creates.
		Command:   []string{"kyquickstart", "serve", "--state", "/state/runner", "--socket", catalog.SuiteRunnerSocket, "--pod-volumes"},
		Resources: corev1.ResourceRequirements{Requests: runnerRequests, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")}},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			RunAsNonRoot:             ptr.To(true),
			RunAsUser:                ptr.To(uid),
			RunAsGroup:               ptr.To(uid),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		VolumeMounts: []corev1.VolumeMount{{Name: runnerState, MountPath: "/state"}, socket},
	})
	return containers, append(volumes,
		corev1.Volume{Name: runnerState, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: runnerState}}},
		corev1.Volume{Name: runnerSocket, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	)
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
// anywhere (ACME and upstreams), its upstreams off the cluster and pods in installer-managed
// namespaces, whose own policies decide what it reaches there. An app with egress "edge"
// reaches only cluster DNS and the edge's pods.
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
		spec.PolicyTypes = append(spec.PolicyTypes, networkingv1.PolicyTypeEgress)
		spec.Egress = []networkingv1.NetworkPolicyEgressRule{
			{Ports: dnsPorts(), To: clusterDNS()},
			{Ports: tcp(80, 443)},
			{To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{managed.Label: managed.Value}}}}},
		}
		for _, u := range a.Upstreams {
			spec.Egress = append(spec.Egress, networkingv1.NetworkPolicyEgressRule{Ports: tcp(int(u.Port())),
				To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: netip.PrefixFrom(u.Addr(), u.Addr().BitLen()).String()}}}})
		}
		return spec
	}
	edgePods := []networkingv1.NetworkPolicyPeer{{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{nsNameLabel: namespaceOf(a.Edge)}},
		PodSelector:       &metav1.LabelSelector{MatchLabels: selector(a.Edge)},
	}}
	if a.Edge != "" && len(k.Ports) > 0 {
		spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{Ports: tcp(k.Ports...), From: edgePods}}
	}
	if k.Egress == "edge" {
		spec.PolicyTypes = append(spec.PolicyTypes, networkingv1.PolicyTypeEgress)
		spec.Egress = []networkingv1.NetworkPolicyEgressRule{{Ports: dnsPorts(), To: clusterDNS()}}
		if a.Edge != "" {
			spec.Egress = append(spec.Egress, networkingv1.NetworkPolicyEgressRule{To: edgePods})
		}
	}
	return spec
}

func dnsPorts() []networkingv1.NetworkPolicyPort {
	return []networkingv1.NetworkPolicyPort{
		{Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To(intstr.FromInt(53))},
		{Protocol: ptr.To(corev1.ProtocolUDP), Port: ptr.To(intstr.FromInt(53))},
	}
}

func clusterDNS() []networkingv1.NetworkPolicyPeer {
	return []networkingv1.NetworkPolicyPeer{{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{nsNameLabel: "kube-system"}},
		PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
	}}
}
