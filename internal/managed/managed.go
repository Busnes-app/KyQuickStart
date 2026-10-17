// Package managed names the labels that mark installer-managed workloads. KyYard refuses
// generic edits to anything labelled Label=Value.
package managed

const (
	Label           = "ky.managed-by"
	Value           = "kyquickstart"
	ReleaseSetLabel = "ky.release-set"
	VersionLabel    = "kyquickstart/version" // catalog version deployed; outlives uninstall and unmanage
)

// Deployed is what a target records about an app's last deployment. Found is false when
// nothing was deployed; Managed is false once the app was unmanaged or was never ours.
type Deployed struct {
	Found, Managed      bool
	ReleaseSet, Version string
}
