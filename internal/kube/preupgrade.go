package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// copiedAnnotation marks a pre-upgrade claim whose copy was verified against the original.
const copiedAnnotation = "kyquickstart/pre-upgrade"

// copyCommand copies each named volume's private directory into its pre-upgrade claim,
// replacing what an interrupted run left, then compares contents, modes, owners, sizes,
// times and link targets with the original.
const copyCommand = `exec 2>&1
for v in "$@"; do
  s=/src/$v d=/dst/$v/` + privateDir + `
  rm -rf "$d"
  mkdir -m 700 "$d"
  cp -a "$s/." "$d/"
  diff -r "$s" "$d" >/dev/null || { echo "$v: copied contents differ from the original"; exit 1; }
  l() { cd "$1" && find . -exec stat -c '%n %f %u %g %s %Y %N' {} + | sort; }
  [ "$(l "$s")" = "$(l "$d")" ] || { echo "$v: copied modes, owners or times differ from the original"; exit 1; }
done`

// PreUpgradeClaim names volume's copy taken before upgrading from version.
func PreUpgradeClaim(from, volume string) string { return "pre-upgrade-" + from + "-vol-" + volume }

// PreUpgradeRefusal says why PreUpgradeCopy cannot cover the app, nil when it can.
func PreUpgradeRefusal(a App) error {
	k := a.Catalog.Kubernetes
	switch {
	case k == nil || len(k.Volumes) == 0:
		return fmt.Errorf("%s keeps no volumes on Kubernetes", a.Name)
	case k.RunAs == 0:
		return fmt.Errorf("%s runs as root, whose volumes the pre-upgrade copy does not cover", a.Name)
	}
	return nil
}

// PreUpgradeCopy stops the app, copies each of its volumes into a new claim named for from,
// verifies the copy and starts the app again, whatever happened. Claims a run already
// verified are kept, never copied over: after a failed deploy the app's volumes may hold
// migrated data. It returns the claims.
func PreUpgradeCopy(ctx context.Context, a App, from string) (claims []string, err error) {
	ns := namespaceOf(a.Name)
	pvcs := a.Client.cs.CoreV1().PersistentVolumeClaims(ns)
	var todo []string
	for _, v := range a.Catalog.Kubernetes.Volumes {
		name := PreUpgradeClaim(from, v.Name)
		claims = append(claims, name)
		c, found, err := getOwned(ctx, pvcs, "claim", name)
		if err != nil {
			return nil, err
		}
		if !found || c.Annotations[copiedAnnotation] != "verified" {
			todo = append(todo, v.Name)
		}
	}
	if len(todo) == 0 {
		return claims, nil
	}
	stop := lifecycleStep{a: a, op: "stop"}
	if err := stop.Apply(ctx); err != nil {
		return nil, fmt.Errorf("stop for the pre-upgrade copy: %w", err)
	}
	defer func() {
		if serr := (lifecycleStep{a: a, op: "start"}).Apply(context.WithoutCancel(ctx)); serr != nil {
			err = errors.Join(err, fmt.Errorf("start %s again: %w", a.Name, serr))
		}
	}()
	for _, v := range todo {
		if err := a.preUpgradeClaim(ctx, from, v); err != nil {
			return nil, err
		}
	}
	err = a.Client.podSession(ctx, a.copyPod(from, todo), "pre-upgrade copy", func(pod string) error {
		out, err := a.Client.ExecReport(ctx, ns, pod, "copy", append([]string{"/bin/sh", "-euc", copyCommand, "copy"}, todo...))
		if err != nil {
			if msg := strings.TrimSpace(string(out)); msg != "" {
				return fmt.Errorf("pre-upgrade copy failed: %s", a.Redact.Redact(msg))
			}
			return fmt.Errorf("pre-upgrade copy failed: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, v := range todo {
		b, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{copiedAnnotation: "verified"}}})
		if _, err := pvcs.Patch(ctx, PreUpgradeClaim(from, v), types.MergePatchType, b, metav1.PatchOptions{}); err != nil {
			return nil, fmt.Errorf("mark %s verified: %w", PreUpgradeClaim(from, v), err)
		}
	}
	return claims, nil
}

// preUpgradeClaim creates volume's copy claim with the original's class and size.
func (a App) preUpgradeClaim(ctx context.Context, from, volume string) error {
	pvcs := a.Client.cs.CoreV1().PersistentVolumeClaims(namespaceOf(a.Name))
	name := PreUpgradeClaim(from, volume)
	if _, found, err := getOwned(ctx, pvcs, "claim", name); err != nil || found {
		return err
	}
	src, found, err := getOwned(ctx, pvcs, "claim", "vol-"+volume)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("claim vol-%s is missing", volume)
	}
	c := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: src.Namespace, Labels: workloadLabels(a.ReleaseSet)},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: src.Spec.Resources.Requests[corev1.ResourceStorage]}},
			StorageClassName: src.Spec.StorageClassName,
		},
	}
	if _, err := pvcs.Create(ctx, c, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create claim %s: %w", name, err)
	}
	return nil
}

// copyPod mounts each volume's private directory read-only and its copy claim, as the app's
// uid under the app's pod security, with no service token and nothing else.
func (a App) copyPod(from string, volumes []string) *corev1.Pod {
	k := a.Catalog.Kubernetes
	res := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")}
	mounts := []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}}
	vols := []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	for _, v := range volumes {
		vols = append(vols,
			corev1.Volume{Name: "src-" + v, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "vol-" + v, ReadOnly: true}}},
			corev1.Volume{Name: "dst-" + v, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: PreUpgradeClaim(from, v)}}})
		mounts = append(mounts,
			corev1.VolumeMount{Name: "src-" + v, MountPath: "/src/" + v, SubPath: privateDir, ReadOnly: true},
			corev1.VolumeMount{Name: "dst-" + v, MountPath: "/dst/" + v})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "kyq-pre-upgrade-", Namespace: namespaceOf(a.Name), Labels: workloadLabels(a.ReleaseSet)},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			ActiveDeadlineSeconds:         ptr.To(int64(6 * 3600)),
			TerminationGracePeriodSeconds: ptr.To(int64(0)),
			AutomountServiceAccountToken:  ptr.To(false), EnableServiceLinks: ptr.To(false),
			SecurityContext: podSecurityContext(k),
			Containers: []corev1.Container{{Name: "copy", Image: CredentialGeneratorImage, ImagePullPolicy: corev1.PullIfNotPresent,
				Command:   []string{"/bin/sleep", "21600"},
				Resources: corev1.ResourceRequirements{Requests: res, Limits: res},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
					RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(k.RunAs), RunAsGroup: ptr.To(k.RunAs),
					Capabilities:   &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
				VolumeMounts: mounts,
			}},
			Volumes: vols,
		},
	}
}
