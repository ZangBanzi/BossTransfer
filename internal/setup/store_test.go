package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStoreLoadsVersion110TokenConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client-setup.json")
	store, err := NewStore(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(Config{
		CloudDrive2Address: "host.docker.internal:19798",
		AuthMode:           "token",
		Token:              "legacy-encrypted-token",
		MountPoint:         "/CloudNAS/CloudDrive",
		SourceRelative:     "115open",
		TargetRelative:     "BossTransfer",
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "auth_mode")
	delete(legacy, "username")
	delete(legacy, "encrypted_password")
	delete(legacy, "cloud_target_dir")
	data, err = json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewStore(path, Config{})
	if err != nil {
		t.Fatalf("load 1.1.0 setup: %v", err)
	}
	got := reloaded.Get()
	if got.AuthMode != "token" || got.Token != "legacy-encrypted-token" || got.MountPoint != "/CloudNAS/CloudDrive" || got.SourceRelative != "115open" || got.TargetRelative != "BossTransfer" {
		t.Fatalf("migrated 1.1.0 setup = %#v", got)
	}
}

func TestStoreEncryptsAndReloadsToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.json")
	store, err := NewStore(path, Config{CloudDrive2Address: "host.docker.internal:19798"})
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		CloudDrive2Address: "192.168.1.5:19798",
		Token:              "secret-api-token",
		MountPoint:         "/CloudNAS/CloudDrive",
		CloudTargetDir:     "/115/downloads/",
		SourceRelative:     "/115open/",
		TargetRelative:     "/BossTransfer/",
	}
	if err := store.Update(want); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), want.Token) {
		t.Fatal("setup file contains plaintext token")
	}
	reloaded, err := NewStore(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	got := reloaded.Get()
	if got.Token != want.Token || got.CloudTargetDir != "/115/downloads" || got.SourceRelative != "115open" || got.TargetRelative != "BossTransfer" {
		t.Fatalf("reloaded config = %#v", got)
	}
	if public := reloaded.Public(); !public.TokenPresent || strings.Contains(public.CloudDrive2Address, want.Token) {
		t.Fatalf("public config = %#v", public)
	}
}

func TestStoreConcurrentUpdatesRemainDecryptableAndConsistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.json")
	store, err := NewStore(path, Config{CloudDrive2Address: "host.docker.internal:19798"})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 32
	start := make(chan struct{})
	errorsCh := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errorsCh <- store.Update(Config{
				CloudDrive2Address: "192.168.1.5:19798",
				Token:              fmt.Sprintf("secret-token-%02d", index),
				MountPoint:         fmt.Sprintf("/mount/%02d", index),
				SourceRelative:     fmt.Sprintf("source/%02d", index),
				TargetRelative:     fmt.Sprintf("target/%02d", index),
			})
		}()
	}
	close(start)
	wait.Wait()
	close(errorsCh)
	for updateErr := range errorsCh {
		if updateErr != nil {
			t.Fatalf("concurrent Update: %v", updateErr)
		}
	}

	live := store.Get()
	reloaded, err := NewStore(path, Config{})
	if err != nil {
		t.Fatalf("reload after concurrent updates: %v", err)
	}
	if got := reloaded.Get(); got != live {
		t.Fatalf("disk and memory diverged after concurrent updates:\n disk = %#v\n memory = %#v", got, live)
	}
	if !strings.HasPrefix(live.Token, "secret-token-") {
		t.Fatalf("unexpected final token: %q", live.Token)
	}
}

func TestStoreAllowsLocalPathsWithoutOptionalCloudDriveCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.json")
	store, err := NewStore(path, Config{CloudDrive2Address: "host.docker.internal:19798", AuthMode: "password"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(Config{
		CloudDrive2Address: "host.docker.internal:19798",
		AuthMode:           "password",
		TargetRelative:     "downloads/movies",
	}); err != nil {
		t.Fatalf("save local paths without CloudDrive2 credentials: %v", err)
	}
	reloaded, err := NewStore(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get(); got.TargetRelative != "downloads/movies" || got.Password != "" || got.Token != "" {
		t.Fatalf("reloaded system-only config = %#v", got)
	}
}
