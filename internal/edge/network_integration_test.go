//go:build e2e

package edge

import (
	"encoding/json"
	"net/netip"
	"os/exec"
	"testing"
)

// createReservedNetwork mirrors the required operator setup: an explicit subnet whose
// dynamic --ip-range is the upper half, leaving low addresses for connector_cidr.
func createReservedNetwork(t *testing.T, name string) {
	t.Helper()
	run := func(args ...string) []byte {
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v %s", args, err, out)
		}
		return out
	}
	// Let Docker pick a free subnet, then recreate it as user-configured.
	run("network", "create", name)
	var networks []struct {
		IPAM struct{ Config []struct{ Subnet string } }
	}
	if err := json.Unmarshal(run("network", "inspect", name), &networks); err != nil || len(networks) != 1 || len(networks[0].IPAM.Config) == 0 {
		t.Fatal("invalid network response")
	}
	run("network", "rm", name)
	subnet, err := netip.ParsePrefix(networks[0].IPAM.Config[0].Subnet)
	if err != nil || !subnet.Addr().Is4() || subnet.Bits() > 28 {
		t.Fatal("unusable Docker subnet", subnet)
	}
	base := subnet.Masked().Addr().As4()
	half := uint32(1) << (31 - subnet.Bits())
	n := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3]) + half
	upper := netip.PrefixFrom(netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}), subnet.Bits()+1)
	// Docker 28 omits an unconfigured gateway from inspect for a user subnet; pin it.
	gateway := subnet.Masked().Addr().Next()
	run("network", "create", "--subnet", subnet.String(), "--ip-range", upper.String(), "--gateway", gateway.String(), name)
}
