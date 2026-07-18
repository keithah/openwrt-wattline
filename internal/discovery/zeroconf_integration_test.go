package discovery

import (
	"fmt"
	"net"
	"testing"

	"github.com/grandcat/zeroconf"
)

func multicastTestInterface(t *testing.T) net.Interface {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		if !lanEligible(iface) {
			continue
		}
		addresses, err := iface.Addrs()
		if err == nil && len(addresses) != 0 {
			return iface
		}
	}
	t.Skip("no eligible multicast interface")
	return net.Interface{}
}

func TestPatchedZeroconfRapidRegisterShutdown(t *testing.T) {
	iface := multicastTestInterface(t)
	for iteration := 0; iteration < 50; iteration++ {
		server, err := zeroconf.Register(fmt.Sprintf("wattline-lifecycle-%d", iteration), "_wattline._tcp", "local.", 8377, []string{"ver=test"}, []net.Interface{iface})
		if err != nil {
			if iteration == 0 {
				t.Skipf("multicast unavailable: %v", err)
			}
			t.Fatalf("register %d: %v", iteration, err)
		}
		server.Shutdown()
	}
}
