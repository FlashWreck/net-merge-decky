// Package tun is the system-wide front-end: a virtual adapter whose routes capture every flow,
// terminated in a userland TCP/IP stack and re-dialed through the Balancer.
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
	tunMask = "255.255.255.0"
	// A ULA on purpose. The OS's source-address policy (RFC 6724, on Windows and in glibc) prefers
	// IPv4 over IPv6 when the only IPv6 source on the route is a ULA, so dual-stack sites go over
	// IPv4 and spread across every adapter, while IPv6-only sites (and explicit IPv6) still work
	// through the IPv6 path. A global-scope address here makes the OS prefer IPv6 everywhere, which
	// funnels all traffic onto whichever adapters happen to have IPv6. Nothing is ever sent from
	// this address.
	tunIP6 = "fd6e:6d65:7267::1"
)

// Two /1 routes per family beat any default route (/0) without touching it, so Off() is just
// deleting these.
var (
	routes4 = []string{"0.0.0.0/1", "128.0.0.0/1"}
	routes6 = []string{"::/1", "8000::/1"}
)

// tunProxy plugs the Balancer into tun2socks: every TCP/UDP flow that enters the TUN is dialed
// out through a pinned NIC. There is no SOCKS hop in between.
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

// Tun is the system-wide switch: On creates the adapter and routes, Off removes them. Both are
// idempotent. If the process dies, the adapter and its routes die with it.
type Tun struct {
	mu  sync.Mutex
	B   *engine.Balancer // set before On
	MTU int              // set before On; see DefaultMTU
	dev device.Device
	stk *stack.Stack
	idx int
	on  bool
}

// DefaultMTU is the TUN's MTU, used both by the userland stack and by the OS. TUN traffic never
// leaves the machine (flows are re-dialed with normal sockets), so it isn't bound by the links'
// 1500. Bigger packets mean far fewer per-packet trips through the userland stack: about 6x fewer
// for bulk TCP at 9000. Not 65535: the TUN library allocates a full-MTU buffer for every inbound
// packet, even a 40-byte ACK.
const DefaultMTU = 9000

// udpSessionTimeout ends idle UDP sessions sooner than tun2socks' 60s. Torrent DHT opens a session
// per peer queried, each holding a socket, two goroutines and two 64 KB buffers. 30s stays above the
// ~25s keepalives common in VPN and NAT-traversal protocols.
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
	// "error", not "warn": tun2socks warns once per dropped UDP packet ("symmetric NAT ... drop
	// packet"), which a torrent client triggers continuously.
	if lvl, e := tlog.ParseLevel("error"); e == nil {
		tlog.SetLogger(tlog.Must(tlog.NewLeveled(lvl)))
	}
	tunnel.T().SetUDPTimeout(udpSessionTimeout)
	CleanupStale()

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
		delRoutes(t.idx) // traffic falls straight back to the normal default route
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

// CleanupStale removes routes left on a surviving net-merge adapter (e.g. after a crash).
func CleanupStale() {
	if ifc, err := net.InterfaceByName(tunName); err == nil {
		delRoutes(ifc.Index)
	}
}
