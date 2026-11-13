package usage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"prism/internal/model"
)

type JournalStore interface {
	ReserveUsage(model.UsageReservation) error
	SettleUsage(string, int64) (bool, error)
	PendingUsage() ([]model.UsageReservation, error)
}

type pendingSettlement struct {
	user  string
	apply func() error
}

// Journal keeps failed completions retryable in-process. Recovered reservations
// have unknown outcomes and remain closed until an audited offline reconciliation.
// It never infers zero bytes from a crash or refunds an unresolved reservation.
type Journal struct {
	store     JournalStore
	mu        sync.Mutex
	recovered map[string]bool
	pending   map[string]pendingSettlement
	logf      func(string, ...any)
}

func NewJournal(store JournalStore, logf func(string, ...any)) (*Journal, error) {
	rows, err := store.PendingUsage()
	if err != nil {
		return nil, fmt.Errorf("load usage liabilities: %w", err)
	}
	j := &Journal{store: store, recovered: map[string]bool{}, pending: map[string]pendingSettlement{}, logf: logf}
	for _, r := range rows {
		j.recovered[r.UserID] = true
	}
	if len(rows) > 0 && logf != nil {
		logf("usage accounting: %d unresolved reservations; affected access requires offline reconciliation", len(rows))
	}
	return j, nil
}
func (j *Journal) Blocked(user string) bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.recovered[user] {
		return true
	}
	for _, p := range j.pending {
		if p.user == user {
			return true
		}
	}
	return false
}
func (j *Journal) queue(id, user string, apply func() error) {
	j.mu.Lock()
	j.pending[id] = pendingSettlement{user, apply}
	j.mu.Unlock()
	if j.logf != nil {
		j.logf("usage accounting: durable write failed; affected access closed pending reconciliation")
	}
}
func (j *Journal) unknown(user string) {
	j.mu.Lock()
	j.recovered[user] = true
	j.mu.Unlock()
	if j.logf != nil {
		j.logf("usage accounting: unknown write outcome; affected access requires offline reconciliation")
	}
}
func (j *Journal) retry() {
	j.mu.Lock()
	pending := make(map[string]pendingSettlement, len(j.pending))
	for id, p := range j.pending {
		pending[id] = p
	}
	j.mu.Unlock()
	for id, p := range pending {
		if p.apply() == nil {
			j.mu.Lock()
			delete(j.pending, id)
			j.mu.Unlock()
		}
	}
}
func (j *Journal) Start(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			j.retry()
		}
	}
}
