package service

import (
	"strings"
	"testing"

	"prism/internal/model"
)

// The shape helpers behind redemption codes. These are pure functions, so they
// are tested without a database: a code's shape is what protects the endpoint
// from being probed, and it should be wrong here rather than in production.

func TestNewRedeemCodeShape(t *testing.T) {
	code, err := newRedeemCode()
	if err != nil {
		t.Fatalf("newRedeemCode: %v", err)
	}
	if !validRedeemCodeShape(code) {
		t.Fatalf("a freshly minted code failed its own shape check: %q", code)
	}
	if got, want := len(code), redeemCodeGroups*redeemCodeGroupLen+(redeemCodeGroups-1); got != want {
		t.Fatalf("length: got %d, want %d", got, want)
	}
	if strings.Count(code, string(redeemCodeSeparator)) != redeemCodeGroups-1 {
		t.Fatalf("separators: %q", code)
	}
}

func TestNewRedeemCodeIsNotRepeated(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		code, err := newRedeemCode()
		if err != nil {
			t.Fatalf("newRedeemCode: %v", err)
		}
		if _, dup := seen[code]; dup {
			t.Fatalf("newRedeemCode repeated %q within 64 draws", code)
		}
		seen[code] = struct{}{}
	}
}

func TestNormalizeRedeemCode(t *testing.T) {
	const canonical = "ABCD-EFGH-JKLM-NPQR-STUV"

	cases := []struct {
		name string
		in   string
	}{
		{"lower case", "abcd-efgh-jklm-npqr-stuv"},
		{"no separators", "ABCDEFGHJKLMNPQRSTUV"},
		{"spaces instead of separators", "ABCD EFGH JKLM NPQR STUV"},
		{"surrounding whitespace", "  ABCD-EFGH-JKLM-NPQR-STUV\n"},
		{"mixed", " abcd efgh-JKLM npqr stuv "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeRedeemCode(tc.in); got != canonical {
				t.Fatalf("got %q, want %q", got, canonical)
			}
		})
	}
}

func TestNormalizeRedeemCodeDoesNotRepairBadInput(t *testing.T) {
	// A character outside the alphabet stays, so the shape check still refuses
	// it. Silently dropping it could turn a typo into somebody else's code.
	got := normalizeRedeemCode("ABCD-EFGH-JKLM-NPQR-STU0")
	if !strings.Contains(got, "0") {
		t.Fatalf("a stray character was removed: %q", got)
	}
	if validRedeemCodeShape(got) {
		t.Fatalf("a code with a stray character passed the shape check: %q", got)
	}
}

func TestValidRedeemCodeShapeRefusals(t *testing.T) {
	valid := "ABCD-EFGH-JKLM-NPQR-STUV"
	if !validRedeemCodeShape(valid) {
		t.Fatalf("the reference code failed: %q", valid)
	}

	refusals := []struct {
		name string
		code string
	}{
		{"empty", ""},
		{"too short", "ABCD-EFGH-JKLM-NPQR-STU"},
		{"too long", "ABCD-EFGH-JKLM-NPQR-STUVW"},
		{"missing separator", "ABCDEFGHJKLMNPQRSTUV"},
		{"wrong separator position", "ABCDE-FGH-JKLM-NPQR-STUV"},
		{"ambiguous symbol 0", "0BCD-EFGH-JKLM-NPQR-STUV"},
		{"ambiguous symbol O", "OBCD-EFGH-JKLM-NPQR-STUV"},
		{"ambiguous symbol 1", "1BCD-EFGH-JKLM-NPQR-STUV"},
		{"lower case", "abcd-efgh-jklm-npqr-stuv"},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			if validRedeemCodeShape(tc.code) {
				t.Fatalf("accepted %q", tc.code)
			}
		})
	}
}

func TestCodeHint(t *testing.T) {
	if got := codeHint("ABCD-EFGH-JKLM-NPQR-STUV"); got != "STUV" {
		t.Fatalf("hint: got %q, want STUV", got)
	}
	if got := codeHint("AB"); got != "AB" {
		t.Fatalf("short hint: got %q, want AB", got)
	}
}

func TestRedeemCodeUsable(t *testing.T) {
	cases := []struct {
		name        string
		status      string
		maxUses     int
		usedCount   int
		expiresAtNs int64
		nowNs       int64
		want        bool
	}{
		{"active and fresh", model.RedeemCodeStatusActive, 1, 0, 0, 100, true},
		{"unlimited uses", model.RedeemCodeStatusActive, 0, 99, 0, 100, true},
		{"spent", model.RedeemCodeStatusActive, 1, 1, 0, 100, false},
		{"expired", model.RedeemCodeStatusActive, 1, 0, 100, 100, false},
		{"not yet expired", model.RedeemCodeStatusActive, 1, 0, 101, 100, true},
		{"disabled", model.RedeemCodeStatusDisabled, 1, 0, 0, 100, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := model.RedeemCode{
				Status:      tc.status,
				MaxUses:     tc.maxUses,
				UsedCount:   tc.usedCount,
				ExpiresAtNs: tc.expiresAtNs,
			}
			if got := redeemCodeUsable(row, tc.nowNs); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
