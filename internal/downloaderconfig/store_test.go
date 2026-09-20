package downloaderconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStoreEncryptsSecretsAndReloadsVerifiedConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		secret string
		check  func(t *testing.T, public Public)
	}{
		{
			name: "password",
			config: Config{
				BaseURL: "http://192.168.1.20:8080", AuthMode: AuthPassword,
				Username: "admin", Password: "  exact password bytes  ", SavePath: "/downloads/movies",
			},
			secret: "  exact password bytes  ",
			check: func(t *testing.T, public Public) {
				t.Helper()
				if !public.PasswordPresent || public.APIKeyPresent || public.Username != "admin" {
					t.Fatalf("password public state = %#v", public)
				}
			},
		},
		{
			name: "api key",
			config: Config{
				BaseURL: "https://10.0.0.8:8443/qbit", AuthMode: AuthAPIKey,
				APIKey: "qbit-secret-api-key", SavePath: "/srv/downloads",
			},
			secret: "qbit-secret-api-key",
			check: func(t *testing.T, public Public) {
				t.Helper()
				if public.PasswordPresent || !public.APIKeyPresent || public.Username != "" {
					t.Fatalf("API key public state = %#v", public)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "qbittorrent.json")
			store, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			verifiedAt := time.Date(2026, 9, 19, 14, 30, 0, 0, time.UTC)
			if err := store.SaveVerified(test.config, "5.2.2", verifiedAt); err != nil {
				t.Fatal(err)
			}

			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), test.secret) {
				t.Fatal("configuration file contains a plaintext downloader secret")
			}
			if !strings.Contains(string(raw), encryptedPrefix) {
				t.Fatalf("configuration file contains no encrypted secret: %s", raw)
			}
			if info, err := os.Stat(path); err != nil {
				t.Fatal(err)
			} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				t.Fatalf("configuration mode = %o, want 600", info.Mode().Perm())
			}

			public := store.Public()
			test.check(t, public)
			if !public.Available || public.Version != "5.2.2" || public.LastVerifiedAt == nil || !public.LastVerifiedAt.Equal(verifiedAt) {
				t.Fatalf("verified public state = %#v", public)
			}
			publicJSON, err := json.Marshal(public)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(publicJSON), test.secret) {
				t.Fatalf("public JSON exposed secret: %s", publicJSON)
			}

			reloaded, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := reloaded.Config(); got != test.config {
				t.Fatalf("reloaded config = %#v, want %#v", got, test.config)
			}
			test.check(t, reloaded.Public())
		})
	}
}

func TestResolveReusesSecretsOnlyForSameAddressAndMode(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "qbittorrent.json"))
	if err != nil {
		t.Fatal(err)
	}
	current := Config{
		BaseURL: "http://192.168.1.20:8080", AuthMode: AuthPassword,
		Username: "admin", Password: "saved-password", SavePath: "/saved/path",
	}
	if err := store.SaveVerified(current, "5.2.1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	reused, err := store.Resolve(Update{BaseURL: current.BaseURL, AuthMode: AuthPassword})
	if err != nil {
		t.Fatal(err)
	}
	if reused != current {
		t.Fatalf("same-address resolved config = %#v, want %#v", reused, current)
	}
	if _, err := store.Resolve(Update{BaseURL: "http://192.168.1.21:8080", AuthMode: AuthPassword, Username: "admin"}); err == nil {
		t.Fatal("changed address reused the saved password")
	}

	switched, err := store.Resolve(Update{BaseURL: current.BaseURL, AuthMode: AuthAPIKey, APIKey: "new-api-key"})
	if err != nil {
		t.Fatal(err)
	}
	if switched.APIKey != "new-api-key" || switched.Username != "" || switched.Password != "" {
		t.Fatalf("password-to-API-key switch retained old credentials: %#v", switched)
	}
	if _, err := store.Resolve(Update{BaseURL: current.BaseURL, Password: "new-password", APIKey: "new-key"}); err == nil {
		t.Fatal("Resolve accepted password and API key together")
	}
}

func TestDeleteClearsConfigurationAndPersistsRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qbittorrent.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveVerified(Config{BaseURL: "http://127.0.0.1:8080", AuthMode: AuthAPIKey, APIKey: "secret"}, "5.2.0", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if got := store.Public(); got.Available || got.BaseURL != "" || got.APIKeyPresent || got.PasswordPresent {
		t.Fatalf("public state after delete = %#v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("configuration still exists after delete: %v", err)
	}
	reloaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Public().Available {
		t.Fatal("deleted configuration became available after reload")
	}
}

func TestOversizedConfigurationIsRejectedBeforePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qbittorrent.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(Update{BaseURL: "http://" + strings.Repeat("a", maxURLBytes), AuthMode: AuthAPIKey, APIKey: "key"}); err == nil {
		t.Fatal("oversized base URL was accepted")
	}
	config := Config{BaseURL: "http://127.0.0.1:8080", AuthMode: AuthAPIKey, APIKey: strings.Repeat("k", maxSecretBytes+1)}
	if err := store.SaveVerified(config, "5.2.0", time.Now().UTC()); err == nil {
		t.Fatal("oversized API key was persisted")
	}
	config.APIKey = "key"
	if err := store.SaveVerified(config, strings.Repeat("v", maxVersionBytes+1), time.Now().UTC()); err == nil {
		t.Fatal("oversized version was persisted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rejected configuration created a state file: %v", err)
	}
}
