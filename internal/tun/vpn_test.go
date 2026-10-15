package tun

import (
	"testing"
)

func TestVPNInterfacePattern(t *testing.T) {
	vpnNames := []string{
		"tun0", "tun1", "tun-mullvad",
		"tap0", "tap1",
		"wg0", "wg1", "wg-home",
		"tailscale0", "tailscale1",
		"zerotier0", "zt12345",
		"wireguard0",
		"openvpn", "openvpn0",
		"proton0", "proton-vpn",
		"mullvad0",
		"ppp0",
	}

	for _, name := range vpnNames {
		if !vpnInterfacePattern.MatchString(name) {
			t.Errorf("expected %q to match vpnInterfacePattern", name)
		}
	}

	nonVPNNames := []string{
		"eth0", "eth1", "eno1", "enp3s0",
		"wlan0", "wlan1", "wlp2s0",
		"usb0", "usb1",
		"docker0",
		"lo",
		"net-merge",
	}

	for _, name := range nonVPNNames {
		if vpnInterfacePattern.MatchString(name) {
			t.Errorf("expected %q NOT to match vpnInterfacePattern", name)
		}
	}
}

func TestDetectActiveVPNSmoke(t *testing.T) {
	dev, hasVPN := DetectActiveVPN()
	t.Logf("DetectActiveVPN result: dev=%q, hasVPN=%v", dev, hasVPN)
}
