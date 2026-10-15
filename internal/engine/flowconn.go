package engine

import (
	"context"
	"errors"
	"net"
	"sync"

	"net-merge/internal/nics"
)

// slotconn tracks flow metrics and releases slot on close
type slotConn struct {
	*net.TCPConn
	sl   *Slot
	fam  Family
	s    *Scheduler
	once sync.Once
}

func (c *slotConn) Read(p []byte) (int, error) {
	n, err := c.TCPConn.Read(p)
	c.sl.bytesIn[c.fam].Add(int64(n))
	return n, err
}

func (c *slotConn) Write(p []byte) (int, error) {
	n, err := c.TCPConn.Write(p)
	c.sl.bytesOut[c.fam].Add(int64(n))
	return n, err
}

// release decrements balancing count on first half close
func (c *slotConn) release() { c.once.Do(func() { c.s.ReleaseFor(c.sl, c.fam) }) }

func (c *slotConn) CloseRead() error  { c.release(); return c.TCPConn.CloseRead() }
func (c *slotConn) CloseWrite() error { c.release(); return c.TCPConn.CloseWrite() }
func (c *slotConn) Close() error      { c.release(); return c.TCPConn.Close() }

// dialpinned dials tcp flow wrapped in slotconn
func (b *Balancer) DialPinned(ctx context.Context, host string, port int) (net.Conn, error) {
	c, sl, f, err := b.DialTCP(ctx, host, port)
	if err != nil {
		return nil, err
	}
	return &slotConn{TCPConn: c.(*net.TCPConn), sl: sl, fam: f, s: b.S}, nil
}

type slotPacketConn struct {
	net.PacketConn
	sl  *Slot
	fam Family
}

func (c *slotPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, a, err := c.PacketConn.ReadFrom(p)
	c.sl.bytesIn[c.fam].Add(int64(n))
	return n, a, err
}

func (c *slotPacketConn) WriteTo(p []byte, a net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(p, a)
	c.sl.bytesOut[c.fam].Add(int64(n))
	return n, err
}

// listenudp opens udp socket pinned to selected adapter
func (b *Balancer) ListenUDP(ctx context.Context, f Family) (net.PacketConn, error) {
	network := "udp4"
	if f == Fam6 {
		network = "udp6"
	}
	tried := map[string]struct{}{}
	lastErr := errors.New("no usable network adapter")
	for {
		sl := b.S.AcquireFor(f, tried)
		if sl == nil {
			return nil, lastErr
		}
		tried[sl.key] = struct{}{}
		lc := net.ListenConfig{Control: nics.ForceEgress(sl.Index, f == Fam6)}
		pc, err := lc.ListenPacket(ctx, network, net.JoinHostPort(sl.ip(f).String(), "0"))
		b.S.ReleaseFor(sl, f)
		if err != nil {
			if classify(err).linkDown {
				b.S.Fail(sl, f, b.FailCooldown)
			}
			lastErr = err
			continue
		}
		return &slotPacketConn{PacketConn: pc, sl: sl, fam: f}, nil
	}
}
