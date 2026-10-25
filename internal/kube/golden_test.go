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
	"kycalendar":  "96bb33a50a77d2b5a9f43a5aa77537ecff07d5054f55073ab1f6a34668bbe62f",
	"kyidentity":  "84e774aa56fc0e360fcb8660367c144276224dfa878db5c130ecf4337b299f1f",
	"kynotes":     "f73a4ae2b18c4402a84c1e3a8912568dece4b9db6f7b0b6c5cae1aa54414f39c",
	"kypulse":     "042b82fb21ae7470f8958bc785efd69ccd861e436dfcbba50ffc0ff35894996b",
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
		c = c.WithPrivateRecovery(false)
		c.Kubernetes = c.Kubernetes.WithHost(name + ".example.test")
		a := App{Name: name, Catalog: c, ReleaseSet: "rs-1", Edge: "npm", StorageClass: "standard", RunnerImage: testRunnerImage}
		if got := render(a).hash(); got != want {
			t.Errorf("%s renders %s, want %s", name, got, want)
		}
	}
}
