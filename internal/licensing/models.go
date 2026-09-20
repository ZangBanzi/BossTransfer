package licensing

import (
	"errors"
	"time"
)

const DefaultLeaseDuration = 24 * time.Hour

var (
	ErrInvalidInput          = errors.New("invalid licensing input")
	ErrPepperRequired        = errors.New("licensing pepper is required")
	ErrLicenseNotFound       = errors.New("license not found")
	ErrLicenseRevoked        = errors.New("license revoked")
	ErrLicenseExpired        = errors.New("license expired")
	ErrAuthorizationCode     = errors.New("authorization code is invalid")
	ErrAuthorizationCodeUsed = errors.New("authorization code has already been used")
	ErrDeviceLimitReached    = errors.New("device limit reached")
	ErrDeviceNotFound        = errors.New("device not found")
	ErrDeviceReleased        = errors.New("device released")
	ErrDeviceRevoked         = errors.New("device revoked")
	ErrDeviceToken           = errors.New("device token is invalid")
	ErrDeviceLeaseExpired    = errors.New("device lease expired")
	ErrInstallationConflict  = errors.New("installation identity conflicts with an existing device")
)

type LicenseStatus string

const (
	LicenseActive  LicenseStatus = "active"
	LicenseRevoked LicenseStatus = "revoked"
	LicenseExpired LicenseStatus = "expired"
)

type DeviceStatus string

const (
	DeviceActive   DeviceStatus = "active"
	DeviceReleased DeviceStatus = "released"
	DeviceRevoked  DeviceStatus = "revoked"
)

type CodeStatus string

const (
	CodeAvailable CodeStatus = "available"
	CodeConsumed  CodeStatus = "consumed"
)

// CreateLicenseRequest creates one customer license and its first one-time
// authorization code. ExpiresAt must be in the future when supplied.
type CreateLicenseRequest struct {
	Customer    string     `json:"customer"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	DeviceLimit int        `json:"device_limit"`
}

// UpdateLicenseRequest changes the expiry and device limit delivered to active
// clients on their next heartbeat.
type UpdateLicenseRequest struct {
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	DeviceLimit int        `json:"device_limit"`
}

type PublicLicense struct {
	ID            string          `json:"id"`
	Customer      string          `json:"customer"`
	Status        LicenseStatus   `json:"status"`
	ExpiresAt     *time.Time      `json:"expires_at,omitempty"`
	DeviceLimit   int             `json:"device_limit"`
	ActiveDevices int             `json:"active_devices"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	RevokedAt     *time.Time      `json:"revoked_at,omitempty"`
	Codes         []CodeSummary   `json:"codes"`
	Devices       []DeviceSummary `json:"devices"`
}

// IssuedCode is the only model that exposes an authorization code. Callers
// must display it once and must not log or persist AuthorizationCode.
type IssuedCode struct {
	LicenseID         string    `json:"license_id"`
	AuthorizationCode string    `json:"authorization_code"`
	Prefix            string    `json:"prefix"`
	Suffix            string    `json:"suffix"`
	CreatedAt         time.Time `json:"created_at"`
}

type CreateLicenseResponse struct {
	License PublicLicense `json:"license"`
	Code    IssuedCode    `json:"code"`
}

type CodeSummary struct {
	ID         string     `json:"id"`
	Prefix     string     `json:"prefix"`
	Suffix     string     `json:"suffix"`
	Status     CodeStatus `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
	DeviceID   string     `json:"device_id,omitempty"`
}

type DeviceSummary struct {
	ID              string       `json:"id"`
	InstallationID  string       `json:"installation_id"`
	PublicKey       string       `json:"public_key"`
	Label           string       `json:"label,omitempty"`
	Status          DeviceStatus `json:"status"`
	ActivatedAt     time.Time    `json:"activated_at"`
	LastHeartbeatAt *time.Time   `json:"last_heartbeat_at,omitempty"`
	LeaseExpiresAt  time.Time    `json:"lease_expires_at"`
	ReleasedAt      *time.Time   `json:"released_at,omitempty"`
	RevokedAt       *time.Time   `json:"revoked_at,omitempty"`
}

type ActivateRequest struct {
	Code           string `json:"code"`
	InstallationID string `json:"installation_id"`
	PublicKey      string `json:"public_key"`
	DeviceLabel    string `json:"device_label,omitempty"`
}

// PublicAuthorization is shared by activation and heartbeat responses. It
// intentionally contains neither the authorization code nor the device token.
type PublicAuthorization struct {
	ID        string        `json:"id"`
	Status    LicenseStatus `json:"status"`
	Customer  string        `json:"customer"`
	ExpiresAt *time.Time    `json:"expires_at,omitempty"`
}

// ActivationResponse returns DeviceToken only from the first successful use
// of a one-time authorization code. The same code is never accepted again.
type ActivationResponse struct {
	DeviceID       string              `json:"device_id"`
	DeviceToken    string              `json:"device_token"`
	License        PublicAuthorization `json:"license"`
	LeaseExpiresAt time.Time           `json:"lease_expires_at"`
}

type HeartbeatResponse struct {
	DeviceID       string              `json:"device_id"`
	License        PublicAuthorization `json:"license"`
	LeaseExpiresAt time.Time           `json:"lease_expires_at"`
}
