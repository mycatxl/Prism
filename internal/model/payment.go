package model

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
)

// ConvertPaymentMicro applies a decimal rate exactly and rounds down once to
// the ledger's micro unit. No binary floating-point product reaches accounting.
func ConvertPaymentMicro(amount int64, rate float64) (int64, error) {
	if amount <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 || rate > 1_000_000 {
		return 0, fmt.Errorf("payment: invalid amount or rate")
	}
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(rate, 'f', -1, 64))
	if !ok {
		return 0, fmt.Errorf("payment: invalid rate")
	}
	r.Mul(r, new(big.Rat).SetInt64(amount))
	n := new(big.Int).Quo(r.Num(), r.Denom())
	if !n.IsInt64() || n.Sign() <= 0 || n.Int64() > 1_000_000_000_000 {
		return 0, fmt.Errorf("payment: converted credit must be between one micro unit and 1000000 balance units")
	}
	return n.Int64(), nil
}
