package requestlog

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	"prism/internal/proxy"
	"prism/internal/safego"
)

// Flush retry policy.
const (
	// defaultRetryBackoffMin and defaultRetryBackoffMax bound the wait between
	// two attempts at a batch that failed to reach the database. The first retry
	// is quick because the common failure is a transient one - SQLite's
	// "database is locked", a rotation in progress, a disk that filled for a
	// moment - and the wait doubles up to the ceiling so a database that stays
	// down is not hammered once a second forever. Stopping at half a minute
	// still delivers a batch a few seconds late rather than a few minutes late
	// once the database is back.
	defaultRetryBackoffMin = 250 * time.Millisecond
	defaultRetryBackoffMax = 30 * time.Second

	// defaultMaxFlushAttempts is how many times one batch is offered to the
	// repository before the service gives up on it. With the backoff above that
	// is a window of about a minute, which covers a rotation or a VACUUM; past
	// it the store is not merely busy, and holding the entries forever would
	// cost memory that the accounting side needs.
	defaultMaxFlushAttempts = 6
)

// requestLogInserter is the persistence surface the flush loop needs.
//
// It is an interface rather than *Repo so a test can inject a store that fails
// the way a real one does - "database is locked", a full disk, a rotation in
// progress - and observe what the service does with the entries afterwards.
// *Repo is the only production implementation.
type requestLogInserter interface {
	InsertBatch(entries []proxy.RequestLogEntry) (int, error)
}

// Service provides an async request log writer.
// EmitRequestLog performs a non-blocking channel send (drops on overflow).
// A background goroutine flushes batches to the Repo.
type Service struct {
	repo      *Repo
	insert    requestLogInserter
	queue     chan proxy.RequestLogEntry
	batchSize int
	interval  time.Duration
	flushReq  chan chan struct{}

	// retry holds the entries of a failed flush, oldest first, until the next
	// attempt. They are the batch itself rather than a copy, so a failed write
	// neither loses the records nor duplicates them once it succeeds.
	retry []proxy.RequestLogEntry
	// retryAttempts counts consecutive failures of the current batch. It is
	// reset by a successful write, so the backoff measures one outage rather
	// than the lifetime of the process.
	retryAttempts int
	// retryAt is when the next attempt may run. Zero means "nothing waiting".
	retryAt time.Time

	// retryBackoffMin, retryBackoffMax and maxFlushAttempts are the policy
	// resolved from the constants above. They are fields so a test can shorten
	// the waits instead of sleeping through them.
	retryBackoffMin  time.Duration
	retryBackoffMax  time.Duration
	maxFlushAttempts int

	// dropped counts the entries the service could not deliver at all, whether
	// because the retry buffer was full or because the attempts ran out. It is
	// atomic because it is meant to be reported while the service is running.
	dropped atomic.Uint64

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// ServiceConfig configures the request log service.
type ServiceConfig struct {
	Repo          *Repo
	QueueSize     int
	FlushBatch    int
	FlushInterval time.Duration
}

// NewService creates a new request log service.
func NewService(cfg ServiceConfig) *Service {
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = 8192
	}
	batchSize := cfg.FlushBatch
	if batchSize <= 0 {
		batchSize = 4096
	}
	interval := cfg.FlushInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	var insert requestLogInserter
	if cfg.Repo != nil {
		insert = cfg.Repo
	}
	return &Service{
		repo:             cfg.Repo,
		insert:           insert,
		queue:            make(chan proxy.RequestLogEntry, queueSize),
		batchSize:        batchSize,
		interval:         interval,
		flushReq:         make(chan chan struct{}, 64),
		retryBackoffMin:  defaultRetryBackoffMin,
		retryBackoffMax:  defaultRetryBackoffMax,
		maxFlushAttempts: defaultMaxFlushAttempts,
		stopCh:           make(chan struct{}),
	}
}

// Start launches the background flush goroutine.
func (s *Service) Start() {
	if s.repo != nil {
		s.repo.setReadBarrier(s.FlushNow)
	}
	s.wg.Add(1)
	// flushLoop owns its wg.Done; the wrapper only adds the panic boundary.
	safego.Go("the request log flush loop", s.flushLoop)
}

// Stop signals the flush loop to stop, drains remaining entries, and returns.
func (s *Service) Stop() {
	if s.repo != nil {
		s.repo.setReadBarrier(nil)
	}
	close(s.stopCh)
	s.wg.Wait()
}

// EmitRequestLog enqueues a log entry. Non-blocking; drops on overflow.
func (s *Service) EmitRequestLog(entry proxy.RequestLogEntry) {
	select {
	case s.queue <- entry:
	default:
		// Queue full — drop entry to avoid blocking hot path.
	}
}

// FlushNow asks the background writer to flush current buffered data to DB,
// then blocks until that flush attempt completes.
func (s *Service) FlushNow() {
	done := make(chan struct{})
	select {
	case s.flushReq <- done:
	case <-s.stopCh:
		return
	}
	select {
	case <-done:
	case <-s.stopCh:
	}
}

// flushLoop runs until stopCh is closed, flushing on batch-size or timer, and
// retrying a batch whose write failed once its backoff has expired.
func (s *Service) flushLoop() {
	defer s.wg.Done()

	batch := make([]proxy.RequestLogEntry, 0, s.batchSize)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	// retryTimer is armed only while a failed batch is waiting, so an idle
	// service costs no wake-ups. The wait is recomputed at the top of every
	// iteration: taking an entry from the queue or serving a barrier flush can
	// change it, and a stale deadline would either retry too early or too late.
	retryTimer := time.NewTimer(time.Hour)
	stopAndDrainTimer(retryTimer)

	for {
		var retryC <-chan time.Time
		if wait, waiting := s.retryWait(); waiting {
			if wait <= 0 {
				s.flushRetry()
				continue
			}
			resetTimer(retryTimer, wait)
			retryC = retryTimer.C
		}

		select {
		case entry := <-s.queue:
			batch = append(batch, entry)
			if len(batch) >= s.batchSize {
				s.flush(batch)
				batch = batch[:0]
			}

		case <-ticker.C:
			if len(batch) > 0 {
				s.flush(batch)
				batch = batch[:0]
			}

		case done := <-s.flushReq:
			batch = s.flushOnBarrier(batch, done)

		case <-retryC:
			// The wait has expired; the top of the loop performs the attempt.

		case <-s.stopCh:
			// Drain remaining.
			s.drainAndFlush(batch)
			return
		}
	}
}

// retryWait reports whether a failed batch is waiting and how long is left
// before it may be attempted again.
func (s *Service) retryWait() (time.Duration, bool) {
	if len(s.retry) == 0 {
		return 0, false
	}
	return time.Until(s.retryAt), true
}

// flushRetry offers the requeued batch to the repository again.
func (s *Service) flushRetry() {
	if len(s.retry) == 0 {
		s.clearRetry()
		return
	}
	pending := s.retry
	s.retry = nil
	s.flush(pending)
}

// retryBufferLimit is how many entries a failed flush may keep for a later
// attempt: the ingest queue plus one batch, which is everything that could
// legitimately be waiting at the moment a write fails. A larger backlog would
// turn a database that stays down into unbounded growth on the data plane's
// heap, so past it the oldest entries are dropped and counted.
func (s *Service) retryBufferLimit() int {
	limit := cap(s.queue) + s.batchSize
	if limit <= 0 {
		limit = s.batchSize
	}
	if limit <= 0 {
		limit = 8192
	}
	return limit
}

// retryBackoff is the wait before the next attempt: doubling from the minimum
// up to the ceiling.
func (s *Service) retryBackoff() time.Duration {
	delay := s.retryBackoffMin
	if delay <= 0 {
		delay = defaultRetryBackoffMin
	}
	ceiling := s.retryBackoffMax
	if ceiling <= 0 {
		ceiling = defaultRetryBackoffMax
	}
	for attempt := 1; attempt < s.retryAttempts; attempt++ {
		if delay >= ceiling {
			break
		}
		delay *= 2
	}
	if delay > ceiling {
		delay = ceiling
	}
	return delay
}

// clearRetry forgets the retry state after a batch landed.
func (s *Service) clearRetry() {
	s.retry = nil
	s.retryAttempts = 0
	s.retryAt = time.Time{}
}

// resetTimer re-arms timer for d, discarding a pending fire.
func resetTimer(timer *time.Timer, d time.Duration) {
	stopAndDrainTimer(timer)
	timer.Reset(d)
}

// stopAndDrainTimer stops timer and throws away a value that already fired, so
// the next Reset starts from a clean channel.
func stopAndDrainTimer(timer *time.Timer) {
	if timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func (s *Service) flushOnBarrier(batch []proxy.RequestLogEntry, firstWaiter chan struct{}) []proxy.RequestLogEntry {
	waiters := []chan struct{}{firstWaiter}
	for {
		select {
		case done := <-s.flushReq:
			waiters = append(waiters, done)
		default:
			goto flushed
		}
	}

flushed:
	// Bound barrier work to current queue depth snapshot so queries cannot be
	// blocked indefinitely by sustained write traffic.
	pending := len(s.queue)
drainLoop:
	for i := 0; i < pending; i++ {
		select {
		case entry := <-s.queue:
			batch = append(batch, entry)
			if len(batch) >= s.batchSize {
				s.flush(batch)
				batch = batch[:0]
			}
		default:
			break drainLoop
		}
	}
	if len(batch) > 0 {
		s.flush(batch)
		batch = batch[:0]
	}
	for _, done := range waiters {
		close(done)
	}
	return batch
}

func (s *Service) drainAndFlush(batch []proxy.RequestLogEntry) {
	for {
		select {
		case entry := <-s.queue:
			batch = append(batch, entry)
			if len(batch) >= s.batchSize {
				s.flush(batch)
				batch = batch[:0]
			}
		default:
			// The last write of the process is also the last chance the entries
			// of an earlier failure get, so it is attempted even when the queue
			// drained into nothing.
			s.flush(batch)
			if len(s.retry) > 0 {
				// Nothing will attempt these again: the loop is returning and
				// Stop is its only caller. A documented loss - logged with its
				// count and added to the drop counter - is what keeps a shutdown
				// during an outage from looking like a clean one.
				s.abandon()
			}
			return
		}
	}
}

// flush writes one batch, and keeps it for another attempt if the repository
// refused it.
//
// A failed InsertBatch used to lose the batch outright: the records of that
// window are both audit evidence and the basis of traffic reconciliation, and
// "database is locked" or a rotation in progress is a reason to try again, not a
// reason to forget them. internal/usage/flush.go requeues the amounts of a
// failed write for the same reason; this is the request-log counterpart of it.
//
// Whatever an earlier failure left behind is merged in first, so a recovering
// database receives the backlog and the new batch in one ordered transaction.
func (s *Service) flush(entries []proxy.RequestLogEntry) {
	if s.insert == nil {
		return
	}
	pending := entries
	if len(s.retry) > 0 {
		// The requeued entries are older, so they go first: the request log is
		// read as a sequence, and a retry that restored the newest records ahead
		// of the ones they followed would be visible as scrambled time.
		pending = append(s.retry, entries...)
		s.retry = nil
	}
	if len(pending) == 0 {
		return
	}
	n, err := s.insert.InsertBatch(pending)
	if err == nil {
		if s.retryAttempts > 0 {
			log.Printf("[requestlog] requeued %d entries landed after %d attempt(s)", len(pending), s.retryAttempts+1)
		}
		s.clearRetry()
		if n > 0 {
			log.Printf("[requestlog] flushed %d entries", n)
		}
		return
	}
	s.requeue(pending, err)
}

// requeue holds a failed batch for a later attempt, with the backoff, the
// attempt ceiling and the memory bound of the retry policy.
func (s *Service) requeue(entries []proxy.RequestLogEntry, err error) {
	s.retryAttempts++
	limit := s.maxFlushAttempts
	if limit <= 0 {
		limit = defaultMaxFlushAttempts
	}
	// The ceiling counts attempts, not retries: a batch that has already been
	// offered as many times as the policy allows is given up now, so the
	// repository is never asked a (limit+1)th time for the same records.
	if s.retryAttempts >= limit {
		s.giveUp(entries, err)
		return
	}
	// The buffer is bounded: past the limit the oldest entries go, because the
	// newest are the ones an operator is about to look for, and a queue that
	// grows with an outage would eventually take memory the accounting side
	// needs. What goes is counted, never silently forgotten.
	if bound := s.retryBufferLimit(); bound > 0 && len(entries) > bound {
		overflow := len(entries) - bound
		s.dropped.Add(uint64(overflow))
		log.Printf("[requestlog] retry buffer full: dropping the %d oldest of %d entries (%d dropped in total)",
			overflow, len(entries), s.dropped.Load())
		entries = entries[overflow:]
	}
	s.retry = append(s.retry[:0], entries...)
	backoff := s.retryBackoff()
	s.retryAt = time.Now().Add(backoff)
	log.Printf("[requestlog] flush of %d entries failed (attempt %d/%d), retrying in %v: %v",
		len(entries), s.retryAttempts, limit, backoff, err)
}

// giveUp drops a batch whose attempts ran out. The count is both logged and
// kept, so a metric can carry it out and no reader has to search the log for it.
func (s *Service) giveUp(entries []proxy.RequestLogEntry, err error) {
	s.dropped.Add(uint64(len(entries)))
	log.Printf("[requestlog] giving up on %d entries after %d failed flushes (%d dropped in total): %v",
		len(entries), s.maxFlushAttempts, s.dropped.Load(), err)
	s.clearRetry()
}

// abandon drops what a shutdown could not write.
func (s *Service) abandon() {
	s.dropped.Add(uint64(len(s.retry)))
	log.Printf("[requestlog] shutdown could not write %d entries (%d dropped in total)",
		len(s.retry), s.dropped.Load())
	s.clearRetry()
}

// DroppedEntries reports how many entries the service could not deliver.
//
// It is the counter behind the log lines: an operator exporting metrics needs
// the number without tailing the log, and a test needs it to prove that a
// persistent failure loses a definite amount rather than an unknown one.
func (s *Service) DroppedEntries() uint64 {
	if s == nil {
		return 0
	}
	return s.dropped.Load()
}

// Repo returns the underlying repository for query access.
func (s *Service) Repo() *Repo {
	return s.repo
}
