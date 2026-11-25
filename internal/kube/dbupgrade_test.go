package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
)

// cluster17 is the operand image earlier releases ran.
const cluster17 = "ghcr.io/cloudnative-pg/postgresql:17.12-minimal-trixie@sha256:4ae89311e5031155a139a5c6d31a80f6deb135443711e61ff7df41cb549fdcaa"

// upgradeFixture is KyIdentity deployed by an earlier release on a Cluster of three 17
// instances, database-1 the primary with a WAL claim, and an operator that upgrades the data
// in place once the Cluster wakes with an image of a higher major, unless fail is set.
type upgradeFixture struct {
	a      App // this release's, on the catalog's 18 image
	cs     *fake.Clientset
	dyn    *dynamicfake.FakeDynamicClient
	data   string // the image of the Cluster's data, as the operator records it
	fail   bool
	events []string // hibernation, image changes and copies, in order
	execs  [][]string
	copyFn func(args []string) error
}

func newUpgradeFixture(t *testing.T) *upgradeFixture {
	t.Helper()
	pollInterval = time.Millisecond
	ctx := context.Background()
	a, cs, dyn, _ := clusteredApp(t, 3)
	f := &upgradeFixture{a: a, cs: cs, dyn: dyn, data: cluster17}
	if imageMajor(f.want()) != 18 {
		t.Fatalf("the catalog's Cluster image is %s", f.want())
	}
	dyn.PrependReactor("get", "clusters", func(action k8stesting.Action) (bool, runtime.Object, error) {
		get := action.(k8stesting.GetAction)
		o, err := dyn.Tracker().Get(clusterResource, get.GetNamespace(), get.GetName())
		if err != nil {
			return true, nil, err
		}
		u := o.(*unstructured.Unstructured).DeepCopy()
		image, _, _ := unstructured.NestedString(u.Object, "spec", "imageName")
		awake := u.GetAnnotations()[hibernation] != "on"
		if awake && (imageMajor(image) == imageMajor(f.data) || imageMajor(image) > imageMajor(f.data) && !f.fail) {
			f.data = image
		}
		status := map[string]any{
			"pgDataImageInfo": map[string]any{"image": f.data, "majorVersion": int64(imageMajor(f.data))},
			"currentPrimary":  "database-1", "phase": "Upgrading Postgres major version", "readyInstances": int64(0),
		}
		if awake && image == f.data {
			status["phase"], status["readyInstances"] = clusterHealthy, int64(3)
		}
		u.Object["status"] = status
		return true, u, nil
	})
	record := func(action k8stesting.Action) (bool, runtime.Object, error) {
		var o map[string]any
		switch a := action.(type) {
		case k8stesting.PatchAction:
			_ = json.Unmarshal(a.GetPatch(), &o)
		case k8stesting.UpdateAction:
			o = a.GetObject().(*unstructured.Unstructured).Object
		}
		if v, found, _ := unstructured.NestedString(o, "metadata", "annotations", hibernation); found {
			f.events = append(f.events, "hibernation "+v)
		}
		if v, found, _ := unstructured.NestedString(o, "spec", "imageName"); found {
			f.events = append(f.events, fmt.Sprintf("image %d", imageMajor(v)))
		}
		return false, nil, nil
	}
	dyn.PrependReactor("patch", "clusters", record)
	dyn.PrependReactor("update", "clusters", record)

	// The earlier release's install.
	old := a
	old.Catalog.Images = maps.Clone(a.Catalog.Images)
	old.Catalog.Images[a.Catalog.Kubernetes.Replicas.ClusterImage] = cluster17
	for _, s := range Steps(old)[:4] {
		if err := s.Apply(ctx); err != nil {
			t.Fatalf("%s: %v", s.ID(), err)
		}
	}
	f.events = nil
	c := clusterOf(t, dyn)
	c.SetUID("cluster-uid")
	if err := dyn.Tracker().Update(clusterResource, c, c.GetNamespace()); err != nil {
		t.Fatal(err)
	}
	claim := func(name, instance, role, pvcRole string) {
		p := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kyq-kyidentity",
				Labels:          map[string]string{clusterLabel: databaseName, cnpgInstance: instance, cnpgRole: role, cnpgPVCRole: pvcRole},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: clusterGroup + "/v1", Kind: "Cluster", Name: databaseName, UID: "cluster-uid"}}},
			Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: ptr.To("longhorn"),
				Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")}}},
		}
		if _, err := cs.CoreV1().PersistentVolumeClaims("kyq-kyidentity").Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	claim("database-1", "database-1", "primary", "PG_DATA")
	claim("database-1-wal", "database-1", "primary", "PG_WAL")
	claim("database-2", "database-2", "replica", "PG_DATA")
	claim("database-3", "database-3", "replica", "PG_DATA")
	// Another Cluster's claim, which is never copied.
	claim("other-1", "other-1", "primary", "PG_DATA")
	o, _ := cs.CoreV1().PersistentVolumeClaims("kyq-kyidentity").Get(ctx, "other-1", metav1.GetOptions{})
	o.OwnerReferences[0].UID = "other-uid"
	if _, err := cs.CoreV1().PersistentVolumeClaims("kyq-kyidentity").Update(ctx, o, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	before := a.Client.execCommand
	a.Client.execCommand = func(ctx context.Context, ns, pod, container string, args []string) ([]byte, error) {
		switch container {
		case "copy":
			if len(args) < 4 || args[2] != copyCommand {
				return nil, errors.New("not the copy command")
			}
			o, _ := dyn.Tracker().Get(clusterResource, "kyq-kyidentity", databaseName)
			f.events = append(f.events, fmt.Sprintf("copy %v while hibernation %s", args[4:], o.(*unstructured.Unstructured).GetAnnotations()[hibernation]))
			if f.copyFn != nil {
				if err := f.copyFn(args[4:]); err != nil {
					return []byte(err.Error()), err
				}
			}
			return nil, nil
		case "postgres":
			f.execs = append(f.execs, append([]string{pod}, args...))
			return nil, nil
		}
		return before(ctx, ns, pod, container, args)
	}
	f.a = a
	return f
}

func (f *upgradeFixture) want() string {
	return f.a.Catalog.Images[f.a.Catalog.Kubernetes.Replicas.ClusterImage]
}

func (f *upgradeFixture) step() clusterUpgradeStep {
	for _, s := range Steps(f.a) {
		if u, ok := s.(clusterUpgradeStep); ok {
			return u
		}
	}
	panic("no upgrade step")
}

func (f *upgradeFixture) image(t *testing.T) string {
	t.Helper()
	image, _, _ := unstructured.NestedString(clusterOf(t, f.dyn).Object, "spec", "imageName")
	return image
}

// copies are the installer's copy claims, by name.
func (f *upgradeFixture) copies(t *testing.T) map[string]corev1.PersistentVolumeClaim {
	t.Helper()
	list, err := f.cs.CoreV1().PersistentVolumeClaims("kyq-kyidentity").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]corev1.PersistentVolumeClaim{}
	for _, p := range list.Items {
		if strings.HasPrefix(p.Name, "pre-upgrade-pg") {
			out[p.Name] = p
		}
	}
	return out
}

// noClaimDeleted fails the test if anything deleted a claim.
func (f *upgradeFixture) noClaimDeleted(t *testing.T) {
	t.Helper()
	for _, a := range f.cs.Actions() {
		if a.GetVerb() == "delete" && a.GetResource().Resource == "persistentvolumeclaims" {
			t.Fatalf("claim deleted: %v", a)
		}
	}
}

var wantCopies = []string{"pre-upgrade-pg17-database-1", "pre-upgrade-pg17-database-1-wal", "pre-upgrade-pg17-database-2", "pre-upgrade-pg17-database-3"}

func TestClusterUpgrade(t *testing.T) {
	ctx := context.Background()
	f := newUpgradeFixture(t)
	var pods []*corev1.Pod
	f.cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		pods = append(pods, action.(k8stesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy())
		return false, nil, nil
	})
	step := f.step()
	if done, err := step.Inspect(ctx); err != nil || done {
		t.Fatalf("a 17 Cluster needs no upgrade: %v", err)
	}
	// This release's deploy alone never moves the data.
	deploy := Steps(f.a)[3]
	if err := deploy.Apply(ctx); err == nil || !strings.Contains(err.Error(), "database-upgrade moves it first") || imageMajor(f.image(t)) != 17 {
		t.Fatalf("deploy over a 17 Cluster: %v, image %s", err, f.image(t))
	}
	if err := step.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	// Hibernated, every instance copied while it was, then the image and the wake together.
	want := []string{
		"hibernation on",
		"copy [database-1:pgdata database-1-wal:pg_wal] while hibernation on",
		"copy [database-2:pgdata] while hibernation on",
		"copy [database-3:pgdata] while hibernation on",
		"hibernation off", "image 18",
	}
	if !slices.Equal(f.events, want) {
		t.Fatalf("sequence:\n%s\nwant:\n%s", strings.Join(f.events, "\n"), strings.Join(want, "\n"))
	}
	if f.image(t) != f.want() || clusterOf(t, f.dyn).GetAnnotations()[hibernation] == "on" || imageMajor(f.data) != 18 {
		t.Fatalf("after the upgrade: image %s, data %s", f.image(t), f.data)
	}
	copies := f.copies(t)
	if got := slices.Sorted(maps.Keys(copies)); !slices.Equal(got, wantCopies) {
		t.Fatalf("copies = %v", got)
	}
	for name, c := range copies {
		src := strings.TrimPrefix(name, "pre-upgrade-pg17-")
		role := map[bool]string{true: "primary", false: "replica"}[strings.HasPrefix(src, "database-1")]
		if c.Annotations[copiedAnnotation] != "verified" || c.Annotations[copiedAt] == "" || c.Annotations[copyClusterUID] != "cluster-uid" || !owned(&c) ||
			c.Labels[copyOfLabel] != src || c.Labels[copyMajorLabel] != "17" || c.Labels[copyRole] != role || c.Labels[clusterLabel] != "" ||
			ptr.Deref(c.Spec.StorageClassName, "") != "longhorn" || c.Spec.Resources.Requests.Storage().String() != "2Gi" {
			t.Errorf("%s: %+v %+v", name, c.ObjectMeta, c.Spec)
		}
	}
	// One copy pod per instance, as the instances' postgres, reading the originals only.
	var copyPods int
	for _, p := range pods {
		if !strings.HasPrefix(p.GenerateName, "kyq-pre-upgrade-") {
			continue
		}
		copyPods++
		c := p.Spec.Containers[0]
		if *c.SecurityContext.RunAsUser != cnpgUID || *p.Spec.SecurityContext.FSGroup != cnpgUID {
			t.Errorf("copy pod runs as %d", *c.SecurityContext.RunAsUser)
		}
		for _, m := range c.VolumeMounts {
			if strings.HasPrefix(m.Name, "src-") && (!m.ReadOnly || m.SubPath != "pgdata" && m.SubPath != "pg_wal") {
				t.Errorf("source mount %+v", m)
			}
		}
	}
	if copyPods != 3 {
		t.Errorf("%d copy pods", copyPods)
	}
	// ANALYZE of what pg_upgrade carried no statistics for, on the primary.
	if len(f.execs) != 1 || f.execs[0][0] != "database-1" || !slices.Contains(f.execs[0], "--missing-stats-only") {
		t.Errorf("execs = %v", f.execs)
	}
	note := step.Note()
	for _, s := range append(wantCopies, "kubectl -n kyq-kyidentity delete pvc", "PostgreSQL 17 to 18", "ANALYZE ran on database-1") {
		if !strings.Contains(note, s) {
			t.Errorf("note lacks %q: %s", s, note)
		}
	}
	if err := step.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	// The deploy then finds the image it wants; a rerun copies nothing again.
	if err := deploy.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	f.events = nil
	if done, err := step.Inspect(ctx); err != nil || !done {
		t.Fatalf("rerun: %v", err)
	}
	if len(f.copies(t)) != 4 {
		t.Fatal("copies went")
	}
	f.noClaimDeleted(t)
}

// A copy that fails wakes the Cluster on 17, unchanged: no image change, nothing deleted.
func TestClusterUpgradeCopyFailure(t *testing.T) {
	ctx := context.Background()
	f := newUpgradeFixture(t)
	f.copyFn = func(args []string) error {
		if args[0] == "database-2:pgdata" {
			return errors.New("database-2: copied contents differ from the original")
		}
		return nil
	}
	step := f.step()
	err := step.Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "started again on PostgreSQL 17, unchanged") || !strings.Contains(err.Error(), "copied contents differ") {
		t.Fatalf("apply = %v", err)
	}
	if slices.Contains(f.events, "image 18") || f.image(t) != cluster17 || clusterOf(t, f.dyn).GetAnnotations()[hibernation] != "off" {
		t.Fatalf("after a failed copy: %v, image %s", f.events, f.image(t))
	}
	if c := f.copies(t)["pre-upgrade-pg17-database-2"]; c.Annotations[copiedAnnotation] == "verified" {
		t.Error("a failed copy is marked verified")
	}
	if err := step.Verify(ctx); err == nil {
		t.Fatal("verify passes on 17 data")
	}
	if err := Steps(f.a)[3].Apply(ctx); err == nil || imageMajor(f.image(t)) != 17 {
		t.Fatalf("deploy after a failed copy: %v", err)
	}
	f.noClaimDeleted(t)

	// The next run copies every claim again, from the 17 data that took writes since.
	f.copyFn, f.events = nil, nil
	if err := step.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(slices.DeleteFunc(slices.Clone(f.events), func(e string) bool { return !strings.HasPrefix(e, "copy ") })); n != 3 || f.image(t) != f.want() {
		t.Fatalf("rerun: %v", f.events)
	}
	f.noClaimDeleted(t)
}

// A claim the storage refuses stops nothing.
func TestClusterUpgradeClaimRefused(t *testing.T) {
	f := newUpgradeFixture(t)
	f.cs.PrependReactor("create", "persistentvolumeclaims", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(corev1.Resource("persistentvolumeclaims"), "x", errors.New("exceeded quota"))
	})
	err := f.step().Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exceeded quota") || !strings.Contains(err.Error(), "was not stopped") {
		t.Fatalf("apply = %v", err)
	}
	if len(f.events) != 0 || f.image(t) != cluster17 {
		t.Fatalf("events %v", f.events)
	}
}

// An operator older than 1.26 refuses the image: woken on 17, the copies kept.
func TestClusterUpgradeOperatorRefuses(t *testing.T) {
	f := newUpgradeFixture(t)
	f.dyn.PrependReactor("patch", "clusters", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if strings.Contains(string(action.(k8stesting.PatchAction).GetPatch()), "imageName") {
			return true, nil, apierrors.NewBadRequest("can't upgrade between majors")
		}
		return false, nil, nil
	})
	err := f.step().Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "1.26") || !strings.Contains(err.Error(), "started again on PostgreSQL 17") {
		t.Fatalf("apply = %v", err)
	}
	if f.image(t) != cluster17 || clusterOf(t, f.dyn).GetAnnotations()[hibernation] != "off" || len(f.copies(t)) != 4 {
		t.Fatalf("after the refusal: image %s, %d copies", f.image(t), len(f.copies(t)))
	}
	f.noClaimDeleted(t)
}

// A pg_upgrade that does not finish is rolled back as CloudNativePG documents: its image again.
func TestClusterUpgradeRollback(t *testing.T) {
	f := newUpgradeFixture(t)
	f.fail = true
	clusterTimeout = 30 * time.Millisecond
	t.Cleanup(func() { clusterTimeout = time.Minute })
	err := f.step().Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "set back to "+cluster17) || !strings.Contains(err.Error(), "copies stay") {
		t.Fatalf("apply = %v", err)
	}
	if f.image(t) != cluster17 || imageMajor(f.data) != 17 || !slices.Equal(f.events[len(f.events)-1:], []string{"image 17"}) {
		t.Fatalf("after the rollback: image %s, events %v", f.image(t), f.events)
	}
	if len(f.copies(t)) != 4 {
		t.Fatal("copies went")
	}
	f.noClaimDeleted(t)
}

// A copy that is not this Cluster's is never overwritten.
func TestClusterUpgradeForeignCopy(t *testing.T) {
	ctx := context.Background()
	f := newUpgradeFixture(t)
	p := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "pre-upgrade-pg17-database-2", Namespace: "kyq-kyidentity",
		Labels: workloadLabels("rs"), Annotations: map[string]string{copyClusterUID: "earlier-uid", copiedAnnotation: "verified"}}}
	if _, err := f.cs.CoreV1().PersistentVolumeClaims("kyq-kyidentity").Create(ctx, p, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := f.step().Apply(ctx); err == nil || !strings.Contains(err.Error(), "not this Cluster's copy") {
		t.Fatalf("apply = %v", err)
	}
	if len(f.events) != 0 || f.copies(t)["pre-upgrade-pg17-database-2"].Annotations[copiedAnnotation] != "verified" {
		t.Fatalf("events %v", f.events)
	}
}

// A fresh install starts on 18 directly: nothing to copy, nothing hibernated.
func TestClusterUpgradeFresh(t *testing.T) {
	pollInterval = time.Millisecond
	ctx := context.Background()
	a, cs, dyn, _ := clusteredApp(t, 3)
	var patched []types.PatchType
	dyn.PrependReactor("patch", "clusters", func(action k8stesting.Action) (bool, runtime.Object, error) {
		patched = append(patched, action.(k8stesting.PatchAction).GetPatchType())
		return false, nil, nil
	})
	for _, s := range Steps(a)[:4] {
		if done, err := s.Inspect(ctx); s.ID() == "kyidentity.database-upgrade" && (err != nil || !done) {
			t.Fatalf("upgrade step without a Cluster: %v", err)
		}
		if err := s.Apply(ctx); err != nil {
			t.Fatalf("%s: %v", s.ID(), err)
		}
	}
	if image, _, _ := unstructured.NestedString(clusterOf(t, dyn).Object, "spec", "imageName"); imageMajor(image) != 18 || len(patched) != 0 {
		t.Fatalf("fresh Cluster: %s, patches %v", image, patched)
	}
	list, _ := cs.CoreV1().PersistentVolumeClaims("kyq-kyidentity").List(ctx, metav1.ListOptions{})
	for _, p := range list.Items {
		if strings.HasPrefix(p.Name, "pre-upgrade-") {
			t.Fatalf("copy %s on a fresh install", p.Name)
		}
	}
}

func TestImageMajor(t *testing.T) {
	for ref, want := range map[string]int{
		cluster17: 17, "ghcr.io/cloudnative-pg/postgresql:18.7-minimal-trixie": 18, "localhost:5000/pg:16": 16,
		"localhost:5000/pg": 0, "pg@sha256:abc": 0, "pg:latest": 0,
	} {
		if got := imageMajor(ref); got != want {
			t.Errorf("%s: %d, want %d", ref, got, want)
		}
	}
}
