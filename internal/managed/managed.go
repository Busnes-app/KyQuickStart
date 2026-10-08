// Package managed names the labels that mark installer-managed workloads. KyYard refuses
// generic edits to anything labelled Label=Value.
package managed

const (
	Label           = "ky.managed-by"
	Value           = "kyquickstart"
	ReleaseSetLabel = "ky.release-set"
)
