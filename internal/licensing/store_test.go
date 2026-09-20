package licensing

import (
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var testPepper = []byte("0123456789abcdef0123456789abcdef")

func newTestStore(t *testing.T) (*Store, string, *time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "licensing.json")
	store, err := NewStore(path, testPepper)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	return store, path, &now
}

func createTestLicense(t *testing.T, store *Store, limit int, expiresAt *time.Time) CreateLicenseResponse {
	t.Helper()
	created, err := store.CreateLicense(CreateLicenseRequest{
		Customer:    "示例客户",
		ExpiresAt:   expiresAt,
		DeviceLimit: limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func activateTestDevice(t *testing.T, store *Store, code, installationID string) ActivationResponse {
	t.Helper()
	activation, err := store.Activate(ActivateRequest{
		Code:           code,
		InstallationID: installationID,
		PublicKey:      "ed25519:" + installationID,
		DeviceLabel:    "NAS " + installationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return activation
}

func TestCreateActivateHeartbeatAndSecretBoundaries(t *testing.T) {
	store, path, _ := newTestStore(t)
	expires := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	created := createTestLicense(t, store, 2, &expires)
	compactCode := strings.ReplaceAll(created.Code.AuthorizationCode, "-", "")
	codeBytes, decodeErr := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.TrimPrefix(compactCode, "BT"))
	if decodeErr != nil || !strings.HasPrefix(compactCode, "BT") || len(codeBytes) < 16 {
		t.Fatalf("authorization code does not contain at least 128 bits: %q", created.Code.AuthorizationCode)
	}
	activation := activateTestDevice(t, store, created.Code.AuthorizationCode, "install-a")
	if activation.DeviceID == "" || activation.DeviceToken == "" {
		t.Fatalf("activation omitted device credentials: %#v", activation)
	}
	if activation.License.Status != LicenseActive || activation.License.Customer != "示例客户" {
		t.Fatalf("unexpected public authorization: %#v", activation.License)
	}

	heartbeat, err := store.Heartbeat(activation.DeviceID, "Bearer "+activation.DeviceToken)
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.License.Status != activation.License.Status || heartbeat.License.Customer != activation.License.Customer || heartbeat.LeaseExpiresAt.IsZero() {
		t.Fatalf("heartbeat state does not match activation: %#v", heartbeat)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), created.Code.AuthorizationCode) {
		t.Fatal("authorization code was stored in plaintext")
	}
	if strings.Contains(string(raw), activation.DeviceToken) {
		t.Fatal("device token was stored in plaintext")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("store permissions = %o, want 600", got)
	}

	listed := store.ListLicenses()
	listedJSON, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(listedJSON), created.Code.AuthorizationCode) || strings.Contains(string(listedJSON), activation.DeviceToken) {
		t.Fatalf("list response exposed a secret: %s", listedJSON)
	}
	if len(listed) != 1 || len(listed[0].Codes) != 1 || listed[0].Codes[0].Prefix == "" || listed[0].Codes[0].Suffix == "" {
		t.Fatalf("list omitted safe code metadata: %#v", listed)
	}
}

func TestAuthorizeDeviceIsReadOnlyAndLeaseBound(t *testing.T) {
	store, path, now := newTestStore(t)
	created := createTestLicense(t, store, 1, nil)
	activation := activateTestDevice(t, store, created.Code.AuthorizationCode, "install-read-only")

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := store.AuthorizeDevice(activation.DeviceID, "Bearer "+activation.DeviceToken)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.DeviceID != activation.DeviceID || authorization.LeaseExpiresAt != activation.LeaseExpiresAt {
		t.Fatalf("read-only authorization = %#v, activation = %#v", authorization, activation)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("read-only authorization rewrote the licensing store")
	}
	license, err := store.GetLicense(created.License.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(license.Devices) != 1 || license.Devices[0].LastHeartbeatAt != nil {
		t.Fatalf("read-only authorization changed heartbeat state: %#v", license.Devices)
	}
	if _, err := store.AuthorizeDevice(activation.DeviceID, "Bearer wrong-token"); !errors.Is(err, ErrDeviceToken) {
		t.Fatalf("invalid token authorization error = %v, want ErrDeviceToken", err)
	}

	*now = activation.LeaseExpiresAt
	beforeExpired, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AuthorizeDevice(activation.DeviceID, "Bearer "+activation.DeviceToken); !errors.Is(err, ErrDeviceLeaseExpired) {
		t.Fatalf("expired lease authorization error = %v, want ErrDeviceLeaseExpired", err)
	}
	afterExpired, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterExpired) != string(beforeExpired) {
		t.Fatal("rejected read-only authorization rewrote the licensing store")
	}

	renewed, err := store.Heartbeat(activation.DeviceID, "Bearer "+activation.DeviceToken)
	if err != nil {
		t.Fatalf("heartbeat could not renew an expired lease: %v", err)
	}
	if !renewed.LeaseExpiresAt.After(*now) {
		t.Fatalf("renewed lease expires at %v, want after %v", renewed.LeaseExpiresAt, *now)
	}
	if _, err := store.AuthorizeDevice(activation.DeviceID, "Bearer "+activation.DeviceToken); err != nil {
		t.Fatalf("authorization after heartbeat renewal failed: %v", err)
	}
	if _, err := store.RevokeLicense(created.License.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AuthorizeDevice(activation.DeviceID, "Bearer "+activation.DeviceToken); !errors.Is(err, ErrLicenseRevoked) {
		t.Fatalf("revoked license authorization error = %v, want ErrLicenseRevoked", err)
	}

	expiringStore, _, expiringNow := newTestStore(t)
	expiresAt := expiringNow.Add(time.Hour)
	expiringLicense := createTestLicense(t, expiringStore, 1, &expiresAt)
	expiringDevice := activateTestDevice(t, expiringStore, expiringLicense.Code.AuthorizationCode, "install-expiring")
	*expiringNow = expiresAt
	if _, err := expiringStore.AuthorizeDevice(expiringDevice.DeviceID, "Bearer "+expiringDevice.DeviceToken); !errors.Is(err, ErrLicenseExpired) {
		t.Fatalf("expired license authorization error = %v, want ErrLicenseExpired", err)
	}
}

func TestActivationCodeCannotBeReplayedBySameDevice(t *testing.T) {
	store, _, _ := newTestStore(t)
	created := createTestLicense(t, store, 1, nil)
	first := activateTestDevice(t, store, created.Code.AuthorizationCode, "install-a")
	_, err := store.Activate(ActivateRequest{
		Code: strings.ToLower(created.Code.AuthorizationCode), InstallationID: "install-a", PublicKey: "ed25519:install-a",
	})
	if !errors.Is(err, ErrAuthorizationCodeUsed) {
		t.Fatalf("same-device replay error = %v", err)
	}
	if _, err := store.Heartbeat(first.DeviceID, "Bearer "+first.DeviceToken); err != nil {
		t.Fatalf("original device token heartbeat failed: %v", err)
	}
	license, err := store.GetLicense(created.License.ID)
	if err != nil {
		t.Fatal(err)
	}
	if license.ActiveDevices != 1 || len(license.Devices) != 1 {
		t.Fatalf("replayed activation duplicated device: %#v", license)
	}
}

func TestDeviceLimitAndRelease(t *testing.T) {
	store, _, _ := newTestStore(t)
	created := createTestLicense(t, store, 1, nil)
	secondCode, err := store.IssueCode(created.License.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := activateTestDevice(t, store, created.Code.AuthorizationCode, "install-a")
	_, err = store.Activate(ActivateRequest{
		Code:           secondCode.AuthorizationCode,
		InstallationID: "install-b",
		PublicKey:      "ed25519:install-b",
	})
	if !errors.Is(err, ErrDeviceLimitReached) {
		t.Fatalf("second activation error = %v, want ErrDeviceLimitReached", err)
	}
	if _, err := store.ReleaseDevice(first.DeviceID); err != nil {
		t.Fatal(err)
	}
	second := activateTestDevice(t, store, secondCode.AuthorizationCode, "install-b")
	if second.DeviceID == first.DeviceID {
		t.Fatal("different installation unexpectedly reused device id")
	}
	if _, err := store.Heartbeat(first.DeviceID, "Bearer "+first.DeviceToken); !errors.Is(err, ErrDeviceToken) {
		t.Fatalf("released device heartbeat error = %v, want ErrDeviceToken", err)
	}
}

func TestConcurrentActivationCannotExceedDeviceLimit(t *testing.T) {
	store, _, _ := newTestStore(t)
	created := createTestLicense(t, store, 1, nil)
	secondCode, err := store.IssueCode(created.License.ID)
	if err != nil {
		t.Fatal(err)
	}
	codes := []string{created.Code.AuthorizationCode, secondCode.AuthorizationCode}
	errorsByAttempt := make(chan error, len(codes))
	var group sync.WaitGroup
	for index, code := range codes {
		group.Add(1)
		go func(index int, code string) {
			defer group.Done()
			_, activateErr := store.Activate(ActivateRequest{
				Code:           code,
				InstallationID: fmt.Sprintf("install-%d", index),
				PublicKey:      fmt.Sprintf("key-%d", index),
			})
			errorsByAttempt <- activateErr
		}(index, code)
	}
	group.Wait()
	close(errorsByAttempt)
	successes := 0
	limitErrors := 0
	for err := range errorsByAttempt {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrDeviceLimitReached):
			limitErrors++
		default:
			t.Fatalf("unexpected activation error: %v", err)
		}
	}
	if successes != 1 || limitErrors != 1 {
		t.Fatalf("activation outcomes: successes=%d limit_errors=%d", successes, limitErrors)
	}
}

func TestRevokeAndExpiration(t *testing.T) {
	store, _, now := newTestStore(t)
	expires := now.Add(time.Hour)
	created := createTestLicense(t, store, 1, &expires)
	activation := activateTestDevice(t, store, created.Code.AuthorizationCode, "install-a")

	revoked, err := store.RevokeLicense(created.License.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Status != LicenseRevoked || len(revoked.Devices) != 1 || revoked.Devices[0].Status != DeviceRevoked {
		t.Fatalf("license was not fully revoked: %#v", revoked)
	}
	if _, err := store.Heartbeat(activation.DeviceID, "Bearer "+activation.DeviceToken); !errors.Is(err, ErrLicenseRevoked) {
		t.Fatalf("revoked heartbeat error = %v, want ErrLicenseRevoked", err)
	}
	if _, err := store.IssueCode(created.License.ID); !errors.Is(err, ErrLicenseRevoked) {
		t.Fatalf("revoked issue-code error = %v", err)
	}

	expiringStore, _, expiringNow := newTestStore(t)
	expires = expiringNow.Add(time.Hour)
	expiring := createTestLicense(t, expiringStore, 1, &expires)
	*expiringNow = expiringNow.Add(2 * time.Hour)
	if _, err := expiringStore.Activate(ActivateRequest{
		Code:           expiring.Code.AuthorizationCode,
		InstallationID: "install-expired",
		PublicKey:      "key-expired",
	}); !errors.Is(err, ErrLicenseExpired) {
		t.Fatalf("expired activation error = %v, want ErrLicenseExpired", err)
	}
	listed, err := expiringStore.GetLicense(expiring.License.ID)
	if err != nil {
		t.Fatal(err)
	}
	if listed.Status != LicenseExpired {
		t.Fatalf("expired license status = %q", listed.Status)
	}
}

func TestUpdateLicenseLimitsAreDeliveredByHeartbeat(t *testing.T) {
	store, _, now := newTestStore(t)
	created := createTestLicense(t, store, 2, nil)
	activation := activateTestDevice(t, store, created.Code.AuthorizationCode, "install-a")
	expires := now.Add(48 * time.Hour)
	updated, err := store.UpdateLicense(created.License.ID, UpdateLicenseRequest{
		ExpiresAt:   &expires,
		DeviceLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DeviceLimit != 1 {
		t.Fatalf("unexpected updated license: %#v", updated)
	}
	heartbeat, err := store.Heartbeat(activation.DeviceID, "Bearer "+activation.DeviceToken)
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.License.ExpiresAt == nil || !heartbeat.License.ExpiresAt.Equal(expires) {
		t.Fatalf("heartbeat did not deliver updated policy: %#v", heartbeat)
	}
	if _, err := store.UpdateLicense(created.License.ID, UpdateLicenseRequest{DeviceLimit: 1}); err != nil {
		t.Fatalf("limits-only update error = %v", err)
	}
}

func TestStorePersistsAcrossRestart(t *testing.T) {
	store, path, now := newTestStore(t)
	created := createTestLicense(t, store, 2, nil)
	activation := activateTestDevice(t, store, created.Code.AuthorizationCode, "install-a")

	reloaded, err := NewStore(path, testPepper)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.now = func() time.Time { return *now }
	licenses := reloaded.ListLicenses()
	if len(licenses) != 1 || licenses[0].ID != created.License.ID || licenses[0].ActiveDevices != 1 {
		t.Fatalf("reloaded licenses = %#v", licenses)
	}
	if _, err := reloaded.Heartbeat(activation.DeviceID, "Bearer "+activation.DeviceToken); err != nil {
		t.Fatalf("reloaded token digest did not validate: %v", err)
	}
	_, err = reloaded.Activate(ActivateRequest{
		Code:           created.Code.AuthorizationCode,
		InstallationID: "install-a",
		PublicKey:      "ed25519:install-a",
	})
	if !errors.Is(err, ErrAuthorizationCodeUsed) {
		t.Fatalf("consumed code was accepted again after restart: %v", err)
	}
	if _, err := reloaded.Activate(ActivateRequest{
		Code:           created.Code.AuthorizationCode,
		InstallationID: "different-install",
		PublicKey:      "different-key",
	}); !errors.Is(err, ErrAuthorizationCodeUsed) {
		t.Fatalf("consumed code after restart error = %v", err)
	}
}

func TestNewStoreRejectsMissingPepper(t *testing.T) {
	_, err := NewStore(filepath.Join(t.TempDir(), "licenses.json"), nil)
	if !errors.Is(err, ErrPepperRequired) {
		t.Fatalf("missing pepper error = %v", err)
	}
}
