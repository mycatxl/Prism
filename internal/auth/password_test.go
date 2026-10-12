package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestHashPassword_VerifyRoundTrip(t *testing.T) {
	const password = "correct horse battery staple"

	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := VerifyPassword(encoded, password); err != nil {
		t.Fatalf("VerifyPassword with the right password: %v", err)
	}

	err = VerifyPassword(encoded, "wrong password")
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("VerifyPassword with a wrong password: got %v, want ErrMismatch", err)
	}
}

// The hash must carry its own parameters so the cost can be raised later
// without invalidating rows already stored.
func TestHashPassword_PHCFormat(t *testing.T) {
	encoded, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if !strings.HasPrefix(encoded, "$argon2id$") {
		t.Errorf("hash must be an argon2id PHC string, got %q", encoded)
	}
	if !strings.Contains(encoded, "m=65536,t=3,p=2") {
		t.Errorf("hash must record the cost parameters, got %q", encoded)
	}

	params, salt, key, err := decodeHash(encoded)
	if err != nil {
		t.Fatalf("decodeHash: %v", err)
	}
	if params.memoryKiB != 64*1024 || params.time != 3 || params.threads != 2 {
		t.Errorf("parameters did not round-trip: %+v", params)
	}
	if len(salt) != argonSaltLength {
		t.Errorf("salt length: got %d, want %d", len(salt), argonSaltLength)
	}
	if len(key) != argonKeyLength {
		t.Errorf("key length: got %d, want %d", len(key), argonKeyLength)
	}
}

// A fresh salt per call keeps two identical passwords from looking identical in
// the table.
func TestHashPassword_SaltIsRandom(t *testing.T) {
	const password = "same password"

	first, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if first == second {
		t.Fatal("hashing the same password twice produced the same string")
	}
	// Both must still verify.
	if err := VerifyPassword(first, password); err != nil {
		t.Errorf("first hash: %v", err)
	}
	if err := VerifyPassword(second, password); err != nil {
		t.Errorf("second hash: %v", err)
	}
}

func TestHashPassword_RejectsEmpty(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Fatal("an empty password must be rejected")
	}
}

// A corrupt row must be reported as such, not as a failed login, so the caller
// can tell the two apart.
func TestVerifyPassword_MalformedHash(t *testing.T) {
	cases := []struct {
		name    string
		encoded string
	}{
		{"empty", ""},
		{"not argon2", "$bcrypt$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"},
		{"wrong version", "$argon2id$v=16$m=65536,t=3,p=2$c2FsdA$aGFzaA"},
		{"too few fields", "$argon2id$v=19$m=65536,t=3,p=2"},
		{"bad parameters", "$argon2id$v=19$m=x,t=y,p=z$c2FsdA$aGFzaA"},
		{"zero cost", "$argon2id$v=19$m=0,t=0,p=0$c2FsdA$aGFzaA"},
		{"bad base64 salt", "$argon2id$v=19$m=65536,t=3,p=2$!!!$aGFzaA"},
		{"bad base64 key", "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$!!!"},
		{"empty salt", "$argon2id$v=19$m=65536,t=3,p=2$$aGFzaA"},
		{"empty key", "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$"},
	}
	for _, tc := range cases {
		err := VerifyPassword(tc.encoded, "whatever")
		if !errors.Is(err, ErrInvalidHash) {
			t.Errorf("%s: got %v, want ErrInvalidHash", tc.name, err)
		}
	}
}

// The verification cost must come from the stored hash, not from the current
// constants, or raising the cost would lock everyone out.
func TestVerifyPassword_UsesStoredParameters(t *testing.T) {
	// A hash produced with weaker parameters than the current constants.
	const encoded = "$argon2id$v=19$m=8192,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g"

	// It must not panic or reject on the parameters alone: the mismatch here is
	// the password, so ErrMismatch is the expected outcome.
	err := VerifyPassword(encoded, "wrong")
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("got %v, want ErrMismatch", err)
	}
}

// A password containing separator characters must not confuse the parser.
func TestHashPassword_UnusualPasswords(t *testing.T) {
	passwords := []string{
		"$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"with$dollar$signs",
		"with spaces and\ttabs",
		"unicode 密码 🔐",
		strings.Repeat("a", 1024),
	}
	for _, password := range passwords {
		encoded, err := HashPassword(password)
		if err != nil {
			t.Fatalf("HashPassword(%q): %v", password, err)
		}
		if err := VerifyPassword(encoded, password); err != nil {
			t.Errorf("VerifyPassword(%q): %v", password, err)
		}
		// A near miss must still fail.
		if err := VerifyPassword(encoded, password+"x"); !errors.Is(err, ErrMismatch) {
			t.Errorf("VerifyPassword(%q + x): got %v, want ErrMismatch", password, err)
		}
	}
}
