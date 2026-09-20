// Package downloaderconfig persists client-local downloader credentials and
// exposes a secret-free view suitable for HTTP responses.
package downloaderconfig

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

const (
	AuthPassword = "password"
	AuthAPIKey   = "api_key"

	encryptedPrefix  = "aesgcm:v1:"
	maxStateBytes    = 64 << 10
	maxURLBytes      = 2048
	maxUsernameBytes = 256
	maxSecretBytes   = 8192
	maxPathBytes     = 4096
	maxVersionBytes  = 256
)

// Config is the complete in-memory qBittorrent configuration. Password and
// APIKey are never serialized directly.
type Config struct {
	BaseURL  string `json:"-"`
	AuthMode string `json:"-"`
	Username string `json:"-"`
	Password string `json:"-"`
	APIKey   string `json:"-"`
	SavePath string `json:"-"`
}

// Update contains values supplied by a settings request. Empty secrets may be
// resolved from the current configuration only when BaseURL is unchanged.
type Update struct {
	BaseURL  string
	AuthMode string
	Username string
	Password string
	APIKey   string
	SavePath string
}

// Public is safe to return to a browser. It deliberately contains no secret
// material and only reports whether each secret exists.
type Public struct {
	BaseURL         string     `json:"base_url"`
	AuthMode        string     `json:"auth_mode,omitempty"`
	Username        string     `json:"username,omitempty"`
	PasswordPresent bool       `json:"password_present"`
	APIKeyPresent   bool       `json:"api_key_present"`
	SavePath        string     `json:"save_path,omitempty"`
	Available       bool       `json:"available"`
	Version         string     `json:"version,omitempty"`
	LastVerifiedAt  *time.Time `json:"last_verified_at,omitempty"`
}

type diskState struct {
	BaseURL           string     `json:"base_url"`
	AuthMode          string     `json:"auth_mode"`
	Username          string     `json:"username,omitempty"`
	EncryptedPassword string     `json:"encrypted_password,omitempty"`
	EncryptedAPIKey   string     `json:"encrypted_api_key,omitempty"`
	SavePath          string     `json:"save_path,omitempty"`
	Version           string     `json:"version,omitempty"`
	LastVerifiedAt    *time.Time `json:"last_verified_at,omitempty"`
}

type Store struct {
	mu      sync.RWMutex
	path    string
	keyPath string
	state   diskState
	config  Config
}

// Open loads a qBittorrent configuration. A missing file represents an
// unconfigured downloader and is not an error.
func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("qBittorrent 配置路径不能为空")
	}
	store := &Store{
		path:    path,
		keyPath: filepath.Join(filepath.Dir(path), "downloader-config.key"),
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 qBittorrent 配置: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxStateBytes {
		return nil, errors.New("qBittorrent 配置文件无效")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 qBittorrent 配置: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&store.state); err != nil {
		return nil, fmt.Errorf("解析 qBittorrent 配置: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return nil, fmt.Errorf("解析 qBittorrent 配置: %w", err)
	}
	password, err := store.decrypt("password", store.state.EncryptedPassword)
	if err != nil {
		return nil, fmt.Errorf("解密 qBittorrent 密码: %w", err)
	}
	apiKey, err := store.decrypt("api_key", store.state.EncryptedAPIKey)
	if err != nil {
		return nil, fmt.Errorf("解密 qBittorrent API Key: %w", err)
	}
	store.config = Config{
		BaseURL:  strings.TrimSpace(store.state.BaseURL),
		AuthMode: strings.TrimSpace(store.state.AuthMode),
		Username: strings.TrimSpace(store.state.Username),
		Password: password,
		APIKey:   strings.TrimSpace(apiKey),
		SavePath: strings.TrimSpace(store.state.SavePath),
	}
	if err := validateConfig(store.config); err != nil {
		return nil, fmt.Errorf("验证 qBittorrent 配置: %w", err)
	}
	if store.state.LastVerifiedAt == nil || strings.TrimSpace(store.state.Version) == "" {
		return nil, errors.New("验证 qBittorrent 配置: 缺少最近验证信息")
	}
	verifiedAt := store.state.LastVerifiedAt.UTC().Round(0)
	store.state.LastVerifiedAt = &verifiedAt
	return store, nil
}

// Public returns the current secret-free settings view.
func (s *Store) Public() Public {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return publicFrom(s.config, s.state)
}

// Config returns a copy of the current in-memory configuration for later
// downloader operations.
func (s *Store) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

// Resolve builds a complete candidate without modifying the store. Blank
// secrets are reused only for the exact same normalized BaseURL and auth mode.
func (s *Store) Resolve(update Update) (Config, error) {
	update.BaseURL = strings.TrimRight(strings.TrimSpace(update.BaseURL), "/")
	update.AuthMode = strings.ToLower(strings.TrimSpace(update.AuthMode))
	update.Username = strings.TrimSpace(update.Username)
	update.APIKey = strings.TrimSpace(update.APIKey)
	update.SavePath = strings.TrimSpace(update.SavePath)
	if update.BaseURL == "" {
		return Config{}, errors.New("qBittorrent 地址不能为空")
	}
	if update.AuthMode != "" && update.AuthMode != AuthPassword && update.AuthMode != AuthAPIKey {
		return Config{}, errors.New("qBittorrent 认证方式必须是 password 或 api_key")
	}
	if update.Password != "" && update.APIKey != "" {
		return Config{}, errors.New("qBittorrent 密码和 API Key 不能同时填写")
	}

	s.mu.RLock()
	current := s.config
	s.mu.RUnlock()
	sameAddress := current.BaseURL != "" && update.BaseURL == current.BaseURL

	mode := update.AuthMode
	if mode == "" {
		switch {
		case update.APIKey != "":
			mode = AuthAPIKey
		case update.Password != "" || update.Username != "":
			mode = AuthPassword
		case sameAddress:
			mode = current.AuthMode
		}
	}
	if mode == "" {
		return Config{}, errors.New("请选择 qBittorrent 账号密码或 API Key 认证")
	}

	candidate := Config{BaseURL: update.BaseURL, AuthMode: mode, SavePath: update.SavePath}
	if candidate.SavePath == "" && sameAddress {
		candidate.SavePath = current.SavePath
	}
	switch mode {
	case AuthPassword:
		if update.APIKey != "" {
			return Config{}, errors.New("账号密码认证不能同时填写 API Key")
		}
		candidate.Username = update.Username
		candidate.Password = update.Password
		if sameAddress && current.AuthMode == AuthPassword {
			if candidate.Username == "" {
				candidate.Username = current.Username
			}
			if candidate.Password == "" {
				candidate.Password = current.Password
			}
		}
	case AuthAPIKey:
		if update.Password != "" {
			return Config{}, errors.New("API Key 认证不能同时填写密码")
		}
		candidate.APIKey = update.APIKey
		if candidate.APIKey == "" && sameAddress && current.AuthMode == AuthAPIKey {
			candidate.APIKey = current.APIKey
		}
	}
	if err := validateConfig(candidate); err != nil {
		return Config{}, err
	}
	return candidate, nil
}

// SaveVerified atomically persists a candidate after its remote Test call has
// succeeded. Calling it is the only way to mark a configuration available.
func (s *Store) SaveVerified(config Config, version string, verifiedAt time.Time) error {
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.AuthMode = strings.ToLower(strings.TrimSpace(config.AuthMode))
	config.Username = strings.TrimSpace(config.Username)
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.SavePath = strings.TrimSpace(config.SavePath)
	version = strings.TrimSpace(version)
	if err := validateConfig(config); err != nil {
		return err
	}
	if version == "" || verifiedAt.IsZero() {
		return errors.New("qBittorrent 验证结果不完整")
	}
	if len([]byte(version)) > maxVersionBytes {
		return errors.New("qBittorrent 版本信息过长")
	}
	verifiedAt = verifiedAt.UTC().Round(0)

	s.mu.Lock()
	defer s.mu.Unlock()
	encryptedPassword, err := s.encrypt("password", config.Password)
	if err != nil {
		return err
	}
	encryptedAPIKey, err := s.encrypt("api_key", config.APIKey)
	if err != nil {
		return err
	}
	next := diskState{
		BaseURL:           config.BaseURL,
		AuthMode:          config.AuthMode,
		Username:          config.Username,
		EncryptedPassword: encryptedPassword,
		EncryptedAPIKey:   encryptedAPIKey,
		SavePath:          config.SavePath,
		Version:           version,
		LastVerifiedAt:    &verifiedAt,
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxStateBytes {
		return errors.New("qBittorrent 配置内容过大")
	}
	if err := atomicWrite(s.path, data, 0o600); err != nil {
		return fmt.Errorf("保存 qBittorrent 配置: %w", err)
	}
	s.state = next
	s.config = config
	return nil
}

// Delete removes the saved downloader configuration. The encryption key is
// retained so a later configuration can be saved atomically without key races.
func (s *Store) Delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除 qBittorrent 配置: %w", err)
	}
	s.state = diskState{}
	s.config = Config{}
	return nil
}

func validateConfig(config Config) error {
	if config.BaseURL == "" {
		return errors.New("qBittorrent 地址不能为空")
	}
	if len([]byte(config.BaseURL)) > maxURLBytes || len([]byte(config.Username)) > maxUsernameBytes || len([]byte(config.Password)) > maxSecretBytes || len([]byte(config.APIKey)) > maxSecretBytes || len([]byte(config.SavePath)) > maxPathBytes {
		return errors.New("qBittorrent 配置字段过长")
	}
	switch config.AuthMode {
	case AuthPassword:
		if config.Username == "" || config.Password == "" {
			return errors.New("qBittorrent 账号和密码不能为空")
		}
		if config.APIKey != "" {
			return errors.New("账号密码认证不能保存 API Key")
		}
	case AuthAPIKey:
		if strings.TrimSpace(config.APIKey) == "" {
			return errors.New("qBittorrent API Key 不能为空")
		}
		if config.Username != "" || config.Password != "" {
			return errors.New("API Key 认证不能保存账号密码")
		}
	default:
		return errors.New("qBittorrent 认证方式无效")
	}
	return nil
}

func publicFrom(config Config, state diskState) Public {
	return Public{
		BaseURL:         config.BaseURL,
		AuthMode:        config.AuthMode,
		Username:        config.Username,
		PasswordPresent: config.Password != "",
		APIKeyPresent:   config.APIKey != "",
		SavePath:        config.SavePath,
		Available:       state.LastVerifiedAt != nil && strings.TrimSpace(state.Version) != "",
		Version:         state.Version,
		LastVerifiedAt:  cloneTime(state.LastVerifiedAt),
	}
}

func (s *Store) encrypt(kind, plain string) (string, error) {
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
	sealed := aead.Seal(nonce, nonce, []byte(plain), []byte("bosstransfer-downloaderconfig-v1:"+kind))
	return encryptedPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (s *Store) decrypt(kind, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, encryptedPrefix) {
		return "", errors.New("不支持的加密格式")
	}
	sealed, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, encryptedPrefix))
	if err != nil {
		return "", err
	}
	key, err := os.ReadFile(s.keyPath)
	if err != nil {
		return "", err
	}
	if len(key) != 32 {
		return "", errors.New("qBittorrent 配置密钥无效")
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
		return "", errors.New("加密内容不完整")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte("bosstransfer-downloaderconfig-v1:"+kind))
	if err != nil {
		return "", errors.New("加密内容校验失败")
	}
	return string(plain), nil
}

func (s *Store) loadOrCreateKey() ([]byte, error) {
	key, err := os.ReadFile(s.keyPath)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("qBittorrent 配置密钥无效")
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
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".bosstransfer-downloader-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	closeWithError := func(cause error) error {
		if closeErr := temporary.Close(); cause == nil {
			return closeErr
		}
		return cause
	}
	if err := temporary.Chmod(mode); err != nil {
		return closeWithError(err)
	}
	if _, err := temporary.Write(data); err != nil {
		return closeWithError(err)
	}
	if err := temporary.Sync(); err != nil {
		return closeWithError(err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err == nil {
		return nil
	}
	backup := path + ".previous"
	_ = os.Remove(backup)
	if err := os.Rename(path, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Rename(backup, path)
		return err
	}
	_ = os.Remove(backup)
	return nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("配置文件包含多个 JSON 值")
	}
	return err
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
