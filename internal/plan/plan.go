// Package plan orders the selected apps.
package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
)

// Order returns apps with every app after its dependencies; ties sort by name.
func Order(apps []string, cat catalog.Catalog) ([]string, error) {
	names := append([]string(nil), apps...)
	sort.Strings(names)
	selected := map[string]bool{}
	for _, a := range names {
		if _, ok := cat[a]; !ok {
			return nil, fmt.Errorf("app %q is not in the catalog", a)
		}
		selected[a] = true
	}
	waiting := map[string]int{}
	dependents := map[string][]string{}
	for _, a := range names {
		for _, d := range cat[a].DependsOn {
			if !selected[d] {
				return nil, fmt.Errorf("app %q needs %q, which is not selected", a, d)
			}
			waiting[a]++
			dependents[d] = append(dependents[d], a)
		}
	}
	var ready, out []string
	for _, a := range names {
		if waiting[a] == 0 {
			ready = append(ready, a)
		}
	}
	for len(ready) > 0 {
		sort.Strings(ready)
		a := ready[0]
		ready = ready[1:]
		out = append(out, a)
		for _, b := range dependents[a] {
			if waiting[b]--; waiting[b] == 0 {
				ready = append(ready, b)
			}
		}
	}
	if len(out) != len(names) {
		var stuck []string
		for _, a := range names {
			if waiting[a] > 0 {
				stuck = append(stuck, a)
			}
		}
		return nil, fmt.Errorf("dependency cycle among %s", strings.Join(stuck, ", "))
	}
	return out, nil
}
