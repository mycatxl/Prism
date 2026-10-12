// Package auth holds the credential primitives the console uses: password
// hashing, key secrets and the pepper they derive from.
//
// Passwords are stored as argon2id hashes in PHC string form, which is the
// format the reference implementation defines and which carries its own
// parameters, so the cost can be raised later without invalidating existing
// hashes.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id cost parameters. m is in KiB: 64 MiB, three passes, two lanes, which
// is the configuration the design calls for.
const (
	argonMemoryKiB  = 64 * 1024
	argonTime       = 3
	argonThreads    = 2
	argonSaltLength = 16
	argonKeyLength  = 32
)

// ErrInvalidHash is returned when a stored hash is not a well-formed argon2id
// PHC string. It means the row is corrupt, not that the password was wrong.
var ErrInvalidHash = errors.New("auth: malformed password hash")

// ErrMismatch is returned when the password does not match the hash.
var ErrMismatch = errors.New("auth: password does not match")

// argon2idHashPrefix identifies the only algorithm this package accepts.
const argon2idHashPrefix = "$argon2id$"

// HashPassword returns the argon2id PHC string for password.
//
// Every call uses a fresh random salt, so hashing the same password twice gives
// different strings and two users with the same password are not detectable
// from the table.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("auth: password must not be empty")
	}
	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLength)
	return encodeHash(salt, key), nil
}

// VerifyPassword reports whether password matches the encoded argon2id hash.
//
// A malformed hash returns ErrInvalidHash and a wrong password returns
// ErrMismatch, so the caller can tell a corrupt row from a failed login. The
// comparison is constant time.
func VerifyPassword(encoded, password string) error {
	params, salt, want, err := decodeHash(encoded)
	if err != nil {
		return err
	}
	got := argon2.IDKey([]byte(password), salt, params.time, params.memoryKiB, params.threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

// argonParams holds the cost parameters read back from a stored hash.
type argonParams struct {
	memoryKiB uint32
	time      uint32
	threads   uint8
}

// encodeHash renders the PHC string:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<b64 salt>$<b64 hash>
func encodeHash(salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
}

// decodeHash parses a PHC string and returns its parameters, salt and hash.
func decodeHash(encoded string) (argonParams, []byte, []byte, error) {
	var params argonParams

	if !strings.HasPrefix(encoded, argon2idHashPrefix) {
		return params, nil, nil, ErrInvalidHash
	}
	// Split the leading "$" so the fields are: "", "argon2id", "v=..", "m=..",
	// salt, hash.
	fields := strings.Split(encoded, "$")
	if len(fields) != 6 {
		return params, nil, nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(fields[2], "v=%d", &version); err != nil {
		return params, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return params, nil, nil, ErrInvalidHash
	}

	if _, err := fmt.Sscanf(fields[3], "m=%d,t=%d,p=%d", &params.memoryKiB, &params.time, &params.threads); err != nil {
		return params, nil, nil, ErrInvalidHash
	}
	if params.memoryKiB == 0 || params.time == 0 || params.threads == 0 {
		return params, nil, nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(fields[4])
	if err != nil || len(salt) == 0 {
		return params, nil, nil, ErrInvalidHash
	}
	key, err := base64.RawStdEncoding.DecodeString(fields[5])
	if err != nil || len(key) == 0 {
		return params, nil, nil, ErrInvalidHash
	}
	return params, salt, key, nil
}
