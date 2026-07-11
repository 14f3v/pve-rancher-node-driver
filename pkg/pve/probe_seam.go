package pve

import (
	"net"
	"time"
)

// SetProbeDialer replaces the SSH-probe dialer and returns a restore
// function. ONLY for tests of dependent packages (pkg/driver); production
// code must never call it.
func SetProbeDialer(d func(network, address string, timeout time.Duration) (net.Conn, error)) (restore func()) {
	return setProbeDialerForTest(d)
}
