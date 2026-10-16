package usage

import (
	"bytes"
	"errors"
	"log"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/model"
)

// fakeStore records the calls a pass makes and folds hourly rows into daily
// rows in memory, mirroring the store's one-transaction summary: the fold and
// the delete never happen apart, so a pass cannot be observed halfway through
// them the way the old three-call sequence could.
type fakeStore struct {
	mu sync.Mutex

	hourlyRows []model.UsageRow
	dailyRows  []model.UsageRow

	summarizeErr error
	delDayErr    error

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

func (f *fakeStore) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if call == name {
			n++
		}
	}
	return n
}

// dayBucket is the UTC day a timestamp belongs to: the same
// (ts / 86400000000000) * 86400000000000 bucketing the SQL uses.
func dayBucket(tsNs int64) int64 {
	const dayNs = int64(24 * time.Hour)
	return (tsNs / dayNs) * dayNs
}

// upsertDaily mirrors the ON CONFLICT DO UPDATE arm of the real statement: a
// row adds into the bucket already in the table instead of replacing it.
func (f *fakeStore) upsertDaily(row model.UsageRow) {
	for i := range f.dailyRows {
		daily := &f.dailyRows[i]
		if daily.KeyID == row.KeyID && daily.PlatformID == row.PlatformID && daily.TSNs == row.TSNs {
			daily.IngressBytes += row.IngressBytes
			daily.EgressBytes += row.EgressBytes
			daily.Requests += row.Requests
			daily.Errors += row.Errors
			return
		}
	}
	f.dailyRows = append(f.dailyRows, row)
}

// SummarizeUsageHourlyBefore folds every hourly row older than cutoffNs into
// the daily table and removes the rows it folded, returning how many went. The
// real store runs both statements in one transaction; the fake keeps that
// promise by doing both in one call and never leaving one behind the other.
func (f *fakeStore) SummarizeUsageHourlyBefore(cutoffNs int64) (int64, error) {
	f.record("summarizeHourly")
	if f.summarizeErr != nil {
		return 0, f.summarizeErr
	}

	var kept []model.UsageRow
	var removed int64
	for _, row := range f.hourlyRows {
		if row.TSNs >= cutoffNs {
			kept = append(kept, row)
			continue
		}
		removed++
		f.upsertDaily(model.UsageRow{
			KeyID:        row.KeyID,
			UserID:       row.UserID,
			PlatformID:   row.PlatformID,
			TSNs:         dayBucket(row.TSNs),
			IngressBytes: row.IngressBytes,
			EgressBytes:  row.EgressBytes,
			Requests:     row.Requests,
			Errors:       row.Errors,
		})
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

// The interface itself is the guarantee: a pass can only fold the hourly rows
// if the store hands it that as one call. Any of the three old methods coming
// back would make the double-count window expressible again.
func TestRetention_StoreFoldsInOneTransactionalCall(t *testing.T) {
	typ := reflect.TypeOf((*Store)(nil)).Elem()

	if _, ok := typ.MethodByName("SummarizeUsageHourlyBefore"); !ok {
		t.Error("Store must expose SummarizeUsageHourlyBefore: it is the only call that folds and deletes in one transaction")
	}
	for _, old := range []string{"SumUsageHourlyBefore", "UpsertUsageDaily", "DeleteUsageHourlyBefore"} {
		if _, ok := typ.MethodByName(old); ok {
			t.Errorf("Store must not expose %s: calling it separately from the others re-opens the window where a crash leaves the traffic in the daily table and the hourly rows in place", old)
		}
	}
}

// A pass makes exactly one summary call, whatever the hourly table holds, and
// the daily prune after it.
func TestRetention_SummarizesInOneCallPerPass(t *testing.T) {
	store := &fakeStore{}
	retention := &Retention{Store: store}
	if _, err := retention.RunOnce(time.Now().UnixNano()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	want := []string{"summarizeHourly", "deleteDaily"}
	got := store.callOrder()
	if len(got) != len(want) {
		t.Fatalf("a pass must be one summary call plus the daily prune: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call order: got %v, want %v", got, want)
		}
	}
	if n := store.count("summarizeHourly"); n != 1 {
		t.Errorf("summarize calls: got %d, want 1", n)
	}
}

// A failed summary must leave the pass right there: nothing is deleted, so the
// hourly rows are still there for the next pass.
func TestRetention_SummaryFailureStopsBeforeThePrune(t *testing.T) {
	store := &fakeStore{
		hourlyRows:   []model.UsageRow{{KeyID: "k-1", TSNs: 0}},
		summarizeErr: errors.New("boom"),
	}
	retention := &Retention{Store: store}

	if _, err := retention.RunOnce(time.Now().UnixNano()); err == nil {
		t.Fatal("RunOnce must report the summary failure")
	}
	if calls := store.callOrder(); len(calls) != 1 || calls[0] != "summarizeHourly" {
		t.Fatalf("a failed summary must not delete anything: %v", calls)
	}
	if len(store.hourlyRows) != 1 {
		t.Errorf("the hourly row must survive a failed summary: %+v", store.hourlyRows)
	}
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

// Both result fields come from the one summary call: every row counted as
// summarized is also a row that was deleted, and the fold adds into the day
// bucket that is already there instead of replacing it.
func TestRetention_FoldsIntoAnExistingDailyBucket(t *testing.T) {
	const hour = int64(time.Hour)
	const day = int64(24 * hour)
	now := int64(100 * day)

	aged := now - int64(40*day) // well past the hourly window, same UTC day as aged+hour
	store := &fakeStore{
		hourlyRows: []model.UsageRow{
			{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: aged, IngressBytes: 10, Requests: 1},
			{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: aged + hour, IngressBytes: 5, Requests: 2},
		},
		dailyRows: []model.UsageRow{
			// Already summarized earlier in the same day.
			{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: dayBucket(aged), IngressBytes: 7, Requests: 3},
		},
	}

	result, err := (&Retention{Store: store}).RunOnce(now)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Summarized != 2 || result.HourlyRemoved != 2 {
		t.Errorf("both aged rows are folded and deleted by the one call: summarized %d, removed %d", result.Summarized, result.HourlyRemoved)
	}
	if len(store.hourlyRows) != 0 {
		t.Errorf("the aged hourly rows must be gone: %+v", store.hourlyRows)
	}
	if len(store.dailyRows) != 1 {
		t.Fatalf("the folded rows must share the one day bucket: %+v", store.dailyRows)
	}
	if got := store.dailyRows[0]; got.IngressBytes != 22 || got.Requests != 6 {
		t.Errorf("the daily bucket must accumulate (7+10+5 bytes, 3+1+2 requests): %+v", got)
	}
}

// The invariant the transactional call buys: a second pass over the same window
// folds nothing, because the rows whose traffic is already in the daily table
// are gone in the same step that wrote it.
func TestRetention_SecondPassDoesNotDoubleCount(t *testing.T) {
	const hour = int64(time.Hour)
	now := int64(100 * 24 * hour)

	store := &fakeStore{
		hourlyRows: []model.UsageRow{
			{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: now - int64(40*24*hour), IngressBytes: 10, Requests: 1},
		},
	}

	retention := &Retention{Store: store}
	if _, err := retention.RunOnce(now); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	result, err := retention.RunOnce(now)
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if result.Summarized != 0 || result.HourlyRemoved != 0 {
		t.Errorf("the second pass has nothing left to fold: summarized %d, removed %d", result.Summarized, result.HourlyRemoved)
	}
	if len(store.dailyRows) != 1 || store.dailyRows[0].IngressBytes != 10 {
		t.Errorf("the day must not be counted twice: %+v", store.dailyRows)
	}
}

// A pass with nothing to fold leaves the daily table alone, but rows can still
// age past the daily window without any hourly work.
func TestRetention_NothingToFoldStillPrunesDailyRows(t *testing.T) {
	const hour = int64(time.Hour)
	now := int64(100 * 24 * hour)

	store := &fakeStore{
		dailyRows: []model.UsageRow{
			{KeyID: "k-1", UserID: "u-1", PlatformID: "p-1", TSNs: now - int64(500*24*hour), IngressBytes: 1},
		},
	}

	result, err := (&Retention{Store: store}).RunOnce(now)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Summarized != 0 || result.HourlyRemoved != 0 {
		t.Errorf("nothing to fold: summarized %d, removed %d", result.Summarized, result.HourlyRemoved)
	}
	if result.DailyRemoved != 1 {
		t.Errorf("daily removed: got %d, want 1", result.DailyRemoved)
	}
	if len(store.dailyRows) != 0 {
		t.Errorf("the expired daily row must be gone: %+v", store.dailyRows)
	}
}

// A failed daily prune is reported, after the summary has already committed.
func TestRetention_DailyPruneFailureIsReported(t *testing.T) {
	store := &fakeStore{delDayErr: errors.New("locked")}
	retention := &Retention{Store: store}

	if _, err := retention.RunOnce(time.Now().UnixNano()); err == nil {
		t.Fatal("RunOnce must report the daily prune failure")
	}
	if n := store.count("summarizeHourly"); n != 1 {
		t.Errorf("the summary runs before the prune: got %d calls", n)
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
