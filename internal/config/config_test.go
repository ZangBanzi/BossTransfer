package config

import "testing"

func TestLoadManagerRejectsReservedPort(t *testing.T) {
	t.Setenv("BOSSTRANSFER_MANAGER_ADDR", "127.0.0.1:8098")

	_, err := LoadManager()
	if err == nil {
		t.Fatal("expected reserved port error")
	}
	if got, ok := err.(PortReservedError); !ok || got.Port != "8098" {
		t.Fatalf("expected PortReservedError for 8098, got %#v", err)
	}
}

func TestLoadClientUsesDefaults(t *testing.T) {
	t.Setenv("BOSSTRANSFER_CLIENT_ADDR", "")
	t.Setenv("BOSSTRANSFER_CD2_ADDR", "")
	t.Setenv("BOSSTRANSFER_SOURCE_DIR", "")
	t.Setenv("BOSSTRANSFER_TARGET_DIR", "")
	t.Setenv("BOSSTRANSFER_SOURCE_HOST_PATH", "")
	t.Setenv("BOSSTRANSFER_TARGET_HOST_PATH", "")
	t.Setenv("BOSSTRANSFER_CLIENT_DATA_DIR", "")
	t.Setenv("BOSSTRANSFER_SETUP_USER", "")
	t.Setenv("BOSSTRANSFER_SETUP_PASSWORD", "")

	cfg, err := LoadClient()
	if err != nil {
		t.Fatalf("LoadClient returned error: %v", err)
	}
	if cfg.Server.Addr != DefaultClientAddr {
		t.Fatalf("client addr = %q, want %q", cfg.Server.Addr, DefaultClientAddr)
	}
	if cfg.CloudDrive2Addr != DefaultCloudDrive2Addr {
		t.Fatalf("cd2 addr = %q, want %q", cfg.CloudDrive2Addr, DefaultCloudDrive2Addr)
	}
	if cfg.SourceDir != DefaultClientSourceDir {
		t.Fatalf("source dir = %q, want %q", cfg.SourceDir, DefaultClientSourceDir)
	}
	if cfg.TargetDir != DefaultClientTargetDir {
		t.Fatalf("target dir = %q, want %q", cfg.TargetDir, DefaultClientTargetDir)
	}
	if cfg.SourceHostPath != DefaultClientSourceDir || cfg.TargetHostPath != DefaultClientTargetDir {
		t.Fatalf("host display paths = %q/%q", cfg.SourceHostPath, cfg.TargetHostPath)
	}
	if cfg.DataDir != DefaultClientDataDir {
		t.Fatalf("data dir = %q, want %q", cfg.DataDir, DefaultClientDataDir)
	}
	if cfg.SetupUser != DefaultSetupUser || cfg.SetupPassword != DefaultSetupPassword {
		t.Fatalf("unexpected setup credentials defaults: user=%q password=%q", cfg.SetupUser, cfg.SetupPassword)
	}
}

func TestLoadClientAcceptsBootstrapPasswordOnPublicListener(t *testing.T) {
	t.Setenv("BOSSTRANSFER_CLIENT_ADDR", "0.0.0.0:18085")
	t.Setenv("BOSSTRANSFER_SETUP_PASSWORD", "p $ word ")
	cfg, err := LoadClient()
	if err != nil {
		t.Fatalf("expected public listener with bootstrap password to load: %v", err)
	}
	if cfg.SetupPassword != "p $ word " {
		t.Fatalf("bootstrap password was changed: %q", cfg.SetupPassword)
	}
}

func TestBoolEnvFallback(t *testing.T) {
	t.Setenv("BOSSTRANSFER_BOOL_TEST", "not-bool")
	if !BoolEnv("BOSSTRANSFER_BOOL_TEST", true) {
		t.Fatal("invalid bool env should return fallback")
	}

	t.Setenv("BOSSTRANSFER_BOOL_TEST", "false")
	if BoolEnv("BOSSTRANSFER_BOOL_TEST", true) {
		t.Fatal("false bool env should parse to false")
	}
}

func TestIntEnvFallback(t *testing.T) {
	t.Setenv("BOSSTRANSFER_INT_TEST", "nope")
	if IntEnv("BOSSTRANSFER_INT_TEST", 12) != 12 {
		t.Fatal("invalid int env should return fallback")
	}
	t.Setenv("BOSSTRANSFER_INT_TEST", "25")
	if IntEnv("BOSSTRANSFER_INT_TEST", 12) != 25 {
		t.Fatal("valid int env should parse")
	}
}
