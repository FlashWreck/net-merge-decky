package nics

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// forceegress binds socket to interface index to prevent routing loops
func ForceEgress(idx int, v6 bool) func(network, address string, c syscall.RawConn) error {
	if idx <= 0 {
		return nil
	}
	return func(network, address string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BINDTOIFINDEX, idx)
			if serr == unix.ENOPROTOOPT {
				var ifc *net.Interface
				if ifc, serr = net.InterfaceByIndex(idx); serr == nil {
					serr = unix.BindToDevice(int(fd), ifc.Name)
				}
			}
		}); err != nil {
			return err
		}
		return serr
	}
}
