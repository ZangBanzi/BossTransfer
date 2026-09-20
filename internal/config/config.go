package config

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultManagerAddr      = "127.0.0.1:8085"
	DefaultClientAddr       = "127.0.0.1:18085"
	DefaultCloudDrive2Addr  = "127.0.0.1:19798"
	DefaultClientSourceDir  = "/data/source"
	DefaultClientTargetDir  = "/data/target"
	DefaultClientDataDir    = "/var/lib/bosstransfer-client"
	DefaultManagerDataDir   = "/var/lib/bosstransfer"
	DefaultClientMaxResults = 100
	DefaultSetupUser        = "admin"
	DefaultSetupPassword    = "password"
	DefaultManagerUser      = "admin"
	DefaultManagerPassword  = "password"
	DefaultShutdownTimeout  = 10 * time.Second
	DefaultReadHeaderTimout = 5 * time.Second
)

var reservedPorts = map[string]struct{}{
	"8096":  {},
	"8098":  {},
	"8092":  {},
	"3333":  {},
	"3334":  {},
	"19798": {},
}

type Server struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ShutdownTimeout   time.Duration
}

type Manager struct {
	Server          Server
	DataDir         string
	AdminUser       string
	AdminPassword   string
	LicensePepper   string
	Open115ClientID string
}

type Client struct {
	Server          Server
	ManagerURL      string
	CloudDrive2Addr string
	SourceDir       string
	TargetDir       string
	SourceHostPath  string
	TargetHostPath  string
	DataDir         string
	MaxResults      int
	SetupUser       string
	SetupPassword   string
}

func LoadManager() (Manager, error) {
	addr := getenv("BOSSTRANSFER_MANAGER_ADDR", DefaultManagerAddr)
	if err := rejectReservedPort(addr); err != nil {
		return Manager{}, err
	}
	return Manager{
		Server:          newServer(addr),
		DataDir:         getenv("BOSSTRANSFER_DATA_DIR", DefaultManagerDataDir),
		AdminUser:       getenv("BOSSTRANSFER_MANAGER_USER", DefaultManagerUser),
		AdminPassword:   getenvRaw("BOSSTRANSFER_MANAGER_PASSWORD", DefaultManagerPassword),
		LicensePepper:   os.Getenv("BOSSTRANSFER_LICENSE_PEPPER"),
		Open115ClientID: strings.TrimSpace(os.Getenv("BOSSTRANSFER_115_CLIENT_ID")),
	}, nil
}

func LoadClient() (Client, error) {
	addr := getenv("BOSSTRANSFER_CLIENT_ADDR", DefaultClientAddr)
	if err := rejectReservedPort(addr); err != nil {
		return Client{}, err
	}
	// These values bootstrap the persistent Web account on first start only.
	// They deliberately do not prevent the service from starting: the customer
	// can change the account in the Web UI after logging in.
	setupPassword := getenvRaw("BOSSTRANSFER_SETUP_PASSWORD", DefaultSetupPassword)
	sourceDir := getenv("BOSSTRANSFER_SOURCE_DIR", DefaultClientSourceDir)
	targetDir := getenv("BOSSTRANSFER_TARGET_DIR", DefaultClientTargetDir)
	return Client{
		Server:          newServer(addr),
		ManagerURL:      strings.TrimSpace(os.Getenv("BOSSTRANSFER_MANAGER_URL")),
		CloudDrive2Addr: getenv("BOSSTRANSFER_CD2_ADDR", DefaultCloudDrive2Addr),
		SourceDir:       sourceDir,
		TargetDir:       targetDir,
		SourceHostPath:  getenv("BOSSTRANSFER_SOURCE_HOST_PATH", sourceDir),
		TargetHostPath:  getenv("BOSSTRANSFER_TARGET_HOST_PATH", targetDir),
		DataDir:         getenv("BOSSTRANSFER_CLIENT_DATA_DIR", DefaultClientDataDir),
		MaxResults:      IntEnv("BOSSTRANSFER_MAX_RESULTS", DefaultClientMaxResults),
		SetupUser:       getenv("BOSSTRANSFER_SETUP_USER", DefaultSetupUser),
		SetupPassword:   setupPassword,
	}, nil
}

func getenvRaw(key, fallback string) string {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback
	}
	return value
}

func isLoopbackListener(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newServer(addr string) Server {
	return Server{
		Addr:              addr,
		ReadHeaderTimeout: DefaultReadHeaderTimout,
		ShutdownTimeout:   DefaultShutdownTimeout,
	}
}

func getenv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func rejectReservedPort(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if _, reserved := reservedPorts[port]; reserved {
		return PortReservedError{Port: port}
	}
	return nil
}

type PortReservedError struct {
	Port string
}

func (e PortReservedError) Error() string {
	return "port " + e.Port + " is reserved for an existing service"
}

func BoolEnv(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func IntEnv(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
