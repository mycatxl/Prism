package tenant

import (
	"crypto/hkdf"
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
)

const (
	// PepperFileName lives in the state directory with mode 0600. Losing it
	// invalidates every access key and TOTP secret: back it up with state.db.
	PepperFileName = "key_pepper"
	// PepperEnv overrides the file when set.
	PepperEnv = "PRISM_KEY_PEPPER"

	pepperBytes    = 32
	minPepperBytes = 32

	hkdfInfoKeyHMAC = "prism-key-hmac-v1"
	hkdfInfoTOTP    = "prism-totp-v1"
	hkdfInfoSession = "prism-session-v1"
)

// Keys are independent subkeys derived from the pepper with HKDF-SHA256.
type Keys struct {
	KeyHMAC []byte // access-key secret hashing
	TOTP    []byte // AES-256-GCM key for users.totp_secret_enc
	Session []byte // session-ID hashing
}

// LoadOrCreatePepper returns the pepper from $PRISM_KEY_PEPPER (base64, at
// least 32 bytes decoded) or from <stateDir>/key_pepper, creating the file
// with 32 random bytes and mode 0600 when neither exists. An existing file
// with a looser mode is tightened.
func LoadOrCreatePepper(stateDir string) ([]byte, error) {
	if raw, ok := os.LookupEnv(PepperEnv); ok && strings.TrimSpace(raw) != "" {
		return decodePepper(raw, PepperEnv)
	}
	path := filepath.Join(stateDir, PepperFileName)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("chmod %s: %w", path, err)
		}
		return decodePepper(string(data), path)
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	pepper := make([]byte, pepperBytes)
	if _, err := rand.Read(pepper); err != nil {
		return nil, fmt.Errorf("generate pepper: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", stateDir, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) { // lost a creation race; use the winner
			return LoadOrCreatePepper(stateDir)
		}
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	_, werr := f.WriteString(base64.StdEncoding.EncodeToString(pepper) + "\n")
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return pepper, nil
}

func decodePepper(raw, source string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: pepper must be base64: %w", source, err)
	}
	if len(b) < minPepperBytes {
		return nil, fmt.Errorf("%s: pepper must decode to at least %d bytes", source, minPepperBytes)
	}
	return b, nil
}

// DeriveKeys derives the per-purpose subkeys from pepper.
func DeriveKeys(pepper []byte) (Keys, error) {
	derive := func(info string) ([]byte, error) {
		return hkdf.Key(sha256.New, pepper, nil, info, 32)
	}
	var k Keys
	var err error
	if k.KeyHMAC, err = derive(hkdfInfoKeyHMAC); err != nil {
		return Keys{}, err
	}
	if k.TOTP, err = derive(hkdfInfoTOTP); err != nil {
		return Keys{}, err
	}
	if k.Session, err = derive(hkdfInfoSession); err != nil {
		return Keys{}, err
	}
	return k, nil
}

// HashSecret returns HMAC-SHA256(key, secret), the value stored in
// access_keys.secret_hash.
func HashSecret(key []byte, secret string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(secret))
	return m.Sum(nil)
}

// VerifySecret compares secret against hash in constant time.
func VerifySecret(key []byte, secret string, hash []byte) bool {
	return subtle.ConstantTimeCompare(HashSecret(key, secret), hash) == 1
}
