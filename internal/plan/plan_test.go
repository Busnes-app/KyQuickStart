package plan

import (
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
)

func cat(deps map[string][]string) catalog.Catalog {
	c := catalog.Catalog{}
	for name, d := range deps {
		c[name] = catalog.App{Manifest: catalog.Manifest{Name: name, DependsOn: d}}
	}
	return c
}

func TestOrder(t *testing.T) {
	c := cat(map[string][]string{"web": {"db"}, "db": nil, "cache": nil, "worker": {"db", "cache"}})
	got, err := Order([]string{"worker", "web", "db", "cache"}, c)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cache", "db", "web", "worker"}
	if !slices.Equal(got, want) {
		t.Errorf("Order = %v, want %v", got, want)
	}
}

func TestOrderErrors(t *testing.T) {
	c := cat(map[string][]string{"a": {"b"}, "b": {"a"}, "c": {"missing"}, "missing": nil, "d": nil})
	cases := map[string]struct {
		apps []string
		want string
	}{
		"unknown app":        {[]string{"zzz"}, `"zzz" is not in the catalog`},
		"missing dependency": {[]string{"c"}, `"c" needs "missing", which is not selected`},
		"cycle":              {[]string{"a", "b", "d"}, "dependency cycle among a, b"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Order(tc.apps, c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
