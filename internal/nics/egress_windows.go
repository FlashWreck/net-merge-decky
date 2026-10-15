package nics

import (
	"math/bits"
	"syscall"
)

const ipUnicastIf = 31 // IP_UNICAST_IF and IPV6_UNICAST_IF share the value

// ForceEgress forces a socket's egress interface regardless of the routing table, so our own
// outbound flows can never be captured by the TUN's routes (no routing loop). IPv4 takes the
// interface index in network byte order, IPv6 in host byte order.
func ForceEgress(idx int, v6 bool) func(network, address string, c syscall.RawConn) error {
	if idx <= 0 {
		return nil
	}
	level, val := syscall.IPPROTO_IP, int(int32(bits.ReverseBytes32(uint32(idx))))
	if v6 {
		level, val = syscall.IPPROTO_IPV6, idx
	}
	return func(network, address string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(syscall.Handle(fd), level, ipUnicastIf, val)
		}); err != nil {
			return err
		}
		return serr
	}
}
