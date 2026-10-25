//go:build e2e

package e2e

import "strings"

// recoveryLine is the pairing sentence a product's handover carries: with an internal
// KyRecovery in the plan its products may pair by hand (#93); without one they cannot.
func recoveryLine(out string) string {
	if byHand := "Pair it with KyRecovery on its backup page"; strings.Contains(out, byHand) {
		return byHand
	}
	return "No catalog product can pair with KyRecovery in this release"
}
