package outbound

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

func TestQuietRuntimeKeepsLoopbackNetworkActive(t *testing.T) {
	runtime, err := NewSingboxRuntime(SingboxBuilderConfig{QuietInterfaceMonitor: true, DNSUpstreams: []string{"https://1.1.1.1/dns-query"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	if service.FromContext[pause.Manager](runtime.ctx).IsNetworkPaused() {
		t.Fatal("loopback-only fixture was marked offline; WireGuard retransmission cannot proceed")
	}
}

func TestNodePauseCallbackCanDrainWaitingTimer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = pause.WithDefaultManager(ctx)
	parent := service.FromContext[pause.Manager](ctx)
	manager := &nodePauseManager{Manager: parent, ctx: ctx}
	callback := manager.RegisterCallback(func(int) {
		drained := make(chan struct{})
		go func() { manager.WaitActive(); close(drained) }()
		<-drained // Models Device.Down synchronously waiting for a timer.
	})
	defer manager.UnregisterCallback(callback)
	paused := make(chan struct{})
	go func() { parent.NetworkPause(); close(paused) }()
	select {
	case <-paused:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("network pause deadlocked with a timer waiting for network recovery")
	}
	if !parent.IsNetworkPaused() {
		t.Fatal("draining a timer incorrectly resumed the network")
	}
}

func TestClosingNodeCancelsPauseWaitWithoutWakingOtherNodes(t *testing.T) {
	runtime, err := NewSingboxRuntime(SingboxBuilderConfig{QuietInterfaceMonitor: true, DNSUpstreams: []string{"https://1.1.1.1/dns-query"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	var handles []*singboxHandle
	for range 2 {
		ob, err := runtime.Build([]byte(`{"type":"direct"}`))
		if err != nil {
			t.Fatal(err)
		}
		handle := ob.(*singboxHandle)
		handles = append(handles, handle)
		defer func() { _ = handle.Close() }()
	}
	parent := service.FromContext[pause.Manager](runtime.ctx)
	parent.NetworkPause()
	waits := []chan struct{}{make(chan struct{}), make(chan struct{})}
	for i, handle := range handles {
		manager := service.FromContext[pause.Manager](handle.ctx)
		go func() { manager.WaitActive(); close(waits[i]) }()
	}
	if err := handles[0].Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waits[0]:
	case <-time.After(time.Second):
		t.Fatal("removed node's pause wait did not end")
	}
	if !parent.IsNetworkPaused() {
		t.Fatal("closing a node changed the runtime network state")
	}
	select {
	case <-waits[1]:
		t.Fatal("closing one node released another node's wait")
	default:
	}
	parent.NetworkWake()
	select {
	case <-waits[1]:
	case <-time.After(time.Second):
		t.Fatal("network recovery did not release surviving node")
	}
}
