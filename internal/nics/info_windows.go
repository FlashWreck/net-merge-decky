package nics

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// adapterInfo is what Windows knows about an adapter beyond what package net exposes.
type adapterInfo struct {
	v4, v6  bool   // has an IPv4 / IPv6 default gateway (can reach the internet over that family)
	virtual string // why this is not real network hardware; empty if it is
	desc    string
}

// IANA ifType values for real network hardware. Tunnels (131), PPP (23), "proprietary virtual"
// (53, used by most VPN drivers) and software loopback (24) are not in this set.
func physicalIfType(t uint32) bool {
	switch t {
	case 6, 62, 69, 117, // ethernet (incl. fast / gigabit variants)
		71,       // IEEE 802.11 Wi-Fi
		243, 244: // mobile broadband (WWAN)
		return true
	}
	return false
}

// adapterInfos returns, per interface index, the up adapters with their gateway families, type
// and description. This is what lets any adapter (Wi-Fi, Ethernet, USB tether, phone hotspot)
// be picked up automatically while VPNs, tunnels, virtual switches and LAN-only adapters are
// skipped, without maintaining a list of product names.
func adapterInfos() (map[int]adapterInfo, error) {
	const flags = windows.GAA_FLAG_INCLUDE_GATEWAYS | windows.GAA_FLAG_SKIP_ANYCAST |
		windows.GAA_FLAG_SKIP_MULTICAST | windows.GAA_FLAG_SKIP_DNS_SERVER
	size := uint32(32 << 10)
	for try := 0; try < 4; try++ {
		buf := make([]byte, size)
		aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, aa, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return nil, err
		}
		m := map[int]adapterInfo{}
		for a := aa; a != nil; a = a.Next {
			if a.OperStatus != windows.IfOperStatusUp {
				continue
			}
			info := adapterInfo{desc: windows.UTF16PtrToString(a.Description)}
			if !physicalIfType(a.IfType) {
				info.virtual = fmt.Sprintf("tunnel/virtual interface type %d (%s)", a.IfType, info.desc)
			}
			for g := a.FirstGatewayAddress; g != nil; g = g.Next {
				if g.Address.Sockaddr == nil {
					continue
				}
				switch g.Address.Sockaddr.Addr.Family {
				case windows.AF_INET:
					info.v4 = true
				case windows.AF_INET6:
					info.v6 = true
				}
			}
			// One adapter can appear per family; merge by index.
			for _, idx := range []uint32{a.IfIndex, a.Ipv6IfIndex} {
				if idx == 0 {
					continue
				}
				cur := m[int(idx)]
				cur.v4, cur.v6 = cur.v4 || info.v4, cur.v6 || info.v6
				cur.virtual, cur.desc = info.virtual, info.desc
				m[int(idx)] = cur
			}
		}
		return m, nil
	}
	return nil, windows.ERROR_BUFFER_OVERFLOW
}
