package nics

import "golang.org/x/sys/windows"

// Octets reads Windows' own byte counters for an adapter. They cover every app's traffic, not
// just what net-merge relays: apps like qBittorrent bind to each adapter directly and bypass the
// TUN, but still show up here.
func Octets(idx int) (in, out uint64, ok bool) {
	if idx <= 0 {
		return 0, 0, false
	}
	row := windows.MibIfRow2{InterfaceIndex: uint32(idx)}
	if windows.GetIfEntry2Ex(windows.MibIfEntryNormal, &row) != nil {
		return 0, 0, false
	}
	return row.InOctets, row.OutOctets, true
}
