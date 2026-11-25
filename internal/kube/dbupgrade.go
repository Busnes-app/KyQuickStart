package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

// A Cluster an earlier release made holds PostgreSQL 17 data. Owner decision 2026-11-25: it
// moves to the catalog's newer major only like this. Hibernate it; copy every claim it owns
// into a claim of the installer's and verify the copy; only then set the new image and wake
// it, so CloudNativePG runs its offline in-place pg_upgrade --link; wait until it is healthy
// and ANALYZE what pg_upgrade left without statistics. A copy that cannot be made wakes it on
// the old major, unchanged. The copies are never deleted by the installer.
const (
	cnpgUID         = 26 // postgres in CloudNativePG's operand images, which runs the instances
	cnpgPVCRole     = "cnpg.io/pvcRole"
	cnpgInstance    = "cnpg.io/instanceName"
	cnpgRole        = "cnpg.io/instanceRole"
	copyOfLabel     = "kyquickstart/copy-of"  // the claim copied
	copyMajorLabel  = "kyquickstart/postgres" // the PostgreSQL major it holds
	copyInstance    = "kyquickstart/instance"
	copyRole        = "kyquickstart/instance-role" // primary or replica, when copied
	copiedAt        = "kyquickstart/copied-at"
	copyClusterUID  = "kyquickstart/cluster-uid"
	upgradeDocument = "docs/guided-setup.md, \"Going back to PostgreSQL 17\""
)

// cnpgDirs is the directory each of a Cluster's claims keeps, by its role. A claim of any
// other role (a tablespace's) is refused: the installer never makes one.
var cnpgDirs = map[string]string{"PG_DATA": "pgdata", "PG_WAL": "pg_wal"}

// ClusterCopyClaim names the copy of a Cluster's claim taken before upgrading from major.
func ClusterCopyClaim(major int, claim string) string {
	return fmt.Sprintf("pre-upgrade-pg%d-%s", major, claim)
}

// imageMajor is the PostgreSQL major of an operand image reference, from its tag as
// CloudNativePG reads it; 0 when it has none.
func imageMajor(ref string) int {
	ref, _, _ = strings.Cut(ref, "@")
	_, tag, found := strings.Cut(ref[strings.LastIndex(ref, "/")+1:], ":")
	if !found {
		return 0
	}
	end := strings.IndexFunc(tag, func(r rune) bool { return r < '0' || r > '9' })
	if end < 0 {
		end = len(tag)
	}
	n, _ := strconv.Atoi(tag[:end])
	return n
}

// dataMajor is the major of the Cluster's data: what the operator records (1.26 and later),
// else its image's.
func dataMajor(o *unstructured.Unstructured) int {
	if n, found, _ := unstructured.NestedInt64(o.Object, "status", "pgDataImageInfo", "majorVersion"); found && n > 0 {
		return int(n)
	}
	image, _, _ := unstructured.NestedString(o.Object, "spec", "imageName")
	return imageMajor(image)
}

type clusterUpgradeStep struct {
	a    App
	note *string
}

func (s clusterUpgradeStep) ID() string        { return s.a.Name + ".database-upgrade" }
func (s clusterUpgradeStep) InputHash() string { return s.want() }
func (s clusterUpgradeStep) Note() string      { return *s.note }

func (s clusterUpgradeStep) want() string {
	return s.a.Catalog.Images[s.a.Catalog.Kubernetes.Replicas.ClusterImage]
}

// Inspect is done without a Cluster (a fresh one starts on the catalog's image) and once its
// data is the catalog's major.
func (s clusterUpgradeStep) Inspect(ctx context.Context) (bool, error) {
	o, err := s.a.Client.clusterObject(ctx, s.a.Name)
	if err != nil || o == nil {
		return err == nil, err
	}
	to, from := imageMajor(s.want()), dataMajor(o)
	if to == 0 || from == 0 {
		return false, fmt.Errorf("cannot tell the PostgreSQL major of cluster %s (%d) or of image %s (%d)", databaseName, from, s.want(), to)
	}
	return from >= to, nil
}

func (s clusterUpgradeStep) Verify(ctx context.Context) error {
	done, err := s.Inspect(ctx)
	if err == nil && !done {
		err = fmt.Errorf("cluster %s still holds data older than PostgreSQL %d", databaseName, imageMajor(s.want()))
	}
	return err
}

func (s clusterUpgradeStep) Apply(ctx context.Context) error {
	a, c, want := s.a, s.a.Client, s.want()
	o, err := c.clusterObject(ctx, a.Name)
	if err != nil || o == nil {
		return err
	}
	from, to := dataMajor(o), imageMajor(want)
	if from >= to {
		return nil
	}
	claims, err := a.clusterClaims(ctx, o)
	if err != nil {
		return err
	}
	ns := namespaceOf(a.Name)
	var names []string
	for _, p := range claims {
		names = append(names, ClusterCopyClaim(from, p.Name))
	}
	copies := fmt.Sprintf("copies %s of claims %s in namespace %s", strings.Join(names, ", "), strings.Join(claimNames(claims), ", "), ns)
	keep := fmt.Sprintf("The copies stay until you delete them: kubectl -n %s delete pvc %s. Rollback from them: %s", ns, strings.Join(names, " "), upgradeDocument)
	// A run that stopped after the switch below finds the new image set: the copies are made.
	if image, _, _ := unstructured.NestedString(o.Object, "spec", "imageName"); imageMajor(image) == from {
		if err := a.copyCluster(ctx, o, claims, from); err != nil {
			return err
		}
		// One write sets the image and wakes the Cluster: the operator upgrades it on waking.
		patch := map[string]any{
			"metadata": map[string]any{"resourceVersion": o.GetResourceVersion(), "annotations": map[string]string{hibernation: "off"}},
			"spec":     map[string]any{"imageName": want},
		}
		if err := c.patchCluster(ctx, a.Name, patch); err != nil {
			werr := errors.Join(c.hibernate(ctx, a.Name, false), c.waitCluster(ctx, a.Name))
			return errors.Join(fmt.Errorf("cluster %s refused PostgreSQL %d (%w; in-place major upgrades need CloudNativePG 1.26 or later): it was started again on PostgreSQL %d, unchanged. %s", databaseName, to, err, from, keep), werr)
		}
	}
	upgraded := func(o *unstructured.Unstructured) string {
		if why := clusterState(o); why != "" {
			return why
		}
		if dataMajor(o) < to {
			return fmt.Sprintf("cluster %s holds PostgreSQL %d data", databaseName, dataMajor(o))
		}
		return ""
	}
	if err := c.waitClusterFor(ctx, a.Name, upgraded); err != nil {
		o, gerr := c.clusterObject(ctx, a.Name)
		if gerr != nil || o == nil || dataMajor(o) >= to {
			return fmt.Errorf("the upgrade to PostgreSQL %d ran but the Cluster is not healthy yet (%w); apply again to wait for it. %s", to, err, keep)
		}
		// CloudNativePG's documented rollback of a failed upgrade: the previous image again,
		// and the operator deletes the upgrade's job and starts the unchanged data.
		previous, _, _ := unstructured.NestedString(o.Object, "status", "pgDataImageInfo", "image")
		if previous == "" {
			return fmt.Errorf("the upgrade to PostgreSQL %d did not finish (%w), and the Cluster records no previous image to go back to. %s", to, err, keep)
		}
		rerr := c.patchCluster(ctx, a.Name, map[string]any{"spec": map[string]any{"imageName": previous}})
		if rerr == nil {
			rerr = c.waitCluster(ctx, a.Name)
		}
		return errors.Join(fmt.Errorf("the upgrade to PostgreSQL %d did not finish (%w): the image was set back to %s, as CloudNativePG documents for a failed upgrade, and the Cluster runs PostgreSQL %d on its unchanged data. %s", to, err, previous, from, keep), rerr)
	}
	*s.note = fmt.Sprintf("PostgreSQL %d to %d: %s (verified against the originals before the upgrade). %s", from, to, copies, keep)
	if msg := a.analyze(ctx); msg != "" {
		*s.note += ". " + msg
	}
	return nil
}

// analyze gathers the statistics pg_upgrade did not carry over, on the primary. A failure is
// reported, not fatal: the upgrade is done, and autovacuum gathers them in time.
func (a App) analyze(ctx context.Context) string {
	o, err := a.Client.clusterObject(ctx, a.Name)
	if err != nil || o == nil {
		return fmt.Sprintf("ANALYZE was not run (%v)", err)
	}
	primary, _, _ := unstructured.NestedString(o.Object, "status", "currentPrimary")
	cmd := []string{"vacuumdb", "--all", "--analyze-only", "--missing-stats-only"}
	if out, err := a.Client.ExecReport(ctx, namespaceOf(a.Name), primary, "postgres", cmd); err != nil {
		return fmt.Sprintf("ANALYZE failed (%v: %s); run it yourself: kubectl -n %s exec %s -c postgres -- %s", err, strings.TrimSpace(string(out)), namespaceOf(a.Name), primary, strings.Join(cmd, " "))
	}
	return "ANALYZE ran on " + primary
}

// patchCluster merge-patches the app's Cluster.
func (c *Client) patchCluster(ctx context.Context, app string, patch map[string]any) error {
	b, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	if _, err := c.dyn.Resource(clusterResource).Namespace(namespaceOf(app)).Patch(ctx, databaseName, types.MergePatchType, b, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("cluster %s: %w", databaseName, err)
	}
	return nil
}

func claimNames(claims []corev1.PersistentVolumeClaim) []string {
	var out []string
	for _, p := range claims {
		out = append(out, p.Name)
	}
	return out
}

// clusterClaims are the claims the Cluster owns, by name. Listing them needs list on claims,
// which a plan with database_instances asks for.
func (a App) clusterClaims(ctx context.Context, o *unstructured.Unstructured) ([]corev1.PersistentVolumeClaim, error) {
	ns := namespaceOf(a.Name)
	list, err := a.Client.cs.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{LabelSelector: clusterLabel + "=" + databaseName})
	if err != nil {
		return nil, fmt.Errorf("list the claims of cluster %s: %w", databaseName, err)
	}
	var out []corev1.PersistentVolumeClaim
	for _, p := range list.Items {
		if p.DeletionTimestamp != nil || !slices.ContainsFunc(p.OwnerReferences, func(r metav1.OwnerReference) bool { return r.UID == o.GetUID() }) {
			continue
		}
		if cnpgDirs[p.Labels[cnpgPVCRole]] == "" {
			return nil, fmt.Errorf("claim %s of cluster %s has role %q, which the pre-upgrade copy does not cover", p.Name, databaseName, p.Labels[cnpgPVCRole])
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cluster %s owns no claims to copy", databaseName)
	}
	slices.SortFunc(out, func(x, y corev1.PersistentVolumeClaim) int { return strings.Compare(x.Name, y.Name) })
	return out, nil
}

// copyCluster makes a verified copy of each of the Cluster's claims while it is hibernated,
// and wakes it again on its old major, unchanged, if any copy fails. A copy from an earlier
// attempt on this Cluster is replaced: the data it copied has not changed since but has taken
// writes. A copy of another Cluster (one recreated from the copies) is never touched.
func (a App) copyCluster(ctx context.Context, o *unstructured.Unstructured, claims []corev1.PersistentVolumeClaim, from int) (err error) {
	pvcs := a.Client.cs.CoreV1().PersistentVolumeClaims(namespaceOf(a.Name))
	// The claims first, while the Cluster runs: a quota or class refusal stops nothing.
	for _, p := range claims {
		name := ClusterCopyClaim(from, p.Name)
		have, err := pvcs.Get(ctx, name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			role := "replica"
			if p.Labels[cnpgRole] == "primary" {
				role = "primary"
			}
			labels := workloadLabels(a.ReleaseSet)
			labels[copyOfLabel], labels[copyMajorLabel], labels[copyInstance], labels[copyRole] = p.Name, strconv.Itoa(from), p.Labels[cnpgInstance], role
			c := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.Namespace, Labels: labels, Annotations: map[string]string{copyClusterUID: string(o.GetUID())}},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: p.Spec.Resources.Requests[corev1.ResourceStorage]}},
					StorageClassName: p.Spec.StorageClassName,
				},
			}
			if _, err := pvcs.Create(ctx, c, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("no upgrade: create claim %s: %w; cluster %s was not stopped", name, err, databaseName)
			}
		case err != nil:
			return fmt.Errorf("claim %s: %w", name, err)
		case !owned(have) || have.Annotations[copyClusterUID] != string(o.GetUID()):
			return fmt.Errorf("no upgrade: claim %s exists and is not this Cluster's copy (perhaps an earlier Cluster's, see %s); keep or delete it by hand, then apply again; cluster %s was not stopped", name, upgradeDocument, databaseName)
		}
		// Unverified until this run's copy is.
		if _, err := pvcs.Patch(ctx, name, types.MergePatchType, []byte(`{"metadata":{"annotations":{"`+copiedAnnotation+`":null}}}`), metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("claim %s: %w", name, err)
		}
	}
	if err := a.Client.hibernate(ctx, a.Name, true); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			wake := context.WithoutCancel(ctx)
			err = errors.Join(fmt.Errorf("no upgrade: %w; cluster %s was started again on PostgreSQL %d, unchanged, and its image was not changed", err, databaseName, from),
				a.Client.hibernate(wake, a.Name, false), a.Client.waitCluster(wake, a.Name))
		}
	}()
	if err := waitGone(ctx, a, func(ctx context.Context) error {
		left, err := a.Client.podsLeft(ctx, a.Name, clusterPods)
		if err == nil && left {
			err = fmt.Errorf("an instance of cluster %s is still running", databaseName)
		}
		return err
	}); err != nil {
		return err
	}
	// One pod per instance: on node-local storage each instance's claims are on its own node.
	byInstance := map[string][]claimCopy{}
	for _, p := range claims {
		i := p.Labels[cnpgInstance]
		byInstance[i] = append(byInstance[i], claimCopy{name: p.Name, src: p.Name, dir: cnpgDirs[p.Labels[cnpgPVCRole]], dst: ClusterCopyClaim(from, p.Name)})
	}
	sc := &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true), FSGroup: ptr.To(int64(cnpgUID)), FSGroupChangePolicy: ptr.To(corev1.FSGroupChangeOnRootMismatch),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	for _, i := range slices.Sorted(maps.Keys(byInstance)) {
		if err := a.copyClaims(ctx, a.copyPod(byInstance[i], cnpgUID, sc), byInstance[i]); err != nil {
			return err
		}
	}
	at := time.Now().UTC().Format(time.RFC3339)
	for _, p := range claims {
		name := ClusterCopyClaim(from, p.Name)
		b, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{copiedAnnotation: "verified", copiedAt: at}}})
		if _, err := pvcs.Patch(ctx, name, types.MergePatchType, b, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("mark %s verified: %w", name, err)
		}
	}
	return nil
}
