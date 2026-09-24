package jobs

import (
	"sync"
	"time"

	"prism/internal/intel/store"
)

// Progress is one SSE frame payload (§7).
type Progress struct {
	Done    int    `json:"done"`
	Failed  int    `json:"failed"`
	Skipped int    `json:"skipped"`
	Total   int    `json:"total"`
	Status  string `json:"status"`
}

// ProgressFromRow maps the store view onto the SSE payload.
func ProgressFromRow(view store.JobProgress) Progress {
	return Progress{
		Done:    view.Done,
		Failed:  view.Failed,
		Skipped: view.Skipped,
		Total:   view.Total,
		Status:  view.Status,
	}
}

// Subscription is one SSE client of one job.
type Subscription struct {
	C      <-chan Progress
	close  func()
	closed sync.Once
}

// Close releases the subscription. It is safe to call more than once.
func (s *Subscription) Close() {
	if s == nil || s.close == nil {
		return
	}
	s.closed.Do(s.close)
}

type subscriber struct {
	jobID string
	ch    chan Progress
}

// hub is the in-memory SSE broadcaster. It is deliberately bounded: at most
// SSEMaxSubscribers clients exist and each client queue holds sseBufferSize
// frames. A slow client has frames dropped and counted instead of blocking the
// worker pool (R4); disconnecting clients simply re-fetch GET .../jobs/{id}.
type hub struct {
	mu          sync.Mutex
	subscribers map[*subscriber]struct{}
	lastPush    map[string]int64
	dropped     int64
	interval    time.Duration
}

func newHub(timeout time.Duration) *hub {
	return &hub{
		subscribers: make(map[*subscriber]struct{}),
		lastPush:    make(map[string]int64),
		interval:    timeout,
	}
}

// Subscribe registers one client for a job.
func (h *hub) Subscribe(jobID string) (*Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subscribers) >= SSEMaxSubscribers {
		return nil, ErrTooManySubscribers
	}
	sub := &subscriber{jobID: jobID, ch: make(chan Progress, sseBufferSize)}
	h.subscribers[sub] = struct{}{}

	subscription := &Subscription{C: sub.ch}
	subscription.close = func() {
		h.mu.Lock()
		if _, ok := h.subscribers[sub]; ok {
			delete(h.subscribers, sub)
			close(sub.ch)
		}
		h.mu.Unlock()
	}
	return subscription, nil
}

// push fans a frame out to every client of one job. The rate limit of §7 ("at
// most one push per second") is enforced per job against the injected clock.
func (h *hub) push(jobID string, frame Progress, nowNs int64, force bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !force {
		if last, ok := h.lastPush[jobID]; ok && nowNs-last < int64(h.interval) {
			return
		}
	}
	h.lastPush[jobID] = nowNs

	for sub := range h.subscribers {
		if sub.jobID != jobID {
			continue
		}
		select {
		case sub.ch <- frame:
		default:
			// Bounded over-limit behaviour: drop the frame and count it.
			h.dropped++
		}
	}
}

// Forget drops the rate-limit bookkeeping of one finished job.
func (h *hub) Forget(jobID string) {
	h.mu.Lock()
	delete(h.lastPush, jobID)
	h.mu.Unlock()
}

// Subscribers reports the current number of SSE clients.
func (h *hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subscribers)
}

// Dropped reports how many frames were dropped because a client was slow.
func (h *hub) Dropped() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dropped
}
