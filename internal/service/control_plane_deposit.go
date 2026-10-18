package service

import (
	"context"
	"errors"
	"strings"
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
)

// DepositIntentView is what the deposit page shows.
type DepositIntentView struct {
	ID          string `json:"id"`
	AmountMicro int64  `json:"amount_micro"`
	Status      string `json:"status"`
	Address     string `json:"address"`
	TxID        string `json:"tx_id"`
	CreatedAtNs int64  `json:"created_at_ns"`
	ExpiresAtNs int64  `json:"expires_at_ns"`
	HoldUntilNs int64  `json:"hold_until_ns"`
	PaidAtNs    int64  `json:"paid_at_ns"`
}

// DepositInfo describes whether deposits are available and on what terms.
type DepositInfo struct {
	Enabled    bool   `json:"enabled"`
	Address    string `json:"address"`
	Contract   string `json:"contract"`
	MinMicro   int64  `json:"min_micro"`
	MaxMicro   int64  `json:"max_micro"`
	WindowSecs int64  `json:"window_secs"`
}

func (s *ControlPlaneService) depositAddress() string {
	if s.EnvCfg == nil {
		return ""
	}
	return strings.TrimSpace(s.EnvCfg.USDTAddress)
}

// SelfDepositInfo reports the terms of a deposit, or that the feature is off.
//
// An unconfigured receiving address is not an error: an operator who has not set
// one up simply has no on-chain deposits, and everything else keeps working.
func (s *ControlPlaneService) SelfDepositInfo() DepositInfo {
	address := s.depositAddress()
	info := DepositInfo{
		Enabled:    address != "",
		Address:    address,
		MinMicro:   depositMinMicro,
		MaxMicro:   depositMaxMicro,
		WindowSecs: depositWindowNs / 1_000_000_000,
	}
	if s.EnvCfg != nil {
		info.Contract = s.EnvCfg.USDTContract
	}
	return info
}

// SelfDeposit opens an intent for wantMicro and returns the amount to pay.
//
// The amount returned is wantMicro plus the smallest number of micro-units that
// nobody else is holding. Whole amounts are never handed out: a round number is
// the one most likely to collide with an unrelated transfer, which is exactly
// what the unique amount exists to avoid.
func (s *ControlPlaneService) SelfDeposit(userID string, wantMicro int64, nowNs int64) (DepositIntentView, error) {
	if s.depositAddress() == "" {
		return DepositIntentView{}, invalidArg("deposit: on-chain deposits are not configured")
	}
	if wantMicro < depositMinMicro {
		return DepositIntentView{}, invalidArg("amount_micro: the smallest deposit is 1 USDT")
	}
	if wantMicro > depositMaxMicro {
		return DepositIntentView{}, invalidArg("amount_micro: the largest deposit is 100000 USDT")
	}
	user, err := s.Engine.GetUser(userID)
	if err != nil {
		return DepositIntentView{}, err
	}
	if user.Status != model.UserStatusActive {
		return DepositIntentView{}, forbidden("deposit: the account is not active")
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
		ID:          intentID,
		UserID:      userID,
		CreatedAtNs: nowNs,
		ExpiresAtNs: nowNs + depositWindowNs,
		HoldUntilNs: nowNs + depositWindowNs + depositHoldNs,
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
	return DepositIntentView{
		ID:          intent.ID,
		AmountMicro: intent.AmountMicro,
		Status:      intent.Status,
		Address:     s.depositAddress(),
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
	address := s.depositAddress()
	if address == "" {
		return 0, nil
	}
	if s.Tron == nil {
		return 0, nil
	}

	watermarkMs, err := s.Engine.ChainCursor(depositCursorName)
	if err != nil {
		return 0, err
	}
	transfers, err := s.Tron.TransfersSince(ctx, address, watermarkMs, 200)
	if err != nil {
		return 0, err
	}

	credited := 0
	for _, transfer := range transfers {
		if transfer.AmountMicro < depositMinMicro {
			continue
		}
		blockTSNs := transfer.BlockTSMs * 1_000_000
		entryID, err := newID("bl_")
		if err != nil {
			return credited, err
		}
		entry := model.BalanceEntry{
			ID:     entryID,
			Note:   "on-chain deposit",
			Actor:  "chain",
			RefID:  transfer.TxID,
			Reason: model.BalanceReasonDeposit,
		}
		if _, err := s.Engine.CreditDeposit(transfer.TxID, transfer.AmountMicro, blockTSNs, nowNs, depositCursorName, transfer.BlockTSMs, entry); err != nil {
			if errors.Is(err, state.ErrNotFound) {
				// No intent owns this transfer. It stays on chain for the
				// operator to handle by hand; nothing is guessed here.
				continue
			}
			if errors.Is(err, state.ErrConflict) {
				// Already credited. The cursor below still moves past it.
				continue
			}
			return credited, err
		}
		credited++
	}

	// Advance past everything that was read, so the next poll starts where this
	// one stopped. Matched transfers already advanced it inside their own
	// transaction; this covers the unmatched ones.
	if len(transfers) > 0 {
		newest := transfers[len(transfers)-1].BlockTSMs
		if newest > watermarkMs {
			if err := s.Engine.AdvanceChainCursor(depositCursorName, newest, nowNs); err != nil {
				return credited, err
			}
		}
	}
	return credited, nil
}

// StartDepositScanner runs the deposit sweep and chain listener until the
// returned function is called.
//
// The two loops share one goroutine because they are both about the same pool of
// amounts: the sweep frees what expired, the scan credits what arrived. When
// deposits are not configured there is nothing to watch and the returned
// function does nothing.
func StartDepositScanner(cp *ControlPlaneService, interval time.Duration, logf func(string, ...any)) func() {
	if cp == nil || cp.depositAddress() == "" || cp.Tron == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = depositPollInterval
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	stop := make(chan struct{})
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
					ctx, cancel := context.WithTimeout(context.Background(), interval)
					_, err := cp.ScanDeposits(ctx, nowNs)
					cancel()
					if err != nil {
						logf("[service] scan deposits: %v", err)
					}
				})
			}
		}
	}()

	return func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}
}
