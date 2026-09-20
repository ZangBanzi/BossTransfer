package localauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBootstrapPersistsHashedPasswordAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth", "credentials.json")
	const password = "  password with spaces  "
	store, err := Open(path, Bootstrap{
		Username:   "admin",
		Password:   password,
		MustChange: true,
	}, Options{SessionTTL: 30 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	if got := store.Status(); got != (Account{Username: "admin", MustChange: true}) {
		t.Fatalf("Status() = %#v", got)
	}
	if !store.VerifyCredentials("admin", password) {
		t.Fatal("exact bootstrap password was rejected")
	}
	if store.VerifyCredentials("admin", strings.TrimSpace(password)) {
		t.Fatal("password whitespace was trimmed")
	}
	if store.VerifyCredentials("other", password) || store.VerifyCredentials("admin", "wrong") {
		t.Fatal("invalid credentials were accepted")
	}
	for _, attempt := range []struct {
		username string
		password string
	}{
		{username: "other", password: password},
		{username: "admin", password: "wrong"},
	} {
		if _, err := store.Login(attempt.username, attempt.password); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("invalid Login error = %v", err)
		}
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), password) {
		t.Fatal("credential file contains the plaintext password")
	}
	var persisted credentialRecord
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Algorithm != passwordAlgorithm || persisted.Iterations != passwordIterations {
		t.Fatalf("unexpected password derivation parameters: %#v", persisted)
	}
	if persisted.Salt == "" || persisted.Hash == "" {
		t.Fatal("credential file is missing the salt or password hash")
	}
	salt, err := base64.RawStdEncoding.DecodeString(persisted.Salt)
	if err != nil || len(salt) != passwordSaltBytes {
		t.Fatalf("persisted salt length = %d, error = %v", len(salt), err)
	}
	derived, err := base64.RawStdEncoding.DecodeString(persisted.Hash)
	if err != nil || len(derived) != passwordHashBytes {
		t.Fatalf("persisted hash length = %d, error = %v", len(derived), err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("credential mode = %04o, want 0600", got)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	} else if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o700 {
		t.Fatalf("credential directory mode = %04o, want 0700", got)
	}

	session, err := store.Login("admin", password)
	if err != nil {
		t.Fatal(err)
	}
	decodedToken, decodeErr := base64.RawURLEncoding.DecodeString(session.Token)
	if decodeErr != nil || len(decodedToken) != sessionTokenBytes || !session.MustChange {
		t.Fatalf("unexpected session: %#v", session)
	}
	if principal, err := store.ValidateSession(session.Token); err != nil || principal.Username != "admin" || !principal.MustChange {
		t.Fatalf("ValidateSession() = %#v, %v", principal, err)
	}

	reloaded, err := Open(path, Bootstrap{Username: "ignored", Password: "ignored"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.VerifyCredentials("admin", password) || reloaded.VerifyCredentials("ignored", "ignored") {
		t.Fatal("existing credentials were not preserved on reload")
	}
	if _, err := reloaded.ValidateSession(session.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("session survived a process-store reload: %v", err)
	}
}

func TestUpdateCredentialsPreservesPasswordBytesAndRevokesSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := newTestStore(t, path, "admin", "old password", true)
	first, err := store.Login("admin", "old password")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Login("admin", "old password")
	if err != nil {
		t.Fatal(err)
	}

	const newPassword = "\tnew password with exact whitespace\r\n"
	if err := store.UpdateCredentials(first.Token, CredentialUpdate{
		Username:   "owner",
		Password:   newPassword,
		MustChange: false,
	}); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first.Token, second.Token} {
		if _, err := store.ValidateSession(token); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("old session remains valid after credential update: %v", err)
		}
	}
	if store.VerifyCredentials("admin", "old password") {
		t.Fatal("old credentials remain valid")
	}
	if !store.VerifyCredentials("owner", newPassword) {
		t.Fatal("updated credentials were rejected")
	}
	if store.VerifyCredentials("owner", strings.TrimSpace(newPassword)) {
		t.Fatal("updated password whitespace was trimmed")
	}
	if got := store.Status(); got != (Account{Username: "owner", MustChange: false}) {
		t.Fatalf("Status() = %#v", got)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), newPassword) || strings.Contains(string(raw), "old password") {
		t.Fatal("credential file contains a plaintext password")
	}
	reloaded, err := Open(path, Bootstrap{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.VerifyCredentials("owner", newPassword) {
		t.Fatal("updated credentials did not survive reload")
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".bosstransfer-auth-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("atomic credential writes left temporary files: %v", leftovers)
	}
}

func TestSessionExpiryAndRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := newTestStore(t, path, "admin", "password", true)
	clock := time.Date(2026, time.September, 19, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	store.sessionTTL = 15 * time.Minute

	first, err := store.Login("admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	if want := clock.Add(15 * time.Minute); !first.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %s, want %s", first.ExpiresAt, want)
	}
	clock = clock.Add(15*time.Minute - time.Nanosecond)
	if _, err := store.ValidateSession(first.Token); err != nil {
		t.Fatalf("session expired early: %v", err)
	}
	clock = clock.Add(time.Nanosecond)
	if _, err := store.ValidateSession(first.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired session error = %v", err)
	}
	if store.RevokeSession(first.Token) {
		t.Fatal("expired session was not removed")
	}

	clock = clock.Add(time.Minute)
	second, err := store.Login("admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	if !store.RevokeSession(second.Token) || store.RevokeSession(second.Token) {
		t.Fatal("single-session revocation is not idempotent")
	}
	third, err := store.Login("admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	store.RevokeAllSessions()
	if _, err := store.ValidateSession(third.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("RevokeAllSessions left a valid session: %v", err)
	}
}

func TestSessionLimitEvictsOldestLogin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store, err := Open(path, Bootstrap{Username: "admin", Password: "password"}, Options{
		SessionTTL:  time.Hour,
		MaxSessions: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, time.September, 19, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	first, err := store.Login("admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	second, err := store.Login("admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	third, err := store.Login("admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ValidateSession(first.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("oldest session was not evicted: %v", err)
	}
	for _, token := range []string{second.Token, third.Token} {
		if _, err := store.ValidateSession(token); err != nil {
			t.Fatalf("newer session was evicted: %v", err)
		}
	}
}

func TestInvalidSessionCannotChangeCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := newTestStore(t, path, "admin", "password", true)
	err := store.UpdateCredentials("not-a-session", CredentialUpdate{
		Username: "attacker",
		Password: "attacker password",
	})
	if !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("UpdateCredentials error = %v", err)
	}
	if !store.VerifyCredentials("admin", "password") {
		t.Fatal("invalid update changed the credentials")
	}
}

func TestAccountInputBoundsPreventUnreloadableCredentials(t *testing.T) {
	root := t.TempDir()
	invalidBootstraps := []Bootstrap{
		{Username: strings.Repeat("u", MaxUsernameBytes+1), Password: "password"},
		{Username: string([]byte{0xff}), Password: "password"},
		{Username: "admin", Password: strings.Repeat("p", MaxPasswordBytes+1)},
	}
	for index, bootstrap := range invalidBootstraps {
		path := filepath.Join(root, fmt.Sprintf("invalid-%d.json", index))
		if _, err := Open(path, bootstrap, Options{}); !errors.Is(err, ErrInvalidAccount) {
			t.Fatalf("invalid bootstrap %d error = %v", index, err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid bootstrap %d created a credential file: %v", index, err)
		}
	}

	boundaryUsername := strings.Repeat("u", MaxUsernameBytes)
	boundaryPassword := strings.Repeat("p", MaxPasswordBytes)
	boundaryPath := filepath.Join(root, "boundary.json")
	boundary := newTestStore(t, boundaryPath, boundaryUsername, boundaryPassword, false)
	if !boundary.VerifyCredentials(boundaryUsername, boundaryPassword) {
		t.Fatal("boundary-sized credentials were rejected")
	}
	if _, err := Open(boundaryPath, Bootstrap{}, Options{}); err != nil {
		t.Fatalf("boundary-sized credentials could not be reloaded: %v", err)
	}

	path := filepath.Join(root, "active.json")
	store := newTestStore(t, path, "admin", "password", true)
	session, err := store.Login("admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	invalidUpdates := []CredentialUpdate{
		{Username: strings.Repeat("u", MaxUsernameBytes+1), Password: "new password"},
		{Username: string([]byte{0xff}), Password: "new password"},
		{Username: "owner", Password: strings.Repeat("p", MaxPasswordBytes+1)},
	}
	for index, update := range invalidUpdates {
		if err := store.UpdateCredentials(session.Token, update); !errors.Is(err, ErrInvalidAccount) {
			t.Fatalf("invalid update %d error = %v", index, err)
		}
		if !store.VerifyCredentials("admin", "password") {
			t.Fatalf("invalid update %d changed active credentials", index)
		}
		if _, err := store.ValidateSession(session.Token); err != nil {
			t.Fatalf("invalid update %d revoked the active session: %v", index, err)
		}
	}
}

func TestFailedCredentialWriteLeavesAccountAndSessionIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := newTestStore(t, path, "admin", "password", true)
	session, err := store.Login("admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	store.path = t.TempDir() // Renaming a regular file over a directory must fail.
	err = store.UpdateCredentials(session.Token, CredentialUpdate{
		Username: "owner",
		Password: "new password",
	})
	if err == nil {
		t.Fatal("UpdateCredentials unexpectedly succeeded")
	}
	if !store.VerifyCredentials("admin", "password") {
		t.Fatal("failed persistence changed the in-memory credentials")
	}
	if _, err := store.ValidateSession(session.Token); err != nil {
		t.Fatalf("failed persistence revoked the active session: %v", err)
	}
}

func TestIndependentStoresUseDifferentSalts(t *testing.T) {
	root := t.TempDir()
	first := newTestStore(t, filepath.Join(root, "first.json"), "admin", "same password", true)
	second := newTestStore(t, filepath.Join(root, "second.json"), "admin", "same password", true)
	if first.credential.Salt == second.credential.Salt || first.credential.Hash == second.credential.Hash {
		t.Fatal("independent accounts reused a salt or password hash")
	}
}

func TestConcurrentSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := newTestStore(t, path, "admin", "password", false)

	const workers = 8
	sessions := make([]Session, workers)
	var loginWait sync.WaitGroup
	loginErrors := make(chan error, workers)
	for index := range sessions {
		index := index
		loginWait.Add(1)
		go func() {
			defer loginWait.Done()
			session, err := store.Login("admin", "password")
			if err == nil {
				sessions[index] = session
			}
			loginErrors <- err
		}()
	}
	loginWait.Wait()
	close(loginErrors)
	for err := range loginErrors {
		if err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	validationErrors := make(chan error, workers*4)
	var validationWait sync.WaitGroup
	for index := 0; index < workers*4; index++ {
		token := sessions[index%workers].Token
		validationWait.Add(1)
		go func() {
			defer validationWait.Done()
			<-start
			principal, err := store.ValidateSession(token)
			if err == nil && principal.Username != "admin" {
				err = fmt.Errorf("unexpected principal: %#v", principal)
			}
			validationErrors <- err
		}()
	}
	close(start)
	validationWait.Wait()
	close(validationErrors)
	for err := range validationErrors {
		if err != nil {
			t.Fatal(err)
		}
	}

	var revokeWait sync.WaitGroup
	for _, session := range sessions {
		session := session
		revokeWait.Add(1)
		go func() {
			defer revokeWait.Done()
			store.RevokeSession(session.Token)
		}()
	}
	revokeWait.Wait()
	for _, session := range sessions {
		if _, err := store.ValidateSession(session.Token); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("concurrently revoked session remains valid: %v", err)
		}
	}
}

func TestConcurrentCredentialUpdatesHaveOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := newTestStore(t, path, "admin", "password", true)
	first, err := store.Login("admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Login("admin", "password")
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		username string
		password string
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	updates := []struct {
		session  Session
		username string
		password string
	}{
		{session: first, username: "first-owner", password: "first password"},
		{session: second, username: "second-owner", password: "second password"},
	}
	for _, update := range updates {
		update := update
		go func() {
			<-start
			err := store.UpdateCredentials(update.session.Token, CredentialUpdate{
				Username: update.username,
				Password: update.password,
			})
			results <- result{username: update.username, password: update.password, err: err}
		}()
	}
	close(start)

	var winner result
	successes := 0
	invalidSessions := 0
	for range updates {
		result := <-results
		switch {
		case result.err == nil:
			winner = result
			successes++
		case errors.Is(result.err, ErrInvalidSession):
			invalidSessions++
		default:
			t.Fatalf("unexpected concurrent update error: %v", result.err)
		}
	}
	if successes != 1 || invalidSessions != 1 {
		t.Fatalf("concurrent update outcomes: successes=%d invalid_sessions=%d", successes, invalidSessions)
	}
	if !store.VerifyCredentials(winner.username, winner.password) {
		t.Fatal("winning credentials are not active in memory")
	}
	reloaded, err := Open(path, Bootstrap{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.VerifyCredentials(winner.username, winner.password) {
		t.Fatal("winning credentials are not active on disk")
	}
}

func TestLoginRacingCredentialUpdateCannotCreateOldSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := newTestStore(t, path, "admin", "old password", true)
	updateSession, err := store.Login("admin", "old password")
	if err != nil {
		t.Fatal(err)
	}

	controlled := &blockingTokenReader{
		waiting: make(chan struct{}),
		release: make(chan struct{}),
	}
	store.random = controlled
	loginResult := make(chan error, 1)
	go func() {
		_, err := store.Login("admin", "old password")
		loginResult <- err
	}()
	<-controlled.waiting

	if err := store.UpdateCredentials(updateSession.Token, CredentialUpdate{
		Username: "owner",
		Password: "new password",
	}); err != nil {
		t.Fatal(err)
	}
	close(controlled.release)
	if err := <-loginResult; !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("racing old-password Login error = %v", err)
	}
	if len(store.sessions) != 0 {
		t.Fatalf("old-password Login left %d sessions after update", len(store.sessions))
	}
	if !store.VerifyCredentials("owner", "new password") {
		t.Fatal("updated credentials are not active")
	}
}

func TestEntropyFailureDoesNotChangeState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store := newTestStore(t, path, "admin", "password", true)
	session, err := store.Login("admin", "password")
	if err != nil {
		t.Fatal(err)
	}
	store.random = failingReader{}

	if _, err := store.Login("admin", "password"); err == nil {
		t.Fatal("Login succeeded with a failed entropy source")
	}
	if err := store.UpdateCredentials(session.Token, CredentialUpdate{
		Username: "owner",
		Password: "new password",
	}); err == nil {
		t.Fatal("UpdateCredentials succeeded with a failed entropy source")
	}
	if !store.VerifyCredentials("admin", "password") {
		t.Fatal("entropy failure changed the credentials")
	}
	if _, err := store.ValidateSession(session.Token); err != nil {
		t.Fatalf("entropy failure revoked the existing session: %v", err)
	}
}

func TestOpenRejectsInvalidCredentialFiles(t *testing.T) {
	tests := map[string]string{
		"malformed":         `{`,
		"unknown field":     `{"version":1,"username":"admin","algorithm":"pbkdf2-hmac-sha256","iterations":600000,"salt":"AAAAAAAAAAAAAAAAAAAAAA","hash":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","must_change":true,"password":"plaintext"}`,
		"unknown version":   `{"version":2,"username":"admin","algorithm":"pbkdf2-hmac-sha256","iterations":600000,"salt":"AAAAAAAAAAAAAAAAAAAAAA","hash":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","must_change":true}`,
		"unknown algorithm": `{"version":1,"username":"admin","algorithm":"plain-sha256","iterations":600000,"salt":"AAAAAAAAAAAAAAAAAAAAAA","hash":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","must_change":true}`,
		"weak iterations":   `{"version":1,"username":"admin","algorithm":"pbkdf2-hmac-sha256","iterations":1,"salt":"AAAAAAAAAAAAAAAAAAAAAA","hash":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","must_change":true}`,
		"high iterations":   `{"version":1,"username":"admin","algorithm":"pbkdf2-hmac-sha256","iterations":10000000,"salt":"AAAAAAAAAAAAAAAAAAAAAA","hash":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","must_change":true}`,
		"bad salt":          `{"version":1,"username":"admin","algorithm":"pbkdf2-hmac-sha256","iterations":600000,"salt":"%%%","hash":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","must_change":true}`,
		"short salt":        `{"version":1,"username":"admin","algorithm":"pbkdf2-hmac-sha256","iterations":600000,"salt":"AAAAAAAAAAAAAAAAAAAA","hash":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","must_change":true}`,
		"bad hash":          `{"version":1,"username":"admin","algorithm":"pbkdf2-hmac-sha256","iterations":600000,"salt":"AAAAAAAAAAAAAAAAAAAAAA","hash":"%%%","must_change":true}`,
		"trailing value":    `{"version":1,"username":"admin","algorithm":"pbkdf2-hmac-sha256","iterations":600000,"salt":"AAAAAAAAAAAAAAAAAAAAAA","hash":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","must_change":true} {}`,
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credentials.json")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path, Bootstrap{Username: "admin", Password: "password"}, Options{}); err == nil {
				t.Fatal("Open unexpectedly accepted invalid credentials")
			}
		})
	}
}

func TestOpenRejectsOversizedAndNonRegularCredentialPaths(t *testing.T) {
	oversized := filepath.Join(t.TempDir(), "oversized.json")
	if err := os.WriteFile(oversized, make([]byte, maxCredentialFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(oversized, Bootstrap{Username: "admin", Password: "password"}, Options{}); err == nil {
		t.Fatal("Open accepted an oversized credential file")
	}

	directory := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(directory, Bootstrap{Username: "admin", Password: "password"}, Options{}); err == nil {
		t.Fatal("Open accepted a directory as a credential file")
	}

	t.Run("symbolic link", func(t *testing.T) {
		realPath := filepath.Join(t.TempDir(), "real.json")
		newTestStore(t, realPath, "admin", "password", true)
		linkPath := filepath.Join(t.TempDir(), "link.json")
		if err := os.Symlink(realPath, linkPath); err != nil {
			t.Skipf("symbolic links are unavailable: %v", err)
		}
		if _, err := Open(linkPath, Bootstrap{}, Options{}); err == nil {
			t.Fatal("Open accepted a symbolic-link credential path")
		}
	})
}

func TestOpenTightensExistingCredentialMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	newTestStore(t, path, "admin", "password", true)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, Bootstrap{}, Options{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential mode after reload = %04o, want 0600", got)
	}
}

func TestOpenValidatesBootstrapOnlyForNewStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if _, err := Open(path, Bootstrap{}, Options{}); !errors.Is(err, ErrInvalidAccount) {
		t.Fatalf("empty bootstrap error = %v", err)
	}
	store := newTestStore(t, path, "admin", "password", true)
	if _, err := Open(path, Bootstrap{}, Options{}); err != nil {
		t.Fatalf("existing store required bootstrap credentials: %v", err)
	}
	if !store.VerifyCredentials("admin", "password") {
		t.Fatal("valid bootstrap credentials were rejected")
	}
}

func newTestStore(t *testing.T, path, username, password string, mustChange bool) *Store {
	t.Helper()
	store, err := Open(path, Bootstrap{
		Username:   username,
		Password:   password,
		MustChange: mustChange,
	}, Options{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type blockingTokenReader struct {
	waiting chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingTokenReader) Read(buffer []byte) (int, error) {
	if len(buffer) == sessionTokenBytes {
		r.once.Do(func() { close(r.waiting) })
		<-r.release
	}
	for index := range buffer {
		buffer[index] = byte(index + 1)
	}
	return len(buffer), nil
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}
