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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
)

// cluster17 is the operand image earlier releases ran.
const cluster17 = "ghcr.io/cloudnative-pg/postgresql:17.12-minimal-trixie@sha256:4ae89311e5031155a139a5c6d31a80f6deb135443711e61ff7df41cb549fdcaa"

// upgradeFixture is KyIdentity deployed by an earlier release on a Cluster of three 17
// instances, database-1 the primary, on nodes node-1 to node-3, and an
// operator that upgrades the data in place once the Cluster wakes with an image of a higher
// major, unless fail is set. The operator writes the Cluster's status after every read, so a
// write that carries the resourceVersion it read conflicts, as in CI run 45719982937.
type upgradeFixture struct {
	a      App // this release's, on the catalog's 18 image
	cs     *fake.Clientset
	dyn    *dynamicfake.FakeDynamicClient
	data   string // the image of the Cluster's data, as the operator records it
	fail   bool
	events []string // hibernation, image changes and copies, in order
	execs  [][]string
	copyFn func(args []string) error
	podFn  func(*corev1.Pod) error
	rv     int // the Cluster's resourceVersion, which the operator bumps after every read
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
	stamp, stamps := 0, copyStamp
	copyStamp = func() string { stamp++; return fmt.Sprintf("2026112%d-000000", stamp) }
	t.Cleanup(func() { copyStamp = stamps })
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
		f.rv++
		u.SetResourceVersion(strconv.Itoa(f.rv))
		f.rv++ // the operator's status write, after this read
		return true, u, nil
	})
	record := func(action k8stesting.Action) (bool, runtime.Object, error) {
		var o map[string]any
		switch a := action.(type) {
		case k8stesting.PatchAction:
			_ = json.Unmarshal(a.GetPatch(), &o)
			// The API server refuses a patch naming another uid: uid is immutable.
			if uid, _, _ := unstructured.NestedString(o, "metadata", "uid"); uid != "" && uid != string(clusterOf(t, dyn).GetUID()) {
				return true, nil, apierrors.NewInvalid(schema.GroupKind{Group: clusterGroup, Kind: "Cluster"}, databaseName, nil)
			}
			if rv, _, _ := unstructured.NestedString(o, "metadata", "resourceVersion"); rv != "" && rv != strconv.Itoa(f.rv) {
				return true, nil, apierrors.NewConflict(clusterResource.GroupResource(), databaseName, errors.New("the object has been modified; please apply your changes to the latest version and try again"))
			}
		case k8stesting.UpdateAction:
			o = a.GetObject().(*unstructured.Unstructured).Object
		}
		if v, found, _ := unstructured.NestedString(o, "metadata", "annotations", hibernation); found {
			f.events = append(f.events, "hibernation "+v)
			f.instances(v != "on") // the operator stops or starts the instances

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
	claim("database-2", "database-2", "replica", "PG_DATA")
	claim("database-3", "database-3", "replica", "PG_DATA")
	f.instances(true)
	// The installer's pods: each copy pod's claims, node and when it started.
	cs.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		p := action.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		if !strings.HasPrefix(p.GenerateName, "kyq-pre-upgrade-") {
			return false, nil, nil
		}
		var claims []string
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && strings.HasPrefix(v.Name, "src-") {
				claims = append(claims, v.PersistentVolumeClaim.ClaimName)
			}
		}
		node := "any node"
		if aff := p.Spec.Affinity; aff != nil {
			node = aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields[0].Values[0]
		}
		o, _ := dyn.Tracker().Get(clusterResource, "kyq-kyidentity", databaseName)
		f.events = append(f.events, fmt.Sprintf("pod %v on %s while hibernation %q", claims, node, o.(*unstructured.Unstructured).GetAnnotations()[hibernation]))
		if f.podFn != nil {
			if err := f.podFn(p); err != nil {
				return true, nil, err
			}
		}
		return false, nil, nil
	})
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

// instances starts, or stops, the Cluster's instance pods database-N on node-N.
func (f *upgradeFixture) instances(run bool) {
	pods := corev1.SchemeGroupVersion.WithResource("pods")
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("database-%d", i)
		if !run {
			_ = f.cs.Tracker().Delete(pods, "kyq-kyidentity", name)
			continue
		}
		_ = f.cs.Tracker().Create(pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kyq-kyidentity", Labels: map[string]string{clusterLabel: databaseName}},
			Spec: corev1.PodSpec{NodeName: fmt.Sprintf("node-%d", i)}}, "kyq-kyidentity")
	}
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

// copiesOf are the copy names one attempt makes, at stamp.
func copiesOf(stamp string) []string {
	var out []string
	for _, c := range []string{"database-1", "database-2", "database-3"} {
		out = append(out, ClusterCopyClaim(17, c, stamp))
	}
	return out
}

// awakeOn17 fails the test unless the Cluster runs, unhibernated, on its 17 image.
func (f *upgradeFixture) awakeOn17(t *testing.T) {
	t.Helper()
	if f.image(t) != cluster17 || clusterOf(t, f.dyn).GetAnnotations()[hibernation] != "off" || imageMajor(f.data) != 17 {
		t.Fatalf("not awake on 17: image %s, hibernation %q, data %s, events %v", f.image(t), clusterOf(t, f.dyn).GetAnnotations()[hibernation], f.data, f.events)
	}
}

const stamp1, stamp2 = "20261121-000000", "20261122-000000"

func TestClusterUpgrade(t *testing.T) {
	ctx := context.Background()
	f := newUpgradeFixture(t)
	step := f.step()
	if done, err := step.Inspect(ctx); err != nil || done {
		t.Fatalf("a 17 Cluster needs no upgrade: %v", err)
	}
	// This release's deploy alone never moves the data.
	deploy := Steps(f.a)[3]
	if err := deploy.Apply(ctx); err == nil || !strings.Contains(err.Error(), "database-upgrade moves it first") || imageMajor(f.image(t)) != 17 {
		t.Fatalf("deploy over a 17 Cluster: %v, image %s", err, f.image(t))
	}
	f.events = nil
	// The operator writes the Cluster between every read and write: no write may carry the
	// resourceVersion it read (CI run 45719982937).
	if err := step.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	c := copiesOf(stamp1)
	// A storage check per instance on its node while it runs; then hibernated, every
	// instance copied while it is, then the image and the wake together.
	want := []string{
		`pod [database-1] on node-1 while hibernation ""`,
		`pod [database-2] on node-2 while hibernation ""`,
		`pod [database-3] on node-3 while hibernation ""`,
		"hibernation on",
		`pod [database-1] on any node while hibernation "on"`,
		"copy [database-1:pgdata] while hibernation on",
		`pod [database-2] on any node while hibernation "on"`,
		"copy [database-2:pgdata] while hibernation on",
		`pod [database-3] on any node while hibernation "on"`,
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
	if got := slices.Sorted(maps.Keys(copies)); !slices.Equal(got, c) {
		t.Fatalf("copies = %v", got)
	}
	for name, p := range copies {
		src := strings.TrimSuffix(strings.TrimPrefix(name, "pre-upgrade-pg17-"), "-"+stamp1)
		role := map[bool]string{true: "primary", false: "replica"}[strings.HasPrefix(src, "database-1")]
		if p.Annotations[copiedAnnotation] != "verified" || p.Annotations[copiedAt] == "" || p.Annotations[copyClusterUID] != "cluster-uid" || !owned(&p) ||
			p.Labels[copyOfLabel] != src || p.Labels[copyMajorLabel] != "17" || p.Labels[copyRole] != role || p.Labels[clusterLabel] != "" ||
			ptr.Deref(p.Spec.StorageClassName, "") != "longhorn" || p.Spec.Resources.Requests.Storage().String() != "2Gi" {
			t.Errorf("%s: %+v %+v", name, p.ObjectMeta, p.Spec)
		}
	}
	// Copy pods run as the instances' postgres, reading the originals only.
	for _, a := range f.cs.Actions() {
		p, ok := a.(k8stesting.CreateAction)
		if !ok || a.GetResource().Resource != "pods" || !strings.HasPrefix(p.GetObject().(*corev1.Pod).GenerateName, "kyq-pre-upgrade-") {
			continue
		}
		pod := p.GetObject().(*corev1.Pod)
		ct := pod.Spec.Containers[0]
		if *ct.SecurityContext.RunAsUser != cnpgUID || *pod.Spec.SecurityContext.FSGroup != cnpgUID {
			t.Errorf("copy pod runs as %d", *ct.SecurityContext.RunAsUser)
		}
		for _, m := range ct.VolumeMounts {
			if strings.HasPrefix(m.Name, "src-") && (!m.ReadOnly || m.SubPath != "pgdata") {
				t.Errorf("source mount %+v", m)
			}
		}
	}
	// ANALYZE of what pg_upgrade carried no statistics for, on the primary.
	if len(f.execs) != 1 || f.execs[0][0] != "database-1" || !slices.Contains(f.execs[0], "--missing-stats-only") {
		t.Errorf("execs = %v", f.execs)
	}
	note := step.Note()
	for _, s := range []string{"kubectl -n kyq-kyidentity delete pvc " + strings.Join(c, " "), "PostgreSQL 17 to 18", "ANALYZE ran on database-1", "of database-2, verified"} {
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
	if len(f.copies(t)) != 3 {
		t.Fatal("copies went")
	}
	f.noClaimDeleted(t)
}

// A copy that fails wakes the Cluster on 17, unchanged: no image change, nothing deleted. The
// next attempt makes new copies beside the first one's, which stay.
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
	if err == nil || !strings.Contains(err.Error(), "started again on PostgreSQL 17, unchanged") || !strings.Contains(err.Error(), "copied contents differ") ||
		!strings.Contains(err.Error(), "kubectl -n kyq-kyidentity delete pvc "+strings.Join(copiesOf(stamp1), " ")) {
		t.Fatalf("apply = %v", err)
	}
	if slices.Contains(f.events, "image 18") {
		t.Fatalf("image changed: %v", f.events)
	}
	f.awakeOn17(t)
	if c := f.copies(t)[ClusterCopyClaim(17, "database-2", stamp1)]; c.Annotations[copiedAnnotation] == "verified" {
		t.Error("a failed copy is marked verified")
	}
	if err := step.Verify(ctx); err == nil {
		t.Fatal("verify passes on 17 data")
	}
	if err := Steps(f.a)[3].Apply(ctx); err == nil || imageMajor(f.image(t)) != 17 {
		t.Fatalf("deploy after a failed copy: %v", err)
	}
	f.noClaimDeleted(t)

	// The next run copies every claim again, into new claims, from the 17 data that took
	// writes since. The first attempt's copies are kept untouched and listed.
	first := f.copies(t)
	f.copyFn, f.events = nil, nil
	if err := step.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(slices.DeleteFunc(slices.Clone(f.events), func(e string) bool { return !strings.HasPrefix(e, "copy ") })); n != 3 || f.image(t) != f.want() {
		t.Fatalf("rerun: %v", f.events)
	}
	all := f.copies(t)
	both := slices.Sorted(slices.Values(append(copiesOf(stamp1), copiesOf(stamp2)...)))
	if got := slices.Sorted(maps.Keys(all)); !slices.Equal(got, both) {
		t.Fatalf("copies %v, want %v", got, both)
	}
	for name, p := range first {
		if all[name].ResourceVersion != p.ResourceVersion {
			t.Errorf("the first attempt's copy %s was changed", name)
		}
	}
	if !strings.Contains(step.Note(), "delete pvc "+strings.Join(both, " ")) {
		t.Errorf("note: %s", step.Note())
	}
	f.noClaimDeleted(t)
}

// Whatever fails once the Cluster is hibernated wakes it on its 17 image, also when the
// failing write did land, and reports the error.
func TestClusterUpgradeFailsAfterHibernation(t *testing.T) {
	lost := errors.New("connection reset")
	for name, fail := range map[string]func(f *upgradeFixture) k8stesting.ReactionFunc{
		// The hibernation lands, its answer is lost.
		"hibernation answer lost": func(f *upgradeFixture) k8stesting.ReactionFunc {
			return func(action k8stesting.Action) (bool, runtime.Object, error) {
				p := action.(k8stesting.PatchAction)
				if p.GetResource().Resource == "clusters" && strings.Contains(string(p.GetPatch()), `"on"`) {
					return f.applied(t, p, lost)
				}
				return false, nil, nil
			}
		},
		"copy not marked verified": func(f *upgradeFixture) k8stesting.ReactionFunc {
			return func(action k8stesting.Action) (bool, runtime.Object, error) {
				p := action.(k8stesting.PatchAction)
				if p.GetResource().Resource == "persistentvolumeclaims" && strings.Contains(string(p.GetPatch()), "verified") {
					return true, nil, lost
				}
				return false, nil, nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newUpgradeFixture(t)
			if strings.HasPrefix(name, "copy") {
				f.cs.PrependReactor("patch", "persistentvolumeclaims", fail(f))
			} else {
				f.dyn.PrependReactor("patch", "clusters", fail(f))
			}
			err := f.step().Apply(context.Background())
			if err == nil || !errors.Is(err, lost) || !strings.Contains(err.Error(), "started again on PostgreSQL 17, unchanged") {
				t.Fatalf("apply = %v", err)
			}
			f.awakeOn17(t)
			if len(f.copies(t)) != 3 {
				t.Fatalf("%d copies", len(f.copies(t)))
			}
			f.noClaimDeleted(t)
		})
	}
}

// A Cluster deleted and recreated, or changed, while it was copied is never switched to 18;
// a recreated one is not touched at all.
func TestClusterUpgradeClusterChangedDuringCopy(t *testing.T) {
	for name, change := range map[string]func(o *unstructured.Unstructured){
		"recreated": func(o *unstructured.Unstructured) { o.SetUID("new-uid") },
		"image": func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "pg:17.13", "spec", "imageName")
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newUpgradeFixture(t)
			f.copyFn = func([]string) error {
				o := clusterOf(t, f.dyn)
				change(o)
				return f.dyn.Tracker().Update(clusterResource, o, o.GetNamespace())
			}
			err := f.step().Apply(context.Background())
			if err == nil || slices.Contains(f.events, "image 18") || imageMajor(f.image(t)) != 17 {
				t.Fatalf("apply = %v, events %v", err, f.events)
			}
			if name == "recreated" {
				if !strings.Contains(err.Error(), "deleted or recreated") || !strings.Contains(err.Error(), "failed") || clusterOf(t, f.dyn).GetAnnotations()[hibernation] != "on" {
					t.Fatalf("apply = %v", err)
				}
			} else {
				f.awakeOn17(t)
			}
			if len(f.copies(t)) != 3 {
				t.Fatalf("%d copies", len(f.copies(t)))
			}
			f.noClaimDeleted(t)
		})
	}
}

// A switch that landed with its answer lost is an upgrade under way: never set back to 17
// then. One whose state cannot be read is reported, and nothing more is written.
func TestClusterUpgradeSwitchAnswerLost(t *testing.T) {
	lost := errors.New("connection reset")
	timeout := clusterTimeout
	clusterTimeout = time.Second
	t.Cleanup(func() { clusterTimeout = timeout })
	t.Run("landed", func(t *testing.T) {
		f := newUpgradeFixture(t)
		f.dyn.PrependReactor("patch", "clusters", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if p := action.(k8stesting.PatchAction); strings.Contains(string(p.GetPatch()), f.want()) {
				return f.applied(t, p, lost)
			}
			return false, nil, nil
		})
		if err := f.step().Apply(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.image(t) != f.want() || imageMajor(f.data) != 18 || slices.Contains(f.events, "image 17") {
			t.Fatalf("image %s, data %s, events %v", f.image(t), f.data, f.events)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		f := newUpgradeFixture(t)
		switched := false
		f.dyn.PrependReactor("patch", "clusters", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if p := action.(k8stesting.PatchAction); strings.Contains(string(p.GetPatch()), f.want()) {
				switched = true
				return f.applied(t, p, lost)
			}
			return false, nil, nil
		})
		f.dyn.PrependReactor("get", "clusters", func(k8stesting.Action) (bool, runtime.Object, error) {
			if switched {
				return true, nil, lost
			}
			return false, nil, nil
		})
		f.events = nil
		err := f.step().Apply(context.Background())
		if err == nil || !strings.Contains(err.Error(), "cannot be read") || strings.Contains(err.Error(), "started again") {
			t.Fatalf("apply = %v", err)
		}
		// applied bypasses the recorder: any event about the Cluster is a later write.
		if f.image(t) != f.want() || slices.ContainsFunc(f.events, func(e string) bool { return strings.HasPrefix(e, "image") || e == "hibernation off" }) {
			t.Fatalf("written after the lost switch: image %s, events %v", f.image(t), f.events)
		}
		f.noClaimDeleted(t)
	})
}

// A failover while the copies are provisioned moves the primary: the copies carry the roles
// the instances had when the Cluster stopped.
func TestClusterUpgradeFailoverBeforeStop(t *testing.T) {
	ctx := context.Background()
	f := newUpgradeFixture(t)
	failed := false
	f.podFn = func(*corev1.Pod) error {
		if failed {
			return nil
		}
		failed = true
		// Through the tracker: this runs inside the clientset's reactor.
		gvr := corev1.SchemeGroupVersion.WithResource("persistentvolumeclaims")
		for name, role := range map[string]string{"database-1": "replica", "database-2": "primary"} {
			o, err := f.cs.Tracker().Get(gvr, "kyq-kyidentity", name)
			if err != nil {
				return err
			}
			p := o.(*corev1.PersistentVolumeClaim).DeepCopy()
			p.Labels[cnpgRole] = role
			if err := f.cs.Tracker().Update(gvr, p, "kyq-kyidentity"); err != nil {
				return err
			}
		}
		return nil
	}
	if err := f.step().Apply(ctx); err != nil {
		t.Fatal(err)
	}
	copies := f.copies(t)
	for claim, role := range map[string]string{"database-1": "replica", "database-2": "primary", "database-3": "replica"} {
		if got := copies[ClusterCopyClaim(17, claim, stamp1)].Labels[copyRole]; got != role {
			t.Errorf("copy of %s labelled %s, want %s", claim, got, role)
		}
	}
}

// applied applies patch p, of the hibernation and the image, to the Cluster as the API server
// would, then answers with err.
func (f *upgradeFixture) applied(t *testing.T, p k8stesting.PatchAction, err error) (bool, runtime.Object, error) {
	t.Helper()
	var patch map[string]any
	if jerr := json.Unmarshal(p.GetPatch(), &patch); jerr != nil {
		t.Fatal(jerr)
	}
	o := clusterOf(t, f.dyn)
	if v, found, _ := unstructured.NestedString(patch, "metadata", "annotations", hibernation); found {
		o.SetAnnotations(map[string]string{hibernation: v})
		f.instances(v != "on")
	}
	if v, found, _ := unstructured.NestedString(patch, "spec", "imageName"); found {
		_ = unstructured.SetNestedField(o.Object, v, "spec", "imageName")
	}
	if uerr := f.dyn.Tracker().Update(clusterResource, o, o.GetNamespace()); uerr != nil {
		t.Fatal(uerr)
	}
	return true, nil, err
}

// A separate WAL claim, which the installer never makes, refuses before anything stops: its
// pgdata/pg_wal links to a path the copy pod does not mount.
func TestClusterUpgradeWALClaimRefused(t *testing.T) {
	ctx := context.Background()
	f := newUpgradeFixture(t)
	p := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "database-1-wal", Namespace: "kyq-kyidentity",
		Labels:          map[string]string{clusterLabel: databaseName, cnpgInstance: "database-1", cnpgRole: "primary", cnpgPVCRole: "PG_WAL"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: clusterGroup + "/v1", Kind: "Cluster", Name: databaseName, UID: "cluster-uid"}}}}
	if _, err := f.cs.CoreV1().PersistentVolumeClaims("kyq-kyidentity").Create(ctx, p, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	err := f.step().Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), `"PG_WAL"`) || !strings.Contains(err.Error(), "nothing was stopped") {
		t.Fatalf("apply = %v", err)
	}
	if len(f.events) != 0 || len(f.copies(t)) != 0 || f.image(t) != cluster17 {
		t.Fatalf("events %v, %d copies", f.events, len(f.copies(t)))
	}
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

// Storage that cannot provide a copy refuses before the Cluster is stopped, and names the
// earlier copies taking space, which it never deletes.
func TestClusterUpgradeStorageCheckRefused(t *testing.T) {
	ctx := context.Background()
	f := newUpgradeFixture(t)
	// An earlier failed attempt's copies, and another Cluster's.
	f.copyFn = func([]string) error { return errors.New("no space left on device") }
	if err := f.step().Apply(ctx); err == nil {
		t.Fatal("copy failure accepted")
	}
	f.copyFn = nil
	labels := workloadLabels("rs")
	labels[copyOfLabel] = "database-1"
	other := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "pre-upgrade-pg17-database-1-20250101-000000", Namespace: "kyq-kyidentity",
		Labels: labels, Annotations: map[string]string{copyClusterUID: "earlier-uid", copiedAnnotation: "verified"}},
		Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")}}}}
	if _, err := f.cs.CoreV1().PersistentVolumeClaims("kyq-kyidentity").Create(ctx, other, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	f.events = nil
	f.podFn = func(p *corev1.Pod) error {
		if p.Spec.Affinity != nil { // the storage check, while the Cluster runs
			return errors.New("0/3 nodes are available: insufficient storage")
		}
		return nil
	}
	err := f.step().Apply(ctx)
	if err == nil || !strings.Contains(err.Error(), "was not stopped") || !strings.Contains(err.Error(), "earlier copies take space") ||
		!strings.Contains(err.Error(), other.Name+" (2Gi, verified)") || !strings.Contains(err.Error(), ClusterCopyClaim(17, "database-2", stamp1)+" (2Gi, unverified)") {
		t.Fatalf("apply = %v", err)
	}
	if slices.ContainsFunc(f.events, func(e string) bool { return strings.HasPrefix(e, "hibernation") || strings.HasPrefix(e, "image") }) {
		t.Fatalf("the Cluster was touched: %v", f.events)
	}
	if f.image(t) != cluster17 || len(f.copies(t)) != 7 {
		t.Fatalf("image %s, %d copies", f.image(t), len(f.copies(t)))
	}
	f.noClaimDeleted(t)
}

// An operator older than 1.26 refuses the image: woken on 17, the copies kept.
func TestClusterUpgradeOperatorRefuses(t *testing.T) {
	f := newUpgradeFixture(t)
	f.dyn.PrependReactor("patch", "clusters", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if strings.Contains(string(action.(k8stesting.PatchAction).GetPatch()), f.want()) {
			return true, nil, apierrors.NewBadRequest("can't upgrade between majors")
		}
		return false, nil, nil
	})
	err := f.step().Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "1.26") || !strings.Contains(err.Error(), "started again on PostgreSQL 17") {
		t.Fatalf("apply = %v", err)
	}
	f.awakeOn17(t)
	if len(f.copies(t)) != 3 {
		t.Fatalf("after the refusal: %d copies", len(f.copies(t)))
	}
	f.noClaimDeleted(t)
}

// A pg_upgrade that does not finish is rolled back as CloudNativePG documents: its image again.
func TestClusterUpgradeRollback(t *testing.T) {
	f := newUpgradeFixture(t)
	f.fail = true
	timeout := clusterTimeout
	clusterTimeout = 30 * time.Millisecond
	t.Cleanup(func() { clusterTimeout = timeout })
	err := f.step().Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "set back to "+cluster17) || !strings.Contains(err.Error(), "They stay until you delete them") {
		t.Fatalf("apply = %v", err)
	}
	if f.image(t) != cluster17 || imageMajor(f.data) != 17 || !slices.Equal(f.events[len(f.events)-1:], []string{"image 17"}) {
		t.Fatalf("after the rollback: image %s, events %v", f.image(t), f.events)
	}
	if len(f.copies(t)) != 3 {
		t.Fatal("copies went")
	}
	f.noClaimDeleted(t)
}

// A copy of another Cluster (one recreated from the copies) is never touched nor offered.
func TestClusterUpgradeForeignCopy(t *testing.T) {
	ctx := context.Background()
	f := newUpgradeFixture(t)
	labels := workloadLabels("rs")
	labels[copyOfLabel] = "database-2"
	p := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "pre-upgrade-pg17-database-2-20250101-000000", Namespace: "kyq-kyidentity",
		Labels: labels, Annotations: map[string]string{copyClusterUID: "earlier-uid", copiedAnnotation: "verified"}}}
	p, err := f.cs.CoreV1().PersistentVolumeClaims("kyq-kyidentity").Create(ctx, p, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	step := f.step()
	if err := step.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.copies(t)[p.Name]; got.ResourceVersion != p.ResourceVersion {
		t.Fatalf("the other Cluster's copy changed: %+v", got)
	}
	if strings.Contains(step.Note(), p.Name) {
		t.Fatalf("note offers the other Cluster's copy: %s", step.Note())
	}
	f.noClaimDeleted(t)
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
