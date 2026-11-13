package node

import (
	"context"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
	"io"
	"net"
	"sync"
)

// GuardPrivacyOutbound ties probes and customer streams to this node's lifetime.
// Ordinary health failures never cancel it; a privacy deletion closes every
// established socket and cancels dials already in progress.
func (e *NodeEntry) GuardPrivacyOutbound(ob adapter.Outbound) adapter.Outbound {
	return &privacyOutbound{Outbound: ob, entry: e}
}

type privacyOutbound struct {
	adapter.Outbound
	entry *NodeEntry
}

func (o *privacyOutbound) Close() error {
	if c, ok := o.Outbound.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
func (o *privacyOutbound) DialContext(ctx context.Context, network string, dst M.Socksaddr) (net.Conn, error) {
	ctx, cancel := o.entry.BindPrivacyContext(ctx)
	defer cancel()
	conn, err := o.Outbound.DialContext(ctx, network, dst)
	if err != nil {
		return nil, err
	}
	release, ok := o.entry.trackPrivacyCloser(conn)
	if !ok {
		return nil, context.Canceled
	}
	return &privacyConn{Conn: conn, release: release}, nil
}
func (o *privacyOutbound) ListenPacket(ctx context.Context, dst M.Socksaddr) (net.PacketConn, error) {
	ctx, cancel := o.entry.BindPrivacyContext(ctx)
	defer cancel()
	conn, err := o.Outbound.ListenPacket(ctx, dst)
	if err != nil {
		return nil, err
	}
	release, ok := o.entry.trackPrivacyCloser(conn)
	if !ok {
		return nil, context.Canceled
	}
	return &privacyPacketConn{PacketConn: conn, release: release}, nil
}
func (e *NodeEntry) trackPrivacyCloser(c io.Closer) (func(), bool) {
	e.privacyConnMu.Lock()
	if e.privacyClosed {
		e.privacyConnMu.Unlock()
		_ = c.Close()
		return func() {}, false
	}
	if e.privacyConns == nil {
		e.privacyConns = map[io.Closer]struct{}{}
	}
	e.privacyConns[c] = struct{}{}
	e.privacyConnMu.Unlock()
	return func() { e.privacyConnMu.Lock(); delete(e.privacyConns, c); e.privacyConnMu.Unlock() }, true
}

type privacyConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *privacyConn) Close() error { c.once.Do(c.release); return c.Conn.Close() }
func (c *privacyConn) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return c.Close()
}
func (c *privacyConn) CloseRead() error {
	if half, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return half.CloseRead()
	}
	return c.Close()
}

type privacyPacketConn struct {
	net.PacketConn
	release func()
	once    sync.Once
}

func (c *privacyPacketConn) Close() error { c.once.Do(c.release); return c.PacketConn.Close() }
