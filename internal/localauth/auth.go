// Package localauth provides a small, persistent local account and an
// in-memory web session store. It is intentionally independent of HTTP so the
// client and manager can choose their own cookie and handler policy.
package localauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	DefaultSessionTTL  = 12 * time.Hour
	DefaultMaxSessions = 256
	MaxUsernameBytes   = 256
	MaxPasswordBytes   = 4096
	sessionTokenBytes  = 32
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidSession     = errors.New("invalid or expired session")
	ErrInvalidAccount     = errors.New("username and password are required")
)

// Bootstrap is used only when the credential file does not exist. Callers own
// the policy for its values, including any product default.
type Bootstrap struct {
	Username   string
	Password   string
	MustChange bool
}

// Options configures runtime-only session behavior.
type Options struct {
	SessionTTL  time.Duration
	MaxSessions int
}

// Account is the non-secret persistent account state.
type Account struct {
	Username   string `json:"username"`
	MustChange bool   `json:"must_change"`
}

// Principal is returned for an authenticated session.
type Principal struct {
	Username   string    `json:"username"`
	MustChange bool      `json:"must_change"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Session contains a newly issued bearer token. Token is returned only when a
// session is created; the Store retains only its SHA-256 digest.
type Session struct {
	Token      string    `json:"-"`
	Username   string    `json:"username"`
	MustChange bool      `json:"must_change"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// CredentialUpdate replaces both local credentials. Password is processed
// byte-for-byte: leading and trailing whitespace are significant.
type CredentialUpdate struct {
	Username   string
	Password   string
	MustChange bool
}

type sessionRecord struct {
	username  string
	expiresAt time.Time
}

// Store persists one local account and keeps sessions in memory. A process
// restart intentionally invalidates all sessions. Store coordinates goroutines
// in one process; callers must give each process its own credential path.
type Store struct {
	mu sync.RWMutex

	path        string
	credential  credentialRecord
	generation  uint64
	sessions    map[[sha256.Size]byte]sessionRecord
	sessionTTL  time.Duration
	maxSessions int
	now         func() time.Time
	random      io.Reader
}

// Open loads an existing local account or atomically creates it from
// bootstrap. Bootstrap is ignored when a valid credential file already
// exists.
func Open(path string, bootstrap Bootstrap, options Options) (*Store, error) {
	if path == "" {
		return nil, errors.New("local auth credential path is required")
	}
	ttl := options.SessionTTL
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	maxSessions := options.MaxSessions
	if maxSessions <= 0 {
		maxSessions = DefaultMaxSessions
	}

	store := &Store{
		path:        path,
		sessions:    make(map[[sha256.Size]byte]sessionRecord),
		sessionTTL:  ttl,
		maxSessions: maxSessions,
		now:         time.Now,
		random:      rand.Reader,
	}
	record, found, err := loadCredential(path)
	if err != nil {
		return nil, err
	}
	if !found {
		if err := validateAccount(bootstrap.Username, bootstrap.Password); err != nil {
			return nil, fmt.Errorf("initialize local auth: %w", err)
		}
		record, err = newCredential(bootstrap.Username, bootstrap.Password, bootstrap.MustChange, store.random)
		if err != nil {
			return nil, fmt.Errorf("initialize local auth: %w", err)
		}
		if err := saveCredential(path, record); err != nil {
			return nil, fmt.Errorf("initialize local auth: %w", err)
		}
	}
	store.credential = record
	store.generation = 1
	return store, nil
}

// Status returns non-secret account state.
func (s *Store) Status() Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Account{Username: s.credential.Username, MustChange: s.credential.MustChange}
}

// VerifyCredentials validates credentials without creating a session.
func (s *Store) VerifyCredentials(username, password string) bool {
	record, generation := s.credentialSnapshot()
	valid := record.verify(username, password)
	if !valid {
		return false
	}

	// Do not accept a credential which was replaced while its relatively
	// expensive password derivation was running.
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation == generation
}

// Login validates credentials and returns a new random, expiring session.
func (s *Store) Login(username, password string) (Session, error) {
	record, generation := s.credentialSnapshot()
	if !record.verify(username, password) {
		return Session{}, ErrInvalidCredentials
	}

	tokenBytes := make([]byte, sessionTokenBytes)
	if _, err := io.ReadFull(s.random, tokenBytes); err != nil {
		return Session{}, fmt.Errorf("create local auth session: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	digest := sha256.Sum256([]byte(token))

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation {
		return Session{}, ErrInvalidCredentials
	}
	now := s.now()
	expiresAt := now.Add(s.sessionTTL)
	s.pruneExpiredLocked(now)
	s.evictSessionsLocked()
	s.sessions[digest] = sessionRecord{username: record.Username, expiresAt: expiresAt}
	return Session{
		Token:      token,
		Username:   record.Username,
		MustChange: record.MustChange,
		ExpiresAt:  expiresAt.UTC(),
	}, nil
}

// ValidateSession authenticates a session token and returns current account
// state. Expired sessions are removed lazily.
func (s *Store) ValidateSession(token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrInvalidSession
	}
	digest := sha256.Sum256([]byte(token))

	s.mu.RLock()
	now := s.now()
	record, ok := s.sessions[digest]
	if !ok {
		s.mu.RUnlock()
		return Principal{}, ErrInvalidSession
	}
	if !now.Before(record.expiresAt) {
		s.mu.RUnlock()
		s.mu.Lock()
		if current, exists := s.sessions[digest]; exists && !now.Before(current.expiresAt) {
			delete(s.sessions, digest)
		}
		s.mu.Unlock()
		return Principal{}, ErrInvalidSession
	}
	if !constantTimeStringEqual(record.username, s.credential.Username) {
		s.mu.RUnlock()
		return Principal{}, ErrInvalidSession
	}
	principal := Principal{
		Username:   s.credential.Username,
		MustChange: s.credential.MustChange,
		ExpiresAt:  record.expiresAt.UTC(),
	}
	s.mu.RUnlock()
	return principal, nil
}

// UpdateCredentials requires a valid session, atomically persists the new
// account, and revokes every session after success. The caller should require a
// fresh login using the new credentials.
func (s *Store) UpdateCredentials(sessionToken string, update CredentialUpdate) error {
	if _, err := s.ValidateSession(sessionToken); err != nil {
		return err
	}
	if err := validateAccount(update.Username, update.Password); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(sessionToken))

	// Generate the salt and perform the KDF before taking the write lock so
	// ordinary session validation is not blocked by password hashing.
	next, err := newCredential(update.Username, update.Password, update.MustChange, s.random)
	if err != nil {
		return fmt.Errorf("update local credentials: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	currentSession, ok := s.sessions[digest]
	if !ok || !now.Before(currentSession.expiresAt) || !constantTimeStringEqual(currentSession.username, s.credential.Username) {
		if ok && !now.Before(currentSession.expiresAt) {
			delete(s.sessions, digest)
		}
		return ErrInvalidSession
	}
	if err := saveCredential(s.path, next); err != nil {
		return fmt.Errorf("update local credentials: %w", err)
	}
	s.credential = next
	s.generation++
	clear(s.sessions)
	return nil
}

// RevokeSession removes a single session. It returns whether a session was
// present, allowing logout handlers to remain idempotent.
func (s *Store) RevokeSession(token string) bool {
	if token == "" {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	_, existed := s.sessions[digest]
	delete(s.sessions, digest)
	return existed
}

// RevokeAllSessions invalidates every current login.
func (s *Store) RevokeAllSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.sessions)
}

func (s *Store) credentialSnapshot() (credentialRecord, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.credential, s.generation
}

func (s *Store) pruneExpiredLocked(now time.Time) {
	for digest, session := range s.sessions {
		if !now.Before(session.expiresAt) {
			delete(s.sessions, digest)
		}
	}
}

func (s *Store) evictSessionsLocked() {
	for len(s.sessions) >= s.maxSessions {
		var oldestDigest [sha256.Size]byte
		var oldestExpiry time.Time
		first := true
		for digest, session := range s.sessions {
			if first || session.expiresAt.Before(oldestExpiry) {
				oldestDigest = digest
				oldestExpiry = session.expiresAt
				first = false
			}
		}
		delete(s.sessions, oldestDigest)
	}
}

func validateAccount(username, password string) error {
	if err := validateUsername(username); err != nil {
		return err
	}
	if password == "" || len(password) > MaxPasswordBytes {
		return ErrInvalidAccount
	}
	return nil
}

func validateUsername(username string) error {
	if username == "" || len(username) > MaxUsernameBytes || !utf8.ValidString(username) {
		return ErrInvalidAccount
	}
	return nil
}

func constantTimeStringEqual(left, right string) bool {
	leftHash := sha256.Sum256([]byte(left))
	rightHash := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}
