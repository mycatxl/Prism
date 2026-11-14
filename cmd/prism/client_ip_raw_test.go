package main

import (
	"net"
	"testing"

	"prism/internal/proxy"
)

type rawClientPeerConn struct {
	net.Conn
	peer net.Addr
}

func (c rawClientPeerConn) RemoteAddr() net.Addr { return c.peer }

func TestRawSOCKSClientIPAlwaysComesFromTCPPeer(t *testing.T) {
	proxy.SetTrustedProxies([]string{"127.0.0.1/32", "10.0.0.0/8"})
	t.Cleanup(func() { proxy.SetTrustedProxies(nil) })
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "198.51.100.7", "2001:db8::7"} {
		conn := rawClientPeerConn{peer: &net.TCPAddr{IP: net.ParseIP(ip), Port: 54321}}
		if got := inboundConnClientIP(conn); got != ip {
			t.Fatalf("raw peer attribution = %q, want %q", got, ip)
		}
	}
	if inboundConnClientIP(nil) != "" || inboundConnClientIP(rawClientPeerConn{}) != "" {
		t.Fatal("missing raw peers must not invent an address")
	}
}
