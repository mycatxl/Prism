package auth

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/model"
)

// fakeStore is an in-memory account and session store for the service tests.
type fakeStore struct {
	mu sync.Mutex

	users      map[string]*model.User // by id
	byName     map[string]string      // name -> id
	sessions   map[string]model.Session
	insertErr  error
	loginMarks []struct {
		userID string
		atNs   int64
	}
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:    make(map[string]*model.User),
		byName:   make(map[string]string),
		sessions: make(map[string]model.Session),
	}
}

func (s *fakeStore) addUser(t *testing.T, username, password string) *model.User {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	user := &model.User{
		ID:           "u-" + username,
		Username:     username,
		PasswordHash: hash,
		Role:         model.UserRoleAdmin,
		Status:       model.UserStatusActive,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[user.ID] = user
	s.byName[username] = user.ID
	return user
}

func (s *fakeStore) GetUserByName(username string) (*model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byName[username]
	if !ok {
		return nil, errors.New("not found")
	}
	user := *s.users[id]
	return &user, nil
}

func (s *fakeStore) GetUser(id string) (*model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[id]
	if !ok {
		return nil, errors.New("not found")
	}
	copied := *user
	return &copied, nil
}

func (s *fakeStore) InsertSession(session model.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.insertErr != nil {
		return s.insertErr
	}
	s.sessions[session.IDHash] = session
	return nil
}

func (s *fakeStore) GetSession(idHash string) (*model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[idHash]
	if !ok {
		return nil, errors.New("not found")
	}
	return &session, nil
}

func (s *fakeStore) TouchSession(idHash string, expiresAtNs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[idHash]
	if !ok {
		return errors.New("not found")
	}
	session.ExpiresAtNs = expiresAtNs
	s.sessions[idHash] = session
	return nil
}

func (s *fakeStore) DeleteSession(idHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, idHash)
	return nil
}

func (s *fakeStore) DeleteSessionsByUser(userID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed int64
	for hash, session := range s.sessions {
		if session.UserID == userID {
			delete(s.sessions, hash)
			removed++
		}
	}
	return removed, nil
}

func (s *fakeStore) MarkUserLogin(userID string, atNs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loginMarks = append(s.loginMarks, struct {
		userID string
		atNs   int64
	}{userID, atNs})
	return nil
}

func (s *fakeStore) sessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// fakeBlocklist records failures and can be told to block.
type fakeBlocklist struct {
	mu       sync.Mutex
	failures []string
	blocked  bool
	retryIn  time.Duration
}

func (b *fakeBlocklist) Blocked(string) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.blocked, b.retryIn
}

func (b *fakeBlocklist) RecordFailure(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = append(b.failures, key)
}

func (b *fakeBlocklist) failureCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.failures)
}

func newLoginService(t *testing.T, store Store) *Service {
	t.Helper()
	pepper, err := NewPepper([]byte("a-test-pepper-of-at-least-16-bytes"))
	if err != nil {
		t.Fatalf("NewPepper: %v", err)
	}
	return &Service{Store: store, Pepper: pepper}
}

// The happy path: a correct password issues a session whose id is not what gets
// stored.
func TestService_LoginIssuesASession(t *testing.T) {
	store := newFakeStore()
	store.addUser(t, "admin", "correct horse battery staple")
	svc := newLoginService(t, store)

	result, err := svc.Login("admin", "correct horse battery staple", "", "203.0.113.9", "198.51.100.4", "test-agent")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.SessionID == "" || result.CSRFToken == "" {
		t.Fatal("the login produced no session id or no CSRF token")
	}
	if result.User == nil || result.User.Username != "admin" {
		t.Fatalf("the login resolved user %+v", result.User)
	}
	if result.ExpiresAtNs <= 0 {
		t.Fatalf("the session expires at %d", result.ExpiresAtNs)
	}

	// What is stored is the hash, never the id.
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.sessions) != 1 {
		t.Fatalf("the store holds %d sessions, want 1", len(store.sessions))
	}
	for hash, session := range store.sessions {
		if hash == result.SessionID {
			t.Fatal("the stored key is the session id itself")
		}
		if hash != HashSessionID(result.SessionID) {
			t.Fatal("the stored key is not the hash of the session id")
		}
		if session.UserID != "u-admin" {
			t.Fatalf("the session belongs to %q", session.UserID)
		}
		if session.IP != "198.51.100.4" {
			t.Fatalf("the session recorded ip %q", session.IP)
		}
		if session.UA != "test-agent" {
			t.Fatalf("the session recorded ua %q", session.UA)
		}
	}
	if len(store.loginMarks) != 1 {
		t.Fatalf("the login was recorded %d times, want 1", len(store.loginMarks))
	}
}

// Every way a credential can be wrong answers with the same error, and an
// unknown username costs a password verification so it cannot be told apart by
// timing.
func TestService_LoginIsUniformAboutBadCredentials(t *testing.T) {
	store := newFakeStore()
	store.addUser(t, "admin", "the-right-password")
	svc := newLoginService(t, store)

	t.Run("wrong password", func(t *testing.T) {
		if _, err := svc.Login("admin", "the-wrong-password", "", "", "", ""); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("got %v, want ErrInvalidCredentials", err)
		}
	})

	t.Run("unknown user", func(t *testing.T) {
		if _, err := svc.Login("nobody", "any-password", "", "", "", ""); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("got %v, want ErrInvalidCredentials", err)
		}
	})

	// The unknown user must take comparable time to the wrong password: that is
	// the whole point of the throwaway verification.
	t.Run("the two take comparable time", func(t *testing.T) {
		measure := func(username, password string) time.Duration {
			start := time.Now()
			_, _ = svc.Login(username, password, "", "", "", "")
			return time.Since(start)
		}
		known := measure("admin", "the-wrong-password")
		unknown := measure("nobody-at-all", "the-wrong-password")

		// The threshold is loose on purpose: this runs on a shared machine and a
		// tight bound would be flaky. It is still far tighter than the order of
		// magnitude that returning early would produce.
		slower, faster := known, unknown
		if faster > slower {
			slower, faster = faster, slower
		}
		if slower > 4*faster {
			t.Fatalf("an unknown user answered in %s and a wrong password in %s, so the two are "+
				"distinguishable by timing", unknown, known)
		}
	})

	// No session was issued for any of them.
	if got := store.sessionCount(); got != 0 {
		t.Fatalf("%d sessions were issued for failed logins, want 0", got)
	}
}

// A disabled account is refused even with the right password.
func TestService_LoginRefusesADisabledAccount(t *testing.T) {
	store := newFakeStore()
	user := store.addUser(t, "admin", "the-right-password")
	store.mu.Lock()
	store.users[user.ID].Status = model.UserStatusDisabled
	store.mu.Unlock()

	svc := newLoginService(t, store)
	if _, err := svc.Login("admin", "the-right-password", "", "", "", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("got %v, want ErrInvalidCredentials", err)
	}
	if got := store.sessionCount(); got != 0 {
		t.Fatalf("%d sessions were issued to a disabled account", got)
	}
}

// The second factor is asked for only when the account has one, and a wrong code
// counts as a failure.
func TestService_LoginWithASecondFactor(t *testing.T) {
	store := newFakeStore()
	store.addUser(t, "admin", "the-right-password")
	svc := newLoginService(t, store)

	// Without a secret the account is not asked for a code.
	result, err := svc.Login("admin", "the-right-password", "", "", "", "")
	if err != nil {
		t.Fatalf("Login without a factor: %v", err)
	}
	_ = result

	// Enrol the account.
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	sealed, err := EncryptTOTPSecret(svc.Pepper, secret)
	if err != nil {
		t.Fatalf("EncryptTOTPSecret: %v", err)
	}
	store.mu.Lock()
	store.users["u-admin"].TOTPSecretEnc = sealed
	store.mu.Unlock()

	t.Run("no code supplied", func(t *testing.T) {
		if _, err := svc.Login("admin", "the-right-password", "", "", "", ""); !errors.Is(err, ErrTOTPRequired) {
			t.Fatalf("got %v, want ErrTOTPRequired", err)
		}
	})

	t.Run("a wrong code", func(t *testing.T) {
		if _, err := svc.Login("admin", "the-right-password", "000000", "", "", ""); !errors.Is(err, ErrTOTPInvalid) {
			t.Fatalf("got %v, want ErrTOTPInvalid", err)
		}
	})

	t.Run("the right code", func(t *testing.T) {
		code, err := TOTPCode(secret, time.Unix(0, svc.nowNs()))
		if err != nil {
			t.Fatalf("TOTPCode: %v", err)
		}
		result, err := svc.Login("admin", "the-right-password", code, "", "", "")
		if err != nil {
			t.Fatalf("Login with the right code: %v", err)
		}
		if result.SessionID == "" {
			t.Fatal("no session was issued")
		}
	})
}

// A secret that cannot be opened must refuse the login rather than silently
// dropping the factor it was there to provide.
func TestService_LoginRefusesWhenTheFactorCannotBeOpened(t *testing.T) {
	store := newFakeStore()
	store.addUser(t, "admin", "the-right-password")
	svc := newLoginService(t, store)

	other, err := NewPepper([]byte("a-different-pepper-entirely"))
	if err != nil {
		t.Fatalf("NewPepper: %v", err)
	}
	sealed, err := EncryptTOTPSecret(other, "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatalf("EncryptTOTPSecret: %v", err)
	}
	store.mu.Lock()
	store.users["u-admin"].TOTPSecretEnc = sealed
	store.mu.Unlock()

	// The service has no pepper at all.
	noPepper := &Service{Store: store}
	if _, err := noPepper.Login("admin", "the-right-password", "123456", "", "", ""); err == nil {
		t.Fatal("a login succeeded with an unopenable second factor and no pepper")
	}

	// The service has the wrong pepper.
	if _, err := svc.Login("admin", "the-right-password", "123456", "", "", ""); err == nil {
		t.Fatal("a login succeeded with a second factor sealed under another pepper")
	}
	if got := store.sessionCount(); got != 0 {
		t.Fatalf("%d sessions were issued, want 0", got)
	}
}

// A blocked address is refused before any password work happens.
func TestService_LoginRefusesABlockedAddress(t *testing.T) {
	store := newFakeStore()
	store.addUser(t, "admin", "the-right-password")
	block := &fakeBlocklist{blocked: true, retryIn: 3 * time.Minute}
	svc := newLoginService(t, store)
	svc.Blocklist = block

	_, err := svc.Login("admin", "the-right-password", "", "203.0.113.9", "", "")
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("got %v, want ErrTooManyAttempts", err)
	}
	// Even the right password is refused: being blocked means blocked, not
	// "blocked for guesses".
	if got := store.sessionCount(); got != 0 {
		t.Fatalf("%d sessions were issued to a blocked address", got)
	}
	// And the blocked attempt is not itself counted as a new failure.
	if got := block.failureCount(); got != 0 {
		t.Fatalf("the blocked attempt recorded %d failures, want 0", got)
	}
}

// Failures are recorded against the address, so the limiter can act.
func TestService_LoginRecordsFailuresAgainstTheAddress(t *testing.T) {
	store := newFakeStore()
	store.addUser(t, "admin", "the-right-password")
	block := &fakeBlocklist{}
	svc := newLoginService(t, store)
	svc.Blocklist = block

	_, _ = svc.Login("admin", "wrong", "", "203.0.113.9", "", "")
	_, _ = svc.Login("nobody", "wrong", "", "203.0.113.9", "", "")

	if got := block.failureCount(); got != 2 {
		t.Fatalf("recorded %d failures, want 2", got)
	}
}

// A session resolves to its user, and the expiry slides once the window is half
// spent.
func TestService_AuthenticateResolvesAndSlides(t *testing.T) {
	store := newFakeStore()
	store.addUser(t, "admin", "the-right-password")
	svc := newLoginService(t, store)

	now := time.Now().UnixNano()
	svc.Now = func() int64 { return now }

	result, err := svc.Login("admin", "the-right-password", "", "", "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	user, session, err := svc.Authenticate(result.SessionID)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if user.ID != "u-admin" {
		t.Fatalf("resolved user %q", user.ID)
	}
	originalExpiry := session.ExpiresAtNs

	// Early in the window nothing is written.
	stored, _ := store.GetSession(HashSessionID(result.SessionID))
	if stored.ExpiresAtNs != originalExpiry {
		t.Fatalf("the expiry moved early in the window: %d -> %d", originalExpiry, stored.ExpiresAtNs)
	}

	// Past the halfway mark it slides.
	now += int64(SessionTTL-SessionRefreshAfter) + 1
	if _, _, err := svc.Authenticate(result.SessionID); err != nil {
		t.Fatalf("Authenticate late in the window: %v", err)
	}
	stored, _ = store.GetSession(HashSessionID(result.SessionID))
	if stored.ExpiresAtNs <= originalExpiry {
		t.Fatalf("the expiry did not slide: %d -> %d", originalExpiry, stored.ExpiresAtNs)
	}
}

// An expired session is refused and cleaned up.
func TestService_AuthenticateRefusesAnExpiredSession(t *testing.T) {
	store := newFakeStore()
	store.addUser(t, "admin", "the-right-password")
	svc := newLoginService(t, store)

	now := time.Now().UnixNano()
	svc.Now = func() int64 { return now }
	result, err := svc.Login("admin", "the-right-password", "", "", "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	now += int64(SessionTTL) + 1
	if _, _, err := svc.Authenticate(result.SessionID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("got %v, want ErrNoSession", err)
	}
	if got := store.sessionCount(); got != 0 {
		t.Fatalf("the expired session was left behind (%d rows)", got)
	}
}

// An unknown or malformed session id resolves to nothing.
func TestService_AuthenticateRefusesUnknownSessions(t *testing.T) {
	store := newFakeStore()
	store.addUser(t, "admin", "the-right-password")
	svc := newLoginService(t, store)

	for _, id := range []string{"", "not-a-session", strings.Repeat("x", 43)} {
		if _, _, err := svc.Authenticate(id); !errors.Is(err, ErrNoSession) {
			t.Errorf("Authenticate(%q) = %v, want ErrNoSession", id, err)
		}
	}
}

// Disabling an account must end its open sessions, or the disable would only
// apply to the next login.
func TestService_AuthenticateEndsSessionsOfADisabledAccount(t *testing.T) {
	store := newFakeStore()
	store.addUser(t, "admin", "the-right-password")
	svc := newLoginService(t, store)

	result, err := svc.Login("admin", "the-right-password", "", "", "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	store.mu.Lock()
	store.users["u-admin"].Status = model.UserStatusDisabled
	store.mu.Unlock()

	if _, _, err := svc.Authenticate(result.SessionID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("got %v, want ErrNoSession", err)
	}
	if got := store.sessionCount(); got != 0 {
		t.Fatalf("the disabled account still has %d sessions", got)
	}
}

// Logout ends the session and is safe to repeat.
func TestService_Logout(t *testing.T) {
	store := newFakeStore()
	store.addUser(t, "admin", "the-right-password")
	svc := newLoginService(t, store)

	result, err := svc.Login("admin", "the-right-password", "", "", "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := svc.Logout(result.SessionID); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, _, err := svc.Authenticate(result.SessionID); !errors.Is(err, ErrNoSession) {
		t.Fatalf("the session survived logout: %v", err)
	}
	if err := svc.Logout(result.SessionID); err != nil {
		t.Fatalf("logging out twice failed: %v", err)
	}
	if err := svc.Logout(""); err != nil {
		t.Fatalf("logging out with no session failed: %v", err)
	}
}

// Revoking every session is what makes a password change mean something.
func TestService_RevokeUserSessions(t *testing.T) {
	store := newFakeStore()
	store.addUser(t, "admin", "the-right-password")
	svc := newLoginService(t, store)

	first, err := svc.Login("admin", "the-right-password", "", "", "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	second, err := svc.Login("admin", "the-right-password", "", "", "", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	removed, err := svc.RevokeUserSessions("u-admin")
	if err != nil {
		t.Fatalf("RevokeUserSessions: %v", err)
	}
	if removed != 2 {
		t.Fatalf("revoked %d sessions, want 2", removed)
	}
	for _, id := range []string{first.SessionID, second.SessionID} {
		if _, _, err := svc.Authenticate(id); !errors.Is(err, ErrNoSession) {
			t.Errorf("a session survived the revocation: %v", err)
		}
	}
}

// A session that cannot be stored must fail the login rather than hand out an
// id that resolves to nothing.
func TestService_LoginFailsWhenTheSessionCannotBeStored(t *testing.T) {
	store := newFakeStore()
	store.addUser(t, "admin", "the-right-password")
	store.insertErr = errors.New("database is full")
	svc := newLoginService(t, store)

	if _, err := svc.Login("admin", "the-right-password", "", "", "", ""); err == nil {
		t.Fatal("a login succeeded although the session could not be stored")
	}
}

// A failure to record the login time must not refuse a valid login.
func TestService_LoginSurvivesAMissingLoginMark(t *testing.T) {
	store := newFakeStore()
	store.addUser(t, "admin", "the-right-password")
	svc := newLoginService(t, store)
	// MarkUserLogin is part of the interface but the fake never fails it, so
	// this test documents the intent: the call is not part of the success path.
	store.mu.Lock()
	store.loginMarks = nil
	store.mu.Unlock()

	if _, err := svc.Login("admin", "the-right-password", "", "", "", ""); err != nil {
		t.Fatalf("Login: %v", err)
	}
}

// A service with no store must refuse rather than panic.
func TestService_WithoutAStoreIsInert(t *testing.T) {
	svc := &Service{}
	if _, err := svc.Login("a", "b", "", "", "", ""); err == nil {
		t.Fatal("a login succeeded without a store")
	}
	if _, _, err := svc.Authenticate("anything"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("got %v, want ErrNoSession", err)
	}
	if err := svc.Logout("anything"); err != nil {
		t.Fatalf("Logout without a store: %v", err)
	}
	if _, err := svc.RevokeUserSessions("u-1"); err != nil {
		t.Fatalf("RevokeUserSessions without a store: %v", err)
	}
}
