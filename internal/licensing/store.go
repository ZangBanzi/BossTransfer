package licensing

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	diskVersion                = 1
	encryptedDeviceTokenPrefix = "aesgcm:v1:"
)

type Store struct {
	mu            sync.RWMutex
	path          string
	pepper        []byte
	leaseDuration time.Duration
	now           func() time.Time
	state         diskState
}

type diskState struct {
	Version  int                       `json:"version"`
	Licenses map[string]*licenseRecord `json:"licenses"`
	Codes    map[string]*codeRecord    `json:"codes"`
	Devices  map[string]*deviceRecord  `json:"devices"`
}

type licenseRecord struct {
	ID                 string        `json:"id"`
	Customer           string        `json:"customer"`
	ExternalUser       string        `json:"external_user,omitempty"`
	Status             LicenseStatus `json:"status"`
	ExpiresAt          *time.Time    `json:"expires_at,omitempty"`
	DeviceLimit        int           `json:"device_limit"`
	AllowedDownloaders []string      `json:"allowed_downloaders"`
	CreatedAt          time.Time     `json:"created_at"`
	UpdatedAt          time.Time     `json:"updated_at"`
	RevokedAt          *time.Time    `json:"revoked_at,omitempty"`
}

type codeRecord struct {
	ID         string     `json:"id"`
	LicenseID  string     `json:"license_id"`
	Digest     string     `json:"digest"`
	Prefix     string     `json:"prefix"`
	Suffix     string     `json:"suffix"`
	Status     CodeStatus `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
	DeviceID   string     `json:"device_id,omitempty"`
}

type deviceRecord struct {
	ID              string       `json:"id"`
	LicenseID       string       `json:"license_id"`
	InstallationID  string       `json:"installation_id"`
	PublicKey       string       `json:"public_key"`
	Label           string       `json:"label,omitempty"`
	TokenDigest     string       `json:"token_digest"`
	EncryptedToken  string       `json:"encrypted_token"`
	Status          DeviceStatus `json:"status"`
	ActivatedAt     time.Time    `json:"activated_at"`
	LastHeartbeatAt *time.Time   `json:"last_heartbeat_at,omitempty"`
	LeaseExpiresAt  time.Time    `json:"lease_expires_at"`
	ReleasedAt      *time.Time   `json:"released_at,omitempty"`
	RevokedAt       *time.Time   `json:"revoked_at,omitempty"`
}

// NewStore loads or creates a single-process JSON authorization store. Pepper
// is used as the HMAC key for both authorization-code and device-token
// digests. The caller owns key generation and durable secret storage.
func NewStore(path string, pepper []byte) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("%w: store path is required", ErrInvalidInput)
	}
	if len(pepper) < 16 {
		return nil, ErrPepperRequired
	}
	s := &Store{
		path:          path,
		pepper:        append([]byte(nil), pepper...),
		leaseDuration: DefaultLeaseDuration,
		now:           time.Now,
		state:         newDiskState(),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// CreateLicense creates a customer license and the first one-time code.
func (s *Store) CreateLicense(request CreateLicenseRequest) (CreateLicenseResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.currentTime()
	normalized, err := normalizeCreateRequest(request, now)
	if err != nil {
		return CreateLicenseResponse{}, err
	}
	next := cloneDiskState(s.state)
	licenseID, err := randomID("lic_", 16)
	if err != nil {
		return CreateLicenseResponse{}, err
	}
	record := &licenseRecord{
		ID:          licenseID,
		Customer:    normalized.Customer,
		Status:      LicenseActive,
		ExpiresAt:   cloneTime(normalized.ExpiresAt),
		DeviceLimit: normalized.DeviceLimit,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	next.Licenses[licenseID] = record
	issued, err := s.issueCodeInState(&next, licenseID, now)
	if err != nil {
		return CreateLicenseResponse{}, err
	}
	if err := s.commit(next); err != nil {
		return CreateLicenseResponse{}, err
	}
	return CreateLicenseResponse{
		License: s.publicLicenseLocked(record.ID, now),
		Code:    issued,
	}, nil
}

// UpdateLicense changes the limits of an active license. Active devices pick
// up the new downloader policy and expiry on their next heartbeat.
func (s *Store) UpdateLicense(licenseID string, request UpdateLicenseRequest) (PublicLicense, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	licenseID = strings.TrimSpace(licenseID)
	now := s.currentTime()
	license, ok := s.state.Licenses[licenseID]
	if !ok {
		return PublicLicense{}, ErrLicenseNotFound
	}
	if license.Status == LicenseRevoked {
		return PublicLicense{}, ErrLicenseRevoked
	}
	if request.DeviceLimit < 1 || request.DeviceLimit > 10000 {
		return PublicLicense{}, fmt.Errorf("%w: device_limit must be between 1 and 10000", ErrInvalidInput)
	}
	activeDevices := activeDeviceCount(s.state, licenseID)
	if request.DeviceLimit < activeDevices {
		return PublicLicense{}, fmt.Errorf("%w: device_limit cannot be lower than active devices", ErrInvalidInput)
	}
	var expiresAt *time.Time
	if request.ExpiresAt != nil {
		expires := request.ExpiresAt.UTC().Round(0)
		if !expires.After(now) {
			return PublicLicense{}, fmt.Errorf("%w: expires_at must be in the future", ErrInvalidInput)
		}
		expiresAt = &expires
	}
	next := cloneDiskState(s.state)
	nextLicense := next.Licenses[licenseID]
	nextLicense.ExpiresAt = cloneTime(expiresAt)
	nextLicense.DeviceLimit = request.DeviceLimit
	nextLicense.UpdatedAt = now
	for _, device := range next.Devices {
		if device.LicenseID == licenseID && device.Status == DeviceActive {
			device.LeaseExpiresAt = leaseExpiry(now, s.leaseDuration, expiresAt)
		}
	}
	if err := s.commit(next); err != nil {
		return PublicLicense{}, err
	}
	return s.publicLicenseLocked(licenseID, now), nil
}

// IssueCode creates another one-time code for an existing active license.
// DeviceLimit remains the maximum number of concurrently active devices.
func (s *Store) IssueCode(licenseID string) (IssuedCode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	licenseID = strings.TrimSpace(licenseID)
	now := s.currentTime()
	license, ok := s.state.Licenses[licenseID]
	if !ok {
		return IssuedCode{}, ErrLicenseNotFound
	}
	if err := validateLicenseUsable(license, now); err != nil {
		return IssuedCode{}, err
	}
	next := cloneDiskState(s.state)
	issued, err := s.issueCodeInState(&next, licenseID, now)
	if err != nil {
		return IssuedCode{}, err
	}
	if err := s.commit(next); err != nil {
		return IssuedCode{}, err
	}
	return issued, nil
}

// Activate consumes a one-time authorization code and binds it to one
// installation/public-key pair. A consumed code is never accepted again;
// losing an activation response requires the manager to issue a new code.
func (s *Store) Activate(request ActivateRequest) (ActivationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	request.Code = normalizeCode(request.Code)
	request.InstallationID = strings.TrimSpace(request.InstallationID)
	request.PublicKey = strings.TrimSpace(request.PublicKey)
	request.DeviceLabel = strings.TrimSpace(request.DeviceLabel)
	if request.Code == "" || request.InstallationID == "" || request.PublicKey == "" {
		return ActivationResponse{}, fmt.Errorf("%w: code, installation_id and public_key are required", ErrInvalidInput)
	}
	if len(request.InstallationID) > 256 || len(request.PublicKey) > 4096 || len(request.DeviceLabel) > 256 {
		return ActivationResponse{}, fmt.Errorf("%w: activation field is too long", ErrInvalidInput)
	}

	now := s.currentTime()
	digest := s.digest("code", request.Code)
	code, ok := findCodeByDigest(s.state, digest)
	if !ok {
		return ActivationResponse{}, ErrAuthorizationCode
	}
	license, ok := s.state.Licenses[code.LicenseID]
	if !ok {
		return ActivationResponse{}, ErrLicenseNotFound
	}
	if err := validateLicenseUsable(license, now); err != nil {
		return ActivationResponse{}, err
	}
	if conflict := findInstallationConflict(s.state, request.InstallationID, request.PublicKey); conflict {
		return ActivationResponse{}, ErrInstallationConflict
	}

	if code.Status == CodeConsumed {
		return ActivationResponse{}, ErrAuthorizationCodeUsed
	}

	next := cloneDiskState(s.state)
	nextLicense := next.Licenses[license.ID]
	device := findLicenseDevice(next, license.ID, request.InstallationID, request.PublicKey)
	if device == nil || device.Status != DeviceActive {
		if activeDeviceCount(next, license.ID) >= license.DeviceLimit {
			return ActivationResponse{}, ErrDeviceLimitReached
		}
	}

	deviceToken, err := randomSecret(32)
	if err != nil {
		return ActivationResponse{}, err
	}
	encryptedToken, err := s.encryptDeviceToken(deviceToken)
	if err != nil {
		return ActivationResponse{}, err
	}
	leaseExpiresAt := leaseExpiry(now, s.leaseDuration, nextLicense.ExpiresAt)
	if device == nil {
		deviceID, idErr := randomID("dev_", 16)
		if idErr != nil {
			return ActivationResponse{}, idErr
		}
		device = &deviceRecord{
			ID:             deviceID,
			LicenseID:      license.ID,
			InstallationID: request.InstallationID,
			PublicKey:      request.PublicKey,
			ActivatedAt:    now,
		}
		next.Devices[deviceID] = device
	}
	device.Label = request.DeviceLabel
	device.TokenDigest = s.digest("token", deviceToken)
	device.EncryptedToken = encryptedToken
	device.Status = DeviceActive
	device.LeaseExpiresAt = leaseExpiresAt
	device.ReleasedAt = nil
	device.RevokedAt = nil

	nextCode := next.Codes[code.ID]
	nextCode.Status = CodeConsumed
	nextCode.DeviceID = device.ID
	nextCode.ConsumedAt = timePointer(now)
	nextLicense.UpdatedAt = now
	if err := s.commit(next); err != nil {
		return ActivationResponse{}, err
	}
	return activationResponse(deviceToken, device, nextLicense, now), nil
}

// AuthorizeDevice validates an HTTP Authorization header, the current device
// lease, and its license without changing in-memory or persisted state. Catalog
// requests use this read-only path; Heartbeat remains responsible for renewal.
func (s *Store) AuthorizeDevice(deviceID, bearerToken string) (HeartbeatResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := s.currentTime()
	device, license, err := s.validateDeviceCredentialLocked(deviceID, bearerToken, now)
	if err != nil {
		return HeartbeatResponse{}, err
	}
	if device.LeaseExpiresAt.IsZero() || !now.Before(device.LeaseExpiresAt) {
		return HeartbeatResponse{}, ErrDeviceLeaseExpired
	}
	return HeartbeatResponse{
		DeviceID:       device.ID,
		License:        publicAuthorization(license, now),
		LeaseExpiresAt: device.LeaseExpiresAt,
	}, nil
}

// Heartbeat validates an HTTP Authorization header in the form
// "Bearer <device-token>" and renews the device lease. An expired device lease
// may still be renewed when the device and license otherwise remain active.
func (s *Store) Heartbeat(deviceID, bearerToken string) (HeartbeatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.currentTime()
	device, license, err := s.validateDeviceCredentialLocked(deviceID, bearerToken, now)
	if err != nil {
		return HeartbeatResponse{}, err
	}

	next := cloneDiskState(s.state)
	nextDevice := next.Devices[device.ID]
	nextLicense := next.Licenses[license.ID]
	nextDevice.LastHeartbeatAt = timePointer(now)
	nextDevice.LeaseExpiresAt = leaseExpiry(now, s.leaseDuration, nextLicense.ExpiresAt)
	nextLicense.UpdatedAt = now
	if err := s.commit(next); err != nil {
		return HeartbeatResponse{}, err
	}
	return HeartbeatResponse{
		DeviceID:       nextDevice.ID,
		License:        publicAuthorization(nextLicense, now),
		LeaseExpiresAt: nextDevice.LeaseExpiresAt,
	}, nil
}

// validateDeviceCredentialLocked performs the checks shared by read-only
// authorization and lease renewal. The caller must hold s.mu for reading or
// writing.
func (s *Store) validateDeviceCredentialLocked(deviceID, bearerToken string, now time.Time) (*deviceRecord, *licenseRecord, error) {
	deviceID = strings.TrimSpace(deviceID)
	token, err := parseBearerToken(bearerToken)
	if err != nil || deviceID == "" {
		return nil, nil, ErrDeviceToken
	}
	device, ok := s.state.Devices[deviceID]
	if !ok {
		return nil, nil, ErrDeviceNotFound
	}
	if !hmac.Equal([]byte(device.TokenDigest), []byte(s.digest("token", token))) {
		return nil, nil, ErrDeviceToken
	}
	license, ok := s.state.Licenses[device.LicenseID]
	if !ok {
		return nil, nil, ErrLicenseNotFound
	}
	if err := validateLicenseUsable(license, now); err != nil {
		return nil, nil, err
	}
	switch device.Status {
	case DeviceReleased:
		return nil, nil, ErrDeviceReleased
	case DeviceRevoked:
		return nil, nil, ErrDeviceRevoked
	case DeviceActive:
	default:
		return nil, nil, ErrDeviceNotFound
	}
	return device, license, nil
}

// RevokeLicense revokes a license and every currently active device bound to
// it. It is idempotent.
func (s *Store) RevokeLicense(licenseID string) (PublicLicense, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	licenseID = strings.TrimSpace(licenseID)
	license, ok := s.state.Licenses[licenseID]
	if !ok {
		return PublicLicense{}, ErrLicenseNotFound
	}
	now := s.currentTime()
	if license.Status == LicenseRevoked {
		return s.publicLicenseLocked(licenseID, now), nil
	}
	next := cloneDiskState(s.state)
	nextLicense := next.Licenses[licenseID]
	nextLicense.Status = LicenseRevoked
	nextLicense.RevokedAt = timePointer(now)
	nextLicense.UpdatedAt = now
	for _, device := range next.Devices {
		if device.LicenseID == licenseID && device.Status == DeviceActive {
			device.Status = DeviceRevoked
			device.RevokedAt = timePointer(now)
		}
	}
	if err := s.commit(next); err != nil {
		return PublicLicense{}, err
	}
	return s.publicLicenseLocked(licenseID, now), nil
}

// ReleaseDevice invalidates one device token and frees its license slot. It is
// idempotent for an already released device.
func (s *Store) ReleaseDevice(deviceID string) (DeviceSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	deviceID = strings.TrimSpace(deviceID)
	device, ok := s.state.Devices[deviceID]
	if !ok {
		return DeviceSummary{}, ErrDeviceNotFound
	}
	if device.Status == DeviceReleased {
		return deviceSummary(device), nil
	}
	now := s.currentTime()
	next := cloneDiskState(s.state)
	nextDevice := next.Devices[deviceID]
	nextDevice.Status = DeviceReleased
	nextDevice.TokenDigest = ""
	nextDevice.EncryptedToken = ""
	nextDevice.ReleasedAt = timePointer(now)
	if license := next.Licenses[nextDevice.LicenseID]; license != nil {
		license.UpdatedAt = now
	}
	if err := s.commit(next); err != nil {
		return DeviceSummary{}, err
	}
	return deviceSummary(nextDevice), nil
}

func (s *Store) ListLicenses() []PublicLicense {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := s.currentTime()
	ids := make([]string, 0, len(s.state.Licenses))
	for id := range s.state.Licenses {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]PublicLicense, 0, len(ids))
	for _, id := range ids {
		result = append(result, s.publicLicenseLocked(id, now))
	}
	return result
}

func (s *Store) GetLicense(licenseID string) (PublicLicense, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	licenseID = strings.TrimSpace(licenseID)
	if _, ok := s.state.Licenses[licenseID]; !ok {
		return PublicLicense{}, ErrLicenseNotFound
	}
	return s.publicLicenseLocked(licenseID, s.currentTime()), nil
}

func (s *Store) issueCodeInState(state *diskState, licenseID string, now time.Time) (IssuedCode, error) {
	for attempts := 0; attempts < 8; attempts++ {
		plain, err := randomAuthorizationCode()
		if err != nil {
			return IssuedCode{}, err
		}
		digest := s.digest("code", plain)
		if _, exists := findCodeByDigest(*state, digest); exists {
			continue
		}
		codeID, err := randomID("code_", 16)
		if err != nil {
			return IssuedCode{}, err
		}
		compact := strings.ReplaceAll(plain, "-", "")
		prefix := compact
		if len(prefix) > 8 {
			prefix = prefix[:8]
		}
		suffix := compact
		if len(suffix) > 6 {
			suffix = suffix[len(suffix)-6:]
		}
		state.Codes[codeID] = &codeRecord{
			ID:        codeID,
			LicenseID: licenseID,
			Digest:    digest,
			Prefix:    prefix,
			Suffix:    suffix,
			Status:    CodeAvailable,
			CreatedAt: now,
		}
		return IssuedCode{
			LicenseID:         licenseID,
			AuthorizationCode: plain,
			Prefix:            prefix,
			Suffix:            suffix,
			CreatedAt:         now,
		}, nil
	}
	return IssuedCode{}, errors.New("could not generate a unique authorization code")
}

func (s *Store) publicLicenseLocked(licenseID string, now time.Time) PublicLicense {
	license := s.state.Licenses[licenseID]
	result := PublicLicense{
		ID:            license.ID,
		Customer:      license.Customer,
		Status:        effectiveLicenseStatus(license, now),
		ExpiresAt:     cloneTime(license.ExpiresAt),
		DeviceLimit:   license.DeviceLimit,
		ActiveDevices: activeDeviceCount(s.state, license.ID),
		CreatedAt:     license.CreatedAt,
		UpdatedAt:     license.UpdatedAt,
		RevokedAt:     cloneTime(license.RevokedAt),
		Codes:         make([]CodeSummary, 0),
		Devices:       make([]DeviceSummary, 0),
	}
	for _, code := range s.state.Codes {
		if code.LicenseID == license.ID {
			result.Codes = append(result.Codes, codeSummary(code))
		}
	}
	for _, device := range s.state.Devices {
		if device.LicenseID == license.ID {
			result.Devices = append(result.Devices, deviceSummary(device))
		}
	}
	sort.Slice(result.Codes, func(i, j int) bool {
		if result.Codes[i].CreatedAt.Equal(result.Codes[j].CreatedAt) {
			return result.Codes[i].ID < result.Codes[j].ID
		}
		return result.Codes[i].CreatedAt.Before(result.Codes[j].CreatedAt)
	})
	sort.Slice(result.Devices, func(i, j int) bool {
		if result.Devices[i].ActivatedAt.Equal(result.Devices[j].ActivatedAt) {
			return result.Devices[i].ID < result.Devices[j].ID
		}
		return result.Devices[i].ActivatedAt.Before(result.Devices[j].ActivatedAt)
	})
	return result
}

func (s *Store) digest(kind, secret string) string {
	mac := hmac.New(sha256.New, s.pepper)
	_, _ = io.WriteString(mac, kind)
	_, _ = io.WriteString(mac, "\x00")
	_, _ = io.WriteString(mac, secret)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Store) encryptDeviceToken(token string) (string, error) {
	key := s.deviceEncryptionKey()
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(token), []byte("bosstransfer-device-token-v1"))
	return encryptedDeviceTokenPrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (s *Store) decryptDeviceToken(value string) (string, error) {
	if !strings.HasPrefix(value, encryptedDeviceTokenPrefix) {
		return "", errors.New("unsupported encrypted device token format")
	}
	sealed, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, encryptedDeviceTokenPrefix))
	if err != nil {
		return "", errors.New("invalid encrypted device token")
	}
	key := s.deviceEncryptionKey()
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < aead.NonceSize() {
		return "", errors.New("encrypted device token is truncated")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte("bosstransfer-device-token-v1"))
	if err != nil {
		return "", errors.New("encrypted device token authentication failed")
	}
	return string(plain), nil
}

func (s *Store) deviceEncryptionKey() [sha256.Size]byte {
	input := make([]byte, 0, len(s.pepper)+40)
	input = append(input, "bosstransfer-token-encryption-v1\x00"...)
	input = append(input, s.pepper...)
	return sha256.Sum256(input)
}

func (s *Store) currentTime() time.Time {
	return s.now().UTC().Round(0)
}

func (s *Store) commit(next diskState) error {
	if err := writeDiskState(s.path, next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			backupPath := s.path + ".previous"
			backup, backupErr := os.ReadFile(backupPath)
			if errors.Is(backupErr, os.ErrNotExist) {
				return nil
			}
			if backupErr != nil {
				return fmt.Errorf("recover licensing store: %w", backupErr)
			}
			data = backup
			if renameErr := os.Rename(backupPath, s.path); renameErr != nil {
				return fmt.Errorf("restore licensing store: %w", renameErr)
			}
		} else {
			return fmt.Errorf("read licensing store: %w", err)
		}
	}
	var state diskState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode licensing store: %w", err)
	}
	if state.Version != diskVersion {
		return fmt.Errorf("unsupported licensing store version %d", state.Version)
	}
	initializeDiskState(&state)
	if err := validateDiskState(state); err != nil {
		return fmt.Errorf("validate licensing store: %w", err)
	}
	for _, device := range state.Devices {
		if device.Status != DeviceActive {
			continue
		}
		token, err := s.decryptDeviceToken(device.EncryptedToken)
		if err != nil || !hmac.Equal([]byte(device.TokenDigest), []byte(s.digest("token", token))) {
			return errors.New("validate licensing store: device token cannot be recovered with the supplied pepper")
		}
	}
	s.state = state
	return nil
}

func writeDiskState(path string, state diskState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode licensing store: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create licensing directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".licensing-*.tmp")
	if err != nil {
		return fmt.Errorf("create licensing temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("protect licensing temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write licensing temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync licensing temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close licensing temporary file: %w", err)
	}
	if err := replaceFile(tmpPath, path); err != nil {
		return fmt.Errorf("replace licensing store: %w", err)
	}
	return nil
}

func replaceFile(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	backup := destination + ".previous"
	_ = os.Remove(backup)
	if err := os.Rename(destination, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		_ = os.Rename(backup, destination)
		return err
	}
	_ = os.Remove(backup)
	return nil
}

func newDiskState() diskState {
	return diskState{
		Version:  diskVersion,
		Licenses: make(map[string]*licenseRecord),
		Codes:    make(map[string]*codeRecord),
		Devices:  make(map[string]*deviceRecord),
	}
}

func initializeDiskState(state *diskState) {
	if state.Licenses == nil {
		state.Licenses = make(map[string]*licenseRecord)
	}
	if state.Codes == nil {
		state.Codes = make(map[string]*codeRecord)
	}
	if state.Devices == nil {
		state.Devices = make(map[string]*deviceRecord)
	}
}

func cloneDiskState(state diskState) diskState {
	clone := newDiskState()
	clone.Version = state.Version
	for id, license := range state.Licenses {
		copy := *license
		copy.ExpiresAt = cloneTime(license.ExpiresAt)
		copy.RevokedAt = cloneTime(license.RevokedAt)
		copy.AllowedDownloaders = cloneStrings(license.AllowedDownloaders)
		clone.Licenses[id] = &copy
	}
	for id, code := range state.Codes {
		copy := *code
		copy.ConsumedAt = cloneTime(code.ConsumedAt)
		clone.Codes[id] = &copy
	}
	for id, device := range state.Devices {
		copy := *device
		copy.LastHeartbeatAt = cloneTime(device.LastHeartbeatAt)
		copy.ReleasedAt = cloneTime(device.ReleasedAt)
		copy.RevokedAt = cloneTime(device.RevokedAt)
		clone.Devices[id] = &copy
	}
	return clone
}

func validateDiskState(state diskState) error {
	codeDigests := make(map[string]struct{}, len(state.Codes))
	for id, license := range state.Licenses {
		if license == nil || license.ID != id || license.Customer == "" || license.DeviceLimit < 1 || (license.Status != LicenseActive && license.Status != LicenseRevoked) {
			return errors.New("invalid license record")
		}
	}
	for id, code := range state.Codes {
		if code == nil || code.ID != id || code.Digest == "" || state.Licenses[code.LicenseID] == nil || (code.Status != CodeAvailable && code.Status != CodeConsumed) {
			return errors.New("invalid authorization code record")
		}
		if _, duplicate := codeDigests[code.Digest]; duplicate {
			return errors.New("duplicate authorization code digest")
		}
		codeDigests[code.Digest] = struct{}{}
		if code.Status == CodeConsumed {
			device := state.Devices[code.DeviceID]
			if device == nil || device.LicenseID != code.LicenseID || code.ConsumedAt == nil {
				return errors.New("invalid consumed authorization code")
			}
		}
	}
	for id, device := range state.Devices {
		if device == nil || device.ID != id || state.Licenses[device.LicenseID] == nil || device.InstallationID == "" || device.PublicKey == "" || (device.Status != DeviceActive && device.Status != DeviceReleased && device.Status != DeviceRevoked) {
			return errors.New("invalid device record")
		}
		if device.Status == DeviceActive && (device.TokenDigest == "" || device.EncryptedToken == "") {
			return errors.New("active device has incomplete token material")
		}
	}
	return nil
}

func normalizeCreateRequest(request CreateLicenseRequest, now time.Time) (CreateLicenseRequest, error) {
	request.Customer = strings.TrimSpace(request.Customer)
	if request.Customer == "" || len(request.Customer) > 256 {
		return CreateLicenseRequest{}, fmt.Errorf("%w: valid customer is required", ErrInvalidInput)
	}
	if request.DeviceLimit < 1 || request.DeviceLimit > 10000 {
		return CreateLicenseRequest{}, fmt.Errorf("%w: device_limit must be between 1 and 10000", ErrInvalidInput)
	}
	if request.ExpiresAt != nil {
		expires := request.ExpiresAt.UTC().Round(0)
		if !expires.After(now) {
			return CreateLicenseRequest{}, fmt.Errorf("%w: expires_at must be in the future", ErrInvalidInput)
		}
		request.ExpiresAt = &expires
	}
	return request, nil
}

func validateLicenseUsable(license *licenseRecord, now time.Time) error {
	if license.Status == LicenseRevoked {
		return ErrLicenseRevoked
	}
	if license.ExpiresAt != nil && !now.Before(*license.ExpiresAt) {
		return ErrLicenseExpired
	}
	return nil
}

func effectiveLicenseStatus(license *licenseRecord, now time.Time) LicenseStatus {
	if license.Status == LicenseRevoked {
		return LicenseRevoked
	}
	if license.ExpiresAt != nil && !now.Before(*license.ExpiresAt) {
		return LicenseExpired
	}
	return LicenseActive
}

func publicAuthorization(license *licenseRecord, now time.Time) PublicAuthorization {
	return PublicAuthorization{
		ID:        license.ID,
		Status:    effectiveLicenseStatus(license, now),
		Customer:  license.Customer,
		ExpiresAt: cloneTime(license.ExpiresAt),
	}
}

func activationResponse(token string, device *deviceRecord, license *licenseRecord, now time.Time) ActivationResponse {
	return ActivationResponse{
		DeviceID:       device.ID,
		DeviceToken:    token,
		License:        publicAuthorization(license, now),
		LeaseExpiresAt: device.LeaseExpiresAt,
	}
}

func codeSummary(code *codeRecord) CodeSummary {
	return CodeSummary{
		ID:         code.ID,
		Prefix:     code.Prefix,
		Suffix:     code.Suffix,
		Status:     code.Status,
		CreatedAt:  code.CreatedAt,
		ConsumedAt: cloneTime(code.ConsumedAt),
		DeviceID:   code.DeviceID,
	}
}

func deviceSummary(device *deviceRecord) DeviceSummary {
	return DeviceSummary{
		ID:              device.ID,
		InstallationID:  device.InstallationID,
		PublicKey:       device.PublicKey,
		Label:           device.Label,
		Status:          device.Status,
		ActivatedAt:     device.ActivatedAt,
		LastHeartbeatAt: cloneTime(device.LastHeartbeatAt),
		LeaseExpiresAt:  device.LeaseExpiresAt,
		ReleasedAt:      cloneTime(device.ReleasedAt),
		RevokedAt:       cloneTime(device.RevokedAt),
	}
}

func findCodeByDigest(state diskState, digest string) (*codeRecord, bool) {
	for _, code := range state.Codes {
		if hmac.Equal([]byte(code.Digest), []byte(digest)) {
			return code, true
		}
	}
	return nil, false
}

func findLicenseDevice(state diskState, licenseID, installationID, publicKey string) *deviceRecord {
	for _, device := range state.Devices {
		if device.LicenseID == licenseID && device.InstallationID == installationID && device.PublicKey == publicKey {
			return device
		}
	}
	return nil
}

func findInstallationConflict(state diskState, installationID, publicKey string) bool {
	for _, device := range state.Devices {
		if device.InstallationID == installationID && device.PublicKey != publicKey {
			return true
		}
	}
	return false
}

func activeDeviceCount(state diskState, licenseID string) int {
	count := 0
	for _, device := range state.Devices {
		if device.LicenseID == licenseID && device.Status == DeviceActive {
			count++
		}
	}
	return count
}

func leaseExpiry(now time.Time, duration time.Duration, licenseExpiry *time.Time) time.Time {
	expires := now.Add(duration)
	if licenseExpiry != nil && expires.After(*licenseExpiry) {
		return *licenseExpiry
	}
	return expires
}

func parseBearerToken(value string) (string, error) {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", ErrDeviceToken
	}
	return parts[1], nil
}

func normalizeCode(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func randomAuthorizationCode() (string, error) {
	raw := make([]byte, 20) // 160 bits of entropy.
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	parts := make([]string, 0, 1+(len(encoded)+3)/4)
	parts = append(parts, "BT")
	for len(encoded) > 0 {
		width := 4
		if len(encoded) < width {
			width = len(encoded)
		}
		parts = append(parts, encoded[:width])
		encoded = encoded[width:]
	}
	return strings.Join(parts, "-"), nil
}

func randomSecret(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func randomID(prefix string, bytes int) (string, error) {
	secret, err := randomSecret(bytes)
	if err != nil {
		return "", err
	}
	return prefix + secret, nil
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string(nil), values...)
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}
