package licenseclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorePersistsEncryptedDeviceCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "license.json")
	store, err := Open(path, "https://manager.example")
	if err != nil {
		t.Fatal(err)
	}
	lease := time.Now().Add(time.Hour).UTC()
	if err := store.SaveActivation("https://manager.example", "device-1", "very-secret-device-token", License{ID: "license-1", Customer: "Alice", Status: "active"}, lease, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "very-secret-device-token") {
		t.Fatal("device token was stored in plaintext")
	}
	reloaded, err := Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	deviceID, token, err := reloaded.DeviceCredential()
	if err != nil || deviceID != "device-1" || token != "very-secret-device-token" {
		t.Fatalf("credential = %q %q, %v", deviceID, token, err)
	}
	if !reloaded.Status(time.Now()).Activated {
		t.Fatal("reloaded activation is not active")
	}
}

func TestStatusRequiresUnexpiredLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "license.json")
	store, err := Open(path, "https://manager.example")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.SaveActivation("https://manager.example", "device-1", "token", License{ID: "license-1", Status: "active"}, now.Add(-time.Second), now); err != nil {
		t.Fatal(err)
	}
	if store.Status(now).Activated {
		t.Fatal("expired lease is active")
	}
}
