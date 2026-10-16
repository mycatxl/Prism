package auth

import (
	"strings"
	"testing"
	"time"
)

// The RFC 6238 Appendix B vectors. The published codes are eight digits, and
// this implementation produces six; the six-digit code is the last six digits
// of the eight-digit one, because both are the same value reduced modulo a
// power of ten. Checking against the published numbers is what makes this a
// conformance test rather than a test that the code agrees with itself.
func TestTOTPCode_MatchesTheRFC6238Vectors(t *testing.T) {
	// The RFC's shared secret is the ASCII string "12345678901234567890".
	secret := base32NoPadding.EncodeToString([]byte("12345678901234567890"))

	cases := []struct {
		unix int64
		want string // the RFC's eight-digit answer
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}

	for _, tc := range cases {
		got, err := TOTPCode(secret, time.Unix(tc.unix, 0))
		if err != nil {
			t.Fatalf("TOTPCode(%d): %v", tc.unix, err)
		}
		wantSix := tc.want[len(tc.want)-totpDigits:]
		if got != wantSix {
			t.Errorf("TOTPCode at %d = %s, want %s (the last six digits of the RFC's %s)",
				tc.unix, got, wantSix, tc.want)
		}
	}
}

// The code changes when the step rolls over and not before.
func TestTOTPCode_IsStableWithinAStep(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}

	// The base is aligned to a step boundary, because the counter is a floor of
	// the timestamp: starting mid-step would make "the next step" land at a
	// different offset than the test assumes.
	base := time.Unix((1_700_000_000/int64(totpStep.Seconds()))*int64(totpStep.Seconds()), 0)
	first, err := TOTPCode(secret, base)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}

	// Anywhere inside the same step gives the same code.
	for _, delta := range []time.Duration{time.Second, 10 * time.Second, totpStep - time.Second} {
		got, err := TOTPCode(secret, base.Add(delta))
		if err != nil {
			t.Fatalf("TOTPCode: %v", err)
		}
		if got != first {
			t.Errorf("the code changed %s into the step: %s != %s", delta, got, first)
		}
	}

	// One step later is outside it.
	next, err := TOTPCode(secret, base.Add(totpStep))
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	if next == first {
		t.Log("two consecutive codes happened to be equal; not a failure")
	}
}

// The accepted window is one step either side, which is what covers ordinary
// clock drift between the server and a phone.
func TestVerifyTOTP_AcceptsOneStepOfDrift(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)

	previous, err := TOTPCode(secret, now.Add(-totpStep))
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	next, err := TOTPCode(secret, now.Add(totpStep))
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	current, err := TOTPCode(secret, now)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}

	if !VerifyTOTP(secret, current, now) {
		t.Error("the current code was refused")
	}
	if !VerifyTOTP(secret, previous, now) {
		t.Error("the previous step's code was refused, so a slow clock would lock the user out")
	}
	if !VerifyTOTP(secret, next, now) {
		t.Error("the next step's code was refused, so a fast clock would lock the user out")
	}

	// Two steps away is outside the window.
	tooOld, err := TOTPCode(secret, now.Add(-2*totpStep))
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	if VerifyTOTP(secret, tooOld, now) {
		t.Error("a code two steps old was accepted, which widens the guessing window for no benefit")
	}
}

// Anything malformed is refused rather than being coerced.
func TestVerifyTOTP_RefusesMalformedInput(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	valid, err := TOTPCode(secret, now)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}

	cases := []struct {
		name string
		code string
	}{
		{"empty", ""},
		{"too short", valid[:5]},
		{"too long", valid + "0"},
		{"letters", "abcdef"},
		{"only spaces", "      "},
		{"an old code of the right shape", "000000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if VerifyTOTP(secret, tc.code, now) {
				t.Errorf("VerifyTOTP accepted %q", tc.code)
			}
		})
	}

	// A secret that is not valid Base32 refuses every code rather than
	// panicking.
	if VerifyTOTP("not base32!!", valid, now) {
		t.Error("a code was accepted for an undecodable secret")
	}
	_ = valid
}

// A code from one secret must not work for another.
func TestVerifyTOTP_CodeIsBoundToTheSecret(t *testing.T) {
	first, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	second, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	if first == second {
		t.Fatal("two generated secrets are identical")
	}

	now := time.Unix(1_700_000_000, 0)
	code, err := TOTPCode(first, now)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	if VerifyTOTP(second, code, now) {
		t.Error("a code from one secret was accepted for another")
	}
}

// Secrets are shown with padding and in mixed case by different applications,
// so all of those spellings have to work.
func TestTOTPCode_AcceptsTheSpellingsUsersPaste(t *testing.T) {
	raw := []byte("12345678901234567890")
	canonical := base32NoPadding.EncodeToString(raw)
	now := time.Unix(1_700_000_000, 0)

	want, err := TOTPCode(canonical, now)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}

	for _, spelling := range []string{
		canonical,
		strings.ToLower(canonical),
		canonical + "=",      // one pad byte
		canonical + "======", // the app's padding
		" " + canonical + " ",
	} {
		got, err := TOTPCode(spelling, now)
		if err != nil {
			t.Errorf("TOTPCode(%q): %v", spelling, err)
			continue
		}
		if got != want {
			t.Errorf("TOTPCode(%q) = %s, want %s", spelling, got, want)
		}
	}
}

// A generated secret decodes to the expected length, so it is what
// authenticator apps expect to receive.
func TestGenerateTOTPSecret_ProducesTheExpectedShape(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		secret, err := GenerateTOTPSecret()
		if err != nil {
			t.Fatalf("GenerateTOTPSecret: %v", err)
		}
		if strings.ContainsAny(secret, "=") {
			t.Fatalf("the secret %q carries padding", secret)
		}
		key, err := decodeTOTPSecret(secret)
		if err != nil {
			t.Fatalf("the generated secret does not decode: %v", err)
		}
		if len(key) != totpSecretBytes {
			t.Fatalf("the secret decodes to %d bytes, want %d", len(key), totpSecretBytes)
		}
		if seen[secret] {
			t.Fatalf("a duplicate secret was generated: %q", secret)
		}
		seen[secret] = true
	}
}

// The secret is stored sealed, and only the pepper that sealed it can open it.
func TestTOTPSecretEncryptionRoundTrip(t *testing.T) {
	pepper, err := NewPepper([]byte("a-test-pepper-of-at-least-16-bytes"))
	if err != nil {
		t.Fatalf("NewPepper: %v", err)
	}
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}

	sealed, err := EncryptTOTPSecret(pepper, secret)
	if err != nil {
		t.Fatalf("EncryptTOTPSecret: %v", err)
	}
	// The plaintext must not be recoverable from the stored bytes.
	if strings.Contains(string(sealed), secret) {
		t.Fatal("the sealed value contains the plaintext secret")
	}

	opened, err := DecryptTOTPSecret(pepper, sealed)
	if err != nil {
		t.Fatalf("DecryptTOTPSecret: %v", err)
	}
	if opened != secret {
		t.Fatalf("round trip gave %q, want %q", opened, secret)
	}

	// The same plaintext seals differently each time, because the nonce is
	// random: two users enrolling with the same secret must not be detectable
	// from the stored bytes.
	again, err := EncryptTOTPSecret(pepper, secret)
	if err != nil {
		t.Fatalf("EncryptTOTPSecret: %v", err)
	}
	if string(again) == string(sealed) {
		t.Fatal("sealing the same secret twice produced identical bytes, so the nonce is not random")
	}
}

// A different pepper cannot open the secret: losing the pepper means losing the
// second factor, which is why the backup has to include it.
func TestDecryptTOTPSecret_RefusesAnotherPepper(t *testing.T) {
	sealer, err := NewPepper([]byte("the-pepper-that-sealed-it"))
	if err != nil {
		t.Fatalf("NewPepper: %v", err)
	}
	other, err := NewPepper([]byte("a-completely-different-pepper"))
	if err != nil {
		t.Fatalf("NewPepper: %v", err)
	}

	sealed, err := EncryptTOTPSecret(sealer, "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatalf("EncryptTOTPSecret: %v", err)
	}
	if _, err := DecryptTOTPSecret(other, sealed); err == nil {
		t.Fatal("a secret sealed under one pepper was opened with another")
	}
}

// A tampered or truncated value fails closed rather than being treated as an
// empty secret, which would silently disable the second factor for that user.
func TestDecryptTOTPSecret_RefusesTamperedAndTruncatedValues(t *testing.T) {
	pepper, err := NewPepper([]byte("a-test-pepper-of-at-least-16-bytes"))
	if err != nil {
		t.Fatalf("NewPepper: %v", err)
	}
	sealed, err := EncryptTOTPSecret(pepper, "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatalf("EncryptTOTPSecret: %v", err)
	}

	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := DecryptTOTPSecret(pepper, tampered); err == nil {
		t.Error("a tampered secret was opened")
	}

	for _, truncated := range [][]byte{nil, {}, sealed[:4], sealed[:11]} {
		if _, err := DecryptTOTPSecret(pepper, truncated); err == nil {
			t.Errorf("a %d-byte value was opened as a secret", len(truncated))
		}
	}
}

// No pepper means no second factor, and that has to be an error rather than a
// silent success.
func TestTOTPEncryption_WithoutAPepperIsAnError(t *testing.T) {
	if _, err := EncryptTOTPSecret(nil, "JBSWY3DPEHPK3PXP"); err == nil {
		t.Error("sealing without a pepper succeeded")
	}
	if _, err := DecryptTOTPSecret(nil, []byte("whatever")); err == nil {
		t.Error("opening without a pepper succeeded")
	}
}

// The enrolment URI has to be scannable: the right scheme, the secret, and the
// parameters the apps expect.
func TestTOTPURI(t *testing.T) {
	got := TOTPURI("Prism", "admin", "JBSWY3DPEHPK3PXP")

	for _, want := range []string{
		"otpauth://totp/",
		"secret=JBSWY3DPEHPK3PXP",
		"issuer=Prism",
		"algorithm=SHA1",
		"digits=6",
		"period=30",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the URI %q does not contain %q", got, want)
		}
	}

	// A label with characters that would break the URI is escaped.
	escaped := TOTPURI("Prism", "user name+tag", "JBSWY3DPEHPK3PXP")
	if strings.Contains(escaped, "user name+tag") {
		t.Errorf("the label was not escaped: %q", escaped)
	}
	if !strings.Contains(escaped, "user%20name%2Btag") {
		t.Errorf("the label was escaped unexpectedly: %q", escaped)
	}

	// Without an issuer the label is just the account.
	bare := TOTPURI("", "admin", "JBSWY3DPEHPK3PXP")
	if !strings.Contains(bare, "/admin?") {
		t.Errorf("the bare URI is %q", bare)
	}
}

// VerifyTOTPStep is the primitive the single-use rule is built on: it answers
// with the step the code belonged to, which is the only stable identity a code
// has. Two attempts a few seconds apart must report the same step, and an older
// code inside the tolerance must report the older step rather than the one it is
// being verified at.
func TestVerifyTOTPStep_ReportsTheStepTheCodeBelongedTo(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	// Aligned to a step boundary so "the previous step" is exactly one step.
	base := time.Unix((1_700_000_000/int64(totpStep.Seconds()))*int64(totpStep.Seconds()), 0)
	want := base.Unix() * int64(time.Second)

	current, err := TOTPCode(secret, base)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	stepNs, ok := VerifyTOTPStep(secret, current, base)
	if !ok {
		t.Fatal("the current code was refused")
	}
	if stepNs != want {
		t.Fatalf("the step of the current code: got %d, want %d", stepNs, want)
	}

	// A few seconds later, still inside the same step, the same code reports
	// the same step: this is what makes the ledger comparable across attempts.
	later := base.Add(totpStep - time.Second)
	if stepNs, ok := VerifyTOTPStep(secret, current, later); !ok || stepNs != want {
		t.Fatalf("the step of the same code later in its window: got (%d, %v), want (%d, true)", stepNs, ok, want)
	}

	// The previous step's code is accepted inside the tolerance, and it names
	// its own step.
	previous, err := TOTPCode(secret, base.Add(-totpStep))
	if err != nil {
		t.Fatalf("TOTPCode(previous): %v", err)
	}
	twoOld, err := TOTPCode(secret, base.Add(-2*totpStep))
	if err != nil {
		t.Fatalf("TOTPCode(two steps old): %v", err)
	}
	if previous == current || previous == twoOld {
		// A one-in-a-million collision between two steps of this secret; the
		// newest match would win and the assertion below would be about that
		// instead.
		t.Log("two steps of this secret generated the same code; skipping the ordering check")
	} else if stepNs, ok := VerifyTOTPStep(secret, previous, base); !ok || stepNs != want-int64(totpStep) {
		t.Fatalf("the step of the previous code: got (%d, %v), want (%d, true)", stepNs, ok, want-int64(totpStep))
	}

	// Outside the tolerated window: refused, and no step is reported.
	if stepNs, ok := VerifyTOTPStep(secret, twoOld, base); ok || stepNs != 0 {
		t.Fatalf("a code two steps old reported (%d, %v), want (0, false)", stepNs, ok)
	}

	for _, code := range []string{"", "12345", current + "0", "abcdef", "      "} {
		if stepNs, ok := VerifyTOTPStep(secret, code, base); ok {
			t.Errorf("the malformed code %q reported the step %d", code, stepNs)
		}
	}
	if _, ok := VerifyTOTPStep("not base32!!", current, base); ok {
		t.Error("a code was accepted for an undecodable secret")
	}
}
