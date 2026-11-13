package outbound

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/service/pause"
)

// nodePauseManager preserves runtime network/device state while allowing a
// removed node's timer wait to end. WireGuard stops timers synchronously; an
// uncancellable WaitActive there otherwise deadlocks removal during an outage.
type nodePauseManager struct {
	pause.Manager
	ctx      context.Context
	draining atomic.Int32
}

func (m *nodePauseManager) RegisterCallback(callback pause.Callback) *list.Element[pause.Callback] {
	return m.Manager.RegisterCallback(func(event int) {
		// The parent invokes callbacks under its lock. WireGuard's callback
		// synchronously stops timers; allow their current invocation to drain.
		m.draining.Add(1)
		defer m.draining.Add(-1)
		callback(event)
	})
}

func (m *nodePauseManager) WaitActive() {
	// Do not register callbacks here: this method may hold WireGuard's timer
	// lock while a parent pause callback holds the callback lock and stops it.
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for m.IsPaused() && m.draining.Load() == 0 {
		select {
		case <-m.ctx.Done():
			return
		case <-tick.C:
		}
	}
}
