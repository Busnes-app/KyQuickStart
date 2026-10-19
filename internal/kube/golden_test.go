package kube

import (
	"maps"
	"slices"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
)

// renderedHashes are the hashes of every Kubernetes catalog app's objects as origin/master
// (eca04d6) rendered them: adding a field for one app must not redeploy the others.
var renderedHashes = map[string]string{
	"kycalendar": "6afd512799d26b78d2431b482f8e850fa8394d875b14ab77529e32b47b460627",
	"kyidentity": "c8fa44227b822b930df575ea177388ba681c8b04da2005717807cabcd559260f",
	"kynotes":    "048c95d97187c8258ecc7c6bcca37e4ce7ba44ddec5c52066d6726a6f0a8873f",
	"kypulse":    "c48e95ecc6b3a18057a4ca2f1f2d4bde070cdddfcccaeb7e171d68cca4e24bd1",
	"kyrecovery": "6a7567dde69a4929683285afe0a577cb6482ee206f13f651111c9ca4199cb650",
	"npm":        "70adde3f158ad6e8fd04a589d963bd1cf5c16dfb0019c8545cb6e34797c999f1",
}

func TestRenderGolden(t *testing.T) {
	cat, err := catalog.Load(catalog.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range slices.Sorted(maps.Keys(cat)) {
		c := cat[name]
		if c.Kubernetes == nil || c.Kubernetes.SuiteRunner {
			continue
		}
		want, ok := renderedHashes[name]
		if !ok {
			t.Errorf("%s has no recorded hash", name)
			continue
		}
		c.Kubernetes = c.Kubernetes.WithHost(name + ".example.test")
		a := App{Name: name, Catalog: c, ReleaseSet: "rs-1", Edge: "npm", StorageClass: "standard", RunnerImage: testRunnerImage}
		if got := render(a).hash(); got != want {
			t.Errorf("%s renders %s, want %s", name, got, want)
		}
	}
}
