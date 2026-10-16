package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// TOTP parameters.
//
// These are the values every authenticator app assumes, so nothing here is
// configurable: a non-standard step or digit count would produce codes that
// Google Authenticator, 1Password and the rest would all disagree with, and the
// user cannot be asked which convention their app follows.
const (
	// totpDigits is the length of a generated code.
	totpDigits = 6
	// totpStep is the length of one code's validity window.
	totpStep = 30 * time.Second
	// totpSecretBytes is the size of a generated secret. 20 bytes is what
	// SHA-1 based TOTP uses and what the authenticator apps expect in a
	// Base32 payload.
	totpSecretBytes = 20
	// totpSkew is how many steps either side of the current one are accepted.
	//
	// One step of tolerance each way covers ordinary clock drift between the
	// server and a phone without meaningfully widening the window an attacker
	// guessing a code has to hit: five accepted steps out of a million possible
	// codes is still a one-in-200000 chance per attempt.
	totpSkew = 1
)

// base32NoPadding is the encoding authenticator apps use in otpauth:// URIs:
// RFC 4648 Base32, uppercase, without padding.
var base32NoPadding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a new random secret, encoded the way it is shown
// to a user and stored.
func GenerateTOTPSecret() (string, error) {
	buf := make([]byte, totpSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: generate TOTP secret: %w", err)
	}
	return base32NoPadding.EncodeToString(buf), nil
}

// TOTPCode returns the code for a secret at a given time.
//
// It implements RFC 6238 over HMAC-SHA1, which is what authenticator apps
// implement. SHA-1 is not a weakness here despite its reputation: the digest is
// used as a keyed MAC over a counter, not as a collision-resistant hash, and no
// known attack on SHA-1 helps recover the secret or predict the next code.
func TOTPCode(secret string, at time.Time) (string, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return "", err
	}
	counter := uint64(at.Unix() / int64(totpStep.Seconds()))

	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)

	// Dynamic truncation, RFC 4226 section 5.3: the low nibble of the last byte
	// picks the four bytes to read, which is what keeps every bit of the digest
	// contributing to the result.
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff

	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, value%mod), nil
}

// VerifyTOTP reports whether code is valid for secret at the given time, within
// the accepted step tolerance.
//
// The comparison is constant time so a caller cannot be turned into an oracle
// that reveals a correct prefix. Every accepted step is checked even after one
// matches, so the time taken does not depend on which step matched.
func VerifyTOTP(secret, code string, at time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}
	match := 0
	for offset := -totpSkew; offset <= totpSkew; offset++ {
		candidate, err := TOTPCode(secret, at.Add(time.Duration(offset)*totpStep))
		if err != nil {
			return false
		}
		match |= subtle.ConstantTimeCompare([]byte(candidate), []byte(code))
	}
	return match == 1
}

// VerifyTOTPStep reports whether code is valid for secret at the given time and,
// when it is, the start of the step the code belonged to.
//
// The step is the identity of the code - TOTPCode is a pure function of the
// secret and the step - which is what makes a code single-use: a caller can spend
// the step it matched (a compare-and-set against the account's
// totp_last_used_ns) so the same code cannot authorise a second login while it is
// still inside the accepted tolerance.
//
// The step returned is the newest match. Every offset is still checked after one
// matches, exactly as VerifyTOTP does, so the work done does not depend on which
// step matched, and the selection between them is arithmetic rather than a
// branch.
func VerifyTOTPStep(secret, code string, at time.Time) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, false
	}
	var matchedStep int64
	match := 0
	for offset := -totpSkew; offset <= totpSkew; offset++ {
		moment := at.Add(time.Duration(offset) * totpStep)
		candidate, err := TOTPCode(secret, moment)
		if err != nil {
			return 0, false
		}
		hit := subtle.ConstantTimeCompare([]byte(candidate), []byte(code))
		match |= hit
		// Last match wins, without branching on the comparison: hit is 0 or 1,
		// so the second term is zero for every step that did not match.
		matchedStep = matchedStep*(1-int64(hit)) + totpStepStartNs(moment)*int64(hit)
	}
	if match != 1 {
		return 0, false
	}
	return matchedStep, true
}

// totpStepStartNs returns the Unix nanoseconds at which the step containing
// moment began, which is the value a spent step is recorded as.
//
// It is derived from the whole second: TOTP counts steps of thirty seconds from
// the epoch, so a time truncated to the second lands in the same step as the
// sub-second part around it. The division truncates towards zero, and only
// pre-epoch times would feel that.
func totpStepStartNs(moment time.Time) int64 {
	const stepSeconds = int64(totpStep / time.Second)
	return moment.Unix() / stepSeconds * stepSeconds * int64(time.Second)
}

// TOTPURI renders the otpauth:// URI an authenticator app scans.
//
// The issuer and account are parameters rather than being read from
// configuration, because the caller is the only one that knows which user is
// enrolling.
func TOTPURI(issuer, account, secret string) string {
	label := account
	if issuer != "" {
		label = issuer + ":" + account
	}
	return fmt.Sprintf(
		"otpauth://totp/%s?secret=%s&issuer=%s&algorithm=SHA1&digits=%d&period=%d",
		urlEscape(label),
		secret,
		urlEscape(issuer),
		totpDigits,
		int(totpStep.Seconds()),
	)
}

// decodeTOTPSecret accepts a secret with or without padding and in either case,
// because users copy them out of apps that all display them slightly
// differently.
func decodeTOTPSecret(secret string) ([]byte, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	normalized = strings.TrimRight(normalized, "=")
	key, err := base32NoPadding.DecodeString(normalized)
	if err != nil {
		return nil, fmt.Errorf("auth: decode TOTP secret: %w", err)
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("auth: TOTP secret is empty")
	}
	return key, nil
}

// EncryptTOTPSecret seals a secret under the pepper's TOTP subkey.
//
// The plaintext secret is never stored: what goes into totp_secret_enc is the
// AES-GCM output, so a database read does not yield a second factor that can be
// cloned. The subkey is derived from the pepper with its own info string, so it
// is not the same key that hashes access-key secrets - a leak of one does not
// weaken the other.
func EncryptTOTPSecret(pepper *Pepper, secret string) ([]byte, error) {
	gcm, err := totpGCM(pepper)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("auth: generate TOTP nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, []byte(secret), nil), nil
}

// DecryptTOTPSecret opens a secret sealed by EncryptTOTPSecret.
//
// It fails closed on anything malformed: a secret that cannot be authenticated
// is not a secret, and treating it as the empty string would silently disable
// the second factor for that user.
func DecryptTOTPSecret(pepper *Pepper, sealed []byte) (string, error) {
	gcm, err := totpGCM(pepper)
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", fmt.Errorf("auth: TOTP secret is too short to be sealed")
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("auth: open TOTP secret: %w", err)
	}
	return string(plaintext), nil
}

// totpGCM builds the AEAD the TOTP secret is sealed with.
func totpGCM(pepper *Pepper) (cipher.AEAD, error) {
	if pepper == nil {
		return nil, fmt.Errorf("auth: no pepper, so no TOTP key")
	}
	key := pepper.TOTPKey()
	if len(key) == 0 {
		return nil, fmt.Errorf("auth: no TOTP key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("auth: TOTP cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("auth: TOTP AEAD: %w", err)
	}
	return gcm, nil
}

// urlEscape percent-encodes the characters that would otherwise break an
// otpauth:// URI. It is deliberately minimal: the label holds a username and an
// issuer name, and encoding the whole thing would make the URI unreadable for
// no benefit.
func urlEscape(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
		case c == '-' || c == '_' || c == '.' || c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
