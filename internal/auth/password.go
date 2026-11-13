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
	"strconv"
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
	// PHC parameters are stored with the hash so older rows can keep their
	// original cost. They are still untrusted input: a corrupt or tampered row
	// must not be able to make a login allocate arbitrary memory or run forever.
	argonMinMemoryKiB    = 8 * 1024
	argonMaxMemoryKiB    = argonMemoryKiB
	argonMaxTime         = argonTime
	argonMaxThreads      = argonThreads
	maxPasswordHashBytes = 1024
)

// ErrInvalidHash is returned when a stored hash is not a well-formed argon2id
// PHC string. It means the row is corrupt, not that the password was wrong.
var ErrInvalidHash = errors.New("auth: malformed password hash")

// ErrMismatch is returned when the password does not match the hash.
var ErrMismatch = errors.New("auth: password does not match")

// Argon2 is intentionally memory-hard. Bound concurrent work so a burst of
// valid-looking login or registration requests cannot multiply the 64 MiB cost
// until the process is killed before the failure limiter catches up.
var argonWorkSlots = make(chan struct{}, 4)

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
	key := deriveArgon2Key([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLength)
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
	got := deriveArgon2Key([]byte(password), salt, params.time, params.memoryKiB, params.threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

func deriveArgon2Key(password, salt []byte, timeCost, memoryKiB uint32, threads uint8, keyLength uint32) []byte {
	argonWorkSlots <- struct{}{}
	defer func() { <-argonWorkSlots }()
	return argon2.IDKey(password, salt, timeCost, memoryKiB, threads, keyLength)
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

	if len(encoded) == 0 || len(encoded) > maxPasswordHashBytes {
		return params, nil, nil, ErrInvalidHash
	}
	if !strings.HasPrefix(encoded, argon2idHashPrefix) {
		return params, nil, nil, ErrInvalidHash
	}
	// Split the leading "$" so the fields are: "", "argon2id", "v=..", "m=..",
	// salt, hash.
	fields := strings.Split(encoded, "$")
	if len(fields) != 6 {
		return params, nil, nil, ErrInvalidHash
	}

	version, err := parsePHCUint(fields[2], "v=", 32)
	if err != nil {
		return params, nil, nil, ErrInvalidHash
	}
	if version != uint64(argon2.Version) {
		return params, nil, nil, ErrInvalidHash
	}

	parts := strings.Split(fields[3], ",")
	if len(parts) != 3 {
		return params, nil, nil, ErrInvalidHash
	}
	memory, err := parsePHCUint(parts[0], "m=", 32)
	if err != nil {
		return params, nil, nil, ErrInvalidHash
	}
	passes, err := parsePHCUint(parts[1], "t=", 32)
	if err != nil {
		return params, nil, nil, ErrInvalidHash
	}
	threads, err := parsePHCUint(parts[2], "p=", 8)
	if err != nil {
		return params, nil, nil, ErrInvalidHash
	}
	params.memoryKiB = uint32(memory)
	params.time = uint32(passes)
	params.threads = uint8(threads)
	if params.memoryKiB < argonMinMemoryKiB || params.memoryKiB < 8*uint32(params.threads) ||
		params.memoryKiB > argonMaxMemoryKiB ||
		params.time == 0 || params.time > argonMaxTime ||
		params.threads == 0 || params.threads > argonMaxThreads {
		return params, nil, nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(fields[4])
	if err != nil || len(salt) != argonSaltLength {
		return params, nil, nil, ErrInvalidHash
	}
	key, err := base64.RawStdEncoding.DecodeString(fields[5])
	if err != nil || len(key) != argonKeyLength {
		return params, nil, nil, ErrInvalidHash
	}
	return params, salt, key, nil
}

// parsePHCUint parses one complete PHC parameter. fmt.Sscanf accepts a valid
// prefix (for example "m=65536,garbage"), which is unsafe for an untrusted
// database value because the caller would then verify a hash with parameters
// different from the value it appears to contain.
func parsePHCUint(field, prefix string, bitSize int) (uint64, error) {
	if !strings.HasPrefix(field, prefix) || len(field) == len(prefix) {
		return 0, ErrInvalidHash
	}
	return strconv.ParseUint(field[len(prefix):], 10, bitSize)
}

func passwordHashNeedsTimingPadding(encoded string) bool {
	params, _, _, err := decodeHash(encoded)
	if err != nil {
		return true
	}
	return params.memoryKiB < argonMemoryKiB || params.time < argonTime || params.threads < argonThreads
}
