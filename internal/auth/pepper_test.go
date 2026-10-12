package auth

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNewPepper_DerivesDistinctSubkeys(t *testing.T) {
	raw, err := GeneratePepper()
	if err != nil {
		t.Fatalf("GeneratePepper: %v", err)
	}
	if len(raw) != pepperLength {
		t.Fatalf("pepper length: got %d, want %d", len(raw), pepperLength)
	}

	pepper, err := NewPepper(raw)
	if err != nil {
		t.Fatalf("NewPepper: %v", err)
	}

	// The two subkeys must not be the same: the HMAC subkey verifies key
	// secrets and the TOTP subkey encrypts second factors.
	if bytes.Equal(pepper.TOTPKey(), pepper.keyHMAC) {
		t.Fatal("the HMAC and TOTP subkeys must differ")
	}
	// Neither may equal the pepper itself.
	if bytes.Equal(pepper.TOTPKey(), raw) || bytes.Equal(pepper.keyHMAC, raw) {
		t.Fatal("a subkey must not equal the raw pepper")
	}

	// Derivation is deterministic for the same pepper.
	again, err := NewPepper(raw)
	if err != nil {
		t.Fatalf("NewPepper second time: %v", err)
	}
	if !bytes.Equal(again.keyHMAC, pepper.keyHMAC) {
		t.Error("the HMAC subkey must be stable across loads")
	}
	if !bytes.Equal(again.TOTPKey(), pepper.TOTPKey()) {
		t.Error("the TOTP subkey must be stable across loads")
	}

	// A different pepper gives different subkeys.
	other, err := GeneratePepper()
	if err != nil {
		t.Fatalf("GeneratePepper: %v", err)
	}
	otherPepper, err := NewPepper(other)
	if err != nil {
		t.Fatalf("NewPepper: %v", err)
	}
	if bytes.Equal(otherPepper.keyHMAC, pepper.keyHMAC) {
		t.Error("different peppers must derive different subkeys")
	}
}

func TestNewPepper_RejectsEmpty(t *testing.T) {
	if _, err := NewPepper(nil); !errors.Is(err, ErrPepperMissing) {
		t.Fatalf("got %v, want ErrPepperMissing", err)
	}
	if _, err := NewPepper([]byte{}); !errors.Is(err, ErrPepperMissing) {
		t.Fatalf("got %v, want ErrPepperMissing", err)
	}
}

func TestPepper_HashAndVerifySecret(t *testing.T) {
	raw, _ := GeneratePepper()
	pepper, err := NewPepper(raw)
	if err != nil {
		t.Fatalf("NewPepper: %v", err)
	}

	const secret = "s3cret-value"
	stored := pepper.HashSecret(secret)
	if len(stored) != 32 {
		t.Fatalf("hash length: got %d, want 32", len(stored))
	}
	if !pepper.VerifySecret(stored, secret) {
		t.Error("the right secret must verify")
	}
	if pepper.VerifySecret(stored, "wrong") {
		t.Error("a wrong secret must not verify")
	}
	if pepper.VerifySecret(stored, "") {
		t.Error("an empty secret must not verify")
	}
	if pepper.VerifySecret(nil, secret) {
		t.Error("an empty stored hash must not verify")
	}

	// The same secret hashed twice is identical: this is an HMAC, not a salted
	// hash, because the data plane compares it on every connection.
	if !bytes.Equal(pepper.HashSecret(secret), stored) {
		t.Error("HashSecret must be deterministic")
	}

	// A different pepper must not verify the same secret.
	other, _ := GeneratePepper()
	otherPepper, err := NewPepper(other)
	if err != nil {
		t.Fatalf("NewPepper: %v", err)
	}
	if otherPepper.VerifySecret(stored, secret) {
		t.Error("a secret must not verify under a different pepper")
	}
}

// The accessors must hand out copies so a caller cannot mutate the keys.
func TestPepper_AccessorsCopy(t *testing.T) {
	raw, _ := GeneratePepper()
	pepper, err := NewPepper(raw)
	if err != nil {
		t.Fatalf("NewPepper: %v", err)
	}

	rawCopy := pepper.Raw()
	rawCopy[0] ^= 0xFF
	if bytes.Equal(pepper.Raw(), rawCopy) {
		t.Error("Raw must return a copy")
	}

	totpCopy := pepper.TOTPKey()
	totpCopy[0] ^= 0xFF
	if bytes.Equal(pepper.TOTPKey(), totpCopy) {
		t.Error("TOTPKey must return a copy")
	}
}

func TestPepper_NilIsSafe(t *testing.T) {
	var pepper *Pepper
	if got := pepper.HashSecret("x"); got != nil {
		t.Errorf("HashSecret on nil: got %v, want nil", got)
	}
	if pepper.VerifySecret([]byte{1}, "x") {
		t.Error("VerifySecret on nil must not verify")
	}
	if pepper.TOTPKey() != nil {
		t.Error("TOTPKey on nil must be nil")
	}
	if pepper.Raw() != nil {
		t.Error("Raw on nil must be nil")
	}
}

// --- file handling ---

func TestWritePepperFile_ThenLoad(t *testing.T) {
	dir := t.TempDir()

	raw, err := GeneratePepper()
	if err != nil {
		t.Fatalf("GeneratePepper: %v", err)
	}
	path, err := WritePepperFile(dir, raw)
	if err != nil {
		t.Fatalf("WritePepperFile: %v", err)
	}
	if filepath.Base(path) != PepperFileName {
		t.Errorf("pepper file name: got %q, want %q", filepath.Base(path), PepperFileName)
	}

	// The file must be private.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat pepper: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("pepper file mode: got %o, want 600", perm)
	}

	// It must load back to the same pepper.
	loaded, err := LoadPepper(dir)
	if err != nil {
		t.Fatalf("LoadPepper: %v", err)
	}
	if !bytes.Equal(loaded.Raw(), raw) {
		t.Error("the loaded pepper differs from the written one")
	}
}

// A second write must fail rather than silently replace the pepper, which would
// invalidate every key already issued.
func TestWritePepperFile_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	raw, _ := GeneratePepper()

	if _, err := WritePepperFile(dir, raw); err != nil {
		t.Fatalf("first write: %v", err)
	}

	other, _ := GeneratePepper()
	_, err := WritePepperFile(dir, other)
	if err == nil {
		t.Fatal("overwriting an existing pepper must fail")
	}

	// The original must be intact.
	loaded, err := LoadPepper(dir)
	if err != nil {
		t.Fatalf("LoadPepper: %v", err)
	}
	if !bytes.Equal(loaded.Raw(), raw) {
		t.Error("a refused write must not change the stored pepper")
	}
}

func TestWritePepperFile_RejectsEmptyInputs(t *testing.T) {
	dir := t.TempDir()
	if _, err := WritePepperFile("", []byte("x")); err == nil {
		t.Error("an empty state directory must be rejected")
	}
	if _, err := WritePepperFile(dir, nil); err == nil {
		t.Error("an empty pepper must be rejected")
	}
}

func TestLoadPepper_Missing(t *testing.T) {
	// Make sure the environment cannot satisfy the load.
	t.Setenv(PepperEnvVar, "")

	if _, err := LoadPepper(t.TempDir()); !errors.Is(err, ErrPepperMissing) {
		t.Fatalf("a directory without a pepper: got %v, want ErrPepperMissing", err)
	}
	if _, err := LoadPepper(""); !errors.Is(err, ErrPepperMissing) {
		t.Fatalf("an empty directory: got %v, want ErrPepperMissing", err)
	}
}

// The environment variable wins over the file, which is how an operator
// supplies the pepper from a secret manager instead of the disk.
func TestLoadPepper_EnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	fileRaw, _ := GeneratePepper()
	if _, err := WritePepperFile(dir, fileRaw); err != nil {
		t.Fatalf("WritePepperFile: %v", err)
	}

	t.Setenv(PepperEnvVar, "from-the-environment")
	loaded, err := LoadPepper(dir)
	if err != nil {
		t.Fatalf("LoadPepper: %v", err)
	}
	if !bytes.Equal(loaded.Raw(), []byte("from-the-environment")) {
		t.Errorf("the environment must win over the file, got %q", loaded.Raw())
	}
}

// A pepper file written by hand as plain text is accepted, so an operator can
// supply one without base64 encoding it.
func TestLoadPepper_PlainTextFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, PepperFileName), []byte("plain-text-pepper\n"), 0o600); err != nil {
		t.Fatalf("write pepper: %v", err)
	}

	loaded, err := LoadPepper(dir)
	if err != nil {
		t.Fatalf("LoadPepper: %v", err)
	}
	if !bytes.Equal(loaded.Raw(), []byte("plain-text-pepper")) {
		t.Errorf("plain text pepper: got %q", loaded.Raw())
	}
}
