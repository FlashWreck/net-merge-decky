// package tun captures outbound flows and passes them to balancer
package tun

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/xjasonlyu/tun2socks/v2/core"
	"github.com/xjasonlyu/tun2socks/v2/core/device"
	"github.com/xjasonlyu/tun2socks/v2/core/device/tun"
	"github.com/xjasonlyu/tun2socks/v2/core/option"
	tlog "github.com/xjasonlyu/tun2socks/v2/log"
	M "github.com/xjasonlyu/tun2socks/v2/metadata"
	"github.com/xjasonlyu/tun2socks/v2/proxy/reject"
	"github.com/xjasonlyu/tun2socks/v2/tunnel"

	"net-merge/internal/engine"
)

const (
	tunName = "net-merge"
	tunIP   = "198.18.0.1"
	// ula address allows rfc 6724 to prefer ipv4 for dual stack hosts
	tunIP6 = "fd6e:6d65:7267::1"
)

// pair of slash one routes beat default route without replacing it
var (
	routes4 = []string{"0.0.0.0/1", "128.0.0.0/1"}
	routes6 = []string{"::/1", "8000::/1"}
)

// tunproxy bridges incoming tun packets into balancer
type tunProxy struct{ b *engine.Balancer }

func (p tunProxy) DialContext(ctx context.Context, m *M.Metadata) (net.Conn, error) {
	return p.b.DialPinned(ctx, m.DstIP.Unmap().String(), int(m.DstPort))
}

func (p tunProxy) DialUDP(m *M.Metadata) (net.PacketConn, error) {
	f := engine.Fam4
	if d := m.DstIP.Unmap(); d.Is6() {
		f = engine.Fam6
	}
	return p.b.ListenUDP(context.Background(), f)
}

// tun manages virtual network adapter and netlink routes
type Tun struct {
	mu  sync.Mutex
	B   *engine.Balancer // set before on
	MTU int              // set before on
	dev device.Device
	stk *stack.Stack
	idx int
	on  bool
}

// default mtu for virtual interface
const DefaultMTU = 9000

// udp session timeout for userland stack
const udpSessionTimeout = 30 * time.Second

func New(b *engine.Balancer) *Tun { return &Tun{B: b, MTU: DefaultMTU} }

func (t *Tun) IsOn() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.on
}

func (t *Tun) On() (err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.on {
		return nil
	}
	// log error level to avoid verbose warnings
	if lvl, e := tlog.ParseLevel("error"); e == nil {
		tlog.SetLogger(tlog.Must(tlog.NewLeveled(lvl)))
	}
	tunnel.T().SetUDPTimeout(udpSessionTimeout)
	CleanupStale()

	if vpnDev, hasVPN := DetectActiveVPN(); hasVPN {
		return fmt.Errorf("active VPN detected on %s. Turn off VPN before enabling NetMerge to prevent route conflicts and unencrypted traffic leaks", vpnDev)
	}

	dev, err := tun.Open(tunName, uint32(t.MTU))
	if err != nil {
		return fmt.Errorf("open TUN (%s): %w", openHint, err)
	}
	stk, err := core.CreateStack(&core.Config{
		LinkEndpoint:     dev,
		TransportHandler: tunnel.T(),
		Options:          []option.Option{option.WithTCPModerateReceiveBuffer(true)},
	})
	if err != nil {
		dev.Close()
		return err
	}
	installForwarders(stk, t.B.V6Allowed)
	t.dev, t.stk = dev, stk
	defer func() {
		if err != nil {
			t.teardown()
		}
	}()
	tunnel.T().SetProxy(tunProxy{t.B})

	if t.idx, err = configure(t.MTU); err != nil {
		return err
	}
	t.on = true
	return nil
}

func (t *Tun) Off() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.teardown()
}

func (t *Tun) teardown() {
	if t.idx != 0 {
		delRoutes(t.idx) // restore normal routing
	}
	tunnel.T().SetProxy(&reject.Reject{})
	if t.dev != nil {
		t.dev.Close()
	}
	if t.stk != nil {
		done := make(chan struct{})
		go func() { t.stk.Close(); t.stk.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	t.dev, t.stk, t.idx, t.on = nil, nil, 0, false
}

// cleanupstale removes lingering routes
func CleanupStale() {
	if ifc, err := net.InterfaceByName(tunName); err == nil {
		delRoutes(ifc.Index)
	}
}
