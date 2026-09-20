// Package account115 stores one locally bound 115 Open account and manages
// the device authorization flow. Access and refresh tokens stay encrypted on
// the machine that owns the account.
package account115

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
	"time"
)

const encryptedPrefix = "aesgcm:v1:"

type Config struct {
	ClientID     string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	UserID       string
	UserName     string
	Avatar       string
	FolderID     string
	FolderName   string
}

type PublicConfig struct {
	ClientID      string    `json:"client_id,omitempty"`
	AppConfigured bool      `json:"app_configured"`
	Bound         bool      `json:"bound"`
	UserID        string    `json:"user_id,omitempty"`
	UserName      string    `json:"user_name,omitempty"`
	Avatar        string    `json:"avatar,omitempty"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	FolderID      string    `json:"folder_id"`
	FolderName    string    `json:"folder_name"`
}

type diskConfig struct {
	Version               int       `json:"version"`
	ClientID              string    `json:"client_id,omitempty"`
	EncryptedAccessToken  string    `json:"access_token,omitempty"`
	EncryptedRefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt             time.Time `json:"expires_at,omitempty"`
	UserID                string    `json:"user_id,omitempty"`
	UserName              string    `json:"user_name,omitempty"`
	Avatar                string    `json:"avatar,omitempty"`
	FolderID              string    `json:"folder_id,omitempty"`
	FolderName            string    `json:"folder_name,omitempty"`
}

type Store struct {
	mu      sync.RWMutex
	path    string
	keyPath string
	cfg     Config
}

func Open(path, defaultClientID string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("115 account store path is required")
	}
	store := &Store{
		path: path, keyPath: filepath.Join(filepath.Dir(path), "account-115.key"),
		cfg: Config{ClientID: strings.TrimSpace(defaultClientID), FolderID: "0", FolderName: "根目录"},
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read 115 account: %w", err)
	}
	var disk diskConfig
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, fmt.Errorf("decode 115 account: %w", err)
	}
	if disk.Version != 1 {
		return nil, errors.New("unsupported 115 account version")
	}
	access, err := store.decrypt(disk.EncryptedAccessToken, disk.ClientID, "access")
	if err != nil {
		return nil, fmt.Errorf("decrypt 115 access token: %w", err)
	}
	refresh, err := store.decrypt(disk.EncryptedRefreshToken, disk.ClientID, "refresh")
	if err != nil {
		return nil, fmt.Errorf("decrypt 115 refresh token: %w", err)
	}
	store.cfg = Config{
		ClientID: strings.TrimSpace(disk.ClientID), AccessToken: access, RefreshToken: refresh,
		ExpiresAt: disk.ExpiresAt, UserID: strings.TrimSpace(disk.UserID), UserName: strings.TrimSpace(disk.UserName),
		Avatar: strings.TrimSpace(disk.Avatar), FolderID: folderID(disk.FolderID), FolderName: folderName(disk.FolderName),
	}
	if store.cfg.ClientID == "" {
		store.cfg.ClientID = strings.TrimSpace(defaultClientID)
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
		ClientID: cfg.ClientID, AppConfigured: cfg.ClientID != "", Bound: cfg.AccessToken != "" && cfg.RefreshToken != "",
		UserID: cfg.UserID, UserName: cfg.UserName, Avatar: cfg.Avatar, ExpiresAt: cfg.ExpiresAt,
		FolderID: folderID(cfg.FolderID), FolderName: folderName(cfg.FolderName),
	}
}

func (s *Store) SetClientID(clientID string) error {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" || len(clientID) > 128 {
		return errors.New("请填写有效的 115 Open AppID")
	}
	cfg := s.Get()
	if cfg.ClientID == clientID {
		return nil
	}
	// Tokens are issued to one application. Changing AppID intentionally
	// removes the previous binding so tokens cannot cross application scopes.
	cfg = Config{ClientID: clientID, FolderID: "0", FolderName: "根目录"}
	return s.save(cfg)
}

func (s *Store) SaveAccount(accessToken, refreshToken string, expiresAt time.Time, userID, userName, avatar string) error {
	cfg := s.Get()
	accessToken = strings.TrimSpace(accessToken)
	refreshToken = strings.TrimSpace(refreshToken)
	if cfg.ClientID == "" || accessToken == "" || refreshToken == "" {
		return errors.New("115 授权信息不完整")
	}
	cfg.AccessToken = accessToken
	cfg.RefreshToken = refreshToken
	cfg.ExpiresAt = expiresAt.UTC().Round(0)
	cfg.UserID = strings.TrimSpace(userID)
	cfg.UserName = strings.TrimSpace(userName)
	cfg.Avatar = strings.TrimSpace(avatar)
	return s.save(cfg)
}

func (s *Store) SelectFolder(id, name string) error {
	id = folderID(id)
	name = folderName(name)
	cfg := s.Get()
	if cfg.AccessToken == "" {
		return errors.New("请先绑定 115 账号")
	}
	cfg.FolderID, cfg.FolderName = id, name
	return s.save(cfg)
}

func (s *Store) ClearAccount() error {
	cfg := s.Get()
	return s.save(Config{ClientID: cfg.ClientID, FolderID: "0", FolderName: "根目录"})
}

func (s *Store) save(cfg Config) error {
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.AccessToken = strings.TrimSpace(cfg.AccessToken)
	cfg.RefreshToken = strings.TrimSpace(cfg.RefreshToken)
	cfg.UserID = strings.TrimSpace(cfg.UserID)
	cfg.UserName = strings.TrimSpace(cfg.UserName)
	cfg.Avatar = strings.TrimSpace(cfg.Avatar)
	cfg.FolderID = folderID(cfg.FolderID)
	cfg.FolderName = folderName(cfg.FolderName)

	s.mu.Lock()
	defer s.mu.Unlock()
	access, err := s.encrypt(cfg.AccessToken, cfg.ClientID, "access")
	if err != nil {
		return err
	}
	refresh, err := s.encrypt(cfg.RefreshToken, cfg.ClientID, "refresh")
	if err != nil {
		return err
	}
	disk := diskConfig{
		Version: 1, ClientID: cfg.ClientID, EncryptedAccessToken: access, EncryptedRefreshToken: refresh,
		ExpiresAt: cfg.ExpiresAt, UserID: cfg.UserID, UserName: cfg.UserName, Avatar: cfg.Avatar,
		FolderID: cfg.FolderID, FolderName: cfg.FolderName,
	}
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(s.path, data, 0o600); err != nil {
		return fmt.Errorf("save 115 account: %w", err)
	}
	s.cfg = cfg
	return nil
}

func (s *Store) encrypt(value, clientID, field string) (string, error) {
	if value == "" {
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
	sealed := aead.Seal(nonce, nonce, []byte(value), tokenAAD(clientID, field))
	return encryptedPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (s *Store) decrypt(value, clientID, field string) (string, error) {
	if value == "" {
		return "", nil
	}
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
		return "", errors.New("encrypted token is truncated")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], tokenAAD(clientID, field))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *Store) loadOrCreateKey() ([]byte, error) {
	key, err := os.ReadFile(s.keyPath)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("invalid 115 account key")
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

func tokenAAD(clientID, field string) []byte {
	return []byte("BossTransfer/account115/v1|" + field + "|" + clientID)
}

func folderID(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "0"
}

func folderName(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "根目录"
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bosstransfer-115-*")
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
