package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/hkdf"
)

// The key pepper is the single secret every stored key is derived from: access
// key secrets are verified with an HMAC under one subkey, and TOTP secrets are
// encrypted with a key derived from another. The subkeys never overlap.
//
// Losing the pepper invalidates every access key and every TOTP secret at once,
// which is why the file must be backed up alongside the databases.
const (
	// PepperFileName is the file `prism init` writes into the state directory.
	PepperFileName = "pepper"
	// PepperEnvVar overrides the file when it is set.
	PepperEnvVar = "PRISM_KEY_PEPPER"
	// pepperLength is the raw pepper size in bytes.
	pepperLength = 32

	// HKDF info strings. Distinct strings give unrelated subkeys.
	hkdfInfoKeyHMAC = "prism-key-hmac-v1"
	hkdfInfoTOTP    = "prism-totp-v1"
)

// ErrPepperMissing is returned when no pepper is configured, from either the
// environment or the state directory.
var ErrPepperMissing = errors.New("auth: key pepper is not configured")

// Pepper holds the loaded pepper and the subkeys derived from it.
//
// The subkeys are derived once at load time so the hot path never runs HKDF.
type Pepper struct {
	raw     []byte
	keyHMAC []byte
	totpKey []byte
}

// GeneratePepper returns a fresh random pepper.
func GeneratePepper() ([]byte, error) {
	buf := make([]byte, pepperLength)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("auth: read pepper: %w", err)
	}
	return buf, nil
}

// NewPepper derives the subkeys from a raw pepper.
func NewPepper(raw []byte) (*Pepper, error) {
	if len(raw) == 0 {
		return nil, ErrPepperMissing
	}
	keyHMAC, err := deriveSubkey(raw, hkdfInfoKeyHMAC)
	if err != nil {
		return nil, err
	}
	totpKey, err := deriveSubkey(raw, hkdfInfoTOTP)
	if err != nil {
		return nil, err
	}
	return &Pepper{
		raw:     append([]byte(nil), raw...),
		keyHMAC: keyHMAC,
		totpKey: totpKey,
	}, nil
}

// deriveSubkey runs HKDF-SHA256 over the pepper with the given info string.
func deriveSubkey(raw []byte, info string) ([]byte, error) {
	reader := hkdf.New(sha256.New, raw, nil, []byte(info))
	out := make([]byte, 32)
	if _, err := reader.Read(out); err != nil {
		return nil, fmt.Errorf("auth: derive subkey %q: %w", info, err)
	}
	return out, nil
}

// HashSecret returns the HMAC-SHA256 of a key secret under the key subkey.
// This is what gets stored: the plaintext secret never reaches the database.
func (p *Pepper) HashSecret(secret string) []byte {
	if p == nil {
		return nil
	}
	mac := hmac.New(sha256.New, p.keyHMAC)
	mac.Write([]byte(secret))
	return mac.Sum(nil)
}

// VerifySecret compares secret against a stored hash in constant time.
func (p *Pepper) VerifySecret(stored []byte, secret string) bool {
	if p == nil || len(stored) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare(p.HashSecret(secret), stored) == 1
}

// TOTPKey returns the subkey used to encrypt TOTP secrets. It is a copy: the
// caller cannot mutate the derived key.
func (p *Pepper) TOTPKey() []byte {
	if p == nil {
		return nil
	}
	return append([]byte(nil), p.totpKey...)
}

// Raw returns a copy of the pepper itself, for writing it back to disk.
func (p *Pepper) Raw() []byte {
	if p == nil {
		return nil
	}
	return append([]byte(nil), p.raw...)
}

// LoadPepper resolves the pepper the same way the rest of the configuration
// resolves secrets: an environment variable wins over the file, and the file
// lives in the state directory.
//
// A missing pepper is reported as ErrPepperMissing so the caller can tell it
// apart from a read failure.
func LoadPepper(stateDir string) (*Pepper, error) {
	if raw := strings.TrimSpace(os.Getenv(PepperEnvVar)); raw != "" {
		return NewPepper([]byte(raw))
	}
	if strings.TrimSpace(stateDir) == "" {
		return nil, ErrPepperMissing
	}

	path := filepath.Join(stateDir, PepperFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrPepperMissing
	}
	if err != nil {
		return nil, fmt.Errorf("auth: read pepper file: %w", err)
	}

	// The file holds base64 so a pepper with arbitrary bytes survives a text
	// round-trip; a file written by hand as plain text is accepted too.
	raw, err := decodePepper(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, err
	}
	return NewPepper(raw)
}

// WritePepperFile writes the pepper into stateDir with mode 0600.
//
// It refuses to overwrite an existing file: a second initialization would
// otherwise silently invalidate every key already issued.
func WritePepperFile(stateDir string, raw []byte) (string, error) {
	if strings.TrimSpace(stateDir) == "" {
		return "", errors.New("auth: state directory must not be empty")
	}
	if len(raw) == 0 {
		return "", errors.New("auth: pepper must not be empty")
	}
	path := filepath.Join(stateDir, PepperFileName)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return path, fmt.Errorf("auth: %s already exists; refusing to overwrite it", path)
		}
		return path, fmt.Errorf("auth: create pepper file: %w", err)
	}
	if _, err := f.WriteString(encodePepper(raw) + "\n"); err != nil {
		_ = f.Close()
		return path, fmt.Errorf("auth: write pepper file: %w", err)
	}
	if err := f.Close(); err != nil {
		return path, fmt.Errorf("auth: close pepper file: %w", err)
	}
	// os.OpenFile applies the mode only on creation, so it is set again in case
	// the umask narrowed it.
	if err := os.Chmod(path, 0o600); err != nil {
		return path, fmt.Errorf("auth: secure pepper file: %w", err)
	}
	return path, nil
}

// encodePepper renders the pepper as base64 text.
func encodePepper(raw []byte) string {
	return base64.RawStdEncoding.EncodeToString(raw)
}

// decodePepper accepts either a base64 pepper or a plain-text one.
func decodePepper(value string) ([]byte, error) {
	if value == "" {
		return nil, ErrPepperMissing
	}
	if raw, err := base64.RawStdEncoding.DecodeString(value); err == nil && len(raw) > 0 {
		return raw, nil
	}
	// Not base64: treat the file content as the pepper itself.
	return []byte(value), nil
}
