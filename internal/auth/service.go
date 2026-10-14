package auth

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"prism/internal/model"
)

// Errors a login can fail with. They are distinct so the console can react
// differently - ask for a second factor, or show a "try again later" - without
// the response ever saying which half of a credential was wrong.
var (
	// ErrInvalidCredentials covers an unknown username, a wrong password and a
	// disabled account whose password was nevertheless correct. The caller must
	// not distinguish them to the client: doing so turns the login form into a
	// way to enumerate accounts.
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	// ErrTOTPRequired is returned when the account has a second factor and none
	// was supplied. It is not a credential failure: the console should prompt
	// for the code rather than count this as a bad attempt.
	ErrTOTPRequired = errors.New("auth: a second factor is required")
	// ErrTOTPInvalid is a supplied second factor that did not verify.
	ErrTOTPInvalid = errors.New("auth: the second factor is invalid")
	// ErrTooManyAttempts is returned while an address is blocked after too many
	// failures.
	ErrTooManyAttempts = errors.New("auth: too many failed attempts")
	// ErrNoSession is returned for a missing, expired or revoked session.
	ErrNoSession = errors.New("auth: no valid session")
)

// Store is the persistence the session service needs.
//
// It mirrors the user and session repositories without naming their concrete
// type, so the service can be tested against a fake and so the auth package
// keeps depending on the domain types rather than on a database handle.
type Store interface {
	// GetUserByName finds an account. A missing account is an error.
	GetUserByName(username string) (*model.User, error)
	// GetUser finds an account by id, which is how a session resolves to its
	// owner.
	GetUser(id string) (*model.User, error)
	// InsertSession persists a new session row.
	InsertSession(s model.Session) error
	// GetSession finds a session by its stored hash. A missing or expired row
	// is an error; the caller decides which.
	GetSession(idHash string) (*model.Session, error)
	// TouchSession pushes a session's expiry back.
	TouchSession(idHash string, expiresAtNs int64) error
	// DeleteSession removes one session. Removing one that is already gone is
	// not an error: logout has to be idempotent.
	DeleteSession(idHash string) error
	// DeleteSessionsByUser removes every session of one user and reports how
	// many there were.
	DeleteSessionsByUser(userID string) (int64, error)
	// MarkUserLogin records a successful login.
	MarkUserLogin(userID string, atNs int64) error
}

// Blocklist decides whether an address has failed too often.
//
// It is an interface so the session service does not depend on the HTTP rate
// limiter, and so a test can drive the blocked path directly.
type Blocklist interface {
	// Blocked reports whether key is currently blocked, and for how much
	// longer.
	Blocked(key string) (bool, time.Duration)
	// RecordFailure counts one failed attempt for key.
	RecordFailure(key string)
}

// Service authenticates console users and manages their sessions.
type Service struct {
	// Store is the account and session persistence. Required.
	Store Store
	// Pepper seals and opens TOTP secrets. A deployment without one simply
	// cannot use a second factor.
	Pepper *Pepper
	// Blocklist rate limits failed attempts by client address. Nil disables it.
	Blocklist Blocklist
	// Now is the clock, in Unix nanoseconds. Nil uses the wall clock.
	Now func() int64
	// Logf receives messages about rejected and accepted logins. Nil silences
	// them. It never receives a password or a session id.
	Logf func(format string, args ...any)
}

func (s *Service) nowNs() int64 {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UnixNano()
}

func (s *Service) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// LoginResult is what a successful login produces.
type LoginResult struct {
	// SessionID is the raw session id. It is returned once, here, and only its
	// hash is stored.
	SessionID string
	// CSRFToken is the double-submit token to hand the page.
	CSRFToken string
	// User is the authenticated account.
	User *model.User
	// ExpiresAtNs is when the session ends if nothing touches it.
	ExpiresAtNs int64
}

// Login authenticates a username, password and optional second factor.
//
// The order of the checks is deliberate at every step:
//
//   - the blocklist comes first, so a blocked address cannot make the server do
//     the expensive password work;
//   - the password is verified before the account's status is looked at, so a
//     disabled account cannot be discovered without knowing its password, which
//     would otherwise be an enumeration oracle;
//   - a missing account still pays for a password verification against a
//     throwaway hash, so "no such user" and "wrong password" take the same time.
//
// clientKey is the address the blocklist counts against. It can be empty, in
// which case the blocklist is not consulted.
func (s *Service) Login(username, password, totpCode, clientKey, ip, userAgent string) (LoginResult, error) {
	var result LoginResult
	if s == nil || s.Store == nil {
		return result, fmt.Errorf("auth: no store configured")
	}

	if s.Blocklist != nil && clientKey != "" {
		if blocked, retryAfter := s.Blocklist.Blocked(clientKey); blocked {
			s.logf("auth: login from %s refused, blocked for another %s", clientKey, retryAfter.Round(time.Second))
			return result, ErrTooManyAttempts
		}
	}

	user, err := s.Store.GetUserByName(username)
	if err != nil || user == nil {
		// Pay the same cost as a real verification. Without this, an unknown
		// username answers in microseconds and a known one in tens of
		// milliseconds, which is a reliable oracle from anywhere on the
		// network.
		burnPasswordVerification(password)
		s.recordFailure(clientKey)
		s.logf("auth: login refused for an unknown user")
		return result, ErrInvalidCredentials
	}

	if err := VerifyPassword(user.PasswordHash, password); err != nil {
		s.recordFailure(clientKey)
		s.logf("auth: login refused for user %s, wrong password", user.ID)
		return result, ErrInvalidCredentials
	}

	// The password was right. From here the failures can be specific, because
	// reaching them already required the password and so they leak nothing an
	// attacker does not have.
	if user.Status != model.UserStatusActive {
		s.logf("auth: login refused for user %s, the account is %s", user.ID, user.Status)
		return result, ErrInvalidCredentials
	}

	if err := s.verifySecondFactor(user, totpCode); err != nil {
		s.recordFailure(clientKey)
		return result, err
	}

	nowNs := s.nowNs()
	sessionID, err := NewSessionID()
	if err != nil {
		return result, err
	}
	csrfToken, err := NewCSRFToken()
	if err != nil {
		return result, err
	}

	record, err := NewSession(sessionID, user.ID, ip, userAgent, nowNs)
	if err != nil {
		return result, err
	}
	if err := s.Store.InsertSession(model.Session{
		IDHash:      record.IDHash,
		UserID:      record.UserID,
		CreatedAtNs: record.CreatedAtNs,
		ExpiresAtNs: record.ExpiresAtNs,
		IP:          record.IP,
		UA:          record.UA,
	}); err != nil {
		return result, fmt.Errorf("auth: store session: %w", err)
	}

	// A login timestamp that cannot be written must not fail the login: the
	// session is already valid, and refusing here would lock the user out over
	// a cosmetic column.
	if err := s.Store.MarkUserLogin(user.ID, nowNs); err != nil {
		s.logf("auth: could not record the login time for user %s: %v", user.ID, err)
	}

	s.logf("auth: user %s logged in from %s", user.ID, ip)
	return LoginResult{
		SessionID:   sessionID,
		CSRFToken:   csrfToken,
		User:        user,
		ExpiresAtNs: record.ExpiresAtNs,
	}, nil
}

// verifySecondFactor checks the account's second factor, if it has one.
//
// A user with a sealed secret must present a valid code. A user without one is
// not asked, which is what lets the second factor be optional per account.
//
// The secret is opened here rather than stored in the key state because it is
// needed once per login, not once per request: keeping it out of the data plane
// keeps a database read from yielding a factor that can be cloned.
func (s *Service) verifySecondFactor(user *model.User, code string) error {
	if len(user.TOTPSecretEnc) == 0 {
		return nil
	}
	if s.Pepper == nil {
		// The secret exists but cannot be opened. Refusing is the only safe
		// answer: treating an unopenable factor as absent would let a lost
		// pepper silently remove the protection it was there to provide.
		return fmt.Errorf("auth: user %s has a second factor but no pepper can open it", user.ID)
	}
	secret, err := DecryptTOTPSecret(s.Pepper, user.TOTPSecretEnc)
	if err != nil {
		return fmt.Errorf("auth: open the second factor for user %s: %w", user.ID, err)
	}
	if code == "" {
		return ErrTOTPRequired
	}
	if !VerifyTOTP(secret, code, time.Unix(0, s.nowNs())) {
		return ErrTOTPInvalid
	}
	return nil
}

func (s *Service) recordFailure(clientKey string) {
	if s.Blocklist == nil || clientKey == "" {
		return
	}
	s.Blocklist.RecordFailure(clientKey)
}

// Authenticate resolves a session id to the account behind it.
//
// It refreshes the expiry when the session is in the second half of its window,
// which is what makes the expiry sliding without turning every request into a
// write. A refresh that fails is logged and ignored: the session is still valid
// until its stored expiry, and failing the request would log the user out over
// a bookkeeping problem.
func (s *Service) Authenticate(sessionID string) (*model.User, *model.Session, error) {
	if s == nil || s.Store == nil || sessionID == "" {
		return nil, nil, ErrNoSession
	}

	nowNs := s.nowNs()
	session, err := s.Store.GetSession(HashSessionID(sessionID))
	if err != nil || session == nil {
		return nil, nil, ErrNoSession
	}
	if session.ExpiresAtNs <= nowNs {
		// Expired: remove it on the way past so the table does not accumulate
		// rows that the pruner would eventually collect anyway.
		if err := s.Store.DeleteSession(session.IDHash); err != nil {
			s.logf("auth: could not remove the expired session of user %s: %v", session.UserID, err)
		}
		return nil, nil, ErrNoSession
	}

	user, err := s.userByID(session.UserID)
	if err != nil || user == nil {
		return nil, nil, ErrNoSession
	}
	if user.Status != model.UserStatusActive {
		// The account was disabled while the session was open. Ending the
		// session here is what makes a disable immediate for the console too,
		// not only for the data plane.
		if _, err := s.Store.DeleteSessionsByUser(user.ID); err != nil {
			s.logf("auth: could not revoke the sessions of disabled user %s: %v", user.ID, err)
		}
		return nil, nil, ErrNoSession
	}

	if SessionNeedsRefresh(session.ExpiresAtNs, nowNs) {
		expiresAtNs := SessionExpiry(nowNs)
		if err := s.Store.TouchSession(session.IDHash, expiresAtNs); err != nil {
			s.logf("auth: could not extend the session of user %s: %v", user.ID, err)
		} else {
			session.ExpiresAtNs = expiresAtNs
		}
	}
	return user, session, nil
}

// Logout ends one session. It is idempotent: logging out twice is not an error.
func (s *Service) Logout(sessionID string) error {
	if s == nil || s.Store == nil || sessionID == "" {
		return nil
	}
	if err := s.Store.DeleteSession(HashSessionID(sessionID)); err != nil {
		return fmt.Errorf("auth: end session: %w", err)
	}
	return nil
}

// RevokeUserSessions ends every session of one user.
//
// It is what makes a password change and a disable mean something: without it,
// whoever already holds a cookie keeps it, and the operation the operator just
// performed has no effect on them.
func (s *Service) RevokeUserSessions(userID string) (int64, error) {
	if s == nil || s.Store == nil || userID == "" {
		return 0, nil
	}
	removed, err := s.Store.DeleteSessionsByUser(userID)
	if err != nil {
		return 0, fmt.Errorf("auth: revoke sessions: %w", err)
	}
	return removed, nil
}

// userByID resolves the owner of a session.
func (s *Service) userByID(userID string) (*model.User, error) {
	return s.Store.GetUser(userID)
}

// burnPasswordVerification does the work of a password check against a hash no
// password matches, and throws the answer away.
//
// Its only purpose is timing. An argon2id verification costs tens of
// milliseconds; returning early for an unknown username answers in microseconds
// instead, and that difference is measurable from anywhere on the network. It is
// what turns a login form into a way to test whether an account exists.
//
// The hash is built once, lazily. Building it per call would add the same cost
// to every login including the successful ones, and building it at package
// initialisation would make importing this package cost 64 MiB of argon2 work.
var (
	burnOnce sync.Once
	burnHash string
)

func burnPasswordVerification(password string) {
	burnOnce.Do(func() {
		// The failure is deliberately ignored: with no hash there is nothing to
		// verify against, and the call below then costs nothing. That is a
		// weaker timing guarantee, not a correctness problem.
		burnHash, _ = HashPassword("prism-timing-equaliser-not-a-real-password")
	})
	if burnHash == "" {
		return
	}
	_ = VerifyPassword(burnHash, password)
}
