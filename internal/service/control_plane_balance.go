package service

import (
	"errors"
	"strings"

	"prism/internal/model"
	"prism/internal/state"
)

// Balance and redemption codes: the half of the commercial surface that moves
// money without moving traffic.
//
// Amounts are int64 micro-units throughout - one micro is 1e-6 USDT, which is
// the token's own precision - and are never converted to float. The console
// divides by 1e6 when it renders, and nothing else does arithmetic on them.
//
// Every movement is one ledger row. A balance is therefore never the only
// record of itself: what it is, when it changed and who changed it can all be
// read back, which is what makes an operator able to answer a question about
// somebody's money at all.

const (
	// microPerUnit is how many micro-units make one USDT. Display divides by it.
	microPerUnit = 1_000_000
	// maxBalanceMicro caps a single credit. It is far above any plausible
	// top-up and far below int64 overflow, so a mistyped amount is refused
	// instead of being stored.
	maxBalanceMicro = 1_000_000 * microPerUnit
	// defaultLedgerLimit is how many movements a balance read returns when the
	// caller does not ask for a number.
	defaultLedgerLimit = 50
	// maxLedgerLimit caps what a caller may ask for.
	maxLedgerLimit = 200
)

// BalanceView is a user's balance and its recent movements.
type BalanceView struct {
	BalanceMicro int64                `json:"balance_micro"`
	Ledger       []model.BalanceEntry `json:"ledger"`
}

// RedeemCodeInput is the accepted body of a code creation.
//
// AmountMicro is what the code credits; MaxUses of 0 means unlimited and
// ExpiresInDays of 0 never expires. Count is how many codes to mint at once,
// because an operator selling codes in a community forum mints them in batches.
type RedeemCodeInput struct {
	AmountMicro   int64  `json:"amount_micro"`
	MaxUses       int    `json:"max_uses"`
	ExpiresInDays int    `json:"expires_in_days"`
	Note          string `json:"note"`
	Count         int    `json:"count"`
}

// RedeemCodeView is a code as the console sees it.
//
// The plaintext appears exactly once - in the response that minted it - and is
// not part of this type. What a listing shows is the hint: enough to tell two
// codes apart, useless for redeeming one.
type RedeemCodeView struct {
	ID          string `json:"id"`
	CodeHint    string `json:"code_hint"`
	AmountMicro int64  `json:"amount_micro"`
	MaxUses     int    `json:"max_uses"`
	UsedCount   int    `json:"used_count"`
	Status      string `json:"status"`
	ExpiresAtNs int64  `json:"expires_at_ns"`
	Note        string `json:"note"`
	CreatedAtNs int64  `json:"created_at_ns"`
}

// CreatedRedeemCode is a freshly minted code: the view plus the plaintext, which
// is returned once and never stored.
type CreatedRedeemCode struct {
	RedeemCodeView
	Code string `json:"code"`
}

// SelfBalance returns a user's balance and recent movements.
func (s *ControlPlaneService) SelfBalance(userID string, limit int) (BalanceView, error) {
	if s == nil || s.Engine == nil {
		return BalanceView{}, internal("no state engine", nil)
	}
	if strings.TrimSpace(userID) == "" {
		return BalanceView{}, invalidArg("user: required")
	}
	if limit <= 0 {
		limit = defaultLedgerLimit
	}
	if limit > maxLedgerLimit {
		limit = maxLedgerLimit
	}

	balance, err := s.Engine.GetBalance(userID)
	if err != nil {
		return BalanceView{}, internal("read balance", err)
	}
	ledger, err := s.Engine.ListBalanceLedger(userID, limit)
	if err != nil {
		return BalanceView{}, internal("read ledger", err)
	}
	return BalanceView{BalanceMicro: balance, Ledger: ledger}, nil
}

// Redeem tops up the caller's balance with a code.
//
// Claiming the use and crediting the amount happen in one transaction in the
// repository, so a code is never spent without its money arriving. An unknown
// code, a revoked one, an expired one and a spent one all produce the same
// refusal: telling them apart would turn this endpoint into a way to find codes
// that work.
func (s *ControlPlaneService) Redeem(userID, code string, nowNs int64) (BalanceView, error) {
	if s == nil || s.Engine == nil {
		return BalanceView{}, internal("no state engine", nil)
	}
	if s.Pepper == nil {
		return BalanceView{}, internal("no pepper is configured, so codes cannot be verified", ErrNoPepper)
	}
	if strings.TrimSpace(userID) == "" {
		return BalanceView{}, invalidArg("user: required")
	}

	normalized := normalizeRedeemCode(code)
	if !validRedeemCodeShape(normalized) {
		return BalanceView{}, invalidArg("code: the code is not valid")
	}

	hash := s.Pepper.HashSecret(normalized)
	id, err := newID("bl_")
	if err != nil {
		return BalanceView{}, internal("generate ledger id", err)
	}

	// The amount is the code's, so it is read before the transaction opens; the
	// transaction then re-checks the code's state and credits in one step. A
	// code that disappears between the two is refused by the claim itself.
	amount, err := s.redeemCodeAmount(hash)
	if err != nil {
		return BalanceView{}, err
	}

	entry, err := s.Engine.RedeemCodeAndCredit(hash, model.BalanceEntry{
		ID:          id,
		UserID:      userID,
		DeltaMicro:  amount,
		Reason:      model.BalanceReasonRedeem,
		RefID:       strings.TrimSpace(string(hash)),
		CreatedAtNs: nowNs,
	}, nowNs)
	if errors.Is(err, state.ErrNotFound) {
		return BalanceView{}, invalidArg("code: the code is not valid")
	}
	if errors.Is(err, state.ErrConflict) {
		return BalanceView{}, conflict("that code has already been redeemed")
	}
	if err != nil {
		return BalanceView{}, internal("redeem code", err)
	}

	return BalanceView{BalanceMicro: entry.BalanceAfterMicro}, nil
}

// AdminBalance returns any user's balance and recent movements.
func (s *ControlPlaneService) AdminBalance(userID string, limit int) (BalanceView, error) {
	if s == nil || s.Engine == nil {
		return BalanceView{}, internal("no state engine", nil)
	}
	if _, err := s.Engine.GetUser(userID); err != nil {
		return BalanceView{}, mapUserLookupError(err)
	}
	return s.SelfBalance(userID, limit)
}

// AdjustBalance moves a user's balance by hand.
//
// A note is required: this is the one movement with no external record behind
// it, so the note and the actor are the entire audit trail. The amount is signed,
// and a debit larger than the balance is refused rather than allowed to go
// negative.
func (s *ControlPlaneService) AdjustBalance(userID string, deltaMicro int64, note, actor string, nowNs int64) (BalanceView, error) {
	if s == nil || s.Engine == nil {
		return BalanceView{}, internal("no state engine", nil)
	}
	if _, err := s.Engine.GetUser(userID); err != nil {
		return BalanceView{}, mapUserLookupError(err)
	}
	if deltaMicro == 0 {
		return BalanceView{}, invalidArg("delta_micro: must not be zero")
	}
	if deltaMicro > maxBalanceMicro || deltaMicro < -maxBalanceMicro {
		return BalanceView{}, invalidArg("delta_micro: the amount is out of range")
	}
	note = strings.TrimSpace(note)
	if note == "" {
		return BalanceView{}, invalidArg("note: required for a manual balance change")
	}

	id, err := newID("bl_")
	if err != nil {
		return BalanceView{}, internal("generate ledger id", err)
	}
	if _, err := s.Engine.ApplyBalanceDelta(model.BalanceEntry{
		ID:          id,
		UserID:      userID,
		DeltaMicro:  deltaMicro,
		Reason:      model.BalanceReasonAdminAdjust,
		Note:        note,
		Actor:       actor,
		CreatedAtNs: nowNs,
	}); err != nil {
		if errors.Is(err, state.ErrInsufficientBalance) {
			return BalanceView{}, invalidArg("delta_micro: the balance would go negative")
		}
		return BalanceView{}, internal("adjust balance", err)
	}
	return s.AdminBalance(userID, 0)
}

// CreateRedeemCodes mints codes in one call, returning each plaintext once.
//
// A batch is capped so a typo in the count cannot mint a thousand rows, and the
// amount is checked once for the batch rather than per code.
func (s *ControlPlaneService) CreateRedeemCodes(in RedeemCodeInput, createdBy string, nowNs int64) ([]CreatedRedeemCode, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("no state engine", nil)
	}
	if s.Pepper == nil {
		return nil, internal("no pepper is configured, so codes cannot be minted", ErrNoPepper)
	}
	if in.AmountMicro <= 0 {
		return nil, invalidArg("amount_micro: must be greater than zero")
	}
	if in.AmountMicro > maxBalanceMicro {
		return nil, invalidArg("amount_micro: the amount is out of range")
	}
	if in.MaxUses < 0 {
		return nil, invalidArg("max_uses: must not be negative")
	}
	if in.ExpiresInDays < 0 {
		return nil, invalidArg("expires_in_days: must not be negative")
	}
	count := in.Count
	if count <= 0 {
		count = 1
	}
	if count > maxRedeemCodeBatch {
		return nil, invalidArg("count: at most 100 codes per call")
	}

	created := make([]CreatedRedeemCode, 0, count)
	for i := 0; i < count; i++ {
		id, err := newID("rc_")
		if err != nil {
			return nil, internal("generate redeem code id", err)
		}
		code, err := newRedeemCode()
		if err != nil {
			return nil, internal("generate redeem code", err)
		}
		row := model.RedeemCode{
			ID:          id,
			CodeHash:    s.Pepper.HashSecret(code),
			CodeHint:    codeHint(code),
			AmountMicro: in.AmountMicro,
			MaxUses:     in.MaxUses,
			UsedCount:   0,
			ExpiresAtNs: expiryFromDays(in.ExpiresInDays, nowNs),
			Note:        strings.TrimSpace(in.Note),
			Status:      model.RedeemCodeStatusActive,
			CreatedBy:   createdBy,
			CreatedAtNs: nowNs,
			UpdatedAtNs: nowNs,
		}
		if err := s.Engine.InsertRedeemCode(row); err != nil {
			if errors.Is(err, state.ErrConflict) {
				return nil, conflict("a generated code collided with an existing one")
			}
			return nil, internal("insert redeem code", err)
		}
		created = append(created, CreatedRedeemCode{
			RedeemCodeView: redeemCodeView(row, nowNs),
			Code:           code,
		})
	}
	return created, nil
}

// ListRedeemCodes returns codes, newest first, with the total before paging.
func (s *ControlPlaneService) ListRedeemCodes(limit, offset int, nowNs int64) ([]RedeemCodeView, int, error) {
	if s == nil || s.Engine == nil {
		return nil, 0, internal("no state engine", nil)
	}
	rows, err := s.Engine.ListRedeemCodes()
	if err != nil {
		return nil, 0, internal("list redeem codes", err)
	}
	views := make([]RedeemCodeView, 0, len(rows))
	for _, row := range rows {
		views = append(views, redeemCodeView(row, nowNs))
	}
	total := len(views)
	return pageSlice(views, limit, offset), total, nil
}

// DeleteRedeemCode withdraws a code. Balances it already credited are untouched.
func (s *ControlPlaneService) DeleteRedeemCode(id string) error {
	if s == nil || s.Engine == nil {
		return internal("no state engine", nil)
	}
	if err := s.Engine.DeleteRedeemCode(id); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return notFound("that redeem code does not exist")
		}
		return internal("delete redeem code", err)
	}
	return nil
}

// redeemCodeAmount reads what a code is worth, without consuming it.
//
// This runs before the transaction so the amount is known when the ledger row is
// built. It cannot be the authority on whether the code is still usable - the
// claim inside the transaction is - so a code that is spent or revoked between
// the two is refused there, and the amount read here is discarded.
func (s *ControlPlaneService) redeemCodeAmount(hash []byte) (int64, error) {
	code, err := s.Engine.GetRedeemCodeByHash(hash)
	if errors.Is(err, state.ErrNotFound) {
		return 0, invalidArg("code: the code is not valid")
	}
	if err != nil {
		return 0, internal("read redeem code", err)
	}
	return code.AmountMicro, nil
}

// redeemCodeView renders a code, reporting an exhausted or expired one as
// disabled so a reader sees what the redeem path will do.
func redeemCodeView(row model.RedeemCode, nowNs int64) RedeemCodeView {
	view := RedeemCodeView{
		ID:          row.ID,
		CodeHint:    row.CodeHint,
		AmountMicro: row.AmountMicro,
		MaxUses:     row.MaxUses,
		UsedCount:   row.UsedCount,
		Status:      row.Status,
		ExpiresAtNs: row.ExpiresAtNs,
		Note:        row.Note,
		CreatedAtNs: row.CreatedAtNs,
	}
	if view.Status == model.RedeemCodeStatusActive && !redeemCodeUsable(row, nowNs) {
		view.Status = model.RedeemCodeStatusDisabled
	}
	return view
}

// redeemCodeUsable reports whether the redeem path would accept the code.
func redeemCodeUsable(row model.RedeemCode, nowNs int64) bool {
	if row.Status != model.RedeemCodeStatusActive {
		return false
	}
	if row.ExpiresAtNs > 0 && nowNs >= row.ExpiresAtNs {
		return false
	}
	return row.MaxUses == 0 || row.UsedCount < row.MaxUses
}
