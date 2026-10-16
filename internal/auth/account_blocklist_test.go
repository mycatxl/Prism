package auth

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// The account dimension of the login blocklist.
//
// The address bucket caps how fast one machine may guess. An attempt that sends
// one guess per address never fills it, which is what the account bucket is for:
// it counts the failures of one account wherever they came from, sharing the
// limiter - and so the threshold, the window and the block - with the address
// bucket.

// accountBucket is a blocklist with the semantics of the production limiter:
// per-key counters, a threshold that starts a block, and a Reset that clears a
// key. It exists so these tests can read the account key directly.
type accountBucket struct {
	mu           sync.Mutex
	threshold    int
	blockFor     time.Duration
	counts       map[string]int
	blockedUntil map[string]time.Time
}

var (
	_ Blocklist         = (*accountBucket)(nil)
	_ BlocklistResetter = (*accountBucket)(nil)
)

func newAccountBucket(threshold int, blockFor time.Duration) *accountBucket {
	return &accountBucket{
		threshold:    threshold,
		blockFor:     blockFor,
		counts:       make(map[string]int),
		blockedUntil: make(map[string]time.Time),
	}
}

func (b *accountBucket) Blocked(key string) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.blockedUntil[key]
	if !ok {
		return false, 0
	}
	remaining := time.Until(until)
	if remaining <= 0 {
		return false, 0
	}
	return true, remaining
}

func (b *accountBucket) RecordFailure(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.counts[key]++
	if b.counts[key] >= b.threshold {
		b.blockedUntil[key] = time.Now().Add(b.blockFor)
	}
}

func (b *accountBucket) Reset(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.counts, key)
	delete(b.blockedUntil, key)
}

// count returns how many failures are recorded against key.
func (b *accountBucket) count(key string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.counts[key]
}

// enrolTOTP stores a second factor on an account and returns the plain secret.
func enrolTOTP(t *testing.T, store *fakeStore, svc *Service, userID string) string {
	t.Helper()
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	sealed, err := EncryptTOTPSecret(svc.Pepper, secret)
	if err != nil {
		t.Fatalf("EncryptTOTPSecret: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.users[userID].TOTPSecretEnc = sealed
	return secret
}

// wrongTOTPCode returns a code that cannot verify secret.
func wrongTOTPCode(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := TOTPCode(secret, at)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	if code == "000000" {
		return "111111"
	}
	return "000000"
}

// TestService_LoginBlocksTheAccountBucketAcrossAddresses covers why the account
// bucket exists: a guess run that sends one attempt per address fills no address
// bucket, and without the account bucket it would keep guessing at full speed.
// Once the account bucket is full the account is refused even when the password
// it is offered is the right one.
func TestService_LoginBlocksTheAccountBucketAcrossAddresses(t *testing.T) {
	const (
		rightPassword = "correct horse battery staple"
		threshold     = 10
		attempts      = 12
	)

	store := newFakeStore()
	store.addUser(t, "admin", rightPassword)
	svc := newLoginService(t, store)
	bucket := newAccountBucket(threshold, time.Minute)
	svc.Blocklist = bucket

	for i := 0; i < attempts; i++ {
		// Every attempt comes from its own address, so no address bucket ever
		// fills: whatever blocks below can only be the account bucket.
		clientKey := "203.0.113." + strconv.Itoa(i+1)
		_, err := svc.Login("admin", "not-the-password", "", clientKey, clientKey, "test-agent")
		if i < threshold {
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("attempt %d: err = %v, want ErrInvalidCredentials", i+1, err)
			}
			continue
		}
		if !errors.Is(err, ErrTooManyAttempts) {
			t.Fatalf("attempt %d: err = %v, want ErrTooManyAttempts", i+1, err)
		}
	}

	if blocked, _ := bucket.Blocked("203.0.113.1"); blocked {
		t.Fatal("an address bucket filled, so this run was not distributed after all")
	}
	if blocked, _ := bucket.Blocked(accountFailureKey("u-admin")); !blocked {
		t.Fatal("the account bucket did not fill")
	}

	// The right password, from an address with a clean record, is still
	// refused: the block belongs to the account.
	if _, err := svc.Login("admin", rightPassword, "", "198.51.100.9", "198.51.100.9", "test-agent"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("login with the right password from a fresh address: err = %v, want ErrTooManyAttempts", err)
	}
}

// TestService_LoginCountsAWrongSecondFactorAgainstTheAccount covers the second
// factor specifically: a distributed run against a code is one guess per
// address, so only the account bucket sees it.
func TestService_LoginCountsAWrongSecondFactorAgainstTheAccount(t *testing.T) {
	const (
		rightPassword = "correct horse battery staple"
		threshold     = 10
	)

	store := newFakeStore()
	user := store.addUser(t, "admin", rightPassword)
	svc := newLoginService(t, store)
	bucket := newAccountBucket(threshold, time.Minute)
	svc.Blocklist = bucket
	secret := enrolTOTP(t, store, svc, user.ID)

	for i := 0; i < threshold; i++ {
		clientKey := "198.51.100." + strconv.Itoa(i+1)
		code := wrongTOTPCode(t, secret, time.Unix(0, svc.nowNs()))
		_, err := svc.Login("admin", rightPassword, code, clientKey, clientKey, "test-agent")
		if !errors.Is(err, ErrTOTPInvalid) {
			t.Fatalf("attempt %d: err = %v, want ErrTOTPInvalid", i+1, err)
		}
	}

	if got := bucket.count(accountFailureKey(user.ID)); got != threshold {
		t.Fatalf("the account bucket counted %d second-factor failures, want %d", got, threshold)
	}

	// The right password and the right code, from a fresh address, are refused:
	// the guesses were against the account, not against an address.
	code, err := TOTPCode(secret, time.Unix(0, svc.nowNs()))
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	if _, err := svc.Login("admin", rightPassword, code, "192.0.2.99", "192.0.2.99", "test-agent"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("login with the right password and code: err = %v, want ErrTooManyAttempts", err)
	}
}

// TestService_LoginClearsTheAccountBucketOnSuccess pins the other half: the
// owner signing in proves the failures were somebody else's, so the account
// counter starts again instead of locking the owner out of their own console.
func TestService_LoginClearsTheAccountBucketOnSuccess(t *testing.T) {
	const rightPassword = "correct horse battery staple"

	store := newFakeStore()
	store.addUser(t, "admin", rightPassword)
	svc := newLoginService(t, store)
	// A threshold of 10 with 9 failures on each side of the successful login:
	// the counter can only stay below the threshold if the login cleared it.
	bucket := newAccountBucket(10, time.Minute)
	svc.Blocklist = bucket

	fail := func(offset, count int) {
		t.Helper()
		for i := 0; i < count; i++ {
			clientKey := "203.0.113." + strconv.Itoa(offset+i)
			_, err := svc.Login("admin", "not-the-password", "", clientKey, clientKey, "test-agent")
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("failure %d of %d: err = %v, want ErrInvalidCredentials", i+1, count, err)
			}
		}
	}

	fail(0, 9)
	if _, err := svc.Login("admin", rightPassword, "", "198.51.100.1", "198.51.100.1", "test-agent"); err != nil {
		t.Fatalf("login with the right password: %v", err)
	}
	if got := bucket.count(accountFailureKey("u-admin")); got != 0 {
		t.Fatalf("the account bucket still holds %d failures after a successful login", got)
	}

	fail(100, 9)
	if blocked, _ := bucket.Blocked(accountFailureKey("u-admin")); blocked {
		t.Fatal("the failures from before the successful login were still being counted")
	}
}

// TestService_LoginDoesNotCountTheFirstStepOfATwoStepLogin pins a deliberate
// difference from the address bucket: a missing code is the ordinary first step
// of a two-step login, not a failure. Counting it would let every second-factor
// user lock themselves out by typing their password.
func TestService_LoginDoesNotCountTheFirstStepOfATwoStepLogin(t *testing.T) {
	const rightPassword = "correct horse battery staple"

	store := newFakeStore()
	user := store.addUser(t, "admin", rightPassword)
	svc := newLoginService(t, store)
	// Deliberately small: any first step that reached the counter would block
	// the account well before the loop is over.
	bucket := newAccountBucket(3, time.Minute)
	svc.Blocklist = bucket
	secret := enrolTOTP(t, store, svc, user.ID)

	for i := 0; i < 6; i++ {
		clientKey := "203.0.113." + strconv.Itoa(i+1)
		_, err := svc.Login("admin", rightPassword, "", clientKey, clientKey, "test-agent")
		if !errors.Is(err, ErrTOTPRequired) {
			t.Fatalf("first step %d: err = %v, want ErrTOTPRequired", i+1, err)
		}
	}
	if got := bucket.count(accountFailureKey(user.ID)); got != 0 {
		t.Fatalf("the first step of a two-step login was counted %d times against the account", got)
	}

	code, err := TOTPCode(secret, time.Unix(0, svc.nowNs()))
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	if _, err := svc.Login("admin", rightPassword, code, "192.0.2.99", "192.0.2.99", "test-agent"); err != nil {
		t.Fatalf("login with the right password and code: %v", err)
	}
}

// TestService_LoginWorksWithABlocklistThatCannotReset covers the optional half
// of the contract: clearing is an addition to Blocklist, not a requirement of
// it, and a login whose credential is right must succeed without it.
func TestService_LoginWorksWithABlocklistThatCannotReset(t *testing.T) {
	store := newFakeStore()
	store.addUser(t, "admin", "correct horse battery staple")
	svc := newLoginService(t, store)
	// fakeBlocklist (service_test.go) implements Blocklist and nothing else.
	svc.Blocklist = &fakeBlocklist{}

	if _, err := svc.Login("admin", "correct horse battery staple", "", "203.0.113.9", "203.0.113.9", "test-agent"); err != nil {
		t.Fatalf("login with a blocklist that cannot clear: %v", err)
	}
}
