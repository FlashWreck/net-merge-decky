package tun

import (
	"fmt"
	"net"
	"regexp"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"net-merge/internal/logx"
)

const openHint = "needs root or CAP_NET_ADMIN, and /dev/net/tun"

// vpnInterfacePattern matches common VPN and virtual tunnel interface names
var vpnInterfacePattern = regexp.MustCompile(`(?i)^tun|^tap|^wg|tailscale|zerotier|wireguard|openvpn|proton|mullvad|^zt|^ppp`)

func isVPNLink(link netlink.Link) bool {
	if link == nil {
		return false
	}
	attrs := link.Attrs()
	if attrs == nil || attrs.Flags&net.FlagUp == 0 {
		return false
	}
	name := attrs.Name
	if name == tunName {
		return false
	}
	if vpnInterfacePattern.MatchString(name) {
		return true
	}
	linkType := link.Type()
	if linkType == "tun" || linkType == "wireguard" {
		return true
	}
	return false
}

// DetectActiveVPN checks whether an active VPN tunnel holds a default or /1 route
func DetectActiveVPN() (string, bool) {
	for _, fam := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		routes, err := netlink.RouteListFiltered(fam, &netlink.Route{Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_TABLE)
		if err != nil {
			routes, err = netlink.RouteList(nil, fam)
			if err != nil {
				continue
			}
		}
		for _, r := range routes {
			if r.Table == unix.RT_TABLE_LOCAL || r.Table == 255 {
				continue
			}
			isDefault := false
			isSlashOne := false
			if r.Dst == nil {
				isDefault = true
			} else {
				ones, _ := r.Dst.Mask.Size()
				if ones == 0 {
					isDefault = true
				} else if ones == 1 {
					isSlashOne = true
				}
			}

			if !isDefault && !isSlashOne {
				continue
			}

			links := []int{r.LinkIndex}
			for _, nh := range r.MultiPath {
				links = append(links, nh.LinkIndex)
			}

			for _, idx := range links {
				if idx <= 0 {
					continue
				}
				link, err := netlink.LinkByIndex(idx)
				if err != nil {
					continue
				}
				if isVPNLink(link) {
					return link.Attrs().Name, true
				}
			}
		}
	}
	return "", false
}


// configure assigns ip addresses and capturing routes to tun adapter
func configure(int) (int, error) {
	link, err := netlink.LinkByName(tunName)
	if err != nil {
		return 0, fmt.Errorf("adapter %q did not appear: %w", tunName, err)
	}
	idx := link.Attrs().Index
	if err := netlink.AddrReplace(link, &netlink.Addr{IPNet: hostCIDR(tunIP + "/24")}); err != nil {
		return idx, fmt.Errorf("set address: %w", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return idx, fmt.Errorf("bring up: %w", err)
	}
	for _, dst := range routes4 {
		if err := netlink.RouteReplace(tunRoute(idx, dst)); err != nil {
			return idx, fmt.Errorf("route %s: %w", dst, err)
		}
	}
	// ipv6 is best effort and skips dad for immediate availability
	if err := netlink.AddrReplace(link, &netlink.Addr{IPNet: hostCIDR(tunIP6 + "/64"), Flags: unix.IFA_F_NODAD}); err != nil {
		logx.Printf("warning: IPv6 not captured: %v\n", err)
	} else {
		for _, dst := range routes6 {
			if err := netlink.RouteReplace(tunRoute(idx, dst)); err != nil {
				logx.Printf("warning: IPv6 route %s: %v\n", dst, err)
			}
		}
	}
	return idx, nil
}

// delroutes deletes capturing routes from routing table
func delRoutes(idx int) {
	for _, dst := range append(append([]string{}, routes4...), routes6...) {
		netlink.RouteDel(tunRoute(idx, dst)) // ignore not found errors
	}
}

func tunRoute(idx int, dst string) *netlink.Route {
	return &netlink.Route{LinkIndex: idx, Dst: prefix(dst), Scope: netlink.SCOPE_LINK}
}

// hostcidr parses cidr string into ipnet
func hostCIDR(s string) *net.IPNet {
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

func prefix(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}
