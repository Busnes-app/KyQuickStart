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
// them, and their init container repairs the modes. KyCalendar's moved with #100's
// KYCALENDAR_KYPOST_ALLOW_PRIVATE. Every hash moved, on purpose, with node-failure recovery
// (owner, 2026-11-11): apply restarts each app once to add its probes, spread and tolerations.
var renderedHashes = map[string]string{
	"euro-office": "36dc523ce8e672d14fadeda593b8ed44918ddebbd1f7939f2a7ce28efdd0375a",
	"kycalendar":  "e0cfad44d48b4ba260a124d04d3a070cf0fbc7611003f4492cc922038744e5a0",
	"kyidentity":  "e251f2b34c8e882f8b72e14ffa0bce26844042b0e00867252ad10b4b8fb31824",
	"kynotes":     "bc95618c57aff9b1b45f70954c25d0a686ab542f7b219430cc27f0f5d0cb8448",
	"kypulse":     "7790db6ec9b21a012ac7525f9445418f496b167d597ac21f0241e28d9606ec97",
	"kyrecovery":  "cf5c4c3e7ef5ead88e94ff9ac10784d4e10305fe870e76b1a460a7873a942576",
	"nginx":       "5b9d747cb46f004c7d65f4861bc313d991ee5d54c855e85dc8ceefbdc6a3a040",
	"npm":         "c44905a672dc46b3f2c30d302290eb2272a40d19f54da634d5141d179e43c723",
}

func TestRenderGolden(t *testing.T) {
	cat, err := catalog.Load(catalogtest.Unpinned(catalog.Embedded()))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range slices.Sorted(maps.Keys(cat)) {
		c := cat[name]
		if c.Kubernetes == nil || c.Kubernetes.SuiteRunner || c.Category == "operator" { // its objects are its manifest's
			continue
		}
		want, ok := renderedHashes[name]
		if !ok {
			t.Errorf("%s has no recorded hash", name)
			continue
		}
		c = c.WithPrivateRecovery(false).WithPrivatePeer(false)
		c.Kubernetes = c.Kubernetes.WithHost(name + ".example.test")
		a := App{Name: name, Catalog: c, ReleaseSet: "rs-1", Edges: []string{"npm"}, StorageClass: "standard", RunnerImage: testRunnerImage}
		if got := render(a).hash(); got != want {
			t.Errorf("%s renders %s, want %s", name, got, want)
		}
	}
}
