package engine

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"net-merge/internal/nics"
)

// balancer dials outbound flows through the scheduler
type Balancer struct {
	S              *Scheduler
	ConnectTimeout time.Duration
	FailCooldown   time.Duration
	v6mode         atomic.Int32
}

// v6mode controls ipv6 balancing policy
type V6Mode int32

const (
	// auto balances ipv6 when all links support it
	V6Auto V6Mode = iota
	// on balances ipv6 across capable links
	V6On
	// off disables ipv6
	V6Off
)

func ParseV6Mode(s string) (V6Mode, bool) {
	switch s {
	case "auto":
		return V6Auto, true
	case "on":
		return V6On, true
	case "off":
		return V6Off, true
	}
	return V6Auto, false
}

func (m V6Mode) String() string {
	switch m {
	case V6Auto:
		return "auto"
	case V6On:
		return "on"
	case V6Off:
		return "off"
	default:
		return "auto"
	}
}

func (b *Balancer) SetV6Mode(m V6Mode) { b.v6mode.Store(int32(m)) }
func (b *Balancer) V6Mode() V6Mode     { return V6Mode(b.v6mode.Load()) }

// v6allowed checks if ipv6 is permitted
func (b *Balancer) V6Allowed() bool {
	switch b.V6Mode() {
	case V6Off:
		return false
	case V6On:
		return b.S.V6Usable()
	}
	return b.S.V6CoversAll()
}

// v6state returns current ipv6 state string
func (b *Balancer) V6State() string {
	switch b.V6Mode() {
	case V6Off:
		return "off (refused, apps use IPv4)"
	case V6On:
		return "on (balanced over the adapters that have IPv6)"
	}
	if b.S.V6CoversAll() {
		return "auto: balanced (every adapter has IPv6)"
	}
	return "auto: refused, apps fall back to IPv4 so every adapter is used (not every adapter has IPv6)"
}

func NewBalancer(s *Scheduler) *Balancer {
	return &Balancer{S: s, ConnectTimeout: 8 * time.Second, FailCooldown: 15 * time.Second}
}

type dialClass struct {
	retry    bool
	linkDown bool
}

func classify(err error) dialClass {
	var dns *net.DNSError
	var ne net.Error
	switch {
	case errors.As(err, &dns):
		return dialClass{}
	case errors.Is(err, syscall.EADDRNOTAVAIL), errors.Is(err, syscall.ENETDOWN), errors.Is(err, syscall.ENETUNREACH):
		return dialClass{retry: true, linkDown: true}
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ETIMEDOUT):
		return dialClass{retry: true}
	case errors.Is(err, syscall.ECONNREFUSED):
		return dialClass{}
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return dialClass{retry: true}
	}
	return dialClass{}
}

// dialtcp connects to host and port over selected adapter
func (b *Balancer) DialTCP(ctx context.Context, host string, port int) (net.Conn, *Slot, Family, error) {
	f, network := Fam4, "tcp4"
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		f, network = Fam6, "tcp6"
	}
	tried := map[string]struct{}{}
	var lastErr error = errors.New("no usable network adapter")
	if f == Fam6 {
		lastErr = errors.New("no usable IPv6 adapter")
	}
	for {
		sl := b.S.AcquireFor(f, tried)
		if sl == nil {
			return nil, nil, f, lastErr
		}
		tried[sl.key] = struct{}{}
		d := net.Dialer{
			LocalAddr: &net.TCPAddr{IP: sl.ip(f)},            // bind source ip
			Control:   nics.ForceEgress(sl.Index, f == Fam6), // force interface routing
			Timeout:   b.ConnectTimeout,
			KeepAlive: 30 * time.Second,
		}
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(host, strconv.Itoa(port)))
		if err == nil {
			return conn, sl, f, nil
		}
		b.S.ReleaseFor(sl, f)
		c := classify(err)
		if c.linkDown {
			b.S.Fail(sl, f, b.FailCooldown)
		}
		lastErr = err
		if !c.retry || ctx.Err() != nil {
			return nil, nil, f, err
		}
	}
}
