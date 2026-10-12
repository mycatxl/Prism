package usage

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/model"
)

// fakeStore records the calls a pass makes so the ordering guarantees can be
// asserted without a database.
type fakeStore struct {
	mu sync.Mutex

	hourlyRows []model.UsageRow
	dailyRows  []model.UsageRow

	sumErr     error
	upsertErr  error
	delHourErr error
	delDayErr  error

	calls []string
}

func (f *fakeStore) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
}

func (f *fakeStore) callOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeStore) SumUsageHourlyBefore(cutoffNs int64) ([]model.UsageRow, error) {
	f.record("sum")
	if f.sumErr != nil {
		return nil, f.sumErr
	}
	var out []model.UsageRow
	for _, row := range f.hourlyRows {
		if row.TSNs < cutoffNs {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeStore) UpsertUsageDaily(rows []model.UsageRow) error {
	f.record("upsertDaily")
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.dailyRows = append(f.dailyRows, rows...)
	return nil
}

func (f *fakeStore) DeleteUsageHourlyBefore(cutoffNs int64) (int64, error) {
	f.record("deleteHourly")
	if f.delHourErr != nil {
		return 0, f.delHourErr
	}
	var kept []model.UsageRow
	var removed int64
	for _, row := range f.hourlyRows {
		if row.TSNs < cutoffNs {
			removed++
			continue
		}
		kept = append(kept, row)
	}
	f.hourlyRows = kept
	return removed, nil
}

func (f *fakeStore) DeleteUsageDailyBefore(cutoffNs int64) (int64, error) {
	f.record("deleteDaily")
	if f.delDayErr != nil {
		return 0, f.delDayErr
	}
	var kept []model.UsageRow
	var removed int64
	for _, row := range f.dailyRows {
		if row.TSNs < cutoffNs {
			removed++
			continue
		}
		kept = append(kept, row)
	}
	f.dailyRows = kept
	return removed, nil
}

func TestRetention_RunOnceSummarizesAndPrunes(t *testing.T) {
	const hour = int64(time.Hour)
	now := int64(100 * 24 * hour) // day 100

	store := &fakeStore{
		hourlyRows: []model.UsageRow{
			// Well past the hourly window: summarized and deleted.
			{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: now - int64(40*24*hour), IngressBytes: 10, Requests: 1},
			// Inside the hourly window: untouched.
			{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: now - int64(time.Hour), IngressBytes: 5, Requests: 1},
		},
		dailyRows: []model.UsageRow{
			// Past the daily window: pruned.
			{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: now - int64(500*24*hour), IngressBytes: 1},
		},
	}

	retention := &Retention{Store: store}
	result, err := retention.RunOnce(now)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if result.Summarized != 1 {
		t.Errorf("summarized: got %d, want 1", result.Summarized)
	}
	if result.HourlyRemoved != 1 {
		t.Errorf("hourly removed: got %d, want 1", result.HourlyRemoved)
	}
	if result.DailyRemoved != 1 {
		t.Errorf("daily removed: got %d, want 1", result.DailyRemoved)
	}

	// The fresh hourly row survives.
	if len(store.hourlyRows) != 1 || store.hourlyRows[0].IngressBytes != 5 {
		t.Errorf("the fresh hourly row must survive: %+v", store.hourlyRows)
	}
	// The summarized row landed in the daily table.
	if len(store.dailyRows) != 1 || store.dailyRows[0].IngressBytes != 10 {
		t.Errorf("the summarized row must be in the daily table: %+v", store.dailyRows)
	}
}

// The daily write must happen before the hourly delete, or a failure in between
// would lose the traffic.
func TestRetention_OrderIsAggregateThenDelete(t *testing.T) {
	store := &fakeStore{}
	retention := &Retention{Store: store}
	if _, err := retention.RunOnce(time.Now().UnixNano()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	want := []string{"sum", "deleteHourly", "deleteDaily"}
	got := store.callOrder()
	if len(got) != len(want) {
		t.Fatalf("call order: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call order: got %v, want %v", got, want)
		}
	}
}

// When there is nothing to aggregate the daily write is skipped, but the deletes
// still run: rows can age past the daily window without any hourly work.
func TestRetention_EmptyAggregateSkipsDailyWrite(t *testing.T) {
	store := &fakeStore{}
	retention := &Retention{Store: store}
	if _, err := retention.RunOnce(time.Now().UnixNano()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	for _, call := range store.callOrder() {
		if call == "upsertDaily" {
			t.Fatalf("an empty aggregate must not write to the daily table: %v", store.callOrder())
		}
	}
}

// A failed summary must not delete anything: the rows have to stay for the next
// pass.
func TestRetention_SummaryFailureKeepsRows(t *testing.T) {
	store := &fakeStore{
		hourlyRows: []model.UsageRow{{KeyID: "k-1", TSNs: 0}},
		sumErr:     errors.New("boom"),
	}
	retention := &Retention{Store: store}

	if _, err := retention.RunOnce(time.Now().UnixNano()); err == nil {
		t.Fatal("RunOnce must report the summary failure")
	}
	for _, call := range store.callOrder() {
		if call == "deleteHourly" || call == "deleteDaily" {
			t.Fatalf("a failed summary must not delete anything: %v", store.callOrder())
		}
	}
	if len(store.hourlyRows) != 1 {
		t.Errorf("the hourly row must survive a failed summary: %+v", store.hourlyRows)
	}
}

// A failed daily write must not delete the hourly rows either.
func TestRetention_DailyWriteFailureKeepsHourlyRows(t *testing.T) {
	store := &fakeStore{
		hourlyRows: []model.UsageRow{{KeyID: "k-1", TSNs: 0}},
		upsertErr:  errors.New("disk full"),
	}
	retention := &Retention{Store: store}

	if _, err := retention.RunOnce(time.Now().UnixNano()); err == nil {
		t.Fatal("RunOnce must report the daily write failure")
	}
	for _, call := range store.callOrder() {
		if call == "deleteHourly" {
			t.Fatalf("a failed daily write must not delete the hourly rows: %v", store.callOrder())
		}
	}
	if len(store.hourlyRows) != 1 {
		t.Errorf("the hourly row must survive a failed daily write: %+v", store.hourlyRows)
	}
}

func TestRetention_WindowsAreConfigurable(t *testing.T) {
	const hour = int64(time.Hour)
	now := int64(100 * 24 * hour)

	store := &fakeStore{
		hourlyRows: []model.UsageRow{
			{KeyID: "k-1", TSNs: now - int64(2*hour)},
		},
	}
	// A two-hour hourly window and a one-day daily window.
	retention := &Retention{
		Store:      store,
		HourlyKeep: 1 * time.Hour,
		DailyKeep:  24 * time.Hour,
	}

	result, err := retention.RunOnce(now)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.HourlyRemoved != 1 {
		t.Errorf("with a one-hour window the two-hour-old row must be summarized, removed %d", result.HourlyRemoved)
	}
}

func TestRetention_ZeroWindowsUseDefaults(t *testing.T) {
	retention := &Retention{}
	if got := retention.hourlyKeep(); got != DefaultHourlyKeep {
		t.Errorf("hourly keep: got %v, want %v", got, DefaultHourlyKeep)
	}
	if got := retention.dailyKeep(); got != DefaultDailyKeep {
		t.Errorf("daily keep: got %v, want %v", got, DefaultDailyKeep)
	}
	if got := retention.interval(); got != DefaultInterval {
		t.Errorf("interval: got %v, want %v", got, DefaultInterval)
	}

	// An explicit value wins.
	custom := &Retention{
		HourlyKeep: 2 * time.Hour,
		DailyKeep:  3 * time.Hour,
		Interval:   4 * time.Hour,
	}
	if got := custom.hourlyKeep(); got != 2*time.Hour {
		t.Errorf("hourly keep: got %v, want 2h", got)
	}
	if got := custom.dailyKeep(); got != 3*time.Hour {
		t.Errorf("daily keep: got %v, want 3h", got)
	}
	if got := custom.interval(); got != 4*time.Hour {
		t.Errorf("interval: got %v, want 4h", got)
	}
}

// A nil store must not panic; it makes the pass a no-op.
func TestRetention_NilStoreIsANoOp(t *testing.T) {
	retention := &Retention{}
	if _, err := retention.RunOnce(time.Now().UnixNano()); err != nil {
		t.Fatalf("RunOnce with a nil store: %v", err)
	}
	stop := retention.Start()
	stop()
}

// A non-positive now falls back to the wall clock instead of producing
// nonsensical cutoffs.
func TestRetention_ZeroNowUsesWallClock(t *testing.T) {
	store := &fakeStore{}
	retention := &Retention{Store: store}
	if _, err := retention.RunOnce(0); err != nil {
		t.Fatalf("RunOnce(0): %v", err)
	}
	if len(store.callOrder()) == 0 {
		t.Error("a pass must still run when now is zero")
	}
}

func TestRetention_StartStopsCleanly(t *testing.T) {
	store := &fakeStore{}
	retention := &Retention{Store: store, Interval: time.Millisecond}

	stop := retention.Start()
	// Let a couple of ticks through.
	time.Sleep(20 * time.Millisecond)
	stop()
	// Stopping twice must not panic.
	stop()

	if len(store.callOrder()) == 0 {
		t.Error("the ticker must have run at least one pass")
	}
}

func TestRetention_DefaultLogfWritesToStdLog(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	DefaultLogf("retention %s %d", "probe", 7)

	if got := buf.String(); !strings.Contains(got, "retention probe 7") {
		t.Errorf("DefaultLogf output: got %q, want it to contain %q", got, "retention probe 7")
	}
}

func TestRetention_LogsOnlyWhenSomethingChanged(t *testing.T) {
	var mu sync.Mutex
	var messages []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		messages = append(messages, format)
	}

	// Nothing to do: no log line.
	quiet := &Retention{Store: &fakeStore{}, Logf: logf, Interval: time.Millisecond}
	stop := quiet.Start()
	time.Sleep(10 * time.Millisecond)
	stop()

	mu.Lock()
	quietMessages := len(messages)
	mu.Unlock()
	if quietMessages != 0 {
		t.Errorf("an idle pass must not log, got %d messages", quietMessages)
	}

	// Work to do: one log line.
	busy := &Retention{
		Store: &fakeStore{
			hourlyRows: []model.UsageRow{{KeyID: "k-1", TSNs: 0}},
		},
		Logf:     logf,
		Interval: time.Millisecond,
	}
	stop = busy.Start()
	time.Sleep(10 * time.Millisecond)
	stop()

	mu.Lock()
	busyMessages := len(messages)
	mu.Unlock()
	if busyMessages == 0 {
		t.Error("a pass that summarized rows must log")
	}
}
