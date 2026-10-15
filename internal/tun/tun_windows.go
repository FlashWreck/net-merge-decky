package tun

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	wun "golang.zx2c4.com/wireguard/tun"

	"net-merge/internal/logx"
)

const openHint = "needs admin and wintun.dll next to net-merge.exe"

// routes4 as `route` wants them: destination and mask.
var routes4win = [][2]string{{"0.0.0.0", "128.0.0.0"}, {"128.0.0.0", "128.0.0.0"}}

func init() {
	wun.WintunTunnelType = "net-merge"
	// A fixed GUID keeps one Windows network profile instead of a new "Unidentified network" per run.
	if g, err := windows.GUIDFromString("{3f0d5a7e-9b1c-4e52-8a47-1c6b2d9e5f30}"); err == nil {
		wun.WintunStaticRequestedGUID = &g
	}
}

// configure gives the new adapter its addresses, MTU and routes, and returns its index.
func configure(mtu int) (int, error) {
	if err := run("netsh", "interface", "ipv4", "set", "address", "name="+tunName, "source=static",
		"address="+tunIP, "mask="+tunMask, "store=active"); err != nil {
		return 0, err
	}
	ifc, err := waitIface(tunName, 5*time.Second)
	if err != nil {
		return 0, err
	}
	// Windows gives Wintun adapters an MTU of 65535. Match the stack's MTU, so Windows sizes TCP
	// segments to it and fragments bigger UDP datagrams instead of sending packets the stack drops.
	idx := strconv.Itoa(ifc.Index)
	mtuArg := "mtu=" + strconv.Itoa(mtu)
	if e := run("netsh", "interface", "ipv4", "set", "subinterface", "interface="+idx, mtuArg, "store=active"); e != nil {
		logx.Printf("warning: could not set IPv4 MTU: %v\n", e)
	}
	for _, dst := range routes4win {
		if err = run("route", "add", dst[0], "mask", dst[1], "0.0.0.0", "metric", "1", "if", idx); err != nil {
			return ifc.Index, err
		}
	}
	// IPv6 is best-effort: if it can't be set up, IPv4 still works and IPv6 stays on its normal path.
	if e := run("netsh", "interface", "ipv6", "add", "address", "interface="+idx, "address="+tunIP6+"/64", "store=active"); e != nil {
		logx.Printf("warning: IPv6 not captured: %v\n", e)
	} else {
		if e := run("netsh", "interface", "ipv6", "set", "subinterface", "interface="+idx, mtuArg, "store=active"); e != nil {
			logx.Printf("warning: could not set IPv6 MTU: %v\n", e)
		}
		for _, p := range routes6 {
			if e := run("netsh", "interface", "ipv6", "add", "route", "prefix="+p, "interface="+idx, "metric=1", "store=active"); e != nil {
				logx.Printf("warning: IPv6 route %s: %v\n", p, e)
			}
		}
	}
	return ifc.Index, nil
}

func delRoutes(idx int) {
	for _, dst := range routes4win {
		run("route", "delete", dst[0], "mask", dst[1], "0.0.0.0", "if", strconv.Itoa(idx))
	}
	for _, p := range routes6 {
		run("netsh", "interface", "ipv6", "delete", "route", "prefix="+p, "interface="+strconv.Itoa(idx), "store=active")
	}
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func waitIface(name string, d time.Duration) (*net.Interface, error) {
	deadline := time.Now().Add(d)
	for {
		ifc, err := net.InterfaceByName(name)
		if err == nil {
			return ifc, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("adapter %q did not appear: %w", name, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
