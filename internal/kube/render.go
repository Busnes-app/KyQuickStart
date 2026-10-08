package kube

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

// objects is everything the deploy step writes for one app, in one namespace.
type objects struct {
	Deployment *appsv1.Deployment
	Service    *corev1.Service // nil when the app listens on no port
	Claims     []*corev1.PersistentVolumeClaim
	Policy     *networkingv1.NetworkPolicy
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
func render(app string, c catalog.App, releaseSet string) objects {
	k := c.Kubernetes
	ns := namespaceOf(app)
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: ns, Labels: workloadLabels(releaseSet)}
	}
	res := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(k.Resources.CPU),
		corev1.ResourceMemory: resource.MustParse(k.Resources.Memory),
	}
	container := corev1.Container{
		Name:      appName,
		Image:     c.Images[k.Image],
		Args:      k.Args,
		Resources: corev1.ResourceRequirements{Requests: res, Limits: res},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			RunAsNonRoot:             ptr.To(true),
			RunAsUser:                ptr.To(k.RunAs),
			RunAsGroup:               ptr.To(k.RunAs),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
	}
	volumes := []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	if len(c.Secrets) > 0 {
		volumes = append(volumes, corev1.Volume{Name: "secrets", VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: secretName, DefaultMode: ptr.To(int32(0o440))},
		}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "secrets", MountPath: "/run/secrets", ReadOnly: true})
	}
	o := objects{Policy: &networkingv1.NetworkPolicy{
		ObjectMeta: meta(policyName),
		// No ingress rules: nothing reaches the app until the edge is admitted (Phase 4).
		Spec: networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}},
	}}
	for _, v := range k.Volumes {
		claim := "vol-" + v.Name
		o.Claims = append(o.Claims, &corev1.PersistentVolumeClaim{
			ObjectMeta: meta(claim),
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(v.Size)}},
			},
		})
		volumes = append(volumes, corev1.Volume{Name: claim, VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim},
		}})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: claim, MountPath: v.Mount})
	}
	if len(k.Ports) > 0 {
		svc := &corev1.Service{ObjectMeta: meta(appName), Spec: corev1.ServiceSpec{Selector: selector(app)}}
		for _, p := range k.Ports {
			port := int32(p)
			container.Ports = append(container.Ports, corev1.ContainerPort{ContainerPort: port})
			svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Name: fmt.Sprintf("p%d", p), Port: port, TargetPort: intstr.FromInt32(port)})
		}
		o.Service = svc
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
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels(app, releaseSet)},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					EnableServiceLinks:           ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptr.To(true),
						FSGroup:        ptr.To(k.RunAs),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{container},
					Volumes:    volumes,
				},
			},
		},
	}
	return o
}
