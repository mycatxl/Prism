package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"prism/internal/config"
	"prism/internal/tron"
	"sync"
	"time"

	"prism/internal/model"
	"prism/internal/safego"
	"prism/internal/state"
)

// On-chain deposits.
//
// One fixed receiving address and one unique amount per intent. A bare TRC20
// transfer carries no field for an identifier and the server holds no keys, so
// the amount is the only channel left: the intent's amount is the payer's
// identity for the duration of its occupancy.
//
// Three rules keep that from crediting the wrong account.
//
// The amount is unique among intents that are still holding it, and the partial
// unique index - not a counter - is what allocates it: the candidate is inserted
// and a conflict means taken.
//
// The window is measured on the chain's clock. A transfer is credited when its
// block timestamp falls in [created_at, hold_until) of the intent holding that
// amount, whatever the intent's status is by then, because solidification lags
// and a payment made at the last second is discovered minutes later.
//
// A transfer older than the intent it would match is refused. That is the rule
// that makes reuse safe: a straggler for an expired intent predates the intent
// that holds the amount now, so the time gate rejects it instead of crediting
// somebody else.
//
// What remains, stated plainly: a payer who sends the exact old amount after
// their intent expired and after the amount was taken by a new intent will be
// credited to the new intent's owner. It needs an expired intent, a reused
// amount and a payer who transfers anyway, so its likelihood is bounded by the
// number of intents open at once rather than growing over time. The intent keeps
// the transaction id and the payer's address, and the operator can correct it
// with an admin adjustment that leaves an audit trail.

const (
	// depositWindow is how long the page says "pay this amount now".
	depositWindowNs = int64(15) * 60 * 1_000_000_000
	// depositHoldNs is the silent tail after the window: the amount stays held
	// so a transfer made at the last second is still credited once
	// solidification catches up.
	depositHoldNs = int64(15) * 60 * 1_000_000_000
	// depositMinMicro is the smallest credit. Below one USDT the noise of
	// dust transfers outweighs any deposit.
	depositMinMicro = int64(1_000_000)
	// depositMaxMicro bounds a single intent so a typo cannot ask for a
	// million USDT worth of allocation.
	depositMaxMicro = int64(100_000) * 1_000_000
	// depositAmountAttempts bounds the search for a free amount. 100 values is
	// far more than the number of intents open at any moment.
	depositAmountAttempts = 100
	// depositCursorName is the row in chain_cursor this listener owns.
	depositCursorName = "tron"
	// depositPollInterval is how often the chain is read.
	depositPollInterval = 30 * time.Second
	// depositScanTimeout gives SQLite preparation and provider requests their
	// own budget even when a caller uses a shorter polling cadence.
	depositScanTimeout = 30 * time.Second
)

// DepositIntentView is what the deposit page shows.
type DepositIntentView struct {
	Currency    string  `json:"currency"`
	CreditMicro int64   `json:"credit_micro"`
	Rate        float64 `json:"rate"`
	Contract    string  `json:"contract"`
	ID          string  `json:"id"`
	AmountMicro int64   `json:"amount_micro"`
	Status      string  `json:"status"`
	Address     string  `json:"address"`
	TxID        string  `json:"tx_id"`
	CreatedAtNs int64   `json:"created_at_ns"`
	ExpiresAtNs int64   `json:"expires_at_ns"`
	HoldUntilNs int64   `json:"hold_until_ns"`
	PaidAtNs    int64   `json:"paid_at_ns"`
}

// DepositInfo describes whether deposits are available and on what terms.
type DepositInfo struct {
	TRXEnabled bool    `json:"trx_enabled"`
	TRXRatio   float64 `json:"trx_ratio"`
	Enabled    bool    `json:"enabled"`
	Address    string  `json:"address"`
	Contract   string  `json:"contract"`
	MinMicro   int64   `json:"min_micro"`
	MaxMicro   int64   `json:"max_micro"`
	WindowSecs int64   `json:"window_secs"`
}

func (s *ControlPlaneService) SelfDepositInfo() DepositInfo {
	c := s.paymentConfig()
	return DepositInfo{Enabled: c.TronDepositEnabled && c.TronAddress != "", Address: c.TronAddress, Contract: c.TronUSDTContract,
		MinMicro: depositMinMicro, MaxMicro: depositMaxMicro, WindowSecs: int64(c.TronWindowMinutes) * 60, TRXEnabled: c.TronTRXEnabled, TRXRatio: c.TronTRXRatio}
}

// SelfDeposit opens an intent for wantMicro and returns the amount to pay.
//
// The amount returned is wantMicro plus the smallest number of micro-units that
// nobody else is holding. Whole amounts are never handed out: a round number is
// the one most likely to collide with an unrelated transfer, which is exactly
// what the unique amount exists to avoid.
func (s *ControlPlaneService) SelfDeposit(userID string, wantMicro int64, nowNs int64, currencies ...string) (DepositIntentView, error) {
	c := s.paymentConfig()
	currency := "usdt"
	if len(currencies) > 0 && currencies[0] != "" {
		currency = currencies[0]
	}
	if currency != "usdt" && currency != "trx" {
		return DepositIntentView{}, invalidArg("currency: must be usdt or trx")
	}
	if currency == "trx" && !c.TronTRXEnabled {
		return DepositIntentView{}, invalidArg("deposit: TRX deposits are not enabled")
	}
	if !c.TronDepositEnabled || c.TronAddress == "" {
		return DepositIntentView{}, invalidArg("deposit: on-chain deposits are not configured")
	}
	if wantMicro < depositMinMicro {
		return DepositIntentView{}, invalidArg("amount_micro: the smallest deposit is 1 currency unit")
	}
	if wantMicro > depositMaxMicro {
		return DepositIntentView{}, invalidArg("amount_micro: the largest deposit is 100000 currency units")
	}
	user, err := s.Engine.GetUser(userID)
	if err != nil {
		return DepositIntentView{}, err
	}
	if user.Status != model.UserStatusActive {
		return DepositIntentView{}, forbidden("deposit: the account is not active")
	}

	if err := s.Engine.BindLegacyDepositTargets(c.TronAddress, c.TronUSDTContract); err != nil {
		return DepositIntentView{}, err
	}
	rate := 1.0
	contract := c.TronUSDTContract
	if currency == "trx" {
		rate = c.TronTRXRatio
		contract = ""
	}
	if _, err := model.ConvertPaymentMicro(wantMicro+depositAmountAttempts, rate); err != nil {
		return DepositIntentView{}, invalidArg(err.Error())
	}
	windowNs := int64(c.TronWindowMinutes) * int64(time.Minute)
	if nowNs < 0 || nowNs > math.MaxInt64-windowNs-depositHoldNs {
		return DepositIntentView{}, invalidArg("deposit: invalid order timestamp")
	}
	candidates := depositCandidates(wantMicro)
	if len(candidates) == 0 {
		return DepositIntentView{}, conflict("deposit: no amount is free right now, try again in a minute")
	}
	intentID, err := newID("dep_")
	if err != nil {
		return DepositIntentView{}, err
	}
	intent := model.DepositIntent{
		Currency: currency, Address: c.TronAddress, Contract: contract, Rate: rate,
		ID:          intentID,
		UserID:      userID,
		CreatedAtNs: nowNs,
		ExpiresAtNs: nowNs + windowNs,
		HoldUntilNs: nowNs + windowNs + depositHoldNs,
	}
	stored, err := s.Engine.InsertDepositIntent(intent, candidates, nowNs)
	if err != nil {
		if errors.Is(err, state.ErrConflict) {
			return DepositIntentView{}, conflict("deposit: no amount is free right now, try again in a minute")
		}
		return DepositIntentView{}, err
	}
	return s.depositView(stored), nil
}

// SelfDepositIntent returns one of the caller's own intents.
func (s *ControlPlaneService) SelfDepositIntent(userID, id string) (DepositIntentView, error) {
	intent, err := s.Engine.GetDepositIntent(id)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return DepositIntentView{}, notFound("deposit: no such intent")
		}
		return DepositIntentView{}, err
	}
	if intent.UserID != userID {
		// Another user's intent must look exactly like one that does not exist.
		return DepositIntentView{}, notFound("deposit: no such intent")
	}
	return s.depositView(intent), nil
}

// SelfDeposits lists the caller's intents, newest first.
func (s *ControlPlaneService) SelfDeposits(userID string, limit int) ([]DepositIntentView, error) {
	intents, err := s.Engine.ListDepositIntents(userID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]DepositIntentView, 0, len(intents))
	for _, intent := range intents {
		out = append(out, s.depositView(intent))
	}
	return out, nil
}

func (s *ControlPlaneService) depositView(intent model.DepositIntent) DepositIntentView {
	address := intent.Address
	if address == "" {
		address = s.paymentConfig().TronAddress
	}
	return DepositIntentView{
		Currency: intent.Currency, CreditMicro: intent.CreditMicro, Rate: intent.Rate, Contract: intent.Contract,
		ID:          intent.ID,
		AmountMicro: intent.AmountMicro,
		Status:      intent.Status,
		Address:     address,
		TxID:        intent.TxID,
		CreatedAtNs: intent.CreatedAtNs,
		ExpiresAtNs: intent.ExpiresAtNs,
		HoldUntilNs: intent.HoldUntilNs,
		PaidAtNs:    intent.PaidAtNs,
	}
}

// depositCandidates lists the amounts to try, in order.
//
// It starts one micro-unit above what the user asked for and walks up. A whole
// amount - a zero fractional part - is skipped everywhere, not just at the
// start: those are the amounts an unrelated transfer is most likely to match.
func depositCandidates(wantMicro int64) []int64 {
	out := make([]int64, 0, depositAmountAttempts)
	for offset := int64(1); offset <= depositAmountAttempts; offset++ {
		amount := wantMicro + offset
		if amount%1_000_000 == 0 {
			continue
		}
		out = append(out, amount)
	}
	return out
}

// SweepDeposits ages intents through their two windows.
//
// It runs on its own schedule and again inside allocation, so amounts that were
// abandoned come back even when nobody is opening new intents.
func (s *ControlPlaneService) SweepDeposits(nowNs int64) (int, error) {
	return s.Engine.SweepDeposits(nowNs)
}

// ScanDeposits reads confirmed transfers and credits the ones that match an
// intent.
//
// The chain is queried outside any transaction: the state layer serialises every
// write on one connection, so network IO inside a transaction would block the
// whole system. A matched transfer is written in a single transaction that also
// advances the cursor, and the transaction id makes a replay harmless.
func (s *ControlPlaneService) ScanDeposits(ctx context.Context, nowNs int64) (int, error) {
	s.depositScanMu.Lock()
	defer s.depositScanMu.Unlock()
	c := s.paymentConfig()
	if s.Tron == nil && s.RuntimeCfg == nil {
		return 0, nil
	}
	if c.TronAddress != "" {
		if err := s.Engine.BindLegacyDepositTargets(c.TronAddress, c.TronUSDTContract); err != nil {
			return 0, err
		}
	}
	targets, err := s.Engine.DepositScanTargets()
	if err != nil {
		return 0, err
	}
	if c.TronDepositEnabled && c.TronAddress != "" {
		for _, currency := range []string{"usdt", "trx"} {
			if currency == "trx" && !c.TronTRXEnabled {
				continue
			}
			contract := c.TronUSDTContract
			if currency == "trx" {
				contract = ""
			}
			found := false
			for _, t := range targets {
				if t.Currency == currency && t.Address == c.TronAddress && t.Contract == contract {
					found = true
				}
			}
			if !found {
				targets = append(targets, model.DepositTarget{Currency: currency, Address: c.TronAddress, Contract: contract, SinceNs: nowNs - depositWindowNs - depositHoldNs})
			}
		}
	}
	credited := 0
	var scanErrors []error
	for index, target := range targets {
		budget := 30 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			budget = time.Until(deadline) / time.Duration(len(targets)-index)
		}
		targetCtx, cancel := context.WithTimeout(ctx, budget)
		n, err := s.scanDepositTarget(targetCtx, nowNs, c, target)
		cancel()
		credited += n
		if err != nil {
			scanErrors = append(scanErrors, err)
		}
	}
	return credited, errors.Join(scanErrors...)
}

func (s *ControlPlaneService) scanDepositTarget(ctx context.Context, nowNs int64, c *config.RuntimeConfig, target model.DepositTarget) (int, error) {
	base := s.Tron
	if base == nil {
		endpoint := config.DefaultTronGridEndpoint
		if s.EnvCfg != nil && s.EnvCfg.TronGridEndpoint != "" {
			endpoint = s.EnvCfg.TronGridEndpoint
		}
		base = tron.NewClient(endpoint, c.TronAPIKey, target.Contract)
	}
	client := base.WithSettings(c.TronAPIKey, target.Contract)
	cursor := fmt.Sprintf("tron:%x", sha256.Sum256([]byte(target.Currency+"|"+target.Address+"|"+target.Contract)))
	// Keep the legacy cursor for existing environment-only deployments.
	if s.RuntimeCfg == nil && target.Currency == "usdt" && target.Address == c.TronAddress {
		cursor = depositCursorName
	}
	watermark, err := s.Engine.ChainCursor(cursor)
	if err != nil {
		return 0, err
	}
	since := watermark
	// Revisit outstanding order windows to catch confirmations indexed late.
	if since == 0 || (target.SinceNs > 0 && target.SinceNs/1e6 < since) {
		since = target.SinceNs / 1e6
	}
	savedSince, until, fingerprint, err := s.Engine.ChainScan(cursor)
	if err != nil {
		return 0, err
	}
	if until == 0 {
		until = nowNs/1_000_000 + 60_000
		if err := s.Engine.SaveChainScan(cursor, since, until, ""); err != nil {
			return 0, err
		}
	} else {
		since = savedSince
	}
	client = client.WithPagination(fingerprint, until)
	var page tron.TransferPage
	if target.Currency == "trx" {
		page, err = client.TRXTransfersSince(ctx, target.Address, since, 200)
	} else {
		page, err = client.TransfersSince(ctx, target.Address, since, 200)
	}
	if err != nil {
		// Only an explicitly invalid continuation restarts the bounded interval.
		// Timeouts, cancellation, rate limits and server errors retain progress.
		if fingerprint != "" && errors.Is(err, tron.ErrInvalidFingerprint) {
			return 0, errors.Join(err, s.Engine.SaveChainScan(cursor, since, until, ""))
		}
		return 0, err
	}
	credited := 0
	for _, transfer := range page.Transfers {
		if transfer.AmountMicro < depositMinMicro || transfer.BlockTSMs <= 0 || transfer.BlockTSMs > math.MaxInt64/1_000_000 || transfer.BlockTSMs > nowNs/1e6+60_000 {
			continue
		}
		entryID, err := newID("bl_")
		if err != nil {
			return credited, err
		}
		entry := model.BalanceEntry{ID: entryID, Note: "on-chain " + target.Currency + " deposit", Actor: "chain", RefID: transfer.TxID, Reason: model.BalanceReasonDeposit}
		_, err = s.Engine.CreditDeposit(transfer.TxID, transfer.AmountMicro, transfer.BlockTSMs*1e6, nowNs, cursor, transfer.BlockTSMs, entry, target)
		if errors.Is(err, state.ErrNotFound) || errors.Is(err, state.ErrConflict) {
			continue
		}
		if err != nil {
			return credited, err
		}
		credited++
	}
	if page.NewestRawMs > watermark && page.NewestRawMs <= nowNs/1e6+60_000 {
		err = s.Engine.AdvanceChainCursor(cursor, page.NewestRawMs, nowNs)
	}
	if err != nil {
		return credited, err
	}
	if page.NextFingerprint != "" {
		return credited, s.Engine.SaveChainScan(cursor, since, until, page.NextFingerprint)
	}
	return credited, s.Engine.CompleteChainScan(cursor, target, since, until, nowNs)
}

// StartDepositScanner runs the deposit sweep and chain listener until the
// returned function is called.
//
// The two loops share one goroutine because they are both about the same pool of
// amounts: the sweep frees what expired, the scan credits what arrived. The
// loop also runs while disabled, allowing live enablement and settlement of
// existing orders after an operator closes checkout.
func StartDepositScanner(cp *ControlPlaneService, interval time.Duration, logf func(string, ...any)) func() {
	if cp == nil || cp.Engine == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = depositPollInterval
	}
	passTimeout := max(interval, depositScanTimeout)
	if logf == nil {
		logf = func(string, ...any) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := ctx.Done()
	done := make(chan struct{})
	var once sync.Once

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				// A panic inside one pass must not end the loop: the next
				// tick retries, exactly as it does after a failed pass.
				safego.Run("the deposit scan", func() {
					nowNs := time.Now().UnixNano()
					if _, err := cp.SweepDeposits(nowNs); err != nil {
						logf("[service] sweep deposits: %v", err)
					}
					passCtx, passCancel := context.WithTimeout(ctx, passTimeout)
					_, err := cp.ScanDeposits(passCtx, nowNs)
					passCancel()
					if err != nil {
						logf("[service] scan deposits: %v", err)
					}
				})
			}
		}
	}()

	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}
