package service

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// Redemption codes are shaped differently from invite codes on purpose: a
// top-up code is read off a screen or a chat message and typed once, so it is
// longer than an invite and grouped to survive that. The alphabet is the same
// one - no 0/O, no 1/I/L - because the reason is the same.
//
// The plaintext is never stored. The repository keeps an HMAC of it under the
// key pepper, so a database copy cannot be redeemed, and this file is the only
// place a plaintext exists at all: it is generated here, returned once, and
// dropped.

const (
	// redeemCodeGroups and redeemCodeGroupLen give a 20-symbol code in five
	// groups of four. Twenty symbols from a 32-symbol alphabet is 100 bits,
	// which is far beyond what a rate-limited redemption endpoint allows
	// anyone to guess.
	redeemCodeGroups   = 5
	redeemCodeGroupLen = 4
	// redeemCodeSeparator is what groups the symbols.
	redeemCodeSeparator = '-'
	// maxRedeemCodeBatch caps one minting call.
	maxRedeemCodeBatch = 100
	// redeemCodeHintLen is how much of a code the console shows. Four symbols
	// identify a code to an operator without being enough to redeem it.
	redeemCodeHintLen = 4
)

// newRedeemCode returns a code shaped XXXX-XXXX-XXXX-XXXX-XXXX.
func newRedeemCode() (string, error) {
	buf := make([]byte, redeemCodeGroups*redeemCodeGroupLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("service: generate redeem code: %w", err)
	}

	var out strings.Builder
	for i, b := range buf {
		if i > 0 && i%redeemCodeGroupLen == 0 {
			out.WriteByte(redeemCodeSeparator)
		}
		out.WriteByte(inviteCodeAlphabet[int(b)%len(inviteCodeAlphabet)])
	}
	return out.String(), nil
}

// normalizeRedeemCode puts a typed code into the shape this service mints.
//
// People paste codes with spaces, in lower case, or without the separators, and
// none of that changes which code they hold. What is stripped is only
// presentation: case, whitespace and the separators. Anything else is left
// alone, so a code with a stray character still fails the shape check rather
// than being silently repaired into somebody else's code.
func normalizeRedeemCode(code string) string {
	var out strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(code)) {
		switch r {
		case redeemCodeSeparator, ' ', '\t', '\n', '\r':
			continue
		}
		out.WriteRune(r)
	}
	compact := out.String()

	var grouped strings.Builder
	for i := 0; i < len(compact); i++ {
		if i > 0 && i%redeemCodeGroupLen == 0 {
			grouped.WriteByte(redeemCodeSeparator)
		}
		grouped.WriteByte(compact[i])
	}
	return grouped.String()
}

// validRedeemCodeShape reports whether a normalized string could be a code this
// service minted, without asking the database.
//
// The shape is fixed, so nothing outside it can have come from newRedeemCode and
// is refused before any lookup. That matters here for the same reason it matters
// on the registration path: this endpoint is reachable by any signed-in user and
// a refusal should cost a string scan, not a query.
func validRedeemCodeShape(code string) bool {
	want := redeemCodeGroups*redeemCodeGroupLen + (redeemCodeGroups - 1)
	if len(code) != want {
		return false
	}
	for i := 0; i < len(code); i++ {
		if i%(redeemCodeGroupLen+1) == redeemCodeGroupLen {
			if code[i] != redeemCodeSeparator {
				return false
			}
			continue
		}
		if strings.IndexByte(inviteCodeAlphabet, code[i]) < 0 {
			return false
		}
	}
	return true
}

// codeHint returns the tail of a code: what an operator sees in a list.
func codeHint(code string) string {
	compact := strings.ReplaceAll(code, string(redeemCodeSeparator), "")
	if len(compact) <= redeemCodeHintLen {
		return compact
	}
	return compact[len(compact)-redeemCodeHintLen:]
}
