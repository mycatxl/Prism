package proxy

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// panickingReader panics on Read, standing in for a copy loop that hits a bug.
type panickingReader struct{}

func (panickingReader) Read([]byte) (int, error) {
	panic("boom in a copy loop")
}

// A panic in one direction of the relay must end that tunnel, not the process,
// and must still report a result: the relay waits on both channels for the byte
// counts, so a goroutine that died without sending would park the tunnel forever
// as well.
func TestPumpPreparedTunnelSurvivesAPanickingCopy(t *testing.T) {
	client, clientPeer := net.Pipe()
	upstream, upstreamPeer := net.Pipe()
	defer clientPeer.Close()
	defer upstreamPeer.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(io.Discard, clientPeer)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(io.Discard, upstreamPeer)
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			// Nothing may escape: if it did, this test would crash the whole
			// run rather than fail.
			if recovered := recover(); recovered != nil {
				t.Errorf("the panic escaped the relay: %v", recovered)
			}
		}()
		_ = pumpPreparedTunnelReader(client, panickingReader{}, &preparedTunnel{upstreamConn: upstream}, tunnelPumpOptions{})
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the relay never returned: a copy direction died without reporting")
	}

	_ = clientPeer.Close()
	_ = upstreamPeer.Close()
	wg.Wait()
}

// The panic is reported as a copy error, not as a clean tunnel: the caller must
// not record a healthy relay for a direction that never finished.
func TestPumpPreparedTunnelReportsAPanickingCopyAsAnError(t *testing.T) {
	client, clientPeer := net.Pipe()
	upstream, upstreamPeer := net.Pipe()
	defer clientPeer.Close()
	defer upstreamPeer.Close()

	go func() { _, _ = io.Copy(io.Discard, clientPeer) }()
	go func() { _, _ = io.Copy(io.Discard, upstreamPeer) }()

	result := make(chan tunnelRelayResult, 1)
	go func() {
		result <- pumpPreparedTunnelReader(client, panickingReader{}, &preparedTunnel{upstreamConn: upstream}, tunnelPumpOptions{})
	}()

	select {
	case got := <-result:
		if got.netOK {
			t.Fatalf("a panicking copy was reported as a healthy relay: %+v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the relay never reported a result")
	}

	_ = clientPeer.Close()
	_ = upstreamPeer.Close()
}
