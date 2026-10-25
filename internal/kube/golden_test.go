package kube

import (
	"maps"
	"slices"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/catalog/catalogtest"
)

// renderedHashes are the hashes of every Kubernetes catalog app's objects as origin/master
// before KyYard rendered them: adding a field for one app must not redeploy the others.
// Ky release pins are replaced (catalogtest.Unpinned), so following a release changes none.
// The non-root apps' hashes moved once, on purpose, with #105's fsGroup fix: apply redeploys
// them, and their init container repairs the modes.
var renderedHashes = map[string]string{
	"euro-office": "a65a4a42bbe42da10dc46343eac3260d5dcf030b79e42f80cf270256f355e3e0",
	"kycalendar":  "f7d7c5f8b06cc519da9816cc9ef994417b96f270fbf833ce735fc35a78972255",
	"kyidentity":  "b7d69ba3eec7187b26382dc56d35ea6f065ee9ecf0a273df125729b79d16a214",
	"kynotes":     "39571af36235862a812d81c61d2cc38731d09269de36e8910c81241a9bf26611",
	"kypulse":     "804b6d0d7c8754ed8aeb6e57399aac76aac3130c72a42061166cf9f07024814d",
	"kyrecovery":  "2195bf34bc103d1d443dc041c9b21371b3bc4261b56b8de35b14cd158154140f",
	"npm":         "70adde3f158ad6e8fd04a589d963bd1cf5c16dfb0019c8545cb6e34797c999f1",
}

func TestRenderGolden(t *testing.T) {
	cat, err := catalog.Load(catalogtest.Unpinned(catalog.Embedded()))
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
