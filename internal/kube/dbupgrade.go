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
// other role is refused before anything stops: the installer never makes one. A WAL claim
// (walStorage) would leave pgdata/pg_wal a link to a path the copy pod does not mount.
var cnpgDirs = map[string]string{"PG_DATA": "pgdata"}

// ClusterCopyClaim names the copy of a Cluster's claim taken before upgrading from major, by
// the attempt that made it at stamp.
func ClusterCopyClaim(major int, claim, stamp string) string {
	return fmt.Sprintf("pre-upgrade-pg%d-%s-%s", major, claim, stamp)
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
	from, to, uid := dataMajor(o), imageMajor(want), o.GetUID()
	if from >= to {
		return nil
	}
	// A run that stopped after the switch below finds the new image set: the copies are made.
	if image, _, _ := unstructured.NestedString(o.Object, "spec", "imageName"); imageMajor(image) == from {
		claims, err := a.clusterClaims(ctx, o)
		if err != nil {
			return err
		}
		if err := a.copyAndSwitch(ctx, o, claims, from, image, want); err != nil {
			return fmt.Errorf("%w. %s", err, a.keepCopies(ctx, uid))
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
			return fmt.Errorf("the upgrade to PostgreSQL %d ran but the Cluster is not healthy yet (%w); apply again to wait for it. %s", to, err, a.keepCopies(ctx, uid))
		}
		// CloudNativePG's documented rollback of a failed upgrade: the previous image again,
		// and the operator deletes the upgrade's job and starts the unchanged data.
		previous, _, _ := unstructured.NestedString(o.Object, "status", "pgDataImageInfo", "image")
		if previous == "" {
			return fmt.Errorf("the upgrade to PostgreSQL %d did not finish (%w), and the Cluster records no previous image to go back to. %s", to, err, a.keepCopies(ctx, uid))
		}
		rerr := c.patchCluster(ctx, a.Name, map[string]any{"spec": map[string]any{"imageName": previous}})
		if rerr == nil {
			rerr = c.waitCluster(ctx, a.Name)
		}
		return errors.Join(fmt.Errorf("the upgrade to PostgreSQL %d did not finish (%w): the image was set back to %s, as CloudNativePG documents for a failed upgrade, and the Cluster runs PostgreSQL %d on its unchanged data. %s", to, err, previous, from, a.keepCopies(ctx, uid)), rerr)
	}
	*s.note = fmt.Sprintf("PostgreSQL %d to %d (copies verified against the originals before the upgrade). %s", from, to, a.keepCopies(ctx, uid))
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
			return nil, fmt.Errorf("no upgrade: claim %s of cluster %s has role %q (a separate WAL or tablespace claim, which the installer never makes); the pre-upgrade copy does not cover it, and nothing was stopped", p.Name, databaseName, p.Labels[cnpgPVCRole])
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cluster %s owns no claims to copy", databaseName)
	}
	slices.SortFunc(out, func(x, y corev1.PersistentVolumeClaim) int { return strings.Compare(x.Name, y.Name) })
	return out, nil
}

// copyStamp is the UTC time in a copy's name: every attempt makes copies of its own.
var copyStamp = func() string { return time.Now().UTC().Format("20060102-150405") }

// clusterCopies are the installer's copies in the app's namespace, by name: of the Cluster
// with uid, or of any Cluster when uid is "".
func (a App) clusterCopies(ctx context.Context, uid types.UID) ([]corev1.PersistentVolumeClaim, error) {
	list, err := a.Client.cs.CoreV1().PersistentVolumeClaims(namespaceOf(a.Name)).List(ctx, metav1.ListOptions{LabelSelector: copyOfLabel})
	if err != nil {
		return nil, fmt.Errorf("list the copies in %s: %w", namespaceOf(a.Name), err)
	}
	out := slices.DeleteFunc(list.Items, func(p corev1.PersistentVolumeClaim) bool {
		return !owned(&p) || uid != "" && p.Annotations[copyClusterUID] != string(uid)
	})
	slices.SortFunc(out, func(x, y corev1.PersistentVolumeClaim) int { return strings.Compare(x.Name, y.Name) })
	return out, nil
}

// keepCopies lists the Cluster's copies and how to remove them, which only the operator does.
func (a App) keepCopies(ctx context.Context, uid types.UID) string {
	ns := namespaceOf(a.Name)
	copies, err := a.clusterCopies(ctx, uid)
	if err != nil {
		return fmt.Sprintf("Its copies could not be listed (%v): kubectl -n %s get pvc -l %s", err, ns, copyOfLabel)
	}
	if len(copies) == 0 {
		return "No copies were made"
	}
	var names, list []string
	for _, p := range copies {
		state := "unverified"
		if p.Annotations[copiedAnnotation] == "verified" {
			state = "verified " + p.Annotations[copiedAt]
		}
		names = append(names, p.Name)
		list = append(list, fmt.Sprintf("%s (of %s, %s)", p.Name, p.Labels[copyOfLabel], state))
	}
	return fmt.Sprintf("Copies of cluster %s in namespace %s: %s. They stay until you delete them: kubectl -n %s delete pvc %s. Rollback from a verified one: %s",
		databaseName, ns, strings.Join(list, ", "), ns, strings.Join(names, " "), upgradeDocument)
}

// copyAndSwitch makes a verified copy of each of the Cluster's claims while it is hibernated,
// then sets image want and wakes it in one patch. Anything that fails once it is hibernated
// wakes it on its old image, unchanged. Each attempt makes new copies, beside any an earlier
// one left: none is ever replaced or deleted.
func (a App) copyAndSwitch(ctx context.Context, o *unstructured.Unstructured, claims []corev1.PersistentVolumeClaim, from int, image, want string) (err error) {
	pvcs := a.Client.cs.CoreV1().PersistentVolumeClaims(namespaceOf(a.Name))
	stamp := copyStamp()
	byInstance := map[string][]claimCopy{}
	// The claims first, while the Cluster runs: a quota or class refusal stops nothing.
	for _, p := range claims {
		name := ClusterCopyClaim(from, p.Name, stamp)
		labels := workloadLabels(a.ReleaseSet)
		labels[copyOfLabel], labels[copyMajorLabel], labels[copyInstance], labels[copyRole] = p.Name, strconv.Itoa(from), p.Labels[cnpgInstance], instanceRole(p)
		c := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: p.Namespace, Labels: labels, Annotations: map[string]string{copyClusterUID: string(o.GetUID())}},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: p.Spec.Resources.Requests[corev1.ResourceStorage]}},
				StorageClassName: p.Spec.StorageClassName,
			},
		}
		if _, err := pvcs.Create(ctx, c, metav1.CreateOptions{}); err != nil {
			return a.clusterStorageRefusal(ctx, []string{name}, time.Now(), fmt.Errorf("create claim %s: %w", name, err))
		}
		i := p.Labels[cnpgInstance]
		byInstance[i] = append(byInstance[i], claimCopy{name: p.Name, src: p.Name, dir: cnpgDirs[p.Labels[cnpgPVCRole]], dst: name})
	}
	instances := slices.Sorted(maps.Keys(byInstance))
	sc := &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true), FSGroup: ptr.To(int64(cnpgUID)), FSGroupChangePolicy: ptr.To(corev1.FSGroupChangeOnRootMismatch),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	// The storage check: each instance's copy pod, started once on that instance's node while
	// it still runs, so the storage provisions every copy (beside its original on node-local
	// storage) before sign-in stops. A copy the storage cannot provide refuses here.
	for _, i := range instances {
		check := a.copyPod(byInstance[i], cnpgUID, sc)
		if pod, err := a.Client.cs.CoreV1().Pods(namespaceOf(a.Name)).Get(ctx, i, metav1.GetOptions{}); err == nil && pod.Spec.NodeName != "" {
			pinTo(check, pod.Spec.NodeName)
		}
		start := time.Now()
		if err := a.Client.podSession(ctx, check, "pre-upgrade storage check", func(string) error { return nil }); err != nil {
			var names []string
			for _, c := range byInstance[i] {
				names = append(names, c.dst)
			}
			return a.clusterStorageRefusal(ctx, names, start, err)
		}
	}
	// From here on every way out but success wakes the Cluster on its old image, unless the
	// switch to the new one may have landed. Registered before the hibernation, whose write
	// may land even when its answer is lost.
	unknown := false
	defer func() {
		if err != nil && !unknown {
			wake := context.WithoutCancel(ctx)
			werr := a.Client.patchCluster(wake, a.Name, map[string]any{
				"metadata": map[string]any{"uid": o.GetUID(), "annotations": map[string]string{hibernation: "off"}},
				"spec":     map[string]any{"imageName": image},
			})
			if werr == nil {
				werr = a.Client.waitCluster(wake, a.Name)
			}
			if werr != nil {
				err = fmt.Errorf("no upgrade: %w; then starting cluster %s again on PostgreSQL %d failed (%w): check with kubectl -n %s get cluster %s -o yaml that imageName is %s and annotation %s is off, and set them so",
					err, databaseName, from, werr, namespaceOf(a.Name), databaseName, image, hibernation)
				return
			}
			err = fmt.Errorf("no upgrade: %w; cluster %s was started again on PostgreSQL %d, unchanged", err, databaseName, from)
		}
	}()
	if err := a.Client.hibernate(ctx, a.Name, true); err != nil {
		return err
	}
	if err := waitGone(ctx, a, func(ctx context.Context) error {
		left, err := a.Client.podsLeft(ctx, a.Name, clusterPods)
		if err == nil && left {
			err = fmt.Errorf("an instance of cluster %s is still running", databaseName)
		}
		return err
	}); err != nil {
		return err
	}
	// The claims and their roles as the Cluster stopped: a failover since the list above moves
	// the primary, and the rollback restores the copy labelled primary.
	stopped, err := a.clusterClaims(ctx, o)
	if err != nil {
		return err
	}
	role := map[string]string{}
	for _, p := range stopped {
		role[p.Name] = instanceRole(p)
	}
	if !slices.EqualFunc(stopped, claims, func(x, y corev1.PersistentVolumeClaim) bool { return x.Name == y.Name }) {
		return fmt.Errorf("the claims of cluster %s changed while it stopped (an instance was added or removed)", databaseName)
	}
	// One pod per instance: on node-local storage each instance's claims are on its own node.
	for _, i := range instances {
		if err := a.copyClaims(ctx, a.copyPod(byInstance[i], cnpgUID, sc), byInstance[i]); err != nil {
			return err
		}
	}
	at := time.Now().UTC().Format(time.RFC3339)
	for _, i := range instances {
		for _, c := range byInstance[i] {
			b, _ := json.Marshal(map[string]any{"metadata": map[string]any{
				"labels":      map[string]string{copyRole: role[c.src]},
				"annotations": map[string]string{copiedAnnotation: "verified", copiedAt: at},
			}})
			if _, err := pvcs.Patch(ctx, c.dst, types.MergePatchType, b, metav1.PatchOptions{}); err != nil {
				return fmt.Errorf("mark %s verified: %w", c.dst, err)
			}
		}
	}
	// Still the Cluster that was copied, as it was copied. The patches name its uid, so a
	// Cluster recreated since is refused by the API server, never changed.
	now, err := a.Client.clusterObject(ctx, a.Name)
	if err != nil {
		return err
	}
	if now == nil || now.GetUID() != o.GetUID() {
		return fmt.Errorf("cluster %s was deleted or recreated during the copy", databaseName)
	}
	if have, _, _ := unstructured.NestedString(now.Object, "spec", "imageName"); have != image || now.GetAnnotations()[hibernation] != "on" {
		return fmt.Errorf("cluster %s changed during the copy (image %s, hibernation %q)", databaseName, have, now.GetAnnotations()[hibernation])
	}
	// One write sets the image and wakes the Cluster: the operator upgrades it on waking.
	if err := a.Client.patchCluster(ctx, a.Name, map[string]any{
		"metadata": map[string]any{"uid": o.GetUID(), "annotations": map[string]string{hibernation: "off"}},
		"spec":     map[string]any{"imageName": want},
	}); err != nil {
		// A switch that landed with its answer lost already woke the Cluster, and the operator
		// may be upgrading it: never set 17 back then; the caller's wait decides.
		now, rerr := a.Client.clusterObject(context.WithoutCancel(ctx), a.Name)
		if rerr != nil || now == nil {
			unknown = true
			return fmt.Errorf("cluster %s: the switch to PostgreSQL %d failed (%w) and its state cannot be read (%v): if kubectl -n %s get cluster %s shows imageName %s, apply again to wait for the upgrade; if %s, annotate it %s=off",
				databaseName, imageMajor(want), err, rerr, namespaceOf(a.Name), databaseName, want, image, hibernation)
		}
		if have, _, _ := unstructured.NestedString(now.Object, "spec", "imageName"); have == want && now.GetUID() == o.GetUID() {
			return nil
		}
		return fmt.Errorf("cluster %s refused PostgreSQL %d (%w; in-place major upgrades need CloudNativePG 1.26 or later)", databaseName, imageMajor(want), err)
	}
	return nil
}

// instanceRole is a claim's instance role as the operator labels it: primary or replica.
func instanceRole(p corev1.PersistentVolumeClaim) string {
	if p.Labels[cnpgRole] == "primary" {
		return "primary"
	}
	return "replica"
}

// clusterStorageRefusal explains copies the storage did not provide, before the Cluster was
// stopped, naming every earlier copy that takes space; the operator may delete those.
func (a App) clusterStorageRefusal(ctx context.Context, names []string, start time.Time, cause error) error {
	ns := namespaceOf(a.Name)
	msg := fmt.Sprintf("no upgrade: the storage did not provide the pre-upgrade copies %s (%v); cluster %s was not stopped%s", strings.Join(names, ", "), cause, databaseName, a.warnings(ctx, names, start))
	if copies, err := a.clusterCopies(ctx, ""); err == nil {
		var older []string
		for _, p := range copies {
			if slices.Contains(names, p.Name) {
				continue
			}
			q := p.Spec.Resources.Requests[corev1.ResourceStorage]
			older = append(older, fmt.Sprintf("%s (%s, %s)", p.Name, q.String(), map[bool]string{true: "verified", false: "unverified"}[p.Annotations[copiedAnnotation] == "verified"]))
		}
		if len(older) > 0 {
			msg += fmt.Sprintf("; earlier copies take space: %s. Delete those you no longer need yourself (kubectl -n %s delete pvc <name>) and apply again", strings.Join(older, ", "), ns)
		}
	}
	if a.Redact != nil {
		msg = a.Redact.Redact(msg)
	}
	return errors.New(msg)
}
