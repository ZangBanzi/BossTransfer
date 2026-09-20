package setup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const encryptedTokenPrefix = "aesgcm:v1:"

// Config contains only client-local connection and directory selections.
// Token is encrypted before the config is written and is never exposed by API responses.
type Config struct {
	CloudDrive2Address string `json:"clouddrive2_address"`
	AuthMode           string `json:"auth_mode"`
	Username           string `json:"username,omitempty"`
	Password           string `json:"-"`
	Token              string `json:"-"`
	MountPoint         string `json:"mount_point,omitempty"`
	CloudTargetDir     string `json:"cloud_target_dir,omitempty"`
	SourceRelative     string `json:"source_relative,omitempty"`
	TargetRelative     string `json:"target_relative,omitempty"`
}

type diskConfig struct {
	CloudDrive2Address string `json:"clouddrive2_address"`
	AuthMode           string `json:"auth_mode,omitempty"`
	Username           string `json:"username,omitempty"`
	EncryptedPassword  string `json:"encrypted_password,omitempty"`
	EncryptedToken     string `json:"encrypted_token,omitempty"`
	MountPoint         string `json:"mount_point,omitempty"`
	CloudTargetDir     string `json:"cloud_target_dir,omitempty"`
	SourceRelative     string `json:"source_relative,omitempty"`
	TargetRelative     string `json:"target_relative,omitempty"`
}

type PublicConfig struct {
	CloudDrive2Address string `json:"clouddrive2_address"`
	AuthMode           string `json:"auth_mode"`
	Username           string `json:"username,omitempty"`
	PasswordPresent    bool   `json:"password_present"`
	TokenPresent       bool   `json:"token_present"`
	MountPoint         string `json:"mount_point,omitempty"`
	CloudTargetDir     string `json:"cloud_target_dir,omitempty"`
	SourceRelative     string `json:"source_relative,omitempty"`
	TargetRelative     string `json:"target_relative,omitempty"`
}

type Store struct {
	mu      sync.RWMutex
	path    string
	keyPath string
	cfg     Config
}

func NewStore(path string, defaults Config) (*Store, error) {
	defaults.CloudDrive2Address = strings.TrimSpace(defaults.CloudDrive2Address)
	defaults.AuthMode = normalizeAuthMode(defaults.AuthMode)
	store := &Store{
		path:    path,
		keyPath: filepath.Join(filepath.Dir(path), "setup.key"),
		cfg:     defaults,
	}
	if strings.TrimSpace(path) == "" {
		store.keyPath = ""
		return store, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, fmt.Errorf("read client setup: %w", err)
	}
	var saved diskConfig
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, fmt.Errorf("decode client setup: %w", err)
	}
	token, err := store.decrypt(saved.EncryptedToken)
	if err != nil {
		return nil, fmt.Errorf("decrypt CloudDrive2 token: %w", err)
	}
	password, err := store.decrypt(saved.EncryptedPassword)
	if err != nil {
		return nil, fmt.Errorf("decrypt CloudDrive2 password: %w", err)
	}
	store.cfg = Config{
		CloudDrive2Address: strings.TrimSpace(saved.CloudDrive2Address),
		AuthMode:           normalizeAuthMode(saved.AuthMode),
		Username:           strings.TrimSpace(saved.Username),
		Password:           password,
		Token:              token,
		MountPoint:         strings.TrimSpace(saved.MountPoint),
		CloudTargetDir:     cleanCloudPath(saved.CloudTargetDir),
		SourceRelative:     cleanRelative(saved.SourceRelative),
		TargetRelative:     cleanRelative(saved.TargetRelative),
	}
	if store.cfg.CloudDrive2Address == "" {
		store.cfg.CloudDrive2Address = defaults.CloudDrive2Address
	}
	return store, nil
}

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Store) Public() PublicConfig {
	cfg := s.Get()
	return PublicConfig{
		CloudDrive2Address: cfg.CloudDrive2Address,
		AuthMode:           normalizeAuthMode(cfg.AuthMode),
		Username:           cfg.Username,
		PasswordPresent:    cfg.Password != "",
		TokenPresent:       strings.TrimSpace(cfg.Token) != "",
		MountPoint:         cfg.MountPoint,
		CloudTargetDir:     cfg.CloudTargetDir,
		SourceRelative:     cfg.SourceRelative,
		TargetRelative:     cfg.TargetRelative,
	}
}

func (s *Store) Update(cfg Config) error {
	cfg.CloudDrive2Address = strings.TrimSpace(cfg.CloudDrive2Address)
	cfg.AuthMode = normalizeAuthMode(cfg.AuthMode)
	cfg.Username = strings.TrimSpace(cfg.Username)
	// Password is intentionally not trimmed; spaces can be part of it.
	cfg.Token = strings.TrimSpace(cfg.Token)
	cfg.MountPoint = strings.TrimSpace(cfg.MountPoint)
	cfg.CloudTargetDir = cleanCloudPath(cfg.CloudTargetDir)
	cfg.SourceRelative = cleanRelative(cfg.SourceRelative)
	cfg.TargetRelative = cleanRelative(cfg.TargetRelative)
	// CloudDrive2 is optional. A fresh client must be able to save its system
	// download directory before this integration has been configured.
	cloudDriveConfigured := cfg.Username != "" || cfg.Password != "" || cfg.Token != "" || cfg.MountPoint != "" || cfg.CloudTargetDir != ""
	if cloudDriveConfigured {
		if cfg.CloudDrive2Address == "" {
			return errors.New("CloudDrive2 地址不能为空")
		}
		if cfg.AuthMode == "password" && (cfg.Username == "" || cfg.Password == "") {
			return errors.New("CloudDrive2 账号和密码不能为空")
		}
		if cfg.AuthMode == "token" && cfg.Token == "" {
			return errors.New("CloudDrive2 API Token 不能为空")
		}
	}

	// Hold the write lock through key creation, encryption, disk replacement,
	// and the in-memory update. In particular, two first-time updates must not
	// create different keys and leave the config encrypted by the losing key.
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.path == "" {
		s.cfg = cfg
		return nil
	}
	ciphertext, err := s.encrypt(cfg.Token)
	if err != nil {
		return err
	}
	encryptedPassword, err := s.encrypt(cfg.Password)
	if err != nil {
		return err
	}
	onDisk := diskConfig{
		CloudDrive2Address: cfg.CloudDrive2Address,
		AuthMode:           cfg.AuthMode,
		Username:           cfg.Username,
		EncryptedPassword:  encryptedPassword,
		EncryptedToken:     ciphertext,
		MountPoint:         cfg.MountPoint,
		CloudTargetDir:     cfg.CloudTargetDir,
		SourceRelative:     cfg.SourceRelative,
		TargetRelative:     cfg.TargetRelative,
	}
	data, err := json.MarshalIndent(onDisk, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(s.path, data, 0o600); err != nil {
		return fmt.Errorf("save client setup: %w", err)
	}
	s.cfg = cfg
	return nil
}

func cleanCloudPath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	if value == "" {
		return ""
	}
	return "/" + strings.Trim(strings.TrimPrefix(value, "/"), "/")
}

func normalizeAuthMode(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "password") {
		return "password"
	}
	return "token"
}

func (s *Store) encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
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
	sealed := aead.Seal(nonce, nonce, []byte(plain), nil)
	return encryptedTokenPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (s *Store) decrypt(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, encryptedTokenPrefix) {
		return "", errors.New("unsupported token encryption format")
	}
	encoded := strings.TrimPrefix(value, encryptedTokenPrefix)
	sealed, err := base64.RawStdEncoding.DecodeString(encoded)
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
		return "", errors.New("encrypted token is truncated")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Store) loadOrCreateKey() ([]byte, error) {
	key, err := os.ReadFile(s.keyPath)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("invalid setup encryption key")
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
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bosstransfer-setup-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
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
	return os.Rename(tmpPath, path)
}

func cleanRelative(value string) string {
	value = strings.TrimSpace(filepath.ToSlash(value))
	value = strings.Trim(value, "/")
	if value == "." {
		return ""
	}
	return value
}
