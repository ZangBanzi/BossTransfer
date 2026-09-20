package licenseclient

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const encryptedPrefix = "aesgcm:v1:"

type License struct {
	ID        string     `json:"id"`
	Customer  string     `json:"customer"`
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type Status struct {
	ManagerURL     string     `json:"manager_url"`
	InstallationID string     `json:"installation_id"`
	Activated      bool       `json:"activated"`
	DeviceID       string     `json:"device_id,omitempty"`
	License        License    `json:"license"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	LastCheckedAt  *time.Time `json:"last_checked_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
}

type diskState struct {
	ManagerURL           string     `json:"manager_url"`
	InstallationID       string     `json:"installation_id"`
	PublicKey            string     `json:"public_key"`
	EncryptedPrivateKey  string     `json:"encrypted_private_key"`
	DeviceID             string     `json:"device_id,omitempty"`
	EncryptedDeviceToken string     `json:"encrypted_device_token,omitempty"`
	License              License    `json:"license"`
	LeaseExpiresAt       *time.Time `json:"lease_expires_at,omitempty"`
	LastCheckedAt        *time.Time `json:"last_checked_at,omitempty"`
	LastError            string     `json:"last_error,omitempty"`
}

type Store struct {
	mu      sync.RWMutex
	path    string
	keyPath string
	state   diskState
}

func Open(path, defaultManagerURL string) (*Store, error) {
	store := &Store{path: path, keyPath: filepath.Join(filepath.Dir(path), "client-license.key")}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &store.state); err != nil {
			return nil, fmt.Errorf("decode client license state: %w", err)
		}
		if _, err := store.decrypt(store.state.EncryptedPrivateKey); err != nil {
			return nil, fmt.Errorf("decrypt device identity: %w", err)
		}
		if store.state.EncryptedDeviceToken != "" {
			if _, err := store.decrypt(store.state.EncryptedDeviceToken); err != nil {
				return nil, fmt.Errorf("decrypt device credential: %w", err)
			}
		}
		if store.state.ManagerURL == "" {
			store.state.ManagerURL = strings.TrimSpace(defaultManagerURL)
		}
		return store, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read client license state: %w", err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	privateValue, err := store.encrypt(privateKey)
	if err != nil {
		return nil, err
	}
	store.state = diskState{
		ManagerURL:          strings.TrimSpace(defaultManagerURL),
		InstallationID:      randomID("BTI", 16),
		PublicKey:           base64.RawURLEncoding.EncodeToString(publicKey),
		EncryptedPrivateKey: privateValue,
	}
	if err := store.saveLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Status(now time.Time) Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	activated := s.state.DeviceID != "" && s.state.EncryptedDeviceToken != "" && s.state.License.Status == "active"
	if s.state.LeaseExpiresAt == nil || !now.Before(*s.state.LeaseExpiresAt) {
		activated = false
	}
	if s.state.License.ExpiresAt != nil && !now.Before(*s.state.License.ExpiresAt) {
		activated = false
	}
	return Status{
		ManagerURL: s.state.ManagerURL, InstallationID: s.state.InstallationID,
		Activated: activated, DeviceID: s.state.DeviceID, License: cloneLicense(s.state.License),
		LeaseExpiresAt: cloneTime(s.state.LeaseExpiresAt), LastCheckedAt: cloneTime(s.state.LastCheckedAt), LastError: s.state.LastError,
	}
}

func (s *Store) Identity() (managerURL, installationID, publicKey string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.ManagerURL, s.state.InstallationID, s.state.PublicKey
}

func (s *Store) DeviceCredential() (deviceID, token string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.DeviceID == "" || s.state.EncryptedDeviceToken == "" {
		return "", "", errors.New("客户端尚未激活")
	}
	token, err = s.decrypt(s.state.EncryptedDeviceToken)
	return s.state.DeviceID, token, err
}

func (s *Store) SaveActivation(managerURL, deviceID, token string, license License, leaseExpiresAt, checkedAt time.Time) error {
	managerURL = strings.TrimRight(strings.TrimSpace(managerURL), "/")
	if managerURL == "" || deviceID == "" || token == "" || license.ID == "" {
		return errors.New("activation response is incomplete")
	}
	encryptedToken, err := s.encrypt([]byte(token))
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.state
	s.state.ManagerURL = managerURL
	s.state.DeviceID = deviceID
	s.state.EncryptedDeviceToken = encryptedToken
	s.state.License = cloneLicense(license)
	s.state.LeaseExpiresAt = &leaseExpiresAt
	s.state.LastCheckedAt = &checkedAt
	s.state.LastError = ""
	if err := s.saveLocked(); err != nil {
		s.state = before
		return err
	}
	return nil
}

func (s *Store) SaveHeartbeat(license License, leaseExpiresAt, checkedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.state
	s.state.License = cloneLicense(license)
	s.state.LeaseExpiresAt = &leaseExpiresAt
	s.state.LastCheckedAt = &checkedAt
	s.state.LastError = ""
	if err := s.saveLocked(); err != nil {
		s.state = before
		return err
	}
	return nil
}

func (s *Store) MarkRemoteStatus(status, message string, checkedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.state
	if status != "" {
		s.state.License.Status = status
	}
	s.state.LastError = message
	s.state.LastCheckedAt = &checkedAt
	if err := s.saveLocked(); err != nil {
		s.state = before
		return err
	}
	return nil
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.path, data, 0o600)
}

func (s *Store) encrypt(plain []byte) (string, error) {
	key, err := s.loadOrCreateKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, plain, nil)
	return encryptedPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (s *Store) decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, encryptedPrefix) {
		return "", errors.New("unsupported encryption format")
	}
	sealed, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, encryptedPrefix))
	if err != nil {
		return "", err
	}
	key, err := os.ReadFile(s.keyPath)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < aead.NonceSize() {
		return "", errors.New("encrypted value is truncated")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], nil)
	return string(plain), err
}

func (s *Store) loadOrCreateKey() ([]byte, error) {
	key, err := os.ReadFile(s.keyPath)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("invalid client license key")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := atomicWrite(s.keyPath, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bosstransfer-license-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func randomID(prefix string, bytes int) string {
	value := make([]byte, bytes)
	_, _ = io.ReadFull(rand.Reader, value)
	return prefix + "-" + strings.TrimRight(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value), "=")
}

func cloneLicense(value License) License {
	value.ExpiresAt = cloneTime(value.ExpiresAt)
	return value
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
