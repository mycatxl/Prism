package keys

import (
	"strings"
	"testing"
)

func TestGenerateCredentialShape(t *testing.T) {
	id, secret, err := GenerateCredential()
	if err != nil {
		t.Fatalf("GenerateCredential: %v", err)
	}

	if len(id) != IDHexLength {
		t.Fatalf("id length = %d, want %d (%q)", len(id), IDHexLength, id)
	}
	if len(secret) != SecretHexLength {
		t.Fatalf("secret length = %d, want %d", len(secret), SecretHexLength)
	}
	if !isLowerHex(id) {
		t.Fatalf("id %q is not lowercase hex", id)
	}
	if !isLowerHex(secret) {
		t.Fatalf("secret is not lowercase hex")
	}
	// The shape test rests on hex never containing the separator.
	if strings.Contains(secret, "_") || strings.Contains(id, "_") {
		t.Fatalf("generated halves must not contain the separator")
	}
	if !strings.HasPrefix(FormatCredential(id, secret), CredentialPrefix) {
		t.Fatalf("formatted credential lost its prefix")
	}
}

func TestGenerateCredentialIsUnique(t *testing.T) {
	seenID := make(map[string]bool)
	seenSecret := make(map[string]bool)
	for i := 0; i < 64; i++ {
		id, secret, err := GenerateCredential()
		if err != nil {
			t.Fatalf("GenerateCredential: %v", err)
		}
		if seenID[id] {
			t.Fatalf("duplicate id %q at iteration %d", id, i)
		}
		if seenSecret[secret] {
			t.Fatalf("duplicate secret at iteration %d", i)
		}
		seenID[id] = true
		seenSecret[secret] = true
	}
}

func TestCredentialRoundTrip(t *testing.T) {
	id, secret, err := GenerateCredential()
	if err != nil {
		t.Fatalf("GenerateCredential: %v", err)
	}

	gotID, gotSecret, ok := ParseCredential(FormatCredential(id, secret))
	if !ok {
		t.Fatalf("ParseCredential rejected a credential it just formatted")
	}
	if gotID != id || gotSecret != secret {
		t.Fatalf("round trip = (%q, %q), want (%q, %q)", gotID, gotSecret, id, secret)
	}
}

func TestParseCredentialRejectsForeignValues(t *testing.T) {
	id, secret, err := GenerateCredential()
	if err != nil {
		t.Fatalf("GenerateCredential: %v", err)
	}
	valid := FormatCredential(id, secret)

	cases := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"plain token", "supersecrettoken"},
		{"missing prefix", id + "_" + secret},
		{"wrong prefix", "sk_" + id + "_" + secret},
		{"uppercase prefix", "PK_" + id + "_" + secret},
		{"no separator", CredentialPrefix + id},
		{"no secret", CredentialPrefix + id + "_"},
		{"no id", CredentialPrefix + "_" + secret},
		{"id too short", FormatCredential(id[:IDHexLength-1], secret)},
		{"id too long", FormatCredential(id+"a", secret)},
		{"secret too short", FormatCredential(id, secret[:SecretHexLength-1])},
		{"secret too long", FormatCredential(id, secret+"a")},
		{"uppercase id", FormatCredential(strings.ToUpper(id), secret)},
		{"uppercase secret", FormatCredential(id, strings.ToUpper(secret))},
		{"non hex id", FormatCredential(strings.Repeat("z", IDHexLength), secret)},
		{"non hex secret", FormatCredential(id, strings.Repeat("g", SecretHexLength))},
		{"token that merely starts with pk_", CredentialPrefix + "admin-token-value"},
		{"valid credential plus suffix", valid + "extra"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := ParseCredential(tc.value); ok {
				t.Fatalf("ParseCredential(%q) reported ok, want false", tc.value)
			}
			if IsCredentialShape(tc.value) {
				t.Fatalf("IsCredentialShape(%q) = true, want false", tc.value)
			}
		})
	}
}

func TestParseCredentialKeepsSecretIntact(t *testing.T) {
	// A secret that itself contained an underscore would be silently split by
	// the shape test, so the parser must never accept one. Splitting with
	// SplitN(3) means the third part is everything that follows the second
	// underscore; the length check is what rejects such a value.
	id, secret, err := GenerateCredential()
	if err != nil {
		t.Fatalf("GenerateCredential: %v", err)
	}
	withExtra := FormatCredential(id, secret) + "_" + secret
	if _, _, ok := ParseCredential(withExtra); ok {
		t.Fatalf("a credential with a third separator was accepted")
	}
}

func TestIsCredentialShapeMatchesParseCredential(t *testing.T) {
	id, secret, err := GenerateCredential()
	if err != nil {
		t.Fatalf("GenerateCredential: %v", err)
	}
	value := FormatCredential(id, secret)
	if !IsCredentialShape(value) {
		t.Fatalf("IsCredentialShape(%q) = false, want true", value)
	}

	parsedID, parsedSecret, ok := ParseCredential(value)
	if !ok {
		t.Fatalf("ParseCredential(%q) failed", value)
	}
	if parsedID != id || parsedSecret != secret {
		t.Fatalf("parsed halves do not match the generated ones")
	}
}

func TestIsLowerHex(t *testing.T) {
	cases := map[string]bool{
		"":                 true,
		"0123456789abcdef": true,
		"deadBEEF":         false,
		"0x12":             false,
		"12-34":            false,
		" ":                false,
	}
	for value, want := range cases {
		if got := isLowerHex(value); got != want {
			t.Fatalf("isLowerHex(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestCredentialConstants(t *testing.T) {
	if CredentialPrefix != "pk_" {
		t.Fatalf("CredentialPrefix = %q, want pk_", CredentialPrefix)
	}
	if IDHexLength != 32 {
		t.Fatalf("IDHexLength = %d, want 32", IDHexLength)
	}
	if SecretHexLength != 64 {
		t.Fatalf("SecretHexLength = %d, want 64", SecretHexLength)
	}
	if secretBytes*2 != SecretHexLength {
		t.Fatalf("secretBytes = %d does not encode to %d hex chars", secretBytes, SecretHexLength)
	}
}
