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
var renderedHashes = map[string]string{
	"euro-office": "a65a4a42bbe42da10dc46343eac3260d5dcf030b79e42f80cf270256f355e3e0",
	"kycalendar":  "c5e3721b8abdba2f1b54fcdf4b84c88a46a725a7b7ec462bd0ec03d1ba56c11a",
	"kyidentity":  "b755041ee024e1107ef2c1a5c8a93d2e9ad26c52a70eb4af891130ed0a3a5581",
	"kynotes":     "a7ecd435bce5bc8723663d3ac5ef3e49bdcb14dbd8dac2d77d62615db93755e7",
	"kypulse":     "fab5ed6131eaae41282490f5ae10c1a10e50226da589d9268c26641fc4301d0a",
	"kyrecovery":  "09835a4368382e048c9fe8333de5eda876dc01e4bd76479b855f7823fff5baec",
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
