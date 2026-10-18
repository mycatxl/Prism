package state

import (
	"errors"
	"testing"

	"prism/internal/model"
)

// --- 000027_deposits ---

const depositTestNowNs = int64(1_700_000_000) * 1_000_000_000

const testCursorName = "tron"

func seedDepositUser(t *testing.T, repo *StateRepo, id string) {
	t.Helper()
	if _, err := repo.db.Exec(`
		INSERT INTO users (id, username, password_hash, role, status, created_at_ns, updated_at_ns)
		VALUES (?, ?, 'hash', 'user', 'active', 1, 1)
	`, id, "user-"+id); err != nil {
		t.Fatalf("seed user %s: %v", id, err)
	}
}

func newDepositIntent(id, userID string, amount, createdAtNs int64) model.DepositIntent {
	return model.DepositIntent{
		ID:          id,
		UserID:      userID,
		AmountMicro: amount,
		CreatedAtNs: createdAtNs,
		ExpiresAtNs: createdAtNs + 15*60*1_000_000_000,
		HoldUntilNs: createdAtNs + 30*60*1_000_000_000,
	}
}

func TestDepositIntentAllocationIsUnique(t *testing.T) {
	repo := newTestStateRepo(t)
	seedDepositUser(t, repo, "u-dep-1")
	seedDepositUser(t, repo, "u-dep-2")

	first, err := repo.InsertDepositIntent(
		newDepositIntent("dep-1", "u-dep-1", 0, depositTestNowNs),
		[]int64{10_000_001, 10_000_002},
		depositTestNowNs,
	)
	if err != nil {
		t.Fatalf("first allocation: %v", err)
	}
	if first.AmountMicro != 10_000_001 {
		t.Fatalf("first amount: got %d, want 10000001", first.AmountMicro)
	}

	// The same candidate list must skip the taken amount and land on the next.
	second, err := repo.InsertDepositIntent(
		newDepositIntent("dep-2", "u-dep-2", 0, depositTestNowNs),
		[]int64{10_000_001, 10_000_002},
		depositTestNowNs,
	)
	if err != nil {
		t.Fatalf("second allocation: %v", err)
	}
	if second.AmountMicro != 10_000_002 {
		t.Fatalf("second amount: got %d, want 10000002", second.AmountMicro)
	}

	// Every candidate taken is a conflict, not a duplicate row.
	if _, err := repo.InsertDepositIntent(
		newDepositIntent("dep-3", "u-dep-1", 0, depositTestNowNs),
		[]int64{10_000_001, 10_000_002},
		depositTestNowNs,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("exhausted allocation: got %v, want ErrConflict", err)
	}
}

// An amount comes back as soon as its occupancy ends, and allocation releases
// expired holds before it tries to insert - otherwise an abandoned intent would
// keep its amount forever.
func TestDepositIntentExpiryReturnsAmountToPool(t *testing.T) {
	repo := newTestStateRepo(t)
	seedDepositUser(t, repo, "u-dep-pool")

	intent := newDepositIntent("dep-old", "u-dep-pool", 0, depositTestNowNs)
	if _, err := repo.InsertDepositIntent(intent, []int64{5_000_001}, depositTestNowNs); err != nil {
		t.Fatalf("allocation: %v", err)
	}

	// Still inside the hold: the amount is taken.
	if _, err := repo.InsertDepositIntent(
		newDepositIntent("dep-blocked", "u-dep-pool", 0, depositTestNowNs+60*1_000_000_000),
		[]int64{5_000_001},
		depositTestNowNs+60*1_000_000_000,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("held amount: got %v, want ErrConflict", err)
	}

	// Past hold_until_ns the amount is free again, released by allocation itself.
	laterNs := intent.HoldUntilNs + 1
	reused, err := repo.InsertDepositIntent(
		newDepositIntent("dep-new", "u-dep-pool", 0, laterNs),
		[]int64{5_000_001},
		laterNs,
	)
	if err != nil {
		t.Fatalf("allocation after expiry: %v", err)
	}
	if reused.AmountMicro != 5_000_001 {
		t.Fatalf("reused amount: got %d, want 5000001", reused.AmountMicro)
	}

	old, err := repo.GetDepositIntent("dep-old")
	if err != nil {
		t.Fatalf("read old intent: %v", err)
	}
	if old.Status != model.DepositStatusReleased {
		t.Fatalf("old intent status: got %q, want %q", old.Status, model.DepositStatusReleased)
	}
}

func TestSweepDepositsWalksBothWindows(t *testing.T) {
	repo := newTestStateRepo(t)
	seedDepositUser(t, repo, "u-dep-sweep")

	intent := newDepositIntent("dep-sweep", "u-dep-sweep", 0, depositTestNowNs)
	if _, err := repo.InsertDepositIntent(intent, []int64{7_000_001}, depositTestNowNs); err != nil {
		t.Fatalf("allocation: %v", err)
	}

	// Inside the payment window: still pending.
	if _, err := repo.SweepDeposits(depositTestNowNs + 60*1_000_000_000); err != nil {
		t.Fatalf("sweep inside window: %v", err)
	}
	got, err := repo.GetDepositIntent("dep-sweep")
	if err != nil {
		t.Fatalf("read intent: %v", err)
	}
	if got.Status != model.DepositStatusPending {
		t.Fatalf("inside window: got %q, want pending", got.Status)
	}

	// Past the payment window but inside the hold: unpaid, amount still held.
	if _, err := repo.SweepDeposits(intent.ExpiresAtNs + 1); err != nil {
		t.Fatalf("sweep into hold: %v", err)
	}
	got, err = repo.GetDepositIntent("dep-sweep")
	if err != nil {
		t.Fatalf("read intent: %v", err)
	}
	if got.Status != model.DepositStatusUnpaid {
		t.Fatalf("inside hold: got %q, want unpaid", got.Status)
	}

	// Past the hold: released.
	if _, err := repo.SweepDeposits(intent.HoldUntilNs + 1); err != nil {
		t.Fatalf("sweep past hold: %v", err)
	}
	got, err = repo.GetDepositIntent("dep-sweep")
	if err != nil {
		t.Fatalf("read intent: %v", err)
	}
	if got.Status != model.DepositStatusReleased {
		t.Fatalf("past hold: got %q, want released", got.Status)
	}
}

// A transfer whose block time falls inside an intent's occupancy is credited
// even when the intent has already been marked released: solidification lags, so
// "discovered late" must not mean "lost".
func TestCreditDepositAcceptsLateDiscoveryInsideWindow(t *testing.T) {
	repo := newTestStateRepo(t)
	seedDepositUser(t, repo, "u-dep-credit")

	intent := newDepositIntent("dep-credit", "u-dep-credit", 0, depositTestNowNs)
	if _, err := repo.InsertDepositIntent(intent, []int64{3_000_001}, depositTestNowNs); err != nil {
		t.Fatalf("allocation: %v", err)
	}
	if _, err := repo.SweepDeposits(intent.HoldUntilNs + 1); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	// The transfer happened one second after the intent was created, but it is
	// only seen now, long after the hold expired.
	blockTSNs := intent.CreatedAtNs + 1_000_000_000
	entry := model.BalanceEntry{ID: "bl-credit"}
	credited, err := repo.CreditDeposit("tx-credit", 3_000_001, blockTSNs, depositTestNowNs, testCursorName, blockTSNs/1_000_000, entry)
	if err != nil {
		t.Fatalf("credit: %v", err)
	}
	if credited.ID != "dep-credit" {
		t.Fatalf("credited intent: got %q, want dep-credit", credited.ID)
	}
	if credited.Status != model.DepositStatusPaid {
		t.Fatalf("status after credit: got %q, want paid", credited.Status)
	}

	balance, err := repo.GetBalance("u-dep-credit")
	if err != nil {
		t.Fatalf("read balance: %v", err)
	}
	if balance != 3_000_001 {
		t.Fatalf("balance: got %d, want 3000001", balance)
	}

	watermark, err := repo.ChainCursor(testCursorName)
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if watermark != blockTSNs/1_000_000 {
		t.Fatalf("cursor: got %d, want %d", watermark, blockTSNs/1_000_000)
	}
}

// A transfer older than the intent that holds the amount is refused. This is the
// rule that lets an amount be reused.
func TestCreditDepositRejectsTransferOlderThanIntent(t *testing.T) {
	repo := newTestStateRepo(t)
	seedDepositUser(t, repo, "u-dep-gate")

	intent := newDepositIntent("dep-gate", "u-dep-gate", 0, depositTestNowNs)
	if _, err := repo.InsertDepositIntent(intent, []int64{4_000_001}, depositTestNowNs); err != nil {
		t.Fatalf("allocation: %v", err)
	}

	// One second before the intent existed: no window contains it.
	blockTSNs := intent.CreatedAtNs - 1_000_000_000
	_, err := repo.CreditDeposit("tx-old", 4_000_001, blockTSNs, depositTestNowNs, testCursorName, blockTSNs/1_000_000, model.BalanceEntry{ID: "bl-old"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("transfer before creation: got %v, want ErrNotFound", err)
	}
	balance, err := repo.GetBalance("u-dep-gate")
	if err != nil {
		t.Fatalf("read balance: %v", err)
	}
	if balance != 0 {
		t.Fatalf("balance after refused transfer: got %d, want 0", balance)
	}
}

// The transaction id is the idempotency key: crediting the same transfer twice
// is a conflict, and the balance moves once.
func TestCreditDepositIsIdempotentByTxID(t *testing.T) {
	repo := newTestStateRepo(t)
	seedDepositUser(t, repo, "u-dep-twice")

	intent := newDepositIntent("dep-twice", "u-dep-twice", 0, depositTestNowNs)
	if _, err := repo.InsertDepositIntent(intent, []int64{6_000_001}, depositTestNowNs); err != nil {
		t.Fatalf("allocation: %v", err)
	}
	blockTSNs := intent.CreatedAtNs + 1_000_000_000
	if _, err := repo.CreditDeposit("tx-twice", 6_000_001, blockTSNs, depositTestNowNs, testCursorName, blockTSNs/1_000_000, model.BalanceEntry{ID: "bl-twice"}); err != nil {
		t.Fatalf("first credit: %v", err)
	}
	if _, err := repo.CreditDeposit("tx-twice", 6_000_001, blockTSNs, depositTestNowNs, testCursorName, blockTSNs/1_000_000, model.BalanceEntry{ID: "bl-twice-2"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("replayed credit: got %v, want ErrConflict", err)
	}
	balance, err := repo.GetBalance("u-dep-twice")
	if err != nil {
		t.Fatalf("read balance: %v", err)
	}
	if balance != 6_000_001 {
		t.Fatalf("balance after replay: got %d, want 6000001", balance)
	}
}

// A paid intent no longer holds its amount.
func TestPaidDepositReleasesItsAmount(t *testing.T) {
	repo := newTestStateRepo(t)
	seedDepositUser(t, repo, "u-dep-paid")
	seedDepositUser(t, repo, "u-dep-next")

	intent := newDepositIntent("dep-paid", "u-dep-paid", 0, depositTestNowNs)
	if _, err := repo.InsertDepositIntent(intent, []int64{8_000_001}, depositTestNowNs); err != nil {
		t.Fatalf("allocation: %v", err)
	}
	blockTSNs := intent.CreatedAtNs + 1_000_000_000
	if _, err := repo.CreditDeposit("tx-paid", 8_000_001, blockTSNs, depositTestNowNs, testCursorName, blockTSNs/1_000_000, model.BalanceEntry{ID: "bl-paid"}); err != nil {
		t.Fatalf("credit: %v", err)
	}

	// The amount is immediately available to the next intent.
	next, err := repo.InsertDepositIntent(
		newDepositIntent("dep-next", "u-dep-next", 0, depositTestNowNs+2_000_000_000),
		[]int64{8_000_001},
		depositTestNowNs+2_000_000_000,
	)
	if err != nil {
		t.Fatalf("allocation after payment: %v", err)
	}
	if next.AmountMicro != 8_000_001 {
		t.Fatalf("reused amount: got %d, want 8000001", next.AmountMicro)
	}
}

// TestAdvanceChainCursorNeverMovesBackwards pins that the watermark is
// monotonic. TronGrid answers newest-first by default, so a page handled in
// descending order used to leave the cursor on its OLDEST row; once a backlog
// exceeded one page that pushed every older transfer out of reach for good.
func TestAdvanceChainCursorNeverMovesBackwards(t *testing.T) {
	repo := newTestStateRepo(t)

	if err := repo.AdvanceChainCursor(testCursorName, 5_000, depositTestNowNs); err != nil {
		t.Fatalf("advance to 5000: %v", err)
	}
	if err := repo.AdvanceChainCursor(testCursorName, 3_000, depositTestNowNs); err != nil {
		t.Fatalf("advance backwards: %v", err)
	}
	watermark, err := repo.ChainCursor(testCursorName)
	if err != nil {
		t.Fatalf("ChainCursor: %v", err)
	}
	if watermark != 5_000 {
		t.Fatalf("watermark: got %d, want 5000 (the cursor must never move back)", watermark)
	}

	// A forward move still works.
	if err := repo.AdvanceChainCursor(testCursorName, 7_000, depositTestNowNs); err != nil {
		t.Fatalf("advance to 7000: %v", err)
	}
	if watermark, err = repo.ChainCursor(testCursorName); err != nil {
		t.Fatalf("ChainCursor: %v", err)
	}
	if watermark != 7_000 {
		t.Fatalf("watermark: got %d, want 7000", watermark)
	}
}
