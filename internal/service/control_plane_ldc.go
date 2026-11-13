package service

import (
	"errors"
	"fmt"
	"math/big"
	"prism/internal/ldc"
	"strconv"
	"strings"

	"prism/internal/model"
	"prism/internal/state"
)

// LDCInfo describes whether Linux.do Credit checkout is available.
type LDCInfo struct {
	Enabled bool    `json:"enabled"`
	Rate    float64 `json:"rate"`
}

const ldcPaymentName = "Prism balance deposit"

// SelfLDCInfo reports whether the service has a configured EasyPay client.
func (s *ControlPlaneService) SelfLDCInfo() LDCInfo {
	c := s.paymentConfig()
	return LDCInfo{Enabled: c.LDCEnabled && c.LDCPID != "" && c.LDCKey != "", Rate: c.LDCRate}
}

// CreateLDCDeposit creates a durable pending payment intent and returns the
// signed EasyPay redirect. The amount is represented in the balance ledger's
// micro-units. Float callers use the same exact decimal validation as HTTP.
func (s *ControlPlaneService) CreateLDCDeposit(userID string, points float64, nowNs int64, origins ...string) (string, string, error) {
	return s.CreateLDCDepositDecimal(userID, strconv.FormatFloat(points, 'f', -1, 64), nowNs, origins...)
}

// CreateLDCDepositDecimal preserves the submitted decimal before validation.
func (s *ControlPlaneService) CreateLDCDepositDecimal(userID, points string, nowNs int64, origins ...string) (string, string, error) {
	if s == nil || s.Engine == nil {
		return "", "", internal("no state engine", nil)
	}
	c := s.paymentConfig()
	if !c.LDCEnabled || c.LDCPID == "" || c.LDCKey == "" {
		return "", "", invalidArg("LDC deposits are not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", "", invalidArg("user: required")
	}
	amountMicro, err := normalizeLDCPointsDecimal(points)
	if err != nil {
		return "", "", err
	}
	user, err := s.Engine.GetUser(userID)
	if err != nil {
		return "", "", mapUserLookupError(err)
	}
	if user.Status != model.UserStatusActive {
		return "", "", forbidden("LDC deposit: the account is not active")
	}
	outTradeNo, err := newID("ldc_")
	if err != nil {
		return "", "", internal("generate LDC trade number", err)
	}
	creditMicro, err := model.ConvertPaymentMicro(amountMicro, c.LDCRate)
	if err != nil {
		return "", "", invalidArg(err.Error())
	}
	if len(origins) > 0 && origins[0] != "" {
		if c.LDCNotifyURL == "" {
			c.LDCNotifyURL = origins[0] + "/api/v1/public/ldc/notify"
		}
		if c.LDCReturnURL == "" {
			c.LDCReturnURL = origins[0] + "/ui/deposit"
		}
	}
	redirectURL, err := paymentLDCClient(c).BuildPaymentURLCents(outTradeNo, ldcPaymentName, amountMicro/10_000)
	if err != nil {
		return "", "", internal("build LDC payment URL", err)
	}
	if err := s.Engine.InsertLDCDeposit(model.LDCDeposit{
		OutTradeNo:   outTradeNo,
		UserID:       userID,
		AmountMicro:  creditMicro,
		PaymentMicro: amountMicro, Rate: c.LDCRate, MerchantPID: c.LDCPID, VerificationKey: c.LDCKey,
		Status:      model.LDCDepositStatusPending,
		CreatedAtNs: nowNs,
	}); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return "", "", conflict("LDC trade number already exists")
		}
		return "", "", internal("record LDC deposit", err)
	}
	return redirectURL, outTradeNo, nil
}

// HandleLDCCallback verifies a gateway callback, resolves its trade number to
// the pending intent created above, and credits the account exactly once.
func (s *ControlPlaneService) HandleLDCCallback(params map[string]string, nowNs int64) error {
	if s == nil || s.Engine == nil {
		return internal("no state engine", nil)
	}

	outTradeNo := strings.TrimSpace(params["out_trade_no"])
	if outTradeNo == "" {
		return invalidArg("LDC callback: out_trade_no is required")
	}
	deposit, err := s.Engine.GetLDCDeposit(outTradeNo)
	if errors.Is(err, state.ErrNotFound) {
		return notFound("LDC callback: unknown out_trade_no")
	}
	if err != nil {
		return internal("read LDC deposit", err)
	}
	c := s.paymentConfig()
	key, pid := deposit.VerificationKey, deposit.MerchantPID
	if key == "" {
		key, pid = c.LDCKey, c.LDCPID
	} // pre-upgrade checkout
	client := ldc.NewClient(ldc.Config{PID: pid, Key: key})
	valid, err := client.VerifyCallback(params)
	if err != nil || key == "" || !valid || (params["pid"] != "" && params["pid"] != pid) {
		return invalidArg("LDC callback: invalid signature or trade status")
	}

	// A valid signature must attest to the exact amount of the pending order.
	callbackMicro, parseErr := ldcMoneyToMicro(params["money"])
	if parseErr != nil {
		return invalidArg("LDC callback: invalid money")
	}
	if callbackMicro != deposit.PaymentMicro {
		return invalidArg("LDC callback: money does not match the deposit")
	}

	tradeNo := strings.TrimSpace(params["trade_no"])
	if len(tradeNo) > 256 {
		return invalidArg("LDC callback: invalid trade number")
	}
	entryID, err := newID("bl_")
	if err != nil {
		return internal("generate balance entry id", err)
	}
	err = s.Engine.CreditLDCDeposit(outTradeNo, tradeNo, model.BalanceEntry{ID: entryID, Note: "LDC trade_no: " + tradeNo, Actor: "ldc", CreatedAtNs: nowNs})
	if errors.Is(err, state.ErrConflict) {
		return conflict("LDC callback: trade number is already in use")
	}
	if err != nil {
		return internal("credit LDC deposit", err)
	}
	return nil
}

func normalizeLDCPointsDecimal(raw string) (int64, error) {
	amount, err := ldcMoneyToMicro(raw)
	if err != nil || amount <= 0 || amount%10_000 != 0 {
		return 0, invalidArg("points: use a decimal amount from 0.01 to 1000000 with at most two decimal places")
	}
	return amount, nil
}

func ldcMoneyToMicro(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 32 || strings.Count(raw, ".") > 1 || raw[0] == '.' || raw[len(raw)-1] == '.' {
		return 0, fmt.Errorf("invalid money")
	}
	for _, c := range raw {
		if (c < '0' || c > '9') && c != '.' {
			return 0, fmt.Errorf("invalid money")
		}
	}
	r, ok := new(big.Rat).SetString(raw)
	if !ok || r.Sign() < 0 {
		return 0, fmt.Errorf("invalid money")
	}
	r.Mul(r, new(big.Rat).SetInt64(microPerUnit))
	if !r.IsInt() || !r.Num().IsInt64() || r.Num().Int64() > maxBalanceMicro {
		return 0, fmt.Errorf("money out of range")
	}
	return r.Num().Int64(), nil
}
