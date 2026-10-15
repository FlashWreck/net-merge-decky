package nics

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// nic represents a physical network adapter
type Nic struct {
	Name   string  `json:"name"`
	Index  int     `json:"index"` // interface index for socket routing
	IP     net.IP  `json:"ip"`
	IP6    net.IP  `json:"ip6"`
	Weight float64 `json:"weight"`
}

// skip represents an excluded adapter and reason
type Skip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// virtual regex matches software bridges and vpn tunnels
var (
	virtualName = regexp.MustCompile(`(?i)loopback|tailscale|zerotier|net-merge|docker|podman|^veth|^virbr|^br-|^wg\d|^tun\d|^tap\d|^zt`)
	virtualDesc = regexp.MustCompile(`(?i)virtual|vpn|\btap\b|tap-|tunnel|wireguard|tailscale|zerotier|openvpn|pseudo`)
)

func DiscoverNics(include []string, weights map[string]float64) ([]Nic, error) {
	nics, _, err := DiscoverNicsDetailed(include, weights)
	return nics, err
}

// discover physical adapters with default routes
func DiscoverNicsDetailed(include []string, weights map[string]float64) ([]Nic, []Skip, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}
	var info map[int]adapterInfo
	if len(include) == 0 {
		info, _ = adapterInfos() // fallback to name filtering on error
	}
	var out []Nic
	var skipped []Skip
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		skip := func(reason string) { skipped = append(skipped, Skip{ifc.Name, reason}) }
		ai, haveInfo := info[ifc.Index]
		if len(include) > 0 {
			if !containsFold(include, ifc.Name) {
				continue
			}
		} else {
			switch {
			case virtualName.MatchString(ifc.Name):
				skip("looks like a virtual/VPN adapter (name)")
				continue
			case info != nil && haveInfo && ai.virtual != "":
				skip(ai.virtual)
				continue
			case info != nil && haveInfo && virtualDesc.MatchString(ai.desc):
				skip(fmt.Sprintf("virtual/VPN adapter (%s)", ai.desc))
				continue
			}
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		var ip4, ip6 net.IP
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if v4 := ipn.IP.To4(); v4 != nil {
				if ip4 == nil && !v4.IsLinkLocalUnicast() { // ignore link local ipv4
					ip4 = v4
				}
			} else if ip6 == nil && ipn.IP.IsGlobalUnicast() && !ipn.IP.IsPrivate() { // ignore link local ipv6
				ip6 = ipn.IP
			}
		}
		if info != nil {
			if !ai.v4 {
				ip4 = nil
			}
			if !ai.v6 {
				ip6 = nil
			}
		}
		if ip4 == nil && ip6 == nil {
			if info != nil && !ai.v4 && !ai.v6 {
				skip("no default gateway (no internet route)")
			} else {
				skip("no usable IP address")
			}
			continue
		}
		w := 1.0
		if v, ok := weights[ifc.Name]; ok {
			w = v
		}
		out = append(out, Nic{Name: ifc.Name, Index: ifc.Index, IP: ip4, IP6: ip6, Weight: w})
	}
	return out, skipped, nil
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
