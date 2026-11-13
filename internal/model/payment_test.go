package model

import (
	"math"
	"testing"
)

func TestPaymentConversionDecimalBoundaries(t *testing.T) {
	for _, tc := range []struct {
		amount int64
		rate   float64
		want   int64
	}{{10_000, 0.00016, 1}, {10_000, 0.00006, 0}, {50_000_123, 0.125, 6_250_015}, {1, 1, 1}, {1_000_000_000_000, 1, 1_000_000_000_000}, {1_000_000_000_001, 1, 0}, {1, math.Inf(1), 0}, {math.MaxInt64, 1, 0}} {
		got, err := ConvertPaymentMicro(tc.amount, tc.rate)
		if tc.want == 0 {
			if err == nil {
				t.Fatalf("accepted %d * %g", tc.amount, tc.rate)
			}
		} else if err != nil || got != tc.want {
			t.Fatalf("%d * %g = %d (%v), want %d", tc.amount, tc.rate, got, err, tc.want)
		}
	}
}
