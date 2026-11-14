package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// copiedAnnotation marks a pre-upgrade claim whose copy was verified against the original.
const copiedAnnotation = "kyquickstart/pre-upgrade"

// copyCommand copies each named volume's private directory into its pre-upgrade claim,
// replacing what an interrupted run left, then compares contents, modes, owners, sizes,
// times and link targets with the original. A link's own time is left out: BusyBox cp -a
// does not keep it.
const copyCommand = `exec 2>&1
for v in "$@"; do
  s=/src/$v d=/dst/$v/` + privateDir + `
  rm -rf "$d"
  mkdir -m 700 "$d"
  cp -a "$s/." "$d/"
  diff -r "$s" "$d" >/dev/null || { echo "$v: copied contents differ from the original"; exit 1; }
  l() { (cd "$1" && { find . ! -type l -exec stat -c '%n %f %u %g %s %Y' {} +; find . -type l -exec stat -c '%n %u %g %N' {} +; } | sort) > "$2"; }
  l "$s" /tmp/src; l "$d" /tmp/dst
  cmp -s /tmp/src /tmp/dst || { echo "$v: copied modes, owners or times differ from the original:"; diff /tmp/src /tmp/dst | grep '^[-+][^-+]' | head -10; exit 1; }
done`

// PreUpgradeClaim names volume's copy taken before upgrading from version.
func PreUpgradeClaim(from, volume string) string { return "pre-upgrade-" + from + "-vol-" + volume }

// copyClaimRE matches PreUpgradeClaim's names, never an app's own vol-<name> claims.
var copyClaimRE = regexp.MustCompile(`^pre-upgrade-([0-9]+\.[0-9]+\.[0-9]+(?:\.[0-9]+)?)-vol-[a-z0-9-]+$`)

// Copy is one pre-upgrade claim in an app's namespace.
type Copy struct {
	Claim, From, Size string
	Verified          bool
}

// PreUpgradeCopies lists the installer's pre-upgrade claims in the app's namespace, by name.
// It needs list on persistentvolumeclaims, which preflight does not require.
func PreUpgradeCopies(ctx context.Context, a App) ([]Copy, error) {
	list, err := a.Client.cs.CoreV1().PersistentVolumeClaims(namespaceOf(a.Name)).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list the claims in %s: %w", namespaceOf(a.Name), err)
	}
	var out []Copy
	for _, c := range list.Items {
		m := copyClaimRE.FindStringSubmatch(c.Name)
		if m == nil || !owned(&c) {
			continue
		}
		q := c.Spec.Resources.Requests[corev1.ResourceStorage]
		out = append(out, Copy{Claim: c.Name, From: m[1], Size: q.String(), Verified: c.Annotations[copiedAnnotation] == "verified"})
	}
	slices.SortFunc(out, func(x, y Copy) int { return strings.Compare(x.Claim, y.Claim) })
	return out, nil
}

// DeletePreUpgradeCopy deletes one of the installer's pre-upgrade claims, and nothing else:
// a name PreUpgradeClaim does not make or a claim without the managed label is refused.
func DeletePreUpgradeCopy(ctx context.Context, a App, claim string) error {
	if !copyClaimRE.MatchString(claim) {
		return fmt.Errorf("%s is not a pre-upgrade copy", claim)
	}
	pvcs := a.Client.cs.CoreV1().PersistentVolumeClaims(namespaceOf(a.Name))
	c, found, err := getOwned(ctx, pvcs, "claim", claim)
	if err != nil || !found {
		return err
	}
	uid := c.UID
	if err := pvcs.Delete(ctx, claim, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete claim %s: %w", claim, err)
	}
	return nil
}

// PreUpgradeRefusal says why PreUpgradeCopy cannot cover the app, nil when it can.
func PreUpgradeRefusal(a App) error {
	k := a.Catalog.Kubernetes
	switch {
	case k == nil || len(k.Volumes) == 0:
		return fmt.Errorf("%s keeps no volumes on Kubernetes", a.Name)
	case k.RunAs == 0:
		return fmt.Errorf("%s runs as root, whose volumes the pre-upgrade copy does not cover", a.Name)
	case a.replicated():
		return fmt.Errorf("%s runs as replicas on Postgres, which the pre-upgrade copy does not cover: take a backup in its own Backups screen first", a.Name)
	}
	return nil
}

// PreUpgradeCopy creates a claim named for from for each of the app's volumes and proves the
// target storage can provide them, then stops the app, copies each volume, verifies the copy
// and starts the app again, whatever happened. Claims a run already verified are kept, never
// copied over: after a failed deploy the app's volumes may hold migrated data. It returns the
// claims.
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
	for _, v := range todo {
		if err := a.preUpgradeClaim(ctx, from, v); err != nil {
			return nil, err
		}
	}
	// The copy pod, started once on the app's node while the app still runs: a claim the
	// storage cannot provide fails here, and node-local storage provisions each copy beside
	// its original.
	check := a.copyPod(from, todo)
	if node, err := a.appNode(ctx); err != nil {
		return nil, err
	} else if node != "" {
		check.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{node}}}}},
		}}}
	}
	start := time.Now()
	if err := a.Client.podSession(ctx, check, "pre-upgrade storage check", func(string) error { return nil }); err != nil {
		return nil, a.storageRefusal(ctx, from, todo, start, err)
	}
	stop := lifecycleStep{a: a, op: "stop"}
	if err := stop.patch(ctx, map[string]any{"spec": map[string]any{"replicas": 0}}); err != nil {
		return nil, fmt.Errorf("stop for the pre-upgrade copy: %w", err)
	}
	// Scaled to zero: started again however the rest ends, also when the wait for its pods to
	// drain is cancelled or times out.
	defer func() {
		if serr := (lifecycleStep{a: a, op: "start"}).Apply(context.WithoutCancel(ctx)); serr != nil {
			err = errors.Join(err, fmt.Errorf("start %s again: %w", a.Name, serr))
		}
	}()
	if err := stop.waitStopped(ctx); err != nil {
		return nil, fmt.Errorf("stop for the pre-upgrade copy: %w", err)
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

// storageRefusal explains a storage check that failed: the cluster's warnings about the new
// claims and the check's pod since start, and the earlier copies taking space. Events and the
// claim list need list permissions preflight does not require; without them those parts are
// left out.
func (a App) storageRefusal(ctx context.Context, from string, volumes []string, start time.Time, cause error) error {
	ns := namespaceOf(a.Name)
	var names []string
	for _, v := range volumes {
		names = append(names, PreUpgradeClaim(from, v))
	}
	msg := fmt.Sprintf("the target storage did not provide the pre-upgrade copies %s (%v); %s was not stopped", strings.Join(names, ", "), cause, a.Name)
	if events, err := a.Client.cs.CoreV1().Events(ns).List(ctx, metav1.ListOptions{}); err == nil {
		var seen []string
		for _, e := range events.Items {
			at := e.LastTimestamp.Time
			if at.IsZero() {
				at = e.EventTime.Time
			}
			ours := slices.Contains(names, e.InvolvedObject.Name) || e.InvolvedObject.Kind == "Pod" && strings.HasPrefix(e.InvolvedObject.Name, "kyq-pre-upgrade-")
			text := strings.Join(strings.Fields(e.Reason+": "+e.Message), " ")
			if e.Type != corev1.EventTypeWarning || !ours || at.Before(start.Add(-time.Second)) || slices.Contains(seen, text) {
				continue
			}
			seen = append(seen, text)
		}
		if n := len(seen); n > 5 {
			seen = seen[n-5:]
		}
		for i, t := range seen {
			if len(t) > 300 {
				seen[i] = t[:300] + "..."
			}
		}
		if len(seen) > 0 {
			msg += "; the cluster reports: " + strings.Join(seen, "; ")
		}
	}
	if copies, err := PreUpgradeCopies(ctx, a); err == nil {
		var older []string
		for _, c := range copies {
			if catalog.VersionAtLeast(from, c.From) && from != c.From {
				older = append(older, c.Claim+" ("+c.Size+")")
			}
		}
		if len(older) > 0 {
			msg += fmt.Sprintf("; %d earlier copies take space: %s", len(older), strings.Join(older, ", "))
		}
	}
	msg += fmt.Sprintf("; free space (kyquickstart prune-copies %s removes copies older than the installed version) and run the upgrade again", a.Name)
	if a.Redact != nil {
		msg = a.Redact.Redact(msg)
	}
	return errors.New(msg)
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

// appNode is the node the app's pods run on, "" when none is scheduled or they are on several.
func (a App) appNode(ctx context.Context) (string, error) {
	list, err := a.Client.cs.CoreV1().Pods(namespaceOf(a.Name)).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(selector(a.Name)).String()})
	if err != nil {
		return "", fmt.Errorf("pods of %s: %w", a.Name, err)
	}
	var nodes []string
	for _, p := range list.Items {
		if owned(&p) && p.DeletionTimestamp == nil && p.Spec.NodeName != "" && !slices.Contains(nodes, p.Spec.NodeName) {
			nodes = append(nodes, p.Spec.NodeName)
		}
	}
	if len(nodes) != 1 {
		return "", nil
	}
	return nodes[0], nil
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
