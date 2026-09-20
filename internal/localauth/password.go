package localauth

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const (
	credentialVersion  = 1
	passwordAlgorithm  = "pbkdf2-hmac-sha256"
	passwordIterations = 600_000
	passwordSaltBytes  = 16
	passwordHashBytes  = 32
)

type credentialRecord struct {
	Version    int    `json:"version"`
	Username   string `json:"username"`
	Algorithm  string `json:"algorithm"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	MustChange bool   `json:"must_change"`
}

func newCredential(username, password string, mustChange bool, random io.Reader) (credentialRecord, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := io.ReadFull(random, salt); err != nil {
		return credentialRecord{}, err
	}
	derived, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, passwordHashBytes)
	if err != nil {
		return credentialRecord{}, err
	}
	return credentialRecord{
		Version:    credentialVersion,
		Username:   username,
		Algorithm:  passwordAlgorithm,
		Iterations: passwordIterations,
		Salt:       base64.RawStdEncoding.EncodeToString(salt),
		Hash:       base64.RawStdEncoding.EncodeToString(derived),
		MustChange: mustChange,
	}, nil
}

func (r credentialRecord) validate() error {
	if r.Version != credentialVersion {
		return fmt.Errorf("unsupported credential version %d", r.Version)
	}
	if err := validateUsername(r.Username); err != nil {
		return errors.New("invalid credential username")
	}
	if r.Algorithm != passwordAlgorithm {
		return fmt.Errorf("unsupported password algorithm %q", r.Algorithm)
	}
	if r.Iterations != passwordIterations {
		return errors.New("invalid password iteration count")
	}
	salt, err := base64.RawStdEncoding.DecodeString(r.Salt)
	if err != nil || len(salt) != passwordSaltBytes {
		return errors.New("invalid password salt")
	}
	hash, err := base64.RawStdEncoding.DecodeString(r.Hash)
	if err != nil || len(hash) != passwordHashBytes {
		return errors.New("invalid password hash")
	}
	return nil
}

func (r credentialRecord) verify(username, password string) bool {
	if len(username) > MaxUsernameBytes || len(password) > MaxPasswordBytes {
		return false
	}
	salt, saltErr := base64.RawStdEncoding.DecodeString(r.Salt)
	want, hashErr := base64.RawStdEncoding.DecodeString(r.Hash)
	if saltErr != nil || hashErr != nil || len(salt) == 0 || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, r.Iterations, len(want))
	if err != nil {
		return false
	}
	usernameValid := subtle.ConstantTimeCompare(
		sha256Digest(username),
		sha256Digest(r.Username),
	)
	passwordValid := subtle.ConstantTimeCompare(got, want)
	return usernameValid&passwordValid == 1
}

func sha256Digest(value string) []byte {
	digest := sha256.Sum256([]byte(value))
	return digest[:]
}
