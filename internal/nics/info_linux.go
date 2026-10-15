package nics

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// adapterinfo describes hardware backing and default routes
type adapterInfo struct {
	v4, v6  bool   // default route availability
	virtual string // virtual adapter description if not hardware
	desc    string // kernel driver module name
}

// adapterinfos inspects sysfs and netlink routing tables
func adapterInfos() (map[int]adapterInfo, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	m := map[int]adapterInfo{}
	for _, ifc := range ifaces {
		sys := filepath.Join("/sys/class/net", ifc.Name)
		info := adapterInfo{}
		if drv, err := os.Readlink(filepath.Join(sys, "device", "driver")); err == nil {
			info.desc = filepath.Base(drv)
		}
		if _, err := os.Stat(filepath.Join(sys, "device")); err != nil {
			kind := "software interface"
			if t, err := os.ReadFile(filepath.Join(sys, "type")); err == nil {
				kind += " type " + strings.TrimSpace(string(t))
			}
			info.virtual = fmt.Sprintf("tunnel/virtual interface (%s, no hardware device)", kind)
		}
		m[ifc.Index] = info
	}
	for _, fam := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		routes, err := netlink.RouteListFiltered(fam, &netlink.Route{Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_TABLE)
		if err != nil {
			return nil, err
		}
		for _, r := range routes {
			if r.Dst != nil {
				if ones, _ := r.Dst.Mask.Size(); ones != 0 {
					continue
				}
			}
			links := []int{r.LinkIndex}
			for _, nh := range r.MultiPath {
				links = append(links, nh.LinkIndex)
			}
			for _, idx := range links {
				cur, ok := m[idx]
				if !ok {
					continue
				}
				if fam == netlink.FAMILY_V4 {
					cur.v4 = true
				} else {
					cur.v6 = true
				}
				m[idx] = cur
			}
		}
	}
	return m, nil
}
