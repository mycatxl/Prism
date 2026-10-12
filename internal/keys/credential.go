package keys

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// The access key credential is rendered to the operator as pk_<id>_<secret>.
//
// The ID is the public half: it is what the data plane looks the key up by, and
// it appears in logs and in the console. The secret is the private half and is
// only ever shown once, at creation. Only HMAC(pepper subkey, secret) is
// stored, so a database read does not yield a usable credential.
//
// Both halves are lowercase hex, which is what makes the shape unambiguous:
// hex cannot contain the "_" separator, so splitting the credential needs no
// escaping and a secret can never be mistaken for the start of another part.
const (
	// CredentialPrefix marks a credential as an access key rather than the
	// transitional global proxy token.
	CredentialPrefix = "pk_"
	// IDHexLength is the length of the hex-encoded key id (a UUIDv7 without
	// its dashes).
	IDHexLength = 32
	// SecretHexLength is the length of the hex-encoded secret (32 random
	// bytes).
	SecretHexLength = 64
	secretBytes     = SecretHexLength / 2
)

// GenerateCredential returns a new key id and secret.
//
// The ID is a UUIDv7 without dashes, so keys sort by creation time and the
// column is a plain text primary key. The secret is 32 random bytes; it is
// returned once and never stored.
func GenerateCredential() (id, secret string, err error) {
	uuidV7, err := uuid.NewV7()
	if err != nil {
		return "", "", fmt.Errorf("keys: generate id: %w", err)
	}
	id = strings.ReplaceAll(uuidV7.String(), "-", "")

	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("keys: generate secret: %w", err)
	}
	secret = hex.EncodeToString(buf)
	return id, secret, nil
}

// FormatCredential renders the credential an operator is shown and a client
// sends: pk_<id>_<secret>.
func FormatCredential(id, secret string) string {
	return CredentialPrefix + id + "_" + secret
}

// ParseCredential splits a presented credential into its two halves.
//
// It reports ok=false for anything that is not shaped like an access key. That
// answer is deliberately structural rather than cryptographic: it tells the
// caller whether the credential is even a candidate for key verification, so a
// deployment still using the global proxy token can fall through to comparing
// that token instead. Whether the halves name a real key is decided later, by
// VerifyKey, which fails closed.
func ParseCredential(credential string) (id, secret string, ok bool) {
	if !strings.HasPrefix(credential, CredentialPrefix) {
		return "", "", false
	}
	// SplitN with three parts keeps a malformed secret from being silently
	// truncated: the second half is everything after the second underscore.
	parts := strings.SplitN(credential, "_", 3)
	if len(parts) != 3 {
		return "", "", false
	}
	id, secret = parts[1], parts[2]
	// The lengths are checked as well as the alphabet. That keeps the answer a
	// statement about shape alone, and it keeps a value that is merely hex-ish
	// from being mistaken for a key: the transitional global token is an
	// arbitrary string, and the narrower this test is, the less chance it has
	// of catching that token by accident.
	if len(id) != IDHexLength || len(secret) != SecretHexLength {
		return "", "", false
	}
	if !isLowerHex(id) || !isLowerHex(secret) {
		return "", "", false
	}
	return id, secret, true
}

// IsCredentialShape reports whether a value is shaped like an access key
// without checking whether one exists. The request dispatcher uses it to tell a
// key-bearing path segment from the transitional global token before the
// credential reaches any verifier.
func IsCredentialShape(value string) bool {
	_, _, ok := ParseCredential(value)
	return ok
}

// isLowerHex reports whether every byte is a lowercase hex digit. Uppercase is
// rejected rather than normalized so a credential has exactly one valid
// spelling.
func isLowerHex(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}
