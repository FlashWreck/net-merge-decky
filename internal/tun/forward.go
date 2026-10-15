package tun

import (
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	"github.com/xjasonlyu/tun2socks/v2/tunnel"
)

// installforwarders configures tcp and udp handlers with ipv6 filtering
func installForwarders(s *stack.Stack, allowV6 func() bool) {
	isV6 := func(id stack.TransportEndpointID) bool { return id.LocalAddress.Len() == header.IPv6AddressSize }

	tf := tcp.NewForwarder(s, 0, 2<<10, func(r *tcp.ForwarderRequest) {
		id := r.ID()
		if isV6(id) && !allowV6() {
			r.Complete(true) // rst resets connection for ipv4 fallback
			return
		}
		var wq waiter.Queue
		ep, err := r.CreateEndpoint(&wq)
		if err != nil {
			r.Complete(true) // clean up failed endpoint
			return
		}
		defer r.Complete(false)
		setTCPOptions(s, ep)
		tunnel.T().HandleTCP(&tcpConn{TCPConn: gonet.NewTCPConn(&wq, ep), id: id})
	})
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tf.HandlePacket)

	uf := udp.NewForwarder(s, func(r *udp.ForwarderRequest) bool {
		id := r.ID()
		if isV6(id) && !allowV6() {
			return false // icmp unreachable for udp fallback
		}
		var wq waiter.Queue
		ep, err := r.CreateEndpoint(&wq)
		if err != nil {
			return false
		}
		tunnel.T().HandleUDP(&udpConn{UDPConn: gonet.NewUDPConn(&wq, ep), id: id})
		return true
	})
	s.SetTransportProtocolHandler(udp.ProtocolNumber, uf.HandlePacket)
}

type tcpConn struct {
	*gonet.TCPConn
	id stack.TransportEndpointID
}

func (c *tcpConn) ID() stack.TransportEndpointID { return c.id }

type udpConn struct {
	*gonet.UDPConn
	id stack.TransportEndpointID
}

func (c *udpConn) ID() stack.TransportEndpointID { return c.id }

// settcpoptions configures socket keepalive and buffers
func setTCPOptions(s *stack.Stack, ep tcpip.Endpoint) {
	ep.SocketOptions().SetKeepAlive(true)
	idle := tcpip.KeepaliveIdleOption(60 * time.Second)
	ep.SetSockOpt(&idle)
	interval := tcpip.KeepaliveIntervalOption(30 * time.Second)
	ep.SetSockOpt(&interval)
	ep.SetSockOptInt(tcpip.KeepaliveCountOption, 9)

	var ss tcpip.TCPSendBufferSizeRangeOption
	if err := s.TransportProtocolOption(header.TCPProtocolNumber, &ss); err == nil {
		ep.SocketOptions().SetSendBufferSize(int64(ss.Default), false)
	}
	var rs tcpip.TCPReceiveBufferSizeRangeOption
	if err := s.TransportProtocolOption(header.TCPProtocolNumber, &rs); err == nil {
		ep.SocketOptions().SetReceiveBufferSize(int64(rs.Default), false)
	}
}
